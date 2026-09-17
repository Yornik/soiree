package sheetimport

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Display is what the cell shows, falling back to its stored number for the
// formats that keep a value without any text.
func (c Cell) Display() string {
	if c.Text != "" {
		return c.Text
	}
	if c.HasValue {
		return strconv.FormatFloat(c.Value, 'f', -1, 64)
	}
	return ""
}

// normalizeText folds a label to a comparable form: lower case, single
// spaces, no trailing punctuation. Used for matching only — never for output,
// because the sheet's own spelling is what the planner recognises.
func normalizeText(s string) string {
	s = strings.ToLower(strings.Join(strings.Fields(s), " "))
	return strings.TrimRight(s, " :.-–—")
}

// words splits on anything that is not a letter or digit, which is what makes
// keyword matching work across languages and punctuation styles.
func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// containsWords reports whether needle appears in haystack as a whole word or
// phrase. Substring matching would flag "Total makeup package" as a summary
// row and quietly delete a real line item.
func containsWords(haystack, needle string) bool {
	h, n := words(haystack), words(needle)
	if len(n) == 0 || len(h) < len(n) {
		return false
	}
	for i := 0; i+len(n) <= len(h); i++ {
		hit := true
		for j := range n {
			if h[i+j] != n[j] {
				hit = false
				break
			}
		}
		if hit {
			return true
		}
	}
	return false
}

// matchesSummaryKeyword reports a row label that is a summary line.
//
// The comparison is exact after normalisation — not a substring, not even a
// word search. "Total makeup package" is a line item, and dropping it would
// take money out of the budget with only a line in the report to show for
// it. A summary row the keywords miss is still caught by the
// sum-of-the-rows-above check, and that one only warns. Between a silent
// deletion and a noisy keep, keep.
func matchesSummaryKeyword(label, keyword string) bool {
	return normalizeText(label) == normalizeText(keyword)
}

// splitChildPrefix strips a leading dash-like mark, reporting whether the row
// is written as part of the row above it.
func splitChildPrefix(s string, prefixes []string) (string, bool) {
	trimmed := strings.TrimLeft(s, " \t")
	for _, p := range prefixes {
		if p == "" || !strings.HasPrefix(trimmed, p) {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(trimmed, p))
		if rest == "" {
			// A row of nothing but dashes is a separator, not a child.
			return s, false
		}
		return rest, true
	}
	return s, false
}

// appendNote joins note fragments without inventing punctuation when one side
// is empty.
func appendNote(note, extra string) string {
	note, extra = strings.TrimSpace(note), strings.TrimSpace(extra)
	switch {
	case extra == "":
		return note
	case note == "":
		return extra
	}
	return note + " — " + extra
}

// formatNumber prints a figure the way the report should quote it back.
func formatNumber(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// maxBreakdownNote caps how much of a child breakdown is written into the
// parent's note. A caterer's quote can be thirty dishes long, and the note is
// a grid cell.
const maxBreakdownNote = 6

// breakdownNote summarises the children inside the parent's note, so the
// rolled-up figure is explainable in the UI even though the UI has no notion
// of child rows yet.
func breakdownNote(children []BudgetItem) string {
	parts := make([]string, 0, maxBreakdownNote+1)
	for i, ch := range children {
		if i == maxBreakdownNote {
			parts = append(parts, fmt.Sprintf("+%d more", len(children)-maxBreakdownNote))
			break
		}
		parts = append(parts, fmt.Sprintf("%s %s", ch.Item, formatNumber(ch.Total())))
	}
	return "includes " + strings.Join(parts, "; ")
}

// statusWords maps what people write into the three states the UI allows.
// Unrecognised words are reported rather than mapped by similarity: guessing
// that "cancelled" means "done" would be a lie in the wrong direction.
var statusWords = map[string]string{
	"done": "done", "complete": "done", "completed": "done", "finished": "done",
	"closed": "done", "paid": "done", "yes": "done", "ok": "done", "y": "done",

	"in progress": "in-progress", "in-progress": "in-progress", "inprogress": "in-progress",
	"ongoing": "in-progress", "started": "in-progress", "doing": "in-progress",
	"wip": "in-progress", "partial": "in-progress",

	"not started": "not-started", "not-started": "not-started", "notstarted": "not-started",
	"todo": "not-started", "to do": "not-started", "open": "not-started",
	"pending": "not-started", "new": "not-started", "no": "not-started", "n": "not-started",
}

// normalizeStatus maps a written status onto the UI's three values. ok is
// false when the word is unknown, so the caller can report it.
func normalizeStatus(raw string) (status string, ok bool) {
	s := normalizeText(raw)
	if s == "" {
		return "not-started", true
	}
	if v, hit := statusWords[s]; hit {
		return v, true
	}
	return "not-started", false
}

// dateLayouts covers the unambiguous spellings. Everything else goes through
// the numeric path below.
var dateLayouts = []string{
	"2006-01-02T15:04:05",
	"2006-01-02",
	"2 January 2006",
	"2 Jan 2006",
	"January 2, 2006",
	"Jan 2, 2006",
}

// parseDate returns an ISO date, which is the only form the UI's date input
// accepts.
//
// dayFirst reports that the value was genuinely ambiguous (both parts ≤ 12)
// and was read day-first. 03/04/2027 is the fourth of March in one country
// and the third of April in another; there is nothing in the file to settle
// it, so the choice is made once, applied everywhere, and counted in the
// report.
func parseDate(raw string) (iso string, dayFirst bool, err error) {
	s := strings.TrimSpace(raw)
	if s == "" || isDashLike(s) {
		return "", false, ErrNoValue
	}
	for _, layout := range dateLayouts {
		if t, perr := time.Parse(layout, s); perr == nil {
			return t.Format("2006-01-02"), false, nil
		}
	}

	parts := strings.FieldsFunc(s, func(r rune) bool { return r == '/' || r == '-' || r == '.' || r == ' ' })
	if len(parts) != 3 {
		return "", false, fmt.Errorf("%q is not a date", raw)
	}
	n := make([]int, 3)
	for i, p := range parts {
		v, cerr := strconv.Atoi(p)
		if cerr != nil {
			return "", false, fmt.Errorf("%q is not a date", raw)
		}
		n[i] = v
	}

	var y, m, d int
	switch {
	case len(parts[0]) == 4:
		y, m, d = n[0], n[1], n[2]
	case n[1] > 12 && n[0] <= 12:
		// Only one reading survives: month first.
		y, m, d = n[2], n[0], n[1]
	default:
		y, m, d = n[2], n[1], n[0]
		dayFirst = n[0] <= 12 && n[1] <= 12
	}
	if y < 100 {
		y += 2000
	}
	if m < 1 || m > 12 || d < 1 || d > 31 {
		return "", false, fmt.Errorf("%q is not a date", raw)
	}
	t := time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
	if t.Day() != d || int(t.Month()) != m {
		return "", false, fmt.Errorf("%q is not a real date", raw)
	}
	return t.Format("2006-01-02"), dayFirst, nil
}
