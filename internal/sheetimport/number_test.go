package sheetimport

import (
	"errors"
	"testing"
)

func TestParseNumber(t *testing.T) {
	cases := []struct {
		in      string
		mode    DecimalMode
		want    float64
		assumed bool
		err     error // ErrNoValue, or non-nil for "must fail"
	}{
		// Grouped figures, the ordinary case in a money sheet.
		{in: "12,345,678", want: 12345678},
		{in: "12.345.678", want: 12345678},
		{in: "12 345 678", want: 12345678},
		{in: "7,400", want: 7400, assumed: true},
		{in: "1,234.56", want: 1234.56},
		{in: "1.234,56", want: 1234.56},
		{in: "12.5", want: 12.5},

		// Currency decoration.
		{in: "Rp 12.345.678,-", want: 12345678},
		{in: "€1,200", want: 1200, assumed: true},
		{in: "1200 EUR", want: 1200},
		{in: "40 pcs", want: 40},

		// An abbreviation's full stop is not a separator: left in place it made
		// "Rp.5000" half a rupiah.
		{in: "Rp.5.000", want: 5000, assumed: true},
		{in: "Rp.5000", want: 5000},
		{in: "Rp. 5.000,-", want: 5000, assumed: true},
		{in: "5.000,--", want: 5000, assumed: true},
		// A symbol is not an abbreviation and has no full stop of its own: the
		// one after it is a decimal point. Dropped along with the symbol, fifty
		// cents a stamp came in as fifty a stamp.
		{in: "$.50", want: 0.5},
		{in: "€.25", want: 0.25},
		{in: "$.5", want: 0.5},
		{in: "R$.50", want: 0.5},
		{in: "$ .50", want: 0.5},
		{in: "$.50", mode: DecimalDot, want: 0.5},
		{in: "$.50", mode: DecimalComma, err: errors.New("x")},
		// After letters, one or two digits could be either: fifty, or fifty
		// cents. Both are wrong half the time, so neither is picked.
		{in: "USD.50", err: errors.New("x")},
		{in: "Rp.50", err: errors.New("x")},
		{in: "kr.5", err: errors.New("x")},
		// Three digits are five hundred on either reading, and ",-" says the
		// figure in front of it is whole.
		{in: "Rp.500", want: 500},
		{in: "Rp.50,-", want: 50},

		// Negatives. The typographic minus is what a spreadsheet displays and
		// what a PDF pastes; dropped, a discount becomes a cost.
		{in: "(1,200)", want: -1200, assumed: true},
		{in: "-750", want: -750},
		{in: "−250", want: -250},
		{in: "–250", want: -250},
		{in: "€ −250", want: -250},
		{in: "€−250", want: -250},
		{in: "(−250)", want: -250},
		{in: "(-250)", want: -250},

		// Nothing here.
		{in: "", err: ErrNoValue},
		{in: "   ", err: ErrNoValue},
		{in: "-", err: ErrNoValue},
		{in: "—", err: ErrNoValue},

		// Text that leaked into a numeric column must fail, not import as the
		// stray digit it happens to contain.
		{in: "before 1 March", err: errors.New("x")},
		{in: "1 Mar 2026", err: errors.New("x")},
		{in: "2026-03-01", err: errors.New("x")},
		{in: "5k", err: errors.New("x")},
		{in: "TBD", err: errors.New("x")},

		// A date typed with dots or commas is separators all the way through,
		// and a separator that repeats reads as grouping. Only groups of three
		// are grouping: the date a deposit was paid is not twelve million.
		{in: "12.03.2026", err: errors.New("x")},
		{in: "1.2.2026", err: errors.New("x")},
		{in: "12,03,2026", err: errors.New("x")},
		{in: "10.5.1", err: errors.New("x")},
		{in: "1.2.3", err: errors.New("x")},
		{in: "1.234.5", err: errors.New("x")},
		{in: "1..5", err: errors.New("x")},
		{in: "12.03.2026,5", err: errors.New("x")},

		// Two figures with a word between them are two figures, however much
		// the second one looks like a group of thousands.
		{in: "2 x 500", err: errors.New("x")},
		{in: "10 of 200", err: errors.New("x")},
		{in: "3 - 500", err: errors.New("x")},

		// A sign or a mark the parser cannot place is refused, never trimmed:
		// each of these used to import as the bare figure.
		{in: "250-", err: errors.New("x")},
		{in: "- 250", err: errors.New("x")},
		{in: "- 1 200", err: errors.New("x")},
		{in: "15%", err: errors.New("x")},
		{in: "15 %", err: errors.New("x")},
		{in: "<100", err: errors.New("x")},
		{in: "~100", err: errors.New("x")},
		{in: "≈100", err: errors.New("x")},

		// Forcing the convention settles the ambiguous case.
		{in: "1,500", mode: DecimalComma, want: 1.5},
		{in: "1,500", mode: DecimalDot, want: 1500},
		{in: "1.500", mode: DecimalDot, want: 1.5},
		// It does not make "1.5" fifteen: the other separator is grouping only
		// where it groups.
		{in: "1.5", mode: DecimalComma, err: errors.New("x")},
		{in: "12,5", mode: DecimalDot, err: errors.New("x")},
	}

	for _, c := range cases {
		got, err := ParseNumber(c.in, c.mode)
		switch {
		case errors.Is(c.err, ErrNoValue):
			if !errors.Is(err, ErrNoValue) {
				t.Errorf("ParseNumber(%q) = %v, %v; want ErrNoValue", c.in, got, err)
			}
		case c.err != nil:
			if err == nil {
				t.Errorf("ParseNumber(%q) = %v; want an error", c.in, got.Value)
			}
		default:
			if err != nil {
				t.Errorf("ParseNumber(%q): %v", c.in, err)
				continue
			}
			if got.Value != c.want {
				t.Errorf("ParseNumber(%q) = %v; want %v", c.in, got.Value, c.want)
			}
			if got.AssumedGrouping != c.assumed {
				t.Errorf("ParseNumber(%q) assumed grouping = %v; want %v", c.in, got.AssumedGrouping, c.assumed)
			}
		}
	}
}

func TestParseDate(t *testing.T) {
	cases := []struct {
		in        string
		order     DateOrder
		want      string
		ambiguous bool
		wantErr   bool
	}{
		{in: "2027-05-01", want: "2027-05-01"},
		{in: "2027-05-01T00:00:00", want: "2027-05-01"},
		{in: "03/04/2027", want: "2027-04-03", ambiguous: true},
		{in: "25/12/2027", want: "2027-12-25"},
		{in: "12/25/2027", want: "2027-12-25"}, // only one reading works
		{in: "1 Jan 2027", want: "2027-01-01"},
		{in: "31/02/2027", wantErr: true},
		{in: "next spring", wantErr: true},
		// An order settles the values that read either way, and only those:
		// 25/12 is the twenty-fifth of December in every order there is.
		{in: "03/04/2027", order: DateMonthFirst, want: "2027-03-04", ambiguous: true},
		{in: "25/12/2027", order: DateMonthFirst, want: "2027-12-25"},
		{in: "03/04/2027", order: DateDayFirst, want: "2027-04-03", ambiguous: true},
	}
	for _, c := range cases {
		got, ambiguous, err := parseDate(c.in, c.order)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseDate(%q) = %q; want an error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseDate(%q): %v", c.in, err)
			continue
		}
		if got != c.want || ambiguous != c.ambiguous {
			t.Errorf("parseDate(%q, %s) = %q, ambiguous %v; want %q, %v",
				c.in, c.order, got, ambiguous, c.want, c.ambiguous)
		}
	}
}

func TestDateEvidence(t *testing.T) {
	cases := []struct {
		in       string
		want     DateOrder
		decisive bool
	}{
		{in: "03/25/2027", want: DateMonthFirst, decisive: true},
		{in: "25/03/2027", want: DateDayFirst, decisive: true},
		{in: "03/04/2027"},   // reads either way
		{in: "2027-03-04"},   // already unambiguous
		{in: "next spring"},  // not a date at all
		{in: "25/25/2027"},   // no reading at all
		{in: "1 March 2027"}, // no numeric month to weigh
	}
	for _, c := range cases {
		got, decisive := dateEvidence(c.in)
		if decisive != c.decisive || (decisive && got != c.want) {
			t.Errorf("dateEvidence(%q) = %s, decisive %v; want %s, %v",
				c.in, got, decisive, c.want, c.decisive)
		}
	}
}

func TestContainsWords(t *testing.T) {
	// Used for header titles, where a phrase inside a longer title is exactly
	// what should match.
	if !containsWords("Estimated price (IDR)", "estimated price") {
		t.Error("phrase inside a header title should match")
	}
	if containsWords("Unitary", "unit") {
		t.Error("matching must be by word, not by substring")
	}
}

func TestMatchesSummaryKeyword(t *testing.T) {
	for _, label := range []string{"TOTAL", "Total", " total :"} {
		if !matchesSummaryKeyword(label, "total") {
			t.Errorf("%q should be recognised as a summary row", label)
		}
	}
	// A line item that happens to start with the keyword must survive: a row
	// dropped here is money missing from the budget.
	for _, label := range []string{"Total makeup package", "Totaal"} {
		if matchesSummaryKeyword(label, "total") {
			t.Errorf("%q is a line item, not a summary row", label)
		}
	}
}

func TestColumnRefs(t *testing.T) {
	for ref, want := range map[string]int{"A": 1, "b": 2, "Z": 26, "AA": 27, "3": 3} {
		got, err := ParseColumnRef(ref)
		if err != nil || got != want {
			t.Errorf("ParseColumnRef(%q) = %d, %v; want %d", ref, got, err, want)
		}
	}
	if _, err := ParseColumnRef("A1"); err == nil {
		t.Error("ParseColumnRef(\"A1\") should fail")
	}
	if got := ColumnLabel(27); got != "AA" {
		t.Errorf("ColumnLabel(27) = %q; want AA", got)
	}
}
