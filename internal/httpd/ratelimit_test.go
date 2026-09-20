package httpd

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// Everything here runs without a database, which is the point: the limiter and
// the key it uses are pure functions, and the branch every request takes
// behind a proxy was never executed by the end-to-end 429 tests.

func TestClientIPIsNotTheCallersToChoose(t *testing.T) {
	const peer = "192.0.2.10:44321"

	for _, tc := range []struct {
		name       string
		remoteAddr string
		trustProxy bool
		forwarded  []string // one entry per header line
		want       string
	}{{
		name:       "the header is ignored with no proxy in front",
		remoteAddr: peer,
		forwarded:  []string{"203.0.113.9"},
		want:       "192.0.2.10",
	}, {
		name:       "the proxy's entry is the rightmost one",
		remoteAddr: peer,
		trustProxy: true,
		forwarded:  []string{"198.51.100.7, 203.0.113.9"},
		want:       "203.0.113.9",
	}, {
		// A proxy that appends a line of its own rather than extending the
		// existing value is the case that made the client's forged entry the
		// key: reading one line reads the client's.
		name:       "a header the proxy wrote on its own line still wins",
		remoteAddr: peer,
		trustProxy: true,
		forwarded:  []string{"198.51.100.7", "203.0.113.9"},
		want:       "203.0.113.9",
	}, {
		name:       "an IPv6 caller keys on its /64",
		remoteAddr: peer,
		trustProxy: true,
		forwarded:  []string{"2001:db8:1:2:aaaa::1"},
		want:       "2001:db8:1:2::/64",
	}, {
		// The other half of the household. One key, or the limit means
		// nothing to anybody with a prefix of their own.
		name:       "another address in the same /64 keys the same",
		remoteAddr: peer,
		trustProxy: true,
		forwarded:  []string{"2001:db8:1:2:bbbb::2"},
		want:       "2001:db8:1:2::/64",
	}, {
		name:       "an IPv4-mapped address keys as the IPv4 address it is",
		remoteAddr: peer,
		trustProxy: true,
		forwarded:  []string{"::ffff:203.0.113.9"},
		want:       "203.0.113.9",
	}, {
		// Whatever the last hop is, it is not an address, so it is not a key
		// either: a client that can invent one has its own bucket per request.
		name:       "a last hop that is not an address falls back to the peer",
		remoteAddr: peer,
		trustProxy: true,
		forwarded:  []string{"198.51.100.7, not-an-address"},
		want:       "192.0.2.10",
	}, {
		name:       "a trailing comma falls back to the peer",
		remoteAddr: peer,
		trustProxy: true,
		forwarded:  []string{"203.0.113.9, "},
		want:       "192.0.2.10",
	}, {
		name:       "no header at all falls back to the peer",
		remoteAddr: peer,
		trustProxy: true,
		want:       "192.0.2.10",
	}, {
		name:       "an IPv6 peer keys on its /64 as well",
		remoteAddr: "[2001:db8:1:2::5]:44321",
		want:       "2001:db8:1:2::/64",
	}, {
		name:       "a peer with a zone keys on the prefix without it",
		remoteAddr: "[fe80::1%eth0]:44321",
		want:       "fe80::/64",
	}, {
		// Not a shape net/http produces, but the fallback has to return
		// something rather than an empty key shared by everybody.
		name:       "a peer with no port is taken as it stands",
		remoteAddr: "192.0.2.10",
		want:       "192.0.2.10",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
			r.RemoteAddr = tc.remoteAddr
			for _, line := range tc.forwarded {
				r.Header.Add("X-Forwarded-For", line)
			}
			if got := clientIP(r, tc.trustProxy); got != tc.want {
				t.Errorf("clientIP = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLimitIPKeysOnTheProxysEntry(t *testing.T) {
	// One token, so the second request from the same key is refused and the
	// question "do these two share a bucket?" has a visible answer.
	send := func(trustProxy bool, forwarded ...string) []int {
		a := &Auth{trustProxy: trustProxy}
		h := a.limitIP(newLimiter(1, time.Minute), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}))
		var codes []int
		for _, line := range forwarded {
			r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
			r.RemoteAddr = "192.0.2.10:44321"
			r.Header.Set("X-Forwarded-For", line)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			codes = append(codes, rec.Code)
		}
		return codes
	}

	trusted := send(true, "203.0.113.9", "203.0.113.10")
	if trusted[0] != http.StatusNoContent || trusted[1] != http.StatusNoContent {
		t.Errorf("two clients behind the proxy got %v, want two 204s: they are sharing a bucket", trusted)
	}

	// The same two requests with no proxy configured are one client as far as
	// this deployment can tell, and one client is one budget.
	untrusted := send(false, "203.0.113.9", "203.0.113.10")
	if untrusted[1] != http.StatusTooManyRequests {
		t.Errorf("second request answered %d, want 429: the header was believed without a proxy", untrusted[1])
	}
}

func TestLimiterSpendsAndRefills(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	l := newLimiter(3, time.Minute) // one token back every 20 seconds
	l.now = func() time.Time { return now }

	for i := range 3 {
		if !l.allow("a") {
			t.Fatalf("request %d of the burst was refused", i+1)
		}
	}
	if l.allow("a") {
		t.Error("a fourth request inside the burst was allowed")
	}

	// A separate key has its own budget, or one noisy caller would refuse
	// everybody.
	if !l.allow("b") {
		t.Error("a second key was refused on its first request")
	}

	now = now.Add(20 * time.Second)
	if !l.allow("a") {
		t.Error("no token had come back after a third of the window")
	}
	if l.allow("a") {
		t.Error("more than one token came back in a third of the window")
	}

	// Idling does not bank tokens. Without the clamp an account untouched for
	// a day would arrive with a day's worth of attempts in hand.
	now = now.Add(24 * time.Hour)
	for i := range 3 {
		if !l.allow("a") {
			t.Fatalf("request %d after a long idle was refused", i+1)
		}
	}
	if l.allow("a") {
		t.Error("a long idle handed out more than one burst")
	}
}

func TestLimiterKeepsItsMapBounded(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	l := newLimiter(1, time.Hour)
	l.now = func() time.Time { return now }

	// Every one of these spends its only token, so none of them is
	// sweepable: this is the flood the bound exists for.
	for i := range maxKeys + 512 {
		l.allow(strconv.Itoa(i))
	}
	if len(l.seen) > maxKeys {
		t.Errorf("map holds %d keys, want at most %d: rotating addresses are a memory leak", len(l.seen), maxKeys)
	}

	// Evicting rather than refusing. A caller whose key was dropped is back to
	// a full bucket, which is the price; refusing instead would let anybody
	// who can fill the map lock out every key that is not in it.
	if !l.allow("somebody-new") {
		t.Error("a new key was refused because the map was full, which is a lockout anybody can trigger")
	}
}

func TestLimiterSweepsRefilledBucketsFirst(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	l := newLimiter(2, time.Minute)
	l.now = func() time.Time { return now }

	for i := range maxKeys {
		l.allow(strconv.Itoa(i))
	}
	// Long enough that every bucket above has refilled, and a full bucket is
	// indistinguishable from one that was never created.
	now = now.Add(time.Hour)
	l.allow("somebody-new")

	if len(l.seen) != 1 {
		t.Errorf("map holds %d keys, want 1: a full map of idle callers should be swept, not evicted one at a time", len(l.seen))
	}
}
