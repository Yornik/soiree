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
	"log/slog"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Yornik/soiree/internal/auth"
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

	// minPasswordLen follows the modern advice: length, not composition rules.
	// Requiring a digit and a symbol produces Passw0rd! and a sticky note.
	minPasswordLen = 12
	// maxPasswordLen bounds what gets hashed. Argon2 does not truncate the way
	// bcrypt does, so without a cap a megabyte of "a" is a request that costs
	// the server real work.
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
	tokenIPBurst    = 10
	tokenIPWindow   = time.Hour

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
}

// Auth is the accounts, sessions and roles surface.
type Auth struct {
	store      *store.Store
	mailer     Mailer
	log        *slog.Logger
	baseURL    string
	trustProxy bool

	loginIP   *limiter
	loginAcct *limiter
	tokenIP   *limiter

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
		loginIP:    newLimiter(loginIPBurst, loginIPWindow),
		loginAcct:  newLimiter(loginAcctBurst, loginAcctWindow),
		tokenIP:    newLimiter(tokenIPBurst, tokenIPWindow),
		params:     auth.DefaultParams,
		now:        time.Now,
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
	mux.Handle("POST /api/v1/auth/password-reset", a.limitIP(a.tokenIP, http.HandlerFunc(a.handlePasswordReset)))
	mux.Handle("POST /api/v1/auth/set-password", a.limitIP(a.tokenIP, http.HandlerFunc(a.handleSetPassword)))

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

	// Keyed on the submitted address, whether or not it names an account. A
	// bucket that exists only for real accounts turns 429-versus-401 into the
	// same disclosure the constant-time work above is avoiding.
	if email == "" || !a.loginAcct.allow(email) {
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
		writeError(w, http.StatusInternalServerError, "internal")
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
		writeError(w, http.StatusUnauthorized, "invalid_credentials")
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

	token, err := auth.NewToken()
	if err != nil {
		a.log.Error("could not mint a session token", "err", err)
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	expires := a.now().Add(sessionIdleTimeout)
	if _, err := a.store.CreateSession(r.Context(), store.Session{
		UserID:    user.ID,
		TokenHash: auth.HashToken(token),
		ExpiresAt: expires,
	}); err != nil {
		a.log.Error("could not create session", "user", user.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}

	a.log.Info("login", "user", user.ID, "role", user.Role)
	setSessionCookie(w, token, expires)
	writeJSON(w, http.StatusOK, toDTO(user))
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
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	writeJSON(w, http.StatusOK, toDTO(u))
}

// --- setting a password -----------------------------------------------------

type passwordResetRequest struct {
	Email string `json:"email"`
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

	a.background(func(ctx context.Context) {
		if email == "" {
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
		purpose := store.PurposeReset
		if user.Status == store.StatusInvited {
			purpose = store.PurposeInvite
		}
		if _, err := a.issueToken(ctx, user, purpose); err != nil {
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
		writeErrorMessage(w, http.StatusBadRequest, "weak_password", msg)
		return
	}
	if req.Token == "" {
		writeError(w, http.StatusBadRequest, "invalid_token")
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
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	if !live {
		writeError(w, http.StatusBadRequest, "invalid_token")
		return
	}

	hash, err := a.params.Hash(req.Password)
	if err != nil {
		a.log.Error("could not hash password", "err", err)
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}

	user, err := a.store.ConsumePasswordToken(r.Context(), tokenHash, hash)
	switch {
	case errors.Is(err, store.ErrInvalidToken):
		// Unknown, expired, already used, or attached to a disabled account —
		// one answer for all of them.
		writeError(w, http.StatusBadRequest, "invalid_token")
		return
	case err != nil:
		a.log.Error("could not redeem token", "err", err)
		writeError(w, http.StatusInternalServerError, "internal")
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
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	out := make([]userDTO, 0, len(users))
	for _, u := range users {
		out = append(out, toDTO(u))
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out})
}

func (a *Auth) handleGetUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
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
}

// createUserResponse carries the new account, and the link when — and only
// when — there is no mail to send it by.
type createUserResponse struct {
	User userDTO `json:"user"`
	// SetPasswordURL is present only on a deployment with no SMTP. With a
	// relay configured the link goes to the person it belongs to and nowhere
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
		writeErrorMessage(w, http.StatusBadRequest, "invalid_email", "email must be a valid address")
		return
	}
	role, ok := parseRole(req.Role)
	if !ok {
		writeErrorMessage(w, http.StatusBadRequest, "invalid_role", "role must be admin, editor or viewer")
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
			writeErrorMessage(w, http.StatusConflict, "email_taken", "that address already has an account")
			return
		}
		a.log.Error("could not create user", "err", err)
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	a.log.Info("account created", "user", user.ID, "role", user.Role, "by", actor.ID)

	a.respondWithInvite(w, r, user, store.PurposeInvite, http.StatusCreated)
}

// handleInvite issues a fresh link for an existing account: the first one
// expired, or went to a mailbox nobody reads. Issuing supersedes whatever was
// outstanding, so this is also how a link that leaked is revoked.
func (a *Auth) handleInvite(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	user, err := a.store.User(r.Context(), id)
	if err != nil {
		a.userError(w, err, "read user")
		return
	}
	if user.Status == store.StatusDisabled {
		writeErrorMessage(w, http.StatusConflict, "account_disabled", "enable the account before inviting it")
		return
	}
	purpose := store.PurposeReset
	if user.Status == store.StatusInvited {
		purpose = store.PurposeInvite
	}
	a.respondWithInvite(w, r, user, purpose, http.StatusOK)
}

// respondWithInvite mints the link and decides who gets to see it.
func (a *Auth) respondWithInvite(w http.ResponseWriter, r *http.Request, user store.User, purpose store.TokenPurpose, status int) {
	link, err := a.issueToken(r.Context(), user, purpose)
	if err != nil {
		a.log.Error("could not issue a set-password link", "user", user.ID, "err", err)
		// The account exists; only the link failed. Say so rather than
		// implying the whole thing failed, or the admin creates it twice.
		writeErrorMessage(w, http.StatusInternalServerError, "link_failed",
			"the account was created but no set-password link could be issued; try inviting again")
		return
	}

	res := createUserResponse{User: toDTO(user), MailSent: a.mailer != nil}
	if a.mailer == nil {
		// The documented degraded mode: no SMTP, so the admin passes the link
		// on by hand. A deployment without mail is inconvenient, not broken.
		res.SetPasswordURL = link
	}
	writeJSON(w, status, res)
}

// issueToken records a single-use token and sends the link, returning it.
//
// The return value is a credential. It goes into a response only on the
// no-SMTP path, and it is never logged anywhere.
func (a *Auth) issueToken(ctx context.Context, user store.User, purpose store.TokenPurpose) (string, error) {
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

	link := a.setPasswordURL(token)
	if a.mailer != nil {
		subject, body := inviteMessage(purpose, link)
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
func (a *Auth) setPasswordURL(token string) string {
	return a.baseURL + "/#/set-password?token=" + url.QueryEscape(token)
}

func inviteMessage(purpose store.TokenPurpose, link string) (subject, body string) {
	if purpose == store.PurposeReset {
		subject = "Set a new password"
		body = "Someone asked to set a new password on your account.\n\n" +
			link + "\n\nThe link works once and expires in 24 hours. " +
			"If this was not you, nothing has changed and you can ignore this.\n"
		return subject, body
	}
	subject = "Your account is ready"
	body = "An account has been created for you. Choose a password here:\n\n" +
		link + "\n\nThe link works once and expires in 24 hours.\n"
	return subject, body
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
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	var req updateUserRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Revision == nil {
		writeErrorMessage(w, http.StatusBadRequest, "revision_required",
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
		writeErrorMessage(w, http.StatusBadRequest, "self_change",
			"change your own role or status from another admin's account, or there may be no way back in")
		return
	}

	next := current
	next.Revision = *req.Revision
	if req.Role != nil {
		role, ok := parseRole(*req.Role)
		if !ok {
			writeErrorMessage(w, http.StatusBadRequest, "invalid_role", "role must be admin, editor or viewer")
			return
		}
		next.Role = role
	}
	if req.Status != nil {
		status, ok := parseStatus(*req.Status)
		if !ok {
			writeErrorMessage(w, http.StatusBadRequest, "invalid_status", "status must be invited, active or disabled")
			return
		}
		// An account with no password cannot be made active by decree; it
		// becomes active by somebody following their link.
		if status == store.StatusActive && current.PasswordHash == nil {
			writeErrorMessage(w, http.StatusConflict, "no_password",
				"this account has never set a password; invite it instead")
			return
		}
		next.Status = status
	}
	if req.Email != nil {
		email, ok := parseEmail(*req.Email)
		if !ok {
			writeErrorMessage(w, http.StatusBadRequest, "invalid_email", "email must be a valid address")
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
			writeErrorMessage(w, http.StatusConflict, "email_taken", "that address already has an account")
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
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	revision, err := strconv.ParseInt(r.URL.Query().Get("revision"), 10, 64)
	if err != nil {
		writeErrorMessage(w, http.StatusBadRequest, "revision_required",
			"pass ?revision= with the revision you last saw")
		return
	}
	if id == actor.ID {
		writeErrorMessage(w, http.StatusBadRequest, "self_change", "delete your own account from another admin's")
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
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	a.log.Error("could not "+what, "err", err)
	writeError(w, http.StatusInternalServerError, "internal")
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
func checkPassword(p string) (string, bool) {
	switch {
	case len(p) < minPasswordLen:
		return fmt.Sprintf("a password needs at least %d characters", minPasswordLen), false
	case len(p) > maxPasswordLen:
		return fmt.Sprintf("a password may be at most %d characters", maxPasswordLen), false
	default:
		return "", true
	}
}

func pathID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found")
		return uuid.Nil, false
	}
	return id, true
}

// decodeJSON reads a bounded JSON body, answering the client itself when the
// body is unusable.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json")
		return false
	}
	return true
}

// writeJSON writes a response that no cache may keep. Everything on this
// surface is either a credential exchange or an account list.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	// The status line is already sent; a failure here can only be a broken
	// connection, and there is nothing left to say on it.
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

func writeErrorMessage(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"error": code, "message": message})
}

func tooManyRequests(w http.ResponseWriter) {
	// No Retry-After with a real number: the bucket refills continuously, and
	// a precise answer would only help somebody pacing an attack against it.
	w.Header().Set("Retry-After", "60")
	writeError(w, http.StatusTooManyRequests, "rate_limited")
}
