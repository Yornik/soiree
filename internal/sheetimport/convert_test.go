package sheetimport

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// messyBook is the shape this importer exists for: one sheet holding two
// unrelated tables with different columns, a header repeated partway down,
// figures stored as formatted text next to figures stored as numbers, a
// summary row inside the grid, sub-items written with a leading dash, a
// remark sitting in a numeric column, and a second sheet breaking down one
// line of the first.
//
// Row numbers of sheet "Plan", so the assertions below can be read against
// it:
//
//	 1  merged title
//	 2  blank
//	 3  header: Stage | What is needed | Vendor | Estimated price
//	 4- 6  run-of-show rows
//	 7  the header again, in the middle of the data
//	 8- 9  more run-of-show rows (9 has a deadline in the price column)
//	10  TOTAL
//	11  blank
//	12  header: Item | Qty | Total | Paid
//	13-19  cost rows (15 is a parent, 16-18 its dash children)
//	20  a summary row whose label is not an English keyword
func messyBook(t *testing.T) *Book {
	t.Helper()
	runOfShowHeader := tRow(cStr("Stage"), cStr("What is needed"), cStr("Vendor"), cStr("Estimated price"))

	path := writeODS(t,
		tSheet("Plan",
			tRow(cStr("Garden party — planning"), cCovered(3)),
			tRowRepeat(1, cBlank(4)),
			runOfShowHeader,
			tRow(cStr("Arrival"), cStr("Welcome signage"), cStr("Print shop"), cStr("1,200")),
			tRow(cStr("Arrival"), cStr("Guest book table"), cBlank(1), cStr("-")),
			tRow(cStr("Dinner"), cStr("Long tables"), cStr("Rental co"), cNum(3400, "3,400")),
			runOfShowHeader,
			tRow(cStr("Dinner"), cStr("Folding chairs"), cStr("Rental co"), cNum(2500, "2,500")),
			tRow(cStr("Speeches"), cStr("Microphone"), cStr("Sound hire"), cStr("before 1 March")),
			tRow(cStr("TOTAL"), cBlank(2), cStr("7,100")),
			tRowRepeat(1, cBlank(4)),
			tRow(cStr("Item"), cStr("Qty"), cStr("Total"), cStr("Paid")),
			tRow(cStr("Venue deposit"), cNum(1, "1"), cStr("18,400,000"), cStr("5,600,000")),
			tRow(cStr("Catering"), cNum(40, "40"), cCur(3600000, "Rp 3.600.000"), cBlank(1)),
			tRow(cStr("Decorations :"), cBlank(3)),
			tRow(cStr("- Backdrop"), cNum(1, "1"), cStr("1,250,000"), cBlank(1)),
			tRow(cStr("- Planter boxes"), cNum(6, "6"), cStr("840,000"), cBlank(1)),
			tRow(cStr("- String lights"), cNum(2, "2"), cStr("610,000"), cBlank(1)),
			tRow(cStr("Photographer"), cNum(1, "1"), cStr("2,900,000"), cStr("950,000")),
			tRow(cStr("Totaal"), cBlank(1), cStr("27,600,000"), cBlank(1)),
			emptyTail,
		),
		tSheet("Quote",
			tRow(cStr("Dish"), cStr("Servings"), cStr("Price each"), cStr("Allergens")),
			tRow(cStr("Rice bowls"), cNum(40, "40"), cStr("52,000"), cBlank(1)),
			tRow(cStr("Grilled fish"), cNum(40, "40"), cStr("28,000"), cStr("fish")),
			tRow(cStr("Fruit platter"), cNum(10, "10"), cStr("16,000"), cBlank(1)),
		),
	)

	book, err := ReadODS(path)
	if err != nil {
		t.Fatalf("ReadODS: %v", err)
	}
	return book
}

// messyConfig maps the fixture: the two stacked tables are two entries, with
// their own row ranges and their own columns.
func messyConfig() *Config {
	return &Config{
		Tables: []Table{
			{
				Name:      "run of show",
				Sheet:     "Plan",
				Kind:      KindBudget,
				HeaderRow: 3,
				FirstRow:  4,
				LastRow:   10,
				Columns:   map[string]string{"phase": "A", "item": "B", "vendor": "C", "unit": "D"},
			},
			{
				Name:      "costs",
				Sheet:     "Plan",
				Kind:      KindBudget,
				HeaderRow: 12,
				FirstRow:  13,
				LastRow:   20,
				Columns:   map[string]string{"item": "A", "qty": "B", "total": "C", "paid": "D"},
			},
			{
				Name:       "catering quote",
				Sheet:      "Quote",
				Kind:       KindBudget,
				HeaderRow:  1,
				FirstRow:   2,
				ParentItem: "Catering",
				Columns:    map[string]string{"item": "A", "qty": "B", "unit": "C"},
			},
		},
	}
}

func TestConvertMessySheet(t *testing.T) {
	book := messyBook(t)
	state, report, err := Convert(book, messyConfig())
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}

	runOfShow, costs, quote := &report.Tables[0], &report.Tables[1], &report.Tables[2]

	t.Run("every scanned row is accounted for", func(t *testing.T) {
		for i := range report.Tables {
			tr := &report.Tables[i]
			if got := tr.Accounted(); got != tr.Scanned {
				t.Errorf("table %q: scanned %d rows but accounted for %d", tr.Name, tr.Scanned, got)
			}
		}
	})

	t.Run("the repeated header is skipped", func(t *testing.T) {
		if !skippedRow(runOfShow, 7, "repeats the header") {
			t.Errorf("row 7 should be skipped as a repeated header; skips: %v", runOfShow.Skipped)
		}
	})

	t.Run("the TOTAL row does not become a line item", func(t *testing.T) {
		if !skippedRow(runOfShow, 10, "summary row") {
			t.Errorf("row 10 should be skipped as a summary row; skips: %v", runOfShow.Skipped)
		}
		if findItem(state.BudgetItems, "TOTAL") != nil {
			t.Error("TOTAL was imported as a budget line")
		}
	})

	t.Run("a remark in the price column is reported and kept", func(t *testing.T) {
		item := findItem(state.BudgetItems, "Microphone")
		if item == nil {
			t.Fatal("Microphone missing — a row with an unreadable price must still be imported")
		}
		if item.Unit != 0 {
			t.Errorf("Microphone unit = %v; want 0, the text was not a figure", item.Unit)
		}
		if !strings.Contains(item.Note, "before 1 March") {
			t.Errorf("Microphone note = %q; want the unreadable text kept", item.Note)
		}
		if !warnedRow(runOfShow, 9, "unit column") {
			t.Errorf("row 9 should warn about the unit column; warnings: %v", runOfShow.Warnings)
		}
	})

	t.Run("formatted and typed figures agree", func(t *testing.T) {
		if got := findItem(state.BudgetItems, "Welcome signage").Total(); got != 1200 {
			t.Errorf("Welcome signage = %v; want 1200 from the text \"1,200\"", got)
		}
		if got := findItem(state.BudgetItems, "Long tables").Total(); got != 3400 {
			t.Errorf("Long tables = %v; want 3400 from the typed value", got)
		}
		if got := findItem(state.BudgetItems, "Venue deposit").Total(); got != 18400000 {
			t.Errorf("Venue deposit = %v; want 18400000", got)
		}
		// The currency cell displays "Rp 3.600.000" but stores 3600000. The
		// stored value must win: the display is locale-formatted, and reading
		// it as text would turn it into three point six.
		if got := findItem(state.BudgetItems, "Catering"); got.Qty != 40 || got.Unit != 90000 {
			t.Errorf("Catering unit/qty = %v/%v; want 90000/40 derived from the stated total", got.Unit, got.Qty)
		}
		if runOfShow.AssumedGrouping+costs.AssumedGrouping == 0 {
			t.Error("the report must disclose that a thousands separator was assumed")
		}
	})

	t.Run("dash rows roll into the row above", func(t *testing.T) {
		parent := findItem(state.BudgetItems, "Decorations :")
		if parent == nil {
			t.Fatal("Decorations missing")
		}
		if len(parent.Children) != 3 {
			t.Fatalf("Decorations has %d children; want 3", len(parent.Children))
		}
		if parent.Total() != 2700000 {
			t.Errorf("Decorations = %v; want 2700000, the sum of its children", parent.Total())
		}
		for _, name := range []string{"Backdrop", "Planter boxes", "String lights"} {
			if findItem(state.BudgetItems, name) != nil {
				t.Errorf("%q was imported as a top-level item as well as a child — the budget would be counted twice", name)
			}
		}
		if costs.RolledChildren != 3 || costs.Parents != 1 {
			t.Errorf("report says %d children into %d parents; want 3 into 1", costs.RolledChildren, costs.Parents)
		}
	})

	t.Run("a row equal to the rows above it is flagged", func(t *testing.T) {
		// "Totaal" is not in the English keyword list, so it is imported —
		// but the arithmetic gives it away and the report says so.
		if findItem(state.BudgetItems, "Totaal") == nil {
			t.Error("a row the keywords do not recognise must still be imported, not guessed away")
		}
		if !warnedRow(costs, 20, "equals the sum") {
			t.Errorf("row 20 should warn that it equals the rows above it; warnings: %v", costs.Warnings)
		}
	})

	t.Run("the breakdown sheet lands under one line", func(t *testing.T) {
		catering := findItem(state.BudgetItems, "Catering")
		if len(catering.Children) != 3 {
			t.Fatalf("Catering has %d children; want the 3 rows of the quote", len(catering.Children))
		}
		// The agreed line stands; the quote disagreeing with it is reported
		// rather than silently overwriting the figure.
		if catering.Total() != 3600000 {
			t.Errorf("Catering = %v; want the agreed 3600000", catering.Total())
		}
		if !strings.Contains(strings.Join(quote.Notes, " "), "3360000") {
			t.Errorf("the report should say the breakdown adds up to something else; notes: %v", quote.Notes)
		}
		for _, ch := range catering.Children {
			if ch.ID == "" {
				t.Error("attached children need ids of their own")
			}
		}
	})

	t.Run("unmapped columns are named", func(t *testing.T) {
		if len(quote.UnmappedColumns) != 1 || !strings.Contains(quote.UnmappedColumns[0], "Allergens") {
			t.Errorf("unmapped columns = %v; want the Allergens column named", quote.UnmappedColumns)
		}
	})

	t.Run("costless rows are counted, not dropped", func(t *testing.T) {
		if runOfShow.Costless != 2 {
			t.Errorf("costless rows = %d; want 2", runOfShow.Costless)
		}
		if findItem(state.BudgetItems, "Guest book table") == nil {
			t.Error("a programme row with no cost must still be imported by default")
		}
	})
}

func TestConvertSkipsCostlessOnRequest(t *testing.T) {
	book := messyBook(t)
	cfg := messyConfig()
	cfg.Tables[0].SkipRowsWithoutAmount = true
	cfg.Tables[1].SkipRowsWithoutAmount = true

	state, report, err := Convert(book, cfg)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if findItem(state.BudgetItems, "Guest book table") != nil {
		t.Error("skipRowsWithoutAmount should have dropped the row")
	}
	if !skippedRow(&report.Tables[0], 5, "no amount") {
		t.Errorf("the drop must be reported; skips: %v", report.Tables[0].Skipped)
	}

	// A parent of dash rows carries no amount of its own until its children
	// are folded in. Dropping it during the scan would leave those children
	// to attach to the line above — the decorations silently becoming part of
	// the catering bill, with nothing in the report to say so.
	parent := findItem(state.BudgetItems, "Decorations :")
	if parent == nil {
		t.Fatal("the parent of the dash rows was dropped as costless")
	}
	if len(parent.Children) != 3 || parent.Total() != 2700000 {
		t.Errorf("Decorations = %v with %d children; want 2700000 with 3", parent.Total(), len(parent.Children))
	}
	if catering := findItem(state.BudgetItems, "Catering"); len(catering.Children) != 3 {
		t.Errorf("Catering has %d children; want only the 3 rows of its own quote", len(catering.Children))
	}
	for i := range report.Tables {
		tr := &report.Tables[i]
		if tr.Accounted() != tr.Scanned {
			t.Errorf("table %q: scanned %d rows but accounted for %d", tr.Name, tr.Scanned, tr.Accounted())
		}
	}
}

func TestConvertHonoursConfiguredKeywords(t *testing.T) {
	// The defaults are English. A sheet in any other language says so once,
	// in the mapping, rather than being guessed at.
	book := messyBook(t)
	cfg := messyConfig()
	cfg.TotalKeywords = []string{"total", "totaal"}

	state, report, err := Convert(book, cfg)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if findItem(state.BudgetItems, "Totaal") != nil {
		t.Error("Totaal should have been recognised as a summary row")
	}
	if !skippedRow(&report.Tables[1], 20, "summary row") {
		t.Errorf("row 20 skip not reported; skips: %v", report.Tables[1].Skipped)
	}
}

// A budget in sections, the ordinary shape of a hand-made one: each section
// closes with its own subtotal and the sheet closes with the total of it all.
// None of the three labels is a default keyword, so the arithmetic is the only
// thing standing between this sheet and a budget of three times its size.
//
// Row 1 is the header; rows 2-5 and 6-9 are a heading, two lines and their
// subtotal (1200 and 3800); row 10 is the total (5000).
const sectionsCSV = "Item;Bedrag\n" +
	"LOCATIE;\n" +
	"Zaalhuur;1.000,00\n" +
	"Schoonmaak;200,00\n" +
	"Subtotaal locatie;1.200,00\n" +
	"CATERING;\n" +
	"Diner;3.000,00\n" +
	"Drank;800,00\n" +
	"Subtotaal catering;3.800,00\n" +
	"Totaal;5.000,00\n"

func sectionsConfig() *Config {
	return &Config{
		Decimal: "comma",
		Tables: []Table{{
			Name:      "sections",
			HeaderRow: 1,
			Columns:   map[string]string{"item": "A", "unit": "B"},
		}},
	}
}

func TestConvertFlagsEverySubtotalAndTheTotal(t *testing.T) {
	book, _, err := ReadCSV(strings.NewReader(sectionsCSV), 0)
	if err != nil {
		t.Fatalf("ReadCSV: %v", err)
	}
	state, report, err := Convert(book, sectionsConfig())
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	tr := &report.Tables[0]

	// The second subtotal has to be measured against its own section, and the
	// total against the lines alone: a sum that still holds the first subtotal
	// matches neither.
	for _, row := range []int{5, 9, 10} {
		if !warnedRow(tr, row, "equals the sum") {
			t.Errorf("row %d is a sum of rows above it and should be flagged; warnings: %v", row, tr.Warnings)
		}
	}
	if len(tr.Warnings) != 3 {
		t.Errorf("%d warnings; want exactly the three summary rows: %v", len(tr.Warnings), tr.Warnings)
	}
	// Flagged, never dropped: the check reads arithmetic, and arithmetic can
	// be a coincidence.
	for _, name := range []string{"Subtotaal locatie", "Subtotaal catering", "Totaal"} {
		if findItem(state.BudgetItems, name) == nil {
			t.Errorf("%q was flagged and must still be imported", name)
		}
	}
}

func TestConvertStartsANewSectionAfterAKeywordRow(t *testing.T) {
	// The operator did what the first warning said and listed that label. The
	// rows below it must not go quiet as a result: a clean report over a
	// budget that still counts the second subtotal and the total is the worst
	// outcome there is.
	book, _, err := ReadCSV(strings.NewReader(sectionsCSV), 0)
	if err != nil {
		t.Fatalf("ReadCSV: %v", err)
	}
	cfg := sectionsConfig()
	cfg.TotalKeywords = []string{"total", "subtotaal locatie"}

	_, report, err := Convert(book, cfg)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	tr := &report.Tables[0]
	if !skippedRow(tr, 5, "summary row") {
		t.Fatalf("row 5 should be skipped by its keyword; skips: %v", tr.Skipped)
	}
	for _, row := range []int{9, 10} {
		if !warnedRow(tr, row, "equals the sum") {
			t.Errorf("row %d should still be flagged after the keyword row; warnings: %v", row, tr.Warnings)
		}
	}
}

func TestConvertReportsFiguresItWillNotGuess(t *testing.T) {
	// Text cells, which is every cell of a CSV. Each of these used to import
	// as a confident figure under a report reading "0 warnings": 2500, 250,
	// 250 and 12032026.
	const csv = "Item,Amount\n" +
		"Chairs,2 x 500\n" +
		"Discount,−250\n" +
		"Refund,250-\n" +
		"Deposit paid on,12.03.2026\n"
	book, _, err := ReadCSV(strings.NewReader(csv), 0)
	if err != nil {
		t.Fatalf("ReadCSV: %v", err)
	}
	cfg := &Config{Tables: []Table{{HeaderRow: 1, Columns: map[string]string{"item": "A", "unit": "B"}}}}
	state, report, err := Convert(book, cfg)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	tr := &report.Tables[0]

	if got := findItem(state.BudgetItems, "Discount").Unit; got != -250 {
		t.Errorf("Discount = %v; want -250, the minus sign is typographic but it is a minus sign", got)
	}
	for row, name := range map[int]string{2: "Chairs", 4: "Refund", 5: "Deposit paid on"} {
		item := findItem(state.BudgetItems, name)
		if item.Unit != 0 {
			t.Errorf("%s = %v; want 0, the cell does not hold one plain figure", name, item.Unit)
		}
		if !warnedRow(tr, row, "unit column") {
			t.Errorf("row %d should warn about the unit column; warnings: %v", row, tr.Warnings)
		}
		if !strings.Contains(item.Note, "unit: ") {
			t.Errorf("%s note = %q; want the unread text kept", name, item.Note)
		}
	}
}

func TestConvertRefusesUnmatchedParent(t *testing.T) {
	// Attaching a quote to the wrong line would be invisible in the output,
	// so a parent that cannot be found exactly is a hard failure.
	book := messyBook(t)
	cfg := messyConfig()
	cfg.Tables[2].ParentItem = "Catrering"

	if _, _, err := Convert(book, cfg); err == nil {
		t.Fatal("Convert should refuse a parentItem that matches nothing")
	} else if !strings.Contains(err.Error(), "Catrering") {
		t.Errorf("the error should name the parent it could not find: %v", err)
	}
}

func TestOutputLoadsInTheApp(t *testing.T) {
	book := messyBook(t)
	state, _, err := Convert(book, messyConfig())
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	var buf bytes.Buffer
	if err := state.WriteJSON(&buf); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}

	// web/src/app.js refuses the file unless budgetItems and tasks are both
	// arrays. A nil slice marshals to null, which Array.isArray rejects just
	// as firmly as a missing key — so this is the acceptance criterion for
	// the whole tool, not a formatting preference.
	for _, key := range []string{`"budgetItems": [`, `"tasks": []`, `"sponsors": []`, `"notes": []`} {
		if !strings.Contains(buf.String(), key) {
			t.Errorf("output is missing %s; the app would reject the file", key)
		}
	}

	var round struct {
		BudgetItems []struct {
			ID       string  `json:"id"`
			Item     string  `json:"item"`
			Unit     float64 `json:"unit"`
			Qty      float64 `json:"qty"`
			Sponsors []string
		} `json:"budgetItems"`
	}
	if err := json.Unmarshal(buf.Bytes(), &round); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	seen := map[string]bool{}
	for _, item := range round.BudgetItems {
		if item.ID == "" || seen[item.ID] {
			t.Errorf("budget item %q has a missing or duplicate id %q", item.Item, item.ID)
		}
		seen[item.ID] = true
		if item.Sponsors == nil {
			t.Errorf("budget item %q has a null sponsors list", item.Item)
		}
	}

	// The flat total the app will show must not double-count the rolled-up
	// children: 1200 + 0 + 3400 + 2500 + 0 (run of show)
	//         + 18400000 + 3600000 + 2700000 + 2900000 + 27600000 (costs).
	var total float64
	for _, item := range round.BudgetItems {
		total += item.Unit * item.Qty
	}
	if want := 55207100.0; math.Abs(total-want) > 0.5 {
		t.Errorf("flat budget total = %v; want %v", total, want)
	}
}

func findItem(items []BudgetItem, name string) *BudgetItem {
	for i := range items {
		if items[i].Item == name {
			return &items[i]
		}
	}
	return nil
}

func skippedRow(tr *TableReport, row int, reason string) bool {
	for _, s := range tr.Skipped {
		if s.Row == row && strings.Contains(s.Text, reason) {
			return true
		}
	}
	return false
}

func warnedRow(tr *TableReport, row int, reason string) bool {
	for _, w := range tr.Warnings {
		if w.Row == row && strings.Contains(w.Text, reason) {
			return true
		}
	}
	return false
}
