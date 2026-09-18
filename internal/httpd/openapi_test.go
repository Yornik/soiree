package httpd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	yaml "go.yaml.in/yaml/v3"

	"github.com/Yornik/soiree/internal/config"
	"github.com/Yornik/soiree/internal/migrate"
	"github.com/Yornik/soiree/internal/pgtest"
	"github.com/Yornik/soiree/internal/store"
	"github.com/Yornik/soiree/web"
)

// The specification in api/openapi.yaml is written by hand, so the thing that
// actually goes wrong with it is drift: a route is renamed or removed, and the
// document keeps describing the old one. A reader then follows it to a 404,
// which is worse than no document at all — a missing document is obviously
// missing, and a wrong one is trusted.
//
// This test walks every path and method in the specification and asks the real
// server for it. It does not check the request or response bodies against the
// schemas; that is a different test and a much noisier one. It checks the one
// property a hand-written document loses first: that the route is still there.
//
// # Why the requests are signed in, and why the status alone proves nothing
//
// The obvious version of this test sends anonymous requests and treats 401 as
// proof that a route exists. That version passes whatever it is given.
// RequireWrite wraps the whole /api/v1/ subtree and answers 401 before the
// inner mux ever routes, so every path under the prefix answers 401 —
// including the ones that do not exist.
//
// So the requests carry a session, and the check reads the body rather than
// the status. Two different things answer 404 here and they are easy to tell
// apart:
//
//   - The inner mux answers text/plain "404 page not found". No such route.
//   - The API answers JSON {"error":"not_found"}. The route is there; the row
//     the {id} names is not, which is expected and fine.
//
// The session is an admin's, because /users refuses everybody else, and a 403
// would hide whether the route exists.

// specPathID fills every path template. Any syntactically valid UUID will do:
// no row has it, and a JSON 404 is the expected, passing answer.
const specPathID = "3f2504e0-4f89-11d3-9a0c-0305e82c3301"

// specTemplate matches any {name} segment, rather than {id} alone. A template
// left unsubstituted is sent as a literal, which reaches the handler as an
// unparseable id and draws a 400 — a pass, and a quiet one.
var specTemplate = regexp.MustCompile(`\{[^}]+\}`)

// specRequestTimeout bounds one request. It exists for GET /events, which is a
// stream and would otherwise hold this test open until the package timeout.
const specRequestTimeout = 500 * time.Millisecond

type openAPIDoc struct {
	Servers []struct {
		URL string `yaml:"url"`
	} `yaml:"servers"`
	Paths map[string]map[string]yaml.Node `yaml:"paths"`
}

func loadOpenAPI(t *testing.T) openAPIDoc {
	t.Helper()

	path := filepath.Join("..", "..", "api", "openapi.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	var doc openAPIDoc
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if len(doc.Paths) == 0 {
		t.Fatalf("%s declares no paths", path)
	}
	if len(doc.Servers) == 0 {
		t.Fatalf("%s declares no servers, so the paths have no prefix", path)
	}
	return doc
}

// httpMethods are the keys of a path item that denote an operation. Everything
// else under a path — `parameters`, `summary` — is not one.
var httpMethods = map[string]bool{
	"get": true, "put": true, "post": true, "delete": true,
	"patch": true, "head": true, "options": true, "trace": true,
}

// newFullServer builds a server with everything this specification describes
// switched on: the store, the accounts surface and passkeys.
//
// The fixtures next door each build a part. This one has to build the whole
// thing, because a route that is off is a route that is absent — the passkey
// paths are not mounted at all without a relying party — and a specification
// checked against a half-configured server would report its own coverage as
// missing routes.
func newFullServer(t *testing.T) (http.Handler, *Auth, *store.Store) {
	t.Helper()

	pool := pgtest.Pool(t)
	if _, err := migrate.Run(t.Context(), pool, nil); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(pool)

	s, err := New(
		config.Config{EventName: "Ada's Retirement", Currency: "EUR", Locale: "en-US"},
		web.FS(),
		WithStore(st),
	)
	if err != nil {
		t.Fatalf("New(): %v", err)
	}

	a := NewAuth(AuthOptions{Store: st, BaseURL: passkeyOrigin})
	a.params = cheapParams
	a.background = func(fn func(context.Context)) { fn(context.Background()) }
	if err := a.WithPasskeys(passkeyConfig()); err != nil {
		t.Fatalf("enable passkeys: %v", err)
	}

	return s.WithAuth(a).Handler(), a, st
}

func TestEveryDocumentedRouteExists(t *testing.T) {
	h, a, st := newFullServer(t)

	const password = "correct horse battery staple"
	hash, err := a.params.Hash(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	const email = "spec-admin@example.test"
	if _, err := st.CreateUser(t.Context(), store.User{
		Email: email, Role: store.RoleAdmin, Status: store.StatusActive, PasswordHash: &hash,
	}); err != nil {
		t.Fatalf("create admin: %v", err)
	}

	login := func() *http.Cookie {
		t.Helper()
		body := `{"email":"` + email + `","password":"` + password + `"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("login: status %d, body %s", rec.Code, rec.Body)
		}
		for _, c := range rec.Result().Cookies() {
			if c.Name == sessionCookieName && c.Value != "" {
				return c
			}
		}
		t.Fatalf("login set no session cookie")
		return nil
	}

	session := login()

	doc := loadOpenAPI(t)
	prefix := strings.TrimSuffix(doc.Servers[0].URL, "/")

	// Sorted, so a failure reads the same way twice running.
	paths := make([]string, 0, len(doc.Paths))
	for p := range doc.Paths {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	checked := 0
	for _, specPath := range paths {
		item := doc.Paths[specPath]

		methods := make([]string, 0, len(item))
		for m := range item {
			if httpMethods[m] {
				methods = append(methods, m)
			}
		}
		sort.Strings(methods)

		for _, method := range methods {
			checked++
			url := prefix + specTemplate.ReplaceAllString(specPath, specPathID)
			name := strings.ToUpper(method) + " " + specPath

			t.Run(name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), specRequestTimeout)
				defer cancel()

				// An empty JSON object rather than no body at all: a handler
				// that decodes before it checks anything would otherwise fail
				// on the decode, and a 400 passes this test for the wrong
				// reason.
				req := httptest.NewRequest(strings.ToUpper(method), url, strings.NewReader("{}"))
				req = req.WithContext(ctx)
				req.Header.Set("Content-Type", "application/json")
				req.AddCookie(session)

				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)

				switch rec.Code {
				case http.StatusNotFound:
					// The discriminator. See the note at the top.
					if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "json") {
						t.Errorf("%s is documented, and the server has no such route.\n"+
							"It answered the mux's own 404 (%s: %q), not the API's.\n"+
							"The route was renamed or removed, and api/openapi.yaml still describes it.",
							name, ct, strings.TrimSpace(rec.Body.String()))
					}
				case http.StatusMethodNotAllowed:
					t.Errorf("%s is documented but the server answers 405.\n"+
						"The path exists and that method does not.", name)
				case http.StatusUnauthorized:
					t.Errorf("%s answered 401 with a live admin session.\n"+
						"The session was lost, so every later check in this test is "+
						"meaningless: RequireWrite answers 401 for routes that do not "+
						"exist as readily as for routes that do.", name)
				}
			})

			// Signing out is documented, so it gets called, and it does what it
			// says. Everything after it would otherwise run anonymously — which
			// is the failure this test was written with the first time.
			if specPath == "/auth/logout" {
				session = login()
			}
		}
	}

	// A specification that parsed but described almost nothing would pass
	// every assertion above by describing nothing wrong.
	if checked < 35 {
		t.Errorf("only %d operations checked; api/openapi.yaml has lost most of its surface", checked)
	}
}

// The six collections are registered through one generic `register` call each,
// so a seventh is three lines of Go and no compiler error anywhere near the
// specification. This is the cheap half of the reverse direction: it will not
// notice a new route, but it will notice a new collection, which is how this
// API has actually grown.
func TestEveryCollectionIsDocumented(t *testing.T) {
	doc := loadOpenAPI(t)

	collections := []string{
		"budget-items", "sponsors", "tasks", "notes", "phases", "programme-entries",
	}

	for _, c := range collections {
		for _, want := range []string{"/" + c, "/" + c + "/{id}"} {
			if _, ok := doc.Paths[want]; !ok {
				t.Errorf("collection %q has no %q in api/openapi.yaml", c, want)
			}
		}
	}
}

// The drift test proves a documented route exists. It says nothing about what
// that route answers, and the first run of this specification against the code
// turned up two responses that had been described from inference rather than
// read from the source. So the handful of claims that are easy to get wrong and
// expensive to get wrong — the ones a client branches on — are pinned here.
func TestTheDocumentedErrorContractHolds(t *testing.T) {
	h, a, st := newFullServer(t)

	const password = "correct horse battery staple"
	hash, err := a.params.Hash(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	seat := func(email string, role store.Role) *http.Cookie {
		t.Helper()
		if _, err := st.CreateUser(t.Context(), store.User{
			Email: email, Role: role, Status: store.StatusActive, PasswordHash: &hash,
		}); err != nil {
			t.Fatalf("create %s: %v", role, err)
		}
		body := `{"email":"` + email + `","password":"` + password + `"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("login as %s: %d %s", role, rec.Code, rec.Body)
		}
		for _, c := range rec.Result().Cookies() {
			if c.Name == sessionCookieName && c.Value != "" {
				return c
			}
		}
		t.Fatalf("no cookie for %s", role)
		return nil
	}

	admin := seat("contract-admin@example.test", store.RoleAdmin)
	editor := seat("contract-editor@example.test", store.RoleEditor)
	viewer := seat("contract-viewer@example.test", store.RoleViewer)

	call := func(method, path string, c *http.Cookie) (int, string) {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		if c != nil {
			req.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		var body struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec.Code, body.Error
	}

	cases := []struct {
		what       string
		method     string
		path       string
		as         *http.Cookie
		wantStatus int
		wantCode   string
	}{
		// A delete names the row it deletes. The specification says so for all
		// seven deletes; this is the half of that claim the drift test cannot
		// see, because it sends the parameter.
		{"a collection delete with no revision", "DELETE", "/api/v1/tasks/" + specPathID, admin,
			http.StatusBadRequest, "bad_request"},
		{"an account delete with no revision", "DELETE", "/api/v1/users/" + specPathID, admin,
			http.StatusBadRequest, "revision_required"},

		// The two 403s are different codes, and the specification now names
		// both rather than calling them all "forbidden".
		{"a viewer writing", "PATCH", "/api/v1/settings", viewer,
			http.StatusForbidden, "read_only"},
		{"an editor reaching the accounts surface", "GET", "/api/v1/users", editor,
			http.StatusForbidden, "forbidden"},

		// 401 is the answer that must never be confused with 404. See the note
		// at the top of the file, and the Unauthorized response in the
		// specification.
		{"no session at all", "GET", "/api/v1/plan", nil,
			http.StatusUnauthorized, "unauthenticated"},
	}

	for _, c := range cases {
		t.Run(c.what, func(t *testing.T) {
			status, code := call(c.method, c.path, c.as)
			if status != c.wantStatus || code != c.wantCode {
				t.Errorf("%s %s as %s: got %d/%q, api/openapi.yaml documents %d/%q",
					c.method, c.path, c.what, status, code, c.wantStatus, c.wantCode)
			}
		})
	}
}

// 429 carries a Retry-After, and the specification promises a flat 60.
func TestTheRateLimitAnswersAsDocumented(t *testing.T) {
	h, _, _ := newFullServer(t)

	body := `{"email":"nobody@example.test","password":"wrong-password-here"}`
	var last *httptest.ResponseRecorder
	for i := 0; i < loginIPBurst+2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		last = httptest.NewRecorder()
		h.ServeHTTP(last, req)
		if last.Code == http.StatusTooManyRequests {
			break
		}
	}

	if last.Code != http.StatusTooManyRequests {
		t.Fatalf("the per-IP login limit never tripped; got %d", last.Code)
	}
	if got := last.Header().Get("Retry-After"); got != "60" {
		t.Errorf("Retry-After is %q; api/openapi.yaml documents a flat \"60\"", got)
	}
	var parsed struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(last.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("429 body is not JSON, and api/openapi.yaml documents an ApiError: %v", err)
	}
	if parsed.Error != "rate_limited" {
		t.Errorf("429 code is %q; api/openapi.yaml documents \"rate_limited\"", parsed.Error)
	}
}
