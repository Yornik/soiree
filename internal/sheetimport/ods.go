package sheetimport

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const (
	// An ODS file encodes the empty tail of every sheet as repeat runs: one
	// cell with table:number-columns-repeated="16384" and one row repeated a
	// million times. Materialising those literally turns a 30 kB file into
	// gigabytes of cells, so empty runs are counted and dropped rather than
	// built. These caps only bite on repeat runs that actually carry content.
	maxSheetCols = 4096
	maxSheetRows = 200000
)

// ReadODS reads an OpenDocument spreadsheet.
//
// It is stdlib zip + XML rather than a spreadsheet library on purpose: the
// part of ODS that matters here is one file inside a zip, and the only real
// trap is the repeat encoding above, which a library would not save us from
// understanding anyway.
func ReadODS(path string) (book *Book, err error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() {
		err = errors.Join(err, zr.Close())
	}()

	for _, f := range zr.File {
		if f.Name != "content.xml" {
			continue
		}
		rc, oerr := f.Open()
		if oerr != nil {
			return nil, fmt.Errorf("%s: open content.xml: %w", path, oerr)
		}
		sheets, perr := parseODSContent(rc)
		cerr := rc.Close()
		if perr != nil {
			return nil, fmt.Errorf("%s: %w", path, perr)
		}
		if cerr != nil {
			return nil, fmt.Errorf("%s: close content.xml: %w", path, cerr)
		}
		return &Book{Sheets: sheets}, nil
	}
	return nil, fmt.Errorf("%s: no content.xml inside — is this really an .ods file?", path)
}

// parseODSContent walks the OpenDocument table XML.
//
// Elements are matched on local name only. The same document is written by
// LibreOffice, Excel and half a dozen exporters, which disagree about
// namespace URIs far more often than they disagree about element names.
func parseODSContent(r io.Reader) ([]Sheet, error) {
	dec := xml.NewDecoder(r)

	var (
		sheets      []Sheet
		cur         *Sheet
		row         []Cell
		cell        Cell
		text        strings.Builder
		dateValue   string
		inRow       bool
		inCell      bool
		rowHidden   bool
		cellRepeat  int
		rowRepeat   int
		pendingCols int // empty cells counted but not materialised
		pendingRows int
	)

	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("content.xml: %w", err)
		}

		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "table":
				if cur != nil {
					break
				}
				cur = &Sheet{Name: localAttr(t, "name")}
				pendingRows = 0

			case "table-row":
				if cur == nil {
					break
				}
				inRow, row, pendingCols = true, nil, 0
				rowRepeat = repeatAttr(t, "number-rows-repeated")
				// "collapse" is hidden by hand, "filter" is hidden by an
				// AutoFilter. Either way the row is in the file and not on
				// the person's screen.
				visibility := localAttr(t, "visibility")
				rowHidden = visibility == "collapse" || visibility == "filter"

			case "table-cell", "covered-table-cell":
				// covered-table-cell is the hidden half of a merge. It still
				// occupies a column, and skipping it shifts every column to
				// its right by one — the classic misaligned-import bug.
				if !inRow {
					break
				}
				inCell = true
				cell = Cell{}
				dateValue = ""
				text.Reset()
				cellRepeat = repeatAttr(t, "number-columns-repeated")
				readCellValue(t, &cell, &dateValue)

			case "annotation":
				// Cell comments are text:p inside the cell. Left alone they
				// would be concatenated into the cell's own text.
				if err := dec.Skip(); err != nil {
					return nil, fmt.Errorf("content.xml: skip annotation: %w", err)
				}

			case "s":
				if inCell {
					text.WriteString(strings.Repeat(" ", repeatAttr(t, "c")))
				}
			case "p", "line-break", "tab":
				if inCell && text.Len() > 0 {
					text.WriteByte(' ')
				}
			}

		case xml.CharData:
			if inCell {
				text.Write(t)
			}

		case xml.EndElement:
			switch t.Name.Local {
			case "table-cell", "covered-table-cell":
				if !inCell {
					break
				}
				inCell = false
				finishCell(&cell, text.String(), dateValue)
				if cell.Empty() {
					pendingCols += cellRepeat
					break
				}
				if len(row)+pendingCols+cellRepeat > maxSheetCols {
					return nil, fmt.Errorf("sheet %q has a row wider than %d columns", sheetName(cur), maxSheetCols)
				}
				row = append(row, make([]Cell, pendingCols)...)
				pendingCols = 0
				for i := 0; i < cellRepeat; i++ {
					row = append(row, cell)
				}

			case "table-row":
				if !inRow {
					break
				}
				inRow = false
				if len(row) == 0 {
					pendingRows += rowRepeat
					break
				}
				if len(cur.Rows)+pendingRows+rowRepeat > maxSheetRows {
					return nil, fmt.Errorf("sheet %q has more than %d rows", sheetName(cur), maxSheetRows)
				}
				cur.Rows = append(cur.Rows, make([][]Cell, pendingRows)...)
				pendingRows = 0
				for i := 0; i < rowRepeat; i++ {
					if rowHidden {
						cur.markHidden(len(cur.Rows) + 1)
					}
					cur.Rows = append(cur.Rows, append([]Cell(nil), row...))
				}

			case "table":
				if cur == nil {
					break
				}
				// Trailing empty rows are the file's padding, not data.
				sheets = append(sheets, *cur)
				cur = nil
			}
		}
	}
	return sheets, nil
}

// readCellValue pulls the machine-readable value off the cell element.
//
// office:value is always written dot-decimal and locale-independent, so it
// bypasses the tolerant text parser entirely — running a value the file
// already states exactly through a separator heuristic can only make it
// worse.
func readCellValue(t xml.StartElement, cell *Cell, dateValue *string) {
	switch localAttr(t, "value-type") {
	case "float", "currency", "percentage":
		if v, err := strconv.ParseFloat(localAttr(t, "value"), 64); err == nil {
			cell.Value, cell.HasValue = v, true
		}
	case "date":
		// The displayed text is locale-formatted and ambiguous; the stored
		// value is ISO. Prefer the one that cannot be read two ways.
		*dateValue = localAttr(t, "date-value")
	case "boolean":
		*dateValue = localAttr(t, "boolean-value")
	case "string":
		// Some writers put the text in an attribute as well as in text:p.
		*dateValue = localAttr(t, "string-value")
	}
}

func finishCell(cell *Cell, raw, fallback string) {
	cell.Text = strings.Join(strings.Fields(raw), " ")
	if cell.Text == "" {
		cell.Text = strings.TrimSpace(fallback)
	} else if fallback != "" && isISODate(fallback) {
		cell.Text = fallback
	}
}

func isISODate(s string) bool {
	return len(s) >= 10 && s[4] == '-' && s[7] == '-'
}

func sheetName(s *Sheet) string {
	if s == nil {
		return ""
	}
	return s.Name
}

// localAttr finds an attribute by local name, ignoring its namespace.
func localAttr(t xml.StartElement, name string) string {
	for _, a := range t.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

// repeatAttr reads a repeat count, defaulting to one.
func repeatAttr(t xml.StartElement, name string) int {
	v := localAttr(t, name)
	if v == "" {
		return 1
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 1
	}
	return n
}
