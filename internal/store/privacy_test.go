package store_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Yornik/soiree/internal/store"
)

// Everything below is synthetic. Ada and Grace at example.test, and a venue
// deposit: none of the real planning data this schema was drawn from belongs
// in a repository, least of all in a file about data protection.

// people is the fixture: two contributors, both with accounts, and a ledger
// arranged so that an erasure done wrongly moves a number.
type people struct {
	adaSponsor   store.Sponsor
	graceSponsor store.Sponsor
	adaUser      store.User
	graceUser    store.User

	venue      store.BudgetItem // Ada alone
	catering   store.BudgetItem // Ada + Grace, parent of dessert
	dessert    store.BudgetItem // Ada alone, child of catering
	flowers    store.BudgetItem // Grace alone
	photos     store.BudgetItem // nobody attributed; vendor is Ada herself
	invites    store.BudgetItem // nobody; the note says "Nevada"
	adaTask    store.Task
	graceTask  store.Task
	brotherJob store.Task
	adaNote    store.Note
	graceNote  store.Note
	nevadaNote store.Note
	speech     store.ProgrammeEntry
}

func plant(t *testing.T, s *store.Store) people {
	t.Helper()
	ctx := t.Context()

	var p people
	var err error

	if p.adaUser, err = s.CreateUser(ctx, store.User{Email: "ada@example.test", Role: store.RoleEditor}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	hash := "$argon2id$v=19$m=19456,t=2,p=1$c3ludGhldGlj$bm90YXJlYWxoYXNo"
	p.adaUser.PasswordHash = &hash
	p.adaUser.Status = store.StatusActive
	if p.adaUser, err = s.UpdateUser(ctx, p.adaUser); err != nil {
		t.Fatalf("update user: %v", err)
	}
	if err = s.SetUIPrefs(ctx, p.adaUser.ID, json.RawMessage(`{"colWidth":220}`)); err != nil {
		t.Fatalf("set prefs: %v", err)
	}
	if p.graceUser, err = s.CreateUser(ctx, store.User{Email: "grace@example.test", Role: store.RoleViewer}); err != nil {
		t.Fatalf("create user: %v", err)
	}

	if p.adaSponsor, err = s.CreateSponsor(ctx, store.Sponsor{Code: "Rose", Name: "Ada", Position: 0}, nil); err != nil {
		t.Fatalf("create sponsor: %v", err)
	}
	if p.graceSponsor, err = s.CreateSponsor(ctx, store.Sponsor{Code: "Ivy", Name: "Grace", Position: 1}, nil); err != nil {
		t.Fatalf("create sponsor: %v", err)
	}

	// The actor matters: `updated_by` is a record of what a person did, so a
	// row Ada last changed is data about Ada even when the line is nobody's
	// but the event's. The fixture therefore has each person editing their
	// own, as a real plan would.
	mk := func(actor *uuid.UUID, in store.BudgetItem) store.BudgetItem {
		t.Helper()
		out, err := s.CreateBudgetItem(ctx, in, actor)
		if err != nil {
			t.Fatalf("create budget item %q: %v", in.Item, err)
		}
		return out
	}

	p.venue = mk(&p.adaUser.ID, store.BudgetItem{
		Item: "Venue deposit", Vendor: "Example Hall",
		Unit: store.ToMinor("EUR", 2500), Qty: 1, Paid: store.ToMinor("EUR", 500),
		Note: "Balance due one month before", Position: 0,
		SponsorIDs: []uuid.UUID{p.adaSponsor.ID},
	})
	// Co-sponsored, and edited by the other sponsor: the line whose per-head
	// share moves if a sponsor is deleted rather than anonymised.
	p.catering = mk(&p.graceUser.ID, store.BudgetItem{
		Item: "Catering", Vendor: "Example Kitchen",
		Unit: store.ToMinor("EUR", 45), Qty: 40.5, Position: 1,
		SponsorIDs: []uuid.UUID{p.adaSponsor.ID, p.graceSponsor.ID},
	})
	// A child of a line the subject sponsors, under ON DELETE CASCADE: the
	// hazard an erasure that deletes budget rows would trip over.
	p.dessert = mk(&p.adaUser.ID, store.BudgetItem{
		ParentID: &p.catering.ID, Item: "Catering — dessert", Vendor: "Example Kitchen",
		Unit: store.ToMinor("EUR", 8), Qty: 40, Position: 2,
		SponsorIDs: []uuid.UUID{p.adaSponsor.ID},
	})
	p.flowers = mk(&p.graceUser.ID, store.BudgetItem{
		Item: "Flowers", Vendor: "Example Florist",
		Unit: store.ToMinor("EUR", 300), Qty: 1, Paid: store.ToMinor("EUR", 300), Position: 3,
		SponsorIDs: []uuid.UUID{p.graceSponsor.ID},
	})
	// The person as a vendor, on a line nobody has touched since: somebody's
	// sister doing the photography.
	p.photos = mk(nil, store.BudgetItem{
		Item: "Photographer", Vendor: "Ada",
		Unit: store.ToMinor("EUR", 900), Qty: 1, Position: 4,
	})
	// The trap: "Nevada" contains "ada" and must survive erasing Ada.
	p.invites = mk(&p.graceUser.ID, store.BudgetItem{
		Item: "Printed invitations", Vendor: "Example Print",
		Unit: store.ToMinor("EUR", 4), Qty: 40, Paid: store.ToMinor("EUR", 160),
		Note: "Nevada card stock, unassigned so far", Position: 5,
	})

	// The owner is padded, because a person typing into a grid cell does that
	// and a whole-field match has to survive it.
	if p.adaTask, err = s.CreateTask(ctx, store.Task{Name: "Confirm the caterer", Owner: " Ada ", Position: 0}, &p.adaUser.ID); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if p.graceTask, err = s.CreateTask(ctx, store.Task{Name: "Book the band", Owner: "Grace", Position: 1}, &p.graceUser.ID); err != nil {
		t.Fatalf("create task: %v", err)
	}
	// Owned by somebody else, but written down using her name.
	if p.brotherJob, err = s.CreateTask(ctx, store.Task{Name: "Chase the florist", Owner: "Ada's brother", Position: 2}, nil); err != nil {
		t.Fatalf("create task: %v", err)
	}

	if p.adaNote, err = s.CreateNote(ctx, store.Note{Text: "Ada is chasing the venue", Position: 0}); err != nil {
		t.Fatalf("create note: %v", err)
	}
	if p.graceNote, err = s.CreateNote(ctx, store.Note{Text: "Grace will confirm the flowers", Position: 1}); err != nil {
		t.Fatalf("create note: %v", err)
	}
	if p.nevadaNote, err = s.CreateNote(ctx, store.Note{Text: "Adamant about the Nevada card stock", Position: 2}); err != nil {
		t.Fatalf("create note: %v", err)
	}

	if p.speech, err = s.CreateProgrammeEntry(ctx, store.ProgrammeEntry{
		Title: "Speeches", Note: "Ada speaks first", Position: 0,
	}); err != nil {
		t.Fatalf("create programme entry: %v", err)
	}

	return p
}

func (p people) adaRef() store.SubjectRef {
	return store.SubjectRef{SponsorID: &p.adaSponsor.ID, UserID: &p.adaUser.ID}
}

// --- the books ---------------------------------------------------------------

// ledger is what must not move when a person is erased. Read as text so the
// numeric columns compare exactly rather than through a float.
type ledger struct {
	committedTopLevel string // sum(unit*qty) over the rows that roll up to nothing
	committedAll      string // sum(unit*qty) over every row, children included
	paid              string
	rows              int64
}

func readLedger(t *testing.T, ctx context.Context, s *store.Store) ledger {
	t.Helper()
	var l ledger
	err := s.Pool().QueryRow(ctx, `
		SELECT coalesce(sum(unit * qty) FILTER (WHERE parent_id IS NULL), 0)::text,
		       coalesce(sum(unit * qty), 0)::text,
		       coalesce(sum(paid), 0)::text,
		       count(*)
		  FROM budget_items`).
		Scan(&l.committedTopLevel, &l.committedAll, &l.paid, &l.rows)
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	return l
}

// amountsByItem is every line's money, so a change to one that another
// cancels out cannot hide behind the totals.
func amountsByItem(t *testing.T, ctx context.Context, s *store.Store) map[uuid.UUID]string {
	t.Helper()
	rows, err := s.Pool().Query(ctx,
		`SELECT id, unit::text || '/' || qty::text || '/' || paid::text FROM budget_items`)
	if err != nil {
		t.Fatalf("read amounts: %v", err)
	}
	defer rows.Close()

	out := map[uuid.UUID]string{}
	for rows.Next() {
		var id uuid.UUID
		var amounts string
		if err := rows.Scan(&id, &amounts); err != nil {
			t.Fatalf("scan amounts: %v", err)
		}
		out[id] = amounts
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read amounts: %v", err)
	}
	return out
}

func countRows(t *testing.T, ctx context.Context, s *store.Store, table string) int64 {
	t.Helper()
	var n int64
	if err := s.Pool().QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func matchedIDs[T any](in []store.Matched[T], id func(T) uuid.UUID) map[uuid.UUID][]string {
	out := map[uuid.UUID][]string{}
	for _, m := range in {
		out[id(m.Row)] = m.On
	}
	return out
}

// --- export ------------------------------------------------------------------

func TestExportSubjectFindsEveryRowThatNamesThem(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()
	p := plant(t, s)

	export, err := s.ExportSubject(ctx, p.adaRef())
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	if export.Sponsor == nil || export.Sponsor.Name != "Ada" {
		t.Fatalf("sponsor = %+v, want Ada", export.Sponsor)
	}
	if export.User == nil || export.User.Email != "ada@example.test" {
		t.Fatalf("user = %+v, want ada@example.test", export.User)
	}
	if !export.User.HasPassword {
		t.Error("the export does not disclose that a password is held")
	}
	if len(export.UIPrefs) == 0 {
		t.Error("ui preferences were not exported")
	}

	items := matchedIDs(export.BudgetItems, func(b store.BudgetItem) uuid.UUID { return b.ID })
	for _, want := range []struct {
		id     uuid.UUID
		name   string
		reason string
	}{
		{p.venue.ID, "Venue deposit", store.MatchSponsor},
		{p.catering.ID, "Catering", store.MatchSponsor},
		{p.dessert.ID, "Catering — dessert", store.MatchSponsor},
		{p.photos.ID, "Photographer", store.MatchVendor},
	} {
		on, ok := items[want.id]
		if !ok {
			t.Errorf("%s is missing from the export", want.name)
			continue
		}
		if !slices.Contains(on, want.reason) {
			t.Errorf("%s matched on %v, want %q among them", want.name, on, want.reason)
		}
	}
	// A row she last changed is a record of something she did, so it is hers
	// to see even though the line is the event's.
	if !slices.Contains(items[p.venue.ID], store.MatchUpdatedBy) {
		t.Errorf("Venue deposit matched on %v, want %q among them", items[p.venue.ID], store.MatchUpdatedBy)
	}

	// Grace's line, and a line whose note merely looks like it names Ada.
	if _, leaked := items[p.flowers.ID]; leaked {
		t.Error("Flowers is Grace's line and is in Ada's export")
	}
	if _, leaked := items[p.invites.ID]; leaked {
		t.Error(`"Nevada card stock" was read as naming Ada`)
	}

	tasks := matchedIDs(export.Tasks, func(tk store.Task) uuid.UUID { return tk.ID })
	if _, ok := tasks[p.adaTask.ID]; !ok {
		t.Error("the task Ada owns is missing from the export")
	}
	// Not hers, but her name is written in the cell, so it is data held about
	// her — and the erasure will strike it out, so the export must agree.
	if _, ok := tasks[p.brotherJob.ID]; !ok {
		t.Error(`the task owned by "Ada's brother" is missing from the export`)
	}
	if _, leaked := tasks[p.graceTask.ID]; leaked {
		t.Error("Grace's task is in Ada's export")
	}

	notes := matchedIDs(export.Notes, func(n store.Note) uuid.UUID { return n.ID })
	if _, ok := notes[p.adaNote.ID]; !ok {
		t.Error("the note naming Ada is missing from the export")
	}
	if _, leaked := notes[p.graceNote.ID]; leaked {
		t.Error("Grace's note is in Ada's export")
	}
	if _, leaked := notes[p.nevadaNote.ID]; leaked {
		t.Error(`"Adamant about the Nevada card stock" was read as naming Ada`)
	}

	if len(export.Programme) != 1 || export.Programme[0].Row.ID != p.speech.ID {
		t.Errorf("programme = %+v, want only the entry naming Ada", export.Programme)
	}

	if len(export.Caveats) == 0 {
		t.Error("the export claims completeness the schema cannot support")
	}
}

// TestExportSubjectSaysNothingAboutAnyoneElse checks the document itself
// rather than the structs, because that is what leaves the building.
func TestExportSubjectSaysNothingAboutAnyoneElse(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()
	p := plant(t, s)

	export, err := s.ExportSubject(ctx, p.adaRef())
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	doc, err := export.JSON()
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// The co-sponsored catering line is in there, and it carries Grace's
	// sponsor id — opaque, and needed to see the line is shared. What must not
	// be there is anything that says who that id is.
	for _, secret := range []string{"Grace", "grace@example.test", "Ivy", "Book the band", "argon2id"} {
		if bytes.Contains(doc, []byte(secret)) {
			t.Errorf("the export leaks %q", secret)
		}
	}
	if !bytes.Contains(doc, []byte(p.graceSponsor.ID.String())) {
		t.Error("the co-sponsor's id was stripped; the line no longer reads as shared")
	}
	// Grace last edited the catering line that Ada co-sponsors, so her account
	// id rides along on a row that is in the export for an unrelated reason.
	// That is an identifier for somebody else and has no business here.
	if bytes.Contains(doc, []byte(p.graceUser.ID.String())) {
		t.Error("the export leaks another person's account id through updated_by")
	}
}

// TestExportSubjectByNameAlone is the free-text path: no sponsor row, no
// account, just a name somebody typed. It is what the "owner is free text, not
// a foreign key" caveat is actually about, so it is worth proving both what it
// reaches and what it does not.
func TestExportSubjectByNameAlone(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()
	p := plant(t, s)

	export, err := s.ExportSubject(ctx, store.SubjectRef{Aliases: []string{"Ada"}})
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	if export.Sponsor != nil || export.User != nil {
		t.Errorf("sponsor = %+v, user = %+v, want neither without an id", export.Sponsor, export.User)
	}

	items := matchedIDs(export.BudgetItems, func(b store.BudgetItem) uuid.UUID { return b.ID })
	if _, ok := items[p.photos.ID]; !ok {
		t.Error("the line whose vendor is her name was not found by name alone")
	}
	// Her attributed lines are unreachable this way: budget_item_sponsors keys
	// on a sponsor id, and no query turns the string "Ada" into one.
	for _, missed := range []struct {
		id   uuid.UUID
		name string
	}{{p.venue.ID, "Venue deposit"}, {p.catering.ID, "Catering"}, {p.dessert.ID, "Catering — dessert"}} {
		if _, found := items[missed.id]; found {
			t.Errorf("%s was found without a sponsor id; the attribution is keyed, not textual", missed.name)
		}
	}

	tasks := matchedIDs(export.Tasks, func(tk store.Task) uuid.UUID { return tk.ID })
	if _, ok := tasks[p.adaTask.ID]; !ok {
		t.Error("the task she owns was not found by name alone")
	}
	if _, ok := tasks[p.brotherJob.ID]; !ok {
		t.Error(`the task owned by "Ada's brother" was not found by name alone`)
	}

	notes := matchedIDs(export.Notes, func(n store.Note) uuid.UUID { return n.ID })
	if _, ok := notes[p.adaNote.ID]; !ok {
		t.Error("the note naming her was not found by name alone")
	}
	if _, leaked := notes[p.nevadaNote.ID]; leaked {
		t.Error(`"Nevada" was read as naming Ada`)
	}

	// And it has to say out loud that it could not reach the keyed data,
	// rather than reporting a short list as a complete one.
	var saidSoAboutSponsor, saidSoAboutAccount bool
	for _, c := range export.Caveats {
		if strings.Contains(c, "no sponsor record was named") {
			saidSoAboutSponsor = true
		}
		if strings.Contains(c, "no account was named") {
			saidSoAboutAccount = true
		}
	}
	if !saidSoAboutSponsor || !saidSoAboutAccount {
		t.Errorf("caveats = %q, want them to admit the keyed records were never named", export.Caveats)
	}
}

// TestExportAdmitsTheChangeHistoryIsUnread pins the one caveat that has to
// keep pace with the schema. The change log records every field of every
// change, so a person's name is in it from the moment they are entered, and
// nothing in privacy.go reads that table. An export that reports the gap is
// incomplete; an export that denies the table exists is untrue, and it is
// untrue to the one person entitled to a straight answer.
func TestExportAdmitsTheChangeHistoryIsUnread(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()
	p := plant(t, s)

	// The evidence first, so the caveat below is measured against the schema
	// rather than against itself.
	entries, err := s.ChangeHistory(ctx, store.EntitySponsors, p.adaSponsor.ID, store.HistoryPage{})
	if err != nil {
		t.Fatalf("change history: %v", err)
	}
	var recorded bool
	for _, e := range entries {
		if string(e.Changes["name"].New) == `"Ada"` {
			recorded = true
		}
	}
	if !recorded {
		t.Fatalf("history = %+v, want the entry that records her name", entries)
	}

	export, err := s.ExportSubject(ctx, p.adaRef())
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	var admitted bool
	for _, c := range export.Caveats {
		if strings.Contains(c, "no per-row history") {
			t.Errorf("caveat = %q, but change_log holds her name", c)
		}
		if strings.Contains(c, "change history was not read") {
			admitted = true
		}
	}
	if !admitted {
		t.Errorf("caveats = %q, want one that says the change history was not read", export.Caveats)
	}
}

func TestExportAndEraseRefuseAnEmptySubject(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	if _, err := s.ExportSubject(ctx, store.SubjectRef{Aliases: []string{"  "}}); !errors.Is(err, store.ErrNoSubject) {
		t.Errorf("export: %v, want ErrNoSubject", err)
	}
	if _, err := s.EraseSubject(ctx, store.ErasureRequest{}); !errors.Is(err, store.ErrNoSubject) {
		t.Errorf("erase: %v, want ErrNoSubject", err)
	}
}

// --- erasure -----------------------------------------------------------------

// TestErasureLeavesTheBooksAlone is the whole point. Deleting a sponsor must
// not silently change what the event cost, in either mode.
func TestErasureLeavesTheBooksAlone(t *testing.T) {
	for _, mode := range []store.ErasureMode{store.EraseAnonymise, store.EraseDelete} {
		t.Run(string(mode), func(t *testing.T) {
			s := newStore(t)
			ctx := t.Context()
			p := plant(t, s)

			before := readLedger(t, ctx, s)
			amountsBefore := amountsByItem(t, ctx, s)

			res, err := s.EraseSubject(ctx, store.ErasureRequest{Subject: p.adaRef(), Mode: mode})
			if err != nil {
				t.Fatalf("erase: %v", err)
			}
			if res.BudgetItemsRetained == 0 {
				t.Error("no budget lines were reported as retained")
			}

			after := readLedger(t, ctx, s)
			if after.committedTopLevel != before.committedTopLevel {
				t.Errorf("committed total = %s, was %s", after.committedTopLevel, before.committedTopLevel)
			}
			if after.committedAll != before.committedAll {
				t.Errorf("committed total including children = %s, was %s", after.committedAll, before.committedAll)
			}
			if after.paid != before.paid {
				t.Errorf("paid = %s, was %s", after.paid, before.paid)
			}
			// The assertion that catches a cascade: the child of the line she
			// sponsored is still there.
			if after.rows != before.rows {
				t.Fatalf("%d budget lines, was %d — erasure took money with it", after.rows, before.rows)
			}

			amountsAfter := amountsByItem(t, ctx, s)
			for id, was := range amountsBefore {
				if now, ok := amountsAfter[id]; !ok || now != was {
					t.Errorf("line %s: amounts %q, were %q", id, now, was)
				}
			}
		})
	}
}

// TestAnonymisingKeepsTheSharedLineSplitTheSameWay is the reason anonymise is
// the default: the browser divides a line by the number of sponsors on it, so
// dropping one quietly raises what everybody else owes.
func TestAnonymisingKeepsTheSharedLineSplitTheSameWay(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()
	p := plant(t, s)

	if _, err := s.EraseSubject(ctx, store.ErasureRequest{Subject: p.adaRef()}); err != nil {
		t.Fatalf("erase: %v", err)
	}

	catering, err := s.BudgetItem(ctx, p.catering.ID)
	if err != nil {
		t.Fatalf("budget item: %v", err)
	}
	if len(catering.SponsorIDs) != 2 {
		t.Fatalf("the shared line now has %d sponsors, was 2: Grace's share just moved", len(catering.SponsorIDs))
	}
	if !slices.Contains(catering.SponsorIDs, p.adaSponsor.ID) {
		t.Error("the anonymised contributor lost their attribution")
	}

	// And the tombstone is still a distinct party, not a merge of everyone
	// erased so far.
	sponsor, err := s.Sponsor(ctx, p.adaSponsor.ID)
	if err != nil {
		t.Fatalf("sponsor: %v", err)
	}
	if sponsor.Code == "" || sponsor.Code == p.graceSponsor.Code {
		t.Errorf("tombstone code = %q, want something distinct", sponsor.Code)
	}
}

// TestDeletingDropsTheAttributionButNotTheLine documents the other half of the
// trade, so the cost of choosing EraseDelete is written down and tested.
func TestDeletingDropsTheAttributionButNotTheLine(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()
	p := plant(t, s)

	if _, err := s.EraseSubject(ctx, store.ErasureRequest{
		Subject: p.adaRef(), Mode: store.EraseDelete,
	}); err != nil {
		t.Fatalf("erase: %v", err)
	}

	if _, err := s.Sponsor(ctx, p.adaSponsor.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("sponsor: %v, want ErrNotFound", err)
	}
	if _, err := s.User(ctx, p.adaUser.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("user: %v, want ErrNotFound", err)
	}

	venue, err := s.BudgetItem(ctx, p.venue.ID)
	if err != nil {
		t.Fatalf("budget item: %v", err)
	}
	if len(venue.SponsorIDs) != 0 {
		t.Errorf("sponsors = %v, want the line left unattributed", venue.SponsorIDs)
	}
	if venue.Unit != p.venue.Unit || venue.Paid != p.venue.Paid {
		t.Errorf("amounts moved: unit %d paid %d, were %d and %d",
			venue.Unit, venue.Paid, p.venue.Unit, p.venue.Paid)
	}
	// Deleting the account nulls its attributions rather than deleting the
	// rows — the schema's ON DELETE SET NULL, and the reason anonymise is the
	// default for anybody whose edits are worth attributing.
	if venue.UpdatedBy != nil {
		t.Errorf("updatedBy = %v, want null after the account was deleted", venue.UpdatedBy)
	}
}

func TestErasureRemovesTheIdentifyingFields(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()
	p := plant(t, s)

	res, err := s.EraseSubject(ctx, store.ErasureRequest{Subject: p.adaRef()})
	if err != nil {
		t.Fatalf("erase: %v", err)
	}
	if !res.SponsorAnonymised || !res.AccountAnonymised || !res.UIPrefsDeleted {
		t.Errorf("result = %+v, want the sponsor and account anonymised and the prefs gone", res)
	}

	sponsor, err := s.Sponsor(ctx, p.adaSponsor.ID)
	if err != nil {
		t.Fatalf("sponsor: %v", err)
	}
	if sponsor.Name != "" || sponsor.Code == "Rose" {
		t.Errorf("sponsor = %+v, want no name and no original code", sponsor)
	}

	user, err := s.User(ctx, p.adaUser.ID)
	if err != nil {
		t.Fatalf("user: %v", err)
	}
	if user.Email == "ada@example.test" {
		t.Error("the address survived the erasure")
	}
	if user.PasswordHash != nil {
		t.Error("the password hash survived the erasure")
	}
	if user.Status != store.StatusDisabled {
		t.Errorf("status = %q, want %q", user.Status, store.StatusDisabled)
	}
	if _, err := s.UIPrefs(ctx, p.adaUser.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("ui prefs: %v, want ErrNotFound", err)
	}

	adaTask, err := s.Task(ctx, p.adaTask.ID)
	if err != nil {
		t.Fatalf("task: %v", err)
	}
	if adaTask.Owner != store.Tombstone {
		t.Errorf("owner = %q, want %q", adaTask.Owner, store.Tombstone)
	}
	brother, err := s.Task(ctx, p.brotherJob.ID)
	if err != nil {
		t.Fatalf("task: %v", err)
	}
	if brother.Owner != store.Tombstone+"'s brother" {
		t.Errorf("owner = %q, want her name struck out of it", brother.Owner)
	}

	photos, err := s.BudgetItem(ctx, p.photos.ID)
	if err != nil {
		t.Fatalf("budget item: %v", err)
	}
	if photos.Vendor != store.Tombstone {
		t.Errorf("vendor = %q, want %q", photos.Vendor, store.Tombstone)
	}

	adaNote, err := s.Note(ctx, p.adaNote.ID)
	if err != nil {
		t.Fatalf("note: %v", err)
	}
	if adaNote.Text != store.Tombstone+" is chasing the venue" {
		t.Errorf("note = %q, want her name struck out of it", adaNote.Text)
	}

	speech, err := s.ProgrammeEntry(ctx, p.speech.ID)
	if err != nil {
		t.Fatalf("programme entry: %v", err)
	}
	if speech.Note != store.Tombstone+" speaks first" {
		t.Errorf("programme note = %q, want her name struck out of it", speech.Note)
	}

	// Nothing about Ada is left anywhere in the plan.
	plan, err := s.LoadPlan(ctx)
	if err != nil {
		t.Fatalf("load plan: %v", err)
	}
	blob, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshal plan: %v", err)
	}
	for _, name := range []string{`"Ada"`, "Ada ", "Ada'", "ada@example.test", "Rose"} {
		if bytes.Contains(blob, []byte(name)) {
			t.Errorf("the plan still contains %q after erasure", name)
		}
	}
}

// TestErasureDoesNotMangleWordsThatMerelyContainTheName. Redacting a name out
// of prose by substring would rewrite "Nevada" and "Adamant", which is
// destroying a shared ledger in the name of privacy.
func TestErasureDoesNotMangleWordsThatMerelyContainTheName(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()
	p := plant(t, s)

	if _, err := s.EraseSubject(ctx, store.ErasureRequest{Subject: p.adaRef()}); err != nil {
		t.Fatalf("erase: %v", err)
	}

	nevada, err := s.Note(ctx, p.nevadaNote.ID)
	if err != nil {
		t.Fatalf("note: %v", err)
	}
	if nevada.Text != p.nevadaNote.Text {
		t.Errorf("note = %q, want %q untouched", nevada.Text, p.nevadaNote.Text)
	}
	if nevada.Revision != p.nevadaNote.Revision {
		t.Errorf("revision = %d, want %d — the row was rewritten with the same text",
			nevada.Revision, p.nevadaNote.Revision)
	}

	invites, err := s.BudgetItem(ctx, p.invites.ID)
	if err != nil {
		t.Fatalf("budget item: %v", err)
	}
	if invites.Note != p.invites.Note {
		t.Errorf("note = %q, want %q untouched", invites.Note, p.invites.Note)
	}
}

func TestErasureLeavesOtherPeopleAlone(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()
	p := plant(t, s)

	if _, err := s.EraseSubject(ctx, store.ErasureRequest{Subject: p.adaRef()}); err != nil {
		t.Fatalf("erase: %v", err)
	}

	grace, err := s.Sponsor(ctx, p.graceSponsor.ID)
	if err != nil {
		t.Fatalf("sponsor: %v", err)
	}
	if grace.Name != "Grace" || grace.Code != "Ivy" || grace.Revision != p.graceSponsor.Revision {
		t.Errorf("Grace = %+v, want her untouched at revision %d", grace, p.graceSponsor.Revision)
	}

	graceUser, err := s.User(ctx, p.graceUser.ID)
	if err != nil {
		t.Fatalf("user: %v", err)
	}
	if graceUser.Email != "grace@example.test" {
		t.Errorf("email = %q, want grace@example.test", graceUser.Email)
	}

	graceTask, err := s.Task(ctx, p.graceTask.ID)
	if err != nil {
		t.Fatalf("task: %v", err)
	}
	if graceTask.Owner != "Grace" || graceTask.Revision != p.graceTask.Revision {
		t.Errorf("task = %+v, want it untouched", graceTask)
	}

	graceNote, err := s.Note(ctx, p.graceNote.ID)
	if err != nil {
		t.Fatalf("note: %v", err)
	}
	if graceNote.Text != p.graceNote.Text {
		t.Errorf("note = %q, want %q", graceNote.Text, p.graceNote.Text)
	}
}

func TestErasureIsIdempotent(t *testing.T) {
	for _, mode := range []store.ErasureMode{store.EraseAnonymise, store.EraseDelete} {
		t.Run(string(mode), func(t *testing.T) {
			s := newStore(t)
			ctx := t.Context()
			p := plant(t, s)

			req := store.ErasureRequest{Subject: p.adaRef(), Mode: mode}
			if _, err := s.EraseSubject(ctx, req); err != nil {
				t.Fatalf("first erase: %v", err)
			}

			afterFirst, err := s.LoadPlan(ctx)
			if err != nil {
				t.Fatalf("load plan: %v", err)
			}
			first, err := json.Marshal(afterFirst)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}

			res, err := s.EraseSubject(ctx, req)
			if err != nil {
				t.Fatalf("second erase: %v", err)
			}
			// Nothing left to change, including no revision bumps: a repeated
			// erasure that rewrote rows would spray conflicts at every open
			// browser for no reason.
			if res.SponsorAnonymised || res.SponsorDeleted || res.AccountAnonymised || res.AccountDeleted ||
				res.UIPrefsDeleted || res.TaskOwnersRedacted != 0 || res.VendorsRedacted != 0 ||
				res.ProseRedacted != 0 || res.HistoryRedacted != 0 {
				t.Errorf("second erase changed something: %+v", res)
			}

			afterSecond, err := s.LoadPlan(ctx)
			if err != nil {
				t.Fatalf("load plan: %v", err)
			}
			second, err := json.Marshal(afterSecond)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if !bytes.Equal(first, second) {
				t.Error("the second erasure changed the plan")
			}
		})
	}
}

func TestErasureKeepsProseWhenAsked(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()
	p := plant(t, s)

	if _, err := s.EraseSubject(ctx, store.ErasureRequest{Subject: p.adaRef(), KeepProse: true}); err != nil {
		t.Fatalf("erase: %v", err)
	}

	note, err := s.Note(ctx, p.adaNote.ID)
	if err != nil {
		t.Fatalf("note: %v", err)
	}
	if note.Text != p.adaNote.Text {
		t.Errorf("note = %q, want %q left alone", note.Text, p.adaNote.Text)
	}
	// The keyed and whole-field data still goes: only the lossy part is opted
	// out of.
	task, err := s.Task(ctx, p.adaTask.ID)
	if err != nil {
		t.Fatalf("task: %v", err)
	}
	if task.Owner != store.Tombstone {
		t.Errorf("owner = %q, want %q", task.Owner, store.Tombstone)
	}
}

func TestErasingSomeoneWithNoAccount(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()
	p := plant(t, s)

	// Grace sponsors but has no linked account as far as the schema knows,
	// which is the common case: the sponsor was entered by somebody else.
	res, err := s.EraseSubject(ctx, store.ErasureRequest{
		Subject: store.SubjectRef{SponsorID: &p.graceSponsor.ID},
	})
	if err != nil {
		t.Fatalf("erase: %v", err)
	}
	if !res.SponsorAnonymised {
		t.Errorf("result = %+v, want the sponsor anonymised", res)
	}
	if res.AccountAnonymised || res.AccountDeleted {
		t.Error("an account was touched for a subject that named none")
	}
	// Her account, which the caller never named, is untouched — and the report
	// has to say so rather than imply the erasure was complete.
	user, err := s.User(ctx, p.graceUser.ID)
	if err != nil {
		t.Fatalf("user: %v", err)
	}
	if user.Email != "grace@example.test" {
		t.Errorf("email = %q, want the unnamed account left alone", user.Email)
	}
}

// --- retention ---------------------------------------------------------------

func TestPurgeEmptiesEverything(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()
	plant(t, s)

	res, err := s.PurgeEvent(ctx, store.PurgeOptions{IncludeAccounts: true})
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if res.BudgetItems == 0 || res.Sponsors == 0 || res.Users == 0 || !res.SettingsReset {
		t.Errorf("result = %+v, want it to report what it removed", res)
	}

	for _, table := range []string{
		"budget_item_sponsors", "programme_entries", "budget_items", "sponsors",
		"phases", "tasks", "notes", "user_ui_prefs", "users",
	} {
		if n := countRows(t, ctx, s, table); n != 0 {
			t.Errorf("%s still holds %d rows", table, n)
		}
	}

	// The settings singleton is reset, not deleted: its CHECK allows exactly
	// one row and the application refuses to run without it.
	settings, err := s.Settings(ctx)
	if err != nil {
		t.Fatalf("settings after purge: %v", err)
	}
	if settings.Ceiling != 0 || settings.InflationPct != 0 || settings.FxRate != 0 || settings.SplitEvenly {
		t.Errorf("settings = %+v, want them back at their defaults", settings)
	}

	plan, err := s.LoadPlan(ctx)
	if err != nil {
		t.Fatalf("load plan after purge: %v", err)
	}
	if len(plan.BudgetItems) != 0 || len(plan.Sponsors) != 0 || len(plan.Tasks) != 0 || len(plan.Notes) != 0 {
		t.Errorf("the plan still has rows after a purge: %+v", plan)
	}
	// Empty, but not untouched. A purged plan that read as one nobody had
	// filled in yet would be filled back in by the first browser still holding
	// a copy, most likely the admin's own, reloading to see that the purge
	// worked. That is the decision this function exists to carry out, undone
	// minutes later.
	if plan.Pristine {
		t.Error("a purged plan reads as one nobody has written to")
	}
}

func TestPurgeKeepsAccountsUnlessAsked(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()
	p := plant(t, s)

	if _, err := s.PurgeEvent(ctx, store.PurgeOptions{}); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if n := countRows(t, ctx, s, "budget_items"); n != 0 {
		t.Errorf("budget_items holds %d rows", n)
	}
	if _, err := s.User(ctx, p.adaUser.ID); err != nil {
		t.Errorf("the account was removed by a purge that did not ask for it: %v", err)
	}
	// The preferences belong to the account, so they stay with it.
	if _, err := s.UIPrefs(ctx, p.adaUser.ID); err != nil {
		t.Errorf("ui prefs: %v", err)
	}
}

// --- the change log ----------------------------------------------------------

// logHolds reports whether any entry in the change log still spells a word.
//
// Read as raw text and matched whole-word, case-sensitively, for the same
// reason the erasure itself matches that way: "ada@example.test" contains
// "ada" and so does "Nevada", and neither of them is the word being looked
// for.
func logHolds(t *testing.T, ctx context.Context, s *store.Store, word string) bool {
	t.Helper()
	re := regexp.MustCompile(`\b` + regexp.QuoteMeta(word) + `\b`)

	rows, err := s.Pool().Query(ctx, `SELECT changes::text FROM change_log ORDER BY id`)
	if err != nil {
		t.Fatalf("read change log: %v", err)
	}
	defer rows.Close()

	found := false
	for rows.Next() {
		var changes string
		if err := rows.Scan(&changes); err != nil {
			t.Fatalf("scan change log: %v", err)
		}
		if re.MatchString(changes) {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read change log: %v", err)
	}
	return found
}

// TestErasureStrikesTheNameOutOfTheChangeLog. The log records what every write
// altered, field by field, so it holds each value a row ever carried: the
// name, the address, the owner cell and the sentence. An erasure that rewrites
// the rows and leaves the log alone has moved the person's name rather than
// removed it, into the one table nothing else may edit.
func TestErasureStrikesTheNameOutOfTheChangeLog(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()
	p := plant(t, s)

	// The evidence first, so what follows is measured against a log that
	// demonstrably held her.
	for _, word := range []string{"Ada", "ada@example.test", "Rose"} {
		if !logHolds(t, ctx, s, word) {
			t.Fatalf("the change log does not hold %q to begin with", word)
		}
	}

	res, err := s.EraseSubject(ctx, store.ErasureRequest{Subject: p.adaRef()})
	if err != nil {
		t.Fatalf("erase: %v", err)
	}
	if res.HistoryRedacted == 0 {
		t.Errorf("result = %+v, want the entries it struck her out of", res)
	}

	for _, word := range []string{"Ada", "ada@example.test", "Rose"} {
		if logHolds(t, ctx, s, word) {
			t.Errorf("the change log still holds %q", word)
		}
	}

	// And what must survive it. The amounts are why erasure is allowed nowhere
	// near the money columns; the two words that merely contain her name are
	// why the log is matched with the same whole-word matcher the rows are;
	// and Grace was never the subject of any of this.
	for _, word := range []string{"250000", "Nevada", "Adamant", "Grace", "grace@example.test"} {
		if !logHolds(t, ctx, s, word) {
			t.Errorf("the change log no longer holds %q", word)
		}
	}
}

// TestTheActivityFeedStopsNamingAnErasedPerson. The feed takes each entry's
// label from the log rather than from the row, which is what lets an entry
// about a deleted line still say which line it was. It is also why an erasure
// that stops at the rows leaves an admin screen printing the erased name.
func TestTheActivityFeedStopsNamingAnErasedPerson(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()
	p := plant(t, s)

	if _, err := s.EraseSubject(ctx, store.ErasureRequest{Subject: p.adaRef()}); err != nil {
		t.Fatalf("erase: %v", err)
	}

	for _, feed := range []struct {
		entity string
		id     uuid.UUID
	}{
		{store.EntitySponsors, p.adaSponsor.ID},
		{store.EntityUsers, p.adaUser.ID},
	} {
		entries, err := s.Activity(ctx, store.ActivityFilter{Entity: feed.entity, EntityID: &feed.id}, 0, 50)
		if err != nil {
			t.Fatalf("activity: %v", err)
		}
		if len(entries) == 0 {
			t.Fatalf("%s has no entries to label", feed.entity)
		}
		for _, e := range entries {
			if e.Label == nil {
				t.Errorf("%s entry %d has no label at all", feed.entity, e.ID)
				continue
			}
			if *e.Label != store.Tombstone {
				t.Errorf("%s entry %d is labelled %q, want %q", feed.entity, e.ID, *e.Label, store.Tombstone)
			}
		}
	}
}

// TestPurgeStrikesTheNamesOutOfTheChangeLog. The retention purge empties the
// plan; the log holds a create entry for every row that was ever in it, so a
// purge that stops at the tables keeps a full copy of what it has just decided
// to destroy.
func TestPurgeStrikesTheNamesOutOfTheChangeLog(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()
	plant(t, s)

	res, err := s.PurgeEvent(ctx, store.PurgeOptions{IncludeAccounts: true})
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if res.ChangeLogRedacted == 0 {
		t.Errorf("result = %+v, want the entries it struck the plan out of", res)
	}

	for _, word := range []string{"Ada", "Grace", "Nevada", "Venue deposit", "Example Hall", "ada@example.test"} {
		if logHolds(t, ctx, s, word) {
			t.Errorf("the change log still holds %q after a purge", word)
		}
	}

	// The entries themselves stay, and have to: whether a plan has ever been
	// written to is read from this table, and a purged plan that answered "no"
	// would be filled back in by the first browser to reload.
	if n := countRows(t, ctx, s, "change_log"); n == 0 {
		t.Fatal("the purge emptied the change log")
	}
	plan, err := s.LoadPlan(ctx)
	if err != nil {
		t.Fatalf("load plan: %v", err)
	}
	if plan.Pristine {
		t.Error("a purged plan reads as one nobody has written to")
	}
}

// TestAPurgeThatKeepsTheAccountsKeepsTheirHistory. The log follows the purge's
// own scope: the plan goes, and an account that was not purged keeps the
// history of its own address, which is what tells a change of login address
// from a typo fix.
func TestAPurgeThatKeepsTheAccountsKeepsTheirHistory(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()
	plant(t, s)

	if _, err := s.PurgeEvent(ctx, store.PurgeOptions{}); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if logHolds(t, ctx, s, "Ada") {
		t.Error("the plan's names survived a purge")
	}
	if !logHolds(t, ctx, s, "ada@example.test") {
		t.Error("the account was kept and its history was struck out anyway")
	}
}
