package store_test

import (
	"testing"

	"github.com/Yornik/soiree/internal/store"
)

// The feed is the whole plan's history, newest first, and every entry can say
// who and what — including for a row that no longer exists, which is the entry
// people most want explained.
func TestTheActivityFeedNamesWhatChangedAndWho(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	ada, err := s.CreateUser(ctx, store.User{Email: "ada@example.test", Role: store.RoleAdmin, Status: store.StatusActive})
	if err != nil {
		t.Fatal(err)
	}
	as := store.WithActor(ctx, store.Actor{ID: &ada.ID})

	item, err := s.CreateBudgetItem(as, store.BudgetItem{Item: "Venue", Unit: 250000, Qty: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	item.Paid = 50000
	item, err = s.UpdateBudgetItem(as, item, nil)
	if err != nil {
		t.Fatal(err)
	}
	item.Item = "Venue (the big hall)"
	item, err = s.UpdateBudgetItem(as, item, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteBudgetItem(as, item.ID, item.Revision); err != nil {
		t.Fatal(err)
	}

	feed, err := s.Activity(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		entity string
		action store.ChangeAction
		label  string
	}
	var got []row
	for _, e := range feed {
		label := "<none>"
		if e.Label != nil {
			label = *e.Label
		}
		got = append(got, row{e.Entity, e.Action, label})
	}
	want := []row{
		// Gone, and still named: the delete's own snapshot holds the name.
		{store.EntityBudgetItems, store.ChangeDelete, "Venue (the big hall)"},
		{store.EntityBudgetItems, store.ChangeUpdate, "Venue (the big hall)"},
		// What it was called when this happened, not what it was called later.
		// This entry changed `paid` and does not mention `item` at all, so the
		// name has to come from an earlier entry for the same row.
		{store.EntityBudgetItems, store.ChangeUpdate, "Venue"},
		{store.EntityBudgetItems, store.ChangeCreate, "Venue"},
		{store.EntityUsers, store.ChangeCreate, "ada@example.test"},
	}
	if len(got) != len(want) {
		t.Fatalf("feed has %d entries, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	for _, e := range feed[:4] {
		if e.ActorEmail == nil || *e.ActorEmail != "ada@example.test" {
			t.Errorf("entry %d: actor = %v, want ada", e.ID, e.ActorEmail)
		}
	}
	// The account's own creation had nobody signed in behind it.
	if feed[4].ActorEmail != nil {
		t.Errorf("the first account was created by %q", *feed[4].ActorEmail)
	}

	// Deleting the account takes the address out of the feed, and leaves the
	// entries: that is the whole reason it is joined and not recorded.
	if err := s.DeleteUser(ctx, ada.ID, ada.Revision); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	feed, err = s.Activity(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range feed {
		if e.Entity == store.EntityBudgetItems && e.ActorEmail != nil {
			t.Errorf("entry %d still names %q after the account was deleted", e.ID, *e.ActorEmail)
		}
	}
}

// Paged by id, so an entry added while somebody is reading neither repeats a
// row nor skips one.
func TestTheActivityFeedPagesWithoutRepeating(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		if _, err := s.CreateTask(ctx, store.Task{Name: name}, nil); err != nil {
			t.Fatal(err)
		}
	}

	first, err := s.Activity(ctx, 0, 2)
	if err != nil || len(first) != 2 {
		t.Fatalf("first page: %d entries, %v", len(first), err)
	}
	// Somebody edits while the first page is on screen.
	if _, err := s.CreateTask(ctx, store.Task{Name: "f"}, nil); err != nil {
		t.Fatal(err)
	}
	second, err := s.Activity(ctx, first[len(first)-1].ID, 2)
	if err != nil || len(second) != 2 {
		t.Fatalf("second page: %d entries, %v", len(second), err)
	}

	var names []string
	for _, e := range append(first, second...) {
		names = append(names, *e.Label)
	}
	if got, want := names, []string{"e", "d", "c", "b"}; !equalStrings(got, want) {
		t.Fatalf("two pages read %v, want %v", got, want)
	}

	// The ceiling holds whatever is asked for.
	all, err := s.Activity(ctx, 0, 100000)
	if err != nil || len(all) != 6 {
		t.Fatalf("a huge limit read %d entries, %v", len(all), err)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
