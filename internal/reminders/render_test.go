package reminders

import (
	"regexp"
	"strings"
	"testing"

	"github.com/Yornik/soiree/internal/store"
)

func renderFixture(t *testing.T, zone string) (Digest, string, string) {
	t.Helper()

	cfg := testConfig(t, zone, 14)
	d := Compose(cfg, at(t, "2030-01-17T09:00:00Z"), []store.BudgetItem{
		item(t, "Venue deposit", "Grand Hall", "2030-01-12", 250000, 0),
		item(t, "Florist", "Linus Flowers", "2030-01-22", 42050, 0),
	}, []store.Task{
		task(t, "Send invitations", "Grace", "2030-01-18", store.TaskInProgress),
	})

	msg, err := Render(d)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return d, msg.Text, msg.HTML
}

func TestRenderSaysTheSameThingInBothParts(t *testing.T) {
	_, text, html := renderFixture(t, "UTC")

	for _, want := range []string{
		"Ada's Leaving Do",
		"Venue deposit",
		"Grand Hall",
		"EUR 2500.00",
		"Florist",
		"EUR 420.50",
		"Send invitations",
		"Grace",
		"overdue by 5 days",
		"tomorrow",
		"in 5 days",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text body is missing %q:\n%s", want, text)
		}
		if !strings.Contains(html, want) && !strings.Contains(html, htmlish(want)) {
			t.Errorf("html body is missing %q:\n%s", want, html)
		}
	}
}

// html/template escapes the apostrophe in the event name; this is what that
// looks like, and it is the right behaviour rather than something to work
// around.
func htmlish(s string) string {
	return strings.ReplaceAll(s, "'", "&#39;")
}

func TestSubjectCarriesTheHeadline(t *testing.T) {
	d, _, _ := renderFixture(t, "UTC")
	msg, err := Render(d)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.HasPrefix(msg.Subject, "Ada's Leaving Do: ") {
		t.Errorf("subject = %q, want it to open with the event name", msg.Subject)
	}
	for _, want := range []string{"1 overdue", "2 coming up"} {
		if !strings.Contains(msg.Subject, want) {
			t.Errorf("subject = %q, want it to contain %q", msg.Subject, want)
		}
	}
}

// The regression this project already fixed once in the countdown: a date
// stored as a calendar day must read as the same day for every reader.
// America/New_York is UTC-5, so converting the stored UTC midnight into it
// would move every deadline back a day.
func TestDeadlineDayDoesNotShiftWithTheZone(t *testing.T) {
	var rendered []string
	for _, zone := range []string{"UTC", "America/New_York", "Asia/Bangkok", "Pacific/Kiritimati"} {
		_, text, html := renderFixture(t, zone)
		for _, want := range []string{"Sat 12 Jan 2030", "Tue 22 Jan 2030"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s: text body does not show the deadline as %q:\n%s", zone, want, text)
			}
			if !strings.Contains(html, want) {
				t.Errorf("%s: html body does not show the deadline as %q", zone, want)
			}
		}
		rendered = append(rendered, zone)
	}
	if len(rendered) != 4 {
		t.Fatalf("checked %d zones", len(rendered))
	}
}

// The zone does decide which day counts as today, and so how far away a
// deadline reads as being — that part is supposed to move.
func TestRelativeWordingFollowsTheZone(t *testing.T) {
	cfg := testConfig(t, "Pacific/Kiritimati", 14) // UTC+14
	d := Compose(cfg, at(t, "2030-01-17T23:30:00Z"), []store.BudgetItem{
		item(t, "Venue deposit", "Grand Hall", "2030-01-18", 250000, 0),
	}, nil)

	msg, err := Render(d)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	// 13:30 on the 26th in Kiritimati, so the 26th is today — and the date
	// itself still reads as the 26th.
	if !strings.Contains(msg.Text, "Fri 18 Jan 2030") {
		t.Errorf("date moved:\n%s", msg.Text)
	}
	if !strings.Contains(msg.Text, "today") {
		t.Errorf("a deadline on the local today does not say so:\n%s", msg.Text)
	}
}

// No tracking pixel, no remote image, no stylesheet, no link. A digest that
// reported who opened it would be surveillance of the people it is meant to
// help.
func TestHTMLFetchesNothing(t *testing.T) {
	_, _, html := renderFixture(t, "UTC")

	for _, forbidden := range []string{"<img", "<script", "<link", "<iframe", "background-image", "url(", "http://", "https://", "//"} {
		if strings.Contains(html, forbidden) {
			t.Errorf("html body contains %q, which can cause a third-party request:\n%s", forbidden, html)
		}
	}
	if regexp.MustCompile(`(?i)<a\s`).MatchString(html) {
		t.Error("html body contains a link")
	}
}

func TestRenderEscapesContent(t *testing.T) {
	cfg := testConfig(t, "UTC", 14)
	d := Compose(cfg, at(t, "2030-01-17T09:00:00Z"), []store.BudgetItem{
		item(t, `<script>alert("x")</script>`, "Vendor", "2030-01-18", 100, 0),
	}, nil)

	msg, err := Render(d)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(msg.HTML, "<script>") {
		t.Errorf("an item name was rendered as markup:\n%s", msg.HTML)
	}
	if !strings.Contains(msg.HTML, "&lt;script&gt;") {
		t.Errorf("an item name was not escaped:\n%s", msg.HTML)
	}
}

func TestRenderHandlesBlankFields(t *testing.T) {
	cfg := testConfig(t, "UTC", 14)
	// A deployment that never set SOIREE_EVENT_NAME still sends a digest, and
	// the footer names the event to say why the mail arrived.
	cfg.EventName = ""
	d := Compose(cfg, at(t, "2030-01-17T09:00:00Z"), []store.BudgetItem{
		{LockBy: day(t, "2030-01-18")},
	}, []store.Task{
		{Due: day(t, "2030-01-18"), Status: store.TaskNotStarted},
	})

	msg, err := Render(d)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(msg.Text, "(unnamed line)") || !strings.Contains(msg.Text, "(unnamed task)") {
		t.Errorf("a row with no name rendered as nothing at all:\n%s", msg.Text)
	}
	// No price and no vendor means no dangling separator.
	if strings.Contains(msg.Text, "— ·") || strings.Contains(msg.Text, "· ·") {
		t.Errorf("empty fields left a separator behind:\n%s", msg.Text)
	}
	if !strings.Contains(msg.Text, "active admin of this event") {
		t.Errorf("an unnamed event left a gap where the footer says why this arrived:\n%s", msg.Text)
	}
}

// The footer is the only part of a digest that says anything about the digest
// itself, so what it says has to be true of the recipients this version has.
// It used to describe a "reminder list" and ask the reader to have the address
// taken off it; an active admin is on no such list, so neither they nor the
// person they asked could act on that.
func TestFooterSaysWhyItArrivedAndHowItStops(t *testing.T) {
	_, text, html := renderFixture(t, "UTC")

	for _, body := range []struct{ part, got string }{{"text", text}, {"html", html}} {
		for _, gone := range []string{"reminder list", "taken off"} {
			if strings.Contains(body.got, gone) {
				t.Errorf("%s footer still points at a list nobody is on (%q):\n%s", body.part, gone, body.got)
			}
		}
		// Both reasons an address receives the digest, and both of the things
		// that stop it. One body says this to an admin and to a configured
		// address alike, so leaving either half out makes it false for one of
		// them.
		for _, want := range []string{
			"active admin of Ada's Leaving Do",
			"added your address",
			"reminder settings",
			"no longer an active admin",
		} {
			if !strings.Contains(body.got, want) && !strings.Contains(body.got, htmlish(want)) {
				t.Errorf("%s footer is missing %q:\n%s", body.part, want, body.got)
			}
		}
	}
}

func TestCountAndRelativeWording(t *testing.T) {
	if got := count(1, "item"); got != "1 item" {
		t.Errorf("count(1) = %q", got)
	}
	if got := count(3, "day"); got != "3 days" {
		t.Errorf("count(3) = %q", got)
	}
	for days, want := range map[int]string{
		-5: "overdue by 5 days",
		-1: "overdue since yesterday",
		0:  "today",
		1:  "tomorrow",
		9:  "in 9 days",
	} {
		if got := relative(days); got != want {
			t.Errorf("relative(%d) = %q, want %q", days, got, want)
		}
	}
}
