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
	"unicode/utf8"
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
	if err := checkUTF8(raw); err != nil {
		return nil, 0, err
	}

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
		breaks := 0
		for i, f := range rec {
			row[i] = Cell{Text: strings.TrimSpace(f)}
			breaks += strings.Count(row[i].Text, "\n")
		}
		sheet.Rows = append(sheet.Rows, row)
		if breaks > 0 && len(rec) > 0 {
			// LazyQuotes means an unclosed quote reads the lines under it as
			// part of this field instead of as rows of their own, so the row
			// numbers from here on no longer match the file's lines. The
			// caller has to be told which row it was.
			at, _ := cr.FieldPos(0)
			sheet.markJoined(len(sheet.Rows), LineSpan{First: at, Last: at + breaks})
		}
	}
	return &Book{Sheets: []Sheet{sheet}}, comma, nil
}

// checkUTF8 refuses text that this reader would otherwise mangle in silence.
//
// Excel's plain "CSV (comma delimited)" is Windows-1252 and its "Unicode
// text" is UTF-16. Read as UTF-8, the first turns every accented letter into
// a replacement character and the second puts a NUL between every letter,
// which the planner's own API then refuses on sync. Decoding on a guess is
// the kind of guess this package does not make, and it is not needed: the
// spreadsheet can be exported again, while the mangled text cannot be
// restored.
func checkUTF8(raw []byte) error {
	const remedy = `re-save it as "CSV UTF-8" or as .ods and run again`
	switch {
	case bytes.HasPrefix(raw, []byte{0xFF, 0xFE}), bytes.HasPrefix(raw, []byte{0xFE, 0xFF}):
		return fmt.Errorf(`read csv: the file is UTF-16 (Excel's "Unicode text"): %s`, remedy)
	case bytes.IndexByte(raw, 0) >= 0:
		return fmt.Errorf("read csv: line %d holds a NUL byte, which nothing writes into a spreadsheet on purpose (UTF-16 without a byte order mark?): %s",
			lineOf(raw, bytes.IndexByte(raw, 0)), remedy)
	}
	if i := firstInvalidUTF8(raw); i >= 0 {
		return fmt.Errorf(`read csv: line %d is not UTF-8 (byte %#02x; Excel's plain "CSV" is Windows-1252): %s`,
			lineOf(raw, i), raw[i], remedy)
	}
	return nil
}

// firstInvalidUTF8 is the offset of the first byte that begins no valid
// encoding, or -1. A U+FFFD the file itself spells out is valid UTF-8 and is
// left alone: the damage it records happened before the file reached us.
func firstInvalidUTF8(raw []byte) int {
	for i := 0; i < len(raw); {
		r, size := utf8.DecodeRune(raw[i:])
		if r == utf8.RuneError && size <= 1 {
			return i
		}
		i += size
	}
	return -1
}

// lineOf is the 1-based line the byte at offset i sits on.
func lineOf(raw []byte, i int) int {
	return 1 + bytes.Count(raw[:i], []byte{'\n'})
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
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
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
	// Most people's planning sheet is an .xlsx, so this message is the first
	// thing the tool ever says to them. "Unsupported" on its own leaves them
	// with nothing to do about it, and File > Save As is all it takes.
	switch ext {
	case ".xlsx", ".xlsm", ".xlsb", ".xls", ".fods", ".numbers":
		return nil, "", fmt.Errorf("%s: %s is not read here: save it as OpenDocument (.ods) or as CSV, and run again", path, ext)
	}
	return nil, "", fmt.Errorf("%s: unsupported file type (want .ods, .csv, .tsv or .txt)", path)
}
