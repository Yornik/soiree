package sheetimport

import (
	"fmt"
	"io"
	"strings"
)

// maxListedRows caps how many individual rows are named per reason. Past that
// the count still tells the truth and the listing stays readable.
const maxListedRows = 25

// RowIssue is one thing that happened to one row.
type RowIssue struct {
	Row  int
	Text string
}

// TableReport accounts for every row in one table's range.
//
// Imported + the skip counts must equal the rows scanned; the report prints
// that arithmetic so a reader can check it rather than take it on trust.
type TableReport struct {
	Name      string
	Sheet     string
	Kind      string
	FirstRow  int
	LastRow   int
	HeaderRow int

	Scanned  int
	Imported int
	Blank    int

	RolledChildren int
	Parents        int
	Costless       int

	Skipped         []RowIssue
	Warnings        []RowIssue
	UnmappedColumns []string
	Notes           []string

	AssumedGrouping int
	AssumedDayFirst int
}

// Accounted is every row the table can explain: imported, folded into a
// parent, blank, or skipped for a stated reason. It must equal Scanned. The
// report prints the arithmetic so that a row lost to a bug shows up as a
// number that does not add up, rather than as nothing at all.
func (t *TableReport) Accounted() int {
	return t.Imported + t.RolledChildren + t.Blank + len(t.Skipped)
}

func (t *TableReport) skip(row int, why string) {
	t.Skipped = append(t.Skipped, RowIssue{Row: row, Text: why})
}

func (t *TableReport) warn(row int, what string) {
	t.Warnings = append(t.Warnings, RowIssue{Row: row, Text: what})
}

// Report is the whole run, written to stderr. Nothing in the importer is
// allowed to discard a row without adding to it.
type Report struct {
	Source  string
	Reading string
	Decimal DecimalMode
	DryRun  bool
	Tables  []TableReport
}

// Totals sums the per-table counts.
func (r *Report) Totals() (imported, skipped, warnings int) {
	for i := range r.Tables {
		t := &r.Tables[i]
		imported += t.Imported
		skipped += t.Blank + len(t.Skipped)
		warnings += len(t.Warnings)
	}
	return imported, skipped, warnings
}

// Write renders the report. It builds the text in memory first: a partial
// report cut off by a write error would be worse than none.
func (r *Report) Write(w io.Writer) error {
	_, err := io.WriteString(w, r.String())
	return err
}

func (r *Report) String() string {
	var b lines

	b.add("sheet-import report\n")
	b.addf("  source: %s\n", r.Source)
	if r.Reading != "" {
		b.addf("  read as: %s\n", r.Reading)
	}
	b.addf("  decimal separator: %s\n", r.Decimal)
	if r.DryRun {
		b.add("  dry run: no JSON written\n")
	}

	for i := range r.Tables {
		t := &r.Tables[i]
		b.add("\n")
		b.addf("table %s — sheet %q rows %d-%d, kind %s\n",
			quoteName(t.Name, i), t.Sheet, t.FirstRow, t.LastRow, t.Kind)
		b.addf("  scanned %d rows, imported %d\n", t.Scanned, t.Imported)
		if t.RolledChildren > 0 {
			b.addf("  rolled %s into %s (their amounts are inside the parent, not beside it)\n",
				count(t.RolledChildren, "child row"), count(t.Parents, "parent item"))
		}
		if t.Blank > 0 {
			b.addf("  skipped %s\n", count(t.Blank, "blank row"))
		}
		if t.Costless > 0 {
			b.addf("  %s carried no amount\n", count(t.Costless, "row"))
		}
		writeIssues(&b, "skipped", t.Skipped)
		writeIssues(&b, "warnings", t.Warnings)
		if len(t.UnmappedColumns) > 0 {
			b.addf("  unmapped columns holding data: %s\n", strings.Join(t.UnmappedColumns, ", "))
		}
		if t.AssumedGrouping > 0 {
			b.addf("  assumed a thousands separator in %s (override with -decimal comma|dot)\n",
				count(t.AssumedGrouping, "figure"))
		}
		if t.AssumedDayFirst > 0 {
			b.addf("  read %s as day-first\n", count(t.AssumedDayFirst, "ambiguous date"))
		}
		for _, n := range t.Notes {
			b.addf("  %s\n", n)
		}
		if t.Accounted() != t.Scanned {
			b.addf("  ATTENTION: %d rows scanned but %d accounted for — this is a bug in the importer, do not trust this run\n",
				t.Scanned, t.Accounted())
		}
	}

	imported, skipped, warnings := r.Totals()
	b.addf("\nsummary: %d imported, %d skipped, %s\n", imported, skipped, count(warnings, "warning"))
	return b.String()
}

// lines accumulates the report text.
//
// Writing to a strings.Builder cannot fail, so the returned errors are
// discarded here — in one place, deliberately, rather than at forty call
// sites where a genuinely ignored error would blend in.
type lines struct{ b strings.Builder }

func (l *lines) add(s string) { _, _ = l.b.WriteString(s) }

func (l *lines) addf(format string, args ...any) { _, _ = fmt.Fprintf(&l.b, format, args...) }

func (l *lines) String() string { return l.b.String() }

// count writes "1 row" and "3 rows", because a report that says "1 rows" is a
// report nobody trusts the rest of.
func count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func writeIssues(b *lines, heading string, issues []RowIssue) {
	if len(issues) == 0 {
		return
	}
	b.addf("  %s (%d)\n", heading, len(issues))
	for i, is := range issues {
		if i == maxListedRows {
			b.addf("    … and %d more\n", len(issues)-maxListedRows)
			break
		}
		b.addf("    row %d: %s\n", is.Row, is.Text)
	}
}

func quoteName(name string, i int) string {
	if name == "" {
		return fmt.Sprintf("#%d", i+1)
	}
	return `"` + name + `"`
}
