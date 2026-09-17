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
