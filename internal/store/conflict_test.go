package store_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/Yornik/soiree/internal/store"
)

// The conflict path is the reason this layer exists. Two people edit the same
// plan at once; the browser's current last-write-wins behaviour drops one of
// their edits and tells neither of them.

// TestStaleRevisionIsRefused walks the same scenario over every revisioned
// entity: both people read revision 1, the first write succeeds, the second
// carries a revision that is no longer current and must be refused.
func TestStaleRevisionIsRefused(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	cases := []struct {
		entity string
		// write performs an update at the given revision. Two calls at the
		// same revision is the race.
		write func(revision int64) error
	}{
		{
			entity: "settings",
			write: func(revision int64) error {
				_, err := s.UpdateSettings(ctx, store.Settings{Ceiling: 100 * revision, Revision: revision})
				return err
			},
		},
		{
			entity: "sponsors",
			write: func() func(int64) error {
				sponsor, err := s.CreateSponsor(ctx, store.Sponsor{Code: "Rose", Name: "Ada"}, nil)
				if err != nil {
					t.Fatalf("create sponsor: %v", err)
				}
				return func(revision int64) error {
					sponsor.Revision = revision
					_, err := s.UpdateSponsor(ctx, sponsor, nil)
					return err
				}
			}(),
		},
		{
			// Phases were the exception until migration 0009: no revision
			// column, so two people renaming the same stage of the evening
			// could not be told apart. They are in this table for the same
			// reason everything else is.
			entity: "phases",
			write: func() func(int64) error {
				phase, err := s.CreatePhase(ctx, store.Phase{Name: "Arrival"}, nil)
				if err != nil {
					t.Fatalf("create phase: %v", err)
				}
				return func(revision int64) error {
					phase.Revision = revision
					_, err := s.UpdatePhase(ctx, phase, nil)
					return err
				}
			}(),
		},
		{
			entity: "budget_items",
			write: func() func(int64) error {
				item, err := s.CreateBudgetItem(ctx, store.BudgetItem{Item: "Venue deposit", Qty: 1}, nil)
				if err != nil {
					t.Fatalf("create budget item: %v", err)
				}
				return func(revision int64) error {
					item.Revision = revision
					_, err := s.UpdateBudgetItem(ctx, item, nil)
					return err
				}
			}(),
		},
		{
			entity: "programme_entries",
			write: func() func(int64) error {
				entry, err := s.CreateProgrammeEntry(ctx, store.ProgrammeEntry{Title: "Speeches"})
				if err != nil {
					t.Fatalf("create programme entry: %v", err)
				}
				return func(revision int64) error {
					entry.Revision = revision
					_, err := s.UpdateProgrammeEntry(ctx, entry)
					return err
				}
			}(),
		},
		{
			entity: "tasks",
			write: func() func(int64) error {
				task, err := s.CreateTask(ctx, store.Task{Name: "Send invitations"}, nil)
				if err != nil {
					t.Fatalf("create task: %v", err)
				}
				return func(revision int64) error {
					task.Revision = revision
					_, err := s.UpdateTask(ctx, task, nil)
					return err
				}
			}(),
		},
		{
			entity: "notes",
			write: func() func(int64) error {
				note, err := s.CreateNote(ctx, store.Note{Text: "Check the parking."})
				if err != nil {
					t.Fatalf("create note: %v", err)
				}
				return func(revision int64) error {
					note.Revision = revision
					_, err := s.UpdateNote(ctx, note)
					return err
				}
			}(),
		},
		{
			entity: "users",
			write: func() func(int64) error {
				user, err := s.CreateUser(ctx, store.User{Email: "linus@example.test"})
				if err != nil {
					t.Fatalf("create user: %v", err)
				}
				return func(revision int64) error {
					user.Revision = revision
					_, err := s.UpdateUser(ctx, user)
					return err
				}
			}(),
		},
	}

	for _, tc := range cases {
		t.Run(tc.entity, func(t *testing.T) {
			if err := tc.write(1); err != nil {
				t.Fatalf("first write: %v", err)
			}

			err := tc.write(1)
			if err == nil {
				t.Fatal("a write at a stale revision was accepted")
			}
			if !errors.Is(err, store.ErrStaleRevision) {
				t.Fatalf("second write: %v, want ErrStaleRevision", err)
			}

			var stale *store.StaleRevisionError
			if !errors.As(err, &stale) {
				t.Fatalf("second write: %v, want a *StaleRevisionError", err)
			}
			if stale.Entity != tc.entity {
				t.Errorf("entity = %q, want %q", stale.Entity, tc.entity)
			}
			if stale.Revision != 1 {
				t.Errorf("reported revision = %d, want the caller's 1", stale.Revision)
			}
			// The current row is the payload the client reconciles against
			// instead of refetching the whole plan.
			if stale.Current == nil {
				t.Error("no current row carried by the conflict")
			}
		})
	}
}

// TestStaleRevisionCarriesTheWinningRow is the part the API's 409 body needs:
// not just "you lost", but what the row now says.
func TestStaleRevisionCarriesTheWinningRow(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	item, err := s.CreateBudgetItem(ctx, store.BudgetItem{
		Item: "Venue deposit", Unit: store.ToMinor("EUR", 250), Qty: 1,
	}, nil)
	if err != nil {
		t.Fatalf("create budget item: %v", err)
	}

	// Both people are holding revision 1.
	ada, grace := item, item

	ada.Unit = store.ToMinor("EUR", 300)
	if _, err := s.UpdateBudgetItem(ctx, ada, nil); err != nil {
		t.Fatalf("first write: %v", err)
	}

	grace.Unit = store.ToMinor("EUR", 275)
	_, err = s.UpdateBudgetItem(ctx, grace, nil)

	var stale *store.StaleRevisionError
	if !errors.As(err, &stale) {
		t.Fatalf("second write: %v, want a *StaleRevisionError", err)
	}
	current, ok := stale.Current.(store.BudgetItem)
	if !ok {
		t.Fatalf("current row is %T, want a store.BudgetItem", stale.Current)
	}
	if current.Unit != 30000 {
		t.Errorf("current unit = %d, want the first writer's 30000", current.Unit)
	}
	if current.Revision != 2 {
		t.Errorf("current revision = %d, want 2", current.Revision)
	}
	if stale.ID != item.ID {
		t.Errorf("conflict id = %s, want %s", stale.ID, item.ID)
	}

	// The loser's value must not have landed.
	reread, err := s.BudgetItem(ctx, item.ID)
	if err != nil {
		t.Fatalf("budget item: %v", err)
	}
	if reread.Unit != 30000 {
		t.Errorf("stored unit = %d, want 30000 — the refused write was applied anyway", reread.Unit)
	}
}

// TestStaleRevisionOnDelete: deletes carry the same check, or one person's
// delete silently discards another's edit.
func TestStaleRevisionOnDelete(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	task, err := s.CreateTask(ctx, store.Task{Name: "Book the photographer"}, nil)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	updated, err := s.UpdateTask(ctx, store.Task{
		ID: task.ID, Name: task.Name, Status: store.TaskDone, Revision: task.Revision,
	}, nil)
	if err != nil {
		t.Fatalf("update task: %v", err)
	}

	if err := s.DeleteTask(ctx, task.ID, task.Revision); !errors.Is(err, store.ErrStaleRevision) {
		t.Fatalf("delete at a stale revision: %v, want ErrStaleRevision", err)
	}
	if err := s.DeleteTask(ctx, task.ID, updated.Revision); err != nil {
		t.Fatalf("delete at the current revision: %v", err)
	}
}

// TestUpdateMissingRowIsNotFound: a row somebody else deleted is a different
// problem from a row somebody else edited, and the caller has to tell them
// apart — one is reconcilable, the other is not.
func TestUpdateMissingRowIsNotFound(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	missing := store.Note{ID: uuid.New(), Text: "Gone", Revision: 1}
	_, err := s.UpdateNote(ctx, missing)
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("update of a missing row: %v, want ErrNotFound", err)
	}
	if errors.Is(err, store.ErrStaleRevision) {
		t.Error("a missing row was reported as a revision conflict")
	}

	if err := s.DeleteNote(ctx, missing.ID, 1); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("delete of a missing row: %v, want ErrNotFound", err)
	}
}
