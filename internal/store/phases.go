package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
	actor := resolveActor(ctx, nil)
	return createAudited(ctx, s, EntityPhases, actor, func(tx pgx.Tx) (Phase, error) {
		return queryOne[Phase](ctx, tx, "phases",
			`INSERT INTO phases (id, name, position)
			 VALUES (COALESCE($1, gen_random_uuid()), $2, $3)
			 RETURNING `+phaseColumns,
			newID(in.ID), in.Name, in.Position)
	})
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
// table carries no revision to check against — which is also why the row is
// locked while it is read and rewritten: with no revision to prove nobody
// intervened, the lock is what makes the "from" value in the change log the
// value this write actually replaced.
func (s *Store) UpdatePhase(ctx context.Context, in Phase) (Phase, error) {
	actor := resolveActor(ctx, nil)
	return updateAudited(ctx, s, EntityPhases, phaseColumns, in.ID, actor,
		func(tx pgx.Tx, _ Phase) (Phase, error) {
			return queryOne[Phase](ctx, tx, "phases",
				`UPDATE phases SET name = $1, position = $2 WHERE id = $3
				 RETURNING `+phaseColumns,
				in.Name, in.Position, in.ID)
		})
}

// DeletePhase removes a phase. Its budget items survive with a null phase_id
// rather than being deleted with it: dropping a stage of the evening must not
// silently drop what it was going to cost.
//
// No revision to check, so this cannot use the shared delete path. What the
// items lose — their phase_id — is set to null by the cascade and appears in no
// item's history; see the note in audit.go.
func (s *Store) DeletePhase(ctx context.Context, id uuid.UUID) error {
	actor := resolveActor(ctx, nil)
	_, err := inTx(ctx, s, func(tx pgx.Tx) (struct{}, error) {
		before, err := lockRow[Phase](ctx, tx, EntityPhases, phaseColumns, id)
		if err != nil {
			return struct{}{}, err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM phases WHERE id = $1`, id); err != nil {
			return struct{}{}, fmt.Errorf("phases: %w", err)
		}
		return struct{}{}, recordDelete(ctx, tx, EntityPhases, id, nil, before, actor)
	})
	return err
}
