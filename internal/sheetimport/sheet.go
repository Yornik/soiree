// Package sheetimport turns a spreadsheet a person made for people into the
// JSON the planner's "Import data" button reads.
//
// The whole package obeys two rules, and they are the reason it exists rather
// than a twenty-line csv-to-json script:
//
//  1. Never guess. The caller supplies the column mapping. Where an
//     assumption is unavoidable — which separator is the decimal point, which
//     half of a date is the month — the assumption is made once, applied
//     consistently, and disclosed in the report.
//  2. Never drop a row quietly. Every row that does not reach the output is
//     accounted for by number and reason. An importer that silently loses
//     three lines of a budget is worse than no importer, because nobody
//     recounts a file that "worked".
package sheetimport

import (
	"fmt"
	"strconv"
	"strings"
)

// Cell is one spreadsheet cell: what it displays, and — for formats that
// carry it — what it actually holds.
//
// Keeping both matters. A cell can display "22,500,000" while storing
// 22500000, and it can display a number while storing a string. The typed
// value is authoritative when present; the text is all there is otherwise.
type Cell struct {
	Text     string
	Value    float64
	HasValue bool
}

// Empty reports a cell with nothing in it at all.
func (c Cell) Empty() bool { return c.Text == "" && !c.HasValue }

// Sheet is one table of a workbook. Rows[i] is spreadsheet row i+1 and
// Rows[i][j] is column j+1, padded so that indices line up with what the
// operator reads off their screen — row numbers in the report have to match
// the row numbers in LibreOffice or the report is useless for fixing the
// mapping.
type Sheet struct {
	Name string
	Rows [][]Cell
	// Hidden marks the rows the spreadsheet itself does not show: collapsed
	// by hand, or filtered out. They hold ordinary data and the sheet's own
	// SUM() counts them, so they are read like any other row, but the person
	// whose budget this is has not seen them on screen.
	Hidden map[int]bool
}

// RowHidden reports a row the spreadsheet does not display.
func (s *Sheet) RowHidden(n int) bool { return s.Hidden[n] }

func (s *Sheet) markHidden(n int) {
	if s.Hidden == nil {
		s.Hidden = map[int]bool{}
	}
	s.Hidden[n] = true
}

// Row returns spreadsheet row n (1-based), or nil past the end.
func (s *Sheet) Row(n int) []Cell {
	if n < 1 || n > len(s.Rows) {
		return nil
	}
	return s.Rows[n-1]
}

// Cell returns the cell at 1-based row/column, zero if out of range.
func (s *Sheet) Cell(row, col int) Cell {
	r := s.Row(row)
	if col < 1 || col > len(r) {
		return Cell{}
	}
	return r[col-1]
}

// Width is the widest row in the sheet.
func (s *Sheet) Width() int {
	w := 0
	for _, r := range s.Rows {
		if len(r) > w {
			w = len(r)
		}
	}
	return w
}

// RowEmpty reports a row with no content in any column.
func (s *Sheet) RowEmpty(n int) bool {
	for _, c := range s.Row(n) {
		if !c.Empty() {
			return false
		}
	}
	return true
}

// Book is a workbook: one sheet for CSV, several for ODS.
type Book struct {
	Sheets []Sheet
}

// Lookup finds a sheet by name, or failing that by 1-based position. Name
// wins, so a sheet literally called "2" is still reachable.
func (b *Book) Lookup(ref string) (*Sheet, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		if len(b.Sheets) == 0 {
			return nil, fmt.Errorf("file holds no sheets")
		}
		return &b.Sheets[0], nil
	}
	for i := range b.Sheets {
		if strings.EqualFold(b.Sheets[i].Name, ref) {
			return &b.Sheets[i], nil
		}
	}
	if n, err := strconv.Atoi(ref); err == nil && n >= 1 && n <= len(b.Sheets) {
		return &b.Sheets[n-1], nil
	}
	return nil, fmt.Errorf("no sheet %q (have %s)", ref, strings.Join(b.SheetNames(), ", "))
}

// SheetNames lists the sheets in file order.
func (b *Book) SheetNames() []string {
	out := make([]string, 0, len(b.Sheets))
	for i := range b.Sheets {
		out = append(out, b.Sheets[i].Name)
	}
	return out
}

// ColumnLabel renders a 1-based column index the way a spreadsheet does.
func ColumnLabel(n int) string {
	if n < 1 {
		return ""
	}
	var b []byte
	for n > 0 {
		n--
		b = append([]byte{byte('A' + n%26)}, b...)
		n /= 26
	}
	return string(b)
}

// ParseColumnRef accepts either a spreadsheet column letter ("A", "AC") or a
// 1-based number, and returns the 1-based index. Both spellings exist in the
// wild and refusing one of them buys nothing.
func ParseColumnRef(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty column reference")
	}
	if n, err := strconv.Atoi(s); err == nil {
		if n < 1 {
			return 0, fmt.Errorf("column %q must be 1 or greater", s)
		}
		return n, nil
	}
	n := 0
	for _, r := range strings.ToUpper(s) {
		if r < 'A' || r > 'Z' {
			return 0, fmt.Errorf("%q is not a column letter or number", s)
		}
		n = n*26 + int(r-'A') + 1
	}
	return n, nil
}
