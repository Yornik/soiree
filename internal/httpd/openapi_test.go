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
	"github.com/Yornik/soiree/internal/objstore"
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

	// Files too, so their documented routes are routes. A bucket that holds
	// nothing is enough for "does this path exist?", and keeps this file's
	// tests off the network; what the routes do is attachments_test.go's job,
	// against a real one.
	files := NewAttachments(st, emptyBucket{}, 1<<20, 1<<24, nil)

	return s.WithAuth(a).WithAttachments(files).Handler(), a, st
}

// emptyBucket signs nothing and holds nothing.
type emptyBucket struct{}

func (emptyBucket) PresignPut(string, int64, string, time.Duration) objstore.Upload {
	return objstore.Upload{URL: "https://bucket.example.test/upload", Headers: map[string]string{}}
}
func (emptyBucket) PresignGet(string, time.Duration, string, string) string {
	return "https://bucket.example.test/download"
}
func (emptyBucket) Head(context.Context, string) (int64, error) { return 0, objstore.ErrNotFound }
func (emptyBucket) Delete(context.Context, string) error        { return nil }

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

// A collection added to GET /plan and not to the specification passed every
// test in this file, which is how this one came to exist: `attachments` was in
// the plan for a day before it was in the document, and nothing noticed. The
// plan's keys and the Plan schema's properties have to be the same set.
func TestEveryKeyOfThePlanIsDocumented(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "api", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Required   []string             `yaml:"required"`
				Properties map[string]yaml.Node `yaml:"properties"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse the specification: %v", err)
	}
	schema := doc.Components.Schemas["Plan"]
	if len(schema.Properties) == 0 {
		t.Fatal("api/openapi.yaml has no Plan schema, or it has no properties")
	}

	encoded, err := json.Marshal(encodePlan("EUR", store.Plan{}))
	if err != nil {
		t.Fatal(err)
	}
	var onWire map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &onWire); err != nil {
		t.Fatal(err)
	}

	required := map[string]bool{}
	for _, r := range schema.Required {
		required[r] = true
	}
	for key := range onWire {
		if _, ok := schema.Properties[key]; !ok {
			t.Errorf("GET /plan carries %q, which Plan in api/openapi.yaml does not describe", key)
		}
		// Every key is always present, empty or not, so every key is required.
		if !required[key] {
			t.Errorf("GET /plan always carries %q, and Plan does not list it as required", key)
		}
	}
	for key := range schema.Properties {
		if _, ok := onWire[key]; !ok {
			t.Errorf("Plan in api/openapi.yaml describes %q, which GET /plan does not carry", key)
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

	call := func(method, path, body string, c *http.Cookie) (int, string) {
		t.Helper()
		if body == "" {
			body = "{}"
		}
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if c != nil {
			req.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		var answer struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &answer)
		return rec.Code, answer.Error
	}

	cases := []struct {
		what       string
		method     string
		path       string
		body       string
		as         *http.Cookie
		wantStatus int
		wantCode   string
	}{
		// A delete names the row it deletes. The specification says so for all
		// seven deletes; this is the half of that claim the drift test cannot
		// see, because it sends the parameter.
		{"a collection delete with no revision", "DELETE", "/api/v1/tasks/" + specPathID, "", admin,
			http.StatusBadRequest, "bad_request"},
		{"an account delete with no revision", "DELETE", "/api/v1/users/" + specPathID, "", admin,
			http.StatusBadRequest, "revision_required"},

		// The two 403s are different codes, and the specification now names
		// both rather than calling them all "forbidden".
		{"a viewer writing", "PATCH", "/api/v1/settings", "", viewer,
			http.StatusForbidden, "read_only"},
		{"an editor reaching the accounts surface", "GET", "/api/v1/users", "", editor,
			http.StatusForbidden, "forbidden"},

		// 401 is the answer that must never be confused with 404. See the note
		// at the top of the file, and the Unauthorized response in the
		// specification.
		{"no session at all", "GET", "/api/v1/plan", "", nil,
			http.StatusUnauthorized, "unauthenticated"},

		// A login with no address is a malformed request. It used to answer
		// 429 with Retry-After: 60, which tells a client with a bug to wait a
		// minute and send the same bug again.
		{"a login with no address", "POST", "/api/v1/auth/login", `{"password":"x"}`, nil,
			http.StatusBadRequest, "invalid_email"},

		// "The plan refuses a field it does not know", and the three halves of
		// that section: strict, null refused where null means nothing, and the
		// four echoed fields let through so the obvious client stays legal.
		{"a collection sent a field it does not have", "POST", "/api/v1/tasks", `{"nmae":"Book the band"}`, editor,
			http.StatusBadRequest, "bad_request"},
		{"null on a field that cannot be null", "POST", "/api/v1/budget-items", `{"item":"Flowers","unit":null}`, editor,
			http.StatusBadRequest, "bad_request"},
		{"a row echoed back whole", "POST", "/api/v1/tasks",
			`{"id":"` + specPathID + `","revision":7,"updatedAt":"2026-01-04T10:00:00Z","updatedBy":null,"name":"Book the band"}`, editor,
			http.StatusCreated, ""},
		{"the settings do not have an id to echo", "PATCH", "/api/v1/settings", `{"revision":1,"id":"` + specPathID + `"}`, editor,
			http.StatusBadRequest, "bad_request"},
		{"the settings echoed back as they are read", "PATCH", "/api/v1/settings", `{"revision":1,"updatedAt":"2026-01-04T10:00:00Z"}`, editor,
			http.StatusOK, ""},

		{"an account in a language with no translation", "POST", "/api/v1/users",
			`{"email":"nobody@example.test","role":"viewer","language":"fr"}`, admin,
			http.StatusBadRequest, "invalid_language"},

		// A delete carries no body in this API, and the specification once said
		// this one did. The endpoint travels where a revision does.
		{"unsubscribing with the endpoint in a body", "DELETE", "/api/v1/push/subscriptions",
			`{"endpoint":"https://push.example.test/send/abc"}`, editor,
			http.StatusBadRequest, "bad_request"},
		{"unsubscribing with the endpoint in the query", "DELETE",
			"/api/v1/push/subscriptions?endpoint=https%3A%2F%2Fpush.example.test%2Fsend%2Fabc", "", editor,
			http.StatusNoContent, ""},

		// The accounts surface is NOT strict, and the specification says so.
		// Pinned because it is the half of that sentence somebody would
		// "fix" by assumption.
		{"the accounts surface ignores a field it does not know", "POST", "/api/v1/auth/password-reset",
			`{"email":"nobody@example.test","colour":"green"}`, nil,
			http.StatusAccepted, ""},
	}

	for _, c := range cases {
		t.Run(c.what, func(t *testing.T) {
			status, code := call(c.method, c.path, c.body, c.as)
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

// The specification first described the `change` event as carrying the row
// that changed. It carries four identifiers and never a row, and a client
// written from that description would have waited forever for data that does
// not come. The existing stream tests decode the frame into store.ChangeNotice,
// which is exactly the check that cannot see this: a struct ignores a key it
// has no field for, and says nothing about a key that was renamed.
//
// So this compares raw key sets — what is on the wire against the properties
// of ChangeNotice in api/openapi.yaml — in both directions.
func TestTheChangeFrameIsAsDocumented(t *testing.T) {
	s, ts, _, h := newLiveServer(t)

	st := openStream(t, ts)
	st.awaitResync(t)
	awaitListening(t, s)

	created(t, h, "tasks", `{"name":"Book the band"}`)
	frame := st.await(t, "change event", func(f string) bool {
		return strings.HasPrefix(f, "event: change\n")
	})
	_, data, _ := strings.Cut(frame, "data: ")

	var onWire map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(data)), &onWire); err != nil {
		t.Fatalf("change data is not a JSON object: %v\n%s", err, frame)
	}

	raw, err := os.ReadFile(filepath.Join("..", "..", "api", "openapi.yaml"))
	if err != nil {
		t.Fatalf("read the specification: %v", err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Enum []string `yaml:"enum"`
				} `yaml:"properties"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse the specification: %v", err)
	}
	documented := doc.Components.Schemas["ChangeNotice"].Properties
	if len(documented) == 0 {
		t.Fatal("api/openapi.yaml has no ChangeNotice schema, or it has no properties")
	}

	for key := range onWire {
		if _, ok := documented[key]; !ok {
			t.Errorf("the change event carries %q, which ChangeNotice in api/openapi.yaml does not describe", key)
		}
	}
	for key := range documented {
		if _, ok := onWire[key]; !ok {
			t.Errorf("ChangeNotice in api/openapi.yaml describes %q, which the change event does not carry", key)
		}
	}

	// The entity names are table names with an underscore, not path names with
	// a hyphen, and that is the other thing a reader would guess wrong.
	announced := map[string]bool{}
	for _, e := range []string{
		store.EntityAttachments,
		store.EntityBudgetItems, store.EntityNotes, store.EntityPhases,
		store.EntityProgrammeEntries, store.EntitySettings, store.EntitySponsors, store.EntityTasks,
	} {
		announced[e] = true
	}
	for _, e := range documented["entity"].Enum {
		if !announced[e] {
			t.Errorf("ChangeNotice.entity lists %q, which is not an entity the stream announces", e)
		}
		delete(announced, e)
	}
	for e := range announced {
		t.Errorf("the stream announces %q, which ChangeNotice.entity does not list", e)
	}
}
