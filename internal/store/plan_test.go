package store_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/Yornik/soiree/internal/store"
)

// TestLoadPlanEmpty: a fresh deployment has nothing in it, and "nothing" is a
// plan with empty lists — not an error the page has to special-case.
func TestLoadPlanEmpty(t *testing.T) {
	s := newStore(t)

	plan, err := s.LoadPlan(t.Context())
	if err != nil {
		t.Fatalf("load plan: %v", err)
	}
	if plan.Settings.Revision != 1 {
		t.Errorf("settings revision = %d, want the seeded 1", plan.Settings.Revision)
	}
	if len(plan.Phases)+len(plan.Sponsors)+len(plan.BudgetItems)+
		len(plan.Programme)+len(plan.Tasks)+len(plan.Notes) != 0 {
		t.Errorf("a fresh plan is not empty: %+v", plan)
	}
}

func TestLoadPlan(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	settings, err := s.Settings(ctx)
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	settings.Ceiling = store.ToMinor("EUR", 10000)
	if _, err := s.UpdateSettings(ctx, settings); err != nil {
		t.Fatalf("update settings: %v", err)
	}

	arrival, err := s.CreatePhase(ctx, store.Phase{Name: "Arrival", Position: 0})
	if err != nil {
		t.Fatalf("create phase: %v", err)
	}
	if _, err := s.CreatePhase(ctx, store.Phase{Name: "Dinner", Position: 1}); err != nil {
		t.Fatalf("create phase: %v", err)
	}

	ada, err := s.CreateSponsor(ctx, store.Sponsor{Code: "Rose", Name: "Ada", Position: 0}, nil)
	if err != nil {
		t.Fatalf("create sponsor: %v", err)
	}
	grace, err := s.CreateSponsor(ctx, store.Sponsor{Code: "Ivy", Name: "Grace", Position: 1}, nil)
	if err != nil {
		t.Fatalf("create sponsor: %v", err)
	}

	shared, err := s.CreateBudgetItem(ctx, store.BudgetItem{
		PhaseID: &arrival.ID, Item: "Welcome signage", Vendor: "Local print shop",
		Unit: store.ToMinor("EUR", 5), Qty: 3, Position: 0,
		SponsorIDs: []uuid.UUID{ada.ID, grace.ID},
	}, nil)
	if err != nil {
		t.Fatalf("create budget item: %v", err)
	}
	if _, err := s.CreateBudgetItem(ctx, store.BudgetItem{
		Item: "Printed invitations", Unit: store.ToMinor("EUR", 4), Qty: 40, Position: 1,
	}, nil); err != nil {
		t.Fatalf("create budget item: %v", err)
	}

	for i, title := range []string{"Guests arrive", "Speeches", "Karaoke"} {
		if _, err := s.CreateProgrammeEntry(ctx, store.ProgrammeEntry{
			Title: title, Position: int32(i),
		}); err != nil {
			t.Fatalf("create programme entry: %v", err)
		}
	}
	if _, err := s.CreateTask(ctx, store.Task{Name: "Send invitations", Owner: "Grace"}, nil); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := s.CreateNote(ctx, store.Note{Text: "Check the parking."}); err != nil {
		t.Fatalf("create note: %v", err)
	}

	plan, err := s.LoadPlan(ctx)
	if err != nil {
		t.Fatalf("load plan: %v", err)
	}

	if plan.Settings.Ceiling != 1000000 || plan.Settings.Revision != 2 {
		t.Errorf("settings = %+v, want the updated ceiling at revision 2", plan.Settings)
	}
	counts := map[string]int{
		"phases": len(plan.Phases), "sponsors": len(plan.Sponsors),
		"budgetItems": len(plan.BudgetItems), "programme": len(plan.Programme),
		"tasks": len(plan.Tasks), "notes": len(plan.Notes),
	}
	for name, want := range map[string]int{
		"phases": 2, "sponsors": 2, "budgetItems": 2, "programme": 3, "tasks": 1, "notes": 1,
	} {
		if counts[name] != want {
			t.Errorf("%s = %d, want %d", name, counts[name], want)
		}
	}

	// The attributions have to arrive stitched onto their items, or the page
	// needs a second request to find out who is paying for what.
	var found bool
	for _, item := range plan.BudgetItems {
		if item.ID != shared.ID {
			continue
		}
		found = true
		if len(item.SponsorIDs) != 2 {
			t.Errorf("shared line has %v, want two sponsors", item.SponsorIDs)
		}
	}
	if !found {
		t.Error("the shared line is missing from the plan")
	}

	// Display order survives the round trip; array order would not have.
	if plan.Phases[0].Name != "Arrival" || plan.Phases[1].Name != "Dinner" {
		t.Errorf("phases out of order: %+v", plan.Phases)
	}
	if plan.Programme[0].Title != "Guests arrive" || plan.Programme[2].Title != "Karaoke" {
		t.Errorf("programme out of order: %+v", plan.Programme)
	}
}
