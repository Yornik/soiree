package main

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Yornik/soiree/internal/sheetimport"
)

// A small, entirely invented cost table with the usual defects: a blank line,
// a summary row, a figure written with a thousands separator, and a sub-item
// written with a leading dash.
const costsCSV = `Item,Qty,Total,Paid
Venue deposit,1,"2,500",500
Catering,40,"1,800",0
Decorations,,,
- Backdrop,1,"300",0
- Table flowers,6,"120",0
TOTAL,,"4,720",500
`

func writeFixture(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// quiet swaps stdout and stderr for files, returning what was written to
// stdout. The report is meant for a terminal, not for the test log.
func quiet(t *testing.T, fn func()) string {
	t.Helper()
	dir := t.TempDir()
	out, err := os.Create(filepath.Join(dir, "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	errOut, err := os.Create(filepath.Join(dir, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = out, errOut
	defer func() {
		os.Stdout, os.Stderr = oldOut, oldErr
	}()

	fn()

	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	if err := errOut.Close(); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestRunImportsWithFlagMapping(t *testing.T) {
	csv := writeFixture(t, "costs.csv", costsCSV)
	out := filepath.Join(t.TempDir(), "plan.json")

	var err error
	quiet(t, func() {
		err = run([]string{
			"-header", "1", "-rows", "2-",
			"-map", "item=A,qty=B,total=C", "-map", "paid=D",
			"-o", out, csv,
		})
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	body, rerr := os.ReadFile(out)
	if rerr != nil {
		t.Fatalf("no JSON written: %v", rerr)
	}
	var state struct {
		BudgetItems []struct {
			Item     string  `json:"item"`
			Unit     float64 `json:"unit"`
			Qty      float64 `json:"qty"`
			Children []struct {
				Item string `json:"item"`
			} `json:"children"`
		} `json:"budgetItems"`
		Tasks []any `json:"tasks"`
	}
	if err := json.Unmarshal(body, &state); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}
	if len(state.BudgetItems) != 3 {
		t.Fatalf("imported %d items; want 3 (TOTAL excluded, the two dash rows folded in)", len(state.BudgetItems))
	}
	if state.Tasks == nil {
		t.Error("tasks must be an array, or the app refuses the file")
	}
	// In whole cents from a rounded unit price, which is how the page adds a
	// budget up. The float product would forgive a unit price that does not
	// survive being stored. math.Round and the page's Math.round part ways
	// only on a negative half, and nothing in this fixture is negative.
	var cents float64
	for _, item := range state.BudgetItems {
		cents += math.Round(math.Round(item.Unit*100) * item.Qty)
	}
	if cents != 472000 {
		t.Errorf("budget total = %v cents; want 472000 with the sub-items counted once", cents)
	}
}

func TestRunRefusesACurrencyThatIsNotACode(t *testing.T) {
	csv := writeFixture(t, "costs.csv", costsCSV)

	var err error
	quiet(t, func() {
		err = run([]string{"-header", "1", "-map", "item=A,qty=B,total=C", "-currency", "euro", "-dry-run", csv})
	})
	// A misspelt code would quietly fall back to two decimals, which for a
	// zero-decimal currency is the check not running at all. The wording is
	// pinned because "currency" alone is also in what a binary without the
	// flag answers, and that is not this refusal.
	if err == nil || !strings.Contains(err.Error(), "ISO 4217") {
		t.Fatalf("run -currency euro = %v; want it refused as not a code", err)
	}
}

func TestRunDryRunWritesNothing(t *testing.T) {
	csv := writeFixture(t, "costs.csv", costsCSV)
	out := filepath.Join(t.TempDir(), "plan.json")

	var err error
	quiet(t, func() {
		err = run([]string{"-header", "1", "-rows", "2-", "-map", "item=A,total=C", "-dry-run", "-o", out, csv})
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, serr := os.Stat(out); serr == nil {
		t.Error("-dry-run wrote a file")
	}
}

func TestRunRefusesToGuess(t *testing.T) {
	csv := writeFixture(t, "costs.csv", costsCSV)

	var err error
	quiet(t, func() {
		err = run([]string{csv})
	})
	if err == nil {
		t.Fatal("run without a mapping should refuse")
	}
	// The refusal has to say what to do instead, or the tool is a wall.
	for _, want := range []string{"-mapping", "-map", "-detect"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %s", err, want)
		}
	}
}

func TestRunDetectProposesButImportsNothing(t *testing.T) {
	csv := writeFixture(t, "costs.csv", costsCSV)
	out := filepath.Join(t.TempDir(), "plan.json")

	var err error
	proposal := quiet(t, func() {
		err = run([]string{"-detect", "-o", out, csv})
	})
	if err != nil {
		t.Fatalf("run -detect: %v", err)
	}
	if _, serr := os.Stat(out); serr == nil {
		t.Error("-detect must not import anything, even with -o given")
	}
	cfg, lerr := sheetimport.LoadConfig(strings.NewReader(proposal))
	if lerr != nil {
		t.Fatalf("the proposal must load as a mapping file: %v\n%s", lerr, proposal)
	}
	if len(cfg.Tables) == 0 {
		t.Error("nothing proposed")
	}
}

func TestRunRejectsMixedMappingSources(t *testing.T) {
	csv := writeFixture(t, "costs.csv", costsCSV)
	mapping := writeFixture(t, "mapping.json", `{"tables":[{"columns":{"item":"A"}}]}`)

	var err error
	quiet(t, func() {
		err = run([]string{"-mapping", mapping, "-map", "item=A", csv})
	})
	if err == nil {
		t.Fatal("a mapping file and mapping flags together should be refused, not silently merged")
	}
}

// The report is built in memory before a byte of it is written, because half
// a report is worse than none. The JSON deserves the same: -o truncated the
// previous import before the encoder had agreed to produce anything.
func TestWriteStateKeepsTheOldFileWhenTheNewOneCannotBeWritten(t *testing.T) {
	out := writeFixture(t, "plan.json", `{"budgetItems":["the previous import"]}`)

	state := sheetimport.NewState()
	state.FxRate = math.NaN() // JSON has no way to write it
	if err := writeState(state, out); err == nil {
		t.Fatal("writeState wrote a plan JSON cannot encode")
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(body), "previous import") {
		t.Errorf("the previous import was destroyed by a write that never happened: %q", body)
	}
}

func TestParseRows(t *testing.T) {
	cases := []struct {
		in          string
		first, last int
		wantErr     bool
	}{
		{in: "", first: 0, last: 0},
		{in: "3-32", first: 3, last: 32},
		{in: "34-", first: 34, last: 0},
		{in: "12", first: 12, last: 12},
		{in: "0-5", wantErr: true},
		{in: "a-b", wantErr: true},
	}
	for _, c := range cases {
		first, last, err := parseRows(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseRows(%q) = %d,%d; want an error", c.in, first, last)
			}
			continue
		}
		if err != nil || first != c.first || last != c.last {
			t.Errorf("parseRows(%q) = %d,%d,%v; want %d,%d", c.in, first, last, err, c.first, c.last)
		}
	}
}

func TestColumnFlag(t *testing.T) {
	var c columnFlag
	if err := c.Set("item=A,total=C"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := c.Set("paid=D"); err != nil {
		t.Fatalf("repeated -map should accumulate: %v", err)
	}
	if len(c) != 3 || c["total"] != "C" {
		t.Errorf("columns = %v", map[string]string(c))
	}
	if err := c.Set("item=B"); err == nil {
		t.Error("mapping one field twice should be refused rather than resolved silently")
	}
	if err := c.Set("item"); err == nil {
		t.Error("-map without an = should be refused")
	}
}
