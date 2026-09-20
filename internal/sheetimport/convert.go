package sheetimport

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

// sumTolerance is the relative slack for "this row equals the rows above it".
// Exact equality never fires: the sheet's own total is rounded, and the
// figures reaching us have been through a text parser.
const sumTolerance = 0.005

// minSumRows is how many rows must precede a figure before matching their sum
// means anything. Two rows summing to the third happens by accident.
const minSumRows = 2

// Convert reads the workbook according to the mapping and returns the
// planner state together with the report. The report is returned even on
// success — especially on success, since a clean exit is exactly when nobody
// thinks to check what was dropped.
func Convert(book *Book, cfg *Config) (*State, *Report, error) {
	if err := cfg.Validate(); err != nil {
		return nil, nil, err
	}
	mode, err := ParseDecimalMode(cfg.Decimal)
	if err != nil {
		return nil, nil, err
	}

	c := &converter{
		cfg:   cfg,
		book:  book,
		mode:  mode,
		ids:   newIDGen(),
		state: NewState(),
		rep:   &Report{Decimal: mode, Currency: cfg.Currency, Tables: make([]TableReport, len(cfg.Tables))},
	}
	c.state.Ceiling = cfg.Ceiling
	c.state.InflationPct = cfg.InflationPct

	// Ordinary tables first. A breakdown table names its parent by item text,
	// and that parent may be defined in any table, including a later one.
	for pass := 0; pass < 2; pass++ {
		for i := range cfg.Tables {
			if (cfg.Tables[i].ParentItem != "") != (pass == 1) {
				continue
			}
			if err := c.table(i); err != nil {
				return nil, nil, err
			}
		}
	}
	c.reportOutside()
	return c.state, c.rep, nil
}

type converter struct {
	cfg   *Config
	book  *Book
	mode  DecimalMode
	ids   *idGen
	state *State
	rep   *Report
	// cols is the current table's column index -> field, resolved once per
	// table rather than per row.
	cols map[int]string
	// read is, per sheet some table touches, the rows that a table answers
	// for: its row range and its header row.
	read map[*Sheet]map[int]bool
}

// builtRow is a row that survived the skip checks, still carrying where it
// came from so warnings can name a row number the operator can navigate to.
type builtRow struct {
	item  BudgetItem
	row   int
	child bool
}

func (c *converter) table(idx int) error {
	t := &c.cfg.Tables[idx]
	tr := &c.rep.Tables[idx]

	sh, err := c.book.Lookup(t.Sheet)
	if err != nil {
		return fmt.Errorf("table %s: %w", t.label(idx), err)
	}
	first, last := rowRange(t, sh)
	c.claim(sh, t.HeaderRow, first, last)

	c.cols = t.mappedColumns()
	tr.Name, tr.Sheet, tr.Kind = t.Name, sh.Name, t.kind()
	tr.FirstRow, tr.LastRow, tr.HeaderRow = first, last, t.HeaderRow
	if t.HeaderRow == 0 {
		tr.Notes = append(tr.Notes, "no headerRow set — repeated header rows cannot be recognised in this table")
	}
	c.reportUnmapped(t, tr, sh, first, last)

	switch t.kind() {
	case KindBudget:
		return c.budgetTable(t, tr, sh, first, last)
	case KindTasks:
		c.taskTable(t, tr, sh, first, last)
	case KindNotes:
		c.noteTable(t, tr, sh, first, last)
	}
	return nil
}

// claim records the rows a table answers for.
func (c *converter) claim(sh *Sheet, header, first, last int) {
	if c.read == nil {
		c.read = map[*Sheet]map[int]bool{}
	}
	rows := c.read[sh]
	if rows == nil {
		rows = map[int]bool{}
		c.read[sh] = rows
	}
	rows[header] = true
	for n := first; n <= last; n++ {
		rows[n] = true
	}
}

// reportOutside lists, for every sheet a table reads from, the rows that hold
// something and that no table answers for.
//
// "Never drop a row quietly" is kept table by table, and that is not enough
// on its own: a mapping whose range ends at a section heading accounts for
// every row it scanned and says nothing of the rows beneath. A sheet no table
// names at all is left alone, because leaving a whole sheet out is something
// the operator did on purpose.
func (c *converter) reportOutside() {
	for i := range c.book.Sheets {
		sh := &c.book.Sheets[i]
		read, touched := c.read[sh]
		if !touched {
			continue
		}
		var rows []int
		for n := 1; n <= len(sh.Rows); n++ {
			if !read[n] && !sh.RowEmpty(n) {
				rows = append(rows, n)
			}
		}
		if len(rows) > 0 {
			c.rep.Outside = append(c.rep.Outside, SheetGap{Sheet: sh.Name, Rows: rows})
		}
	}
}

// rowRange resolves the 1-based, inclusive data range.
func rowRange(t *Table, sh *Sheet) (first, last int) {
	first = t.FirstRow
	if first == 0 {
		first = 1
		if t.HeaderRow > 0 {
			first = t.HeaderRow + 1
		}
	}
	last = t.LastRow
	if last == 0 || last > len(sh.Rows) {
		last = len(sh.Rows)
	}
	return first, last
}

// skipRow applies the checks every kind shares and returns the row's label.
// ok is false when the row does not become anything. summary is set when what
// skipped it was a total keyword: the row is gone, but the budget's sum check
// still has to know that a section closed there.
func (c *converter) skipRow(t *Table, tr *TableReport, sh *Sheet, n int, labelField string) (label string, summary, ok bool) {
	if sh.RowEmpty(n) {
		tr.Blank++
		return "", false, false
	}

	mappedEmpty := true
	for col := range c.cols {
		if !sh.Cell(n, col).Empty() {
			mappedEmpty = false
			break
		}
	}
	if mappedEmpty {
		tr.skip(n, "nothing in any mapped column")
		return "", false, false
	}

	if t.HeaderRow > 0 && rowRepeatsHeader(sh, t.HeaderRow, n) {
		tr.skip(n, fmt.Sprintf("repeats the header from row %d", t.HeaderRow))
		return "", false, false
	}

	label = c.text(sh, t, n, labelField)
	if label == "" {
		// Fall back to the leftmost mapped cell holding text, so a summary
		// row that puts "TOTAL" in an unexpected column is still recognised.
		// Numeric cells are skipped: naming a row after the quantity that
		// happens to sit furthest left would be worse than leaving it blank.
		for col := 1; col <= sh.Width(); col++ {
			if _, mapped := c.cols[col]; !mapped {
				continue
			}
			cell := sh.Cell(n, col)
			if cell.Empty() || cell.HasValue {
				continue
			}
			if _, err := ParseNumber(cell.Text, c.mode); err == nil {
				continue
			}
			label = cell.Display()
			break
		}
	}
	for _, kw := range t.totalKeywords(c.cfg) {
		if matchesSummaryKeyword(label, kw) {
			tr.skip(n, fmt.Sprintf("summary row (matched keyword %q)", kw))
			return "", true, false
		}
	}
	return label, false, true
}

// sumRun is a running figure that a later row is measured against.
type sumRun struct {
	sum  float64
	rows int
}

func (s *sumRun) add(v float64) {
	s.sum += v
	s.rows++
}

// matches reports a figure that equals the run so far.
func (s sumRun) matches(v float64) bool {
	return s.rows >= minSumRows && s.sum > 0 && v != 0 &&
		math.Abs(v-s.sum) <= sumTolerance*s.sum
}

func (c *converter) budgetTable(t *Table, tr *TableReport, sh *Sheet, first, last int) error {
	var (
		rows []builtRow
		// A subtotal adds up its own section and the total at the bottom adds
		// up every line, so there are two figures to measure a row against.
		// Neither may ever hold a summary row: leave one subtotal in the sum
		// and every comparison after it is against a figure no row will
		// equal, which is how a sheet in sections came in at three times its
		// size with a single warning.
		//
		// A flag can be a coincidence, though, and then the row left out was a
		// line: two of 500 under a heading, and the total at the bottom equals
		// neither sum. So the third figure takes the other reading, in which
		// every row flagged so far was money. It is the plain running sum this
		// check began as, and keeping it means nothing that sum caught is
		// missed now. What it does not reach is a coincidence inside a later
		// section: there the subtotal still holds the earlier ones.
		section, grand, all sumRun
		lastSummary         int
	)

	for n := first; n <= last; n++ {
		tr.Scanned++
		label, summary, ok := c.skipRow(t, tr, sh, n, "item")
		if summary {
			section, lastSummary = sumRun{}, n
		}
		if !ok {
			continue
		}

		item := BudgetItem{Sponsors: []string{}}
		child := false
		if t.dashChildren() {
			item.Item, child = splitChildPrefix(label, c.cfg.childPrefixes())
		} else {
			item.Item = label
		}
		item.Vendor = c.text(sh, t, n, "vendor")
		item.Phase = c.text(sh, t, n, "phase")
		item.Note = c.text(sh, t, n, "note")

		qty := 1.0
		if q := c.number(sh, t, n, "qty"); q.Present {
			if v, ok := c.value(tr, n, "qty", q, &item.Note); ok {
				qty = v
			}
		}
		switch total := c.number(sh, t, n, "total"); {
		case total.Present:
			// The sheet states the line total. Keep the quantity, because it
			// is meaningful to a planner, and derive the unit price from it.
			v, _ := c.value(tr, n, "total", total, &item.Note)
			if qty != 0 {
				item.Unit, item.Qty = v/qty, qty
				// A row that folds into the one above is exempt: the page
				// never reads a child, and its amount reaches the budget as
				// part of the parent's.
				if folds := child && len(rows) > 0; !folds {
					c.checkStatedTotal(tr, n, v, qty)
				}
			} else {
				item.Unit, item.Qty = v, 1
			}
		default:
			unit := c.number(sh, t, n, "unit")
			v, _ := c.value(tr, n, "unit", unit, &item.Note)
			item.Unit, item.Qty = v, qty
		}
		if paid := c.number(sh, t, n, "paid"); paid.Present {
			v, _ := c.value(tr, n, "paid", paid, &item.Note)
			item.Paid = v
		}
		if raw := c.text(sh, t, n, "lockBy"); raw != "" {
			iso, dayFirst, err := parseDate(raw)
			switch {
			case errors.Is(err, ErrNoValue):
			case err != nil:
				tr.warn(n, fmt.Sprintf("lockBy: %v — kept in the note", err))
				item.Note = appendNote(item.Note, "lockBy: "+raw)
			default:
				item.LockBy = iso
				if dayFirst {
					tr.AssumedDayFirst++
				}
			}
		}

		lineTotal := item.Total()

		// The keyword list cannot know every word for "total", so flag the
		// arithmetic as well: a figure that equals everything above it is
		// almost never another line item. It only ever warns: arithmetic can
		// be a coincidence, and a deleted line is money missing from the
		// budget.
		var equals string
		switch {
		case section.matches(lineTotal) && lastSummary == 0:
			equals = fmt.Sprintf("the %d rows above it", section.rows)
		case section.matches(lineTotal):
			equals = fmt.Sprintf("the %d rows since the summary row at row %d", section.rows, lastSummary)
		case grand.matches(lineTotal):
			equals = fmt.Sprintf("all %d rows above it that are not summary rows themselves", grand.rows)
		case all.matches(lineTotal):
			equals = fmt.Sprintf("all %d rows above it, the ones flagged as sums counted as lines", all.rows)
		}
		if equals != "" {
			tr.warn(n, fmt.Sprintf("%s equals the sum of %s — if this is a total line, add its exact label to totalKeywords (matching is exact, not by substring) or leave it outside the row range",
				formatNumber(lineTotal), equals))
			section, lastSummary = sumRun{}, n
		} else {
			section.add(lineTotal)
			grand.add(lineTotal)
		}
		all.add(lineTotal)

		rows = append(rows, builtRow{item: item, row: n, child: child})
	}

	items, rowOf := assemble(rows, tr)
	for i := range items {
		c.rollUp(&items[i], tr, rowOf[i])
	}

	// Costless rows are judged after the roll-up, never during the scan. A
	// parent row carries no amount of its own until its children have been
	// folded into it, so dropping it earlier would leave those children to
	// attach to whatever line happened to precede it — the decorations
	// quietly becoming part of the catering bill.
	kept := make([]BudgetItem, 0, len(items))
	for i := range items {
		if items[i].Total() == 0 && items[i].Paid == 0 && len(items[i].Children) == 0 {
			tr.Costless++
			if t.SkipRowsWithoutAmount {
				tr.skip(rowOf[i], "no amount in the row")
				continue
			}
		}
		kept = append(kept, items[i])
	}
	for i := range kept {
		c.assignIDs(&kept[i])
	}
	tr.Imported = len(kept)

	if t.ParentItem != "" {
		return c.attach(t.ParentItem, kept, tr)
	}
	c.state.BudgetItems = append(c.state.BudgetItems, kept...)
	return nil
}

// checkStatedTotal warns when total/qty will not come back as total.
//
// The planner stores the unit price, in whole minor units, and works the line
// total out from it. 2500 over 300 invitations is 8.33 a piece and 2499.00 a
// line; with guest-count quantities the gap is whole euros, and the imported
// budget stops adding up to the figure at the bottom of the sheet, which is
// the first thing anybody checks. The plan has nowhere to keep a stated
// total, so this cannot be put right here without giving up the quantity. It
// can be said.
func (c *converter) checkStatedTotal(tr *TableReport, n int, total, qty float64) {
	exp := exponent(c.cfg.Currency)
	// Set by the check, not by the warning: the run where nothing fires is
	// the one where a wrong number of decimals would go unnoticed.
	c.rep.TotalsChecked = true
	stated := roundHalfUp(total * math.Pow10(exp))
	shown := plannerTotal(total/qty, qty, exp)
	if shown == stated {
		return
	}
	tr.warn(n, fmt.Sprintf("total %s over qty %s is not a whole unit price at %s, so the planner will show this line as %s, not %s: map the unit price instead if the sheet has one, or correct the line after the import",
		formatNumber(total), formatNumber(qty), count(exp, "decimal"), formatMinor(shown, exp), formatMinor(stated, exp)))
}

// assemble folds dash-prefixed rows into the row above them.
func assemble(rows []builtRow, tr *TableReport) ([]BudgetItem, []int) {
	items := make([]BudgetItem, 0, len(rows))
	rowOf := make([]int, 0, len(rows))
	for _, br := range rows {
		if br.child && len(items) > 0 {
			last := len(items) - 1
			items[last].Children = append(items[last].Children, br.item)
			continue
		}
		if br.child {
			tr.warn(br.row, "starts with a dash but has no row above it to belong to — imported as its own line")
		}
		items = append(items, br.item)
		rowOf = append(rowOf, br.row)
	}
	return items, rowOf
}

// rollUp folds child amounts into the parent.
//
// The planner's budget is a flat list, so parent and children cannot both
// carry money: counting the caterer's line and the caterer's thirty dishes
// would double the budget. The children stay attached for the record.
func (c *converter) rollUp(parent *BudgetItem, tr *TableReport, row int) {
	if len(parent.Children) == 0 {
		return
	}
	var total, paid float64
	for _, ch := range parent.Children {
		total += ch.Total()
		paid += ch.Paid
	}
	switch {
	case parent.Total() == 0:
		parent.Unit, parent.Qty = total, 1
	case total != 0 && math.Abs(parent.Total()-total) > sumTolerance*math.Abs(parent.Total()):
		// The agreed top-line figure wins over the sum of its parts, which is
		// usually a working estimate — but the disagreement is reported.
		tr.warn(row, fmt.Sprintf("kept its own amount %s; the %d rows under it add up to %s",
			formatNumber(parent.Total()), len(parent.Children), formatNumber(total)))
	}
	if parent.Paid == 0 {
		parent.Paid = paid
	}
	parent.Note = appendNote(parent.Note, breakdownNote(parent.Children))
	tr.RolledChildren += len(parent.Children)
	tr.Parents++
}

// assignIDs numbers an item and its children. Done last so that ids follow
// the order rows appear in the output.
func (c *converter) assignIDs(item *BudgetItem) {
	item.ID = c.ids.next("b")
	for i := range item.Children {
		c.assignIDs(&item.Children[i])
	}
}

// attach hangs a breakdown table under one existing budget item.
//
// The match is exact on the normalised item text. Fuzzy matching here would
// silently file a caterer's quote under the florist, and nothing downstream
// would ever reveal it.
func (c *converter) attach(parentName string, items []BudgetItem, tr *TableReport) error {
	want := normalizeText(parentName)
	found := -1
	matches := 0
	for i := range c.state.BudgetItems {
		if normalizeText(c.state.BudgetItems[i].Item) == want {
			found = i
			matches++
		}
	}
	switch {
	case matches == 0:
		return fmt.Errorf("parentItem %q matches no imported budget item — check the spelling against the other table", parentName)
	case matches > 1:
		return fmt.Errorf("parentItem %q matches %d budget items — it has to identify exactly one", parentName, matches)
	}

	parent := &c.state.BudgetItems[found]
	parent.Children = append(parent.Children, items...)
	tr.Notes = append(tr.Notes, fmt.Sprintf("attached %d rows under budget item %q", len(items), parent.Item))

	var total float64
	for _, ch := range items {
		total += ch.Total()
	}
	switch {
	case parent.Total() == 0:
		// The main sheet had no figure for this line; the breakdown is the
		// figure.
		parent.Unit, parent.Qty = total, 1
	case total != 0 && math.Abs(parent.Total()-total) > sumTolerance*math.Abs(parent.Total()):
		// The line on the main sheet is what was agreed, so it stands — but a
		// quote that no longer adds up to it is exactly what a planner needs
		// told.
		tr.Notes = append(tr.Notes, fmt.Sprintf("budget item %q stays at %s; the breakdown adds up to %s",
			parent.Item, formatNumber(parent.Total()), formatNumber(total)))
	}
	parent.Note = appendNote(parent.Note, breakdownNote(items))
	return nil
}

func (c *converter) taskTable(t *Table, tr *TableReport, sh *Sheet, first, last int) {
	for n := first; n <= last; n++ {
		tr.Scanned++
		label, _, ok := c.skipRow(t, tr, sh, n, "name")
		if !ok {
			continue
		}
		task := Task{ID: c.ids.next("t"), Name: label, Owner: c.text(sh, t, n, "owner"), Status: "not-started"}

		if raw := c.text(sh, t, n, "due"); raw != "" {
			iso, dayFirst, err := parseDate(raw)
			switch {
			case errors.Is(err, ErrNoValue):
			case err != nil:
				// A task has nowhere to park unparsable text, so the warning
				// is the only record — which is why it is a warning and not a
				// silent blank.
				tr.warn(n, fmt.Sprintf("due date: %v — left empty", err))
			default:
				task.Due = iso
				if dayFirst {
					tr.AssumedDayFirst++
				}
			}
		}
		if raw := c.text(sh, t, n, "status"); raw != "" {
			status, known := normalizeStatus(raw)
			if !known {
				tr.warn(n, fmt.Sprintf("status %q is not one of not-started, in-progress or done — imported as not-started", raw))
			}
			task.Status = status
		}
		c.state.Tasks = append(c.state.Tasks, task)
		tr.Imported++
	}
}

func (c *converter) noteTable(t *Table, tr *TableReport, sh *Sheet, first, last int) {
	for n := first; n <= last; n++ {
		tr.Scanned++
		label, _, ok := c.skipRow(t, tr, sh, n, "text")
		if !ok {
			continue
		}
		c.state.Notes = append(c.state.Notes, Note{ID: c.ids.next("n"), Text: label})
		tr.Imported++
	}
}

// text reads a mapped text column.
func (c *converter) text(sh *Sheet, t *Table, n int, field string) string {
	col, ok := t.column(field)
	if !ok {
		return ""
	}
	return sh.Cell(n, col).Display()
}

// cellNumber is one attempt at reading a figure out of a cell.
type cellNumber struct {
	Value   float64
	Present bool
	Assumed bool
	Raw     string
	Err     error
}

// number reads a mapped numeric column.
func (c *converter) number(sh *Sheet, t *Table, n int, field string) cellNumber {
	col, ok := t.column(field)
	if !ok {
		return cellNumber{}
	}
	cell := sh.Cell(n, col)
	if cell.HasValue {
		// The file states the value exactly and locale-independently. Running
		// it through the separator heuristic could only corrupt it.
		return cellNumber{Value: cell.Value, Present: true, Raw: cell.Display()}
	}
	raw := cell.Text
	num, err := ParseNumber(raw, c.mode)
	switch {
	case errors.Is(err, ErrNoValue):
		return cellNumber{Raw: raw}
	case err != nil:
		return cellNumber{Present: true, Raw: raw, Err: err}
	}
	return cellNumber{Value: num.Value, Present: true, Assumed: num.AssumedGrouping, Raw: raw}
}

// value unwraps a cellNumber, reporting failures and preserving the text that
// could not be read. A remark that leaked into a numeric column — a deadline
// written where a price belongs — is real information; it goes into the note
// rather than into the bin.
func (c *converter) value(tr *TableReport, n int, field string, cn cellNumber, note *string) (float64, bool) {
	if cn.Err != nil {
		tr.warn(n, fmt.Sprintf("%s column: %v — kept in the note", field, cn.Err))
		*note = appendNote(*note, field+": "+cn.Raw)
		return 0, false
	}
	if cn.Assumed {
		tr.AssumedGrouping++
	}
	return cn.Value, cn.Present
}

// rowRepeatsHeader spots a header line re-declared partway down the data.
//
// Every non-empty cell must equal the header cell in the same column, so a
// row that merely repeats one word from the header is still imported.
func rowRepeatsHeader(sh *Sheet, headerRow, n int) bool {
	if headerRow == n {
		return true
	}
	header := sh.Row(headerRow)
	matched := 0
	for i, cell := range sh.Row(n) {
		if cell.Empty() {
			continue
		}
		if i >= len(header) || header[i].Empty() {
			return false
		}
		if normalizeText(cell.Display()) != normalizeText(header[i].Display()) {
			return false
		}
		matched++
	}
	return matched >= 2
}

// reportUnmapped names the columns that hold data nobody asked for. This is
// how an operator discovers the column they forgot, which is otherwise
// invisible: the import succeeds and the data is simply not there.
func (c *converter) reportUnmapped(t *Table, tr *TableReport, sh *Sheet, first, last int) {
	mapped := t.mappedColumns()
	for col := 1; col <= sh.Width(); col++ {
		if _, ok := mapped[col]; ok {
			continue
		}
		used := false
		for n := first; n <= last; n++ {
			if !sh.Cell(n, col).Empty() {
				used = true
				break
			}
		}
		if !used {
			continue
		}
		label := ColumnLabel(col)
		if t.HeaderRow > 0 {
			if h := sh.Cell(t.HeaderRow, col).Display(); h != "" {
				label += fmt.Sprintf(" (%q)", strings.TrimSpace(h))
			}
		}
		tr.UnmappedColumns = append(tr.UnmappedColumns, label)
	}
}
