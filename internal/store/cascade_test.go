package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Yornik/soiree/internal/store"
)

// Referential integrity is exactly what a mocked store would never catch: a
// fake happily accepts a cascade that does not exist.

// TestDeletingSponsorLeavesNoDanglingAttribution is the relational win over
// the JSON array the browser keeps today, where a deleted sponsor leaves ids
// behind that resolve to nobody.
func TestDeletingSponsorLeavesNoDanglingAttribution(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	ada, err := s.CreateSponsor(ctx, store.Sponsor{Code: "Rose", Name: "Ada"}, nil)
	if err != nil {
		t.Fatalf("create sponsor: %v", err)
	}
	grace, err := s.CreateSponsor(ctx, store.Sponsor{Code: "Ivy", Name: "Grace", Position: 1}, nil)
	if err != nil {
		t.Fatalf("create sponsor: %v", err)
	}
	item, err := s.CreateBudgetItem(ctx, store.BudgetItem{
		Item: "Catering", Unit: store.ToMinor("EUR", 45), Qty: 40,
		SponsorIDs: []uuid.UUID{ada.ID, grace.ID},
	}, nil)
	if err != nil {
		t.Fatalf("create budget item: %v", err)
	}

	if err := s.DeleteSponsor(ctx, ada.ID, ada.Revision); err != nil {
		t.Fatalf("delete sponsor: %v", err)
	}

	got, err := s.BudgetItem(ctx, item.ID)
	if err != nil {
		t.Fatalf("budget item: %v", err)
	}
	if len(got.SponsorIDs) != 1 || got.SponsorIDs[0] != grace.ID {
		t.Errorf("sponsors = %v, want only Grace", got.SponsorIDs)
	}

	// And nothing left in the join table pointing at a sponsor who is gone.
	var orphans int
	err = s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM budget_item_sponsors bis
		  WHERE NOT EXISTS (SELECT 1 FROM sponsors sp WHERE sp.id = bis.sponsor_id)`,
	).Scan(&orphans)
	if err != nil {
		t.Fatalf("count orphans: %v", err)
	}
	if orphans != 0 {
		t.Errorf("%d attributions point at a deleted sponsor", orphans)
	}
}

// TestDeletingParentRemovesChildren: a component of a quote that outlived the
// quote would start counting towards the total on its own.
func TestDeletingParentRemovesChildren(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	parent, err := s.CreateBudgetItem(ctx, store.BudgetItem{
		Item: "Catering", Unit: store.ToMinor("EUR", 45), Qty: 40,
	}, nil)
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}

	var children []uuid.UUID
	for i, dish := range []string{"Starter", "Main", "Dessert"} {
		child, err := s.CreateBudgetItem(ctx, store.BudgetItem{
			ParentID: &parent.ID, Item: dish, Unit: store.ToMinor("EUR", 15), Qty: 40,
			Position: int32(i),
		}, nil)
		if err != nil {
			t.Fatalf("create child %q: %v", dish, err)
		}
		children = append(children, child.ID)
	}

	found, err := s.BudgetItemChildren(ctx, parent.ID)
	if err != nil {
		t.Fatalf("children: %v", err)
	}
	if len(found) != 3 {
		t.Fatalf("children = %d, want 3", len(found))
	}

	if err := s.DeleteBudgetItem(ctx, parent.ID, parent.Revision); err != nil {
		t.Fatalf("delete parent: %v", err)
	}
	for _, id := range children {
		if _, err := s.BudgetItem(ctx, id); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("child %s survived its parent: %v", id, err)
		}
	}
}

// TestSelfParentIsRefused: a row that is its own parent is an infinite roll-up.
func TestSelfParentIsRefused(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	item, err := s.CreateBudgetItem(ctx, store.BudgetItem{Item: "Flowers", Qty: 1}, nil)
	if err != nil {
		t.Fatalf("create budget item: %v", err)
	}
	item.ParentID = &item.ID
	if _, err := s.UpdateBudgetItem(ctx, item, nil); err == nil {
		t.Error("a budget item was allowed to be its own parent")
	}
}

// TestDeletingALineInsideAParentCycle: the schema refuses only a line that is
// its own parent, so two lines can be made each other's parent through the
// ordinary write path. Whatever the rows say, collecting a line's subtree has
// to finish: a walk that does not holds a connection and grows its list of ids
// for as long as the caller waits, and the lines can never be deleted.
func TestDeletingALineInsideAParentCycle(t *testing.T) {
	for _, length := range []int{2, 3} {
		t.Run(fmt.Sprintf("%d lines", length), func(t *testing.T) {
			s := newStore(t)
			ctx := t.Context()

			items := make([]store.BudgetItem, length)
			for i := range items {
				item, err := s.CreateBudgetItem(ctx, store.BudgetItem{
					Item: fmt.Sprintf("Course %d", i+1), Unit: store.ToMinor("EUR", 15),
					Qty: 40, Position: int32(i),
				}, nil)
				if err != nil {
					t.Fatalf("create line %d: %v", i, err)
				}
				items[i] = item
			}

			// Nothing refuses this today: every line becomes the child of the
			// next, and the last one closes the ring.
			for i := range items {
				items[i].ParentID = &items[(i+1)%length].ID
				updated, err := s.UpdateBudgetItem(ctx, items[i], nil)
				if err != nil {
					t.Fatalf("make line %d the child of line %d: %v", i, (i+1)%length, err)
				}
				items[i] = updated
			}

			// A bound on "never returns", not a timing assertion: the delete is
			// a handful of statements against a database on this machine.
			deleting, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			if err := s.DeleteBudgetItem(deleting, items[0].ID, items[0].Revision); err != nil {
				t.Fatalf("deleting a line in a cycle of %d did not finish: %v", length, err)
			}

			for i, item := range items {
				if _, err := s.BudgetItem(ctx, item.ID); !errors.Is(err, store.ErrNotFound) {
					t.Errorf("line %d survived the delete: %v", i, err)
				}
				// The cascade took it, so only this layer can say what it cost.
				entries := history(t, s, store.EntityBudgetItems, item.ID)
				if len(entries) == 0 {
					t.Errorf("line %d has no history at all", i)
					continue
				}
				if got := entries[0].Action; got != store.ChangeDelete {
					t.Errorf("line %d: newest action = %q, want %q", i, got, store.ChangeDelete)
				}
			}
		})
	}
}

// TestDeletingBudgetItemKeepsProgrammeEntry: dropping the cake from the budget
// does not drop the cake from the evening.
func TestDeletingBudgetItemKeepsProgrammeEntry(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	cake, err := s.CreateBudgetItem(ctx, store.BudgetItem{
		Item: "Cake", Unit: store.ToMinor("EUR", 60), Qty: 1,
	}, nil)
	if err != nil {
		t.Fatalf("create budget item: %v", err)
	}
	entry, err := s.CreateProgrammeEntry(ctx, store.ProgrammeEntry{
		Title: "Cutting the cake", Position: 3, BudgetItemID: &cake.ID,
	})
	if err != nil {
		t.Fatalf("create programme entry: %v", err)
	}

	if err := s.DeleteBudgetItem(ctx, cake.ID, cake.Revision); err != nil {
		t.Fatalf("delete budget item: %v", err)
	}

	got, err := s.ProgrammeEntry(ctx, entry.ID)
	if err != nil {
		t.Fatalf("the programme entry went with its budget item: %v", err)
	}
	if got.BudgetItemID != nil {
		t.Errorf("budgetItemID = %v, want nil after the item was deleted", got.BudgetItemID)
	}
}

// TestDeletingPhaseKeepsItsItems: dropping a stage of the evening must not
// silently drop what it was going to cost.
func TestDeletingPhaseKeepsItsItems(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	phase, err := s.CreatePhase(ctx, store.Phase{Name: "Dinner"}, nil)
	if err != nil {
		t.Fatalf("create phase: %v", err)
	}
	item, err := s.CreateBudgetItem(ctx, store.BudgetItem{
		PhaseID: &phase.ID, Item: "Catering", Qty: 40,
	}, nil)
	if err != nil {
		t.Fatalf("create budget item: %v", err)
	}

	if err := s.DeletePhase(ctx, phase.ID, phase.Revision); err != nil {
		t.Fatalf("delete phase: %v", err)
	}

	got, err := s.BudgetItem(ctx, item.ID)
	if err != nil {
		t.Fatalf("the budget item went with its phase: %v", err)
	}
	if got.PhaseID != nil {
		t.Errorf("phaseID = %v, want nil after the phase was deleted", got.PhaseID)
	}
}

// TestDeletingUserKeepsTheirWork: an account is removed, the plan they edited
// is not. Attribution degrades to "someone"; their private layout goes.
func TestDeletingUserKeepsTheirWork(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	linus, err := s.CreateUser(ctx, store.User{Email: "linus@example.test", Role: store.RoleEditor})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := s.SetUIPrefs(ctx, linus.ID, json.RawMessage(`{"colWidths":[230]}`)); err != nil {
		t.Fatalf("set prefs: %v", err)
	}
	item, err := s.CreateBudgetItem(ctx, store.BudgetItem{Item: "Photographer", Qty: 1}, &linus.ID)
	if err != nil {
		t.Fatalf("create budget item: %v", err)
	}

	if err := s.DeleteUser(ctx, linus.ID, linus.Revision); err != nil {
		t.Fatalf("delete user: %v", err)
	}

	got, err := s.BudgetItem(ctx, item.ID)
	if err != nil {
		t.Fatalf("the budget item went with its author: %v", err)
	}
	if got.UpdatedBy != nil {
		t.Errorf("updatedBy = %v, want nil after the account was deleted", got.UpdatedBy)
	}
	if _, err := s.UIPrefs(ctx, linus.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("prefs survived their owner: %v", err)
	}
}
