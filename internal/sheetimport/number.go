package sheetimport

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// DecimalMode decides how a figure carrying a single separator is read.
//
// "1,234" is 1234 in en-GB and 1.234 in de-DE, and nothing in the cell says
// which one the author meant. DecimalAuto applies the convention that money
// sheets follow (see ParseNumber) and flags every guess so the report can own
// up to it; the other two modes let the operator settle it once for the file.
type DecimalMode int

const (
	DecimalAuto DecimalMode = iota
	DecimalDot
	DecimalComma
)

// ParseDecimalMode maps the CLI/mapping spelling onto a mode.
func ParseDecimalMode(s string) (DecimalMode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "auto":
		return DecimalAuto, nil
	case "dot", ".":
		return DecimalDot, nil
	case "comma", ",":
		return DecimalComma, nil
	}
	return DecimalAuto, fmt.Errorf("unknown decimal mode %q (want auto, dot or comma)", s)
}

func (m DecimalMode) String() string {
	switch m {
	case DecimalDot:
		return "dot"
	case DecimalComma:
		return "comma"
	default:
		return "auto"
	}
}

// ErrNoValue means the cell was deliberately empty — blank, or one of the
// dashes people type to mean "nothing here". It is not a parse failure and
// must not be reported as one, or every empty cell in a sparse sheet turns
// into a warning and the report becomes unreadable.
var ErrNoValue = errors.New("no value")

// Number is a parsed figure plus whether reading it required an assumption.
type Number struct {
	Value float64
	// AssumedGrouping is set when a lone separator followed by exactly three
	// digits was read as a thousands separator. Individually harmless,
	// collectively worth disclosing.
	AssumedGrouping bool
}

const (
	// A figure may sit next to a couple of short words — a currency code
	// ("IDR"), a unit ("pcs"), a qualifier ("per head"). Anything longer is
	// prose that happens to contain a digit, and prose in a numeric column is
	// exactly the case this importer must refuse rather than silently turn
	// into a number: a deadline remark that leaked into the price column
	// would otherwise import as the day of the month.
	maxUnitWords   = 2
	maxUnitWordLen = 4
)

// ParseNumber reads a human-typed figure.
//
// It tolerates grouping separators (",", "." and spaces), currency prefixes,
// a typographic minus, accounting parentheses for negatives, and the trailing
// ",-" some locales write for "and no cents". It refuses anything with a word
// long enough to be prose, more than one figure ("2 x 500", a date in any
// spelling), a separator that groups by anything but threes, a sign or a mark
// it cannot place ("250-", "15%", "<100"), or a magnitude suffix such as "5k" —
// refusing is recoverable because the caller reports it, whereas guessing is
// not.
func ParseNumber(raw string, mode DecimalMode) (Number, error) {
	s := strings.TrimSpace(raw)
	if s == "" || isDashLike(s) {
		return Number{}, ErrNoValue
	}

	negative := false
	if strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
		// Accounting notation: (1,200) is -1200.
		negative = true
		s = strings.TrimSuffix(strings.TrimPrefix(s, "("), ")")
	}

	var groups []string
	words := 0
	closed := false
	for _, f := range strings.Fields(s) {
		if !containsDigit(f) {
			words++
			if words > maxUnitWords || len([]rune(f)) > maxUnitWordLen {
				return Number{}, fmt.Errorf("%q reads as text, not a figure", raw)
			}
			if !isUnitWord(f) {
				// A "-" or a "%" standing on its own. Skipping it as if it were
				// a currency code is how "- 250" lost its sign.
				return Number{}, fmt.Errorf("%q is not a plain figure", raw)
			}
			closed = len(groups) > 0
			continue
		}
		if closed {
			// Space-separated thousands sit next to each other. Digits on both
			// sides of a word are two figures, and joining them made "2 x 500"
			// two and a half thousand.
			return Number{}, fmt.Errorf("%q does not hold exactly one figure", raw)
		}
		groups = append(groups, f)
	}
	core, ok := joinGroups(groups)
	if !ok {
		return Number{}, fmt.Errorf("%q does not hold exactly one figure", raw)
	}
	if core == "" {
		return Number{}, fmt.Errorf("%q holds no digits", raw)
	}

	// Leading currency marks go ("Rp5.000", "€1,200"), and with them the full
	// stop of an abbreviation ("Rp.5.000"), which is not a separator. No other
	// mark does: "<100" and "~100" say the figure is not the figure, and
	// dropping the mark is a guess nobody gets to see. Trailing letters do NOT
	// go either: stripping them would read "5k" as 5, off by three orders of
	// magnitude and completely invisible in the output.
	if bare := strings.TrimLeftFunc(core, isCurrencyMark); bare != core {
		// Only letters abbreviate. After a symbol the full stop is a decimal
		// point, and dropping it made "$.50" fifty.
		last, _ := utf8.DecodeLastRuneInString(core[:len(core)-len(bare)])
		if rest, stop := strings.CutPrefix(bare, "."); stop && unicode.IsLetter(last) {
			// One or two digits and nothing else read as decimals just as well:
			// "USD.50" is fifty, or fifty cents. Three digits or more are not
			// the decimals of any amount of money, and "Rp.5000" is the case
			// this exists for.
			if len(rest) < 3 && strings.TrimFunc(rest, unicode.IsDigit) == "" {
				return Number{}, fmt.Errorf("%q could be %s or 0.%s", raw, rest, rest)
			}
			bare = rest
		}
		core = bare
	}
	// ",-" and ".-" mean "and no cents". A minus at the end of anything else
	// is a sign written the way a ledger does, and a percent sign is not an
	// amount at all; both stay, so that the figure is refused below. Trimming
	// them turned a refund into a cost.
	if t := strings.TrimRight(core, "-"); strings.HasSuffix(t, ",") || strings.HasSuffix(t, ".") {
		core = t
	}
	core = strings.TrimRight(core, ".,")

	if core == "" {
		return Number{}, ErrNoValue
	}

	// The minus a spreadsheet displays and a PDF pastes is U+2212, not the
	// hyphen on the keyboard. It is a minus all the same.
	if r, size := utf8.DecodeRuneInString(core); isDashLike(string(r)) {
		core = "-" + core[size:]
	}

	sign := 1.0
	switch core[0] {
	case '-':
		sign, core = -1, core[1:]
	case '+':
		core = core[1:]
	}
	if core == "" {
		return Number{}, ErrNoValue
	}
	for _, r := range core {
		if !unicode.IsDigit(r) && r != '.' && r != ',' {
			return Number{}, fmt.Errorf("%q is not a plain figure", raw)
		}
	}

	digits, assumed, err := stripSeparators(core, mode)
	if err != nil {
		return Number{}, fmt.Errorf("%q: %w", raw, err)
	}
	v, err := strconv.ParseFloat(digits, 64)
	if err != nil {
		return Number{}, fmt.Errorf("%q is not a number", raw)
	}
	if negative {
		// "(-250)" says negative twice. It does not say positive.
		sign = -1
	}
	return Number{Value: sign * v, AssumedGrouping: assumed}, nil
}

// stripSeparators resolves which separator is the decimal point and removes
// the rest, returning a plain machine-readable numeral.
func stripSeparators(core string, mode DecimalMode) (string, bool, error) {
	lastDot := strings.LastIndexByte(core, '.')
	lastComma := strings.LastIndexByte(core, ',')

	switch {
	case lastDot >= 0 && lastComma >= 0:
		// Both present: whichever comes last is the decimal point, because no
		// convention groups after the decimal point.
		if lastDot > lastComma {
			return grouped(core[:lastDot], ',', replaceSeparators(core, '.'))
		}
		return grouped(core[:lastComma], '.', replaceSeparators(core, ','))

	case lastDot >= 0 || lastComma >= 0:
		sep := byte('.')
		if lastComma >= 0 {
			sep = ','
		}
		if strings.Count(core, string(sep)) > 1 {
			// Repeated: grouping, whatever the locale.
			return grouped(core, sep, replaceSeparators(core, 0))
		}
		switch mode {
		case DecimalDot:
			if sep == '.' {
				return replaceSeparators(core, '.'), false, nil
			}
			return grouped(core, sep, replaceSeparators(core, 0))
		case DecimalComma:
			if sep == ',' {
				return replaceSeparators(core, ','), false, nil
			}
			return grouped(core, sep, replaceSeparators(core, 0))
		}
		// Auto: three trailing digits is grouping. In a planning sheet
		// "1,500" is a figure of fifteen hundred far more often than it is
		// one and a half, but it is a coin flip and the caller discloses it.
		idx := strings.LastIndexByte(core, sep)
		if len(core)-idx-1 == 3 {
			return replaceSeparators(core, 0), true, nil
		}
		return replaceSeparators(core, sep), false, nil
	}
	return core, false, nil
}

// grouped returns numeral only if sep groups whole (the digits left of any
// decimal point) the way a thousands separator does: one to three digits,
// then exactly three after every separator. Every branch that takes a
// separator for grouping without the report disclosing it goes through here.
// "12.03.2026" is dots all the way through and a number in no other respect;
// read as grouping, the date a deposit was paid went into the budget as
// twelve million.
func grouped(whole string, sep byte, numeral string) (string, bool, error) {
	for i, g := range strings.Split(whole, string(sep)) {
		ok := len(g) == 3 || (i == 0 && (len(g) == 1 || len(g) == 2))
		for _, r := range g {
			ok = ok && unicode.IsDigit(r)
		}
		if !ok {
			return "", false, errors.New("its separators do not group in threes (a date, a version, or a decimal point written the other way round)")
		}
	}
	return numeral, false, nil
}

// replaceSeparators drops every separator except decimalSep, which becomes a
// '.'. Pass 0 to drop all of them.
func replaceSeparators(core string, decimalSep byte) string {
	var b strings.Builder
	b.Grow(len(core))
	for i := 0; i < len(core); i++ {
		c := core[i]
		switch c {
		case '.', ',':
			if decimalSep != 0 && c == decimalSep && strings.LastIndexByte(core, decimalSep) == i {
				b.WriteByte('.')
			}
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// joinGroups handles the space as a thousands separator ("22 500 000"),
// which several locales use. It only joins when the run really looks like
// grouping — every group after the first exactly three digits long — so that
// "1 Mar 2026" is refused rather than read as a very large number.
func joinGroups(groups []string) (string, bool) {
	switch len(groups) {
	case 0:
		return "", true
	case 1:
		return groups[0], true
	}
	for i, g := range groups[1:] {
		digits := g
		if i == len(groups)-2 {
			// The final group may carry the decimal part: "22 500 000,50".
			if cut := strings.IndexAny(g, ".,"); cut >= 0 {
				digits = g[:cut]
			}
		}
		if len(digits) != 3 {
			return "", false
		}
		for _, r := range digits {
			if !unicode.IsDigit(r) {
				return "", false
			}
		}
	}
	return strings.Join(groups, ""), true
}

// isCurrencyMark is what may stand in front of a figure: the letters of a
// code ("Rp", "IDR") or a currency symbol.
func isCurrencyMark(r rune) bool {
	return unicode.IsLetter(r) || unicode.Is(unicode.Sc, r)
}

// isUnitWord reports a field that can sit beside a figure without changing
// it: a currency code or symbol, a unit, an abbreviation with its full stops
// ("Rp.", "p.p."). A lone "-", "+" or "%" is none of those.
func isUnitWord(f string) bool {
	marks := 0
	for _, r := range f {
		switch {
		case isCurrencyMark(r):
			marks++
		case r != '.':
			return false
		}
	}
	return marks > 0
}

func containsDigit(s string) bool {
	for _, r := range s {
		if unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

// isDashLike reports the dashes people type to mean "nothing". Treating them
// as parse failures would drown the report in noise.
func isDashLike(s string) bool {
	for _, r := range s {
		switch r {
		case '-', '‐', '‑', '‒', '–', '—', '−':
		default:
			return false
		}
	}
	return s != ""
}
