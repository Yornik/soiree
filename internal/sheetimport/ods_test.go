package sheetimport

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every fixture in this package is written here, by hand, from invented
// content. The quirks are real — two tables in one sheet, headers repeated
// partway down, figures stored as formatted text, a remark in a numeric
// column — but nothing in them comes from anybody's actual spreadsheet.

const odsHeader = `<?xml version="1.0" encoding="UTF-8"?>` +
	`<office:document-content ` +
	`xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0" ` +
	`xmlns:table="urn:oasis:names:tc:opendocument:xmlns:table:1.0" ` +
	`xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0">` +
	`<office:body><office:spreadsheet>`

const odsFooter = `</office:spreadsheet></office:body></office:document-content>`

// writeODS packs sheets into a minimal but structurally real .ods.
func writeODS(t *testing.T, sheets ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.ods")

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	zw := zip.NewWriter(f)

	// A real .ods stores the mimetype first and uncompressed.
	mw, err := zw.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store})
	if err != nil {
		t.Fatalf("fixture mimetype: %v", err)
	}
	if _, err := mw.Write([]byte("application/vnd.oasis.opendocument.spreadsheet")); err != nil {
		t.Fatalf("fixture mimetype: %v", err)
	}

	cw, err := zw.Create("content.xml")
	if err != nil {
		t.Fatalf("fixture content.xml: %v", err)
	}
	if _, err := io.WriteString(cw, odsHeader+strings.Join(sheets, "")+odsFooter); err != nil {
		t.Fatalf("fixture content.xml: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("fixture zip: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("fixture close: %v", err)
	}
	return path
}

func esc(s string) string {
	var b strings.Builder
	if err := xml.EscapeText(&b, []byte(s)); err != nil {
		panic(err)
	}
	return b.String()
}

// cStr is a text cell: the value lives only in the displayed text, which is
// how a hand-typed "22,500,000" reaches us.
func cStr(s string) string {
	if s == "" {
		return cBlank(1)
	}
	return `<table:table-cell office:value-type="string"><text:p>` + esc(s) + `</text:p></table:table-cell>`
}

// cNum is a typed number with its own formatted display text.
func cNum(v float64, display string) string {
	return fmt.Sprintf(`<table:table-cell office:value-type="float" office:value="%v"><text:p>%s</text:p></table:table-cell>`,
		v, esc(display))
}

// cCur is the currency variant, which carries the same office:value.
func cCur(v float64, display string) string {
	return fmt.Sprintf(`<table:table-cell office:value-type="currency" office:currency="IDR" office:value="%v"><text:p>%s</text:p></table:table-cell>`,
		v, esc(display))
}

// cBlank is a run of empty cells, written the way a spreadsheet writes it.
func cBlank(n int) string {
	return fmt.Sprintf(`<table:table-cell table:number-columns-repeated="%d"/>`, n)
}

// cCovered is the hidden half of a merged cell. It still occupies columns.
func cCovered(n int) string {
	return fmt.Sprintf(`<table:covered-table-cell table:number-columns-repeated="%d"/>`, n)
}

func tRow(cells ...string) string {
	return "<table:table-row>" + strings.Join(cells, "") + "</table:table-row>"
}

func tRowRepeat(n int, cells ...string) string {
	return fmt.Sprintf(`<table:table-row table:number-rows-repeated="%d">`, n) +
		strings.Join(cells, "") + "</table:table-row>"
}

// tRowHidden is a row the sheet does not show: "collapse" is hidden by hand,
// "filter" is hidden by an AutoFilter.
func tRowHidden(how string, cells ...string) string {
	return fmt.Sprintf(`<table:table-row table:visibility="%s">`, how) +
		strings.Join(cells, "") + "</table:table-row>"
}

func tSheet(name string, rows ...string) string {
	return fmt.Sprintf(`<table:table table:name="%s">`, esc(name)) +
		`<table:table-column table:number-columns-repeated="4"/>` +
		strings.Join(rows, "") + "</table:table>"
}

// emptyTail is how every real .ods ends: a million empty rows encoded as one.
// Materialising it is the difference between a 30 kB file and an out of
// memory kill.
const emptyTail = `<table:table-row table:number-rows-repeated="1048570">` +
	`<table:table-cell table:number-columns-repeated="16384"/></table:table-row>`

func TestReadODSAlignsColumns(t *testing.T) {
	path := writeODS(t, tSheet("Grid",
		tRow(cStr("Merged title"), cCovered(3)),
		tRowRepeat(2, cBlank(4)), // two blank rows, written as one
		tRow(cStr("A4"), cBlank(2), cStr("D4")),
		tRow(cNum(1234, "1,234"), cStr("B5")),
		emptyTail,
	))

	book, err := ReadODS(path)
	if err != nil {
		t.Fatalf("ReadODS: %v", err)
	}
	if len(book.Sheets) != 1 || book.Sheets[0].Name != "Grid" {
		t.Fatalf("sheets = %v", book.SheetNames())
	}
	sh := &book.Sheets[0]

	// The million-row tail must not be materialised, and the two blank rows
	// written as one repeat must still count as two.
	if len(sh.Rows) != 5 {
		t.Fatalf("rows = %d; want 5 (the empty tail must be dropped)", len(sh.Rows))
	}
	if !sh.RowEmpty(2) || !sh.RowEmpty(3) {
		t.Error("rows 2 and 3 should be blank")
	}
	// The covered cells of the merge must hold their columns open.
	if got := sh.Cell(1, 1).Text; got != "Merged title" {
		t.Errorf("A1 = %q", got)
	}
	// A blank run in the middle of a row must not shift what follows it.
	if got := sh.Cell(4, 4).Text; got != "D4" {
		t.Errorf("D4 = %q; want D4 (a number-columns-repeated run shifted the row)", got)
	}
	if c := sh.Cell(5, 1); !c.HasValue || c.Value != 1234 || c.Text != "1,234" {
		t.Errorf("A5 = %+v; want the typed value 1234 alongside its display text", c)
	}
}

func TestReadODSIgnoresAnnotations(t *testing.T) {
	// A cell comment is text:p inside the cell; left alone it lands in the
	// cell's own text and quietly corrupts the label.
	cell := `<table:table-cell office:value-type="string">` +
		`<office:annotation><text:p>ask Ada about this</text:p></office:annotation>` +
		`<text:p>Venue deposit</text:p></table:table-cell>`
	path := writeODS(t, tSheet("Notes", tRow(cell)))

	book, err := ReadODS(path)
	if err != nil {
		t.Fatalf("ReadODS: %v", err)
	}
	if got := book.Sheets[0].Cell(1, 1).Text; got != "Venue deposit" {
		t.Errorf("A1 = %q; want the cell text without the comment", got)
	}
}

func TestReadODSRejectsNonODS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not.ods")
	if err := os.WriteFile(path, []byte("this is not a zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadODS(path); err == nil {
		t.Error("ReadODS should refuse a file that is not an .ods")
	}
}

// hiddenRowsBook is a cost table with a row hidden by hand (the venue that
// was rejected) and a row hidden by an AutoFilter.
func hiddenRowsBook(t *testing.T) *Book {
	t.Helper()
	path := writeODS(t, tSheet("Costs",
		tRow(cStr("Item"), cStr("Total")),
		tRow(cStr("Venue"), cNum(2500, "2500")),
		tRowHidden("collapse", cStr("Rejected venue"), cNum(7777, "7777")),
		tRowHidden("filter", cStr("Filtered out"), cNum(8888, "8888")),
		emptyTail,
	))
	book, err := ReadODS(path)
	if err != nil {
		t.Fatalf("ReadODS: %v", err)
	}
	return book
}

func hiddenRowsConfig() *Config {
	return &Config{Tables: []Table{{
		HeaderRow: 1,
		FirstRow:  2,
		Columns:   map[string]string{"item": "A", "total": "B"},
	}}}
}

func TestReadODSMarksTheRowsTheSheetHides(t *testing.T) {
	sh := &hiddenRowsBook(t).Sheets[0]
	for _, n := range []int{3, 4} {
		if !sh.RowHidden(n) {
			t.Errorf("row %d is hidden in the file and does not read as hidden", n)
		}
	}
	if sh.RowHidden(2) {
		t.Error("row 2 is an ordinary row")
	}

	// A hidden row written as a repeat run is every one of those rows.
	twice := `<table:table-row table:visibility="collapse" table:number-rows-repeated="2">` +
		cStr("Rejected") + `</table:table-row>`
	book, err := ReadODS(writeODS(t, tSheet("Costs", tRow(cStr("Venue")), twice)))
	if err != nil {
		t.Fatalf("ReadODS: %v", err)
	}
	repeated := &book.Sheets[0]
	if !repeated.RowHidden(2) || !repeated.RowHidden(3) {
		t.Errorf("a repeated hidden row must mark every row it stands for: %v", repeated.Hidden)
	}
}

// People hide the rows they rejected and leave an AutoFilter on. What they
// read off the screen is their budget; what the file holds is everything, and
// a SUM() in the sheet counts the hidden rows too. So the rows go in, and the
// report says which ones the person has not seen.
func TestConvertReportsRowsTheSheetHides(t *testing.T) {
	book, cfg := hiddenRowsBook(t), hiddenRowsConfig()
	state, report, err := Convert(book, cfg)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	tr := &report.Tables[0]

	if len(state.BudgetItems) != 3 {
		t.Fatalf("imported %d items; want 3, hidden rows are still money", len(state.BudgetItems))
	}
	for _, row := range []int{3, 4} {
		if !warnedRow(tr, row, "hidden") {
			t.Errorf("row %d is hidden in the sheet and the report does not say so; warnings: %v", row, tr.Warnings)
		}
	}
}

func TestConvertSkipsHiddenRowsOnRequest(t *testing.T) {
	book, cfg := hiddenRowsBook(t), hiddenRowsConfig()
	cfg.Tables[0].SkipHidden = true

	state, report, err := Convert(book, cfg)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	tr := &report.Tables[0]

	if len(state.BudgetItems) != 1 {
		t.Fatalf("imported %d items; want 1, the two hidden rows left out", len(state.BudgetItems))
	}
	for _, row := range []int{3, 4} {
		if !skippedRow(tr, row, "hidden") {
			t.Errorf("row %d was left out and the report does not say why; skips: %v", row, tr.Skipped)
		}
	}
	// A row left out is still a row the table answers for.
	if tr.Accounted() != tr.Scanned {
		t.Errorf("scanned %d rows, accounted for %d", tr.Scanned, tr.Accounted())
	}
}

// office:value goes to the output untouched, and NaN or Inf is a value JSON
// cannot encode: the run reported an import and then failed writing it, after
// the previous plan.json had already been truncated.
func TestReadODSIgnoresAValueJSONCannotWrite(t *testing.T) {
	cell := `<table:table-cell office:value-type="float" office:value="NaN"><text:p>1.234</text:p></table:table-cell>`
	path := writeODS(t, tSheet("Costs", tRow(cStr("Venue"), cell)))

	book, err := ReadODS(path)
	if err != nil {
		t.Fatalf("ReadODS: %v", err)
	}
	c := book.Sheets[0].Cell(1, 2)
	if c.HasValue {
		t.Errorf("B1 = %+v; want the value left to the text parser, not carried into the plan", c)
	}
	if c.Text != "1.234" {
		t.Errorf("B1 text = %q; want the displayed figure kept", c.Text)
	}
}

// A repeat run is how a sheet says "and the same again". A file of a few
// hundred bytes can say it four thousand columns wide and two hundred
// thousand rows deep, which is tens of gigabytes of cells: the caps at the
// top of ods.go are per row and per column, so neither of them sees it.
func TestReadODSRefusesARepeatRunTooBigToHold(t *testing.T) {
	wide := `<table:table-cell office:value-type="string" table:number-columns-repeated="4000">` +
		`<text:p>x</text:p></table:table-cell>`
	path := writeODS(t, tSheet("Bomb", tRowRepeat(2000, wide)))

	if _, err := ReadODS(path); err == nil {
		t.Error("ReadODS built 8 million cells out of a 700-byte file without complaint")
	}
}
