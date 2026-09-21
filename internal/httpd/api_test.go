package httpd

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Yornik/soiree/internal/config"
	"github.com/Yornik/soiree/internal/migrate"
	"github.com/Yornik/soiree/internal/pgtest"
	"github.com/Yornik/soiree/internal/store"
	"github.com/Yornik/soiree/web"
)

// These tests talk to a real Postgres, because the parts most worth checking —
// the revision conflict, the cascade behind a delete, what a `date` column
// round-trips to — are precisely the parts a fake would get wrong in whichever
// direction it was written. `go test -short` skips them.
//
// Every fixture here is obviously synthetic. The schema was drawn from real
// planning data; none of that data belongs in a repository.

func TestMain(m *testing.M) { pgtest.Main(m, startPostgres) }

// newAPIServer builds a server with the API on, backed by a database of its
// own. It skips under -short, where there is no Docker.
//
// EUR, because the money tests have to be run against a currency that has
// cents. See TestMoneyCrossesTheBoundaryInMajorUnits.
func newAPIServer(t *testing.T) (http.Handler, *pgxpool.Pool) {
	t.Helper()
	return newAPIServerIn(t, "EUR")
}

// newAPIServerIn is newAPIServer against a named currency, which is the one
// thing that decides how the API renders and reads every money field.
func newAPIServerIn(t *testing.T, currency string) (http.Handler, *pgxpool.Pool) {
	t.Helper()

	pool := pgtest.Pool(t)
	if _, err := migrate.Run(t.Context(), pool, nil); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(pool)
	s, err := New(
		config.Config{EventName: "Ada's Retirement", Currency: currency, Locale: "en-US"},
		web.FS(),
		WithStore(st),
	)
	if err != nil {
		t.Fatalf("New(): %v", err)
	}

	// The API is behind RequireWrite, so these tests need a session like any
	// other caller. Signing in here rather than per test keeps all of them
	// about the API instead of about authentication, which has its own suite —
	// and an editor is the least-privileged role that may write, so a test
	// passing here does not depend on being an admin.
	a := NewAuth(AuthOptions{Store: st, BaseURL: "https://soiree.example.test/"})
	a.params = cheapParams
	a.background = func(fn func(context.Context)) { fn(context.Background()) }
	h := s.WithAuth(a).Handler()

	return authedAs(t, h, st, a, store.RoleEditor), pool
}

// newAPIServerParts is the shared construction, handing back the bare handler
// so a caller can choose whether and as whom to sign in.
func newAPIServerParts(t *testing.T, currency string) (http.Handler, *Auth, *pgxpool.Pool) {
	t.Helper()

	pool := pgtest.Pool(t)
	if _, err := migrate.Run(t.Context(), pool, nil); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(pool)
	s, err := New(
		config.Config{EventName: "Ada's Retirement", Currency: currency, Locale: "en-US"},
		web.FS(),
		WithStore(st),
	)
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	a := NewAuth(AuthOptions{Store: st, BaseURL: "https://soiree.example.test/"})
	a.params = cheapParams
	a.background = func(fn func(context.Context)) { fn(context.Background()) }
	return s.WithAuth(a).Handler(), a, pool
}

// newAPIServerUnauthenticated is the same server with no session attached, for
// asserting what an anonymous caller is refused.
func newAPIServerUnauthenticated(t *testing.T) (http.Handler, *pgxpool.Pool) {
	t.Helper()
	h, _, pool := newAPIServerParts(t, "EUR")
	return h, pool
}

// newAPIServerAs is the server with a session in a chosen role.
func newAPIServerAs(t *testing.T, role store.Role) (http.Handler, *pgxpool.Pool) {
	t.Helper()
	h, a, pool := newAPIServerParts(t, "EUR")
	return authedAs(t, h, store.New(pool), a, role), pool
}

// authedAs returns h with every request carrying a session for a new account in
// the given role.
//
// Wrapping the handler rather than changing call() keeps the ~54 existing
// request sites untouched: what they assert about the API is unchanged by the
// API having become authenticated.
func authedAs(t *testing.T, h http.Handler, st *store.Store, a *Auth, role store.Role) http.Handler {
	t.Helper()

	const password = "correct horse battery staple"
	hash, err := a.params.Hash(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	email := string(role) + "@example.test"
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
		t.Fatalf("login as %s: status %d, body %s", role, rec.Code, rec.Body)
	}

	var session *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName && c.Value != "" {
			session = c
		}
	}
	if session == nil {
		t.Fatalf("login as %s set no session cookie", role)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := r.Cookie(sessionCookieName); err != nil {
			r.AddCookie(session)
		}
		h.ServeHTTP(w, r)
	})
}

type response struct {
	status int
	header http.Header
	body   []byte
}

func call(t *testing.T, h http.Handler, method, path, body string) response {
	t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	res := rec.Result()
	defer func() { _ = res.Body.Close() }()
	out, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read %s %s: %v", method, path, err)
	}
	return response{status: res.StatusCode, header: res.Header, body: out}
}

// created posts a body and returns the decoded row, failing the test on
// anything but a 201.
func created(t *testing.T, h http.Handler, collection, body string) map[string]any {
	t.Helper()
	res := call(t, h, http.MethodPost, "/api/v1/"+collection, body)
	if res.status != http.StatusCreated {
		t.Fatalf("POST %s -> %d, want 201\n%s", collection, res.status, res.body)
	}
	return decode(t, res)
}

func decode(t *testing.T, res response) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(res.body, &out); err != nil {
		t.Fatalf("response is not a JSON object: %v\n%s", err, res.body)
	}
	return out
}

func str(t *testing.T, row map[string]any, key string) string {
	t.Helper()
	v, ok := row[key].(string)
	if !ok {
		t.Fatalf("%q = %#v, want a string", key, row[key])
	}
	return v
}

func num(t *testing.T, row map[string]any, key string) float64 {
	t.Helper()
	v, ok := row[key].(float64)
	if !ok {
		t.Fatalf("%q = %#v, want a number", key, row[key])
	}
	return v
}

// TestAPIIsOffWithoutADatabase is the configuration CI's image smoke test runs:
// no DSN, no API, and a frontend that works exactly as it did before.
//
// It needs no database of its own, so unlike everything else here it also runs
// under -short.
func TestAPIIsOffWithoutADatabase(t *testing.T) {
	h := newTestServer(t, config.Config{EventName: "Ada's Retirement"})

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/plan"},
		{http.MethodPost, "/api/v1/tasks"},
		{http.MethodPatch, "/api/v1/budget-items/" + uuid.New().String()},
		{http.MethodDelete, "/api/v1/notes/" + uuid.New().String()},
	} {
		res := call(t, h, tc.method, tc.path, `{}`)
		if res.status != http.StatusNotFound {
			t.Errorf("%s %s -> %d, want 404 when no database is configured", tc.method, tc.path, res.status)
		}
	}

	shell := call(t, h, http.MethodGet, "/", "")
	if shell.status != http.StatusOK || !strings.Contains(string(shell.body), "Ada&#39;s Retirement") {
		t.Errorf("the frontend stopped working without a database: %d", shell.status)
	}
	// Readiness must not depend on a database that was never configured, or a
	// frontend-only deployment never becomes ready.
	for _, p := range []string{"/healthz", "/readyz"} {
		res := call(t, h, http.MethodGet, p, "")
		if res.status != http.StatusOK {
			t.Errorf("%s -> %d, want 200 with no database configured", p, res.status)
		}
	}
}

// TestPlanIsOneRoundTrip walks a create of every entity and then reads the lot
// back in a single request, which is the shape the browser actually uses.
func TestPlanIsOneRoundTrip(t *testing.T) {
	h, _ := newAPIServer(t)

	phase := created(t, h, "phases", `{"name":"Arrival","position":0}`)
	ada := created(t, h, "sponsors", `{"code":"Rose","name":"Ada","position":0}`)
	grace := created(t, h, "sponsors", `{"code":"Ivy","name":"Grace","position":1}`)

	item := created(t, h, "budget-items", `{
		"phaseId": "`+str(t, phase, "id")+`",
		"item": "Venue deposit",
		"vendor": "Example Hall",
		"unit": "250.50",
		"qty": 2.5,
		"paid": "50.00",
		"lockBy": "2030-01-31",
		"note": "Balance due one month before",
		"position": 0,
		"sponsors": ["`+str(t, ada, "id")+`", "`+str(t, grace, "id")+`"]
	}`)
	created(t, h, "programme-entries", `{"title":"Cutting the cake","position":0,"budgetItemId":"`+str(t, item, "id")+`"}`)
	created(t, h, "tasks", `{"name":"Confirm final guest count","owner":"Ada","due":"2030-01-15","position":0}`)
	created(t, h, "notes", `{"text":"Venue balance is due a month out.","position":0}`)

	// The created row comes back whole, in the shapes the browser is promised.
	if got := str(t, item, "unit"); got != "250.50" {
		t.Errorf("unit = %q, want the major units the client sent", got)
	}
	if got := str(t, item, "lockBy"); got != "2030-01-31" {
		t.Errorf("lockBy = %q, want a plain date — a timestamp is a different day east of UTC", got)
	}
	if got := num(t, item, "revision"); got != 1 {
		t.Errorf("revision = %v, want 1 on a fresh row", got)
	}

	res := call(t, h, http.MethodGet, "/api/v1/plan", "")
	if res.status != http.StatusOK {
		t.Fatalf("GET /api/v1/plan -> %d\n%s", res.status, res.body)
	}
	if ct := res.header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	// Shared state two people are editing must never come from a cache.
	if cc := res.header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}

	var plan struct {
		Settings struct {
			// A string, like every other money field on this boundary.
			Ceiling  string `json:"ceiling"`
			Revision int64  `json:"revision"`
		} `json:"settings"`
		Phases      []map[string]any `json:"phases"`
		Sponsors    []map[string]any `json:"sponsors"`
		BudgetItems []map[string]any `json:"budgetItems"`
		Programme   []map[string]any `json:"programme"`
		Tasks       []map[string]any `json:"tasks"`
		Notes       []map[string]any `json:"notes"`
	}
	if err := json.Unmarshal(res.body, &plan); err != nil {
		t.Fatalf("plan is not the documented shape: %v\n%s", err, res.body)
	}

	if plan.Settings.Revision != 1 {
		t.Errorf("settings.revision = %d, want the seeded 1", plan.Settings.Revision)
	}
	if plan.Settings.Ceiling != "0.00" {
		t.Errorf("settings.ceiling = %q, want the seeded zero in EUR major units", plan.Settings.Ceiling)
	}
	for name, list := range map[string][]map[string]any{
		"phases": plan.Phases, "sponsors": plan.Sponsors, "budgetItems": plan.BudgetItems,
		"programme": plan.Programme, "tasks": plan.Tasks, "notes": plan.Notes,
	} {
		if len(list) == 0 {
			t.Errorf("plan.%s is empty", name)
		}
	}
	if len(plan.Sponsors) != 2 {
		t.Errorf("plan.sponsors = %d, want 2", len(plan.Sponsors))
	}

	line := plan.BudgetItems[0]
	sponsors, ok := line["sponsors"].([]any)
	if !ok || len(sponsors) != 2 {
		t.Errorf("budgetItems[0].sponsors = %#v, want the two attributions", line["sponsors"])
	}
	if str(t, line, "vendor") != "Example Hall" {
		t.Errorf("vendor = %q", line["vendor"])
	}
	if str(t, plan.Tasks[0], "due") != "2030-01-15" {
		t.Errorf("task due = %q, want a plain date", plan.Tasks[0]["due"])
	}
	if str(t, plan.Tasks[0], "status") != "not-started" {
		t.Errorf("task status = %q, want the column default", plan.Tasks[0]["status"])
	}
}

// The plan is the one body this API sends over and over: every other open
// browser re-reads the whole of it after anybody's edit and again on every
// reconnect, to readers the origin may be 300 ms away from. It is also
// repetitive JSON, which is what an encoder is good at.
func TestThePlanIsGzippedForAClientThatAcceptsIt(t *testing.T) {
	h, _ := newAPIServer(t)

	// Enough rows to put the body past the size below which the encoder's own
	// header costs more than it saves.
	for i := range 20 {
		created(t, h, "tasks", fmt.Sprintf(
			`{"name":"Confirm the running order with the hall %d","owner":"Ada","position":%d}`, i, i))
	}

	plain := call(t, h, http.MethodGet, "/api/v1/plan", "")
	if plain.status != http.StatusOK {
		t.Fatalf("GET /api/v1/plan -> %d\n%s", plain.status, plain.body)
	}
	if enc := plain.header.Get("Content-Encoding"); enc != "" {
		t.Errorf("no Accept-Encoding should mean no encoding, got %q", enc)
	}
	if v := plain.header.Get("Vary"); !strings.Contains(v, "Accept-Encoding") {
		t.Errorf("Vary = %q, want Accept-Encoding: what this route answers depends on it", v)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/plan", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	res := rec.Result()
	defer func() { _ = res.Body.Close() }()
	encoded, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read the encoded plan: %v", err)
	}

	if got := res.Header.Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip: the largest repeated read goes out raw", got)
	}
	if got := res.Header.Get("Content-Length"); got != strconv.Itoa(len(encoded)) {
		t.Errorf("Content-Length = %q, want the %d bytes actually sent", got, len(encoded))
	}
	if len(encoded) >= len(plain.body) {
		t.Errorf("encoded body (%d) is not smaller than the identity one (%d)", len(encoded), len(plain.body))
	}
	// Encoding it does not make it cacheable: it is still shared state two
	// people are editing.
	if cc := res.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}

	zr, err := gzip.NewReader(bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("the body is not gzip: %v", err)
	}
	decoded, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("decode the plan: %v", err)
	}
	_ = zr.Close()
	if !bytes.Equal(decoded, plain.body) {
		t.Errorf("the encoded plan is not the plan a client without the header gets:\n%s", decoded)
	}
}

// Everything else this API sends is small: an error, a row echoed back, a 204
// with no body at all. The encoder's own header is a poor trade against a few
// hundred bytes. Nothing that does not go through writeJSONCompressed is
// encoded at all.
func TestOnlyABodyWorthEncodingIsEncoded(t *testing.T) {
	large := map[string]string{"note": strings.Repeat("the hall wants the running order. ", 40)}
	small := map[string]string{"note": "the hall wants the running order."}

	for name, c := range map[string]struct {
		payload any
		accept  string
		want    string
	}{
		"a large body for a client that accepts gzip": {large, "gzip", "gzip"},
		"the same body for one that does not":         {large, "identity", ""},
		"a body below the threshold":                  {small, "gzip", ""},
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/plan", nil)
			req.Header.Set("Accept-Encoding", c.accept)
			rec := httptest.NewRecorder()
			writeJSONCompressed(rec, req, http.StatusOK, c.payload)

			if got := rec.Header().Get("Content-Encoding"); got != c.want {
				t.Errorf("Content-Encoding = %q, want %q", got, c.want)
			}
			if v := rec.Header().Get("Vary"); !strings.Contains(v, "Accept-Encoding") {
				t.Errorf("Vary = %q, want Accept-Encoding on both branches", v)
			}
		})
	}

	// A response nothing negotiated, which is every error and every write read
	// back, goes out exactly as it did before.
	rec := httptest.NewRecorder()
	writeJSON(rec, http.StatusOK, large)
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q on a response that negotiated nothing", got)
	}
	if v := rec.Header().Get("Vary"); v != "" {
		t.Errorf("Vary = %q on a response whose body does not depend on it", v)
	}
}

// failingPlan mounts a handler where servePlan sits, behind the same
// StripPrefix, so that what it sees of the request is what a real handler sees:
// a path with the prefix already taken off it.
func failingPlan(err error) http.Handler {
	api := http.NewServeMux()
	api.HandleFunc("GET /plan", func(w http.ResponseWriter, r *http.Request) {
		writeInternal(w, r, err)
	})
	mux := http.NewServeMux()
	mux.Handle(apiPrefix, http.StripPrefix(strings.TrimSuffix(apiPrefix, "/"), api))
	return mux
}

// A 500 that says only what the database said cannot be operated on: there is
// no access log here to join "api request failed" against, and the metrics
// carry no id a line could be matched to. The route comes from the matched
// pattern rather than the path, because a handler under StripPrefix sees
// "/plan" and an operator needs to know which route that was.
func TestAFailureNamesTheRequestThatFailed(t *testing.T) {
	var logged bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	rec := httptest.NewRecorder()
	failingPlan(errors.New(`relation "budget_items" does not exist`)).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/plan", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	line := logged.String()
	for _, want := range []string{`"level":"ERROR"`, `"method":"GET"`, `"pattern":"GET /plan"`} {
		if !strings.Contains(line, want) {
			t.Errorf("the failure reached the log without %s:\n%s", want, line)
		}
	}
}

// A phone that locks its screen mid-read cancels the query it was waiting on.
// Nothing here failed and nobody is left to be told anything. Logged as an
// error and counted as a 500, though, it is indistinguishable from this server
// breaking, on the one number an alert reads and in the one place an operator
// looks. 499 is where it goes instead.
func TestAClientThatWentAwayIsNotAServerFailure(t *testing.T) {
	var logged bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/plan", nil).WithContext(ctx)

	m := NewMetrics("test", "none")
	rec := httptest.NewRecorder()
	// What LoadPlan comes back with when the caller hung up during it.
	m.instrument(failingPlan(fmt.Errorf("load the plan: %w", context.Canceled))).ServeHTTP(rec, req)

	if rec.Code != statusClientClosedRequest {
		t.Errorf("status = %d, want %d for a caller that is no longer there", rec.Code, statusClientClosedRequest)
	}
	if body := rec.Body.String(); body != "" {
		t.Errorf("body = %q, want none: there is nobody on the other end to read it", body)
	}
	if line := logged.String(); strings.Contains(line, `"level":"ERROR"`) {
		t.Errorf("an abandoned request was logged as a failure of this server:\n%s", line)
	}

	scrape := httptest.NewRecorder()
	m.Handler().ServeHTTP(scrape, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	want := `soiree_http_requests_total{method="GET",route="api-plan",status="499"} 1`
	if !strings.Contains(scrape.Body.String(), want) {
		t.Errorf("no %s in the exposition, so a client walking away still reads as a 5xx", want)
	}
}

// A genuine database failure that happens to coincide with a disconnect is
// still this server's to answer for, which is why the cancelled context alone
// does not decide it.
func TestAFailureDuringADisconnectIsStillAFailure(t *testing.T) {
	var logged bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/plan", nil).WithContext(ctx)

	rec := httptest.NewRecorder()
	failingPlan(errors.New("connection refused")).ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500: the database, not the client, is what failed", rec.Code)
	}
	if line := logged.String(); !strings.Contains(line, `"level":"ERROR"`) {
		t.Errorf("the failure did not reach the log as an error:\n%s", line)
	}
}

// A class 22 is a value the column would not hold, and 400 is the right answer
// to it: the caller sent it and the caller is the only one who can act on it.
// The same class also covers a value this server built itself, and that one is
// nobody's mistake but ours. There is no access log here and a 400 is nowhere
// near the 5xx rate an alert reads, so a bound in Go that stopped agreeing
// with its column would present as an honest editor being refused, with
// nothing to find it by. Answered, then, and also said out loud.
func TestAValueTheColumnRefusedIsSaidOutLoud(t *testing.T) {
	h, _ := newAPIServer(t)

	var logged bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	// A NUL byte reaches no bound this API checks; Postgres refuses it as
	// SQLSTATE 22021.
	res := call(t, h, http.MethodPost, "/api/v1/budget-items", `{"item":"Ca\u0000ke"}`)
	if res.status != http.StatusBadRequest {
		t.Fatalf("-> %d, want 400\n%s", res.status, res.body)
	}
	line := logged.String()
	for _, want := range []string{`"level":"WARN"`, `"sqlstate":"22021"`, `"pattern":"POST /budget-items"`} {
		if !strings.Contains(line, want) {
			t.Errorf("the refusal reached the log without %s:\n%s", want, line)
		}
	}

	// The constraints an honest client trips over stay as quiet as they were.
	// A reference to a row that does not exist is a 400 from the same
	// translation, and it says nothing about this server.
	logged.Reset()
	res = call(t, h, http.MethodPost, "/api/v1/budget-items",
		`{"item":"Cake","phaseId":"`+uuid.New().String()+`"}`)
	if res.status != http.StatusBadRequest {
		t.Fatalf("a phase that does not exist -> %d, want 400\n%s", res.status, res.body)
	}
	if logged.Len() != 0 {
		t.Errorf("a foreign key the caller got wrong was logged:\n%s", logged.String())
	}
}

// TestPatchLeavesOmittedFieldsAlone is the reason a patch reads the row first.
// UpdateBudgetItem writes every column and replaces the sponsor attributions
// with exactly what it is given, so a patch of one field built from anything
// but the stored row would silently clear the rest of the line.
func TestPatchLeavesOmittedFieldsAlone(t *testing.T) {
	h, _ := newAPIServer(t)

	ada := created(t, h, "sponsors", `{"code":"Rose","name":"Ada"}`)
	grace := created(t, h, "sponsors", `{"code":"Ivy","name":"Grace","position":1}`)
	item := created(t, h, "budget-items", `{
		"item": "Venue deposit", "vendor": "Example Hall",
		"unit": "250.00", "qty": 2, "paid": "50.00",
		"lockBy": "2030-01-31", "note": "Balance due one month before",
		"sponsors": ["`+str(t, ada, "id")+`", "`+str(t, grace, "id")+`"]
	}`)

	res := call(t, h, http.MethodPatch, "/api/v1/budget-items/"+str(t, item, "id"),
		`{"revision": 1, "unit": "300.00"}`)
	if res.status != http.StatusOK {
		t.Fatalf("PATCH -> %d\n%s", res.status, res.body)
	}
	patched := decode(t, res)

	if got := str(t, patched, "unit"); got != "300.00" {
		t.Errorf("unit = %q, want the patched 300.00", got)
	}
	if got := num(t, patched, "revision"); got != 2 {
		t.Errorf("revision = %v, want 2 after a write", got)
	}
	if sponsors, _ := patched["sponsors"].([]any); len(sponsors) != 2 {
		t.Errorf("sponsors = %#v — a patch of one field cleared the attributions", patched["sponsors"])
	}
	if got := str(t, patched, "note"); got != "Balance due one month before" {
		t.Errorf("note = %q, want it untouched", got)
	}
	if got := str(t, patched, "lockBy"); got != "2030-01-31" {
		t.Errorf("lockBy = %q, want it untouched", got)
	}
	if got := str(t, patched, "paid"); got != "50.00" {
		t.Errorf("paid = %q, want it untouched", got)
	}
}

// TestCreateDefaultsQtyToOne: the column defaults to 1 but the store always
// sends the value it was handed, so an omitted qty would be written as zero —
// and a line whose quantity is zero totals nothing, quietly.
func TestCreateDefaultsQtyToOne(t *testing.T) {
	h, _ := newAPIServer(t)

	item := created(t, h, "budget-items", `{"item":"Welcome signage","unit":"5.00"}`)
	if got := num(t, item, "qty"); got != 1 {
		t.Errorf("qty = %v, want the column default of 1", got)
	}
	// An explicit zero is still a zero; only absence takes the default.
	zero := created(t, h, "budget-items", `{"item":"Cancelled extra","unit":"5.00","qty":0}`)
	if got := num(t, zero, "qty"); got != 0 {
		t.Errorf("qty = %v, want the explicit 0", got)
	}
}

// TestQtyIsHeldToItsColumn: qty is the one figure on this boundary that is not
// money, and `numeric(12,3)` bounds it as exactly as a currency's decimals
// bound an amount. Past that bound Postgres answers with an error the client
// can do nothing about, and a fourth decimal is quietly rounded away, which
// leaves the row the browser is holding different from the row the server has,
// for as long as both are open.
func TestQtyIsHeldToItsColumn(t *testing.T) {
	h, _ := newAPIServer(t)

	for _, tc := range []struct{ name, body, want string }{
		{"a billion", `{"item":"Cake","qty":1000000000}`, "999999999.999"},
		{"a billion once rounded", `{"item":"Cake","qty":999999999.9996}`, "999999999.999"},
		{"a fourth decimal", `{"item":"Cake","qty":0.3333}`, "decimal places"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := call(t, h, http.MethodPost, "/api/v1/budget-items", tc.body)
			if res.status != http.StatusBadRequest {
				t.Fatalf("POST -> %d, want 400\n%s", res.status, res.body)
			}
			row := decode(t, res)
			if got := str(t, row, "error"); got != errBadRequest {
				t.Errorf("error = %q, want %q", got, errBadRequest)
			}
			// Naming the limit is the whole of it: a caller told only that
			// something went wrong can do nothing but send the value again.
			msg := str(t, row, "message")
			if !strings.HasPrefix(msg, "qty ") || !strings.Contains(msg, tc.want) {
				t.Errorf("message = %q, want it to name qty and %q", msg, tc.want)
			}
		})
	}

	// The patch path too, which is the one the grid takes on every keystroke.
	item := created(t, h, "budget-items", `{"item":"Cake","qty":1}`)
	res := call(t, h, http.MethodPatch, "/api/v1/budget-items/"+str(t, item, "id"),
		`{"revision":1,"qty":1000000000}`)
	if res.status != http.StatusBadRequest {
		t.Fatalf("PATCH -> %d, want 400\n%s", res.status, res.body)
	}

	// And everything the column does hold still goes through unchanged. A
	// bound that refuses a storable figure is a worse bug than the one it fixes.
	for _, qty := range []string{"999999999.999", "0.001", "2.5", "0"} {
		row := created(t, h, "budget-items", `{"item":"Cake","qty":`+qty+`}`)
		if got := strconv.FormatFloat(num(t, row, "qty"), 'f', -1, 64); got != qty {
			t.Errorf("qty = %s, want the %s that was sent", got, qty)
		}
	}
}

// --- money at the API boundary -----------------------------------------
//
// The store holds minor units; this API speaks major units as decimal strings.
// These tests are what stands between those two facts and a factor of a
// hundred.
//
// They are written against EUR on purpose. The deployment currency is IDR,
// which is zero-decimal — minor units and major units are the same number — so
// every one of these passes under IDR whether the conversion is there or not.
// That is exactly how the bug got as far as it did. If this suite is ever
// "simplified" to run against the deployment currency alone, it stops testing
// anything.

// storedMinor reads what the database actually holds for one line, which is the
// half of the round trip no HTTP response can show.
func storedMinor(t *testing.T, pool *pgxpool.Pool, id string) (unit, paid int64) {
	t.Helper()
	if err := pool.QueryRow(t.Context(),
		`SELECT unit, paid FROM budget_items WHERE id = $1`, id).Scan(&unit, &paid); err != nil {
		t.Fatalf("read stored minor units: %v", err)
	}
	return unit, paid
}

// TestMoneyCrossesTheBoundaryInMajorUnits is the defect: the store's bigint is
// cents, the browser's number is euros, and passing one straight through as the
// other is a hundredfold error in every figure.
func TestMoneyCrossesTheBoundaryInMajorUnits(t *testing.T) {
	h, pool := newAPIServer(t)

	item := created(t, h, "budget-items",
		`{"item":"Venue deposit","vendor":"Example Hall","unit":"250.50","qty":1,"paid":"5.07"}`)
	id := str(t, item, "id")

	// Out: exactly what went in, to the cent.
	if got := str(t, item, "unit"); got != "250.50" {
		t.Errorf("unit = %q, want %q", got, "250.50")
	}
	if got := str(t, item, "paid"); got != "5.07" {
		t.Errorf("paid = %q, want %q", got, "5.07")
	}

	// Down: minor units, unchanged, which is the part of the design that was
	// already right and must stay that way.
	unit, paid := storedMinor(t, pool, id)
	if unit != 25050 || paid != 507 {
		t.Errorf("stored (unit, paid) = (%d, %d), want (25050, 507) minor units", unit, paid)
	}

	// And back out again through the read the browser actually uses.
	res := call(t, h, http.MethodGet, "/api/v1/plan", "")
	if res.status != http.StatusOK {
		t.Fatalf("GET /api/v1/plan -> %d\n%s", res.status, res.body)
	}
	for _, want := range []string{`"unit":"250.50"`, `"paid":"5.07"`} {
		if !strings.Contains(string(res.body), want) {
			t.Errorf("plan is missing %s:\n%s", want, res.body)
		}
	}

	// A patch is the same boundary in the other direction.
	patched := call(t, h, http.MethodPatch, "/api/v1/budget-items/"+id, `{"revision":1,"paid":"250.50"}`)
	if patched.status != http.StatusOK {
		t.Fatalf("PATCH -> %d\n%s", patched.status, patched.body)
	}
	if got := str(t, decode(t, patched), "paid"); got != "250.50" {
		t.Errorf("patched paid = %q, want %q", got, "250.50")
	}
	if _, paid := storedMinor(t, pool, id); paid != 25050 {
		t.Errorf("stored paid = %d, want 25050 minor units", paid)
	}
}

// TestMoneyInAZeroDecimalCurrencyIsUnchanged: for IDR, minor and major units
// are the same number, so nothing moves. This is the trap rather than the
// proof — it would pass just as well against the bug it exists to rule out —
// and it is here to show the conversion does not invent decimals where the
// currency has none.
func TestMoneyInAZeroDecimalCurrencyIsUnchanged(t *testing.T) {
	h, pool := newAPIServerIn(t, "IDR")

	item := created(t, h, "budget-items",
		`{"item":"Venue deposit","unit":"750000","qty":1,"paid":"150000"}`)
	id := str(t, item, "id")

	if got := str(t, item, "unit"); got != "750000" {
		t.Errorf("unit = %q, want %q — no decimal point on a zero-decimal currency", got, "750000")
	}
	if got := str(t, item, "paid"); got != "150000" {
		t.Errorf("paid = %q, want %q", got, "150000")
	}
	if unit, paid := storedMinor(t, pool, id); unit != 750000 || paid != 150000 {
		t.Errorf("stored (unit, paid) = (%d, %d), want (750000, 150000)", unit, paid)
	}

	// Rupiah have no sen in practice, so a figure carrying them means the
	// column was misread rather than that it needs rounding.
	res := call(t, h, http.MethodPost, "/api/v1/budget-items", `{"item":"Flowers","unit":"750000.50"}`)
	if res.status != http.StatusBadRequest {
		t.Errorf("cents against a zero-decimal currency -> %d, want 400\n%s", res.status, res.body)
	}
}

// TestOddCentsDoNotDrift walks the values a double cannot hold exactly. Every
// one of these has no finite binary form; a boundary that went through a float
// returns them a cent out, or with a tail of nines.
func TestOddCentsDoNotDrift(t *testing.T) {
	h, pool := newAPIServer(t)

	for _, tc := range []struct {
		major string
		minor int64
	}{
		{"0.01", 1},
		{"0.07", 7},
		{"0.10", 10},
		{"19.99", 1999},
		{"1234.56", 123456},
		{"8675.309", 0}, // rejected: three decimals, EUR has two
		{"99999999.99", 9999999999},
	} {
		t.Run(tc.major, func(t *testing.T) {
			res := call(t, h, http.MethodPost, "/api/v1/budget-items",
				`{"item":"Venue deposit","unit":"`+tc.major+`","qty":1}`)
			if tc.minor == 0 {
				if res.status != http.StatusBadRequest {
					t.Fatalf("-> %d, want 400 for more decimals than EUR has\n%s", res.status, res.body)
				}
				return
			}
			if res.status != http.StatusCreated {
				t.Fatalf("-> %d\n%s", res.status, res.body)
			}
			row := decode(t, res)
			if got := str(t, row, "unit"); got != tc.major {
				t.Errorf("unit = %q, want %q — the value drifted crossing the boundary", got, tc.major)
			}
			if unit, _ := storedMinor(t, pool, str(t, row, "id")); unit != tc.minor {
				t.Errorf("stored unit = %d, want %d minor units", unit, tc.minor)
			}
		})
	}
}

// TestTotalsAgreeWithTheLineItems: a budget's whole purpose is that the lines
// add up. Summing what this API reports, in the units it reports them in, must
// give the same answer as the database — and the figures here are chosen so
// that a summation done in major-unit floats does not (45.33 x 40 comes to
// 1813.1999999999998, and 4.07 x 40 to 162.79999999999998).
//
// Integer quantities on purpose: a fractional qty would make this a test about
// rounding rather than about the money boundary.
func TestTotalsAgreeWithTheLineItems(t *testing.T) {
	h, pool := newAPIServer(t)

	for _, line := range []string{
		`{"item":"Venue deposit","unit":"2500.00","qty":1,"paid":"500.00"}`,
		`{"item":"Catering","unit":"45.33","qty":40,"paid":"0.00"}`,
		`{"item":"Printed invitations","unit":"4.07","qty":40,"paid":"162.80"}`,
	} {
		created(t, h, "budget-items", line)
	}

	res := call(t, h, http.MethodGet, "/api/v1/plan", "")
	if res.status != http.StatusOK {
		t.Fatalf("GET /api/v1/plan -> %d\n%s", res.status, res.body)
	}
	var plan struct {
		BudgetItems []struct {
			Unit string  `json:"unit"`
			Qty  float64 `json:"qty"`
			Paid string  `json:"paid"`
		} `json:"budgetItems"`
	}
	if err := json.Unmarshal(res.body, &plan); err != nil {
		t.Fatalf("plan: %v\n%s", err, res.body)
	}
	if len(plan.BudgetItems) != 3 {
		t.Fatalf("budgetItems = %d, want 3", len(plan.BudgetItems))
	}

	var committed, paid int64
	for _, line := range plan.BudgetItems {
		unit, err := store.ParseMajor("EUR", line.Unit)
		if err != nil {
			t.Fatalf("unit %q is not a decimal the API's own parser accepts: %v", line.Unit, err)
		}
		p, err := store.ParseMajor("EUR", line.Paid)
		if err != nil {
			t.Fatalf("paid %q is not a decimal the API's own parser accepts: %v", line.Paid, err)
		}
		committed += unit * int64(line.Qty)
		paid += p
	}

	if committed != 447600 {
		t.Errorf("committed = %d minor units (%s), want 447600",
			committed, store.FormatMajor("EUR", committed))
	}
	if paid != 66280 {
		t.Errorf("paid = %d minor units (%s), want 66280", paid, store.FormatMajor("EUR", paid))
	}

	// The same sums taken from the column the figures actually live in. If
	// these disagree, the boundary is losing something on the way out.
	var dbCommitted, dbPaid int64
	if err := pool.QueryRow(t.Context(),
		`SELECT COALESCE(sum(unit * qty), 0)::bigint, COALESCE(sum(paid), 0)::bigint FROM budget_items`).
		Scan(&dbCommitted, &dbPaid); err != nil {
		t.Fatalf("sum the stored lines: %v", err)
	}
	if dbCommitted != committed || dbPaid != paid {
		t.Errorf("API totals (%d, %d) disagree with the stored lines (%d, %d)",
			committed, paid, dbCommitted, dbPaid)
	}
}

// TestACreateThatNamesItsIdCanBeSentTwice is the answer that never arrived.
//
// A POST that commits and whose answer is lost on the way back is sent again
// by the browser, which has no way to know the row is already stored. Every
// send is a create, so the second one used to add a second budget line: a cost
// counted twice in every total, in a tool whose whole point is the total, and
// nothing anywhere saying so. A create that names its own id is recognisable
// when it goes again, and the duplicate key is the proof that this is the
// same create rather than a second line somebody typed.
func TestACreateThatNamesItsIdCanBeSentTwice(t *testing.T) {
	h, pool := newAPIServer(t)

	id := uuid.New().String()
	body := `{"id":"` + id + `","item":"Venue deposit","unit":"2500.00","qty":1}`

	first := call(t, h, http.MethodPost, "/api/v1/budget-items", body)
	if first.status != http.StatusCreated {
		t.Fatalf("POST -> %d, want 201\n%s", first.status, first.body)
	}
	if got := str(t, decode(t, first), "id"); got != id {
		t.Errorf("id = %q, want the one the create named, %q", got, id)
	}

	// The same request again, which is exactly what the browser sends when the
	// answer above is lost. 200 rather than 201, because nothing was stored.
	second := call(t, h, http.MethodPost, "/api/v1/budget-items", body)
	if second.status != http.StatusOK {
		t.Fatalf("the same create again -> %d, want 200\n%s", second.status, second.body)
	}
	row := decode(t, second)
	if got := str(t, row, "id"); got != id {
		t.Errorf("id = %q, want %q", got, id)
	}
	if got := num(t, row, "revision"); got != 1 {
		t.Errorf("revision = %v, want the stored row's 1", got)
	}

	plan := decode(t, call(t, h, http.MethodGet, "/api/v1/plan", ""))
	items, _ := plan["budgetItems"].([]any)
	if len(items) != 1 {
		t.Errorf("the plan holds %d budget lines, want the one line that was created", len(items))
	}

	// And it happened once. A second entry would put the line in the history
	// twice, which is where anybody arguing about the money looks.
	var creates int
	if err := pool.QueryRow(t.Context(),
		`SELECT count(*) FROM change_log
		  WHERE entity = 'budget_items' AND entity_id = $1 AND action = 'create'`,
		id).Scan(&creates); err != nil {
		t.Fatalf("read the history: %v", err)
	}
	if creates != 1 {
		t.Errorf("the history holds %d creates for the line, want 1", creates)
	}
}

// TestARepeatedCreateAnswersWithTheRowAsStored: the retry carries the row as
// it looks now, which is not always what committed the first time, because
// somebody kept typing while the answer was not arriving. The answer is the
// stored row, so the browser takes that as the version both sides agree on
// and sends the difference as an ordinary patch afterwards. Merging here
// instead would be a write nobody asked for, made from a body whose revision
// means nothing.
func TestARepeatedCreateAnswersWithTheRowAsStored(t *testing.T) {
	h, _ := newAPIServer(t)

	id := uuid.New().String()
	first := call(t, h, http.MethodPost, "/api/v1/tasks",
		`{"id":"`+id+`","name":"Book the band","owner":"Ada"}`)
	if first.status != http.StatusCreated {
		t.Fatalf("POST -> %d, want 201\n%s", first.status, first.body)
	}

	second := call(t, h, http.MethodPost, "/api/v1/tasks",
		`{"id":"`+id+`","name":"Book the band and the lights","owner":"Ada"}`)
	if second.status != http.StatusOK {
		t.Fatalf("the create again -> %d, want 200\n%s", second.status, second.body)
	}
	if got := str(t, decode(t, second), "name"); got != "Book the band" {
		t.Errorf("name = %q, want the stored row's %q", got, "Book the band")
	}
}

// TestAUniqueViolationThatIsNotTheIdIsStillAConflict draws the line around the
// answer above. A duplicate primary key is a create this server has already
// done; every other duplicate key is a different row colliding with one that
// is there, and telling the caller it succeeded would hand them a row that is
// not the one they sent.
func TestAUniqueViolationThatIsNotTheIdIsStillAConflict(t *testing.T) {
	h, _ := newAPIServer(t)

	item := created(t, h, "budget-items", `{"item":"Cake","unit":"100.00","qty":1}`)
	line := str(t, item, "id")
	created(t, h, "programme-entries",
		`{"title":"Cutting the cake","position":0,"budgetItemId":"`+line+`"}`)

	// A second entry against the same budget line, under an id of its own.
	// What it violates is programme_entries' unique index on budget_item_id,
	// not the table's primary key.
	res := call(t, h, http.MethodPost, "/api/v1/programme-entries",
		`{"id":"`+uuid.New().String()+`","title":"Cutting the cake again","position":1,"budgetItemId":"`+line+`"}`)
	if res.status != http.StatusConflict {
		t.Fatalf("a second entry on the same line -> %d, want 409\n%s", res.status, res.body)
	}
	if got := str(t, decode(t, res), "error"); got != errConflict {
		t.Errorf("error = %q, want %q", got, errConflict)
	}
}

// TestACreateWithAnIdThatIsNotAUuidIsRefused: a client that meant to name its
// row and misspelt the id would otherwise be given a server-minted one and
// silently lose the protection it was asking for, and its next retry would be
// the duplicate this whole path exists to prevent.
func TestACreateWithAnIdThatIsNotAUuidIsRefused(t *testing.T) {
	h, _ := newAPIServer(t)

	res := call(t, h, http.MethodPost, "/api/v1/tasks", `{"id":"task-7","name":"Book the band"}`)
	if res.status != http.StatusBadRequest {
		t.Fatalf("POST with a malformed id -> %d, want 400\n%s", res.status, res.body)
	}
	if got := str(t, decode(t, res), "error"); got != errBadRequest {
		t.Errorf("error = %q, want %q", got, errBadRequest)
	}
}

// TestAPatchStillIgnoresTheIdInItsBody: only a create reads it. A patch names
// the row in the path, and a client echoing a whole row back must not be able
// to move it somewhere else by editing one field of what it sends.
func TestAPatchStillIgnoresTheIdInItsBody(t *testing.T) {
	h, _ := newAPIServer(t)

	note := created(t, h, "notes", `{"text":"Check the parking.","position":0}`)
	id := str(t, note, "id")
	elsewhere := uuid.New().String()

	res := call(t, h, http.MethodPatch, "/api/v1/notes/"+id,
		`{"id":"`+elsewhere+`","revision":1,"text":"Check the parking twice."}`)
	if res.status != http.StatusOK {
		t.Fatalf("PATCH -> %d, want 200\n%s", res.status, res.body)
	}
	if got := str(t, decode(t, res), "id"); got != id {
		t.Errorf("id = %q, want the row in the path, %q", got, id)
	}

	plan := call(t, h, http.MethodGet, "/api/v1/plan", "")
	if strings.Contains(string(plan.body), elsewhere) {
		t.Errorf("the patch moved the note to the id in its body:\n%s", plan.body)
	}
}

// TestStaleRevisionIs409 is the conflict path end to end: two people holding
// revision 1, the first write lands, the second is refused and told what the
// row now says rather than overwriting it.
func TestStaleRevisionIs409(t *testing.T) {
	h, _ := newAPIServer(t)

	item := created(t, h, "budget-items", `{"item":"Venue deposit","unit":"250.00","qty":1}`)
	id := str(t, item, "id")

	first := call(t, h, http.MethodPatch, "/api/v1/budget-items/"+id, `{"revision":1,"unit":"300.00"}`)
	if first.status != http.StatusOK {
		t.Fatalf("first write -> %d\n%s", first.status, first.body)
	}

	second := call(t, h, http.MethodPatch, "/api/v1/budget-items/"+id, `{"revision":1,"unit":"275.00"}`)
	if second.status != http.StatusConflict {
		t.Fatalf("second write at a stale revision -> %d, want 409\n%s", second.status, second.body)
	}
	if cc := second.header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("409 Cache-Control = %q, want no-store", cc)
	}

	var conflict struct {
		Error   string         `json:"error"`
		Current map[string]any `json:"current"`
	}
	if err := json.Unmarshal(second.body, &conflict); err != nil {
		t.Fatalf("409 body is not the documented shape: %v\n%s", err, second.body)
	}
	if conflict.Error != "stale_revision" {
		t.Errorf("error = %q, want stale_revision", conflict.Error)
	}
	if got := str(t, conflict.Current, "unit"); got != "300.00" {
		t.Errorf("current.unit = %q, want the winning write's 300.00", got)
	}
	if got := num(t, conflict.Current, "revision"); got != 2 {
		t.Errorf("current.revision = %v, want 2", got)
	}
	if str(t, conflict.Current, "id") != id {
		t.Errorf("current.id = %q, want %q", conflict.Current["id"], id)
	}

	// And the refused write must not have landed anyway.
	plan := call(t, h, http.MethodGet, "/api/v1/plan", "")
	if !strings.Contains(string(plan.body), `"unit":"300.00"`) {
		t.Errorf("the refused write overwrote the winner:\n%s", plan.body)
	}

	// A delete carries the same check, or one person's delete discards
	// another's edit with no more ceremony than deleting a stale row.
	staleDelete := call(t, h, http.MethodDelete, "/api/v1/budget-items/"+id+"?revision=1", "")
	if staleDelete.status != http.StatusConflict {
		t.Errorf("delete at a stale revision -> %d, want 409\n%s", staleDelete.status, staleDelete.body)
	}
}

func TestDeleteRemovesTheRow(t *testing.T) {
	h, _ := newAPIServer(t)

	note := created(t, h, "notes", `{"text":"Check the parking.","position":0}`)
	id := str(t, note, "id")

	res := call(t, h, http.MethodDelete, "/api/v1/notes/"+id+"?revision=1", "")
	if res.status != http.StatusNoContent {
		t.Fatalf("delete -> %d, want 204\n%s", res.status, res.body)
	}
	if len(res.body) != 0 {
		t.Errorf("204 carried a body: %s", res.body)
	}

	plan := call(t, h, http.MethodGet, "/api/v1/plan", "")
	if strings.Contains(string(plan.body), "Check the parking.") {
		t.Errorf("the note survived its delete:\n%s", plan.body)
	}

	// Gone is a different answer from "somebody edited it first", and the
	// client has to be able to tell them apart.
	again := call(t, h, http.MethodDelete, "/api/v1/notes/"+id+"?revision=1", "")
	if again.status != http.StatusNotFound {
		t.Errorf("deleting a deleted row -> %d, want 404\n%s", again.status, again.body)
	}
}

// TestPhaseConflictIs409 is the defect migration 0009 closed, end to end.
//
// `phases` was the one shared table with no revision column, so two people
// renaming the same stage of the evening were never told apart: the second
// write simply won. It now behaves as every other collection does — a patch
// names the revision it is editing, a stale one is refused with the row as it
// now stands, and a delete carries the same check.
func TestPhaseConflictIs409(t *testing.T) {
	h, _ := newAPIServer(t)

	phase := created(t, h, "phases", `{"name":"Arrival","position":0}`)
	id := str(t, phase, "id")
	if got := num(t, phase, "revision"); got != 1 {
		t.Errorf("revision = %v, want 1 on a fresh phase", got)
	}
	if _, ok := phase["updatedAt"]; !ok {
		t.Error("a phase carries no updatedAt")
	}
	if _, ok := phase["updatedBy"]; !ok {
		t.Error("a phase carries no updatedBy")
	}

	// Both people are holding revision 1. Ada renames it first.
	first := call(t, h, http.MethodPatch, "/api/v1/phases/"+id, `{"revision":1,"name":"Guests arrive"}`)
	if first.status != http.StatusOK {
		t.Fatalf("first write -> %d\n%s", first.status, first.body)
	}
	if got := num(t, decode(t, first), "revision"); got != 2 {
		t.Errorf("revision = %v, want 2 after a write", got)
	}

	second := call(t, h, http.MethodPatch, "/api/v1/phases/"+id, `{"revision":1,"name":"Doors open"}`)
	if second.status != http.StatusConflict {
		t.Fatalf("second write at a stale revision -> %d, want 409\n%s", second.status, second.body)
	}

	var conflict struct {
		Error   string         `json:"error"`
		Current map[string]any `json:"current"`
	}
	if err := json.Unmarshal(second.body, &conflict); err != nil {
		t.Fatalf("409 body is not the documented shape: %v\n%s", err, second.body)
	}
	if conflict.Error != "stale_revision" {
		t.Errorf("error = %q, want stale_revision", conflict.Error)
	}
	// Not just "you lost", but what the row now says — the thing the client
	// reconciles against instead of refetching the whole plan.
	if got := str(t, conflict.Current, "name"); got != "Guests arrive" {
		t.Errorf("current.name = %q, want the winning write's", got)
	}
	if got := num(t, conflict.Current, "revision"); got != 2 {
		t.Errorf("current.revision = %v, want 2", got)
	}
	if str(t, conflict.Current, "id") != id {
		t.Errorf("current.id = %q, want %q", conflict.Current["id"], id)
	}

	// And the refused rename must not have landed anyway.
	plan := call(t, h, http.MethodGet, "/api/v1/plan", "")
	if strings.Contains(string(plan.body), "Doors open") {
		t.Errorf("the refused write overwrote the winner:\n%s", plan.body)
	}

	if stale := call(t, h, http.MethodDelete, "/api/v1/phases/"+id+"?revision=1", ""); stale.status != http.StatusConflict {
		t.Errorf("delete at a stale revision -> %d, want 409\n%s", stale.status, stale.body)
	}
	if del := call(t, h, http.MethodDelete, "/api/v1/phases/"+id+"?revision=2", ""); del.status != http.StatusNoContent {
		t.Errorf("delete at the current revision -> %d, want 204\n%s", del.status, del.body)
	}
}

func TestUnknownIDIsNotFound(t *testing.T) {
	h, _ := newAPIServer(t)
	missing := uuid.New().String()

	for _, tc := range []struct {
		name           string
		method, path   string
		body           string
		wantStatusCode int
	}{
		{"patch", http.MethodPatch, "/api/v1/tasks/" + missing, `{"revision":1,"name":"Gone"}`, http.StatusNotFound},
		{"delete", http.MethodDelete, "/api/v1/tasks/" + missing + "?revision=1", "", http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := call(t, h, tc.method, tc.path, tc.body)
			if res.status != tc.wantStatusCode {
				t.Fatalf("-> %d, want %d\n%s", res.status, tc.wantStatusCode, res.body)
			}
			if got := str(t, decode(t, res), "error"); got != "not_found" {
				t.Errorf("error = %q, want not_found", got)
			}
		})
	}
}

func TestBadRequestsAreRefused(t *testing.T) {
	h, _ := newAPIServer(t)

	task := created(t, h, "tasks", `{"name":"Book the photographer"}`)
	taskPath := "/api/v1/tasks/" + str(t, task, "id")
	phase := created(t, h, "phases", `{"name":"Arrival"}`)
	phasePath := "/api/v1/phases/" + str(t, phase, "id")

	cases := []struct {
		name         string
		method, path string
		body         string
		want         int
	}{
		{"malformed json", http.MethodPost, "/api/v1/notes", `{"text": `, http.StatusBadRequest},
		{"not an object", http.MethodPost, "/api/v1/notes", `["text"]`, http.StatusBadRequest},
		{"empty body", http.MethodPost, "/api/v1/notes", "", http.StatusBadRequest},
		{"trailing content", http.MethodPost, "/api/v1/notes", `{"text":"a"}{"text":"b"}`, http.StatusBadRequest},
		// A brace or a bracket too many is a client that concatenated its body
		// wrongly, and is trailing content as much as a second object is.
		{"a brace too many", http.MethodPost, "/api/v1/notes", `{"text":"a"}}`, http.StatusBadRequest},
		{"a bracket too many", http.MethodPost, "/api/v1/notes", `{"text":"a"}]`, http.StatusBadRequest},
		// A misspelt field that silently did nothing is the bug nobody catches.
		{"unknown field", http.MethodPost, "/api/v1/budget-items", `{"item":"Cake","unitt":"5.00"}`, http.StatusBadRequest},
		{"null on a non-nullable field", http.MethodPost, "/api/v1/budget-items", `{"item":null}`, http.StatusBadRequest},
		{"null money", http.MethodPost, "/api/v1/budget-items", `{"unit":null}`, http.StatusBadRequest},
		{"wrong type", http.MethodPost, "/api/v1/budget-items", `{"unit":"lots"}`, http.StatusBadRequest},
		// Money is a decimal string in major units and nothing else. A JSON
		// number is a float in the browser's parser, and 25050 sent where
		// "250.50" was meant is the factor-of-a-hundred nobody notices.
		{"money as a JSON number", http.MethodPost, "/api/v1/budget-items", `{"unit":25050}`, http.StatusBadRequest},
		// Cents beyond what the currency has means the client has the wrong
		// idea about the amount, not that it needs rounding help.
		{"more decimals than EUR has", http.MethodPost, "/api/v1/budget-items", `{"unit":"250.005"}`, http.StatusBadRequest},
		{"money that is not a number at all", http.MethodPost, "/api/v1/budget-items", `{"paid":"1,250.00"}`, http.StatusBadRequest},
		{"unknown task status", http.MethodPost, "/api/v1/tasks", `{"name":"x","status":"maybe"}`, http.StatusBadRequest},
		{"date with a time on it", http.MethodPost, "/api/v1/tasks", `{"name":"x","due":"2030-01-15T00:00:00Z"}`, http.StatusBadRequest},
		{"id that is not a uuid", http.MethodPatch, "/api/v1/tasks/not-a-uuid", `{"revision":1}`, http.StatusBadRequest},
		// Without a revision a patch would overwrite whatever is there now.
		{"patch without a revision", http.MethodPatch, taskPath, `{"name":"x"}`, http.StatusBadRequest},
		{"delete without a revision", http.MethodDelete, taskPath, "", http.StatusBadRequest},
		{"delete with a nonsense revision", http.MethodDelete, taskPath + "?revision=soon", "", http.StatusBadRequest},
		// Phases are held to the same rule as everything else since 0009; they
		// used to be the collection where forgetting the revision was fine.
		{"phase patch without a revision", http.MethodPatch, phasePath, `{"name":"Guests arrive"}`, http.StatusBadRequest},
		{"phase delete without a revision", http.MethodDelete, phasePath, "", http.StatusBadRequest},
		// A reference to a row that does not exist is the caller's mistake, and
		// reporting it as a 500 tells them this server is broken instead.
		{"unknown phase", http.MethodPost, "/api/v1/budget-items",
			`{"item":"Cake","phaseId":"` + uuid.New().String() + `"}`, http.StatusBadRequest},
		// A figure past what numeric(12,3) holds, for the same reason.
		{"qty past its column", http.MethodPost, "/api/v1/budget-items", `{"item":"Cake","qty":1000000000}`, http.StatusBadRequest},
		{"qty with a fourth decimal", http.MethodPost, "/api/v1/budget-items", `{"item":"Cake","qty":0.3333}`, http.StatusBadRequest},
		// A NUL byte reaches no bound this API checks. Postgres refuses it as a
		// data exception, and that class has to arrive as a 400 as well, or the
		// next column added brings the 500 back.
		{"a NUL byte in a text field", http.MethodPost, "/api/v1/budget-items", `{"item":"Ca\u0000ke"}`, http.StatusBadRequest},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := call(t, h, tc.method, tc.path, tc.body)
			if res.status != tc.want {
				t.Fatalf("-> %d, want %d\n%s", res.status, tc.want, res.body)
			}
			body := decode(t, res)
			if _, ok := body["error"]; !ok {
				t.Errorf("error response carries no code: %s", res.body)
			}
		})
	}

	// A method the collection does not serve is a 405, not a 404: the path is
	// real, the verb is not.
	res := call(t, h, http.MethodPut, "/api/v1/notes", `{}`)
	if res.status != http.StatusMethodNotAllowed {
		t.Fatalf("PUT /api/v1/notes -> %d, want 405", res.status)
	}
	if allow := res.header.Get("Allow"); !strings.Contains(allow, http.MethodPost) {
		t.Errorf("405 Allow = %q, want it to name the verbs the collection serves", allow)
	}
	// The mux writes this one itself, so it never reaches a handler — the
	// header has to be set on the way in for it to be here at all.
	if cc := res.header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("405 Cache-Control = %q, want no-store", cc)
	}

	// Same for a path nothing serves.
	unknown := call(t, h, http.MethodGet, "/api/v1/nonexistent", "")
	if unknown.status != http.StatusNotFound {
		t.Errorf("GET /api/v1/nonexistent -> %d, want 404", unknown.status)
	}
	if cc := unknown.header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("API 404 Cache-Control = %q, want no-store", cc)
	}
}

// TestWholeRowCanBeSentBack: strict decoding must not make the obvious client
// illegal — read a row, change one field, send the whole thing back.
func TestWholeRowCanBeSentBack(t *testing.T) {
	h, _ := newAPIServer(t)

	res := call(t, h, http.MethodPost, "/api/v1/tasks", `{"name":"Send invitations","owner":"Grace"}`)
	if res.status != http.StatusCreated {
		t.Fatalf("create -> %d\n%s", res.status, res.body)
	}

	row := decode(t, res)
	row["status"] = "in-progress"
	body, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	patched := call(t, h, http.MethodPatch, "/api/v1/tasks/"+str(t, row, "id"), string(body))
	if patched.status != http.StatusOK {
		t.Fatalf("patching with the whole row -> %d\n%s", patched.status, patched.body)
	}
	if got := str(t, decode(t, patched), "status"); got != "in-progress" {
		t.Errorf("status = %q", got)
	}
}

func TestRequestBodyIsBounded(t *testing.T) {
	h, _ := newAPIServer(t)

	body := `{"text":"` + strings.Repeat("a", maxRequestBytes) + `"}`
	res := call(t, h, http.MethodPost, "/api/v1/notes", body)
	if res.status != http.StatusRequestEntityTooLarge {
		t.Errorf("an oversized body -> %d, want 413", res.status)
	}
}

// TestReadyzChecksTheDatabase: readiness has to mean "can serve traffic", and
// an instance that cannot reach the database cannot. Liveness deliberately does
// not check, or an outage becomes a restart loop.
func TestReadyzChecksTheDatabase(t *testing.T) {
	h, pool := newAPIServer(t)

	if res := call(t, h, http.MethodGet, "/readyz", ""); res.status != http.StatusOK {
		t.Fatalf("/readyz with a reachable database -> %d", res.status)
	}

	pool.Close() // idempotent; pgtest closes it again during cleanup

	if res := call(t, h, http.MethodGet, "/readyz", ""); res.status != http.StatusServiceUnavailable {
		t.Errorf("/readyz with the database gone -> %d, want 503", res.status)
	}
	if res := call(t, h, http.MethodGet, "/healthz", ""); res.status != http.StatusOK {
		t.Error("/healthz failed on a database outage, which turns the outage into a restart loop")
	}
}

// The plan is not public. It holds people's names against amounts of money they
// owe each other, and for several releases every route below was readable and
// writable by anyone who could reach the URL — the middleware, the roles and the
// sessions all existed and nothing here called any of them.
//
// This asserts the subtree, not a list of routes, because the failure was never
// one route being wrong: it was the guard being absent, and a per-route test
// would have passed for every route that existed on the day it was written.
func TestTheAPIRefusesAnyoneNotSignedIn(t *testing.T) {
	h, _ := newAPIServerUnauthenticated(t)

	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/plan", ""},
		{http.MethodGet, "/api/v1/events", ""},
		{http.MethodPost, "/api/v1/budget-items", `{"item":"Venue deposit"}`},
		{http.MethodPatch, "/api/v1/budget-items/" + uuid.Nil.String(), `{"revision":1}`},
		{http.MethodDelete, "/api/v1/budget-items/" + uuid.Nil.String() + "?revision=1", ""},
		{http.MethodPost, "/api/v1/sponsors", `{"code":"Rose"}`},
		{http.MethodPost, "/api/v1/tasks", `{"name":"Book the venue"}`},
		{http.MethodPost, "/api/v1/notes", `{"text":"Lock the caterer"}`},
		{http.MethodPost, "/api/v1/phases", `{"name":"Arrival"}`},
		{http.MethodPost, "/api/v1/programme-entries", `{"title":"Speeches"}`},
		{http.MethodPatch, "/api/v1/settings", `{"revision":1,"ceiling":"1.00"}`},
	} {
		res := call(t, h, c.method, c.path, c.body)
		if res.status != http.StatusUnauthorized {
			t.Errorf("%s %s -> %d, want 401", c.method, c.path, res.status)
		}
	}
}

// A viewer may look and may not touch. The role exists precisely so somebody
// can be shown the budget without being able to move a figure in it.
func TestAViewerMayReadAndMayNotWrite(t *testing.T) {
	h, _ := newAPIServerAs(t, store.RoleViewer)

	if res := call(t, h, http.MethodGet, "/api/v1/plan", ""); res.status != http.StatusOK {
		t.Errorf("GET /plan as a viewer -> %d, want 200", res.status)
	}
	res := call(t, h, http.MethodPost, "/api/v1/budget-items", `{"item":"Venue deposit"}`)
	if res.status != http.StatusForbidden {
		t.Errorf("POST as a viewer -> %d, want 403", res.status)
	}
}

// TestUpdatedByIsTheSessionAccount is the promise `updatedBy` makes: the id of
// the account that wrote the row last.
//
// The session reaches the store through the context, and for a while the four
// tables that carry the column took it from a legacy argument the API always
// leaves nil. So every row a signed-in person wrote said nobody had, an edit
// erased whatever attribution an older row still held, and the only test on the
// field asked whether the key existed. The column is read back here as well as
// the response, because the response is only the statement's RETURNING and the
// subject export matches on what is stored.
func TestUpdatedByIsTheSessionAccount(t *testing.T) {
	h, a, pool := newAPIServerParts(t, "EUR")
	st := store.New(pool)
	asEditor := authedAs(t, h, st, a, store.RoleEditor)
	asAdmin := authedAs(t, h, st, a, store.RoleAdmin)

	accountID := func(role store.Role) string {
		t.Helper()
		var id uuid.UUID
		if err := pool.QueryRow(t.Context(),
			`SELECT id FROM users WHERE email = $1`, string(role)+"@example.test").Scan(&id); err != nil {
			t.Fatalf("read the %s account: %v", role, err)
		}
		return id.String()
	}
	editor, admin := accountID(store.RoleEditor), accountID(store.RoleAdmin)

	for _, c := range []struct{ collection, table, create, patch string }{
		{"budget-items", "budget_items", `{"item":"Venue deposit","unit":"250.00"}`, `{"revision":1,"item":"Venue balance"}`},
		{"sponsors", "sponsors", `{"code":"Rose","name":"Ada"}`, `{"revision":1,"name":"Grace"}`},
		{"tasks", "tasks", `{"name":"Book the band"}`, `{"revision":1,"name":"Book the quartet"}`},
		{"phases", "phases", `{"name":"Arrival"}`, `{"revision":1,"name":"Guests arrive"}`},
	} {
		stored := func(id string) string {
			t.Helper()
			var by *uuid.UUID
			// The table name is one of the four literals above, never input.
			if err := pool.QueryRow(t.Context(),
				`SELECT updated_by FROM `+c.table+` WHERE id = $1`, id).Scan(&by); err != nil {
				t.Fatalf("%s: read updated_by: %v", c.collection, err)
			}
			if by == nil {
				return "NULL"
			}
			return by.String()
		}

		row := created(t, asEditor, c.collection, c.create)
		id := str(t, row, "id")
		if got, _ := row["updatedBy"].(string); got != editor {
			t.Errorf("%s: POST answered updatedBy = %v, want the editor %s", c.collection, row["updatedBy"], editor)
		}
		if got := stored(id); got != editor {
			t.Errorf("%s: POST stored updated_by = %s, want the editor %s", c.collection, got, editor)
		}

		// Somebody else edits it, and the row has to say so rather than keep
		// naming the person who created it.
		res := call(t, asAdmin, http.MethodPatch, "/api/v1/"+c.collection+"/"+id, c.patch)
		if res.status != http.StatusOK {
			t.Fatalf("%s: PATCH -> %d, want 200\n%s", c.collection, res.status, res.body)
		}
		patched := decode(t, res)
		if got, _ := patched["updatedBy"].(string); got != admin {
			t.Errorf("%s: PATCH answered updatedBy = %v, want the admin %s", c.collection, patched["updatedBy"], admin)
		}
		if got := stored(id); got != admin {
			t.Errorf("%s: PATCH stored updated_by = %s, want the admin %s", c.collection, got, admin)
		}
	}
}
