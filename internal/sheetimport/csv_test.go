package sheetimport

import (
	"strings"
	"testing"
)

// A semicolon-delimited file with a header repeated partway down, a status
// nobody standardised, and a deadline written as prose.
const tasksCSV = "Task;Owner;Due;Status\n" +
	"Book the venue;Ada;03/04/2027;done\n" +
	"Send invitations;Grace;2027-05-01;in progress\n" +
	"Order flowers;Linus;;whenever\n" +
	"Task;Owner;Due;Status\n" +
	"Confirm final numbers;Ada;next spring;todo\n"

func TestConvertTasksFromCSV(t *testing.T) {
	book, comma, err := ReadCSV(strings.NewReader(tasksCSV), 0)
	if err != nil {
		t.Fatalf("ReadCSV: %v", err)
	}
	if comma != ';' {
		t.Fatalf("sniffed delimiter %q; want ';'", comma)
	}

	cfg := &Config{Tables: []Table{{
		Name:      "tasks",
		Kind:      KindTasks,
		HeaderRow: 1,
		FirstRow:  2,
		Columns:   map[string]string{"name": "A", "owner": "B", "due": "C", "status": "D"},
	}}}

	state, report, err := Convert(book, cfg)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	tr := &report.Tables[0]

	if len(state.Tasks) != 4 {
		t.Fatalf("imported %d tasks; want 4", len(state.Tasks))
	}
	if tr.Accounted() != tr.Scanned {
		t.Errorf("scanned %d rows, accounted for %d", tr.Scanned, tr.Accounted())
	}
	if !skippedRow(tr, 5, "repeats the header") {
		t.Errorf("the repeated header should be skipped; skips: %v", tr.Skipped)
	}

	byName := map[string]Task{}
	for _, task := range state.Tasks {
		byName[task.Name] = task
	}

	// Both halves of 03/04/2027 are plausible months, so the reading is an
	// assumption and the report has to say how many it made.
	if got := byName["Book the venue"].Due; got != "2027-04-03" {
		t.Errorf("due = %q; want 2027-04-03 read day-first", got)
	}
	if tr.AssumedDayFirst != 1 {
		t.Errorf("day-first assumptions reported = %d; want 1", tr.AssumedDayFirst)
	}
	if got := byName["Book the venue"].Status; got != "done" {
		t.Errorf("status = %q; want done", got)
	}
	if got := byName["Send invitations"].Status; got != "in-progress" {
		t.Errorf("status = %q; want in-progress", got)
	}

	// An unknown status becomes the safe value and is reported. Guessing that
	// "whenever" means done would put a lie in the planner.
	if got := byName["Order flowers"].Status; got != "not-started" {
		t.Errorf("status = %q; want not-started", got)
	}
	if !warnedRow(tr, 4, "whenever") {
		t.Errorf("the unknown status should be warned about; warnings: %v", tr.Warnings)
	}

	// A task has nowhere to keep unreadable text, so the warning is the only
	// record — it must exist.
	if got := byName["Confirm final numbers"].Due; got != "" {
		t.Errorf("due = %q; want empty", got)
	}
	if !warnedRow(tr, 6, "due date") {
		t.Errorf("the unreadable deadline should be warned about; warnings: %v", tr.Warnings)
	}
}

func TestReportNamesWhatItDropped(t *testing.T) {
	book, _, err := ReadCSV(strings.NewReader(tasksCSV), ';')
	if err != nil {
		t.Fatalf("ReadCSV: %v", err)
	}
	cfg := &Config{Tables: []Table{{
		Name:      "tasks",
		Kind:      KindTasks,
		HeaderRow: 1,
		FirstRow:  2,
		Columns:   map[string]string{"name": "A", "owner": "B"},
	}}}
	_, report, err := Convert(book, cfg)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	report.Source = "tasks.csv"

	out := report.String()
	for _, want := range []string{"row 5", "repeats the header", "unmapped columns", "summary:"} {
		if !strings.Contains(out, want) {
			t.Errorf("report does not mention %q:\n%s", want, out)
		}
	}
	// The columns nobody mapped are how an operator notices the column they
	// forgot; it must name them.
	if !strings.Contains(out, "Due") || !strings.Contains(out, "Status") {
		t.Errorf("report should name the unmapped Due and Status columns:\n%s", out)
	}
}

func TestSniffDelimiter(t *testing.T) {
	cases := map[string]rune{
		"a,b,c\n1,2,3\n":        ',',
		"a;b;c\n1;2;3\n":        ';',
		"a\tb\tc\n1\t2\t3\n":    '\t',
		"only one column\n":     ',',
		"a,b;c\n1,2;3\n1,2;3\n": ',',
	}
	for in, want := range cases {
		if got := sniffDelimiter([]byte(in)); got != want {
			t.Errorf("sniffDelimiter(%q) = %q; want %q", in, got, want)
		}
	}
}

// Excel's plain "CSV (comma delimited)" is Windows-1252, and its "Unicode
// text" is UTF-16. Read as UTF-8 the first turns every accented letter into a
// replacement character and the second puts a NUL between every letter.
// Neither used to say anything, and a vendor called "Café René" cannot be
// recovered from the mangled text afterwards, while the sheet it came from
// can still be exported again. So the read stops and says which.
func TestReadCSVRefusesTextThatIsNotUTF8(t *testing.T) {
	cases := map[string]struct{ raw, want string }{
		"windows-1252": {raw: "Item;Vendor\nFlowers;Caf\xe9 Ren\xe9\n", want: "not UTF-8"},
		"utf-16":       {raw: "\xff\xfeI\x00t\x00e\x00m\x00\n\x00", want: "UTF-16"},
		"nul":          {raw: "Item;Vendor\nFlowers;Cat\x00ering\n", want: "NUL"},
	}
	for name, c := range cases {
		_, _, err := ReadCSV(strings.NewReader(c.raw), ';')
		if err == nil {
			t.Errorf("%s: ReadCSV accepted it; the text cannot be repaired once it is in the plan", name)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v; want the refusal to name %s", name, err, c.want)
		}
		// A refusal is only useful if it says what to do with the file.
		if !strings.Contains(err.Error(), ".ods") {
			t.Errorf("%s: %v; want the way out named", name, err)
		}
	}
}

func TestReadCSVKeepsAccentsThatAreUTF8(t *testing.T) {
	book, _, err := ReadCSV(strings.NewReader("Item;Vendor\nFlowers;Café René\n"), ';')
	if err != nil {
		t.Fatalf("ReadCSV: %v", err)
	}
	if got := book.Sheets[0].Cell(2, 2).Text; got != "Café René" {
		t.Errorf("B2 = %q; want the name as the file spells it", got)
	}
}

// LazyQuotes is on so that a stray quote in free text cannot abort the read,
// and the price of that is an unclosed quote swallowing the lines under it
// into one cell. Nothing is lost, but the rows underneath are gone from the
// budget and the row numbers after it no longer match the file's lines, so
// the report has to name the row it happened to.
func TestReportNamesARowReadFromSeveralLines(t *testing.T) {
	const csv = "Item,Total\n" +
		"Venue,2500\n" +
		"\"Unclosed,400\n" +
		"Flowers,500\n"
	book, _, err := ReadCSV(strings.NewReader(csv), ',')
	if err != nil {
		t.Fatalf("ReadCSV: %v", err)
	}
	cfg := &Config{Tables: []Table{{
		HeaderRow: 1,
		FirstRow:  2,
		Columns:   map[string]string{"item": "A", "total": "B"},
	}}}
	_, report, err := Convert(book, cfg)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	tr := &report.Tables[0]
	if !warnedRow(tr, 3, "lines 3-4") {
		t.Errorf("row 3 was read from two lines of the file and nothing says so; warnings: %v", tr.Warnings)
	}
}

// Most people's planning sheet is an .xlsx, so the first thing the tool ever
// says to them is this message. It has to name the way out.
func TestOpenFileSaysWhatToDoWithASheetItCannotRead(t *testing.T) {
	_, _, err := OpenFile("plan.xlsx", 0)
	if err == nil {
		t.Fatal("OpenFile read an .xlsx")
	}
	for _, want := range []string{".ods", "save"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%v; want it to mention %q", err, want)
		}
	}
	// .txt is accepted by the reader, so leaving it out of the list is a way
	// to be told that a file which works is unsupported.
	_, _, err = OpenFile("plan.pdf", 0)
	if err == nil || !strings.Contains(err.Error(), ".txt") {
		t.Errorf("%v; want the accepted types listed in full", err)
	}
}
