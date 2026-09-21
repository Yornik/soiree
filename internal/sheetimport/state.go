package sheetimport

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
)

// State is the object web/src/app.js writes from "Export data (JSON)" and
// reads back through "Import data", so the importer's output drops straight
// into the existing UI.
//
// colWidths and rowHeights are deliberately absent. The import path resets
// both when they are missing, so emitting them would only copy UI constants
// into a data tool where they would rot.
type State struct {
	Ceiling      float64      `json:"ceiling"`
	InflationPct float64      `json:"inflationPct"`
	FxRate       float64      `json:"fxRate"`
	SplitEvenly  bool         `json:"splitEvenly"`
	Sponsors     []Sponsor    `json:"sponsors"`
	BudgetItems  []BudgetItem `json:"budgetItems"`
	Tasks        []Task       `json:"tasks"`
	Notes        []Note       `json:"notes"`
}

// NewState builds an empty state with every collection allocated.
//
// This is load-bearing rather than tidy: the app rejects the file outright
// unless budgetItems and tasks are arrays, and a nil Go slice marshals to
// null, which Array.isArray rejects just as firmly as a missing key.
func NewState() *State {
	return &State{
		Sponsors:    []Sponsor{},
		BudgetItems: []BudgetItem{},
		Tasks:       []Task{},
		Notes:       []Note{},
	}
}

// Sponsor is a person or party costs are attributed to.
type Sponsor struct {
	ID   string `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

// BudgetItem is one line of the budget grid. Line total is unit * qty.
//
// vendor and lockBy are fields the page has a control for, so they travel
// from the file into the plan the way the rest of the line does. Not columns
// of the grid: there is no room for two more, so the page puts them behind a
// button on the line. phase and children are not in today's export shape: the
// page carries them as unknown keys, which survive an export but not a
// database, since it sends the fields it knows and the next plan read
// replaces the rest. They are written all the same, because dropping columns
// a planner actually keeps would lose the data for good, and the schema they
// belong to is already there (see docs/architecture.md).
type BudgetItem struct {
	ID       string       `json:"id"`
	Item     string       `json:"item"`
	Unit     float64      `json:"unit"`
	Qty      float64      `json:"qty"`
	Paid     float64      `json:"paid"`
	Sponsors []string     `json:"sponsors"`
	Note     string       `json:"note"`
	Vendor   string       `json:"vendor,omitempty"`
	LockBy   string       `json:"lockBy,omitempty"`
	Phase    string       `json:"phase,omitempty"`
	Children []BudgetItem `json:"children,omitempty"`
}

// Total is the line total as the app computes it.
func (b BudgetItem) Total() float64 { return b.Unit * b.Qty }

// Task is one row of the task list. Status is constrained by the UI to
// not-started, in-progress or done, and due feeds an <input type="date">, so
// it is either empty or YYYY-MM-DD.
type Task struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Owner  string `json:"owner"`
	Due    string `json:"due"`
	Status string `json:"status"`
}

// Note is one free-text note.
type Note struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// WriteJSON writes the state in the same shape and indentation the export
// button produces, so the two files diff against each other.
func (s *State) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return fmt.Errorf("write json: %w", err)
	}
	return nil
}

// idGen mints the ids the UI uses to key rows.
//
// The app generates random ones; this generates sequential ones so that
// importing the same sheet twice produces byte-identical JSON. That is what
// makes an import reviewable in a diff, and the app only requires the ids to
// be unique within the file.
type idGen struct{ n map[string]int }

func newIDGen() *idGen { return &idGen{n: map[string]int{}} }

func (g *idGen) next(prefix string) string {
	g.n[prefix]++
	return prefix + strconv.Itoa(g.n[prefix])
}
