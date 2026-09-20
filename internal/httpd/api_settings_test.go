package httpd

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PATCH /api/v1/settings, against a real Postgres like the rest of this
// package's API tests — `go test -short` skips them.
//
// The singleton is the collection the write path skipped, so these check the
// two things a client would otherwise discover in production: that the ceiling,
// the inflation buffer and the split-evenly toggle actually persist, and that
// the route behaves like every other patch while doing it.

// settingsRow reads the singleton back through the response the browser
// actually uses, which is the only read there is: GET /plan.
func settingsRow(t *testing.T, h http.Handler) map[string]any {
	t.Helper()

	res := call(t, h, http.MethodGet, "/api/v1/plan", "")
	if res.status != http.StatusOK {
		t.Fatalf("GET /api/v1/plan -> %d\n%s", res.status, res.body)
	}
	var plan struct {
		Settings map[string]any `json:"settings"`
	}
	if err := json.Unmarshal(res.body, &plan); err != nil {
		t.Fatalf("plan is not the documented shape: %v\n%s", err, res.body)
	}
	if plan.Settings == nil {
		t.Fatalf("the plan carries no settings object:\n%s", res.body)
	}
	return plan.Settings
}

// storedCeiling reads what the database actually holds, which is the half of
// the money round trip no HTTP response can show. See the note above
// storedMinor in api_test.go: an echo of the request would pass against the
// hundredfold bug these tests exist to rule out.
func storedCeiling(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var ceiling int64
	if err := pool.QueryRow(t.Context(), `SELECT ceiling FROM settings WHERE id = true`).Scan(&ceiling); err != nil {
		t.Fatalf("read the stored ceiling: %v", err)
	}
	return ceiling
}

func boolean(t *testing.T, row map[string]any, key string) bool {
	t.Helper()
	v, ok := row[key].(bool)
	if !ok {
		t.Fatalf("%q = %#v, want a boolean", key, row[key])
	}
	return v
}

// TestSettingsPatchLeavesOmittedFieldsAlone is the same rule as every other
// patch, and the reason this one reads the row first: UpdateSettings writes
// every column, so a body built from anything but the stored row would reset
// the three knobs it did not mention.
func TestSettingsPatchLeavesOmittedFieldsAlone(t *testing.T) {
	h, _ := newAPIServer(t)

	res := call(t, h, http.MethodPatch, "/api/v1/settings",
		`{"revision":1,"ceiling":"70000.00","inflationPct":4.25,"fxRate":17500.125,"splitEvenly":true}`)
	if res.status != http.StatusOK {
		t.Fatalf("PATCH -> %d\n%s", res.status, res.body)
	}
	row := decode(t, res)
	if got := num(t, row, "revision"); got != 2 {
		t.Fatalf("revision = %v, want 2 after a write", got)
	}

	res = call(t, h, http.MethodPatch, "/api/v1/settings", `{"revision":2,"inflationPct":6.25}`)
	if res.status != http.StatusOK {
		t.Fatalf("second PATCH -> %d\n%s", res.status, res.body)
	}
	patched := decode(t, res)

	if got := num(t, patched, "inflationPct"); got != 6.25 {
		t.Errorf("inflationPct = %v, want the patched 6.25", got)
	}
	if got := str(t, patched, "ceiling"); got != "70000.00" {
		t.Errorf("ceiling = %q — a patch of one knob reset the spending ceiling", got)
	}
	if got := num(t, patched, "fxRate"); got != 17500.125 {
		t.Errorf("fxRate = %v, want it untouched", got)
	}
	if !boolean(t, patched, "splitEvenly") {
		t.Error("splitEvenly = false, want it untouched")
	}
	if got := num(t, patched, "revision"); got != 3 {
		t.Errorf("revision = %v, want 3 after the second write", got)
	}

	// And it is the same row the plan reports — the point of the route is that
	// these three settle in the database rather than in a browser tab.
	plan := settingsRow(t, h)
	if got := str(t, plan, "ceiling"); got != "70000.00" {
		t.Errorf("plan settings.ceiling = %q, want the written 70000.00", got)
	}
	if got := num(t, plan, "inflationPct"); got != 6.25 {
		t.Errorf("plan settings.inflationPct = %v, want 6.25", got)
	}
	if !boolean(t, plan, "splitEvenly") {
		t.Error("plan settings.splitEvenly = false, want the written true")
	}
}

// TestSettingsMoneyCrossesTheBoundaryInMajorUnits: the ceiling is money, so it
// obeys the boundary every other figure does — major units as a decimal string
// on the wire, minor units in the column.
//
// EUR on purpose. Under the deployment currency, which is zero-decimal, this
// passes whether the conversion is there or not; see the note at api_test.go's
// money section.
func TestSettingsMoneyCrossesTheBoundaryInMajorUnits(t *testing.T) {
	h, pool := newAPIServer(t)

	res := call(t, h, http.MethodPatch, "/api/v1/settings", `{"revision":1,"ceiling":"250.50"}`)
	if res.status != http.StatusOK {
		t.Fatalf("PATCH -> %d\n%s", res.status, res.body)
	}
	if got := str(t, decode(t, res), "ceiling"); got != "250.50" {
		t.Errorf("ceiling = %q, want the major units the client sent", got)
	}
	if got := storedCeiling(t, pool); got != 25050 {
		t.Errorf("stored ceiling = %d, want 25050 minor units", got)
	}
	if got := str(t, settingsRow(t, h), "ceiling"); got != "250.50" {
		t.Errorf("plan settings.ceiling = %q, want 250.50 back out", got)
	}

	// A value with no exact binary form, which is what a boundary that went
	// through a float returns a cent out or with a tail of nines.
	res = call(t, h, http.MethodPatch, "/api/v1/settings", `{"revision":2,"ceiling":"99999999.99"}`)
	if res.status != http.StatusOK {
		t.Fatalf("PATCH -> %d\n%s", res.status, res.body)
	}
	if got := str(t, decode(t, res), "ceiling"); got != "99999999.99" {
		t.Errorf("ceiling = %q, want it exact — the value drifted crossing the boundary", got)
	}
	if got := storedCeiling(t, pool); got != 9999999999 {
		t.Errorf("stored ceiling = %d, want 9999999999 minor units", got)
	}
}

// TestSettingsMoneyInAZeroDecimalCurrency: for IDR, minor and major units are
// the same number, so the ceiling must come back without a decimal point — and
// a figure carrying sen means the client has the wrong idea about the amount.
func TestSettingsMoneyInAZeroDecimalCurrency(t *testing.T) {
	h, pool := newAPIServerIn(t, "IDR")

	res := call(t, h, http.MethodPatch, "/api/v1/settings", `{"revision":1,"ceiling":"70000"}`)
	if res.status != http.StatusOK {
		t.Fatalf("PATCH -> %d\n%s", res.status, res.body)
	}
	if got := str(t, decode(t, res), "ceiling"); got != "70000" {
		t.Errorf("ceiling = %q, want %q — no decimal point on a zero-decimal currency", got, "70000")
	}
	if got := storedCeiling(t, pool); got != 70000 {
		t.Errorf("stored ceiling = %d, want 70000", got)
	}

	res = call(t, h, http.MethodPatch, "/api/v1/settings", `{"revision":2,"ceiling":"70000.50"}`)
	if res.status != http.StatusBadRequest {
		t.Errorf("sen against a zero-decimal currency -> %d, want 400\n%s", res.status, res.body)
	}
	if got := storedCeiling(t, pool); got != 70000 {
		t.Errorf("stored ceiling = %d after a refused write, want the previous 70000", got)
	}
}

// ratePatch is a one-knob body at a given revision, written out rather than
// marshalled so the rate reaches the server as the digits these cases are
// about rather than as whatever a float64 round trip makes of them.
func ratePatch(revision int64, field, value string) string {
	return `{"revision":` + strconv.FormatInt(revision, 10) + `,"` + field + `":` + value + `}`
}

// TestSettingsRatesAreHeldToTheirColumns: inflation_pct is numeric(5,2) and
// fx_rate numeric(18,6), and a decimal past those is not refused by the column
// but rounded away. The answer then carries a rate the client did not send, so
// a client comparing what it holds with what it sent finds a difference no
// further write can close and patches again on every pass, for as long as the
// page is open. qty is held to its own column for that reason; these are the
// other two figures here that are not money.
func TestSettingsRatesAreHeldToTheirColumns(t *testing.T) {
	h, _ := newAPIServer(t)

	for _, tc := range []struct{ name, field, value string }{
		// A plan in euros pricing a second currency in rupiah needs about this
		// rate: seven decimals, of which the column keeps six.
		{"a seventh decimal on the fx rate", "fxRate", "0.0000571"},
		{"a third decimal on the inflation buffer", "inflationPct", "4.125"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// At whatever revision the row is on: a refused write leaves it
			// where it was, so a case that lands anyway moves it for the next.
			body := ratePatch(int64(num(t, settingsRow(t, h), "revision")), tc.field, tc.value)
			res := call(t, h, http.MethodPatch, "/api/v1/settings", body)
			if res.status != http.StatusBadRequest {
				t.Fatalf("PATCH -> %d, want 400\n%s", res.status, res.body)
			}
			row := decode(t, res)
			if got := str(t, row, "error"); got != errBadRequest {
				t.Errorf("error = %q, want %q", got, errBadRequest)
			}
			// Naming the limit is the whole of it: a caller told only that
			// something went wrong can do nothing but send the value again.
			msg := str(t, row, "message")
			if !strings.HasPrefix(msg, tc.field+" ") || !strings.Contains(msg, "decimal places") {
				t.Errorf("message = %q, want it to name %s and the limit", msg, tc.field)
			}
		})
	}

	// And every figure the columns do hold still goes through unchanged, up to
	// both bounds: a check that refuses a storable rate is a worse bug than the
	// one it fixes.
	revision := int64(num(t, settingsRow(t, h), "revision"))
	for _, tc := range []struct{ field, value string }{
		{"fxRate", "0.000057"},
		{"fxRate", "17500.125"},
		// A sixth decimal this far up, where the float's own steps are only
		// just finer than the column's. Counting decimals holds it; checking
		// the scale by multiplying, as qty does, would refuse it.
		{"fxRate", "36856787008.043724"},
		{"fxRate", "999999999999"},
		{"fxRate", "-17500.125"},
		{"inflationPct", "4.25"},
		{"inflationPct", "999.99"},
		{"inflationPct", "-2.5"},
		{"inflationPct", "0"},
	} {
		res := call(t, h, http.MethodPatch, "/api/v1/settings", ratePatch(revision, tc.field, tc.value))
		if res.status != http.StatusOK {
			t.Fatalf("PATCH %s %s -> %d\n%s", tc.field, tc.value, res.status, res.body)
		}
		row := decode(t, res)
		if got := strconv.FormatFloat(num(t, row, tc.field), 'f', -1, 64); got != tc.value {
			t.Errorf("%s = %s, want the %s that was sent", tc.field, got, tc.value)
		}
		revision = int64(num(t, row, "revision"))
	}
}

// TestSettingsStaleRevisionIs409 is the conflict path end to end, in the shape
// every other collection uses: two people holding revision 1, the first write
// lands, the second is refused and told what the row now says.
func TestSettingsStaleRevisionIs409(t *testing.T) {
	h, _ := newAPIServer(t)

	first := call(t, h, http.MethodPatch, "/api/v1/settings", `{"revision":1,"ceiling":"70000.00"}`)
	if first.status != http.StatusOK {
		t.Fatalf("first write -> %d\n%s", first.status, first.body)
	}

	second := call(t, h, http.MethodPatch, "/api/v1/settings", `{"revision":1,"ceiling":"55000.00"}`)
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
	// Not just "you lost", but what the row now says — the thing the client
	// reconciles against instead of refetching the whole plan.
	if got := str(t, conflict.Current, "ceiling"); got != "70000.00" {
		t.Errorf("current.ceiling = %q, want the winning write's 70000.00", got)
	}
	if got := num(t, conflict.Current, "revision"); got != 2 {
		t.Errorf("current.revision = %v, want 2", got)
	}

	// And the refused write must not have landed anyway.
	if got := str(t, settingsRow(t, h), "ceiling"); got != "70000.00" {
		t.Errorf("plan settings.ceiling = %q — the refused write overwrote the winner", got)
	}
}

// TestSettingsWholeRowCanBeSentBack: strict decoding must not make the obvious
// client illegal — read the plan, change one knob, send the settings object
// back whole. It carries revision and updatedAt, which are read-only.
func TestSettingsWholeRowCanBeSentBack(t *testing.T) {
	h, _ := newAPIServer(t)

	row := settingsRow(t, h)
	row["ceiling"] = "70000.00"
	body, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	res := call(t, h, http.MethodPatch, "/api/v1/settings", string(body))
	if res.status != http.StatusOK {
		t.Fatalf("patching with the whole settings object -> %d\n%s", res.status, res.body)
	}
	if got := str(t, decode(t, res), "ceiling"); got != "70000.00" {
		t.Errorf("ceiling = %q", got)
	}
}

func TestSettingsBadRequestsAreRefused(t *testing.T) {
	h, pool := newAPIServer(t)

	cases := []struct {
		name string
		body string
	}{
		// Without a revision the write would overwrite whatever is there now.
		{"no revision", `{"ceiling":"70000.00"}`},
		{"revision as a string", `{"revision":"1","ceiling":"70000.00"}`},
		// A misspelt field that silently did nothing is the bug nobody catches,
		// and a ceiling nobody notices did not move is the expensive one.
		{"unknown field", `{"revision":1,"ceilingg":"70000.00"}`},
		// The singleton has no id and no updated_by column, so neither is a
		// field a client can have read from a response.
		{"id echoed back", `{"revision":1,"id":"` + uuid.New().String() + `"}`},
		{"updatedBy echoed back", `{"revision":1,"updatedBy":null}`},
		// Money is a decimal string in major units and nothing else: a JSON
		// number is a float in the browser's parser.
		{"money as a JSON number", `{"revision":1,"ceiling":7000000}`},
		{"more decimals than EUR has", `{"revision":1,"ceiling":"70000.005"}`},
		{"money that is not a number at all", `{"revision":1,"ceiling":"70,000.00"}`},
		{"null money", `{"revision":1,"ceiling":null}`},
		{"null toggle", `{"revision":1,"splitEvenly":null}`},
		{"toggle as a number", `{"revision":1,"splitEvenly":1}`},
		// numeric(5,2) and numeric(18,6): past those the column refuses the
		// write, and a 500 tells the client this server is broken instead. A
		// decimal past them is worse than refused, it is rounded away, so it is
		// refused here.
		{"inflation past what the column holds", `{"revision":1,"inflationPct":1000}`},
		{"fx rate past what the column holds", `{"revision":1,"fxRate":1e18}`},
		{"inflation with a third decimal", `{"revision":1,"inflationPct":4.125}`},
		{"fx rate with a seventh decimal", `{"revision":1,"fxRate":0.0000571}`},
		{"empty body", ``},
		{"not an object", `[{"revision":1}]`},
		{"trailing content", `{"revision":1}{"revision":1}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := call(t, h, http.MethodPatch, "/api/v1/settings", tc.body)
			if res.status != http.StatusBadRequest {
				t.Fatalf("-> %d, want 400\n%s", res.status, res.body)
			}
			if got := str(t, decode(t, res), "error"); got != "bad_request" {
				t.Errorf("error = %q, want bad_request", got)
			}
		})
	}

	// None of them may have written anything on the way to being refused.
	if got := storedCeiling(t, pool); got != 0 {
		t.Errorf("stored ceiling = %d after refused writes only, want the seeded 0", got)
	}

	// The singleton has no POST and no DELETE, and the path is real, so a verb
	// it does not serve is a 405 naming the one it does.
	for _, method := range []string{http.MethodPost, http.MethodDelete, http.MethodPut} {
		res := call(t, h, method, "/api/v1/settings", `{}`)
		if res.status != http.StatusMethodNotAllowed {
			t.Errorf("%s /api/v1/settings -> %d, want 405", method, res.status)
			continue
		}
		if allow := res.header.Get("Allow"); !strings.Contains(allow, http.MethodPatch) {
			t.Errorf("405 Allow = %q, want it to name PATCH", allow)
		}
	}
}

// TestSettingsMetricsRouteLabel: the route label stays a bounded set, so the
// singleton gets a fixed label of its own rather than falling into api-other.
func TestSettingsMetricsRouteLabel(t *testing.T) {
	h, mh, _ := newAPIServerWithMetrics(t)

	if res := call(t, h, http.MethodPatch, "/api/v1/settings", `{"revision":1,"ceiling":"70000.00"}`); res.status != http.StatusOK {
		t.Fatalf("PATCH -> %d\n%s", res.status, res.body)
	}

	out := string(call(t, mh, http.MethodGet, "/metrics", "").body)
	if !strings.Contains(out, `route="api-settings"`) {
		t.Errorf("metrics output missing route=\"api-settings\":\n%s", out)
	}
	if strings.Contains(out, `route="/api/`) {
		t.Error("raw API paths leaked into the route label")
	}
}
