package reminders

import (
	"strings"
	"testing"
	"time"
)

// The period key is what a restarted process recomputes to discover that this
// week's digest has already gone out, so it has to be the same string for
// every instant inside the week and readable enough to grep a log for.
func TestPeriodKeyIsStableWithinAPeriod(t *testing.T) {
	cfg := testConfig(t, "Europe/Amsterdam", 14)
	week := 7 * 24 * time.Hour

	key := func(s string) string {
		return periodKey(week, dayIn(at(t, s), cfg.location()))
	}

	// Monday 14 January 2030 through the Sunday after it.
	start := key("2030-01-13T23:30:00Z") // 00:30 Monday in Amsterdam
	for _, instant := range []string{
		"2030-01-14T09:00:00Z",
		"2030-01-17T09:00:00Z",
		"2030-01-20T22:59:00Z", // 23:59 Sunday in Amsterdam, still inside
	} {
		if got := key(instant); got != start {
			t.Errorf("%s keyed as %q, want %q", instant, got, start)
		}
	}
	if next := key("2030-01-20T23:30:00Z"); next == start {
		t.Errorf("the following week keyed as %q, the same as the previous one", next)
	}
	if want := "digest/7d/2030-01-14"; start != want {
		t.Errorf("key = %q, want %q — a week that starts on a Monday", start, want)
	}
	if !strings.Contains(start, "7d") {
		t.Errorf("key %q does not name the period width", start)
	}
}

// A weekly digest arriving on a Thursday because 1 January 1970 was one is an
// artefact nobody can explain. Buckets start on a Monday.
func TestWeeklyPeriodsStartOnMonday(t *testing.T) {
	for _, s := range []string{"2027-01-01", "2027-06-15", "2028-02-29", "1969-07-20"} {
		today := dayIn(at(t, s+"T12:00:00Z"), time.UTC)
		key := periodKey(7*24*time.Hour, today)
		day, err := time.Parse(time.DateOnly, strings.TrimPrefix(key, "digest/7d/"))
		if err != nil {
			t.Fatalf("unparseable key %q: %v", key, err)
		}
		if day.Weekday() != time.Monday {
			t.Errorf("%s: week starts on a %s (%s)", s, day.Weekday(), key)
		}
		if day.After(today) || today.Sub(day) >= 7*24*time.Hour {
			t.Errorf("%s: %s is not the week containing it", s, key)
		}
	}
}

// Changing the schedule changes what a period means, so it changes the key
// space. One extra digest is the honest outcome of that edit.
func TestPeriodKeyDependsOnTheSchedule(t *testing.T) {
	today := dayIn(at(t, "2030-01-17T09:00:00Z"), time.UTC)
	weekly := periodKey(7*24*time.Hour, today)
	daily := periodKey(24*time.Hour, today)
	if weekly == daily {
		t.Errorf("a weekly and a daily digest share the key %q", weekly)
	}
}

// Buckets are calendar days in the event's zone, not an offset from the epoch.
// A boundary an operator cannot predict is a boundary they cannot reason about
// when two digests arrive.
func TestPeriodBoundariesAreCalendarDays(t *testing.T) {
	amsterdam := testConfig(t, "Europe/Amsterdam", 14).location()
	daily := 24 * time.Hour

	// 23:30 UTC is already the next day in Amsterdam, and the key follows.
	before := periodKey(daily, dayIn(at(t, "2030-01-17T22:30:00Z"), amsterdam))
	after := periodKey(daily, dayIn(at(t, "2030-01-17T23:30:00Z"), amsterdam))
	if before == after {
		t.Error("local midnight did not start a new daily period")
	}
	if !strings.Contains(after, "2030-01-18") {
		t.Errorf("key %q does not name the day it covers", after)
	}
}
