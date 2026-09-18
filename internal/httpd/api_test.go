package httpd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
func newAPIServer(t *testing.T) (http.Handler, *pgxpool.Pool) {
	t.Helper()

	pool := pgtest.Pool(t)
	if _, err := migrate.Run(t.Context(), pool, nil); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	s, err := New(
		config.Config{EventName: "Ada's Retirement", Currency: "EUR", Locale: "en-US"},
		web.FS(),
		WithStore(store.New(pool)),
	)
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	return s.Handler(), pool
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
		"unit": 25050,
		"qty": 2.5,
		"paid": 5000,
		"lockBy": "2030-01-31",
		"note": "Balance due one month before",
		"position": 0,
		"sponsors": ["`+str(t, ada, "id")+`", "`+str(t, grace, "id")+`"]
	}`)
	created(t, h, "programme-entries", `{"title":"Cutting the cake","position":0,"budgetItemId":"`+str(t, item, "id")+`"}`)
	created(t, h, "tasks", `{"name":"Confirm final guest count","owner":"Ada","due":"2030-01-15","position":0}`)
	created(t, h, "notes", `{"text":"Venue balance is due a month out.","position":0}`)

	// The created row comes back whole, in the shapes the browser is promised.
	if got := num(t, item, "unit"); got != 25050 {
		t.Errorf("unit = %v, want 25050 minor units straight through", got)
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
			Ceiling  int64 `json:"ceiling"`
			Revision int64 `json:"revision"`
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
		"unit": 25000, "qty": 2, "paid": 5000,
		"lockBy": "2030-01-31", "note": "Balance due one month before",
		"sponsors": ["`+str(t, ada, "id")+`", "`+str(t, grace, "id")+`"]
	}`)

	res := call(t, h, http.MethodPatch, "/api/v1/budget-items/"+str(t, item, "id"),
		`{"revision": 1, "unit": 30000}`)
	if res.status != http.StatusOK {
		t.Fatalf("PATCH -> %d\n%s", res.status, res.body)
	}
	patched := decode(t, res)

	if got := num(t, patched, "unit"); got != 30000 {
		t.Errorf("unit = %v, want the patched 30000", got)
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
	if got := num(t, patched, "paid"); got != 5000 {
		t.Errorf("paid = %v, want it untouched", got)
	}
}

// TestCreateDefaultsQtyToOne: the column defaults to 1 but the store always
// sends the value it was handed, so an omitted qty would be written as zero —
// and a line whose quantity is zero totals nothing, quietly.
func TestCreateDefaultsQtyToOne(t *testing.T) {
	h, _ := newAPIServer(t)

	item := created(t, h, "budget-items", `{"item":"Welcome signage","unit":500}`)
	if got := num(t, item, "qty"); got != 1 {
		t.Errorf("qty = %v, want the column default of 1", got)
	}
	// An explicit zero is still a zero; only absence takes the default.
	zero := created(t, h, "budget-items", `{"item":"Cancelled extra","unit":500,"qty":0}`)
	if got := num(t, zero, "qty"); got != 0 {
		t.Errorf("qty = %v, want the explicit 0", got)
	}
}

// TestStaleRevisionIs409 is the conflict path end to end: two people holding
// revision 1, the first write lands, the second is refused and told what the
// row now says rather than overwriting it.
func TestStaleRevisionIs409(t *testing.T) {
	h, _ := newAPIServer(t)

	item := created(t, h, "budget-items", `{"item":"Venue deposit","unit":25000,"qty":1}`)
	id := str(t, item, "id")

	first := call(t, h, http.MethodPatch, "/api/v1/budget-items/"+id, `{"revision":1,"unit":30000}`)
	if first.status != http.StatusOK {
		t.Fatalf("first write -> %d\n%s", first.status, first.body)
	}

	second := call(t, h, http.MethodPatch, "/api/v1/budget-items/"+id, `{"revision":1,"unit":27500}`)
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
	if got := num(t, conflict.Current, "unit"); got != 30000 {
		t.Errorf("current.unit = %v, want the winning write's 30000", got)
	}
	if got := num(t, conflict.Current, "revision"); got != 2 {
		t.Errorf("current.revision = %v, want 2", got)
	}
	if str(t, conflict.Current, "id") != id {
		t.Errorf("current.id = %q, want %q", conflict.Current["id"], id)
	}

	// And the refused write must not have landed anyway.
	plan := call(t, h, http.MethodGet, "/api/v1/plan", "")
	if !strings.Contains(string(plan.body), `"unit":30000`) {
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

// TestPhasesHaveNoRevisionCheck records the one table the schema gives no
// revision column, so its writes are last-write-wins and cannot conflict.
func TestPhasesHaveNoRevisionCheck(t *testing.T) {
	h, _ := newAPIServer(t)

	phase := created(t, h, "phases", `{"name":"Arrival","position":0}`)
	id := str(t, phase, "id")
	if _, ok := phase["revision"]; ok {
		t.Error("a phase reported a revision it does not have")
	}

	res := call(t, h, http.MethodPatch, "/api/v1/phases/"+id, `{"name":"Guests arrive"}`)
	if res.status != http.StatusOK {
		t.Fatalf("patching a phase without a revision -> %d\n%s", res.status, res.body)
	}
	if got := str(t, decode(t, res), "name"); got != "Guests arrive" {
		t.Errorf("name = %q", got)
	}

	if del := call(t, h, http.MethodDelete, "/api/v1/phases/"+id, ""); del.status != http.StatusNoContent {
		t.Errorf("deleting a phase without a revision -> %d, want 204\n%s", del.status, del.body)
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
		// A misspelt field that silently did nothing is the bug nobody catches.
		{"unknown field", http.MethodPost, "/api/v1/budget-items", `{"item":"Cake","unitt":500}`, http.StatusBadRequest},
		{"null on a non-nullable field", http.MethodPost, "/api/v1/budget-items", `{"item":null}`, http.StatusBadRequest},
		{"wrong type", http.MethodPost, "/api/v1/budget-items", `{"unit":"lots"}`, http.StatusBadRequest},
		{"unknown task status", http.MethodPost, "/api/v1/tasks", `{"name":"x","status":"maybe"}`, http.StatusBadRequest},
		{"date with a time on it", http.MethodPost, "/api/v1/tasks", `{"name":"x","due":"2030-01-15T00:00:00Z"}`, http.StatusBadRequest},
		{"id that is not a uuid", http.MethodPatch, "/api/v1/tasks/not-a-uuid", `{"revision":1}`, http.StatusBadRequest},
		// Without a revision a patch would overwrite whatever is there now.
		{"patch without a revision", http.MethodPatch, taskPath, `{"name":"x"}`, http.StatusBadRequest},
		{"delete without a revision", http.MethodDelete, taskPath, "", http.StatusBadRequest},
		{"delete with a nonsense revision", http.MethodDelete, taskPath + "?revision=soon", "", http.StatusBadRequest},
		// A reference to a row that does not exist is the caller's mistake, and
		// reporting it as a 500 tells them this server is broken instead.
		{"unknown phase", http.MethodPost, "/api/v1/budget-items",
			`{"item":"Cake","phaseId":"` + uuid.New().String() + `"}`, http.StatusBadRequest},
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
