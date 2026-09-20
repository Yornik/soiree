package sheetimport

import (
	"strings"
	"testing"
)

func TestDetectFindsTwoStackedTables(t *testing.T) {
	book := messyBook(t)
	cfg, notes := Detect(book)

	// The run of show and the cost table share a sheet and share nothing
	// else. A proposal that merges them is the failure this is guarding.
	var planTables int
	for _, tbl := range cfg.Tables {
		if tbl.Sheet == "Plan" {
			planTables++
		}
	}
	if planTables < 2 {
		t.Fatalf("proposed %d tables for sheet Plan; want the two stacked tables found separately:\n%+v", planTables, cfg.Tables)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("a proposal has to be valid enough to run once reviewed: %v", err)
	}

	// Whatever detection could not name must be visible, because the
	// operator's job on this output is to fill in the gaps.
	if len(notes) == 0 {
		t.Error("Detect should explain what it could not work out")
	}

	// The cost table's columns are Item | Qty | Total | Paid.
	var costs *Table
	for i := range cfg.Tables {
		if cfg.Tables[i].FirstRow == 13 {
			costs = &cfg.Tables[i]
		}
	}
	if costs == nil {
		t.Fatalf("the cost table starting at row 13 was not proposed:\n%+v", cfg.Tables)
	}
	for field, want := range map[string]string{"item": "A", "qty": "B", "total": "C", "paid": "D"} {
		if got := costs.Columns[field]; got != want {
			t.Errorf("proposed %s = %q; want %q (columns: %v)", field, got, want, costs.Columns)
		}
	}
	if costs.HeaderRow != 12 {
		t.Errorf("proposed headerRow = %d; want 12", costs.HeaderRow)
	}
}

func TestDetectProposesATaskTable(t *testing.T) {
	book, _, err := ReadCSV(strings.NewReader(tasksCSV), ';')
	if err != nil {
		t.Fatalf("ReadCSV: %v", err)
	}
	cfg, _ := Detect(book)
	if len(cfg.Tables) != 1 {
		t.Fatalf("proposed %d tables; want 1:\n%+v", len(cfg.Tables), cfg.Tables)
	}
	// Owner, due and status with no money anywhere is a task list, not a
	// budget of zeroes.
	if cfg.Tables[0].Kind != KindTasks {
		t.Errorf("proposed kind %q; want tasks", cfg.Tables[0].Kind)
	}
	if got := cfg.Tables[0].Columns["name"]; got != "A" {
		t.Errorf("proposed name column %q; want A (columns: %v)", got, cfg.Tables[0].Columns)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("proposal is not valid: %v", err)
	}
}

// One table to the person who made it: a header, three lines, a section
// heading, three more lines. To detection the heading is a second header, so
// rows 5-8 become a block of their own whose "titles" match nothing.
const headingCSV = "Item,Vendor,Total\n" +
	"Hall,Acme,1000\n" +
	"Cleaning,Acme,200\n" +
	"Security,Acme,350\n" +
	"CATERING,see quote,\n" +
	"Dinner,Cook,3000\n" +
	"Drinks,Bar,800\n" +
	"Cake,Bakery,250\n"

func TestDetectNamesTheBlockItCouldNotPropose(t *testing.T) {
	book, _, err := ReadCSV(strings.NewReader(headingCSV), 0)
	if err != nil {
		t.Fatalf("ReadCSV: %v", err)
	}
	cfg, notes := Detect(book)
	if len(cfg.Tables) != 1 || cfg.Tables[0].LastRow != 4 {
		t.Fatalf("expected the one proposal to stop at row 4, which is what this guards:\n%+v", cfg.Tables)
	}
	// More than half the money is below row 4. A proposal that leaves it out
	// has to say so, or the operator reviews a mapping that looks complete.
	if !noted(notes, "rows 5-8") {
		t.Errorf("the block left out of the proposal is named nowhere; notes: %q", notes)
	}
	// And what to do about it, since "no titles recognised" is only half the
	// story when the row in question was never a row of titles.
	if !noted(notes, "extend lastRow of the table above it to 8") {
		t.Errorf("the note should offer the section-heading reading; notes: %q", notes)
	}
}

func TestDetectNamesASheetItCouldNotRead(t *testing.T) {
	// Titles in a language the keywords do not speak. "Found nothing" is true
	// and useless; which rows were looked at is what the operator can act on.
	const csv = "Omschrijving;Leverancier;Aantal;Prijs\n" +
		"Zaalhuur;Acme;1;1000\n" +
		"Stoelen;Acme;150;4\n"
	book, _, err := ReadCSV(strings.NewReader(csv), 0)
	if err != nil {
		t.Fatalf("ReadCSV: %v", err)
	}
	cfg, notes := Detect(book)
	if len(cfg.Tables) != 0 {
		t.Fatalf("proposed %d tables from titles it cannot know:\n%+v", len(cfg.Tables), cfg.Tables)
	}
	if !noted(notes, "rows 1-3") {
		t.Errorf("the block that was not proposed is named nowhere; notes: %q", notes)
	}
}

func TestDetectNamesARowTooShortToBeATable(t *testing.T) {
	// Row 1 of the fixture is a merged title on its own. Leaving it out is
	// right; leaving it out without a word is not.
	_, notes := Detect(messyBook(t))
	if !noted(notes, "Plan row 1:") {
		t.Errorf("the lone title row is named nowhere; notes: %q", notes)
	}
}

func noted(notes []string, want string) bool {
	for _, n := range notes {
		if strings.Contains(n, want) {
			return true
		}
	}
	return false
}
