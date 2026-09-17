package store_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Yornik/soiree/internal/migrate"
	"github.com/Yornik/soiree/internal/pgtest"
	"github.com/Yornik/soiree/internal/store"
)

// Every fixture in this file is obviously synthetic. The schema was drawn from
// real planning data; none of that data belongs in a repository.

func TestMain(m *testing.M) { pgtest.Main(m, startPostgres) }

func newStore(t *testing.T) *store.Store {
	t.Helper()
	pool := pgtest.Pool(t)
	if _, err := migrate.Run(t.Context(), pool, nil); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store.New(pool)
}

// date returns the UTC midnight that a `date` column round-trips to.
func date(t *testing.T, s string) *time.Time {
	t.Helper()
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		t.Fatalf("bad test date %q: %v", s, err)
	}
	return &d
}

func TestSettingsRoundTrip(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	got, err := s.Settings(ctx)
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	if got.Revision != 1 || got.Ceiling != 0 {
		t.Fatalf("seeded settings = %+v, want revision 1 and a zero ceiling", got)
	}

	got.Ceiling = store.ToMinor("EUR", 10000)
	got.InflationPct = 4.25
	got.FxRate = 17500.123456
	got.SplitEvenly = true

	updated, err := s.UpdateSettings(ctx, got)
	if err != nil {
		t.Fatalf("update settings: %v", err)
	}
	if updated.Revision != 2 {
		t.Errorf("revision = %d, want 2", updated.Revision)
	}

	reread, err := s.Settings(ctx)
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	if reread.Ceiling != 1000000 {
		t.Errorf("ceiling = %d, want 1000000 minor units", reread.Ceiling)
	}
	// The numerics are the ones a careless round trip mangles.
	if reread.InflationPct != 4.25 {
		t.Errorf("inflationPct = %v, want 4.25", reread.InflationPct)
	}
	if reread.FxRate != 17500.123456 {
		t.Errorf("fxRate = %v, want 17500.123456", reread.FxRate)
	}
	if !reread.SplitEvenly {
		t.Error("splitEvenly did not persist")
	}
}

func TestUserRoundTrip(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	created, err := s.CreateUser(ctx, store.User{Email: "ada@example.test", Role: store.RoleAdmin})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if created.ID == uuid.Nil {
		t.Error("no id generated")
	}
	// An admin creates the account; the person sets their own password later.
	if created.Status != store.StatusInvited {
		t.Errorf("status = %q, want %q", created.Status, store.StatusInvited)
	}
	if created.PasswordHash != nil {
		t.Error("a newly invited account must have no password")
	}

	// Case-insensitive, matching the unique index — otherwise the lookup finds
	// nothing where a second signup would also be refused.
	byEmail, err := s.UserByEmail(ctx, "Ada@Example.Test")
	if err != nil {
		t.Fatalf("lookup by email: %v", err)
	}
	if byEmail.ID != created.ID {
		t.Errorf("found %s, want %s", byEmail.ID, created.ID)
	}

	if _, err := s.CreateUser(ctx, store.User{Email: "ADA@example.test"}); err == nil {
		t.Error("a case variant of an existing address was accepted as a second account")
	}

	hash := "$argon2id$v=19$m=19456,t=2,p=1$c3ludGhldGlj$bm90YXJlYWxoYXNo"
	created.PasswordHash = &hash
	created.Status = store.StatusActive
	updated, err := s.UpdateUser(ctx, created)
	if err != nil {
		t.Fatalf("update user: %v", err)
	}
	if updated.Revision != 2 || updated.Status != store.StatusActive {
		t.Errorf("updated = %+v, want revision 2 and an active status", updated)
	}

	if _, err := s.CreateUser(ctx, store.User{Email: "grace@example.test", Role: "owner"}); err == nil {
		t.Error("an unknown role was accepted")
	}

	if err := s.DeleteUser(ctx, updated.ID, updated.Revision); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	if _, err := s.User(ctx, updated.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("after delete: %v, want ErrNotFound", err)
	}
}

func TestPhaseRoundTrip(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	arrival, err := s.CreatePhase(ctx, store.Phase{Name: "Arrival", Position: 0})
	if err != nil {
		t.Fatalf("create phase: %v", err)
	}
	if _, err := s.CreatePhase(ctx, store.Phase{Name: "Dinner", Position: 1}); err != nil {
		t.Fatalf("create phase: %v", err)
	}

	phases, err := s.Phases(ctx)
	if err != nil {
		t.Fatalf("phases: %v", err)
	}
	if len(phases) != 2 || phases[0].Name != "Arrival" || phases[1].Name != "Dinner" {
		t.Fatalf("phases = %+v, want Arrival then Dinner", phases)
	}

	arrival.Name = "Guests arrive"
	if _, err := s.UpdatePhase(ctx, arrival); err != nil {
		t.Fatalf("update phase: %v", err)
	}
	got, err := s.Phase(ctx, arrival.ID)
	if err != nil {
		t.Fatalf("phase: %v", err)
	}
	if got.Name != "Guests arrive" {
		t.Errorf("name = %q, want %q", got.Name, "Guests arrive")
	}

	if err := s.DeletePhase(ctx, arrival.ID); err != nil {
		t.Fatalf("delete phase: %v", err)
	}
	if err := s.DeletePhase(ctx, arrival.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("second delete: %v, want ErrNotFound", err)
	}
}

func TestSponsorRoundTrip(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	ada, err := s.CreateSponsor(ctx, store.Sponsor{Code: "Rose", Name: "Ada", Position: 0}, nil)
	if err != nil {
		t.Fatalf("create sponsor: %v", err)
	}
	if _, err := s.CreateSponsor(ctx, store.Sponsor{Code: "Ivy", Name: "Grace", Position: 1}, nil); err != nil {
		t.Fatalf("create sponsor: %v", err)
	}

	sponsors, err := s.Sponsors(ctx)
	if err != nil {
		t.Fatalf("sponsors: %v", err)
	}
	if len(sponsors) != 2 || sponsors[0].Name != "Ada" {
		t.Fatalf("sponsors = %+v, want Ada first", sponsors)
	}

	ada.Name = "Ada L."
	updated, err := s.UpdateSponsor(ctx, ada, nil)
	if err != nil {
		t.Fatalf("update sponsor: %v", err)
	}
	if updated.Name != "Ada L." || updated.Revision != 2 {
		t.Errorf("updated = %+v, want the new name at revision 2", updated)
	}

	if err := s.DeleteSponsor(ctx, updated.ID, updated.Revision); err != nil {
		t.Fatalf("delete sponsor: %v", err)
	}
	if _, err := s.Sponsor(ctx, updated.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("after delete: %v, want ErrNotFound", err)
	}
}

func TestBudgetItemRoundTrip(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	linus, err := s.CreateUser(ctx, store.User{Email: "linus@example.test", Role: store.RoleEditor})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	phase, err := s.CreatePhase(ctx, store.Phase{Name: "Arrival", Position: 0})
	if err != nil {
		t.Fatalf("create phase: %v", err)
	}
	ada, err := s.CreateSponsor(ctx, store.Sponsor{Code: "Rose", Name: "Ada"}, nil)
	if err != nil {
		t.Fatalf("create sponsor: %v", err)
	}
	grace, err := s.CreateSponsor(ctx, store.Sponsor{Code: "Ivy", Name: "Grace", Position: 1}, nil)
	if err != nil {
		t.Fatalf("create sponsor: %v", err)
	}

	in := store.BudgetItem{
		PhaseID:    &phase.ID,
		Item:       "Venue deposit",
		Vendor:     "Example Hall",
		Unit:       store.ToMinor("EUR", 250.50),
		Qty:        2.5,
		Paid:       store.ToMinor("EUR", 50),
		LockBy:     date(t, "2030-01-31"),
		Note:       "Balance due one month before",
		Position:   0,
		SponsorIDs: []uuid.UUID{ada.ID, grace.ID, ada.ID}, // the repeat is a caller's slip
	}
	created, err := s.CreateBudgetItem(ctx, in, &linus.ID)
	if err != nil {
		t.Fatalf("create budget item: %v", err)
	}

	got, err := s.BudgetItem(ctx, created.ID)
	if err != nil {
		t.Fatalf("budget item: %v", err)
	}
	if got.Item != in.Item || got.Vendor != in.Vendor || got.Note != in.Note {
		t.Errorf("text fields = %q/%q/%q", got.Item, got.Vendor, got.Note)
	}
	if got.Unit != 25050 || got.Paid != 5000 {
		t.Errorf("money = unit %d, paid %d, want 25050 and 5000 minor units", got.Unit, got.Paid)
	}
	if got.Qty != 2.5 {
		t.Errorf("qty = %v, want 2.5", got.Qty)
	}
	if got.LockBy == nil || got.LockBy.Format(time.DateOnly) != "2030-01-31" {
		t.Errorf("lockBy = %v, want 2030-01-31", got.LockBy)
	}
	if got.UpdatedBy == nil || *got.UpdatedBy != linus.ID {
		t.Errorf("updatedBy = %v, want %s", got.UpdatedBy, linus.ID)
	}
	if len(got.SponsorIDs) != 2 {
		t.Errorf("sponsors = %v, want two distinct ids", got.SponsorIDs)
	}
	// What a write returns and what the next read returns must agree.
	if len(created.SponsorIDs) != len(got.SponsorIDs) {
		t.Errorf("create returned %v, read returned %v", created.SponsorIDs, got.SponsorIDs)
	}

	// Dropping one sponsor from the line leaves the sponsor themselves alone.
	got.SponsorIDs = []uuid.UUID{grace.ID}
	got.Paid = store.ToMinor("EUR", 100)
	updated, err := s.UpdateBudgetItem(ctx, got, &linus.ID)
	if err != nil {
		t.Fatalf("update budget item: %v", err)
	}
	if updated.Revision != 2 || updated.Paid != 10000 {
		t.Errorf("updated = revision %d paid %d, want 2 and 10000", updated.Revision, updated.Paid)
	}
	reread, err := s.BudgetItem(ctx, created.ID)
	if err != nil {
		t.Fatalf("budget item: %v", err)
	}
	if len(reread.SponsorIDs) != 1 || reread.SponsorIDs[0] != grace.ID {
		t.Errorf("sponsors = %v, want only Grace", reread.SponsorIDs)
	}
	if _, err := s.Sponsor(ctx, ada.ID); err != nil {
		t.Errorf("removing an attribution deleted the sponsor: %v", err)
	}
}

func TestProgrammeRoundTrip(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	cake, err := s.CreateBudgetItem(ctx, store.BudgetItem{
		Item: "Cake", Unit: store.ToMinor("EUR", 60), Qty: 1, Position: 0,
	}, nil)
	if err != nil {
		t.Fatalf("create budget item: %v", err)
	}

	// Most of the evening costs nothing; that is the point of the table.
	for i, title := range []string{"Guests arrive", "Speeches", "Dinner", "Karaoke", "Closing"} {
		if _, err := s.CreateProgrammeEntry(ctx, store.ProgrammeEntry{
			Title: title, Position: int32(i),
		}); err != nil {
			t.Fatalf("create programme entry %q: %v", title, err)
		}
	}
	cutting, err := s.CreateProgrammeEntry(ctx, store.ProgrammeEntry{
		Title: "Cutting the cake", Note: "Lights down", Position: 5, BudgetItemID: &cake.ID,
	})
	if err != nil {
		t.Fatalf("create programme entry: %v", err)
	}

	programme, err := s.Programme(ctx)
	if err != nil {
		t.Fatalf("programme: %v", err)
	}
	if len(programme) != 6 {
		t.Fatalf("programme has %d entries, want 6", len(programme))
	}
	if programme[0].Title != "Guests arrive" || programme[5].Title != "Cutting the cake" {
		t.Errorf("programme out of order: %q first, %q last", programme[0].Title, programme[5].Title)
	}
	var withCost int
	for _, e := range programme {
		if e.BudgetItemID != nil {
			withCost++
		}
	}
	if withCost != 1 {
		t.Errorf("%d entries carry a cost, want 1", withCost)
	}

	// One cost belongs to at most one moment, or any roll-up double-counts it.
	if _, err := s.CreateProgrammeEntry(ctx, store.ProgrammeEntry{
		Title: "Second helping", Position: 6, BudgetItemID: &cake.ID,
	}); err == nil {
		t.Error("the same budget item was attached to two programme entries")
	}

	cutting.Note = "Lights down, music off"
	updated, err := s.UpdateProgrammeEntry(ctx, cutting)
	if err != nil {
		t.Fatalf("update programme entry: %v", err)
	}
	if updated.Revision != 2 || updated.Note != "Lights down, music off" {
		t.Errorf("updated = %+v", updated)
	}

	if err := s.DeleteProgrammeEntry(ctx, updated.ID, updated.Revision); err != nil {
		t.Fatalf("delete programme entry: %v", err)
	}
	// Dropping the moment must not drop what it cost.
	if _, err := s.BudgetItem(ctx, cake.ID); err != nil {
		t.Errorf("deleting a programme entry took its budget item with it: %v", err)
	}
}

func TestTaskAndNoteRoundTrip(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	task, err := s.CreateTask(ctx, store.Task{
		Name: "Confirm final guest count", Owner: "Ada", Due: date(t, "2030-01-15"), Position: 0,
	}, nil)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if task.Status != store.TaskNotStarted {
		t.Errorf("status = %q, want the default %q", task.Status, store.TaskNotStarted)
	}
	if task.Due == nil || task.Due.Format(time.DateOnly) != "2030-01-15" {
		t.Errorf("due = %v, want 2030-01-15", task.Due)
	}

	task.Status = store.TaskInProgress
	updated, err := s.UpdateTask(ctx, task, nil)
	if err != nil {
		t.Fatalf("update task: %v", err)
	}
	if updated.Status != store.TaskInProgress || updated.Revision != 2 {
		t.Errorf("updated = %+v", updated)
	}

	if _, err := s.CreateTask(ctx, store.Task{Name: "Invalid", Status: "maybe", Position: 1}, nil); err == nil {
		t.Error("an unknown task status was accepted")
	}

	tasks, err := s.Tasks(ctx)
	if err != nil {
		t.Fatalf("tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Errorf("tasks = %d, want 1", len(tasks))
	}
	if err := s.DeleteTask(ctx, updated.ID, updated.Revision); err != nil {
		t.Fatalf("delete task: %v", err)
	}

	note, err := s.CreateNote(ctx, store.Note{Text: "Venue balance is due a month out.", Position: 0})
	if err != nil {
		t.Fatalf("create note: %v", err)
	}
	note.Text = "Venue balance is due a month out — do not let this slip."
	if _, err := s.UpdateNote(ctx, note); err != nil {
		t.Fatalf("update note: %v", err)
	}
	notes, err := s.Notes(ctx)
	if err != nil {
		t.Fatalf("notes: %v", err)
	}
	if len(notes) != 1 || notes[0].Revision != 2 {
		t.Errorf("notes = %+v, want one note at revision 2", notes)
	}
	if err := s.DeleteNote(ctx, notes[0].ID, notes[0].Revision); err != nil {
		t.Fatalf("delete note: %v", err)
	}
}

func TestUIPrefsRoundTrip(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	grace, err := s.CreateUser(ctx, store.User{Email: "grace@example.test"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	if _, err := s.UIPrefs(ctx, grace.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("prefs before any were set: %v, want ErrNotFound", err)
	}

	prefs := json.RawMessage(`{"colWidths":[230,110,70],"rowHeights":{}}`)
	if err := s.SetUIPrefs(ctx, grace.ID, prefs); err != nil {
		t.Fatalf("set prefs: %v", err)
	}
	// Upsert: setting twice replaces rather than failing on the primary key.
	prefs = json.RawMessage(`{"colWidths":[240,110,70],"rowHeights":{}}`)
	if err := s.SetUIPrefs(ctx, grace.ID, prefs); err != nil {
		t.Fatalf("replace prefs: %v", err)
	}

	got, err := s.UIPrefs(ctx, grace.ID)
	if err != nil {
		t.Fatalf("prefs: %v", err)
	}
	var decoded struct {
		ColWidths []int `json:"colWidths"`
	}
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("prefs are not valid json: %v", err)
	}
	if len(decoded.ColWidths) != 3 || decoded.ColWidths[0] != 240 {
		t.Errorf("colWidths = %v, want the replacement", decoded.ColWidths)
	}
}
