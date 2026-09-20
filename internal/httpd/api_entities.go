package httpd

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/Yornik/soiree/internal/store"
)

// One descriptor per table. Everything table-specific lives here; the verbs
// themselves are the generic handlers in api.go, which is what keeps six
// collections from becoming eighteen near-identical handlers that drift.
//
// The actor argument is nil throughout, which is not the same as nobody: the
// session's account travels in the context, where withActor puts it, and the
// store reads it from there for `updated_by` and the change log alike. Naming
// it a second time here could only ever disagree with the session.
//
// Every constructor takes the deployment's currency, including the four whose
// tables hold no money. Decoding money needs it — major units on the wire,
// minor in the column, and the exponent comes from the code — and a decoder
// that takes it only where it is needed today is a decoder that grows a second
// signature the first time a money column lands on another table.

// entity wires one table's three write verbs onto the generic handlers.
type entity[T any] struct {
	// collection is the URL segment and, unchanged, the metrics route label.
	// Both are fixed strings, which is what keeps the label set bounded.
	collection string

	// blank is the row a create starts from. It is not always the zero value:
	// the store sends every column explicitly, so a column default the caller
	// omitted (budget qty, which defaults to 1) would otherwise be written as
	// a zero — and a line priced at zero quantity totals nothing.
	blank T

	// revisioned says the table carries a revision, so its writes name the one
	// the caller last saw and can be refused with a 409. Every collection is,
	// since migration 0009 closed the gap phases used to sit in; the field
	// stays because a collection that is not would be a silent last-write-wins
	// surface, and that should have to be written down.
	revisioned bool

	// decode merges a request body onto a row: blank for a create, the stored
	// row for a patch, so an omitted field keeps what is already there.
	decode func(base T, body []byte) (T, error)

	// key stamps the URL's id and the caller's revision onto the row about to
	// be written. The caller's revision, not the one just read — that is the
	// whole conflict check.
	key func(row *T, id uuid.UUID, revision int64)

	load   func(context.Context, uuid.UUID) (T, error)
	create func(context.Context, T) (T, error)
	update func(context.Context, T) (T, error)
	remove func(ctx context.Context, id uuid.UUID, revision int64) error

	encode func(T) any
}

// --- budget items ------------------------------------------------------

type budgetItemBody struct {
	echoed

	PhaseID  optional[uuid.UUID]   `json:"phaseId"`
	ParentID optional[uuid.UUID]   `json:"parentId"`
	Item     optional[string]      `json:"item"`
	Vendor   optional[string]      `json:"vendor"`
	Unit     optional[moneyText]   `json:"unit"`
	Qty      optional[float64]     `json:"qty"`
	Paid     optional[moneyText]   `json:"paid"`
	LockBy   optional[civilDate]   `json:"lockBy"`
	Note     optional[string]      `json:"note"`
	Position optional[int32]       `json:"position"`
	Sponsors optional[[]uuid.UUID] `json:"sponsors"`
}

func (b budgetItemBody) apply(currency string, row store.BudgetItem) (store.BudgetItem, error) {
	var f fieldErrs
	setPtr(b.PhaseID, &row.PhaseID)
	setPtr(b.ParentID, &row.ParentID)
	setValue(&f, "item", b.Item, &row.Item)
	setValue(&f, "vendor", b.Vendor, &row.Vendor)
	setMoney(&f, "unit", currency, b.Unit, &row.Unit)
	setValue(&f, "qty", b.Qty, &row.Qty)
	setMoney(&f, "paid", currency, b.Paid, &row.Paid)
	setDate(b.LockBy, &row.LockBy)
	setValue(&f, "note", b.Note, &row.Note)
	setValue(&f, "position", b.Position, &row.Position)
	// A null list is read as "nobody", which is the only sensible reading and
	// spares the client an empty-array special case.
	if b.Sponsors.set {
		if b.Sponsors.value == nil {
			row.SponsorIDs = nil
		} else {
			row.SponsorIDs = *b.Sponsors.value
		}
	}
	return row, f.err
}

func budgetItemEntity(s *store.Store, currency string) entity[store.BudgetItem] {
	return entity[store.BudgetItem]{
		collection: "budget-items",
		// See entity.blank: the column defaults to 1 but the store always
		// sends the value it was given.
		blank:      store.BudgetItem{Qty: 1},
		revisioned: true,
		decode: func(base store.BudgetItem, body []byte) (store.BudgetItem, error) {
			return decodeBody[budgetItemBody, store.BudgetItem](body, currency, base)
		},
		key: func(row *store.BudgetItem, id uuid.UUID, revision int64) {
			row.ID, row.Revision = id, revision
		},
		// The read behind a PATCH must be this one rather than a hand-built
		// struct: it is the only read that populates SponsorIDs, and
		// UpdateBudgetItem replaces the attributions with exactly what it is
		// given. A patch of one field would otherwise clear every sponsor on
		// the line.
		load: s.BudgetItem,
		create: func(ctx context.Context, in store.BudgetItem) (store.BudgetItem, error) {
			return s.CreateBudgetItem(ctx, in, nil)
		},
		update: func(ctx context.Context, in store.BudgetItem) (store.BudgetItem, error) {
			return s.UpdateBudgetItem(ctx, in, nil)
		},
		remove: s.DeleteBudgetItem,
		encode: func(b store.BudgetItem) any { return encodeBudgetItem(currency, b) },
	}
}

// --- sponsors ----------------------------------------------------------

type sponsorBody struct {
	echoed

	Code     optional[string] `json:"code"`
	Name     optional[string] `json:"name"`
	Position optional[int32]  `json:"position"`
}

func (b sponsorBody) apply(_ string, row store.Sponsor) (store.Sponsor, error) {
	var f fieldErrs
	setValue(&f, "code", b.Code, &row.Code)
	setValue(&f, "name", b.Name, &row.Name)
	setValue(&f, "position", b.Position, &row.Position)
	return row, f.err
}

func sponsorEntity(s *store.Store, currency string) entity[store.Sponsor] {
	return entity[store.Sponsor]{
		collection: "sponsors",
		revisioned: true,
		decode: func(base store.Sponsor, body []byte) (store.Sponsor, error) {
			return decodeBody[sponsorBody, store.Sponsor](body, currency, base)
		},
		key: func(row *store.Sponsor, id uuid.UUID, revision int64) {
			row.ID, row.Revision = id, revision
		},
		load: s.Sponsor,
		create: func(ctx context.Context, in store.Sponsor) (store.Sponsor, error) {
			return s.CreateSponsor(ctx, in, nil)
		},
		update: func(ctx context.Context, in store.Sponsor) (store.Sponsor, error) {
			return s.UpdateSponsor(ctx, in, nil)
		},
		remove: s.DeleteSponsor,
		encode: asAny(encodeSponsor),
	}
}

// --- tasks -------------------------------------------------------------

type taskBody struct {
	echoed

	Name     optional[string]    `json:"name"`
	Owner    optional[string]    `json:"owner"`
	Due      optional[civilDate] `json:"due"`
	Status   optional[string]    `json:"status"`
	Position optional[int32]     `json:"position"`
}

func (b taskBody) apply(_ string, row store.Task) (store.Task, error) {
	var f fieldErrs
	setValue(&f, "name", b.Name, &row.Name)
	setValue(&f, "owner", b.Owner, &row.Owner)
	setDate(b.Due, &row.Due)
	setValue(&f, "position", b.Position, &row.Position)

	// Checked here as well as by the column's CHECK constraint, so a typo is a
	// 400 naming the three values rather than a 500 carrying a Postgres error.
	if b.Status.set {
		if b.Status.value == nil {
			f.fail("status", "must not be null")
		} else {
			switch status := store.TaskStatus(*b.Status.value); status {
			case store.TaskNotStarted, store.TaskInProgress, store.TaskDone:
				row.Status = status
			default:
				f.fail("status", fmt.Sprintf("must be one of %q, %q or %q",
					store.TaskNotStarted, store.TaskInProgress, store.TaskDone))
			}
		}
	}
	return row, f.err
}

func taskEntity(s *store.Store, currency string) entity[store.Task] {
	return entity[store.Task]{
		collection: "tasks",
		revisioned: true,
		decode: func(base store.Task, body []byte) (store.Task, error) {
			return decodeBody[taskBody, store.Task](body, currency, base)
		},
		key: func(row *store.Task, id uuid.UUID, revision int64) {
			row.ID, row.Revision = id, revision
		},
		load:   s.Task,
		create: func(ctx context.Context, in store.Task) (store.Task, error) { return s.CreateTask(ctx, in, nil) },
		update: func(ctx context.Context, in store.Task) (store.Task, error) { return s.UpdateTask(ctx, in, nil) },
		remove: s.DeleteTask,
		encode: asAny(encodeTask),
	}
}

// --- notes -------------------------------------------------------------

type noteBody struct {
	echoed

	Text     optional[string] `json:"text"`
	Position optional[int32]  `json:"position"`
}

func (b noteBody) apply(_ string, row store.Note) (store.Note, error) {
	var f fieldErrs
	setValue(&f, "text", b.Text, &row.Text)
	setValue(&f, "position", b.Position, &row.Position)
	return row, f.err
}

func noteEntity(s *store.Store, currency string) entity[store.Note] {
	return entity[store.Note]{
		collection: "notes",
		revisioned: true,
		decode: func(base store.Note, body []byte) (store.Note, error) {
			return decodeBody[noteBody, store.Note](body, currency, base)
		},
		key: func(row *store.Note, id uuid.UUID, revision int64) {
			row.ID, row.Revision = id, revision
		},
		load:   s.Note,
		create: s.CreateNote,
		update: s.UpdateNote,
		remove: s.DeleteNote,
		encode: asAny(encodeNote),
	}
}

// --- phases ------------------------------------------------------------

type phaseBody struct {
	echoed

	Name     optional[string] `json:"name"`
	Position optional[int32]  `json:"position"`
}

func (b phaseBody) apply(_ string, row store.Phase) (store.Phase, error) {
	var f fieldErrs
	setValue(&f, "name", b.Name, &row.Name)
	setValue(&f, "position", b.Position, &row.Position)
	return row, f.err
}

// phaseEntity used to be the exception here: `phases` carried no revision, so
// its writes were last-write-wins and could never come back as a 409. Migration
// 0009 gave it the same three columns as every other shared table, and this
// descriptor is now the same shape as the rest — a patch names the revision it
// is editing, a delete carries it as ?revision=N.
func phaseEntity(s *store.Store, currency string) entity[store.Phase] {
	return entity[store.Phase]{
		collection: "phases",
		revisioned: true,
		decode: func(base store.Phase, body []byte) (store.Phase, error) {
			return decodeBody[phaseBody, store.Phase](body, currency, base)
		},
		key: func(row *store.Phase, id uuid.UUID, revision int64) {
			row.ID, row.Revision = id, revision
		},
		load: s.Phase,
		create: func(ctx context.Context, in store.Phase) (store.Phase, error) {
			return s.CreatePhase(ctx, in, nil)
		},
		update: func(ctx context.Context, in store.Phase) (store.Phase, error) {
			return s.UpdatePhase(ctx, in, nil)
		},
		remove: s.DeletePhase,
		encode: asAny(encodePhase),
	}
}

// --- programme entries -------------------------------------------------

type programmeBody struct {
	echoed

	Title        optional[string]    `json:"title"`
	Note         optional[string]    `json:"note"`
	Position     optional[int32]     `json:"position"`
	BudgetItemID optional[uuid.UUID] `json:"budgetItemId"`
}

func (b programmeBody) apply(_ string, row store.ProgrammeEntry) (store.ProgrammeEntry, error) {
	var f fieldErrs
	setValue(&f, "title", b.Title, &row.Title)
	setValue(&f, "note", b.Note, &row.Note)
	setValue(&f, "position", b.Position, &row.Position)
	setPtr(b.BudgetItemID, &row.BudgetItemID)
	return row, f.err
}

func programmeEntity(s *store.Store, currency string) entity[store.ProgrammeEntry] {
	return entity[store.ProgrammeEntry]{
		collection: "programme-entries",
		revisioned: true,
		decode: func(base store.ProgrammeEntry, body []byte) (store.ProgrammeEntry, error) {
			return decodeBody[programmeBody, store.ProgrammeEntry](body, currency, base)
		},
		key: func(row *store.ProgrammeEntry, id uuid.UUID, revision int64) {
			row.ID, row.Revision = id, revision
		},
		load:   s.ProgrammeEntry,
		create: s.CreateProgrammeEntry,
		update: s.UpdateProgrammeEntry,
		remove: s.DeleteProgrammeEntry,
		encode: asAny(encodeProgrammeEntry),
	}
}
