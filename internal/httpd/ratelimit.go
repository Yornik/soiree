package httpd

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// limiter is a token bucket per key, in memory.
//
// Scope, honestly: this is per process. The Deployment runs more than one
// replica, so the effective limit across the cluster is this one multiplied by
// the replica count, and a client whose requests land on different replicas
// gets a fresh allowance on each. A shared counter in Postgres or Valkey would
// fix that and would put a network round trip in front of every login attempt,
// including the ones that are about to fail. For an application a dozen people
// use, slowing a password-guessing run from millions per hour to a few hundred
// is the part that matters, and this does that.
//
// What it is not: a defence against a distributed attack. Per-IP limits never
// are. The real defence against guessing is Argon2id at 19 MiB, which caps the
// rate at something like single-digit attempts per second per core no matter
// who is asking.
type limiter struct {
	mu     sync.Mutex
	burst  float64
	refill float64 // tokens per second
	now    func() time.Time
	seen   map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

// maxKeys bounds the map. Without it, a flood of requests from spoofed or
// rotating addresses is a memory leak with a rate limiter attached.
const maxKeys = 4096

// newLimiter builds a bucket that allows burst requests immediately and then
// one per interval/burst.
func newLimiter(burst int, per time.Duration) *limiter {
	return &limiter{
		burst:  float64(burst),
		refill: float64(burst) / per.Seconds(),
		now:    time.Now,
		seen:   make(map[string]*bucket),
	}
}

// allow takes a token for key, reporting whether there was one.
func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	b, ok := l.seen[key]
	if !ok {
		if len(l.seen) >= maxKeys {
			l.sweep()
		}
		// A new key starts full, and last is zero so the refill below clamps
		// to burst rather than overflowing.
		b = &bucket{tokens: l.burst}
		l.seen[key] = b
	}

	b.tokens = min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.refill)
	b.last = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// sweep drops buckets that have refilled completely, because a full bucket is
// indistinguishable from one that was never created. When a flood of fresh
// keys means none of them has, it drops the least recently used one instead,
// so that maxKeys is a bound rather than a hope. Called with the lock held.
//
// Evicting, not refusing: a key that is turned away when the map is full hands
// anybody who can fill it a way to lock out every caller not already in it,
// which is a worse failure than the one the bound is for. The evicted caller
// gets a full bucket back, and buying one that way costs an attacker a
// mapful of requests.
func (l *limiter) sweep() {
	now := l.now()
	var (
		oldestKey string
		oldest    time.Time
		found     bool
	)
	for k, b := range l.seen {
		if b.tokens+now.Sub(b.last).Seconds()*l.refill >= l.burst {
			delete(l.seen, k)
			continue
		}
		if !found || b.last.Before(oldest) {
			oldestKey, oldest, found = k, b.last, true
		}
	}
	if found && len(l.seen) >= maxKeys {
		delete(l.seen, oldestKey)
	}
}

// clientIP is the address the rate limiters key on.
//
// r.RemoteAddr by default, and X-Forwarded-For only when the deployment says
// there is a proxy in front. Believing the header unconditionally would let
// anyone set their own key and have their own bucket per request, which is a
// rate limiter that limits nothing.
//
// When it is believed, the *rightmost* entry is taken, not the leftmost. A
// client can put anything it likes in the header; the trusted proxy appends
// the address it actually saw, so the last entry is the only one it wrote and
// the only one worth reading. The familiar advice to take the first entry is
// for working out who the original client was through a chain of proxies you
// control — a different question, with a different answer, and taking the
// first here would hand the key straight back to the client.
//
// Every line of the header counts, not just the first one. A proxy is free to
// append a line of its own instead of extending the one already there, and
// reading a single line then reads the line the client wrote.
func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if forwarded := strings.Join(r.Header.Values("X-Forwarded-For"), ","); forwarded != "" {
			parts := strings.Split(forwarded, ",")
			if key, ok := ipKey(strings.TrimSpace(parts[len(parts)-1])); ok {
				return key
			}
			// A last hop that is not an address is a proxy that did not write
			// it, so fall through: the peer is the only thing left that
			// nobody could have chosen.
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if key, ok := ipKey(host); ok {
		return key
	}
	return host
}

// ipKey is the bucket an address belongs to.
//
// One IPv4 address is one caller, near enough. One IPv6 address is not: the
// smallest allocation a household or a phone gets is a /64, so a client that
// walks the low 64 bits has a fresh bucket per request and no limit at all.
// The key is therefore the /64, and everybody inside one shares a budget, the
// same deal a household behind a single NAT address has always had.
func ipKey(s string) (string, bool) {
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return "", false
	}
	// The zone names an interface on whichever machine wrote it down, and
	// says nothing about who is asking. Unmap so that an address arriving in
	// the ::ffff:0:0/96 form keys the same as the IPv4 address it is.
	addr = addr.WithZone("").Unmap()
	if addr.Is4() {
		return addr.String(), true
	}
	return netip.PrefixFrom(addr, 64).Masked().String(), true
}
