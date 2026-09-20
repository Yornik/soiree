// Command soiree-import converts a planning spreadsheet into the JSON the
// planner's "Import data" button reads.
//
// It takes an explicit column mapping and never invents one. -detect proposes
// a mapping and prints it for the operator to correct; nothing is imported on
// that run. The report on stderr accounts for every row that was scanned,
// because the failure mode that matters here is not a crash — it is an
// importer that quietly leaves three rows of a budget behind.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/Yornik/soiree/internal/sheetimport"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		errf("soiree-import: %v\n", err)
		os.Exit(1)
	}
}

type options struct {
	mapping    string
	detect     bool
	listSheets bool
	dryRun     bool
	out        string
	delimiter  string

	sheet         string
	rows          string
	header        int
	kind          string
	columns       columnFlag
	decimal       string
	currency      string
	totalKeywords string
	skipCostless  bool
	skipHidden    bool
	ceiling       float64
}

func run(args []string) error {
	var o options
	fs := flag.NewFlagSet("soiree-import", flag.ContinueOnError)
	fs.StringVar(&o.mapping, "mapping", "", "JSON mapping file describing the tables and their columns")
	fs.BoolVar(&o.detect, "detect", false, "propose a mapping on stdout and import nothing")
	fs.BoolVar(&o.listSheets, "list-sheets", false, "list the sheets in the file and stop")
	fs.BoolVar(&o.dryRun, "dry-run", false, "print the report but write no JSON")
	fs.StringVar(&o.out, "o", "-", "write JSON here (\"-\" for stdout)")
	fs.StringVar(&o.delimiter, "delimiter", "", "CSV delimiter: , ; | or tab (default: sniffed)")

	fs.StringVar(&o.sheet, "sheet", "", "single-table mode: sheet name or 1-based index")
	fs.StringVar(&o.rows, "rows", "", "single-table mode: data rows, e.g. 3-32 or 34- (1-based, inclusive)")
	fs.IntVar(&o.header, "header", 0, "single-table mode: 1-based header row")
	fs.StringVar(&o.kind, "kind", "budget", "single-table mode: budget, tasks or notes")
	fs.Var(&o.columns, "map", "single-table mode: field=column, repeatable or comma-separated (e.g. item=B,total=E)")
	fs.StringVar(&o.decimal, "decimal", "auto", "decimal separator: auto, dot or comma; with dot or comma a figure written the other way round is refused, not reread")
	fs.StringVar(&o.currency, "currency", "", "the planner's SOIREE_CURRENCY; decides the decimals a stated total is checked at (default: 2 decimals)")
	fs.StringVar(&o.totalKeywords, "total-keywords", "", "comma-separated words marking a summary row (default: total, subtotal, grand total, sum)")
	fs.BoolVar(&o.skipCostless, "skip-costless", false, "drop rows with no figure instead of importing them at zero")
	fs.BoolVar(&o.skipHidden, "skip-hidden", false, "leave out the rows the spreadsheet hides, by hand or by a filter (default: import them and name them in the report)")
	fs.Float64Var(&o.ceiling, "ceiling", 0, "budget ceiling to record in the output")
	fs.Usage = func() { usage(fs) }

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return errors.New("give exactly one spreadsheet to read")
	}
	path := fs.Arg(0)

	comma, err := parseDelimiter(o.delimiter)
	if err != nil {
		return err
	}
	book, reading, err := sheetimport.OpenFile(path, comma)
	if err != nil {
		return err
	}

	if o.listSheets {
		for i, name := range book.SheetNames() {
			errf("%d: %q (%d rows, %d columns)\n", i+1, name, len(book.Sheets[i].Rows), book.Sheets[i].Width())
		}
		return nil
	}

	if o.detect {
		return propose(book, path, reading, o)
	}

	cfg, err := buildConfig(o)
	if err != nil {
		return err
	}

	state, report, err := sheetimport.Convert(book, cfg)
	if err != nil {
		return err
	}
	report.Source, report.Reading, report.DryRun = path, reading, o.dryRun
	if werr := report.Write(os.Stderr); werr != nil {
		return werr
	}
	if o.dryRun {
		return nil
	}
	return writeState(state, o.out)
}

// propose runs detection. It prints the mapping and stops: applying a guessed
// mapping unasked is the one thing this tool must never do.
func propose(book *sheetimport.Book, path, reading string, o options) error {
	cfg, notes := sheetimport.Detect(book)
	cfg.Decimal = o.decimal
	cfg.Currency = o.currency
	cfg.Ceiling = o.ceiling

	errf("soiree-import: proposed mapping for %s (%s)\n", path, reading)
	for _, n := range notes {
		errf("  %s\n", n)
	}
	if err := cfg.Validate(); err != nil {
		errf("  the proposal is not yet valid: %v\n", err)
	}
	errf("  nothing imported — review this, then run again with -mapping\n")
	return cfg.WriteJSON(os.Stdout)
}

// buildConfig takes the mapping from a file or from the single-table flags,
// and refuses to proceed without one.
func buildConfig(o options) (cfg *sheetimport.Config, err error) {
	flagsUsed := o.sheet != "" || o.rows != "" || o.header != 0 || len(o.columns) > 0

	if o.mapping != "" {
		if flagsUsed {
			return nil, errors.New("-mapping already says where everything is; drop the -sheet/-rows/-header/-map flags")
		}
		f, oerr := os.Open(o.mapping)
		if oerr != nil {
			return nil, oerr
		}
		defer func() {
			err = errors.Join(err, f.Close())
		}()
		cfg, err = sheetimport.LoadConfig(f)
		if err != nil {
			return nil, err
		}
		if o.decimal != "auto" {
			cfg.Decimal = o.decimal
		}
		if o.currency != "" {
			cfg.Currency = o.currency
		}
		if o.ceiling != 0 {
			cfg.Ceiling = o.ceiling
		}
		return cfg, nil
	}

	if len(o.columns) == 0 {
		return nil, errors.New("no column mapping: pass -mapping FILE, or -map field=COLUMN for a single table, or -detect to have one proposed")
	}

	first, last, err := parseRows(o.rows)
	if err != nil {
		return nil, err
	}
	table := sheetimport.Table{
		Name:                  "cli",
		Sheet:                 o.sheet,
		Kind:                  o.kind,
		HeaderRow:             o.header,
		FirstRow:              first,
		LastRow:               last,
		Columns:               map[string]string(o.columns),
		SkipRowsWithoutAmount: o.skipCostless,
		SkipHidden:            o.skipHidden,
	}
	cfg = &sheetimport.Config{
		Decimal:  o.decimal,
		Currency: o.currency,
		Ceiling:  o.ceiling,
		Tables:   []sheetimport.Table{table},
	}
	if o.totalKeywords != "" {
		cfg.TotalKeywords = splitList(o.totalKeywords)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// writeState encodes the plan before it opens anything, for the reason the
// report gives for building its text in memory first: a file the encoder
// never agreed to fill is worse than no file, and -o names the previous
// import.
func writeState(state *sheetimport.State, out string) (err error) {
	var buf bytes.Buffer
	if err := state.WriteJSON(&buf); err != nil {
		return err
	}
	if out == "-" {
		_, err := os.Stdout.Write(buf.Bytes())
		return err
	}
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, f.Close())
	}()
	_, err = f.Write(buf.Bytes())
	return err
}

// columnFlag collects repeated -map flags.
type columnFlag map[string]string

func (c *columnFlag) String() string {
	if c == nil || len(*c) == 0 {
		return ""
	}
	b, err := json.Marshal(map[string]string(*c))
	if err != nil {
		return ""
	}
	return string(b)
}

func (c *columnFlag) Set(v string) error {
	if *c == nil {
		*c = columnFlag{}
	}
	for _, pair := range splitList(v) {
		field, ref, ok := strings.Cut(pair, "=")
		if !ok {
			return fmt.Errorf("-map %q: want field=COLUMN", pair)
		}
		field, ref = strings.TrimSpace(field), strings.TrimSpace(ref)
		if field == "" || ref == "" {
			return fmt.Errorf("-map %q: want field=COLUMN", pair)
		}
		if prev, dup := (*c)[field]; dup {
			return fmt.Errorf("-map %s given twice (%s and %s)", field, prev, ref)
		}
		(*c)[field] = ref
	}
	return nil
}

// parseRows reads "3-32", "34-" or "12".
func parseRows(spec string) (first, last int, err error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return 0, 0, nil
	}
	lo, hi, ranged := strings.Cut(spec, "-")
	first, err = atoiOrZero(lo)
	if err != nil {
		return 0, 0, fmt.Errorf("-rows %q: %w", spec, err)
	}
	if !ranged {
		return first, first, nil
	}
	last, err = atoiOrZero(hi)
	if err != nil {
		return 0, 0, fmt.Errorf("-rows %q: %w", spec, err)
	}
	return first, last, nil
}

func atoiOrZero(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%q is not a 1-based row number", s)
	}
	return n, nil
}

func parseDelimiter(s string) (rune, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return 0, nil // sniff
	case "tab", "\\t", "\t":
		return '\t', nil
	}
	r := []rune(s)
	if len(r) != 1 {
		return 0, fmt.Errorf("-delimiter %q: give a single character, or \"tab\"", s)
	}
	return r[0], nil
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// errf writes a diagnostic. Nothing useful can be done about a failed write
// to stderr, and the report is worthless if half of it is a returned error.
func errf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format, args...)
}

func usage(fs *flag.FlagSet) {
	errf(`soiree-import — turn a planning spreadsheet into planner JSON

usage:
  soiree-import [flags] FILE.ods|FILE.csv

The mapping is explicit. Either name the columns on the command line for a
single table, or write a mapping file for a sheet holding several.

  # see what is in the file
  soiree-import -list-sheets plan.ods

  # have a mapping proposed, then correct it by hand
  soiree-import -detect plan.ods > mapping.json

  # one table, mapped on the command line
  soiree-import -sheet Costs -header 2 -rows 3-32 \
      -map item=A,vendor=C,total=E,paid=F -dry-run plan.ods

  # the real thing
  soiree-import -mapping mapping.json -o plan.json plan.ods

mapping file:
  {
    "decimal": "auto",                  // auto | dot | comma
    "currency": "EUR",                  // the planner's SOIREE_CURRENCY
    "totalKeywords": ["total"],         // rows whose label matches are summaries
    "tables": [
      {
        "name": "run of show",
        "sheet": "Plan",                 // name or 1-based index
        "kind": "tasks",                 // budget (default) | tasks | notes
        "headerRow": 2,
        "firstRow": 3, "lastRow": 32,
        "columns": { "name": "A", "owner": "C", "due": "D", "status": "E" }
      },
      {
        "name": "costs",
        "sheet": "Plan",
        "kind": "budget",
        "headerRow": 34,
        "firstRow": 35, "lastRow": 53,
        "columns": { "item": "A", "qty": "B", "total": "C", "paid": "D" },
        "skipRowsWithoutAmount": false
      },
      {
        "name": "catering quote",
        "sheet": "Quote",
        "headerRow": 1, "firstRow": 2,
        "parentItem": "Catering",        // rows become children of that item
        "columns": { "item": "A", "qty": "B", "unit": "C" }
      }
    ]
  }

fields — budget: item, vendor, unit, qty, total, paid, note, lockBy, phase
         tasks:  name, owner, due, status
         notes:  text

Map unit or total, never both. A row whose label starts with "-" becomes a
child of the row above it and its amount is rolled into the parent, because
the planner's budget is a flat list and counting both would double it.

With total and qty mapped, the unit price is total / qty, and the planner
keeps a unit price in whole cents: 2500 over 300 comes back as 2499.00. Such
a line is imported as it stands and raises a warning. How many decimals a
"cent" has follows the currency, so say which one the planner runs with
(-currency, or "currency" in the mapping) unless it keeps two: IDR and JPY
keep none.

Summary keywords match a row's label exactly, ignoring case and trailing
punctuation — "TOTAL" and "total :" match "total", "TOTAL COSTS" does not. So
list every spelling the sheet actually uses (the defaults are English: total,
subtotal, grand total, sum). Matching is deliberately not by substring: a line
item called "Total makeup package" must not be deleted. A summary row the
keywords miss still raises a warning when its figure equals the sum of the rows
above it: the rows of its own section for a subtotal, every line in the table
for the total at the bottom. It is imported all the same, so act on the warning.
The check needs two rows to add up: a subtotal under a single line looks like
a repeated price, is not flagged, and takes the totals below it along. List
such a label as a keyword.

flags:
`)
	fs.PrintDefaults()
}
