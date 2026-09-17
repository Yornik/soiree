package sheetimport

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// SheetNameCSV is the single sheet a CSV file presents, so mappings can name
// a sheet uniformly whichever format they were written for.
const SheetNameCSV = "csv"

// ReadCSV reads delimiter-separated text.
//
// comma of 0 asks for sniffing; the delimiter actually used is returned so
// the caller can put it in the report. Sniffing is allowed here (and column
// mapping is not) because the delimiter is a property of the file that can be
// checked by eye in the report, whereas a guessed column mapping silently
// files money under the wrong heading.
func ReadCSV(r io.Reader, comma rune) (*Book, rune, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, 0, fmt.Errorf("read csv: %w", err)
	}
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF}) // Excel writes a BOM

	if comma == 0 {
		comma = sniffDelimiter(raw)
	}

	cr := csv.NewReader(bytes.NewReader(raw))
	cr.Comma = comma
	// Hand-made sheets have ragged rows and stray quotes in free text. Both
	// are ordinary here, so neither may abort the read.
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true

	sheet := Sheet{Name: SheetNameCSV}
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, comma, fmt.Errorf("read csv: %w", err)
		}
		row := make([]Cell, len(rec))
		for i, f := range rec {
			row[i] = Cell{Text: strings.TrimSpace(f)}
		}
		sheet.Rows = append(sheet.Rows, row)
	}
	return &Book{Sheets: []Sheet{sheet}}, comma, nil
}

// sniffDelimiter picks the separator that appears most consistently in the
// first few lines. Semicolon files are the norm wherever the comma is the
// decimal point, so defaulting blindly to a comma would mangle exactly the
// locales this importer has to survive.
func sniffDelimiter(raw []byte) rune {
	const sample = 10
	counts := map[rune]int{',': 0, ';': 0, '\t': 0, '|': 0}
	lines := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		for d := range counts {
			counts[d] += strings.Count(line, string(d))
		}
		if lines++; lines >= sample {
			break
		}
	}
	best, bestN := ',', 0
	for _, d := range []rune{',', ';', '\t', '|'} { // stable order, comma wins ties
		if counts[d] > bestN {
			best, bestN = d, counts[d]
		}
	}
	return best
}

// OpenFile reads a spreadsheet chosen by extension. The note describes how
// the file was read and belongs at the top of the report — "which delimiter
// did it decide on" is the first question when a CSV imports as one column.
func OpenFile(path string, comma rune) (book *Book, note string, err error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".ods":
		b, rerr := ReadODS(path)
		if rerr != nil {
			return nil, "", rerr
		}
		return b, fmt.Sprintf("OpenDocument spreadsheet, %d sheet(s): %s",
			len(b.Sheets), strings.Join(b.SheetNames(), ", ")), nil

	case ".csv", ".tsv", ".txt":
		f, oerr := os.Open(path)
		if oerr != nil {
			return nil, "", oerr
		}
		// Named returns, and no := on err below, so the close error cannot be
		// shadowed away by a block-scoped variable.
		defer func() {
			err = errors.Join(err, f.Close())
		}()
		sniffed := comma == 0
		b, used, rerr := ReadCSV(f, comma)
		if rerr != nil {
			return nil, "", rerr
		}
		how := "delimiter " + strconv.QuoteRune(used)
		if sniffed {
			how += " (sniffed — override with -delimiter)"
		}
		return b, "delimited text, " + how, nil
	}
	return nil, "", fmt.Errorf("%s: unsupported file type (want .ods, .csv or .tsv)", path)
}
