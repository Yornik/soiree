package reminders

import (
	"fmt"
	"html/template"
	"strings"

	"github.com/Yornik/soiree/internal/mailer"
	"github.com/Yornik/soiree/internal/push"
	"github.com/Yornik/soiree/internal/store"
)

// dayFormat is how every date in the digest is written: weekday, day, month,
// year. The weekday is there because "the 2nd" and "Tuesday" are different
// amounts of information to somebody deciding what to do this week, and the
// year because a planning horizon crosses one.
//
// Not a locale-aware format. The browser owns localised formatting and has a
// locale to do it with; a mail has one body for every reader, and an
// unambiguous English date beats a date formatted for the wrong person.
const dayFormat = "Mon 2 Jan 2006"

// Render builds the two bodies of the digest.
//
// Both say the same thing. The HTML is a table with inline styles and nothing
// else: no image, no stylesheet, no link, nothing that causes the reader's
// client to fetch anything from anywhere. A digest that reported who opened it
// would be surveillance of the people it is trying to help, and this project
// forbids third-party requests in the browser for the same reason.
func Render(d Digest) (mailer.Message, error) {
	v := build(d)

	html, err := renderHTML(v)
	if err != nil {
		return mailer.Message{}, err
	}

	return mailer.Message{
		Subject: v.Subject,
		Text:    renderText(v),
		HTML:    html,
	}, nil
}

// NotificationTag collapses the digest notifications on a device: a second one
// replaces the first rather than stacking under it.
//
// A phone that was switched off for a fortnight should show this week's digest
// and not last week's as well — the older one is strictly worse information,
// and two notifications saying nearly the same thing is how somebody learns to
// swipe both away without reading either.
const NotificationTag = "soiree-deadlines"

// RenderPush builds the notification form of the digest.
//
// Deliberately not the digest. A push payload is a few kilobytes, encrypted end
// to end, and shown in two lines on a lock screen; the readable version of "six
// things are due, here they are" does not exist at that size. So this says how
// much needs attention and how much of it is late — the same clause the mail
// puts in its subject — and the click opens the planner, where the detail
// already is and is already current.
//
// baseURL may be empty, in which case the click opens the service worker's own
// scope. That is the correct relative answer rather than a degraded one: the
// notification came from this origin, so this origin is what it opens.
func RenderPush(d Digest, baseURL string) push.Notification {
	return push.Notification{
		Title: heading(d),
		Body:  headline(d),
		URL:   openURL(baseURL),
		Tag:   NotificationTag,
	}
}

// openURL is the page a notification's click opens: the planner itself, since
// everything the digest alludes to is on it.
func openURL(baseURL string) string {
	if baseURL == "" {
		return "/"
	}
	return strings.TrimRight(baseURL, "/") + "/"
}

type rowView struct {
	Date    string
	Title   string
	Detail  string
	When    string
	Overdue bool
}

type sectionView struct {
	Title string
	Note  string
	Rows  []rowView
}

type digestView struct {
	Subject  string
	Heading  string
	Summary  string
	Sections []sectionView
	Footer   string
}

// build turns a digest into fully-formatted strings, so that the two
// templates lay out the same words rather than each deciding how a date or an
// amount is written.
func build(d Digest) digestView {
	v := digestView{
		Subject: subject(d),
		Heading: heading(d),
		Summary: summary(d),
		// The footer names both reasons an address receives the digest and
		// both of the things that stop it, rather than the one that applies,
		// because a single body reaches every active admin and everything
		// SOIREE_REMINDER_TO names, as well as somebody who is both, for whom
		// either wording alone would be false. It used to describe a list
		// that nothing has read since admins became recipients, and asked for
		// the address to be taken off it, which neither the reader nor the
		// person they asked could do.
		Footer: fmt.Sprintf(
			"Dates are the days recorded against each line, shown exactly as stored. "+
				"Today is %s in %s.\nYou receive this because you are an active admin "+
				"of %s, or because whoever runs it added your address. There is no "+
				"per-person off switch: the digest stops when that address is removed "+
				"from the reminder settings, or when the account is no longer an "+
				"active admin.",
			d.Today.Format(dayFormat), d.Zone, orPlaceholder(d.EventName, "this event")),
	}

	if len(d.Deadlines) > 0 {
		s := sectionView{
			Title: "Decisions to lock in",
			Note:  fmt.Sprintf("lock-by dates up to %s", d.Horizon.Format(dayFormat)),
		}
		for _, it := range d.Deadlines {
			s.Rows = append(s.Rows, rowView{
				Date:    it.Date.Format(dayFormat),
				Title:   orPlaceholder(it.Item, "(unnamed line)"),
				Detail:  join(it.Vendor, money(d.Currency, it.Amount)),
				When:    relative(it.Days),
				Overdue: it.Days < 0,
			})
		}
		v.Sections = append(v.Sections, s)
	}

	if len(d.Tasks) > 0 {
		s := sectionView{
			Title: "Tasks",
			Note:  fmt.Sprintf("due up to %s", d.Horizon.Format(dayFormat)),
		}
		for _, t := range d.Tasks {
			s.Rows = append(s.Rows, rowView{
				Date:    t.Date.Format(dayFormat),
				Title:   orPlaceholder(t.Name, "(unnamed task)"),
				Detail:  join(t.Owner, statusWord(t.Status)),
				When:    relative(t.Days),
				Overdue: t.Days < 0,
			})
		}
		v.Sections = append(v.Sections, s)
	}

	return v
}

// subject says the whole story, because on a phone the subject is often the
// only part that gets read.
func subject(d Digest) string {
	if d.EventName != "" {
		return d.EventName + ": " + headline(d)
	}
	return "Deadlines: " + headline(d)
}

// headline is the digest in one clause: what needs attention and how much of
// it is already late.
//
// Shared by the mail's subject line and the notification's body, so the two
// channels say the same words about the same week. They have the same reason
// to be short — a subject line and a lock screen both get about two lines of
// attention — and keeping one function means neither can drift into describing
// the digest differently from the other.
func headline(d Digest) string {
	overdue := d.Overdue()
	soon := d.Count() - overdue

	switch {
	case overdue > 0 && soon > 0:
		return fmt.Sprintf("%d overdue, %d coming up", overdue, soon)
	case overdue > 0:
		return fmt.Sprintf("%d overdue", overdue)
	default:
		return fmt.Sprintf("%s to decide in the next %s", count(soon, "item"), count(d.WindowDays, "day"))
	}
}

func heading(d Digest) string {
	if d.EventName != "" {
		return "Deadlines — " + d.EventName
	}
	return "Deadlines"
}

func summary(d Digest) string {
	overdue := d.Overdue()
	if overdue == 0 {
		return fmt.Sprintf("%s in the next %s.", count(d.Count(), "thing"), count(d.WindowDays, "day"))
	}
	return fmt.Sprintf("%s in the next %s, of which %d already past.",
		count(d.Count(), "thing"), count(d.WindowDays, "day"), overdue)
}

// renderText writes the plain-text body, which is the one that has to work
// everywhere: a terminal client, a screen reader, the two-line preview on a
// lock screen.
func renderText(v digestView) string {
	var b strings.Builder
	line := func(format string, args ...any) {
		// strings.Builder documents its write error as always nil.
		_, _ = fmt.Fprintf(&b, format+"\n", args...)
	}

	line("%s", v.Heading)
	line("%s", strings.Repeat("=", len([]rune(v.Heading))))
	line("")
	line("%s", v.Summary)

	for _, s := range v.Sections {
		line("")
		if s.Note != "" {
			line("%s (%s)", s.Title, s.Note)
		} else {
			line("%s", s.Title)
		}
		line("%s", strings.Repeat("-", 40))
		for _, r := range s.Rows {
			detail := ""
			if r.Detail != "" {
				detail = " — " + r.Detail
			}
			line("  %-16s %s%s", r.Date, r.Title, detail)
			line("  %-16s %s", "", r.When)
		}
	}

	line("")
	line("%s", v.Footer)
	return b.String()
}

// htmlTemplate is deliberately plain. Mail clients are not browsers: no
// stylesheet, no web font, no layout that needs one. Inline styles on a table,
// and it degrades to something readable in every client that ignores them.
var htmlTemplate = template.Must(template.New("digest").Parse(`<div style="font-family:Georgia,'Times New Roman',serif;font-size:16px;line-height:1.5;color:#1c1917;max-width:38em">
<h1 style="font-size:20px;margin:0 0 .4em">{{.Heading}}</h1>
<p style="margin:0 0 1.4em">{{.Summary}}</p>
{{range .Sections}}<h2 style="font-size:15px;text-transform:uppercase;letter-spacing:.06em;margin:1.6em 0 .2em">{{.Title}}</h2>
{{if .Note}}<p style="margin:0 0 .6em;font-size:13px;color:#57534e">{{.Note}}</p>
{{end}}<table style="border-collapse:collapse;width:100%">
{{range .Rows}}<tr>
<td style="padding:.45em .8em .45em 0;vertical-align:top;white-space:nowrap;border-top:1px solid #e7e5e4;font-variant-numeric:tabular-nums">{{.Date}}</td>
<td style="padding:.45em 0;vertical-align:top;border-top:1px solid #e7e5e4">{{.Title}}{{if .Detail}}<br><span style="font-size:13px;color:#57534e">{{.Detail}}</span>{{end}}</td>
<td style="padding:.45em 0 .45em .8em;vertical-align:top;text-align:right;white-space:nowrap;border-top:1px solid #e7e5e4;font-size:13px;{{if .Overdue}}color:#9f1239;font-weight:bold{{else}}color:#57534e{{end}}">{{.When}}</td>
</tr>
{{end}}</table>
{{end}}<p style="margin:2em 0 0;font-size:12px;color:#78716c;border-top:1px solid #e7e5e4;padding-top:.8em">{{.Footer}}</p>
</div>
`))

func renderHTML(v digestView) (string, error) {
	var b strings.Builder
	if err := htmlTemplate.Execute(&b, v); err != nil {
		return "", fmt.Errorf("render digest html: %w", err)
	}
	return b.String(), nil
}

// relative says how far off a date is in words, because "Fri 20 Feb" and "four
// days late" are answers to different questions and the second is the one that
// makes somebody act.
func relative(days int) string {
	switch {
	case days < -1:
		return fmt.Sprintf("overdue by %d days", -days)
	case days == -1:
		return "overdue since yesterday"
	case days == 0:
		return "today"
	case days == 1:
		return "tomorrow"
	default:
		return fmt.Sprintf("in %d days", days)
	}
}

// money writes an amount as the machine-readable decimal plus its currency
// code. No symbol and no thousands separator: the code is unambiguous where a
// bare $ or a European decimal comma is not, and this mail has no locale.
func money(currency string, minor int64) string {
	if minor == 0 {
		return ""
	}
	return currency + " " + store.FormatMajor(currency, minor)
}

func statusWord(s store.TaskStatus) string {
	if s == store.TaskInProgress {
		return "in progress"
	}
	return ""
}

func join(parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, " · ")
}

func orPlaceholder(s, placeholder string) string {
	if strings.TrimSpace(s) == "" {
		return placeholder
	}
	return s
}

// count writes "1 item" and "3 items".
func count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
