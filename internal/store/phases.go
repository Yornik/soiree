package store

import (
	"context"

	"github.com/google/uuid"
)

// Phase is a named stage of the event that budget items group under —
// "Arrival", "Dinner", "Speeches". Real planning sheets organise costs by the
// run of the evening rather than as one flat list.
//
// Note what is missing: a phase has no revision, no updated_at and no
// updated_by, because the schema in docs/architecture.md does not give it any.
// Updates here are therefore last-write-wins, unlike every other shared table
// in this package. See the note in the report accompanying this milestone.
type Phase struct {
	ID       uuid.UUID `db:"id"`
	Name     string    `db:"name"`
	Position int32     `db:"position"`
}

const phaseColumns = `id, name, position`

// CreatePhase inserts a phase. A zero ID lets the database generate one.
func (s *Store) CreatePhase(ctx context.Context, in Phase) (Phase, error) {
	return queryOne[Phase](ctx, s.pool, "phases",
		`INSERT INTO phases (id, name, position)
		 VALUES (COALESCE($1, gen_random_uuid()), $2, $3)
		 RETURNING `+phaseColumns,
		newID(in.ID), in.Name, in.Position)
}

// Phase reads one phase.
func (s *Store) Phase(ctx context.Context, id uuid.UUID) (Phase, error) {
	return queryOne[Phase](ctx, s.pool, "phases",
		`SELECT `+phaseColumns+` FROM phases WHERE id = $1`, id)
}

// Phases lists phases in display order. Positions can collide — two rows
// dragged to the same slot in the same second — so id breaks the tie and the
// order stays stable between calls.
func (s *Store) Phases(ctx context.Context) ([]Phase, error) {
	return queryAll[Phase](ctx, s.pool, "phases",
		`SELECT `+phaseColumns+` FROM phases ORDER BY position, id`)
}

// UpdatePhase renames or reorders a phase. No revision check, because the
// table carries no revision to check against.
func (s *Store) UpdatePhase(ctx context.Context, in Phase) (Phase, error) {
	return queryOne[Phase](ctx, s.pool, "phases",
		`UPDATE phases SET name = $1, position = $2 WHERE id = $3
		 RETURNING `+phaseColumns,
		in.Name, in.Position, in.ID)
}

// DeletePhase removes a phase. Its budget items survive with a null phase_id
// rather than being deleted with it: dropping a stage of the evening must not
// silently drop what it was going to cost.
func (s *Store) DeletePhase(ctx context.Context, id uuid.UUID) error {
	n, err := s.exec(ctx, "phases", `DELETE FROM phases WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return notFoundErr("phases")
	}
	return nil
}
