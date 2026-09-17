package reminders

import (
	"testing"
	"time"

	"github.com/Yornik/soiree/internal/store"
)

// Every fixture here is obviously synthetic. The schema was drawn from real
// planning data; none of that data belongs in a repository.

// day is the UTC midnight a `date` column round-trips to, which is what pgx
// hands back for lock_by and due.
func day(t *testing.T, s string) *time.Time {
	t.Helper()
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		t.Fatalf("bad test date %q: %v", s, err)
	}
	return &d
}

func at(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("bad test instant %q: %v", s, err)
	}
	return ts
}

func testConfig(t *testing.T, zone string, windowDays int) Config {
	t.Helper()
	loc, err := time.LoadLocation(zone)
	if err != nil {
		t.Fatalf("load %s: %v", zone, err)
	}
	return Config{
		Schedule:   DefaultSchedule,
		WindowDays: windowDays,
		Location:   loc,
		Zone:       zone,
		EventName:  "Ada's Leaving Do",
		Currency:   "EUR",
		To:         []string{"ada@example.test"},
	}
}

func item(t *testing.T, name, vendor, lockBy string, unit int64, paid int64) store.BudgetItem {
	t.Helper()
	it := store.BudgetItem{Item: name, Vendor: vendor, Unit: unit, Qty: 1, Paid: paid}
	if lockBy != "" {
		it.LockBy = day(t, lockBy)
	}
	return it
}

func task(t *testing.T, name, owner, due string, status store.TaskStatus) store.Task {
	t.Helper()
	tk := store.Task{Name: name, Owner: owner, Status: status}
	if due != "" {
		tk.Due = day(t, due)
	}
	return tk
}

func names(d Digest) []string {
	out := make([]string, 0, d.Count())
	for _, it := range d.Deadlines {
		out = append(out, it.Item)
	}
	for _, tk := range d.Tasks {
		out = append(out, tk.Name)
	}
	return out
}

func equal(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// The window is inclusive at both ends. A deadline landing exactly on the
// horizon belongs in this digest, because the alternative is that it first
// appears in the one sent after it has passed.
func TestWindowBoundariesAreInclusive(t *testing.T) {
	cfg := testConfig(t, "UTC", 14)
	now := at(t, "2027-02-25T09:00:00Z")

	items := []store.BudgetItem{
		item(t, "day before the window opens", "Vendor", "2027-02-24", 100, 0),
		item(t, "the day itself", "Vendor", "2027-02-25", 100, 0),
		item(t, "last day inside the window", "Vendor", "2027-03-11", 100, 0),
		item(t, "one day past the window", "Vendor", "2027-03-12", 100, 0),
	}

	got := names(Compose(cfg, now, items, nil))
	want := []string{"day before the window opens", "the day itself", "last day inside the window"}
	if !equal(got, want) {
		t.Errorf("selected %v, want %v", got, want)
	}
}

// A deadline that has already passed is the most important line in the mail,
// so it is never dropped for being old.
func TestOverdueItemsAreIncludedAndCounted(t *testing.T) {
	cfg := testConfig(t, "UTC", 14)
	now := at(t, "2027-02-25T09:00:00Z")

	d := Compose(cfg, now, []store.BudgetItem{
		item(t, "Venue deposit", "Grand Hall", "2027-01-05", 250000, 0),
		item(t, "Florist", "Linus Flowers", "2027-03-01", 40000, 0),
	}, []store.Task{
		task(t, "Send invitations", "Grace", "2027-02-20", store.TaskNotStarted),
	})

	if d.Count() != 3 {
		t.Fatalf("count = %d, want 3", d.Count())
	}
	if d.Overdue() != 2 {
		t.Errorf("overdue = %d, want 2 (the deposit and the invitations)", d.Overdue())
	}
	if d.Deadlines[0].Item != "Venue deposit" {
		t.Errorf("first deadline is %q, want the most overdue one first", d.Deadlines[0].Item)
	}
	if d.Deadlines[0].Days != -51 {
		t.Errorf("Venue deposit is %d days out, want -51", d.Deadlines[0].Days)
	}
	if d.Deadlines[0].Amount != 250000 {
		t.Errorf("amount = %d, want the full 250000 since nothing is paid", d.Deadlines[0].Amount)
	}
}

// `paid` is the only commitment signal the schema has: money has gone to the
// vendor, so the decision this deadline is about has been made. Reminding
// about it is how a digest becomes noise.
func TestDepositPaidIsNotReminded(t *testing.T) {
	cfg := testConfig(t, "UTC", 14)
	now := at(t, "2027-02-25T09:00:00Z")

	d := Compose(cfg, now, []store.BudgetItem{
		item(t, "Venue deposit", "Grand Hall", "2027-02-27", 250000, 50000),
		item(t, "Florist", "Linus Flowers", "2027-02-27", 40000, 0),
	}, nil)

	if got := names(d); !equal(got, []string{"Florist"}) {
		t.Errorf("selected %v, want only the line with nothing paid", got)
	}
}

func TestResolvedAndUndatedRowsAreSkipped(t *testing.T) {
	cfg := testConfig(t, "UTC", 14)
	now := at(t, "2027-02-25T09:00:00Z")

	d := Compose(cfg, now,
		[]store.BudgetItem{
			item(t, "no deadline recorded", "Vendor", "", 100, 0),
		},
		[]store.Task{
			task(t, "Book the band", "Ada", "2027-02-26", store.TaskDone),
			task(t, "Confirm the cake", "Linus", "2027-02-26", store.TaskInProgress),
			task(t, "no due date", "Grace", "", store.TaskNotStarted),
		})

	if got := names(d); !equal(got, []string{"Confirm the cake"}) {
		t.Errorf("selected %v, want only the unfinished dated task", got)
	}
}

// An empty digest is worse than silence: a weekly mail that usually says
// nothing is a weekly mail nobody opens.
func TestNothingDueMakesAnEmptyDigest(t *testing.T) {
	cfg := testConfig(t, "UTC", 14)
	now := at(t, "2027-02-25T09:00:00Z")

	d := Compose(cfg, now, []store.BudgetItem{
		item(t, "Venue deposit", "Grand Hall", "2027-06-01", 250000, 0),
	}, []store.Task{
		task(t, "Send invitations", "Grace", "2027-06-01", store.TaskNotStarted),
	})

	if !d.Empty() {
		t.Errorf("digest is not empty: %v", names(d))
	}
}

// Which day it *is* depends on where the event is. At half past eleven at
// night in Amsterdam it is already tomorrow; in New York it is still
// yesterday afternoon.
func TestTodayIsTheDayInTheConfiguredZone(t *testing.T) {
	now := at(t, "2027-02-25T23:30:00Z")

	for zone, want := range map[string]string{
		"UTC":              "2027-02-25",
		"Europe/Amsterdam": "2027-02-26",
		"America/New_York": "2027-02-25",
		"Asia/Bangkok":     "2027-02-26",
	} {
		d := Compose(testConfig(t, zone, 14), now, nil, nil)
		if got := d.Today.Format(time.DateOnly); got != want {
			t.Errorf("%s: today = %s, want %s", zone, got, want)
		}
		if d.Today.Location() != time.UTC {
			t.Errorf("%s: today is not pinned to UTC, which breaks the day arithmetic", zone)
		}
	}
}

// Deadlines and tasks are sorted by date, soonest first, with ties broken by
// name so the same data always produces the same mail.
func TestOrderingIsStable(t *testing.T) {
	cfg := testConfig(t, "UTC", 30)
	now := at(t, "2027-02-25T09:00:00Z")

	d := Compose(cfg, now, []store.BudgetItem{
		item(t, "Photographer", "Grace Optics", "2027-03-05", 100, 0),
		item(t, "Venue deposit", "Grand Hall", "2027-02-20", 100, 0),
		item(t, "Awning", "Linus Tents", "2027-03-05", 100, 0),
	}, nil)

	want := []string{"Venue deposit", "Awning", "Photographer"}
	if got := names(d); !equal(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

// A window of zero days is a legitimate setting: remind me about what is due
// today and what is already late, and nothing else.
func TestZeroWindowKeepsTodayAndOverdue(t *testing.T) {
	cfg := testConfig(t, "UTC", 0)
	now := at(t, "2027-02-25T09:00:00Z")

	d := Compose(cfg, now, []store.BudgetItem{
		item(t, "yesterday", "Vendor", "2027-02-24", 100, 0),
		item(t, "today", "Vendor", "2027-02-25", 100, 0),
		item(t, "tomorrow", "Vendor", "2027-02-26", 100, 0),
	}, nil)

	if got := names(d); !equal(got, []string{"yesterday", "today"}) {
		t.Errorf("selected %v, want yesterday and today", got)
	}
}
