package sheetimport

import (
	"math"
	"strconv"
	"strings"
)

// The planner keeps money as whole minor units, the unit price included, and
// how many decimals that is depends on the currency. The importer writes
// major-unit numbers and never rounds them itself, but it has to know where
// the planner will: a unit price derived from a stated total is the one
// figure here that the sheet did not state, and it is rarely a whole number
// of cents.

// defaultExponent applies to any code not in the table below.
const defaultExponent = 2

// minorUnitExponent mirrors internal/store/money.go, its one departure from
// ISO 4217 included: IDR is whole rupiah. It is a copy rather than an import
// because the store brings the database driver with it, and this package is
// what a spreadsheet converter links. TestExponentMirrorsTheStore compares
// the two over every three-letter code, so the copy cannot drift unnoticed.
var minorUnitExponent = map[string]int{
	"IDR": 0,

	"BIF": 0, "CLP": 0, "DJF": 0, "GNF": 0, "ISK": 0, "JPY": 0,
	"KMF": 0, "KRW": 0, "PYG": 0, "RWF": 0, "UGX": 0, "UYI": 0,
	"VND": 0, "VUV": 0, "XAF": 0, "XOF": 0, "XPF": 0,

	"BHD": 3, "IQD": 3, "JOD": 3, "KWD": 3, "LYD": 3, "OMR": 3, "TND": 3,

	"CLF": 4,
}

// exponent returns the decimals the planner keeps for a currency code. No
// code, like an unknown one, gets the two-decimal default.
func exponent(currency string) int {
	if exp, ok := minorUnitExponent[strings.ToUpper(strings.TrimSpace(currency))]; ok {
		return exp
	}
	return defaultExponent
}

// roundHalfUp is JavaScript's Math.round, which is what the page rounds with.
// math.Round goes away from zero and differs on a negative half.
func roundHalfUp(v float64) float64 { return math.Floor(v + 0.5) }

// plannerTotal is the line total the planner will show for a unit price and a
// quantity, in minor units. It follows web/src/app.js step for step: the unit
// is rounded to whole minor units first (toMinor), then multiplied and
// rounded again (lineTotalMinor).
func plannerTotal(unit, qty float64, exp int) float64 {
	return roundHalfUp(roundHalfUp(unit*math.Pow10(exp)) * qty)
}

// formatMinor prints minor units as a major-unit figure with the currency's
// own number of decimals.
func formatMinor(minor float64, exp int) string {
	return strconv.FormatFloat(minor/math.Pow10(exp), 'f', exp, 64)
}
