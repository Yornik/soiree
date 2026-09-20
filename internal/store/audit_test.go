package store_test

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/Yornik/soiree/internal/store"
)

// The change log is the answer to "who moved the venue figure, from what, to
// what, and when". These tests are about that answer being complete: recorded
// once per change, correct on both sides, attributed, and still there after the
// row it describes is gone.
//
// Every fixture here is synthetic, as everywhere else in this package.

// history reads one row's history, newest first, failing the test rather than
// making every caller handle an error that means the test is broken.
func history(t *testing.T, s *store.Store, entity string, id uuid.UUID) []store.ChangeEntry {
	t.Helper()
	entries, err := s.ChangeHistory(t.Context(), entity, id, store.HistoryPage{})
	if err != nil {
		t.Fatalf("change history: %v", err)
	}
	return entries
}

// asInt decodes a recorded value as the whole number it was stored as. Money is
// minor units, and the point of recording raw values is that this works.
func asInt(t *testing.T, raw json.RawMessage) int64 {
	t.Helper()
	var n int64
	if err := json.Unmarshal(raw, &n); err != nil {
		t.Fatalf("recorded value %s is not a whole number: %v", raw, err)
	}
	return n
}

func asString(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("recorded value %s is not a string: %v", raw, err)
	}
	return s
}

// TestUpdateRecordsOneEntry is the question the whole table exists for: the
// venue figure moved, and the log says by whose hand, from what, to what.
func TestUpdateRecordsOneEntry(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	item, err := s.CreateBudgetItem(ctx, store.BudgetItem{
		Item: "Venue deposit", Vendor: "Example Hall",
		Unit: store.ToMinor("EUR", 2500), Qty: 1,
	}, nil)
	if err != nil {
		t.Fatalf("create budget item: %v", err)
	}

	item.Unit = store.ToMinor("EUR", 3000)
	updated, err := s.UpdateBudgetItem(ctx, item, nil)
	if err != nil {
		t.Fatalf("update budget item: %v", err)
	}

	entries := history(t, s, store.EntityBudgetItems, item.ID)
	if len(entries) != 2 {
		t.Fatalf("history has %d entries, want the create and one update", len(entries))
	}

	// Newest first.
	got := entries[0]
	if got.Action != store.ChangeUpdate {
		t.Errorf("action = %q, want %q", got.Action, store.ChangeUpdate)
	}
	if got.EntityID == nil || *got.EntityID != item.ID {
		t.Errorf("entityID = %v, want %s", got.EntityID, item.ID)
	}
	if got.Revision == nil || *got.Revision != updated.Revision {
		t.Errorf("revision = %v, want the resulting %d", got.Revision, updated.Revision)
	}

	// Only the field that moved.
	if len(got.Changes) != 1 {
		t.Fatalf("changes = %v, want only unit", got.Changes)
	}
	unit, ok := got.Changes["unit"]
	if !ok {
		t.Fatalf("changes = %v, want a unit entry", got.Changes)
	}
	// Raw minor units on both sides. A formatted "€2,500.00" would be rewritten
	// by a later change of currency, and a history that can be rewritten proves
	// nothing.
	if old := asInt(t, unit.Old); old != 250000 {
		t.Errorf("unit.old = %d, want 250000 minor units", old)
	}
	if fresh := asInt(t, unit.New); fresh != 300000 {
		t.Errorf("unit.new = %d, want 300000 minor units", fresh)
	}

	// And the create, which is what makes the history readable from the start.
	created := entries[1]
	if created.Action != store.ChangeCreate {
		t.Errorf("oldest action = %q, want %q", created.Action, store.ChangeCreate)
	}
	if asString(t, created.Changes["item"].New) != "Venue deposit" {
		t.Errorf("create did not record the item name: %v", created.Changes["item"])
	}
	if string(created.Changes["item"].Old) != "null" {
		t.Errorf("create recorded a previous value: %s", created.Changes["item"].Old)
	}
}

// TestUpdateThatChangesNothingRecordsNothing: a client resending a field it did
// not touch is a normal thing, and a history full of entries saying nothing is
// a history nobody reads.
//
// The row's revision still advances, so revisions in the log skip numbers. That
// is deliberate and documented on the table.
func TestUpdateThatChangesNothingRecordsNothing(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	item, err := s.CreateBudgetItem(ctx, store.BudgetItem{
		Item: "Venue deposit", Unit: store.ToMinor("EUR", 2500), Qty: 1,
	}, nil)
	if err != nil {
		t.Fatalf("create budget item: %v", err)
	}

	unchanged, err := s.UpdateBudgetItem(ctx, item, nil)
	if err != nil {
		t.Fatalf("update budget item: %v", err)
	}
	if unchanged.Revision != 2 {
		t.Errorf("revision = %d, want 2 — the write still happened", unchanged.Revision)
	}

	entries := history(t, s, store.EntityBudgetItems, item.ID)
	if len(entries) != 1 || entries[0].Action != store.ChangeCreate {
		t.Fatalf("history = %d entries, want only the create", len(entries))
	}
}

// TestWriteAndHistoryCommitTogether is the property the whole design rests on:
// a write cannot happen without being recorded, because the two are one
// transaction. An actor naming an account that does not exist makes the history
// insert fail, and the edit it described has to fail with it.
func TestWriteAndHistoryCommitTogether(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	item, err := s.CreateBudgetItem(ctx, store.BudgetItem{
		Item: "Venue deposit", Unit: store.ToMinor("EUR", 2500), Qty: 1,
	}, nil)
	if err != nil {
		t.Fatalf("create budget item: %v", err)
	}

	ghost := uuid.New()
	doomed := store.WithActor(ctx, store.Actor{ID: &ghost, Label: "ghost"})

	item.Unit = store.ToMinor("EUR", 9999)
	if _, err := s.UpdateBudgetItem(doomed, item, nil); err == nil {
		t.Fatal("a write whose history could not be recorded was accepted")
	}

	reread, err := s.BudgetItem(ctx, item.ID)
	if err != nil {
		t.Fatalf("budget item: %v", err)
	}
	if reread.Unit != 250000 || reread.Revision != 1 {
		t.Errorf("item = unit %d revision %d, want the write rolled back", reread.Unit, reread.Revision)
	}

	entries := history(t, s, store.EntityBudgetItems, item.ID)
	if len(entries) != 1 {
		t.Errorf("history has %d entries, want only the create — the rolled back write left one behind", len(entries))
	}
}

// TestHistoryOutlivesTheRow is the entire point. A deleted line still answers
// for what it cost, and so does the breakdown the cascade took with it.
func TestHistoryOutlivesTheRow(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	parent, err := s.CreateBudgetItem(ctx, store.BudgetItem{
		Item: "Catering", Unit: store.ToMinor("EUR", 4500), Qty: 40,
	}, nil)
	if err != nil {
		t.Fatalf("create budget item: %v", err)
	}
	child, err := s.CreateBudgetItem(ctx, store.BudgetItem{
		ParentID: &parent.ID, Item: "Dessert course",
		Unit: store.ToMinor("EUR", 750), Qty: 40, Position: 1,
	}, nil)
	if err != nil {
		t.Fatalf("create child budget item: %v", err)
	}

	if err := s.DeleteBudgetItem(ctx, parent.ID, parent.Revision); err != nil {
		t.Fatalf("delete budget item: %v", err)
	}
	if _, err := s.BudgetItem(ctx, child.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("child after cascade: %v, want ErrNotFound", err)
	}

	for _, tc := range []struct {
		name string
		id   uuid.UUID
		unit int64
	}{
		{"parent", parent.ID, 450000},
		{"child, taken by the cascade", child.ID, 75000},
	} {
		entries := history(t, s, store.EntityBudgetItems, tc.id)
		if len(entries) != 2 {
			t.Fatalf("%s: history has %d entries, want the create and the delete", tc.name, len(entries))
		}
		got := entries[0]
		if got.Action != store.ChangeDelete {
			t.Errorf("%s: newest action = %q, want %q", tc.name, got.Action, store.ChangeDelete)
		}
		// The final state, on the old side: what the row was worth when it went.
		if old := asInt(t, got.Changes["unit"].Old); old != tc.unit {
			t.Errorf("%s: recorded final unit = %d, want %d", tc.name, old, tc.unit)
		}
		if string(got.Changes["unit"].New) != "null" {
			t.Errorf("%s: delete recorded a new value: %s", tc.name, got.Changes["unit"].New)
		}
	}
}

// TestActorIsRecorded covers all three cases that exist today: a session that
// knows who is acting, a caller that does not, and an account deleted after the
// fact — which must cost the entry its name, never its existence.
func TestActorIsRecorded(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	ada, err := s.CreateUser(ctx, store.User{Email: "ada@example.test", Role: store.RoleEditor})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	item, err := s.CreateBudgetItem(ctx, store.BudgetItem{
		Item: "Venue deposit", Unit: store.ToMinor("EUR", 2500), Qty: 1,
	}, nil)
	if err != nil {
		t.Fatalf("create budget item: %v", err)
	}

	// No account anywhere: recorded as unknown rather than refused. Accounts are
	// a separate milestone and holding writes hostage to it would mean no
	// history at all until it lands.
	if got := history(t, s, store.EntityBudgetItems, item.ID)[0]; got.ActorID != nil || got.ActorLabel != "unknown" {
		t.Errorf("actor = %v/%q, want nobody and %q", got.ActorID, got.ActorLabel, "unknown")
	}

	asAda := store.WithActor(ctx, store.Actor{ID: &ada.ID, Label: "editor"})
	item.Unit = store.ToMinor("EUR", 3000)
	if _, err := s.UpdateBudgetItem(asAda, item, nil); err != nil {
		t.Fatalf("update budget item: %v", err)
	}

	got := history(t, s, store.EntityBudgetItems, item.ID)[0]
	if got.ActorID == nil || *got.ActorID != ada.ID {
		t.Fatalf("actor = %v, want %s", got.ActorID, ada.ID)
	}
	if got.ActorLabel != "editor" {
		t.Errorf("actor label = %q, want %q", got.ActorLabel, "editor")
	}

	// Deleting the account anonymises the entry and keeps it. The alternative
	// would let anyone erase what they changed by deleting themselves, and the
	// money would still have moved.
	if err := s.DeleteUser(ctx, ada.ID, ada.Revision); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	after := history(t, s, store.EntityBudgetItems, item.ID)
	if len(after) != 2 {
		t.Fatalf("history has %d entries after deleting the actor, want 2", len(after))
	}
	if after[0].ActorID != nil {
		t.Errorf("actor = %v, want nobody after the account was deleted", after[0].ActorID)
	}
	if asInt(t, after[0].Changes["unit"].New) != 300000 {
		t.Error("deleting the actor changed what the entry says happened")
	}
}

// TestTheRowAndTheLogNameTheSameActor holds `updated_by` to the actor the entry
// was given. The two were resolved once and then written from different
// places: the log from the context, the column from the explicit argument. The
// API names its actor in the context only, so the log said who and the row said
// nobody, and an edit put NULL over an attribution the row already had.
func TestTheRowAndTheLogNameTheSameActor(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	ada, err := s.CreateUser(ctx, store.User{Email: "ada@example.test", Role: store.RoleEditor})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	grace, err := s.CreateUser(ctx, store.User{Email: "grace@example.test", Role: store.RoleEditor})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	asAda := store.WithActor(ctx, store.Actor{ID: &ada.ID})
	asGrace := store.WithActor(ctx, store.Actor{ID: &grace.ID})

	agree := func(step string, by *uuid.UUID, id uuid.UUID, want uuid.UUID) {
		t.Helper()
		if by == nil || *by != want {
			t.Errorf("%s: updated_by = %v, want %s", step, by, want)
		}
		if got := history(t, s, store.EntityTasks, id)[0]; got.ActorID == nil || *got.ActorID != want {
			t.Errorf("%s: the entry names %v, want %s", step, got.ActorID, want)
		}
	}

	// The shape of every API write: the session in the context, no argument.
	task, err := s.CreateTask(asAda, store.Task{Name: "Book the band"}, nil)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	agree("create", task.UpdatedBy, task.ID, ada.ID)

	task.Name = "Book the quartet"
	if task, err = s.UpdateTask(asGrace, task, nil); err != nil {
		t.Fatalf("update task: %v", err)
	}
	agree("update", task.UpdatedBy, task.ID, grace.ID)

	// Where both are given the session wins, in the column as in the log: the
	// argument is only ever the caller's own claim.
	task.Name = "Book the trio"
	if task, err = s.UpdateTask(asAda, task, &grace.ID); err != nil {
		t.Fatalf("update task: %v", err)
	}
	agree("update with both", task.UpdatedBy, task.ID, ada.ID)

	reread, err := s.Task(ctx, task.ID)
	if err != nil {
		t.Fatalf("task: %v", err)
	}
	if reread.UpdatedBy == nil || *reread.UpdatedBy != ada.ID {
		t.Errorf("a read finds updated_by = %v, want %s", reread.UpdatedBy, ada.ID)
	}
}

// TestConcurrentUpdatesEachRecordOnce: four people editing the same line at
// once must produce four entries, not three and not five. Lose one and the
// history no longer adds up to the figure now on the row.
func TestConcurrentUpdatesEachRecordOnce(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	item, err := s.CreateBudgetItem(ctx, store.BudgetItem{
		Item: "Venue deposit", Unit: store.ToMinor("EUR", 2500), Qty: 1,
	}, nil)
	if err != nil {
		t.Fatalf("create budget item: %v", err)
	}

	const writers = 4
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Each writer lands exactly one edit, refetching after a conflict —
			// which is what the browser will do when it gets its 409.
			for {
				current, err := s.BudgetItem(ctx, item.ID)
				if err != nil {
					t.Errorf("writer %d: budget item: %v", i, err)
					return
				}
				current.Paid += 100
				if _, err := s.UpdateBudgetItem(ctx, current, nil); err == nil {
					return
				} else if !errors.Is(err, store.ErrStaleRevision) {
					t.Errorf("writer %d: update: %v", i, err)
					return
				}
			}
		}()
	}
	wg.Wait()

	final, err := s.BudgetItem(ctx, item.ID)
	if err != nil {
		t.Fatalf("budget item: %v", err)
	}
	if final.Paid != writers*100 {
		t.Errorf("paid = %d, want %d — an edit was lost", final.Paid, writers*100)
	}

	entries := history(t, s, store.EntityBudgetItems, item.ID)
	if len(entries) != writers+1 {
		t.Fatalf("history has %d entries, want %d updates plus the create", len(entries), writers)
	}
	// The recorded steps have to chain: each entry's old is the previous
	// entry's new, all the way up to the figure now on the row.
	want := final.Paid
	for _, e := range entries[:writers] {
		if got := asInt(t, e.Changes["paid"].New); got != want {
			t.Fatalf("entry %d records paid %d, want %d — the chain is broken", e.ID, got, want)
		}
		want = asInt(t, e.Changes["paid"].Old)
	}
	if want != 0 {
		t.Errorf("the chain starts at %d, want the original 0", want)
	}
}

// TestHistoryIsNewestFirstAndPaged: the reading this table exists for. Newest
// first because the argument is always about the last change, and paged because
// a table that only grows should not be read whole.
func TestHistoryIsNewestFirstAndPaged(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	task, err := s.CreateTask(ctx, store.Task{Name: "Confirm the guest count"}, nil)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	for _, owner := range []string{"Ada", "Grace", "Linus"} {
		task.Owner = owner
		task, err = s.UpdateTask(ctx, task, nil)
		if err != nil {
			t.Fatalf("update task: %v", err)
		}
	}

	all := history(t, s, store.EntityTasks, task.ID)
	if len(all) != 4 {
		t.Fatalf("history has %d entries, want three updates plus the create", len(all))
	}
	if asString(t, all[0].Changes["owner"].New) != "Linus" {
		t.Errorf("newest entry is %v, want the last edit", all[0].Changes["owner"])
	}
	for i := 1; i < len(all); i++ {
		if all[i].ID >= all[i-1].ID {
			t.Fatalf("entries are not newest first: %d follows %d", all[i].ID, all[i-1].ID)
		}
	}

	page, err := s.ChangeHistory(ctx, store.EntityTasks, task.ID, store.HistoryPage{Limit: 2, Offset: 1})
	if err != nil {
		t.Fatalf("change history: %v", err)
	}
	if len(page) != 2 {
		t.Fatalf("page has %d entries, want 2", len(page))
	}
	if page[0].ID != all[1].ID || page[1].ID != all[2].ID {
		t.Errorf("page = %d,%d, want %d,%d", page[0].ID, page[1].ID, all[1].ID, all[2].ID)
	}
}

// TestChangeLogRefusesEditsAndDeletes: append-only has to be enforced, not
// promised. The application owns this database, so nothing but the trigger
// stands between a careless UPDATE and a history that agrees with whoever
// edited it last.
func TestChangeLogRefusesEditsAndDeletes(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	note, err := s.CreateNote(ctx, store.Note{Text: "Venue balance is due a month out."})
	if err != nil {
		t.Fatalf("create note: %v", err)
	}
	entries := history(t, s, store.EntityNotes, note.ID)
	if len(entries) != 1 {
		t.Fatalf("history has %d entries, want the create", len(entries))
	}

	if _, err := s.Pool().Exec(ctx,
		`UPDATE change_log SET changes = '{}'::jsonb WHERE id = $1`, entries[0].ID); err == nil {
		t.Error("a history entry was rewritten")
	}
	if _, err := s.Pool().Exec(ctx, `DELETE FROM change_log WHERE id = $1`, entries[0].ID); err == nil {
		t.Error("a history entry was deleted")
	}

	if len(history(t, s, store.EntityNotes, note.ID)) != 1 {
		t.Error("the entry did not survive")
	}
}

// TestAccountHistoryKeepsThePasswordOut: an append-only table is exactly the
// wrong place for a password hash, and exactly the right place for a change of
// login address — that one is an account takeover when it is not a typo fix,
// and only the old and new values tell the two apart.
func TestAccountHistoryKeepsThePasswordOut(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	hash := "$argon2id$v=19$m=19456,t=2,p=1$c3ludGhldGlj$bm90YXJlYWxoYXNo"
	grace, err := s.CreateUser(ctx, store.User{
		Email: "grace@example.test", Role: store.RoleViewer, PasswordHash: &hash,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	grace.Role = store.RoleAdmin
	grace.Email = "grace.h@example.test"
	if _, err := s.UpdateUser(ctx, grace); err != nil {
		t.Fatalf("update user: %v", err)
	}

	entries := history(t, s, store.EntityUsers, grace.ID)
	if len(entries) != 2 {
		t.Fatalf("history has %d entries, want the create and the change", len(entries))
	}

	// The two questions worth answering about an account.
	role := entries[0].Changes["role"]
	if asString(t, role.Old) != string(store.RoleViewer) || asString(t, role.New) != string(store.RoleAdmin) {
		t.Errorf("role change = %v, want viewer to admin", role)
	}
	email := entries[0].Changes["email"]
	if asString(t, email.Old) != "grace@example.test" || asString(t, email.New) != "grace.h@example.test" {
		t.Errorf("email change = %v, want both addresses recorded", email)
	}

	for _, e := range entries {
		hashed, changed := e.Changes["password_hash"]
		if !changed {
			continue
		}
		if asString(t, hashed.New) != "redacted" {
			t.Errorf("the password hash was recorded verbatim: %s", hashed.New)
		}
	}
	// The create set one, so the redacted entry has to be there — otherwise
	// this test would pass by never looking at anything.
	if _, changed := entries[1].Changes["password_hash"]; !changed {
		t.Error("the create did not record that a password hash was set")
	}
}

// TestSettingsHistoryUsesTheNilID: the singleton has no id of its own, and the
// ceiling is money — "who raised the budget" is one of the questions that
// starts the argument.
func TestSettingsHistoryUsesTheNilID(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	settings, err := s.Settings(ctx)
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	settings.Ceiling = store.ToMinor("EUR", 7000)
	if _, err := s.UpdateSettings(ctx, settings); err != nil {
		t.Fatalf("update settings: %v", err)
	}

	entries := history(t, s, store.EntitySettings, uuid.Nil)
	if len(entries) != 1 {
		t.Fatalf("history has %d entries, want the one update", len(entries))
	}
	if entries[0].EntityID != nil {
		t.Errorf("entityID = %v, want none for the singleton", entries[0].EntityID)
	}
	if got := asInt(t, entries[0].Changes["ceiling"].New); got != 700000 {
		t.Errorf("ceiling = %d, want 700000 minor units", got)
	}
}
