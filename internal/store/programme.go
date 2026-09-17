package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ProgrammeEntry is one moment in the run of show: guests arrive, speeches,
// cake, dinner, karaoke, closing.
//
// Most of these cost nothing — they are there so everyone knows what happens
// when — which is exactly why they are not budget items with a zero price. A
// timeline modelled as budget lines fills the ledger with empty rows and makes
// the item count meaningless. BudgetItemID attaches a cost to a moment for the
// few entries that have one.
type ProgrammeEntry struct {
	ID       uuid.UUID `db:"id"`
	Title    string    `db:"title"`
	Note     string    `db:"note"`
	Position int32     `db:"position"`
	// Optional in both directions: most entries have no cost, and plenty of
	// budget items never appear on stage.
	BudgetItemID *uuid.UUID `db:"budget_item_id"`
	Revision     int64      `db:"revision"`
	UpdatedAt    time.Time  `db:"updated_at"`
}

const programmeColumns = `id, title, note, position, budget_item_id, revision, updated_at`

// CreateProgrammeEntry inserts a moment in the evening.
func (s *Store) CreateProgrammeEntry(ctx context.Context, in ProgrammeEntry) (ProgrammeEntry, error) {
	actor := resolveActor(ctx, nil)
	return createAudited(ctx, s, EntityProgrammeEntries, actor, func(tx pgx.Tx) (ProgrammeEntry, error) {
		return queryOne[ProgrammeEntry](ctx, tx, "programme_entries",
			`INSERT INTO programme_entries (id, title, note, position, budget_item_id)
			 VALUES (COALESCE($1, gen_random_uuid()), $2, $3, $4, $5)
			 RETURNING `+programmeColumns,
			newID(in.ID), in.Title, in.Note, in.Position, in.BudgetItemID)
	})
}

// ProgrammeEntry reads one entry.
func (s *Store) ProgrammeEntry(ctx context.Context, id uuid.UUID) (ProgrammeEntry, error) {
	return queryOne[ProgrammeEntry](ctx, s.pool, "programme_entries",
		`SELECT `+programmeColumns+` FROM programme_entries WHERE id = $1`, id)
}

// Programme lists the evening in order.
func (s *Store) Programme(ctx context.Context) ([]ProgrammeEntry, error) {
	return queryAll[ProgrammeEntry](ctx, s.pool, "programme_entries",
		`SELECT `+programmeColumns+` FROM programme_entries ORDER BY position, id`)
}

// UpdateProgrammeEntry writes every mutable field, refusing the write if
// in.Revision is no longer current.
func (s *Store) UpdateProgrammeEntry(ctx context.Context, in ProgrammeEntry) (ProgrammeEntry, error) {
	actor := resolveActor(ctx, nil)
	out, err := updateAudited(ctx, s, EntityProgrammeEntries, programmeColumns, in.ID, actor,
		func(tx pgx.Tx, _ ProgrammeEntry) (ProgrammeEntry, error) {
			return queryOne[ProgrammeEntry](ctx, tx, "programme_entries",
				`UPDATE programme_entries
				    SET title = $1, note = $2, position = $3, budget_item_id = $4,
				        revision = revision + 1, updated_at = now()
				  WHERE id = $5 AND revision = $6
				RETURNING `+programmeColumns,
				in.Title, in.Note, in.Position, in.BudgetItemID, in.ID, in.Revision)
		})
	if err == nil {
		return out, nil
	}
	if !isNotFound(err) {
		return ProgrammeEntry{}, err
	}

	current, err := s.ProgrammeEntry(ctx, in.ID)
	return ProgrammeEntry{}, conflict("programme_entries", in.ID, in.Revision, current, err)
}

// DeleteProgrammeEntry removes a moment from the evening, refusing if revision
// is no longer current. Any budget item it referenced is untouched: the cost
// of the cake does not disappear because the cake stopped being a scheduled
// moment.
func (s *Store) DeleteProgrammeEntry(ctx context.Context, id uuid.UUID, revision int64) error {
	err := deleteAudited[ProgrammeEntry](ctx, s, EntityProgrammeEntries, programmeColumns, id, revision)
	if err == nil {
		return nil
	}
	if !isNotFound(err) {
		return err
	}

	current, err := s.ProgrammeEntry(ctx, id)
	return conflict("programme_entries", id, revision, current, err)
}
