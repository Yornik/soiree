package httpd

// Middleware for the accounts surface, kept in its own file so the REST API
// landing alongside it can wrap its routes without the two changing the same
// lines.
//
// The rule these express: authorisation is decided on the server, per request,
// from a session the client cannot forge. The browser is told a role so it can
// hide buttons, and that is a courtesy — nothing here trusts it.

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Yornik/soiree/internal/auth"
	"github.com/Yornik/soiree/internal/store"
)

// ctxKey is unexported so nothing outside this package can put a user into a
// request context and have it believed.
type ctxKey int

const userCtxKey ctxKey = iota

// UserFrom returns the account a request is authenticated as.
//
// The second return is the whole point: a handler that forgets to check it
// gets a zero User whose role is "", which outranks nothing, rather than
// something that silently reads as a viewer.
func UserFrom(ctx context.Context) (store.User, bool) {
	u, ok := ctx.Value(userCtxKey).(store.User)
	return u, ok
}

// Authenticate resolves the session cookie onto an account and puts it in the
// request context.
//
// It never rejects. Deciding who is calling and deciding whether they may is
// two jobs, and keeping them apart is what lets a public endpoint still know
// that an admin is the one calling it.
func (a *Auth) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := sessionToken(r)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}

		sess, user, err := a.store.SessionByToken(r.Context(), auth.HashToken(token), sessionMaxLifetime)
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				a.log.Error("session lookup failed", "err", err)
			}
			// A cookie that no longer resolves is cleared, so a browser
			// holding a revoked session stops sending it rather than
			// re-presenting it on every request for the next month.
			clearSessionCookie(w)
			next.ServeHTTP(w, r)
			return
		}

		// Slide the idle window forward — in the database *and* in the browser.
		// Doing only the first would leave the cookie expiring at the moment
		// it was issued, so somebody using this every day would still be
		// logged out on the seventh, and no session could ever reach the
		// absolute cap. Both, or neither, or the two disagree.
		//
		// At most once an hour: the window is measured in days, and a write
		// per request to record it would cost far more than it tells anybody.
		if a.now().Sub(sess.LastSeenAt) >= sessionTouchInterval {
			setSessionCookie(w, token, a.now(), sessionIdleTimeout)
			expires := a.now().Add(sessionIdleTimeout)
			// Detached from the request: the caller has their answer either
			// way, and this failing is not a reason to fail their request.
			a.background(func(ctx context.Context) {
				if err := a.store.TouchSession(ctx, sess.ID, expires); err != nil {
					a.log.Error("could not extend session", "err", err)
				}
			})
		}

		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userCtxKey, user)))
	})
}

// RequireAuth refuses anyone without a live session.
func (a *Auth) RequireAuth(next http.Handler) http.Handler {
	return a.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := UserFrom(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthenticated")
			return
		}
		next.ServeHTTP(w, r)
	}))
}

// RequireRole refuses anyone below min.
//
// Exported for the REST API: `mux.Handle("GET /api/v1/plan",
// a.RequireRole(store.RoleViewer)(h))`.
func (a *Auth) RequireRole(min store.Role) func(http.Handler) http.Handler {
	want := rank(min)
	return func(next http.Handler) http.Handler {
		return a.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, ok := UserFrom(r.Context())
			if !ok {
				writeError(w, http.StatusUnauthorized, "unauthenticated")
				return
			}
			if rank(u.Role) < want {
				writeError(w, http.StatusForbidden, "forbidden")
				return
			}
			next.ServeHTTP(w, r)
		}))
	}
}

// RequireWrite lets anybody signed in read, and only an editor or an admin
// write.
//
// This is the middleware the REST API's CRUD subtree wants, because the rule
// it encodes — a viewer is read-only — is a property of the method rather than
// of the route, and attaching it per route is how one new route ends up
// unguarded six months from now:
//
//	mux.Handle("/api/v1/budget-items/", a.RequireWrite(crud))
func (a *Auth) RequireWrite(next http.Handler) http.Handler {
	return a.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := UserFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthenticated")
			return
		}
		if isMutating(r.Method) && rank(u.Role) < rank(store.RoleEditor) {
			writeError(w, http.StatusForbidden, "read_only")
			return
		}
		next.ServeHTTP(w, r)
	}))
}

// limitIP refuses a caller that has used up its per-address allowance.
func (a *Auth) limitIP(l *limiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.allow(clientIP(r, a.trustProxy)) {
			tooManyRequests(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isMutating reports whether a method is expected to change something.
// GET, HEAD and OPTIONS are the safe methods; everything else is a write,
// including any method this application does not implement — an unknown method
// should be refused to a viewer, not waved through.
func isMutating(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	default:
		return true
	}
}

// rank orders the roles. An unrecognised role — including the zero value of a
// User that was never authenticated — ranks below every real one, so a
// comparison against it fails closed.
func rank(r store.Role) int {
	switch r {
	case store.RoleAdmin:
		return 3
	case store.RoleEditor:
		return 2
	case store.RoleViewer:
		return 1
	default:
		return 0
	}
}

// sessionToken reads the session cookie.
func sessionToken(r *http.Request) (string, bool) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil || c.Value == "" {
		return "", false
	}
	return c.Value, true
}

// setSessionCookie installs a session cookie.
//
// HttpOnly so script cannot read it, which is what stops an XSS anywhere on
// the origin from turning into a stolen session. Secure so it never crosses
// plain HTTP — browsers treat localhost as a secure context, so development
// over http://localhost still works. SameSite=Lax so a form on another site
// cannot post to this one with the user's session attached, while an ordinary
// link from a mail still arrives logged in. Path=/ because the API and the
// page it serves share an origin.
// lifetime is passed rather than an absolute expiry so that MaxAge and Expires
// cannot disagree: deriving one from the other through time.Now would reach
// past the injected clock and drift the moment a test moves it.
func setSessionCookie(w http.ResponseWriter, token string, now time.Time, lifetime time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  now.Add(lifetime),
		MaxAge:   int(lifetime.Seconds()),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

// clearSessionCookie expires the cookie. Every attribute other than the value
// has to match the one that was set, or some browsers keep both.
func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}
