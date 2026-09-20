package httpd

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Yornik/soiree/internal/store"
)

// clearsTheSession reports whether a response tells the browser to drop its
// session cookie.
func clearsTheSession(header http.Header) bool {
	res := http.Response{Header: header}
	for _, c := range res.Cookies() {
		if c.Name == sessionCookieName && c.MaxAge < 0 {
			return true
		}
	}
	return false
}

// A database that does not answer has not said the session is gone, and the
// page takes a 401 at its word: it signs the person out and asks nothing twice.
// The cookie is HttpOnly, so one cleared by mistake is not something the page
// can put back either. A failover of a few seconds used to cost every open tab
// a sign-in, queued through Argon2 four at a time, for sessions that were all
// still valid.
//
// One route per guard, because each of them writes its own refusal. The pool is
// closed rather than the store faked: the error that matters is whatever pgx
// really returns when there is nothing to talk to.
func TestADatabaseOutageIsNotASignOut(t *testing.T) {
	h, pool := newAPIServerAs(t, store.RoleAdmin)

	routes := []struct{ guard, method, path, body string }{
		{"RequireWrite", http.MethodGet, "/api/v1/plan", ""},
		{"RequireWrite", http.MethodPost, "/api/v1/notes", `{"text":"Lock the caterer"}`},
		// What the page probes when its event stream is refused without a
		// reason. A 401 here is the one answer that signs anybody out.
		{"RequireAuth", http.MethodGet, "/api/v1/auth/session", ""},
		{"RequireRole", http.MethodGet, "/api/v1/users", ""},
	}

	for _, c := range routes {
		if res := call(t, h, c.method, c.path, c.body); res.status >= 300 {
			t.Fatalf("%s %s with the database up -> %d, so this test would prove nothing\n%s",
				c.method, c.path, res.status, res.body)
		}
	}

	pool.Close() // idempotent; pgtest closes it again during cleanup

	for _, c := range routes {
		res := call(t, h, c.method, c.path, c.body)
		if res.status != http.StatusServiceUnavailable {
			t.Errorf("%s: %s %s with the database gone -> %d, want 503\n%s",
				c.guard, c.method, c.path, res.status, res.body)
		}
		if got := res.header.Get("Retry-After"); got == "" {
			t.Errorf("%s: %s %s: no Retry-After, so nothing says a moment later is worth trying",
				c.guard, c.method, c.path)
		}
		if res.status < 400 || str(t, decode(t, res), "error") != "unavailable" {
			t.Errorf("%s: %s %s: the body is %s, want the code unavailable",
				c.guard, c.method, c.path, res.body)
		}
		if clearsTheSession(res.header) {
			t.Errorf("%s: %s %s cleared the session cookie on an outage, which signs out somebody whose session is fine",
				c.guard, c.method, c.path)
		}
	}
}

// The other half of the same branch, so the fix for the outage cannot quietly
// become "never clear the cookie": a token that resolves to nothing is an
// answer, and the browser holding it should stop presenting it.
func TestACookieThatNoLongerResolvesIsCleared(t *testing.T) {
	h, _ := newAPIServerUnauthenticated(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/plan", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "not-a-session-anybody-holds"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("an unknown session -> %d, want 401\n%s", rec.Code, rec.Body)
	}
	if !clearsTheSession(rec.Header()) {
		t.Error("an unknown session kept its cookie, so the browser re-presents it on every request for a month")
	}
}

// Somebody closing a tab mid-request cancels the lookup, and that lands in the
// same branch as an outage. Nobody is there to read an answer, so the two
// things worth pinning are that the log does not call it a database fault, and
// that the request goes no further.
func TestACallerWhoHungUpIsNotLoggedAsAnOutage(t *testing.T) {
	h, a, pool := newAPIServerParts(t, "EUR")
	h = authedAs(t, h, store.New(pool), a, store.RoleEditor)

	var logged bytes.Buffer
	a.log = slog.New(slog.NewJSONHandler(&logged, nil))

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/plan", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if bytes.Contains(logged.Bytes(), []byte(`"level":"ERROR"`)) {
		t.Errorf("a cancelled request was logged as an error:\n%s", logged.String())
	}
	if clearsTheSession(rec.Header()) {
		t.Error("a cancelled request cleared the session cookie")
	}
	if rec.Body.Len() != 0 {
		t.Errorf("a cancelled request was carried on to a handler, which answered:\n%s", rec.Body)
	}
}
