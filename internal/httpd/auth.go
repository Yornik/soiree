package httpd

// The accounts surface: logging in, setting a password, and the admin's view
// of who has an account.
//
// Two shapes recur and are worth naming up front.
//
// Nothing here ever tells a stranger whether an address has an account. The
// reset endpoint answers identically either way and does its work off the
// response path so the timing matches too; a login against an unknown address
// still pays for one Argon2 evaluation against a hash nobody can match. An
// address list is the first half of a credential-stuffing run, and this holds
// the finances of people who never signed up for anything.
//
// No secret is ever logged. Not the token in a link, not a password, not the
// session cookie's value. The logger here takes account ids and error strings.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Yornik/soiree/internal/auth"
	"github.com/Yornik/soiree/internal/config"
	"github.com/Yornik/soiree/internal/store"
)

const (
	// sessionCookieName is prefix-free on purpose. __Host- would pin the
	// cookie to the exact origin and forbid a Domain attribute, which is
	// stricter and would also break a `docker run` over plain HTTP entirely —
	// the prefix requires Secure *and* HTTPS, where a bare Secure cookie is
	// still accepted on localhost.
	sessionCookieName = "soiree_session"

	// sessionIdleTimeout is how long a browser may go quiet before it has to
	// log in again. A week: this is a planner people open when something needs
	// deciding, which is not daily.
	sessionIdleTimeout = 7 * 24 * time.Hour

	// sessionMaxLifetime caps a session however much it is used, so a cookie
	// lifted from a machine somebody no longer owns does not work forever.
	sessionMaxLifetime = 30 * 24 * time.Hour

	// sessionTouchInterval is how often use is written back. Sliding the
	// expiry on every request would be a database write per request to record
	// something only measured in days.
	sessionTouchInterval = time.Hour

	// passwordTokenTTL is the life of a set-password link, from the design.
	passwordTokenTTL = 24 * time.Hour

	// maxPasswordLen bounds what gets hashed. Argon2 does not truncate the way
	// bcrypt does, so without a cap a megabyte of "a" is a request that costs
	// the server real work.
	//
	// Bytes, unlike the floor, and deliberately: this one is a limit on the
	// work asked of Argon2, which is handed bytes. The floor is a promise
	// about how much somebody chose, which is counted the way they count it.
	maxPasswordLen = 1024

	// maxRequestBody bounds a JSON body. Every request here is a handful of
	// short strings.
	maxRequestBody = 16 << 10

	// backgroundTimeout bounds work that outlives its request — mail, mostly.
	backgroundTimeout = 30 * time.Second
)

// Rate limits. Deliberately loose enough that a person who has forgotten which
// password they used will not be locked out, and tight enough that nobody is
// running a dictionary through this.
const (
	loginIPBurst    = 20
	loginIPWindow   = time.Minute
	loginAcctBurst  = 10
	loginAcctWindow = 15 * time.Minute
	// Separate buckets for asking for a link and for redeeming one. Sharing
	// them means somebody who mistypes a short password three times has spent
	// a third of their hour's allowance on the endpoint they still need.
	resetIPBurst  = 10
	resetIPWindow = time.Hour
	// A much tighter bucket per account, because a reset is the one request
	// here whose cost lands on somebody who did not send it: their inbox, and
	// the relay's standing with whoever hosts it. Three an hour is more than
	// anybody who has genuinely lost a password needs.
	resetAcctBurst  = 3
	resetAcctWindow = time.Hour
	redeemIPBurst   = 20
	// redeemIPWindow bounds guessing at a token. 20 an hour against 256 bits
	// is not a race anybody wins; the limit is here so the attempt costs
	// something rather than because it could ever succeed.
	redeemIPWindow = time.Hour
	// Asking for a passkey challenge is not an attempt to get in: nothing is
	// checked until login/finish, which is charged to the login bucket like a
	// password. The page asks for one whenever the sign-in screen is drawn, so
	// that the tap finds it waiting, and charging that to loginIP would let
	// looking at the screen spend the allowance for using it. It is still a
	// public endpoint that writes a row, so it is still bounded.
	passkeyBeginIPBurst  = 30
	passkeyBeginIPWindow = time.Minute
	// A page reports a passkey prompt its browser refused, once per refusal.
	// Anybody can post one, and each is a log line, so the allowance is what a
	// person failing repeatedly would use and no more.
	passkeyReportIPBurst  = 10
	passkeyReportIPWindow = time.Minute

	// sweepInterval is how often expired sessions and spent links are cleared
	// out. Nothing depends on it for correctness — both lookups already refuse
	// them — so it is housekeeping, and hourly is plenty.
	sweepInterval = time.Hour

	// tokenRetention keeps a redeemed link's row around past its expiry. It is
	// the only evidence that a link was used, and an account that has been
	// taken over is investigated afterwards, not during.
	tokenRetention = 7 * 24 * time.Hour
)

// Mailer is the one thing this package needs from the mail package.
//
// An interface so that a nil value is a legal, meaningful state — "this
// deployment has no SMTP" — rather than a flag threaded through every handler,
// and so that a test never opens a socket.
type Mailer interface {
	Send(ctx context.Context, to, subject, body string) error
}

// AuthOptions are the dependencies of the accounts surface.
type AuthOptions struct {
	Store  *store.Store
	Mailer Mailer // nil when SMTP is not configured
	Logger *slog.Logger

	// BaseURL is the origin links are built from. Empty produces relative
	// links, which are still usable by an admin pasting them into a browser
	// and are refused at startup if mail is configured.
	BaseURL string

	// TrustProxyHeaders says whether X-Forwarded-For may be believed.
	TrustProxyHeaders bool

	// Locale is the deployment's SOIREE_LOCALE. Only its language is used
	// here: it is what a mail is written in when whoever asked for it did not
	// say, which keeps the mail in step with the page its link opens.
	Locale string

	// EventName is what the mails say they are about. Empty is legal and
	// falls back to wording that names nothing, the way the digest does.
	EventName string
}

// Auth is the accounts, sessions and roles surface.
type Auth struct {
	store      *store.Store
	mailer     Mailer
	log        *slog.Logger
	baseURL    string
	trustProxy bool

	// defaultLanguage is the deployment's language, resolved once. See
	// mailLanguage.
	defaultLanguage string

	// eventName is what the mails are about. See inviteMessage.
	eventName string

	loginIP         *limiter
	loginAcct       *limiter
	resetIP         *limiter
	resetAcct       *limiter
	redeemIP        *limiter
	passkeyBeginIP  *limiter
	passkeyReportIP *limiter

	// passkeys is the WebAuthn surface, nil unless WithPasskeys turned it on.
	// Nil is what leaves its routes unmounted; see passkeys.go.
	passkeys *passkeys

	// params is the hashing policy. A field rather than a package constant so
	// the HTTP tests can turn the cost down; production never sets it and gets
	// auth.DefaultParams.
	params auth.Params

	// now is the clock, injectable so a test can age a session without
	// sleeping through it.
	now func() time.Time

	// background runs work that must outlive its request. Replaced in tests
	// with a synchronous version, which is what makes "did this send a mail?"
	// answerable without sleeping.
	background func(func(context.Context))
}

// NewAuth builds the accounts surface.
func NewAuth(o AuthOptions) *Auth {
	log := o.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Auth{
		store:      o.Store,
		mailer:     o.Mailer,
		log:        log,
		baseURL:    strings.TrimRight(o.BaseURL, "/"),
		trustProxy: o.TrustProxyHeaders,

		defaultLanguage: languageOfLocale(o.Locale),
		eventName:       o.EventName,

		loginIP:         newLimiter(loginIPBurst, loginIPWindow),
		loginAcct:       newLimiter(loginAcctBurst, loginAcctWindow),
		resetIP:         newLimiter(resetIPBurst, resetIPWindow),
		resetAcct:       newLimiter(resetAcctBurst, resetAcctWindow),
		redeemIP:        newLimiter(redeemIPBurst, redeemIPWindow),
		passkeyBeginIP:  newLimiter(passkeyBeginIPBurst, passkeyBeginIPWindow),
		passkeyReportIP: newLimiter(passkeyReportIPBurst, passkeyReportIPWindow),
		params:          auth.DefaultParams,
		now:             time.Now,
		background: func(fn func(context.Context)) {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), backgroundTimeout)
				defer cancel()
				fn(ctx)
			}()
		},
	}
}

// Register adds the accounts routes to mux.
//
// Everything lives under /api/v1/auth/ and /api/v1/users/. The REST API owns
// the rest of /api/v1 and registers on this same mux; the two do not overlap.
func (a *Auth) Register(mux *http.ServeMux) {
	// Public. Rate-limited per address, because these are the three doors
	// somebody who is not logged in can knock on.
	mux.Handle("POST /api/v1/auth/login", a.limitIP(a.loginIP, http.HandlerFunc(a.handleLogin)))
	mux.Handle("POST /api/v1/auth/logout", http.HandlerFunc(a.handleLogout))
	mux.Handle("POST /api/v1/auth/password-reset", a.limitIP(a.resetIP, http.HandlerFunc(a.handlePasswordReset)))
	// Not wrapped in the limiter: handleSetPassword charges for the request
	// itself, after the checks that cost nothing, so a fumbled password does
	// not spend the allowance the redemption still needs.
	mux.Handle("POST /api/v1/auth/set-password", http.HandlerFunc(a.handleSetPassword))

	// Passkeys, when this deployment has them. A no-op otherwise: the paths are
	// then not routes at all. See passkeys.go.
	a.registerPasskeyRoutes(mux)

	// Who am I. Any live session; the browser uses it to decide what to draw.
	mux.Handle("GET /api/v1/auth/session", a.RequireAuth(http.HandlerFunc(a.handleSession)))

	// Administration. Every one of these is an admin-only route, checked
	// server-side per request against the role as it stands in the database —
	// not as it stood when the session was created.
	admin := a.RequireRole(store.RoleAdmin)
	mux.Handle("GET /api/v1/users", admin(http.HandlerFunc(a.handleListUsers)))
	mux.Handle("POST /api/v1/users", admin(http.HandlerFunc(a.handleCreateUser)))
	mux.Handle("GET /api/v1/users/{id}", admin(http.HandlerFunc(a.handleGetUser)))
	mux.Handle("PATCH /api/v1/users/{id}", admin(http.HandlerFunc(a.handleUpdateUser)))
	mux.Handle("DELETE /api/v1/users/{id}", admin(http.HandlerFunc(a.handleDeleteUser)))
	mux.Handle("POST /api/v1/users/{id}/invite", admin(http.HandlerFunc(a.handleInvite)))
	mux.Handle("POST /api/v1/users/{id}/revoke-credentials",
		admin(http.HandlerFunc(a.handleRevokeCredentials)))
}

// Sweep clears out expired sessions and spent links until ctx is done.
//
// Housekeeping, not security: SessionByToken and ConsumePasswordToken already
// refuse everything this deletes. It exists so the two tables do not grow
// without bound in a deployment that runs for a year, and it runs on every
// replica because deleting rows that are already dead is idempotent.
func (a *Auth) Sweep(ctx context.Context) {
	t := time.NewTicker(sweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := a.store.DeleteExpiredSessions(ctx, sessionMaxLifetime); err != nil {
				a.log.Error("could not sweep sessions", "err", err)
			} else if n > 0 {
				a.log.Info("expired sessions removed", "count", n)
			}
			if _, err := a.store.DeleteExpiredPasswordTokens(ctx, tokenRetention); err != nil {
				a.log.Error("could not sweep password tokens", "err", err)
			}
		}
	}
}

// userDTO is the wire form of an account.
//
// A separate type rather than json tags on store.User, so that adding a column
// to the table cannot publish it by accident. The column this is protecting
// the world from is password_hash.
type userDTO struct {
	ID        uuid.UUID  `json:"id"`
	Email     string     `json:"email"`
	Role      string     `json:"role"`
	Status    string     `json:"status"`
	CreatedBy *uuid.UUID `json:"createdBy"`
	CreatedAt time.Time  `json:"createdAt"`
	Revision  int64      `json:"revision"`
	UpdatedAt time.Time  `json:"updatedAt"`
}

func toDTO(u store.User) userDTO {
	return userDTO{
		ID:        u.ID,
		Email:     u.Email,
		Role:      string(u.Role),
		Status:    string(u.Status),
		CreatedBy: u.CreatedBy,
		CreatedAt: u.CreatedAt,
		Revision:  u.Revision,
		UpdatedAt: u.UpdatedAt,
	}
}

// --- logging in -------------------------------------------------------------

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// handleLogin exchanges an address and a password for a session cookie.
//
// Every path through this function performs exactly one Argon2 evaluation,
// including the paths where there is no account, no password set, or the
// account is disabled. That is the whole reason for the shape below: the
// obvious early return on "no such user" makes the response time the answer to
// "does this address have an account here?".
func (a *Auth) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	email := normaliseEmail(req.Email)

	// No address at all is a malformed request, and is said to be one. It used
	// to share the branch below and answer 429 with Retry-After: 60 — telling a
	// client with a bug to wait a minute and send the same bug again. Refusing
	// it outright discloses nothing: whether the field is empty is a fact about
	// the request, not about any account.
	if email == "" {
		writeError(w, http.StatusBadRequest, "invalid_email", "email is required")
		return
	}

	// Keyed on the submitted address, whether or not it names an account. A
	// bucket that exists only for real accounts turns 429-versus-401 into the
	// same disclosure the constant-time work above is avoiding.
	if !a.loginAcct.allow(email) {
		tooManyRequests(w)
		return
	}

	hash := auth.DummyHash()
	user, err := a.store.UserByEmail(r.Context(), email)
	switch {
	case err == nil && user.PasswordHash != nil:
		hash = *user.PasswordHash
	case err == nil || errors.Is(err, store.ErrNotFound):
		// No account, or an invited one that has never set a password. Fall
		// through to the dummy hash and pay the same cost.
	default:
		a.log.Error("login lookup failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "")
		return
	}

	ok, rehash, verifyErr := a.params.Verify(hash, req.Password)
	if verifyErr != nil {
		// A stored hash this build cannot parse. Refuse the login rather than
		// letting a damaged row become a way in, and say so in the log because
		// it needs a person.
		a.log.Error("stored password hash is unreadable", "user", user.ID, "err", verifyErr)
		ok = false
	}
	// Checked after the verify, not before, so a disabled account costs the
	// same as an active one.
	if !ok || err != nil || user.PasswordHash == nil || user.Status != store.StatusActive {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "")
		return
	}

	// The one moment the plaintext is in hand and the stored encoding can be
	// brought up to current policy without asking anybody to do anything.
	if rehash {
		if newHash, hashErr := a.params.Hash(req.Password); hashErr != nil {
			a.log.Error("could not re-hash password", "user", user.ID, "err", hashErr)
		} else if err := a.store.SetPasswordHash(r.Context(), user.ID, newHash); err != nil {
			a.log.Error("could not store re-hashed password", "user", user.ID, "err", err)
		} else {
			a.log.Info("password re-hashed at current parameters", "user", user.ID)
		}
	}

	if !a.startSession(w, r, user, "password") {
		return
	}
	writeJSON(w, http.StatusOK, toDTO(user))
}

// startSession is the one place a session comes into existence.
//
// Every way of proving who you are ends here — a password today, a passkey in
// passkeys.go — and that is the point of it being a function rather than a
// paragraph inside handleLogin. A second way to mint a session is a second
// place for the cookie's attributes, the idle window and the absolute lifetime
// cap to drift out of agreement, and the drift would show up as a security
// property that holds on one path and not the other.
//
// It reports whether it succeeded, having already answered the client if not.
// The caller writes the body: what a successful login returns is the caller's
// business, and this has exactly one job.
//
// method names how the person proved it, for the log. Which of the two ways in
// was used is the sort of thing that matters only in retrospect, which is
// precisely when it is too late to start recording it.
func (a *Auth) startSession(w http.ResponseWriter, r *http.Request, user store.User, method string) bool {
	token, err := auth.NewToken()
	if err != nil {
		a.log.Error("could not mint a session token", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "")
		return false
	}
	if _, err := a.store.CreateSession(r.Context(), store.Session{
		UserID:    user.ID,
		TokenHash: auth.HashToken(token),
		ExpiresAt: a.now().Add(sessionIdleTimeout),
	}); err != nil {
		a.log.Error("could not create session", "user", user.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "")
		return false
	}

	a.log.Info("login", "user", user.ID, "role", user.Role, "method", method)
	setSessionCookie(w, token, a.now(), sessionIdleTimeout)
	return true
}

// handleLogout destroys the session this request arrived with.
//
// Unauthenticated and idempotent: logging out is not something to refuse, and
// a browser holding a session that has already expired still wants its cookie
// cleared.
func (a *Auth) handleLogout(w http.ResponseWriter, r *http.Request) {
	if token, ok := sessionToken(r); ok {
		if err := a.store.DeleteSessionByToken(r.Context(), auth.HashToken(token)); err != nil {
			a.log.Error("could not delete session", "err", err)
		}
	}
	clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// handleSession reports who the caller is.
func (a *Auth) handleSession(w http.ResponseWriter, r *http.Request) {
	u, ok := UserFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "")
		return
	}
	writeJSON(w, http.StatusOK, toDTO(u))
}

// --- setting a password -----------------------------------------------------

type passwordResetRequest struct {
	Email string `json:"email"`
	// Language is the language the sign-in screen is being read in. Optional.
	Language *string `json:"language"`
}

// handlePasswordReset mails a set-password link, if there is anybody to mail
// it to.
//
// The response is identical whether or not the address has an account: same
// status, same body, same headers. The work — the lookup, the token, the
// insert, the SMTP conversation — happens after the response is decided and
// off the response path, so the *timing* matches as well as the bytes. An
// endpoint that answers in 4 ms for a stranger and 300 ms for a user has
// announced the difference just as loudly as a different status code would.
func (a *Auth) handlePasswordReset(w http.ResponseWriter, r *http.Request) {
	var req passwordResetRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	email := normaliseEmail(req.Email)

	// The sign-in screen sends the language it is being read in, which is the
	// best evidence there is of what this person reads: nobody else is
	// involved in a reset, and the server has never seen their browser. One it
	// has no translation for is ignored rather than refused — this endpoint
	// answers 202 to everything, and a language is not worth an exception.
	var language *string
	if req.Language != nil {
		if l, ok := parseLanguage(*req.Language); ok {
			language = &l
		}
	}

	a.background(func(ctx context.Context) {
		if email == "" {
			return
		}
		// With no relay there is nobody to send the link to, and issuing one
		// supersedes whatever is outstanding. On a deployment without SMTP
		// this request would consume the link an admin is in the middle of
		// passing on by hand, in exchange for a link nobody ever sees.
		if a.mailer == nil {
			return
		}
		user, err := a.store.UserByEmail(ctx, email)
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				a.log.Error("reset lookup failed", "err", err)
			}
			return
		}
		// A disabled account gets nothing. Re-enabling is an admin's decision,
		// not something a reset link should route around.
		if user.Status == store.StatusDisabled {
			return
		}
		// Keyed on the account, which is the thing being spent: an address
		// nobody has an account for never gets this far, so the map holds a
		// dozen entries rather than one per address anybody cares to type.
		// Safe here in a way it would not be at login, because nothing about
		// this bucket reaches the caller: the 202 above is already written,
		// and refusing costs neither a different answer nor a different
		// amount of time.
		//
		// Refusing means issuing nothing at all, so the last link stays the
		// live one. Somebody who knows an address can use up its allowance,
		// which is the same trade loginAcct already makes, and an admin's
		// re-invite is not charged to this bucket.
		if !a.resetAcct.allow(user.ID.String()) {
			return
		}
		purpose := store.PurposeReset
		if user.Status == store.StatusInvited {
			purpose = store.PurposeInvite
		}
		// Nobody to hand it to but the mailer: this is the anonymous route,
		// and its link has no reader other than the address it is sent to.
		if _, err := a.issueToken(ctx, user, purpose, language, false); err != nil {
			a.log.Error("could not issue a reset link", "user", user.ID, "err", err)
		}
	})

	// 202: something may happen, and we are not saying what.
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

type setPasswordRequest struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

// handleSetPassword redeems a link and sets a password.
//
// The token arrives in the body rather than the query string, so it does not
// land in an access log, a Referer header or a browser history entry. The link
// that carries it keeps it in the URL fragment for the same reason — a
// fragment is never sent to a server.
func (a *Auth) handleSetPassword(w http.ResponseWriter, r *http.Request) {
	var req setPasswordRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if msg, ok := checkPassword(req.Password); !ok {
		writeError(w, http.StatusBadRequest, "weak_password", msg)
		return
	}
	if req.Token == "" {
		writeError(w, http.StatusBadRequest, "invalid_token", "")
		return
	}
	// Charged here rather than in middleware: everything above this line is a
	// free format check, and somebody choosing a password that turns out to be
	// too short should not burn the budget for the attempt that follows.
	if !a.redeemIP.allow(clientIP(r, a.trustProxy)) {
		tooManyRequests(w)
		return
	}
	tokenHash := auth.HashToken(req.Token)

	// Cheap check before the expensive one. Hashing first would make this
	// endpoint a way to spend 19 MiB and two Argon2 passes of somebody else's
	// memory bandwidth per request with a token made of nothing. The
	// redemption below is still the authority — this is allowed to be stale.
	live, err := a.store.PasswordTokenLive(r.Context(), tokenHash)
	if err != nil {
		a.log.Error("could not check token", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "")
		return
	}
	if !live {
		writeError(w, http.StatusBadRequest, "invalid_token", "")
		return
	}

	hash, err := a.params.Hash(req.Password)
	if err != nil {
		a.log.Error("could not hash password", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "")
		return
	}

	user, err := a.store.ConsumePasswordToken(r.Context(), tokenHash, hash)
	switch {
	case errors.Is(err, store.ErrInvalidToken):
		// Unknown, expired, already used, or attached to a disabled account —
		// one answer for all of them.
		writeError(w, http.StatusBadRequest, "invalid_token", "")
		return
	case err != nil:
		a.log.Error("could not redeem token", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "")
		return
	}

	// Redeeming revoked every session this account had, including the caller's
	// if they were signed in. Clearing the cookie keeps the browser honest
	// about that.
	clearSessionCookie(w)
	a.log.Info("password set", "user", user.ID)
	w.WriteHeader(http.StatusNoContent)
}

// --- administering accounts -------------------------------------------------

func (a *Auth) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := a.store.Users(r.Context())
	if err != nil {
		a.log.Error("could not list users", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "")
		return
	}
	out := make([]userDTO, 0, len(users))
	for _, u := range users {
		out = append(out, toDTO(u))
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out})
}

func (a *Auth) handleGetUser(w http.ResponseWriter, r *http.Request) {
	id, ok := authPathID(w, r)
	if !ok {
		return
	}
	user, err := a.store.User(r.Context(), id)
	if err != nil {
		a.userError(w, err, "read user")
		return
	}
	writeJSON(w, http.StatusOK, toDTO(user))
}

type createUserRequest struct {
	Email string `json:"email"`
	Role  string `json:"role"`
	// Language is the language of the invitation, and of nothing else: it is
	// used to write this one mail and the link inside it, and is not stored.
	// The admin creating the account is the one person who knows what the
	// person they are inviting reads; after that first mail, the page follows
	// the reader's own browser, which is better evidence than anything an
	// admin typed once. Omitted or null, the mail is in the deployment's
	// language.
	Language *string `json:"language"`
	// Deliver is who this one link is for. See chosenDelivery.
	Deliver string `json:"deliver"`
}

// createUserResponse carries the new account, and the link when — and only
// when — the admin is the one who has to carry it.
type createUserResponse struct {
	User userDTO `json:"user"`
	// SetPasswordURL is present on a deployment with no SMTP, and when an
	// admin asked for the link instead of the mail. With a relay configured
	// and nobody asking, it goes to the person it belongs to and nowhere
	// else, because an admin who never sees it cannot use it.
	SetPasswordURL string `json:"setPasswordUrl,omitempty"`
	// MailSent says whether delivery was attempted, so the admin knows
	// whether to expect the other field.
	MailSent bool `json:"mailSent"`
}

// handleCreateUser is the only way an account comes into existence.
//
// No self-service sign-up and no open invite link: an admin names an address
// and a role, and the account starts with no password at all. What the new
// person receives is a single-use link, and the admin never learns the
// password that ends up behind it.
func (a *Auth) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	actor, _ := UserFrom(r.Context())

	var req createUserRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	email, ok := parseEmail(req.Email)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_email", "email must be a valid address")
		return
	}
	role, ok := parseRole(req.Role)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_role", "role must be admin, editor or viewer")
		return
	}

	language, ok := a.chosenLanguage(w, req.Language)
	if !ok {
		return
	}
	// Before the account exists, so that a misspelled delivery does not leave
	// an address taken by a request that was refused.
	toAdmin, ok := a.chosenDelivery(w, req.Deliver)
	if !ok {
		return
	}

	user, err := a.store.CreateUser(r.Context(), store.User{
		Email:     email,
		Role:      role,
		Status:    store.StatusInvited,
		CreatedBy: &actor.ID,
	})
	if err != nil {
		if store.IsUniqueViolation(err) {
			writeError(w, http.StatusConflict, "email_taken", "that address already has an account")
			return
		}
		a.log.Error("could not create user", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "")
		return
	}
	a.log.Info("account created", "user", user.ID, "role", user.Role, "by", actor.ID)

	a.respondWithInvite(w, r, user, store.PurposeInvite, language, toAdmin, http.StatusCreated)
}

// handleInvite issues a fresh link for an existing account: the first one
// expired, or went to a mailbox nobody reads. Issuing supersedes whatever was
// outstanding, so this is also how a link that leaked is revoked.
func (a *Auth) handleInvite(w http.ResponseWriter, r *http.Request) {
	id, ok := authPathID(w, r)
	if !ok {
		return
	}
	user, err := a.store.User(r.Context(), id)
	if err != nil {
		a.userError(w, err, "read user")
		return
	}
	if user.Status == store.StatusDisabled {
		writeError(w, http.StatusConflict, "account_disabled", "enable the account before inviting it")
		return
	}
	purpose := store.PurposeReset
	if user.Status == store.StatusInvited {
		purpose = store.PurposeInvite
	}

	// The body is optional, and so is everything in it. This route took no
	// body at all before a mail had a language, and `curl -X POST` with
	// nothing attached must go on meaning "send it, in the usual language".
	var req inviteRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid_json", "")
		return
	}
	language, ok := a.chosenLanguage(w, req.Language)
	if !ok {
		return
	}
	toAdmin, ok := a.chosenDelivery(w, req.Deliver)
	if !ok {
		return
	}
	a.respondWithInvite(w, r, user, purpose, language, toAdmin, http.StatusOK)
}

type inviteRequest struct {
	// Language is the language of this one mail. See createUserRequest.
	Language *string `json:"language"`
	// Deliver is who this one link is for. See chosenDelivery.
	Deliver string `json:"deliver"`
}

// chosenLanguage reads the language an admin picked for a mail. Nil and true
// means nothing was picked. A language there is no translation for is refused
// rather than quietly answered in another one: an admin who asked for Dutch
// and sent English would not find out.
func (a *Auth) chosenLanguage(w http.ResponseWriter, asked *string) (*string, bool) {
	if asked == nil {
		return nil, true
	}
	l, ok := parseLanguage(*asked)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_language", "language must be one of "+strings.Join(languages, ", "))
		return nil, false
	}
	return &l, true
}

// chosenDelivery reads who an admin asked the link to go to. "mail", or
// nothing at all, is the default and the one that sends it. "link" holds the
// mail back and returns the link for the admin to carry.
//
// It exists because a mail a relay drops is a dead end: the invited person has
// nothing, re-inviting sends the same mail down the same pipe, and until this
// no route handed the link over. An admin could already reach the same place
// by pointing the account at a mailbox of their own and re-inviting, which
// yields the same link and records nothing about how it was got. So this is
// the honest version of something that was possible anyway: explicit, asked
// for, and logged.
func (a *Auth) chosenDelivery(w http.ResponseWriter, asked string) (toAdmin, ok bool) {
	switch strings.ToLower(strings.TrimSpace(asked)) {
	case "", "mail":
		return false, true
	case "link":
		return true, true
	}
	writeError(w, http.StatusBadRequest, "invalid_deliver", "deliver must be mail or link")
	return false, false
}

// respondWithInvite mints the link and decides who gets to see it.
func (a *Auth) respondWithInvite(w http.ResponseWriter, r *http.Request, user store.User, purpose store.TokenPurpose, language *string, toAdmin bool, status int) {
	// The half of the rule that is worth keeping. An account somebody is
	// already using has a password, a session and a history in the activity
	// log to impersonate, so its link goes to its owner and to nobody else;
	// otherwise taking over a live account, another admin's included, would be
	// a button. An account that has never set a password has none of those and
	// is where the dead end actually is. Nothing to weigh when there is no
	// relay: every link already comes back to the admin there.
	//
	// On the hash rather than on the status, which is close to the same answer
	// and not the same fact: an admin may write a status, and an account moved
	// back to invited keeps the password it had.
	if toAdmin && a.mailer != nil && user.PasswordHash != nil {
		writeError(w, http.StatusConflict, "account_active",
			"a link for an account that has set a password goes to its owner by mail")
		return
	}

	link, err := a.issueToken(r.Context(), user, purpose, language, toAdmin)
	if err != nil {
		a.log.Error("could not issue a set-password link", "user", user.ID, "err", err)
		// The account exists; only the link failed. Say so rather than
		// implying the whole thing failed, or the admin creates it twice.
		writeError(w, http.StatusInternalServerError, "link_failed",
			"the account was created but no set-password link could be issued; try inviting again")
		return
	}

	res := createUserResponse{User: toDTO(user), MailSent: a.mailer != nil && !toAdmin}
	if a.mailer == nil || toAdmin {
		// The documented degraded mode: no SMTP, so the admin passes the link
		// on by hand. A deployment without mail is inconvenient, not broken.
		// And the same by request, for the one case where the mail is the
		// thing that failed.
		res.SetPasswordURL = link
	}
	if toAdmin {
		// The link itself is never logged. Who took one out is, because this
		// is the one way a credential leaves here in somebody else's hands.
		actor, _ := UserFrom(r.Context())
		a.log.Info("account link issued to admin", "user", user.ID, "by", actor.ID)
	}
	writeJSON(w, status, res)
}

// issueToken records a single-use token and sends the link, returning it.
//
// The return value is a credential. It goes into a response on the no-SMTP
// path and when an admin asked to carry it themselves, and it is never logged
// anywhere.
//
// toAdmin holds the mail back: the caller is taking the link instead of
// sending it, and two links where one was expected is one link nobody can
// account for.
//
// language is whatever whoever asked for the mail said it should be in, or nil.
// It shapes this mail and this link and is kept nowhere.
func (a *Auth) issueToken(ctx context.Context, user store.User, purpose store.TokenPurpose, language *string, toAdmin bool) (string, error) {
	token, err := auth.NewToken()
	if err != nil {
		return "", err
	}
	if _, err := a.store.IssuePasswordToken(ctx, store.PasswordToken{
		UserID:    user.ID,
		TokenHash: auth.HashToken(token),
		Purpose:   purpose,
		ExpiresAt: a.now().Add(passwordTokenTTL),
	}); err != nil {
		return "", err
	}

	link := a.setPasswordURL(token, language)
	if a.mailer != nil && !toAdmin {
		subject, body := inviteMessage(purpose, link, a.mailLanguage(language), a.eventName, a.baseURL)
		a.background(func(ctx context.Context) {
			if err := a.mailer.Send(ctx, user.Email, subject, body); err != nil {
				// The error, never the message. The body is the link.
				a.log.Error("could not send account mail", "user", user.ID, "err", err)
			}
		})
	}
	return link, nil
}

// setPasswordURL builds the link.
//
// The token sits in the URL fragment, not the query string. A fragment is
// never sent to a server, so the one-time secret stays out of this
// application's access logs, out of any proxy's in front of it, and out of the
// Referer header the page would otherwise leak it through. The origin comes
// from configuration and never from the request's Host header, which the
// client controls.
//
// A mail somebody chose a language for carries it as ?lang=, so the screen the
// link opens is in the language the mail was. That part is a query string and
// is sent to the server, which is fine: it is a language tag, and the secret
// is still behind the #. With no language chosen the link is bare, and the
// page decides for itself — from the reader's browser, then the deployment.
func (a *Auth) setPasswordURL(token string, language *string) string {
	query := ""
	if language != nil {
		if l, ok := parseLanguage(*language); ok {
			query = "?lang=" + l
		}
	}
	return a.baseURL + "/" + query + "#/set-password?token=" + url.QueryEscape(token)
}

// inviteMessage composes the two mails this surface sends.
//
// Plain text and short: the link is the message. They name the event, which
// they deliberately did not until this was reversed. The old reasoning was
// that a mail to a mistyped address should tell a stranger nothing about whose
// planner this is; what it protected turned out to be nothing, because the
// link's own hostname is in the mail and the page behind it hands the event's
// name and date to any anonymous visitor, and the stranger is holding a
// working credential besides. What the omission did cost was borne by the
// person the mail was meant for, who had an unsigned account mail from an
// unfamiliar domain to judge on twenty words and a tokenised URL. The digest
// has named the event in its subject all along. The admin who sent it is still
// deliberately unnamed.
//
// Only the invitation carries the line about an expired link. Whoever asked
// for a reset has just used the control it points at.
//
// site is the deployment's own address, and it never goes in front of the
// link: the first https:// in the body is how a reader, and every test here,
// finds the thing to click.
func inviteMessage(purpose store.TokenPurpose, link, language, eventName, site string) (subject, body string) {
	m := accountMail(language, purpose)

	subject, lede := m.subject, m.lede
	if eventName != "" {
		subject = fmt.Sprintf(m.subjectNamed, eventName)
		lede = fmt.Sprintf(m.ledeNamed, eventName)
	}
	tail := m.tail
	// Configuration refuses mail without a base URL, so an empty site is a
	// caller inside this package rather than a deployment. Say nothing rather
	// than point at nowhere.
	if m.expired != "" && site != "" {
		tail += " " + fmt.Sprintf(m.expired, site)
	}
	return subject, lede + "\n\n" + link + "\n\n" + tail + "\n"
}

// mailText is one language's wording for one of the two mails.
//
// The named halves take the event name and are what a deployment sends; the
// bare ones are the fallback for a deployment that has emptied
// SOIREE_EVENT_NAME, the way internal/reminders/render.go falls back for the
// digest's subject. Adding a language means adding a case to accountMail, and
// leaving a field empty there is caught by TestEveryLanguageHasItsOwnMails.
type mailText struct {
	subject, subjectNamed string // subjectNamed takes the event name
	lede, ledeNamed       string // what stands above the link
	tail                  string // what stands below it
	expired               string // takes the site; the invitation only
}

func accountMail(language string, purpose store.TokenPurpose) mailText {
	reset := purpose == store.PurposeReset
	switch language {
	case "nl":
		if reset {
			return mailText{
				subject:      "Stel een nieuw wachtwoord in",
				subjectNamed: "%s: stel een nieuw wachtwoord in",
				lede:         "Iemand heeft gevraagd om een nieuw wachtwoord voor je account in te stellen.",
				ledeNamed:    "Iemand heeft gevraagd om een nieuw wachtwoord voor je account voor %s in te stellen.",
				tail: "De link werkt één keer en verloopt na 24 uur. " +
					"Was jij dit niet, dan is er niets veranderd en kun je dit bericht negeren.",
			}
		}
		return mailText{
			subject:      "Je account staat klaar",
			subjectNamed: "%s: kies een wachtwoord",
			lede:         "Er is een account voor je aangemaakt. Kies hier een wachtwoord:",
			ledeNamed:    "Je bent toegevoegd aan de planner voor %s. Kies hier een wachtwoord:",
			tail:         "De link werkt één keer en verloopt na 24 uur.",
			expired: "Is de link verlopen, ga dan naar %s en kies " +
				"\"Mail me een link om een nieuw wachtwoord in te stellen\"; " +
				"je krijgt er dan een nieuwe op dit adres.",
		}
	case "id":
		if reset {
			return mailText{
				subject:      "Buat kata sandi baru",
				subjectNamed: "%s: buat kata sandi baru",
				lede:         "Seseorang meminta pembuatan kata sandi baru untuk akunmu.",
				ledeNamed:    "Seseorang meminta pembuatan kata sandi baru untuk akunmu di %s.",
				tail: "Tautan ini hanya bisa dipakai sekali dan kedaluwarsa dalam 24 jam. " +
					"Kalau ini bukan kamu, tidak ada yang berubah dan pesan ini bisa diabaikan.",
			}
		}
		return mailText{
			subject:      "Akunmu sudah siap",
			subjectNamed: "%s: buat kata sandi",
			lede:         "Sebuah akun telah dibuat untukmu. Buat kata sandi di sini:",
			ledeNamed:    "Kamu telah ditambahkan ke perencana %s. Buat kata sandi di sini:",
			tail:         "Tautan ini hanya bisa dipakai sekali dan kedaluwarsa dalam 24 jam.",
			expired: "Kalau sudah kedaluwarsa, buka %s lalu pilih " +
				"\"Kirimi saya tautan untuk membuat kata sandi baru\" " +
				"agar tautan baru dikirim ke alamat ini.",
		}
	}
	if reset {
		return mailText{
			subject:      "Set a new password",
			subjectNamed: "%s: set a new password",
			lede:         "Someone asked to set a new password on your account.",
			ledeNamed:    "Someone asked to set a new password on your account for %s.",
			tail: "The link works once and expires in 24 hours. " +
				"If this was not you, nothing has changed and you can ignore this.",
		}
	}
	return mailText{
		subject:      "Your account is ready",
		subjectNamed: "%s: choose your password",
		lede:         "An account has been created for you. Choose a password here:",
		ledeNamed:    "You have been added to the planner for %s. Choose a password here:",
		tail:         "The link works once and expires in 24 hours.",
		expired: "If it has expired, open %s and choose " +
			"\"Email me a link to set a new password\" " +
			"to have a fresh one sent to this address.",
	}
}

// handleRevokeCredentials takes away every way into somebody's account: their
// sessions, their passkeys, a registration in flight, and any set-password
// link still outstanding. The password is left alone, so the way back is the
// fresh link an admin sends next.
//
// It exists because that fresh link is not enough on its own. Registering a
// passkey asks only for a live session, so a minute at an unattended browser
// buys a credential of one's own, and that credential outlives every remedy
// this surface otherwise has: setting a password does not touch it, each login
// with it mints a new session so the absolute cap never reaches it, and
// disabling the account only parks it until somebody enables the account
// again. Until this route, removing one meant deleting the account.
//
// The answer says nothing about what was found. How many passkeys somebody had
// is not an admin's business: there is still no admin view of anybody's
// credentials, and taking them all away needs none.
func (a *Auth) handleRevokeCredentials(w http.ResponseWriter, r *http.Request) {
	actor, _ := UserFrom(r.Context())
	id, ok := authPathID(w, r)
	if !ok {
		return
	}
	// Not on oneself. An admin's own passkeys are on their own account screen,
	// where the list says which device is which and the one row that does not
	// belong can go without the rest; doing it here would take all of them and
	// sign the admin out of the screen they are standing on.
	if id == actor.ID {
		writeError(w, http.StatusBadRequest, "self_change",
			"remove your own passkeys and sessions from your account screen")
		return
	}
	// Read first, so an account that is not there is a 404 rather than a
	// revocation that reports success and did nothing.
	if _, err := a.store.User(r.Context(), id); err != nil {
		a.userError(w, err, "read user")
		return
	}
	if err := a.store.RevokeCredentials(r.Context(), id); err != nil {
		a.log.Error("could not revoke credentials", "user", id, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "")
		return
	}
	a.log.Info("credentials revoked", "user", id, "by", actor.ID)
	w.WriteHeader(http.StatusNoContent)
}

type updateUserRequest struct {
	Revision *int64  `json:"revision"`
	Role     *string `json:"role"`
	Status   *string `json:"status"`
	Email    *string `json:"email"`
}

// handleUpdateUser changes a role, a status or an address.
//
// Carries the same revision check as every other write in this application: if
// another admin changed this account first, the write is refused and the
// account as it now stands comes back, rather than one admin silently undoing
// the other.
func (a *Auth) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	actor, _ := UserFrom(r.Context())
	id, ok := authPathID(w, r)
	if !ok {
		return
	}

	var req updateUserRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Revision == nil {
		writeError(w, http.StatusBadRequest, "revision_required",
			"include the revision you last saw, so a concurrent edit is refused rather than overwritten")
		return
	}

	current, err := a.store.User(r.Context(), id)
	if err != nil {
		a.userError(w, err, "read user")
		return
	}

	// An admin cannot demote or disable themselves. Not paternalism: the only
	// way to get the role back is another admin, and a deployment with one
	// admin who has just made themselves a viewer has no way back in at all.
	if current.ID == actor.ID && (req.Role != nil || req.Status != nil) {
		writeError(w, http.StatusBadRequest, "self_change",
			"change your own role or status from another admin's account, or there may be no way back in")
		return
	}

	next := current
	next.Revision = *req.Revision
	if req.Role != nil {
		role, ok := parseRole(*req.Role)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_role", "role must be admin, editor or viewer")
			return
		}
		next.Role = role
	}
	if req.Status != nil {
		status, ok := parseStatus(*req.Status)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_status", "status must be invited, active or disabled")
			return
		}
		// An account with no password cannot be made active by decree; it
		// becomes active by somebody following their link.
		if status == store.StatusActive && current.PasswordHash == nil {
			writeError(w, http.StatusConflict, "no_password",
				"this account has never set a password; invite it instead")
			return
		}
		next.Status = status
	}
	if req.Email != nil {
		email, ok := parseEmail(*req.Email)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_email", "email must be a valid address")
			return
		}
		next.Email = email
	}
	updated, err := a.store.UpdateUser(r.Context(), next)
	if err != nil {
		var stale *store.StaleRevisionError
		if errors.As(err, &stale) {
			a.writeStale(w, stale)
			return
		}
		if store.IsUniqueViolation(err) {
			writeError(w, http.StatusConflict, "email_taken", "that address already has an account")
			return
		}
		a.userError(w, err, "update user")
		return
	}

	// Disabling is expected to take effect now, not whenever the session
	// happens to expire. The session lookup already refuses a non-active
	// account, so this is belt and braces — and it is the belt that survives
	// somebody later relaxing that join.
	if updated.Status == store.StatusDisabled {
		if _, err := a.store.DeleteSessionsForUser(r.Context(), updated.ID); err != nil {
			a.log.Error("could not revoke sessions", "user", updated.ID, "err", err)
		}
	}
	a.log.Info("account updated", "user", updated.ID, "role", updated.Role, "status", updated.Status, "by", actor.ID)
	writeJSON(w, http.StatusOK, toDTO(updated))
}

// handleDeleteUser removes an account.
//
// The revision goes in the query string because a DELETE body is poorly
// supported by intermediaries, and it is not optional: deleting the wrong
// person because somebody else edited the row first is exactly what the
// revision check exists to prevent.
func (a *Auth) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	actor, _ := UserFrom(r.Context())
	id, ok := authPathID(w, r)
	if !ok {
		return
	}
	revision, err := strconv.ParseInt(r.URL.Query().Get("revision"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "revision_required",
			"pass ?revision= with the revision you last saw")
		return
	}
	if id == actor.ID {
		writeError(w, http.StatusBadRequest, "self_change", "delete your own account from another admin's")
		return
	}

	if err := a.store.DeleteUser(r.Context(), id, revision); err != nil {
		var stale *store.StaleRevisionError
		if errors.As(err, &stale) {
			a.writeStale(w, stale)
			return
		}
		a.userError(w, err, "delete user")
		return
	}
	a.log.Info("account deleted", "user", id, "by", actor.ID)
	w.WriteHeader(http.StatusNoContent)
}

// writeStale returns the 409 the rest of the API returns, carrying the row as
// it now stands so the caller can reconcile instead of refetching.
func (a *Auth) writeStale(w http.ResponseWriter, stale *store.StaleRevisionError) {
	body := map[string]any{"error": "stale_revision"}
	if u, ok := stale.Current.(store.User); ok {
		body["current"] = toDTO(u)
	}
	writeJSON(w, http.StatusConflict, body)
}

// userError maps a store error onto a status.
func (a *Auth) userError(w http.ResponseWriter, err error, what string) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "")
		return
	}
	a.log.Error("could not "+what, "err", err)
	writeError(w, http.StatusInternalServerError, "internal", "")
}

// --- shared plumbing --------------------------------------------------------

// normaliseEmail lowercases and trims, matching the case-insensitive unique
// index on the table. Anything else lets Ada@ and ada@ be two accounts in the
// application's head and one in the database's.
func normaliseEmail(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func parseEmail(s string) (string, bool) {
	addr, err := mail.ParseAddress(strings.TrimSpace(s))
	if err != nil {
		return "", false
	}
	return normaliseEmail(addr.Address), true
}

func parseRole(s string) (store.Role, bool) {
	switch store.Role(strings.TrimSpace(s)) {
	case store.RoleAdmin:
		return store.RoleAdmin, true
	case store.RoleEditor:
		return store.RoleEditor, true
	case store.RoleViewer:
		return store.RoleViewer, true
	default:
		return "", false
	}
}

func parseStatus(s string) (store.UserStatus, bool) {
	switch store.UserStatus(strings.TrimSpace(s)) {
	case store.StatusInvited:
		return store.StatusInvited, true
	case store.StatusActive:
		return store.StatusActive, true
	case store.StatusDisabled:
		return store.StatusDisabled, true
	default:
		return "", false
	}
}

// checkPassword enforces length and nothing else.
//
// Composition rules — a capital, a digit, a symbol — measurably push people
// towards Summer2026! and a sticky note. Length is the property that actually
// costs an attacker something.
//
// The floor is config's, and asked for rather than measured here, because the
// way of counting drifts as quietly as the number: twelve counted in bytes is
// four characters to anybody writing in a script that takes three bytes each,
// which is not the promise the operator was given.
func checkPassword(p string) (string, bool) {
	switch {
	case config.PasswordTooShort(p):
		return fmt.Sprintf("a password needs at least %d characters", config.MinPasswordLen), false
	case len(p) > maxPasswordLen:
		return fmt.Sprintf("a password may be at most %d characters", maxPasswordLen), false
	default:
		return "", true
	}
}

func authPathID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "")
		return uuid.Nil, false
	}
	return id, true
}

// decodeJSON reads a bounded JSON body, answering the client itself when the
// body is unusable.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "")
		return false
	}
	return true
}

// writeJSON writes a response that no cache may keep. Everything on this
// surface is either a credential exchange or an account list.
func tooManyRequests(w http.ResponseWriter) {
	// No Retry-After with a real number: the bucket refills continuously, and
	// a precise answer would only help somebody pacing an attack against it.
	w.Header().Set("Retry-After", "60")
	writeError(w, http.StatusTooManyRequests, "rate_limited", "")
}
