package httpd

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Yornik/soiree/internal/auth"
	"github.com/Yornik/soiree/internal/config"
	"github.com/Yornik/soiree/internal/migrate"
	"github.com/Yornik/soiree/internal/pgtest"
	"github.com/Yornik/soiree/internal/store"
	"github.com/Yornik/soiree/web"
)

// Ada, Grace and Linus at example.test. Obviously synthetic, deliberately.
const (
	goodPassword  = "a long enough password"
	otherPassword = "a different long password"
)

// cheapParams keeps the suite quick. Argon2id at policy is 19 MiB and two
// passes per call, and CI runs with -race on top of that; the policy
// parameters are asserted in package auth, which is where they belong.
var cheapParams = auth.Params{Memory: 1024, Time: 1, Threads: 1, KeyLen: 32, SaltLen: 16}

// fakeMailer records what would have been sent. A test that opened a socket
// would be testing the relay.
type fakeMailer struct {
	mu   sync.Mutex
	sent []sentMail
}

type sentMail struct{ to, subject, body string }

func (f *fakeMailer) Send(_ context.Context, to, subject, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, sentMail{to, subject, body})
	return nil
}

func (f *fakeMailer) messages() []sentMail {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentMail(nil), f.sent...)
}

type fixture struct {
	h     http.Handler
	a     *Auth
	store *store.Store
	mail  *fakeMailer
}

// newFixture builds the accounts surface over a database of its own.
//
// withMail decides which of the two documented deployments is under test: one
// with a relay, where the link goes to its owner and the admin never sees it,
// and one without, where the admin is handed the link to pass on.
func newFixture(t *testing.T, withMail bool) *fixture {
	t.Helper()
	return newFixtureIn(t, withMail, "")
}

// newFixtureIn is newFixture on a deployment with a locale, which is what
// decides the language an account with none of its own is written to in.
func newFixtureIn(t *testing.T, withMail bool, locale string) *fixture {
	t.Helper()
	return newFixtureFor(t, withMail, locale, "")
}

// newFixtureFor is newFixtureIn on a deployment that has an event name, which
// every real one has, since SOIREE_EVENT_NAME carries a default. It is what
// the account mails are written around. The other two leave it empty on
// purpose, so that they go on reading the wording a deployment with no name
// falls back to.
func newFixtureFor(t *testing.T, withMail bool, locale, eventName string) *fixture {
	t.Helper()

	pool := pgtest.Pool(t)
	if _, err := migrate.Run(t.Context(), pool, nil); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(pool)

	fake := &fakeMailer{}
	var mailer Mailer
	if withMail {
		mailer = fake
	}

	a := NewAuth(AuthOptions{
		Store: st, Mailer: mailer, BaseURL: "https://soiree.example.test/",
		Locale: locale, EventName: eventName,
	})
	a.params = cheapParams
	// Synchronous, so "did this send a mail?" is answerable without sleeping
	// and the race detector has nothing in flight at the end of a test.
	a.background = func(fn func(context.Context)) { fn(context.Background()) }

	mux := http.NewServeMux()
	a.Register(mux)
	// A stand-in for the CRUD routes the REST API owns, wrapped in the
	// middleware this package exports for exactly that purpose.
	mux.Handle("/api/v1/budget-items", a.RequireWrite(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))

	return &fixture{h: mux, a: a, store: st, mail: fake}
}

// seed creates an account that can log in, skipping the invitation dance for
// the tests that are not about it.
func (f *fixture) seed(t *testing.T, email string, role store.Role, password string) store.User {
	t.Helper()
	hash, err := f.a.params.Hash(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	u, err := f.store.CreateUser(t.Context(), store.User{
		Email: email, Role: role, Status: store.StatusActive, PasswordHash: &hash,
	})
	if err != nil {
		t.Fatalf("seed %s: %v", email, err)
	}
	return u
}

// do sends a JSON request, optionally carrying a session cookie.
//
// httptest.NewRequest with the cookie copied across by hand, rather than
// httptest.NewServer with a cookie jar: the session cookie is Secure, and a
// jar attached to a plain-HTTP test server silently drops it, which shows up
// as a session bug that is not there.
func (f *fixture) do(t *testing.T, method, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()

	var r *http.Request
	if body == nil {
		r = httptest.NewRequest(method, path, nil)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		r = httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}

	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, r)
	return rec
}

// login returns the session cookie for an account.
func (f *fixture) login(t *testing.T, email, password string) *http.Cookie {
	t.Helper()
	rec := f.do(t, http.MethodPost, "/api/v1/auth/login",
		map[string]string{"email": email, "password": password}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("login as %s: status %d, body %s", email, rec.Code, rec.Body)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName && c.Value != "" {
			return c
		}
	}
	t.Fatalf("login as %s set no session cookie", email)
	return nil
}

func decodeTestBody[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
	return out
}

// tokenFromLink pulls the secret out of a set-password URL.
func tokenFromLink(t *testing.T, link string) string {
	t.Helper()
	u, err := url.Parse(link)
	if err != nil {
		t.Fatalf("parse link: %v", err)
	}
	// The token lives in the fragment, not the query string, so that it never
	// reaches a server log or a Referer header.
	if u.Fragment == "" {
		t.Fatalf("link %q carries no fragment; the token must not be in the query string", link)
	}
	_, query, ok := strings.Cut(u.Fragment, "?")
	if !ok {
		t.Fatalf("link fragment %q has no query part", u.Fragment)
	}
	values, err := url.ParseQuery(query)
	if err != nil {
		t.Fatalf("parse fragment query: %v", err)
	}
	token := values.Get("token")
	if token == "" {
		t.Fatalf("link %q carries no token", link)
	}
	return token
}

// linkFromMail pulls the set-password link out of a mail body. Every
// translation puts it on a line of its own, which is what makes this work
// without knowing which one was sent.
func linkFromMail(t *testing.T, body string) string {
	t.Helper()
	for _, field := range strings.Fields(body) {
		if strings.HasPrefix(field, "https://") {
			return field
		}
	}
	t.Fatalf("no link in mail body %q", body)
	return ""
}

func TestLoginIssuesASessionCookie(t *testing.T) {
	f := newFixture(t, false)
	f.seed(t, "ada@example.test", store.RoleAdmin, goodPassword)

	rec := f.do(t, http.MethodPost, "/api/v1/auth/login",
		map[string]string{"email": "Ada@Example.test", "password": goodPassword}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}

	// The response must describe the account without carrying the one field
	// that would make a leaked log a password database.
	if strings.Contains(rec.Body.String(), "argon2") || strings.Contains(rec.Body.String(), "passwordHash") {
		t.Errorf("login response leaks the stored hash: %s", rec.Body)
	}
	body := decodeTestBody[userDTO](t, rec)
	if body.Email != "ada@example.test" || body.Role != "admin" {
		t.Errorf("body = %+v, want ada@example.test as admin", body)
	}

	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no session cookie was set")
	}
	switch {
	case !cookie.HttpOnly:
		t.Error("session cookie is not HttpOnly, so script on the origin can read it")
	case !cookie.Secure:
		t.Error("session cookie is not Secure, so it can cross plain HTTP")
	case cookie.SameSite != http.SameSiteLaxMode:
		t.Errorf("session cookie SameSite = %v, want Lax", cookie.SameSite)
	case cookie.Path != "/":
		t.Errorf("session cookie path = %q, want /", cookie.Path)
	}
	// The cookie must be a bearer of nothing: no id, no role, no expiry the
	// client could edit. Everything about the session is server-side.
	if strings.Contains(cookie.Value, "ada") || strings.Contains(cookie.Value, "admin") {
		t.Errorf("session cookie value %q carries account data", cookie.Value)
	}

	if rec := f.do(t, http.MethodGet, "/api/v1/auth/session", nil, cookie); rec.Code != http.StatusOK {
		t.Fatalf("session lookup: status %d, body %s", rec.Code, rec.Body)
	}

	if rec := f.do(t, http.MethodPost, "/api/v1/auth/logout", nil, cookie); rec.Code != http.StatusNoContent {
		t.Fatalf("logout: status %d", rec.Code)
	}
	// Logging out is server-side: the same cookie value must stop working,
	// not merely stop being sent.
	if rec := f.do(t, http.MethodGet, "/api/v1/auth/session", nil, cookie); rec.Code != http.StatusUnauthorized {
		t.Errorf("after logout, session lookup = %d, want 401", rec.Code)
	}
}

func TestLoginRefusesEverythingItShould(t *testing.T) {
	f := newFixture(t, false)
	f.seed(t, "ada@example.test", store.RoleEditor, goodPassword)

	// Invited: the account exists, an admin created it, nobody has followed
	// the link yet. There is no password to be right about.
	if _, err := f.store.CreateUser(t.Context(), store.User{
		Email: "grace@example.test", Role: store.RoleEditor, Status: store.StatusInvited,
	}); err != nil {
		t.Fatalf("seed invited: %v", err)
	}

	disabledHash, err := f.a.params.Hash(goodPassword)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	disabled, err := f.store.CreateUser(t.Context(), store.User{
		Email: "linus@example.test", Role: store.RoleAdmin,
		Status: store.StatusActive, PasswordHash: &disabledHash,
	})
	if err != nil {
		t.Fatalf("seed disabled: %v", err)
	}
	disabled.Status = store.StatusDisabled
	if _, err := f.store.UpdateUser(t.Context(), disabled); err != nil {
		t.Fatalf("disable: %v", err)
	}

	for name, creds := range map[string][2]string{
		"wrong password":       {"ada@example.test", otherPassword},
		"empty password":       {"ada@example.test", ""},
		"unknown address":      {"nobody@example.test", goodPassword},
		"invited, no password": {"grace@example.test", goodPassword},
		"disabled account":     {"linus@example.test", goodPassword},
	} {
		t.Run(name, func(t *testing.T) {
			rec := f.do(t, http.MethodPost, "/api/v1/auth/login",
				map[string]string{"email": creds[0], "password": creds[1]}, nil)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401; body %s", rec.Code, rec.Body)
			}
			// One message for every refusal. "No such account" and "wrong
			// password" as separate answers is an address oracle.
			if got := decodeTestBody[map[string]string](t, rec)["error"]; got != "invalid_credentials" {
				t.Errorf("error = %q, want invalid_credentials for every refusal", got)
			}
			for _, c := range rec.Result().Cookies() {
				if c.Name == sessionCookieName && c.Value != "" {
					t.Error("a refused login still issued a session")
				}
			}
		})
	}
}

func TestPasswordResetCannotEnumerateAccounts(t *testing.T) {
	f := newFixture(t, true)
	f.seed(t, "ada@example.test", store.RoleEditor, goodPassword)

	known := f.do(t, http.MethodPost, "/api/v1/auth/password-reset",
		map[string]string{"email": "ada@example.test"}, nil)
	unknown := f.do(t, http.MethodPost, "/api/v1/auth/password-reset",
		map[string]string{"email": "nobody@example.test"}, nil)

	if known.Code != http.StatusAccepted || unknown.Code != http.StatusAccepted {
		t.Fatalf("status = %d and %d, want 202 for both", known.Code, unknown.Code)
	}
	if known.Body.String() != unknown.Body.String() {
		t.Errorf("bodies differ:\n known:   %s unknown: %s", known.Body, unknown.Body)
	}
	// Headers too: a Set-Cookie, a Retry-After, even a different Content-Length
	// answers the question the body refuses to.
	if !maps.EqualFunc(known.Header(), unknown.Header(), func(a, b []string) bool {
		return strings.Join(a, ",") == strings.Join(b, ",")
	}) {
		t.Errorf("headers differ:\n known:   %v\n unknown: %v", known.Header(), unknown.Header())
	}

	// The mail, on the other hand, must only exist for the real account — and
	// it is sent off the response path, which is what keeps the *timing* from
	// saying what the body does not.
	sent := f.mail.messages()
	if len(sent) != 1 {
		t.Fatalf("%d mails sent, want exactly 1 (for the account that exists)", len(sent))
	}
	if sent[0].to != "ada@example.test" {
		t.Errorf("mail went to %q", sent[0].to)
	}
	if !strings.Contains(sent[0].body, "https://soiree.example.test/#/set-password?token=") {
		t.Errorf("mail body carries no set-password link: %q", sent[0].body)
	}
}

func TestPasswordResetIgnoresDisabledAccounts(t *testing.T) {
	f := newFixture(t, true)
	u := f.seed(t, "linus@example.test", store.RoleEditor, goodPassword)
	u.Status = store.StatusDisabled
	if _, err := f.store.UpdateUser(t.Context(), u); err != nil {
		t.Fatalf("disable: %v", err)
	}

	rec := f.do(t, http.MethodPost, "/api/v1/auth/password-reset",
		map[string]string{"email": "linus@example.test"}, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 regardless", rec.Code)
	}
	// Re-enabling somebody is an admin's decision. A reset link that routes
	// around it makes disabling an account a suggestion.
	if n := len(f.mail.messages()); n != 0 {
		t.Errorf("%d mails sent to a disabled account, want 0", n)
	}
}

func TestPasswordResetWithoutMailLeavesTheHandedLinkAlone(t *testing.T) {
	f := newFixture(t, false) // no SMTP: the admin is handed the link
	f.seed(t, "ada@example.test", store.RoleAdmin, goodPassword)
	admin := f.login(t, "ada@example.test", goodPassword)

	rec := f.do(t, http.MethodPost, "/api/v1/users",
		map[string]string{"email": "grace@example.test", "role": "editor"}, admin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create user: status %d, body %s", rec.Code, rec.Body)
	}
	token := tokenFromLink(t, decodeTestBody[createUserResponse](t, rec).SetPasswordURL)

	// Anyone who knows the address can send this, and without a relay the link
	// it issues goes nowhere. Issuing one anyway would consume the invitation
	// the admin is in the middle of passing on by hand.
	reset := f.do(t, http.MethodPost, "/api/v1/auth/password-reset",
		map[string]string{"email": "grace@example.test"}, nil)
	if reset.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 regardless", reset.Code)
	}

	rec = f.do(t, http.MethodPost, "/api/v1/auth/set-password",
		map[string]string{"token": token, "password": otherPassword}, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("the invitation no longer works: status %d, body %s", rec.Code, rec.Body)
	}
	f.login(t, "grace@example.test", otherPassword)
}

func TestPasswordResetIsRateLimitedPerAccount(t *testing.T) {
	f := newFixture(t, true)
	f.seed(t, "ada@example.test", store.RoleEditor, goodPassword)

	for range resetAcctBurst {
		rec := f.do(t, http.MethodPost, "/api/v1/auth/password-reset",
			map[string]string{"email": "ada@example.test"}, nil)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want 202", rec.Code)
		}
	}
	sent := f.mail.messages()
	if len(sent) != resetAcctBurst {
		t.Fatalf("%d mails sent for %d requests, want one each", len(sent), resetAcctBurst)
	}
	token := tokenFromLink(t, linkFromMail(t, sent[len(sent)-1].body))

	// Past the burst nothing happens at all: no mail through the deployment's
	// relay, and, because issuing supersedes, the last link stays the live
	// one. The answer is the same 202 either way, so the caller cannot tell
	// which side of the limit they are on.
	rec := f.do(t, http.MethodPost, "/api/v1/auth/password-reset",
		map[string]string{"email": "ada@example.test"}, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 past the limit too", rec.Code)
	}
	if n := len(f.mail.messages()); n != resetAcctBurst {
		t.Errorf("%d mails sent, want %d: the request past the burst sent one anyway", n, resetAcctBurst)
	}

	rec = f.do(t, http.MethodPost, "/api/v1/auth/set-password",
		map[string]string{"token": token, "password": otherPassword}, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("the last mailed link no longer works: status %d, body %s", rec.Code, rec.Body)
	}

	// An admin re-inviting is not charged to this bucket: somebody has to be
	// able to get a locked-out account back in.
	f.seed(t, "linus@example.test", store.RoleAdmin, goodPassword)
	session := f.login(t, "linus@example.test", goodPassword)
	u, err := f.store.UserByEmail(t.Context(), "ada@example.test")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	rec = f.do(t, http.MethodPost, "/api/v1/users/"+u.ID.String()+"/invite", nil, session)
	if rec.Code != http.StatusOK {
		t.Fatalf("re-invite: status %d, body %s", rec.Code, rec.Body)
	}
	if n := len(f.mail.messages()); n != resetAcctBurst+1 {
		t.Errorf("%d mails sent, want %d: an admin re-invite was refused by the reset bucket", n, resetAcctBurst+1)
	}
}

func TestInvitationSetsAPasswordExactlyOnce(t *testing.T) {
	f := newFixture(t, false) // no SMTP: the admin is handed the link
	f.seed(t, "ada@example.test", store.RoleAdmin, goodPassword)
	admin := f.login(t, "ada@example.test", goodPassword)

	rec := f.do(t, http.MethodPost, "/api/v1/users",
		map[string]string{"email": "grace@example.test", "role": "editor"}, admin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create user: status %d, body %s", rec.Code, rec.Body)
	}
	created := decodeTestBody[createUserResponse](t, rec)
	if created.User.Status != "invited" {
		t.Errorf("new account status = %q, want invited: an admin never picks the password", created.User.Status)
	}
	if created.MailSent {
		t.Error("mailSent is true on a deployment with no SMTP")
	}
	if created.SetPasswordURL == "" {
		t.Fatal("no SMTP and no link in the response: the admin has no way to pass one on")
	}
	token := tokenFromLink(t, created.SetPasswordURL)

	// Too short is refused before anything is consumed.
	rec = f.do(t, http.MethodPost, "/api/v1/auth/set-password",
		map[string]string{"token": token, "password": "short"}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("weak password: status %d, want 400", rec.Code)
	}
	if got := decodeTestBody[map[string]string](t, rec)["error"]; got != "weak_password" {
		t.Errorf("error = %q, want weak_password", got)
	}

	rec = f.do(t, http.MethodPost, "/api/v1/auth/set-password",
		map[string]string{"token": token, "password": otherPassword}, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("set password: status %d, body %s", rec.Code, rec.Body)
	}

	// Following the link is what activates the account, and the password that
	// results is the one the person chose.
	cookie := f.login(t, "grace@example.test", otherPassword)
	who := decodeTestBody[userDTO](t, f.do(t, http.MethodGet, "/api/v1/auth/session", nil, cookie))
	if who.Status != "active" || who.Role != "editor" {
		t.Errorf("account = %q/%q, want active/editor", who.Status, who.Role)
	}

	// Replay. A link that still works after use is a link that works for
	// whoever else can read the mailbox.
	rec = f.do(t, http.MethodPost, "/api/v1/auth/set-password",
		map[string]string{"token": token, "password": "yet another password"}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("replayed link: status %d, want 400", rec.Code)
	}
	// Identical to an invented token, so a guess cannot be confirmed as having
	// once been real.
	invented := f.do(t, http.MethodPost, "/api/v1/auth/set-password",
		map[string]string{"token": "not-a-real-token", "password": "yet another password"}, nil)
	if replayed, made := rec.Body.String(), invented.Body.String(); replayed != made {
		t.Errorf("a used link answers %q and an invented one %q; they must be indistinguishable", replayed, made)
	}

	// And the password that was set is still the one that works.
	f.login(t, "grace@example.test", otherPassword)
}

func TestCreateUserMailsTheLinkAndDoesNotReturnIt(t *testing.T) {
	f := newFixture(t, true)
	f.seed(t, "ada@example.test", store.RoleAdmin, goodPassword)
	admin := f.login(t, "ada@example.test", goodPassword)

	rec := f.do(t, http.MethodPost, "/api/v1/users",
		map[string]string{"email": "linus@example.test", "role": "viewer"}, admin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	created := decodeTestBody[createUserResponse](t, rec)
	// With a relay configured the link goes to the person it belongs to and
	// nowhere else. An admin who never sees it cannot use it.
	if created.SetPasswordURL != "" {
		t.Error("the set-password link came back to the admin even though mail is configured")
	}
	if !created.MailSent {
		t.Error("mailSent is false with a relay configured")
	}

	sent := f.mail.messages()
	if len(sent) != 1 || sent[0].to != "linus@example.test" {
		t.Fatalf("mails = %+v, want one to linus@example.test", sent)
	}

	// Creating the same address twice is a conflict, not a second account.
	rec = f.do(t, http.MethodPost, "/api/v1/users",
		map[string]string{"email": "LINUS@example.test", "role": "admin"}, admin)
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate address: status %d, want 409", rec.Code)
	}
}

// A mail the relay drops leaves an invited person with nothing and an admin
// with no way to help: re-inviting sends the same mail down the same pipe, and
// no route here hands the link over. So an admin can ask for the link instead
// of the mail, and the asking is logged.
func TestAnAdminCanAskForTheLinkInsteadOfTheMail(t *testing.T) {
	f := newFixture(t, true) // a relay is configured, so the link is normally never returned
	f.seed(t, "ada@example.test", store.RoleAdmin, goodPassword)
	admin := f.login(t, "ada@example.test", goodPassword)

	rec := f.do(t, http.MethodPost, "/api/v1/users",
		map[string]string{"email": "grace@example.test", "role": "editor", "deliver": "link"}, admin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	created := decodeTestBody[createUserResponse](t, rec)
	if created.SetPasswordURL == "" {
		t.Fatalf("no link came back for an admin who asked to carry it: %s", rec.Body)
	}
	if created.MailSent {
		t.Error("mailSent is true although the mail was the thing being skipped")
	}
	if n := len(f.mail.messages()); n != 0 {
		t.Errorf("%d mails sent for a link the admin asked to pass on by hand", n)
	}

	// A real link, not a decoration: it is the account's one live token.
	rec = f.do(t, http.MethodPost, "/api/v1/auth/set-password",
		map[string]string{"token": tokenFromLink(t, created.SetPasswordURL), "password": goodPassword}, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("the link the admin was given does not work: %d %s", rec.Code, rec.Body)
	}
}

// Asking for the link is not the only way to be handed one. A deployment
// without a relay hands one back on every invitation, and it is the one where
// the refusal above cannot fire, so the account on the other end may be one
// somebody is using. Whoever asked is written down there too.
func TestALinkHandedOverWithoutARelayIsLoggedToo(t *testing.T) {
	f := newFixture(t, false) // no SMTP: every link comes back to the admin
	ada := f.seed(t, "ada@example.test", store.RoleAdmin, goodPassword)
	linus := f.seed(t, "linus@example.test", store.RoleEditor, goodPassword)
	admin := f.login(t, "ada@example.test", goodPassword)

	var logged bytes.Buffer
	f.a.log = slog.New(slog.NewJSONHandler(&logged, nil))

	// No body, so no delivery was asked for: the ordinary invitation, for an
	// account that already has a password.
	rec := f.do(t, http.MethodPost, "/api/v1/users/"+linus.ID.String()+"/invite", nil, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("invite: status %d, body %s", rec.Code, rec.Body)
	}
	link := decodeTestBody[createUserResponse](t, rec).SetPasswordURL
	if link == "" {
		t.Fatalf("no link came back on a deployment without a relay: %s", rec.Body)
	}

	out := logged.String()
	if !strings.Contains(out, "account link issued to admin") ||
		!strings.Contains(out, linus.ID.String()) || !strings.Contains(out, ada.ID.String()) {
		t.Errorf("the log does not say who took a link out and for whom: %s", out)
	}
	// Who asked and for whom, and nothing that opens the account.
	if strings.Contains(out, tokenFromLink(t, link)) {
		t.Error("the link's token was logged with it")
	}
}

// The half of the rule that is worth keeping. An account somebody is already
// using has a password and a history to impersonate, so its link goes to its
// owner and nowhere else, and an admin cannot take it over in one click.
func TestALinkToPassOnIsRefusedForAnAccountInUse(t *testing.T) {
	f := newFixture(t, true)
	f.seed(t, "ada@example.test", store.RoleAdmin, goodPassword)
	grace := f.seed(t, "grace@example.test", store.RoleEditor, goodPassword)
	admin := f.login(t, "ada@example.test", goodPassword)

	rec := f.do(t, http.MethodPost, "/api/v1/users/"+grace.ID.String()+"/invite",
		map[string]string{"deliver": "link"}, admin)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "account_active") {
		t.Fatalf("status = %d, body %s; want 409 account_active", rec.Code, rec.Body)
	}
	if n := len(f.mail.messages()); n != 0 {
		t.Errorf("a refused request sent %d mails", n)
	}

	// The same route still mails the account itself a reset, as it always did.
	rec = f.do(t, http.MethodPost, "/api/v1/users/"+grace.ID.String()+"/invite", nil, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("re-invite: status %d, body %s", rec.Code, rec.Body)
	}
	if sent := decodeTestBody[createUserResponse](t, rec); sent.SetPasswordURL != "" {
		t.Error("the ordinary re-invite handed the admin the link")
	}
	if n := len(f.mail.messages()); n != 1 {
		t.Errorf("%d mails sent, want 1", n)
	}
}

// And it stays refused for an account somebody has parked back at invited. A
// status is an admin's to write; whether a password was ever set is not, and
// that is the fact the refusal turns on.
func TestALinkToPassOnIsRefusedForAnAccountParkedBackAtInvited(t *testing.T) {
	f := newFixture(t, true)
	f.seed(t, "ada@example.test", store.RoleAdmin, goodPassword)
	grace := f.seed(t, "grace@example.test", store.RoleEditor, goodPassword)
	admin := f.login(t, "ada@example.test", goodPassword)

	rec := f.do(t, http.MethodPatch, "/api/v1/users/"+grace.ID.String(),
		map[string]any{"revision": grace.Revision, "status": "invited"}, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("parking the account at invited: %d %s", rec.Code, rec.Body)
	}

	rec = f.do(t, http.MethodPost, "/api/v1/users/"+grace.ID.String()+"/invite",
		map[string]string{"deliver": "link"}, admin)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "account_active") {
		t.Fatalf("status = %d, body %s; want 409 account_active", rec.Code, rec.Body)
	}
	if n := len(f.mail.messages()); n != 0 {
		t.Errorf("a refused request sent %d mails", n)
	}
}

// A delivery nobody implements is refused before the account it names exists,
// or an admin correcting a typo finds the address already taken by the request
// that was refused.
func TestAnUnknownDeliveryLeavesNoAccountBehind(t *testing.T) {
	f := newFixture(t, true)
	f.seed(t, "ada@example.test", store.RoleAdmin, goodPassword)
	admin := f.login(t, "ada@example.test", goodPassword)

	rec := f.do(t, http.MethodPost, "/api/v1/users",
		map[string]string{"email": "linus@example.test", "role": "viewer", "deliver": "carrier pigeon"}, admin)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_deliver") {
		t.Fatalf("status = %d, body %s; want 400 invalid_deliver", rec.Code, rec.Body)
	}
	if _, err := f.store.UserByEmail(t.Context(), "linus@example.test"); err == nil {
		t.Error("a refused request created the account anyway")
	}
	if n := len(f.mail.messages()); n != 0 {
		t.Errorf("a refused request sent %d mails", n)
	}
}

// What an invited person has to go on is this one mail, and it asks them to
// type a password into a site they may never have heard of. So it names the
// event it is an invitation to, and it names somewhere to go when the link has
// gone stale: not the link, which works once and is gone in a day.
func TestAnInvitationSaysWhatItIsAnInvitationTo(t *testing.T) {
	const event = "A Celebration"
	f := newFixtureFor(t, true, "en-US", event)
	f.seed(t, "ada@example.test", store.RoleAdmin, goodPassword)
	admin := f.login(t, "ada@example.test", goodPassword)

	rec := f.do(t, http.MethodPost, "/api/v1/users",
		map[string]string{"email": "grace@example.test", "role": "editor"}, admin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	sent := f.mail.messages()
	if len(sent) != 1 {
		t.Fatalf("mails = %d, want 1", len(sent))
	}
	if !strings.Contains(sent[0].subject, event) {
		t.Errorf("the subject does not name the event: %q", sent[0].subject)
	}
	if !strings.Contains(sent[0].body, event) {
		t.Errorf("the body does not name the event: %q", sent[0].body)
	}
	// The deployment's own address, somewhere other than inside the one link
	// the mail carries.
	rest := strings.ReplaceAll(sent[0].body, linkFromMail(t, sent[0].body), "")
	if !strings.Contains(rest, "https://soiree.example.test") {
		t.Errorf("an expired link leaves this invitation with nothing to offer: %q", sent[0].body)
	}
}

func TestRolesAreEnforcedOnTheServer(t *testing.T) {
	f := newFixture(t, false)
	f.seed(t, "ada@example.test", store.RoleAdmin, goodPassword)
	f.seed(t, "grace@example.test", store.RoleEditor, goodPassword)
	f.seed(t, "linus@example.test", store.RoleViewer, goodPassword)

	admin := f.login(t, "ada@example.test", goodPassword)
	editor := f.login(t, "grace@example.test", goodPassword)
	viewer := f.login(t, "linus@example.test", goodPassword)

	// Administration is admin-only. An editor is not a lesser admin.
	for name, tc := range map[string]struct {
		cookie *http.Cookie
		want   int
	}{
		"nobody": {nil, http.StatusUnauthorized},
		"viewer": {viewer, http.StatusForbidden},
		"editor": {editor, http.StatusForbidden},
		"admin":  {admin, http.StatusOK},
	} {
		t.Run("list users as "+name, func(t *testing.T) {
			rec := f.do(t, http.MethodGet, "/api/v1/users", nil, tc.cookie)
			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d; body %s", rec.Code, tc.want, rec.Body)
			}
		})
		t.Run("create user as "+name, func(t *testing.T) {
			rec := f.do(t, http.MethodPost, "/api/v1/users",
				map[string]string{"email": name + "-invented@example.test", "role": "viewer"}, tc.cookie)
			want := tc.want
			if want == http.StatusOK {
				want = http.StatusCreated
			}
			if rec.Code != want {
				t.Errorf("status = %d, want %d; body %s", rec.Code, want, rec.Body)
			}
		})
	}

	// And a viewer is read-only everywhere, which is a property of the method
	// rather than of the route — the check the REST API's CRUD subtree wraps
	// itself in.
	for name, tc := range map[string]struct {
		cookie       *http.Cookie
		read, mutate int
	}{
		"nobody": {nil, http.StatusUnauthorized, http.StatusUnauthorized},
		"viewer": {viewer, http.StatusOK, http.StatusForbidden},
		"editor": {editor, http.StatusOK, http.StatusOK},
		"admin":  {admin, http.StatusOK, http.StatusOK},
	} {
		t.Run("write as "+name, func(t *testing.T) {
			if rec := f.do(t, http.MethodGet, "/api/v1/budget-items", nil, tc.cookie); rec.Code != tc.read {
				t.Errorf("GET = %d, want %d", rec.Code, tc.read)
			}
			for _, method := range []string{http.MethodPost, http.MethodPatch, http.MethodDelete, http.MethodPut} {
				if rec := f.do(t, method, "/api/v1/budget-items", nil, tc.cookie); rec.Code != tc.mutate {
					t.Errorf("%s = %d, want %d", method, rec.Code, tc.mutate)
				}
			}
		})
	}
}

func TestRoleChangeTakesEffectOnTheNextRequest(t *testing.T) {
	f := newFixture(t, false)
	f.seed(t, "ada@example.test", store.RoleAdmin, goodPassword)
	grace := f.seed(t, "grace@example.test", store.RoleEditor, goodPassword)

	admin := f.login(t, "ada@example.test", goodPassword)
	editor := f.login(t, "grace@example.test", goodPassword)

	if rec := f.do(t, http.MethodPost, "/api/v1/budget-items", nil, editor); rec.Code != http.StatusOK {
		t.Fatalf("editor could not write: %d", rec.Code)
	}

	rec := f.do(t, http.MethodPatch, "/api/v1/users/"+grace.ID.String(),
		map[string]any{"revision": grace.Revision, "role": "viewer"}, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("demote: status %d, body %s", rec.Code, rec.Body)
	}

	// The session survives; the role does not. Authorisation is read from the
	// row on every request rather than baked into the session at login, which
	// is the difference between a demotion and a request to log out.
	if rec := f.do(t, http.MethodPost, "/api/v1/budget-items", nil, editor); rec.Code != http.StatusForbidden {
		t.Errorf("a demoted editor can still write: %d", rec.Code)
	}
	if rec := f.do(t, http.MethodGet, "/api/v1/budget-items", nil, editor); rec.Code != http.StatusOK {
		t.Errorf("a demoted editor cannot read: %d", rec.Code)
	}

	// A stale revision is refused rather than overwriting whoever got there
	// first, and comes back with the row as it now stands.
	rec = f.do(t, http.MethodPatch, "/api/v1/users/"+grace.ID.String(),
		map[string]any{"revision": grace.Revision, "role": "admin"}, admin)
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale write: status %d, want 409", rec.Code)
	}
	conflict := decodeTestBody[map[string]any](t, rec)
	if conflict["error"] != "stale_revision" || conflict["current"] == nil {
		t.Errorf("409 body = %v, want stale_revision carrying the current row", conflict)
	}
}

func TestDisablingAnAccountEndsItsSessions(t *testing.T) {
	f := newFixture(t, false)
	f.seed(t, "ada@example.test", store.RoleAdmin, goodPassword)
	linus := f.seed(t, "linus@example.test", store.RoleEditor, goodPassword)

	admin := f.login(t, "ada@example.test", goodPassword)
	victim := f.login(t, "linus@example.test", goodPassword)

	rec := f.do(t, http.MethodPatch, "/api/v1/users/"+linus.ID.String(),
		map[string]any{"revision": linus.Revision, "status": "disabled"}, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("disable: status %d, body %s", rec.Code, rec.Body)
	}
	if rec := f.do(t, http.MethodGet, "/api/v1/auth/session", nil, victim); rec.Code != http.StatusUnauthorized {
		t.Errorf("a disabled account's session still works: %d", rec.Code)
	}
	// And the password that worked a moment ago no longer gets a new one.
	rec = f.do(t, http.MethodPost, "/api/v1/auth/login",
		map[string]string{"email": "linus@example.test", "password": goodPassword}, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("a disabled account logged back in: %d", rec.Code)
	}
}

// A passkey registered from a borrowed session outlives every remedy this
// application documents: a fresh set-password link ends the sessions and
// leaves the credential where it is, and each login with it mints a new
// session, so the thirty-day cap never bites either. Revoking is what takes it
// away, and it needs no list of anybody's passkeys to do it.
func TestRevokingCredentialsTakesAwayEveryWayIn(t *testing.T) {
	f := newFixture(t, false)
	f.seed(t, "ada@example.test", store.RoleAdmin, goodPassword)
	linus := f.seed(t, "linus@example.test", store.RoleEditor, goodPassword)

	admin := f.login(t, "ada@example.test", goodPassword)
	victim := f.login(t, "linus@example.test", goodPassword)

	// What an intruder leaves behind. The bytes are synthetic, which does no
	// harm here: nothing on this path looks inside them.
	if _, err := f.store.CreatePasskeyCredential(t.Context(), store.PasskeyCredential{
		UserID:       linus.ID,
		CredentialID: []byte("cred-from-a-borrowed-session"),
		PublicKey:    []byte("cose-public-key"),
		Label:        "a phone nobody recognises",
	}); err != nil {
		t.Fatalf("register a passkey: %v", err)
	}

	// And a link that is still outstanding, which the same act should spend.
	rec := f.do(t, http.MethodPost, "/api/v1/users/"+linus.ID.String()+"/invite", nil, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("invite: status %d, body %s", rec.Code, rec.Body)
	}
	token := tokenFromLink(t, decodeTestBody[createUserResponse](t, rec).SetPasswordURL)

	rec = f.do(t, http.MethodPost, "/api/v1/users/"+linus.ID.String()+"/revoke-credentials", nil, admin)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke: status %d, body %s", rec.Code, rec.Body)
	}
	// Nothing about what was found, or this route would be the admin view of
	// somebody's passkeys that this application deliberately does not have.
	if rec.Body.Len() != 0 {
		t.Errorf("the answer carries a body: %s", rec.Body)
	}

	if rec := f.do(t, http.MethodGet, "/api/v1/auth/session", nil, victim); rec.Code != http.StatusUnauthorized {
		t.Errorf("a revoked session still works: %d", rec.Code)
	}
	rows, err := f.store.PasskeyCredentials(t.Context(), linus.ID)
	if err != nil {
		t.Fatalf("read passkeys: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("passkeys left = %d, want none", len(rows))
	}
	rec = f.do(t, http.MethodPost, "/api/v1/auth/set-password",
		map[string]string{"token": token, "password": otherPassword}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("the outstanding link still redeems: status %d, want 400", rec.Code)
	}

	// The password is untouched: this hands an account back to its owner
	// rather than shutting it, which is what disabling is for. f.login fails
	// the test if it no longer works.
	f.login(t, "linus@example.test", goodPassword)
}

// Revoking is an admin's, on somebody else's account. One's own credentials
// are on one's own account screen, where the passkey list says which device is
// which and the row that does not belong can go without the rest; doing it to
// oneself here would take all of them and sign the admin out of the screen
// they are standing on.
func TestRevokingCredentialsIsAdminOnlyAndNotOnOneself(t *testing.T) {
	f := newFixture(t, false)
	ada := f.seed(t, "ada@example.test", store.RoleAdmin, goodPassword)
	grace := f.seed(t, "grace@example.test", store.RoleEditor, goodPassword)
	f.seed(t, "linus@example.test", store.RoleViewer, goodPassword)

	admin := f.login(t, "ada@example.test", goodPassword)
	editor := f.login(t, "grace@example.test", goodPassword)
	viewer := f.login(t, "linus@example.test", goodPassword)

	path := "/api/v1/users/" + grace.ID.String() + "/revoke-credentials"
	for name, tc := range map[string]struct {
		cookie *http.Cookie
		want   int
	}{
		"nobody": {nil, http.StatusUnauthorized},
		"viewer": {viewer, http.StatusForbidden},
		"editor": {editor, http.StatusForbidden},
	} {
		t.Run("as "+name, func(t *testing.T) {
			if rec := f.do(t, http.MethodPost, path, nil, tc.cookie); rec.Code != tc.want {
				t.Errorf("status = %d, want %d; body %s", rec.Code, tc.want, rec.Body)
			}
		})
	}

	rec := f.do(t, http.MethodPost, "/api/v1/users/"+ada.ID.String()+"/revoke-credentials", nil, admin)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("revoking one's own: status %d, want 400; body %s", rec.Code, rec.Body)
	}
}

// The store records who revoked, and the session making the request is the
// only place that answer exists. This route is not under withActor, the
// accounts surface being mounted on the main mux, so the handler is what
// carries it and one that stopped would leave an entry naming nobody.
func TestRevokingCredentialsRecordsTheAdminWhoDidIt(t *testing.T) {
	f := newFixture(t, false)
	ada := f.seed(t, "ada@example.test", store.RoleAdmin, goodPassword)
	grace := f.seed(t, "grace@example.test", store.RoleEditor, goodPassword)
	admin := f.login(t, "ada@example.test", goodPassword)

	rec := f.do(t, http.MethodPost, "/api/v1/users/"+grace.ID.String()+"/revoke-credentials", nil, admin)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke: status %d, body %s", rec.Code, rec.Body)
	}

	entries, err := f.store.ChangeHistory(t.Context(), store.EntityUsers, grace.ID, store.HistoryPage{})
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("the revocation left nothing in the change log")
	}
	entry := entries[0]
	if _, recorded := entry.Changes["credentials"]; !recorded {
		t.Fatalf("the newest entry is not the revocation: %v", entry.Changes)
	}
	if entry.ActorID == nil || *entry.ActorID != ada.ID {
		t.Errorf("actor = %v, want the admin who called it", entry.ActorID)
	}
}

func TestAnAdminCannotLockThemselvesOut(t *testing.T) {
	f := newFixture(t, false)
	ada := f.seed(t, "ada@example.test", store.RoleAdmin, goodPassword)
	admin := f.login(t, "ada@example.test", goodPassword)

	// The only way back from a self-demotion is another admin, and a
	// deployment with one admin who has just made themselves a viewer has no
	// way back in at all.
	for name, patch := range map[string]map[string]any{
		"demotion": {"revision": ada.Revision, "role": "viewer"},
		"disable":  {"revision": ada.Revision, "status": "disabled"},
	} {
		t.Run(name, func(t *testing.T) {
			rec := f.do(t, http.MethodPatch, "/api/v1/users/"+ada.ID.String(), patch, admin)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body)
			}
		})
	}
	rec := f.do(t, http.MethodDelete, "/api/v1/users/"+ada.ID.String()+"?revision=1", nil, admin)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("self-delete: status %d, want 400", rec.Code)
	}
}

func TestLoginIsRateLimitedPerAccount(t *testing.T) {
	f := newFixture(t, false)
	f.seed(t, "ada@example.test", store.RoleAdmin, goodPassword)

	// Take the per-address limiter out of the way, or it fires first and this
	// passes without ever exercising the per-account one. Every request below
	// comes from the same test IP.
	f.a.loginIP = newLimiter(10_000, time.Minute)

	var limited bool
	for range loginAcctBurst + 2 {
		rec := f.do(t, http.MethodPost, "/api/v1/auth/login",
			map[string]string{"email": "ada@example.test", "password": otherPassword}, nil)
		if rec.Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatalf("a run of %d wrong passwords was never rate-limited", loginAcctBurst+2)
	}

	// Keyed on the submitted address whether or not it names an account: a
	// bucket that only exists for real accounts makes 429-versus-401 the same
	// disclosure the constant-time work avoids.
	var limitedUnknown bool
	for range loginAcctBurst + 2 {
		rec := f.do(t, http.MethodPost, "/api/v1/auth/login",
			map[string]string{"email": "nobody@example.test", "password": otherPassword}, nil)
		if rec.Code == http.StatusTooManyRequests {
			limitedUnknown = true
			break
		}
	}
	if !limitedUnknown {
		t.Error("an unknown address is not rate-limited, which tells an attacker it is unknown")
	}
}

func TestAccountsSurfaceIsWiredIntoTheServer(t *testing.T) {
	f := newFixture(t, false)

	srv, err := New(config.Config{Currency: "EUR", Locale: "en-US"}, web.FS())
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	h := srv.WithAuth(f.a).Handler()

	// The routes have to be reachable through the real handler chain, not just
	// through the mux the other tests build by hand.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/users", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/v1/users = %d, want 401 from the wired server", rec.Code)
	}

	// And a server with no database still serves the shell, which is what a
	// bare `docker run` and the image smoke test do.
	bare, err := New(config.Config{Currency: "EUR", Locale: "en-US"}, web.FS())
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	rec = httptest.NewRecorder()
	bare.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("a server with no accounts surface does not serve the shell: %d", rec.Code)
	}
}

func TestSessionSlidesForwardWithUse(t *testing.T) {
	f := newFixture(t, false)
	user := f.seed(t, "ada@example.test", store.RoleAdmin, goodPassword)
	cookie := f.login(t, "ada@example.test", goodPassword)

	// Inside the touch interval nothing is rewritten: sliding the expiry on
	// every request would be a database write per request to record something
	// measured in days.
	rec := f.do(t, http.MethodGet, "/api/v1/auth/session", nil, cookie)
	if len(rec.Result().Cookies()) != 0 {
		t.Errorf("a request inside the touch interval re-issued the cookie: %v", rec.Result().Cookies())
	}

	before, err := f.store.Sessions(t.Context(), user.ID)
	if err != nil || len(before) != 1 {
		t.Fatalf("sessions = %v, %v; want exactly one", before, err)
	}

	// An hour later the window slides — in the browser as well as in the
	// database. Moving only the row would leave the cookie expiring at the
	// moment it was issued, so somebody using this daily would still be logged
	// out on the seventh day and no session could reach the absolute cap.
	base := time.Now()
	f.a.now = func() time.Time { return base.Add(2 * sessionTouchInterval) }

	rec = f.do(t, http.MethodGet, "/api/v1/auth/session", nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var refreshed *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName {
			refreshed = c
		}
	}
	if refreshed == nil {
		t.Fatal("the session cookie was not re-issued, so its expiry never slides")
	}
	if refreshed.Value != cookie.Value {
		t.Error("sliding the window changed the session token; it should extend, not rotate")
	}
	if !refreshed.Expires.After(cookie.Expires) {
		t.Errorf("refreshed Expires = %v, want later than the original %v", refreshed.Expires, cookie.Expires)
	}
	if refreshed.MaxAge != int(sessionIdleTimeout.Seconds()) {
		t.Errorf("refreshed MaxAge = %d, want %d", refreshed.MaxAge, int(sessionIdleTimeout.Seconds()))
	}

	after, err := f.store.Sessions(t.Context(), user.ID)
	if err != nil || len(after) != 1 {
		t.Fatalf("sessions = %v, %v; want exactly one", after, err)
	}
	if !after[0].ExpiresAt.After(before[0].ExpiresAt) {
		t.Errorf("stored expiry = %v, want later than %v", after[0].ExpiresAt, before[0].ExpiresAt)
	}
}

func TestLoginRehashesAPasswordStoredAtWeakerParameters(t *testing.T) {
	f := newFixture(t, false)

	// An account whose password was hashed before the cost parameters were
	// raised. The fixture's policy is cheapParams at m=1024; this is below it.
	weak := auth.Params{Memory: 512, Time: 1, Threads: 1, KeyLen: 32, SaltLen: 16}
	stale, err := weak.Hash(goodPassword)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	user, err := f.store.CreateUser(t.Context(), store.User{
		Email: "grace@example.test", Role: store.RoleEditor,
		Status: store.StatusActive, PasswordHash: &stale,
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Raising the floor must not lock anybody out: the old password still
	// works, and logging in is the one moment the plaintext is in hand and the
	// stored encoding can be brought up to policy without asking anybody to do
	// anything.
	f.login(t, "grace@example.test", goodPassword)

	reread, err := f.store.User(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("read user: %v", err)
	}
	if reread.PasswordHash == nil || *reread.PasswordHash == stale {
		t.Fatal("the stored hash was not rewritten at the current parameters")
	}
	if !strings.Contains(*reread.PasswordHash, "m=1024,t=1,p=1") {
		t.Errorf("stored hash = %q, want it re-encoded at the current parameters", *reread.PasswordHash)
	}
	if reread.Revision <= user.Revision {
		t.Errorf("revision = %d, want it past %d", reread.Revision, user.Revision)
	}

	// The rewrite has to be of the same password, which is the assertion that
	// catches a re-hash that stored something else.
	f.login(t, "grace@example.test", goodPassword)
	rec := f.do(t, http.MethodPost, "/api/v1/auth/login",
		map[string]string{"email": "grace@example.test", "password": otherPassword}, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("the wrong password works after a re-hash: %d", rec.Code)
	}

	// And a hash already at policy is left alone.
	before := *reread.PasswordHash
	f.login(t, "grace@example.test", goodPassword)
	again, err := f.store.User(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("read user: %v", err)
	}
	if *again.PasswordHash != before {
		t.Error("a hash already at policy was re-hashed again on the next login")
	}
}

func TestSetPasswordDoesNotChargeForAWeakPassword(t *testing.T) {
	f := newFixture(t, false)
	f.seed(t, "ada@example.test", store.RoleAdmin, goodPassword)
	admin := f.login(t, "ada@example.test", goodPassword)

	rec := f.do(t, http.MethodPost, "/api/v1/users",
		map[string]string{"email": "linus@example.test", "role": "viewer"}, admin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create user: status %d, body %s", rec.Code, rec.Body)
	}
	token := tokenFromLink(t, decodeTestBody[createUserResponse](t, rec).SetPasswordURL)

	// Somebody choosing a password that turns out to be too short must not
	// spend the allowance the redemption still needs.
	for range redeemIPBurst + 5 {
		rec := f.do(t, http.MethodPost, "/api/v1/auth/set-password",
			map[string]string{"token": token, "password": "short"}, nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 for a weak password", rec.Code)
		}
	}
	rec = f.do(t, http.MethodPost, "/api/v1/auth/set-password",
		map[string]string{"token": token, "password": otherPassword}, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("after a run of weak passwords, setting a good one = %d, body %s", rec.Code, rec.Body)
	}
	f.login(t, "linus@example.test", otherPassword)
}

// A floor is a number and a way of counting it, and the way of counting is the
// half that drifts unnoticed. This route is where everybody who is not the
// operator meets the floor, so what it refuses has to be what the environment
// refuses at startup.
func TestTheSetPasswordFloorCountsCharactersNotBytes(t *testing.T) {
	// Four characters in a script that takes three bytes each. Checked rather
	// than assumed: edited to something else, the case would stop telling the
	// two measures apart and would pass whichever one the code used.
	const twelveBytes = "秘密の鍵"
	if len(twelveBytes) != config.MinPasswordLen ||
		utf8.RuneCountInString(twelveBytes) >= config.MinPasswordLen {
		t.Fatalf("%q is %d bytes and %d characters; the case no longer tells the two apart",
			twelveBytes, len(twelveBytes), utf8.RuneCountInString(twelveBytes))
	}

	for _, tc := range []struct {
		name string
		pass string
		ok   bool
	}{
		{"twelve ascii characters", "twelve chars", true},
		{"eleven ascii characters", "eleven char", false},
		{"four three-byte characters", twelveBytes, false},
		{"twelve accented characters", strings.Repeat("é", config.MinPasswordLen), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg, ok := checkPassword(tc.pass)
			if ok != tc.ok {
				t.Errorf("checkPassword(%q) = %v (%q), want %v: %d bytes, %d characters",
					tc.pass, ok, msg, tc.ok, len(tc.pass), utf8.RuneCountInString(tc.pass))
			}
		})
	}
}
