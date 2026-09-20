package sheetimport

import (
	"fmt"
	"strings"
)

// headerKeywords proposes a field for a column title. It is English-leaning
// on purpose and that is a limitation, not a feature: detection exists to
// save typing on ordinary sheets, and anything it fails to recognise is
// listed as unmapped for the operator to fill in by hand. Nothing here is
// ever applied without the operator passing the mapping back in.
var headerKeywords = []struct {
	field string
	words []string
}{
	{"vendor", []string{"vendor", "supplier", "provider", "shop", "contact"}},
	{"qty", []string{"qty", "quantity", "pcs", "pax", "count", "number of"}},
	{"paid", []string{"paid", "deposit", "down payment", "dp", "settled"}},
	{"total", []string{"total", "amount", "line total", "subtotal"}},
	{"unit", []string{"unit", "unit price", "price", "rate", "cost", "estimate", "estimated price"}},
	{"lockBy", []string{"deadline", "decide by", "lock by", "confirm by"}},
	{"due", []string{"due", "due date", "date"}},
	{"owner", []string{"owner", "pic", "responsible", "assignee", "who"}},
	{"status", []string{"status", "progress", "state"}},
	{"note", []string{"note", "notes", "remark", "remarks", "comment", "description"}},
	{"phase", []string{"phase", "stage", "segment", "session"}},
	{"item", []string{"item", "name", "what", "activity", "task", "need", "needs"}},
}

// minDetectRows keeps a stray label or a two-line signature block from being
// proposed as a table.
const minDetectRows = 2

// Detect proposes a mapping and explains what it saw.
//
// It never returns something ready to run blind: the caller prints it, the
// operator reads it, corrects the columns detection could not name, and feeds
// it back with -mapping. That round trip is the whole point — a sheet where
// one table ends and another begins is obvious to the person who made it and
// guesswork to a program.
func Detect(book *Book) (*Config, []string) {
	cfg := &Config{}
	var notes []string

	for i := range book.Sheets {
		sh := &book.Sheets[i]
		for _, blk := range blocks(sh) {
			table, blockNotes := proposeTable(sh, blk)
			// Before the nil check, not after it. A block that could not be
			// proposed is the one whose note matters most: those rows are in
			// no table, and the mapping printed below looks complete without
			// them.
			notes = append(notes, blockNotes...)
			if table == nil {
				continue
			}
			cfg.Tables = append(cfg.Tables, *table)
		}
	}
	if len(cfg.Tables) == 0 {
		notes = append(notes, "found nothing that looks like a table — write the mapping by hand")
	}
	notes = append(notes, mergeHints(cfg.Tables)...)
	return cfg, notes
}

// mergeHints flags neighbouring proposals with identical columns. Detection
// cannot tell a header repeated in the middle of one table from the start of
// a second table with the same columns — but the operator can, at a glance,
// and only one of those two readings keeps the rows together.
func mergeHints(tables []Table) []string {
	var out []string
	for i := 1; i < len(tables); i++ {
		a, b := tables[i-1], tables[i]
		if a.Sheet != b.Sheet || a.Kind != b.Kind || !sameColumns(a.Columns, b.Columns) {
			continue
		}
		out = append(out, fmt.Sprintf(
			"%s rows %d-%d and %d-%d have the same columns — if row %d is a repeated header rather than a new table, merge them into one entry (firstRow %d, lastRow %d, headerRow %d)",
			a.Sheet, a.FirstRow, a.LastRow, b.FirstRow, b.LastRow, b.HeaderRow, a.FirstRow, b.LastRow, a.HeaderRow))
	}
	return out
}

func sameColumns(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// block is a run of consecutive non-empty rows.
type block struct{ first, last int }

// blocks splits a sheet on blank rows and on a second header appearing
// further down. Two unrelated tables stacked in one sheet is the normal
// shape of a hand-made planning file, not an edge case.
func blocks(sh *Sheet) []block {
	var out []block
	start := 0
	for n := 1; n <= len(sh.Rows); n++ {
		if sh.RowEmpty(n) {
			if start != 0 {
				out = append(out, block{start, n - 1})
				start = 0
			}
			continue
		}
		if start == 0 {
			start = n
			continue
		}
		// A row that looks like a header inside a run means the run holds two
		// tables with different columns.
		if n > start+minDetectRows && looksLikeHeader(sh, n) && !looksLikeHeader(sh, n-1) {
			out = append(out, block{start, n - 1})
			start = n
		}
	}
	if start != 0 {
		out = append(out, block{start, len(sh.Rows)})
	}
	return out
}

// looksLikeHeader is text in most columns and no figures anywhere.
func looksLikeHeader(sh *Sheet, n int) bool {
	filled, numeric := 0, 0
	for _, cell := range sh.Row(n) {
		if cell.Empty() {
			continue
		}
		filled++
		if cell.HasValue {
			numeric++
			continue
		}
		if _, err := ParseNumber(cell.Text, DecimalAuto); err == nil {
			numeric++
		}
	}
	return filled >= 2 && numeric == 0
}

// proposeTable returns a table, or nil and the reason. Never nil alone: a
// block left out of the proposal without a word is rows dropped quietly, one
// step before the import.
func proposeTable(sh *Sheet, blk block) (*Table, []string) {
	if blk.last-blk.first+1 < minDetectRows {
		return nil, []string{fmt.Sprintf("%s %s: too short to be a table and not proposed; map it by hand if it holds data",
			sh.Name, rowSpan(blk.first, blk.last))}
	}
	header := 0
	first := blk.first
	if looksLikeHeader(sh, blk.first) {
		header, first = blk.first, blk.first+1
	}
	if first > blk.last {
		return nil, []string{fmt.Sprintf("%s %s: a header with nothing under it; not proposed",
			sh.Name, rowSpan(blk.first, blk.last))}
	}

	table := &Table{
		Name:      fmt.Sprintf("%s rows %d-%d", sh.Name, first, blk.last),
		Sheet:     sh.Name,
		Kind:      KindBudget,
		HeaderRow: header,
		FirstRow:  first,
		LastRow:   blk.last,
		Columns:   map[string]string{},
	}

	var notes []string
	taken := map[string]bool{}
	var unnamed []string
	for col := 1; col <= sh.Width(); col++ {
		title := ""
		if header > 0 {
			title = sh.Cell(header, col).Display()
		}
		if title == "" {
			if columnUsed(sh, col, first, blk.last) {
				unnamed = append(unnamed, ColumnLabel(col))
			}
			continue
		}
		field := matchField(title)
		if field == "" || taken[field] {
			unnamed = append(unnamed, fmt.Sprintf("%s (%q)", ColumnLabel(col), title))
			continue
		}
		taken[field] = true
		table.Columns[field] = ColumnLabel(col)
	}

	// The vocabularies differ per kind, so a table whose columns are all task
	// columns is proposed as tasks rather than as a budget of zeroes.
	if taken["status"] || taken["owner"] || taken["due"] {
		if !taken["unit"] && !taken["total"] && !taken["paid"] {
			table.Kind = KindTasks
			table.Columns = tasksColumns(table.Columns)
		}
	}
	if table.Kind == KindBudget && taken["unit"] && taken["total"] {
		// Mapping both would leave the line total ambiguous. The stated total
		// is the figure people reconcile against, so that is the proposal.
		delete(table.Columns, "unit")
		notes = append(notes, fmt.Sprintf("%s rows %d-%d: both a unit price and a total column; proposing total — swap them if the unit price is the reliable one",
			sh.Name, first, blk.last))
	}
	// Each kind has its own vocabulary; a field the chosen kind cannot hold
	// is reported as unmapped rather than written into a mapping that would
	// then fail validation.
	for field, ref := range table.Columns {
		if _, err := canonicalField(table.Kind, field); err != nil {
			delete(table.Columns, field)
			unnamed = append(unnamed, fmt.Sprintf("%s (looks like %s, which a %s table has no place for)", ref, field, table.Kind))
		}
	}
	if len(table.Columns) == 0 {
		note := fmt.Sprintf("%s rows %d-%d: no column titles recognised — map them by hand",
			sh.Name, blk.first, blk.last)
		if header > 1 && !sh.RowEmpty(header-1) {
			// No blank row above: blocks() split a run here because this row
			// reads like a header. "CATERING | see quote" reads like one too,
			// and so does a line whose only figure was refused ("2 x 500").
			// The rows above may not have been proposed either, hence the if.
			note += fmt.Sprintf(", or if row %d is not a header (a section heading, or a line whose figure could not be read) and the rows above it were proposed, extend lastRow of the table above it to %d",
				header, blk.last)
		}
		notes = append(notes, note)
		return nil, notes
	}
	if len(unnamed) > 0 {
		notes = append(notes, fmt.Sprintf("%s rows %d-%d: columns left unmapped: %s",
			sh.Name, first, blk.last, strings.Join(unnamed, ", ")))
	}
	if header == 0 {
		notes = append(notes, fmt.Sprintf("%s rows %d-%d: no header row found; set headerRow to catch repeated headers",
			sh.Name, first, blk.last))
	}
	return table, notes
}

// tasksColumns renames the fields a tasks table has its own word for, and
// drops nothing: a field it cannot hold is left in place for the caller's
// "has no place for" loop to report as unmapped. Filtering here instead took
// a column out of the proposal without a word, which is the one thing
// detection is not allowed to do — and a task's deadline is its due date, so
// that column was a date the reminders would never have run on.
func tasksColumns(in map[string]string) map[string]string {
	out := map[string]string{}
	for field, ref := range in {
		switch {
		case field == "item":
			field = "name"
		case field == "lockBy" && in["due"] == "":
			field = "due"
		}
		out[field] = ref
	}
	return out
}

func columnUsed(sh *Sheet, col, first, last int) bool {
	for n := first; n <= last; n++ {
		if !sh.Cell(n, col).Empty() {
			return true
		}
	}
	return false
}

// matchField picks the field whose keywords the title mentions, preferring
// the more specific list (the tables above are ordered accordingly, so
// "unit price" is not claimed by "price" alone).
func matchField(title string) string {
	for _, kw := range headerKeywords {
		for _, w := range kw.words {
			if containsWords(title, w) {
				return kw.field
			}
		}
	}
	return ""
}
