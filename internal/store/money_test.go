package store_test

import (
	"math"
	"testing"

	"github.com/Yornik/soiree/internal/store"
)

// These need no database: the conversion is the part that silently loses
// money, and it should be caught without Docker anywhere near it.

func TestExponent(t *testing.T) {
	cases := map[string]int{
		"EUR":   2,
		"SEK":   2,
		"eur":   2, // case is not the caller's problem
		" EUR ": 2,
		"JPY":   0,
		"IDR":   0, // deliberately not the ISO 4217 value; see money.go
		"KWD":   3,
		"ZZZ":   2, // unknown codes get the common case
	}
	for code, want := range cases {
		if got := store.Exponent(code); got != want {
			t.Errorf("Exponent(%q) = %d, want %d", code, got, want)
		}
	}
}

func TestMinorUnitsZeroDecimal(t *testing.T) {
	// A zero-decimal currency: minor units and major units are the same
	// number, and adding a decimal point to one would inflate it a hundredfold.
	const currency = "IDR"

	if got := store.ToMinor(currency, 750000); got != 750000 {
		t.Errorf("ToMinor(%s, 750000) = %d, want 750000", currency, got)
	}
	if got := store.ToMajor(currency, 750000); got != 750000 {
		t.Errorf("ToMajor(%s, 750000) = %v, want 750000", currency, got)
	}
	if got := store.FormatMajor(currency, 750000); got != "750000" {
		t.Errorf("FormatMajor(%s, 750000) = %q, want \"750000\"", currency, got)
	}

	got, err := store.ParseMajor(currency, "750000")
	if err != nil {
		t.Fatalf("ParseMajor: %v", err)
	}
	if got != 750000 {
		t.Errorf("ParseMajor(%s, \"750000\") = %d, want 750000", currency, got)
	}

	// Cents in a currency that has none means the column was misread, not
	// that the figure should be quietly rounded.
	if _, err := store.ParseMajor(currency, "750000.50"); err == nil {
		t.Error("ParseMajor accepted decimals for a zero-decimal currency")
	}
}

func TestMinorUnitsTwoDecimal(t *testing.T) {
	const currency = "EUR"

	if got := store.ToMinor(currency, 1234.56); got != 123456 {
		t.Errorf("ToMinor(%s, 1234.56) = %d, want 123456", currency, got)
	}
	if got := store.ToMajor(currency, 123456); math.Abs(got-1234.56) > 1e-9 {
		t.Errorf("ToMajor(%s, 123456) = %v, want 1234.56", currency, got)
	}
	if got := store.FormatMajor(currency, 123456); got != "1234.56" {
		t.Errorf("FormatMajor(%s, 123456) = %q, want \"1234.56\"", currency, got)
	}
	// The case the whole scheme exists for: a fraction of a major unit that a
	// whole-number column would drop.
	if got := store.FormatMajor(currency, 5); got != "0.05" {
		t.Errorf("FormatMajor(%s, 5) = %q, want \"0.05\"", currency, got)
	}
	if got := store.FormatMajor(currency, -750); got != "-7.50" {
		t.Errorf("FormatMajor(%s, -750) = %q, want \"-7.50\"", currency, got)
	}

	for in, want := range map[string]int64{
		"1234.56": 123456,
		"1234.5":  123450, // a short fraction is a scale, not an error
		"1234":    123400,
		"-7.50":   -750,
		"+0.05":   5,
		" 0.05 ":  5,
	} {
		got, err := store.ParseMajor(currency, in)
		if err != nil {
			t.Errorf("ParseMajor(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseMajor(%q) = %d, want %d", in, got, want)
		}
	}

	for _, in := range []string{"", ".", "1.234", "1.2.3", "twelve", "1 234,56", "1.", "-"} {
		if got, err := store.ParseMajor(currency, in); err == nil {
			t.Errorf("ParseMajor(%q) = %d, want an error", in, got)
		}
	}
}

// TestMinorUnitsRoundTrip is the property that matters: whatever goes into the
// database comes back out as the same amount of money.
func TestMinorUnitsRoundTrip(t *testing.T) {
	for _, currency := range []string{"EUR", "SEK", "IDR", "JPY", "KWD"} {
		for _, minor := range []int64{0, 1, 5, 99, 100, 250000, -4200, math.MaxInt64, math.MinInt64} {
			formatted := store.FormatMajor(currency, minor)
			got, err := store.ParseMajor(currency, formatted)
			if err != nil {
				t.Errorf("%s %d formatted as %q: %v", currency, minor, formatted, err)
				continue
			}
			if got != minor {
				t.Errorf("%s: %d -> %q -> %d", currency, minor, formatted, got)
			}
		}
	}
}
