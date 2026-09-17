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
	d := Compose(cfg, at(t, "2027-02-25T09:00:00Z"), []store.BudgetItem{
		item(t, "Venue deposit", "Grand Hall", "2027-02-20", 250000, 0),
		item(t, "Florist", "Linus Flowers", "2027-03-02", 42050, 0),
	}, []store.Task{
		task(t, "Send invitations", "Grace", "2027-02-26", store.TaskInProgress),
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
	for _, zone := range []string{"UTC", "America/New_York", "Asia/Jakarta", "Pacific/Kiritimati"} {
		_, text, html := renderFixture(t, zone)
		for _, want := range []string{"Sat 20 Feb 2027", "Tue 2 Mar 2027"} {
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
	d := Compose(cfg, at(t, "2027-02-25T23:30:00Z"), []store.BudgetItem{
		item(t, "Venue deposit", "Grand Hall", "2027-02-26", 250000, 0),
	}, nil)

	msg, err := Render(d)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	// 13:30 on the 26th in Kiritimati, so the 26th is today — and the date
	// itself still reads as the 26th.
	if !strings.Contains(msg.Text, "Fri 26 Feb 2027") {
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
	d := Compose(cfg, at(t, "2027-02-25T09:00:00Z"), []store.BudgetItem{
		item(t, `<script>alert("x")</script>`, "Vendor", "2027-02-26", 100, 0),
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
	d := Compose(cfg, at(t, "2027-02-25T09:00:00Z"), []store.BudgetItem{
		{LockBy: day(t, "2027-02-26")},
	}, []store.Task{
		{Due: day(t, "2027-02-26"), Status: store.TaskNotStarted},
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
