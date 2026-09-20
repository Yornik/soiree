package sheetimport

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// The three shapes a table of rows can be imported as.
const (
	KindBudget = "budget"
	KindTasks  = "tasks"
	KindNotes  = "notes"
)

// fieldsByKind is the entire mapping vocabulary. A field not listed here is a
// typo, and a typo has to be an error: silently ignoring an unknown key is
// how a column of paid deposits disappears without anyone noticing.
var fieldsByKind = map[string][]string{
	KindBudget: {"item", "vendor", "unit", "qty", "total", "paid", "note", "lockBy", "phase"},
	KindTasks:  {"name", "owner", "due", "status"},
	KindNotes:  {"text"},
}

// Rows whose label matches one of these are computed, not data — a TOTAL line
// sitting inside the grid. The defaults are English because they have to be
// something; a sheet in any other language sets totalKeywords, and the
// sum-of-the-rows-above check catches what the keywords miss.
//
// Matching is exact after normalisation (see matchesSummaryKeyword), so each
// spelling the sheet uses has to be listed — including multi-word ones like
// "grand total", which fire on that label and no other.
var defaultTotalKeywords = []string{"total", "subtotal", "grand total", "sum"}

// A leading dash is how people write "part of the line above" in a cell.
var defaultChildPrefixes = []string{"-", "–", "—", "•", "*", "·"}

// Config is the mapping file: what lives where, in a file the operator writes
// once and keeps next to the spreadsheet.
type Config struct {
	// Decimal is auto, dot or comma. See DecimalMode.
	Decimal string `json:"decimal,omitempty"`
	// Currency is the planner's SOIREE_CURRENCY. Nothing is converted by it;
	// it says how many decimals the planner keeps, which decides whether a
	// stated total survives being stored as a unit price. Empty means two.
	Currency string `json:"currency,omitempty"`
	// TotalKeywords replaces the defaults for every table.
	TotalKeywords []string `json:"totalKeywords,omitempty"`
	// ChildPrefixes replaces the leading marks that make a row a child.
	ChildPrefixes []string `json:"childPrefixes,omitempty"`
	// Ceiling and InflationPct carry over the two settings that live outside
	// any table.
	Ceiling      float64 `json:"ceiling,omitempty"`
	InflationPct float64 `json:"inflationPct,omitempty"`
	Tables       []Table `json:"tables"`
}

// Table is one logical table. A sheet can hold several: real planning sheets
// stack a run-of-show table on top of a cost table with entirely different
// columns, and reading that as one table produces nonsense in both halves.
type Table struct {
	// Name labels the table in the report only.
	Name string `json:"name,omitempty"`
	// Sheet is a sheet name or a 1-based index; empty means the first sheet.
	Sheet string `json:"sheet,omitempty"`
	// Kind is budget (default), tasks or notes. A run-of-show table with no
	// money in it belongs in tasks, not as a budget of zeroes.
	Kind string `json:"kind,omitempty"`
	// HeaderRow is the 1-based row holding the column titles. Optional, but
	// without it repeated headers further down cannot be recognised.
	HeaderRow int `json:"headerRow,omitempty"`
	// FirstRow and LastRow bound the data, 1-based and inclusive. LastRow 0
	// runs to the end of the sheet.
	FirstRow int `json:"firstRow,omitempty"`
	LastRow  int `json:"lastRow,omitempty"`
	// Columns maps a field name to a column letter or 1-based number.
	Columns map[string]string `json:"columns"`
	// ParentItem attaches every row of this table under the budget item with
	// exactly this name — the per-dish quote on its own sheet that is one
	// line on the main table.
	ParentItem string `json:"parentItem,omitempty"`
	// TotalKeywords overrides the file-wide list for this table.
	TotalKeywords []string `json:"totalKeywords,omitempty"`
	// SkipRowsWithoutAmount drops rows with no figure in them instead of
	// importing them at zero.
	SkipRowsWithoutAmount bool `json:"skipRowsWithoutAmount,omitempty"`
	// SkipHidden leaves out the rows the spreadsheet does not show. Off by
	// default: a hidden row is still in the sheet's own SUM(), so dropping it
	// unasked is the worse of the two silences. Either way the report names
	// them.
	SkipHidden bool `json:"skipHidden,omitempty"`
	// DashChildren turns the leading-dash convention off when a sheet uses
	// dashes decoratively. Nil means on.
	DashChildren *bool `json:"dashChildren,omitempty"`
}

// LoadConfig reads a mapping file.
//
// Unknown keys are rejected rather than ignored: in a file whose entire job
// is to say where things are, a key that does nothing is a mistake the
// operator wants to hear about before the import, not after.
func LoadConfig(r io.Reader) (*Config, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("mapping: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// WriteJSON renders a mapping file, for --detect to propose one.
func (c *Config) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(c); err != nil {
		return fmt.Errorf("write mapping: %w", err)
	}
	return nil
}

// Validate checks the mapping on its own terms, before any file is opened, so
// a bad mapping fails immediately instead of halfway through a report.
func (c *Config) Validate() error {
	if _, err := ParseDecimalMode(c.Decimal); err != nil {
		return err
	}
	if cur := strings.TrimSpace(c.Currency); cur != "" && len(cur) != 3 {
		return fmt.Errorf("currency must be a 3-letter ISO 4217 code, got %q", c.Currency)
	}
	if len(c.Tables) == 0 {
		return fmt.Errorf("mapping defines no tables")
	}
	for i := range c.Tables {
		if err := c.Tables[i].validate(); err != nil {
			return fmt.Errorf("table %s: %w", c.Tables[i].label(i), err)
		}
	}
	return nil
}

func (t *Table) label(i int) string {
	if t.Name != "" {
		return `"` + t.Name + `"`
	}
	return fmt.Sprintf("#%d", i+1)
}

func (t *Table) kind() string {
	if t.Kind == "" {
		return KindBudget
	}
	return t.Kind
}

func (t *Table) dashChildren() bool {
	return t.DashChildren == nil || *t.DashChildren
}

func (t *Table) validate() error {
	fields, ok := fieldsByKind[t.kind()]
	if !ok {
		return fmt.Errorf("unknown kind %q (want budget, tasks or notes)", t.Kind)
	}
	if len(t.Columns) == 0 {
		return fmt.Errorf("no columns mapped (fields for kind %s: %s)", t.kind(), strings.Join(fields, ", "))
	}
	if t.FirstRow < 0 || t.LastRow < 0 || t.HeaderRow < 0 {
		return fmt.Errorf("row numbers are 1-based and cannot be negative")
	}
	if t.LastRow != 0 && t.FirstRow != 0 && t.LastRow < t.FirstRow {
		return fmt.Errorf("lastRow %d is before firstRow %d", t.LastRow, t.FirstRow)
	}
	if t.ParentItem != "" && t.kind() != KindBudget {
		return fmt.Errorf("parentItem only applies to a budget table")
	}

	seen := map[int]string{}
	for name, ref := range t.Columns {
		field, err := canonicalField(t.kind(), name)
		if err != nil {
			return err
		}
		col, err := ParseColumnRef(ref)
		if err != nil {
			return fmt.Errorf("field %s: %w", field, err)
		}
		if other, dup := seen[col]; dup {
			return fmt.Errorf("column %s is mapped to both %s and %s", ColumnLabel(col), other, field)
		}
		seen[col] = field
	}
	if t.kind() == KindBudget {
		if _, hasUnit := t.column("unit"); hasUnit {
			if _, hasTotal := t.column("total"); hasTotal {
				return fmt.Errorf("map unit or total, not both — the line total would be ambiguous")
			}
		}
	}
	return nil
}

// column resolves a field to a 1-based column index. It assumes validate has
// run, which is guaranteed by every path that reaches conversion.
func (t *Table) column(field string) (int, bool) {
	for name, ref := range t.Columns {
		if strings.EqualFold(name, field) {
			col, err := ParseColumnRef(ref)
			if err != nil {
				return 0, false
			}
			return col, true
		}
	}
	return 0, false
}

// mappedColumns lists every column index the table claims.
func (t *Table) mappedColumns() map[int]string {
	out := map[int]string{}
	for name, ref := range t.Columns {
		if col, err := ParseColumnRef(ref); err == nil {
			field, err := canonicalField(t.kind(), name)
			if err != nil {
				field = name
			}
			out[col] = field
		}
	}
	return out
}

func (t *Table) totalKeywords(c *Config) []string {
	switch {
	case len(t.TotalKeywords) > 0:
		return t.TotalKeywords
	case len(c.TotalKeywords) > 0:
		return c.TotalKeywords
	}
	return defaultTotalKeywords
}

func (c *Config) childPrefixes() []string {
	if len(c.ChildPrefixes) > 0 {
		return c.ChildPrefixes
	}
	return defaultChildPrefixes
}

// canonicalField maps a case-insensitive field name onto the spelling the
// converter uses, and rejects anything outside the vocabulary.
func canonicalField(kind, name string) (string, error) {
	for _, f := range fieldsByKind[kind] {
		if strings.EqualFold(f, strings.TrimSpace(name)) {
			return f, nil
		}
	}
	known := append([]string(nil), fieldsByKind[kind]...)
	sort.Strings(known)
	return "", fmt.Errorf("unknown field %q for kind %s (known: %s)", name, kind, strings.Join(known, ", "))
}
