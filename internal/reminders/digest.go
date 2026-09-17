package reminders

import (
	"math"
	"sort"
	"time"

	"github.com/Yornik/soiree/internal/store"
)

// Digest is what one period's mail says.
//
// It is built by a pure function from the rows and an instant, so every
// question about window boundaries, overdue items and empty digests is
// answerable without a database or a mail server.
type Digest struct {
	EventName  string
	Currency   string
	Zone       string
	WindowDays int

	// Today is the UTC midnight standing for the calendar day it currently is
	// in the configured zone. See the note on day arithmetic below: every date
	// in this package is a calendar day pinned to UTC midnight, never an
	// instant that gets converted.
	Today time.Time
	// Horizon is the last day inside the window, inclusive.
	Horizon time.Time

	Deadlines []Deadline
	Tasks     []TaskDue
}

// Deadline is a budget line whose decision date is close or past.
type Deadline struct {
	Item   string
	Vendor string
	Date   time.Time // the stored calendar day, at UTC midnight
	Days   int       // days from Today; negative is overdue
	Amount int64     // minor units, zero when the line has no price yet
}

// TaskDue is a task whose due date is close or past. Tasks are in the digest
// because "the florist has to be confirmed by Friday" and "someone has to
// phone the florist by Friday" are the same shape of obligation to the person
// reading their mail on Sunday.
type TaskDue struct {
	Name   string
	Owner  string
	Date   time.Time
	Days   int
	Status store.TaskStatus
}

// Empty reports a digest with nothing in it. Sending one anyway is worse than
// silence: a weekly mail that usually says nothing is a weekly mail nobody
// opens, and the week it does say something is the week it gets ignored.
func (d Digest) Empty() bool { return len(d.Deadlines) == 0 && len(d.Tasks) == 0 }

// Count is how many things need attention.
func (d Digest) Count() int { return len(d.Deadlines) + len(d.Tasks) }

// Overdue is how many of them are already past their date.
func (d Digest) Overdue() int {
	n := 0
	for _, it := range d.Deadlines {
		if it.Days < 0 {
			n++
		}
	}
	for _, t := range d.Tasks {
		if t.Days < 0 {
			n++
		}
	}
	return n
}

// Compose selects what belongs in the digest for the day `now` falls on.
//
// Selection happens in Go rather than in SQL. This is a tool for one evening
// and a dozen people — the budget is hundreds of rows, not millions — and
// doing it here means the window boundaries are tested by the same code path
// that runs in production, with no date parameter crossing the driver on the
// way.
//
// What is left out is as deliberate as what is in:
//
//   - A budget line with something paid against it is not reminded about. The
//     schema has no "decided" flag, and `paid` is the only commitment signal
//     it does have: a deposit has gone to the vendor, so the decision this
//     deadline is about has been made. This is an interpretation, not a fact
//     the schema states, which is why it is written down here, in the
//     migration, and in a test named for it.
//   - A task marked done is not reminded about, which needs no interpretation.
//   - A line with no lock_by and a task with no due date are never in the
//     answer, however urgent they might be. A deadline nobody recorded is not
//     something this can find.
func Compose(cfg Config, now time.Time, items []store.BudgetItem, tasks []store.Task) Digest {
	today := dayIn(now, cfg.location())
	horizon := today.AddDate(0, 0, cfg.WindowDays)

	d := Digest{
		EventName:  cfg.EventName,
		Currency:   cfg.Currency,
		Zone:       cfg.Zone,
		WindowDays: cfg.WindowDays,
		Today:      today,
		Horizon:    horizon,
	}

	for _, it := range items {
		if it.LockBy == nil || it.Paid != 0 {
			continue
		}
		day := storedDay(*it.LockBy)
		// Inclusive at both ends: a deadline landing exactly on the horizon is
		// in this digest, and one landing exactly today is not yet overdue but
		// is certainly due.
		if day.After(horizon) {
			continue
		}
		d.Deadlines = append(d.Deadlines, Deadline{
			Item:   it.Item,
			Vendor: it.Vendor,
			Date:   day,
			Days:   daysBetween(today, day),
			Amount: total(it),
		})
	}

	for _, t := range tasks {
		if t.Due == nil || t.Status == store.TaskDone {
			continue
		}
		day := storedDay(*t.Due)
		if day.After(horizon) {
			continue
		}
		d.Tasks = append(d.Tasks, TaskDue{
			Name:   t.Name,
			Owner:  t.Owner,
			Date:   day,
			Days:   daysBetween(today, day),
			Status: t.Status,
		})
	}

	// Soonest first, and the most overdue at the very top — which is the order
	// somebody reading on a phone needs, because they will not scroll.
	sort.SliceStable(d.Deadlines, func(i, j int) bool {
		if !d.Deadlines[i].Date.Equal(d.Deadlines[j].Date) {
			return d.Deadlines[i].Date.Before(d.Deadlines[j].Date)
		}
		return d.Deadlines[i].Item < d.Deadlines[j].Item
	})
	sort.SliceStable(d.Tasks, func(i, j int) bool {
		if !d.Tasks[i].Date.Equal(d.Tasks[j].Date) {
			return d.Tasks[i].Date.Before(d.Tasks[j].Date)
		}
		return d.Tasks[i].Name < d.Tasks[j].Name
	})

	return d
}

// total is what a line costs, in minor units. Compose only reaches here for
// lines with nothing paid, so this is also what is still owed.
//
// Qty is a float64 because the column is numeric(12,3); rounding at the end of
// one multiplication is the only float step, and the result goes straight back
// to an integer.
func total(it store.BudgetItem) int64 {
	if it.Unit == 0 || it.Qty == 0 {
		return 0
	}
	return int64(math.Round(float64(it.Unit) * it.Qty))
}

// Day arithmetic. Three rules, and the whole timezone correctness of this
// package is in them.
//
// 1. Which day it *is* depends on where the event is, so `today` is derived by
//    reading the clock in the configured zone.
// 2. A deadline is a calendar day, not an instant. lock_by and due are `date`
//    columns and pgx hands them back as UTC midnight; rendering them means
//    reading the year, month and day that were stored and nothing else.
//    Converting one into a zone west of UTC moves it to the previous day, and a
//    deadline that reads as a different day depending on the reader is exactly
//    the bug the countdown already had once.
// 3. Both are then UTC midnights, so the difference between them divides
//    cleanly by 24 hours with no DST hour to lose.

// dayIn reduces an instant to the calendar day it falls on in loc, pinned to
// UTC midnight.
func dayIn(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// storedDay takes the calendar day a `date` column round-tripped to, without
// moving it anywhere.
func storedDay(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// daysBetween counts whole days from one UTC midnight to another.
func daysBetween(from, to time.Time) int {
	return int(to.Sub(from) / (24 * time.Hour))
}
