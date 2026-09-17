package store

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Money is stored as a whole number of minor units — cents, sen, yen — which
// is the only representation that survives arithmetic intact. What a minor
// unit is worth depends on the currency, and that exponent comes from the
// currency code rather than from a per-deployment setting: getting it wrong is
// a factor of a hundred, and nobody notices a factor of a hundred in a number
// they have not added up by hand.

// defaultExponent applies to any code not in the table below, which is the
// overwhelming majority of the world's currencies.
const defaultExponent = 2

// minorUnitExponent lists the currencies that are not two-decimal.
//
// Seeded from ISO 4217, with one deliberate departure: IDR. The standard
// assigns it two decimals for the sen, a unit withdrawn from circulation
// decades ago — no Indonesian price, invoice or spreadsheet carries one, and
// treating rupiah as two-decimal would show every figure a hundred times too
// small. The project's own source data is whole rupiah, so whole rupiah is
// what this table says.
var minorUnitExponent = map[string]int{
	"IDR": 0, // see above — not the ISO 4217 value

	// Zero-decimal per ISO 4217.
	"BIF": 0, "CLP": 0, "DJF": 0, "GNF": 0, "ISK": 0, "JPY": 0,
	"KMF": 0, "KRW": 0, "PYG": 0, "RWF": 0, "UGX": 0, "UYI": 0,
	"VND": 0, "VUV": 0, "XAF": 0, "XOF": 0, "XPF": 0,

	// Three-decimal per ISO 4217.
	"BHD": 3, "IQD": 3, "JOD": 3, "KWD": 3, "LYD": 3, "OMR": 3, "TND": 3,

	// Four-decimal per ISO 4217.
	"CLF": 4,
}

// Exponent returns how many minor units make up one major unit of currency,
// as a power of ten. An unknown code gets the two-decimal default, because a
// wrong guess of two is right far more often than a refusal is useful.
func Exponent(currency string) int {
	if exp, ok := minorUnitExponent[strings.ToUpper(strings.TrimSpace(currency))]; ok {
		return exp
	}
	return defaultExponent
}

// ToMinor converts a major-unit amount to the minor units stored in the
// database, rounding half away from zero.
//
// The float is the lossy step, not the conversion: 1.005 is already slightly
// below 1.005 by the time it arrives. Where the input is text — an import, a
// form field, a JSON body — use ParseMajor, which never sees a float at all.
func ToMinor(currency string, major float64) int64 {
	if math.IsNaN(major) || math.IsInf(major, 0) {
		return 0
	}
	return int64(math.Round(major * math.Pow10(Exponent(currency))))
}

// ToMajor converts stored minor units back to major units. Exact for any
// amount a planner will ever type; use FormatMajor when the result is going to
// be displayed or compared.
func ToMajor(currency string, minor int64) float64 {
	return float64(minor) / math.Pow10(Exponent(currency))
}

// FormatMajor renders minor units as a plain decimal string with exactly the
// currency's number of decimal places, using integer arithmetic throughout.
// It is not localised: this is the machine-readable form, and the browser owns
// how a number looks to a person.
func FormatMajor(currency string, minor int64) string {
	exp := Exponent(currency)
	if exp == 0 {
		return strconv.FormatInt(minor, 10)
	}

	// Via uint64, because negating math.MinInt64 overflows back to itself.
	mag := uint64(minor)
	sign := ""
	if minor < 0 {
		mag = -mag
		sign = "-"
	}

	div := pow10(exp)
	return fmt.Sprintf("%s%d.%0*d", sign, mag/div, exp, mag%div)
}

// ParseMajor reads a plain decimal string into minor units, exactly. More
// decimal places than the currency has is an error rather than a silent
// round: a figure typed with cents against a currency that has none means the
// column was misidentified, and that is worth a complaint at import time.
func ParseMajor(currency, s string) (int64, error) {
	exp := Exponent(currency)

	str := strings.TrimSpace(s)
	if str == "" {
		return 0, fmt.Errorf("parse %q as %s: empty", s, currency)
	}

	neg := false
	switch str[0] {
	case '-':
		neg, str = true, str[1:]
	case '+':
		str = str[1:]
	}

	whole, frac, hasFrac := strings.Cut(str, ".")
	if whole == "" || (hasFrac && frac == "") {
		return 0, fmt.Errorf("parse %q as %s: not a decimal number", s, currency)
	}
	if len(frac) > exp {
		return 0, fmt.Errorf("parse %q as %s: %s has %d decimal place(s), got %d", s, currency, currency, exp, len(frac))
	}
	if !digitsOnly(whole) || !digitsOnly(frac) {
		return 0, fmt.Errorf("parse %q as %s: not a decimal number", s, currency)
	}

	// Shifting the decimal point by padding is what keeps this exact: the
	// digits go straight into an integer without a float in between.
	digits := whole + frac + strings.Repeat("0", exp-len(frac))
	if neg {
		digits = "-" + digits
	}
	minor, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %q as %s: %w", s, currency, err)
	}
	return minor, nil
}

func digitsOnly(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func pow10(exp int) uint64 {
	p := uint64(1)
	for range exp {
		p *= 10
	}
	return p
}
