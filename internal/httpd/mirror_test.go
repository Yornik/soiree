package httpd

import (
	"io/fs"
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/Yornik/soiree/internal/store"
	"github.com/Yornik/soiree/web"
)

/*
 * Two things in this repository are written out by hand twice, in two
 * languages, and kept in step by nothing but a comment asking for it.
 *
 * The first is the currency exponent table: internal/store/money.go and again
 * MINOR_UNIT_EXPONENT in web/src/app.js, where the comment above it calls a
 * disagreement a factor of a hundred. The second is which languages the
 * interface is written in: the list here in language.go, LANGS in app.js,
 * LANGUAGE_NAMES in auth.js, and a block of strings per language in each of
 * those two files.
 *
 * Every copy is in step today. The tests below are what makes the next edit
 * to one side fail rather than ship: they read the frontend sources out of
 * the embedded FS and compare. No database and no browser, so they run in the
 * `go test -short` loop, which is the whole value of them. Drift between a Go
 * file and the JavaScript file beside it should be a red line while the
 * second half of the edit is still in somebody's head.
 *
 * The parsing is deliberately narrow: one named literal, one line at a time,
 * no attempt at JavaScript. A rename or a reindent leaves the regexps with
 * nothing to match, so each reader below fails the test when it finds nothing
 * rather than quietly comparing two empty tables and passing.
 */

// The table is the couple of dozen currencies that are not two-decimal, and
// each language block is well over a hundred strings. Anything far short of
// that is a parse that went wrong rather than a table that shrank.
const (
	fewestCurrencies = 20
	fewestStrings    = 100
)

var (
	// `IDR: 0,` and `BIF: 0, CLP: 0,` alike: the table puts several on a line.
	exponentEntry = regexp.MustCompile(`([A-Z]{3}): (\d)`)
	langHeading   = regexp.MustCompile(`^    ([a-z]{2}): \{$`)
	stringEntry   = regexp.MustCompile(`^      '([^']+)': (.*)$`)
	// Wider than the substitution app.js does, which is a single character.
	// A sentence with `{name}` in it is one that will print its braces, and
	// comparing it against the other languages is how that gets noticed.
	placeholderPattern = regexp.MustCompile(`\{\w+\}`)
	langTag            = regexp.MustCompile(`'([a-z]{2})'`)
	langKey            = regexp.MustCompile(`\b([a-z]{2}):`)
)

// 'c.item' is left out of the Indonesian table on purpose: it is the word an
// Indonesian spreadsheet uses for that column, so the budget header is meant
// to arrive through the English fallback. It is also the only exercise the
// per-key fallback has, and e2e/tests/language.spec.js asserts that path
// produces the word and never the key.
var untranslatedOnPurpose = map[string]map[string]bool{
	"id": {"c.item": true},
}

func TestTheCurrencyExponentsAreTheSameInThePageAsInTheStore(t *testing.T) {
	// What a code the Go table does not list gets back: defaultExponent in
	// money.go. It is how this test tells "listed as two-decimal" apart from
	// "not listed at all", since Exponent answers for every code there is.
	const unlisted = 2

	page := pageExponents(t)
	for _, code := range slices.Sorted(maps.Keys(page)) {
		if got := store.Exponent(code); got != page[code] {
			t.Errorf("web/src/app.js gives %s %d decimal place(s), internal/store/money.go gives %d",
				code, page[code], got)
		}
	}

	// The other direction is the one that costs money: a currency added to
	// the Go table alone, which the page then reads two decimals into.
	// minorUnitExponent is unexported, and this test would rather ask the
	// function than read the file the function lives in, so it asks about
	// every three-letter code there is. That is 17576 map lookups.
	for a := byte('A'); a <= 'Z'; a++ {
		for b := byte('A'); b <= 'Z'; b++ {
			for c := byte('A'); c <= 'Z'; c++ {
				code := string([]byte{a, b, c})
				exp := store.Exponent(code)
				if exp == unlisted {
					continue
				}
				if _, ok := page[code]; !ok {
					t.Errorf("internal/store/money.go gives %s %d decimal place(s) and web/src/app.js does not list it, so the page would use %d",
						code, exp, unlisted)
				}
			}
		}
	}
}

func TestEveryPlannerStringIsWrittenInEveryLanguage(t *testing.T) {
	// auth.js has this same test of its own in
	// e2e/tests/auth-ui.spec.js; app.js, the planner itself, had none.
	table := stringsTable(t, "app.js")
	english := table["en"]
	if len(english) < fewestStrings {
		t.Fatalf("web/src/app.js: the English block parsed as %d strings, which is not the table", len(english))
	}

	for _, lang := range slices.Sorted(maps.Keys(table)) {
		if lang == "en" {
			continue
		}
		for _, key := range slices.Sorted(maps.Keys(english)) {
			blanks, written := table[lang][key]
			switch {
			case !written && untranslatedOnPurpose[lang][key]:
				// Left to the English fallback deliberately; see above.
			case !written:
				t.Errorf("web/src/app.js: %s has no %q, so that screen ships half in English", lang, key)
			case untranslatedOnPurpose[lang][key]:
				t.Errorf("web/src/app.js: %s now has %q, so take it out of untranslatedOnPurpose", lang, key)
			case blanks != english[key]:
				t.Errorf("web/src/app.js: %s %q fills %q where English fills %q, so the braces are shown to somebody",
					lang, key, blanks, english[key])
			}
		}
		// A key English does not have is one nothing ever reads, because the
		// lookup starts from the chosen language and falls back to English:
		// usually half of a rename.
		for _, key := range slices.Sorted(maps.Keys(table[lang])) {
			if _, ok := english[key]; !ok {
				t.Errorf("web/src/app.js: %s has %q, which English does not, so nothing draws it", lang, key)
			}
		}
	}
}

func TestTheLanguageListIsTheSameEverywhereItIsWritten(t *testing.T) {
	want := strings.Join(slices.Sorted(slices.Values(languages)), " ")

	for _, copied := range []struct {
		where string
		langs []string
	}{
		{"web/src/app.js LANGS", pageLanguages(t)},
		{"web/src/auth.js LANGUAGE_NAMES", accountLanguageNames(t)},
		{"web/src/app.js STRINGS", slices.Sorted(maps.Keys(stringsTable(t, "app.js")))},
		{"web/src/auth.js STRINGS", slices.Sorted(maps.Keys(stringsTable(t, "auth.js")))},
	} {
		if got := strings.Join(copied.langs, " "); got != want {
			t.Errorf("%s has [%s]; internal/httpd/language.go has [%s]", copied.where, got, want)
		}
	}
}

// pageExponents reads MINOR_UNIT_EXPONENT out of web/src/app.js.
func pageExponents(t *testing.T) map[string]int {
	t.Helper()

	table := map[string]int{}
	for _, line := range jsLiteral(t, "app.js", "var MINOR_UNIT_EXPONENT = {") {
		// The entries carry trailing comments naming the departure from ISO
		// 4217, and a comment is prose rather than table.
		if at := strings.Index(line, "//"); at >= 0 {
			line = line[:at]
		}
		for _, entry := range exponentEntry.FindAllStringSubmatch(line, -1) {
			table[entry[1]] = int(entry[2][0] - '0')
		}
	}
	if len(table) < fewestCurrencies {
		t.Fatalf("web/src/app.js: MINOR_UNIT_EXPONENT parsed as %d entries, which is not the table", len(table))
	}
	return table
}

// stringsTable reads a `var STRINGS = {` literal and returns, per language
// and key, the blanks that string leaves to fill. The strings themselves are
// nobody's business here: what travels between two translations of one
// sentence is the placeholders in it.
func stringsTable(t *testing.T, file string) map[string]map[string]string {
	t.Helper()

	table := map[string]map[string]string{}
	lang := ""
	for _, line := range jsLiteral(t, file, "var STRINGS = {") {
		if heading := langHeading.FindStringSubmatch(line); heading != nil {
			lang = heading[1]
			table[lang] = map[string]string{}
			continue
		}
		entry := stringEntry.FindStringSubmatch(line)
		if entry == nil || lang == "" {
			continue
		}
		blanks := placeholderPattern.FindAllString(entry[2], -1)
		slices.Sort(blanks)
		table[lang][entry[1]] = strings.Join(slices.Compact(blanks), " ")
	}
	if len(table) == 0 {
		t.Fatalf("%s: no language blocks in the STRINGS table", file)
	}
	return table
}

// pageLanguages reads `var LANGS = ['en', ...]` out of web/src/app.js.
func pageLanguages(t *testing.T) []string {
	t.Helper()

	for _, line := range readFrontend(t, "app.js") {
		if !strings.Contains(line, "var LANGS = [") {
			continue
		}
		var langs []string
		for _, tag := range langTag.FindAllStringSubmatch(line, -1) {
			langs = append(langs, tag[1])
		}
		slices.Sort(langs)
		return langs
	}
	t.Fatal("web/src/app.js holds no LANGS list")
	return nil
}

// accountLanguageNames reads the keys of `var LANGUAGE_NAMES = {` out of
// web/src/auth.js. The names themselves are each in their own language and so
// not in any string table.
func accountLanguageNames(t *testing.T) []string {
	t.Helper()

	for _, line := range readFrontend(t, "auth.js") {
		if !strings.Contains(line, "var LANGUAGE_NAMES = {") {
			continue
		}
		var langs []string
		for _, key := range langKey.FindAllStringSubmatch(line, -1) {
			langs = append(langs, key[1])
		}
		slices.Sort(langs)
		return langs
	}
	t.Fatal("web/src/auth.js holds no LANGUAGE_NAMES table")
	return nil
}

// jsLiteral returns the lines of the object literal that opening declares, from
// the line after it to the line that closes it at the top level of the file.
func jsLiteral(t *testing.T, file, opening string) []string {
	t.Helper()

	lines := readFrontend(t, file)
	for n, line := range lines {
		if !strings.Contains(line, opening) {
			continue
		}
		for end := n + 1; end < len(lines); end++ {
			if lines[end] == "  };" {
				return lines[n+1 : end]
			}
		}
		t.Fatalf("%s: %s is never closed", file, opening)
	}
	t.Fatalf("%s holds no %s", file, opening)
	return nil
}

// readFrontend returns one of the embedded sources, split into lines. It
// reads the embedded copy rather than the working tree so the test is about
// what this binary serves.
func readFrontend(t *testing.T, name string) []string {
	t.Helper()

	src, err := fs.ReadFile(web.FS(), name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return strings.Split(string(src), "\n")
}
