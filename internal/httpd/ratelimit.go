package httpd

import (
	"net"
	"net/http"
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
// indistinguishable from one that was never created. Called with the lock
// held.
func (l *limiter) sweep() {
	now := l.now()
	for k, b := range l.seen {
		if b.tokens+now.Sub(b.last).Seconds()*l.refill >= l.burst {
			delete(l.seen, k)
		}
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
func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
