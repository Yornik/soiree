package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Phase is a named stage of the event that budget items group under —
// "Arrival", "Dinner", "Speeches". Real planning sheets organise costs by the
// run of the evening rather than as one flat list.
//
// It carries the same revision, updated_at and updated_by as every other shared
// table, since migration 0009. It was the one table without them, which made
// its writes last-write-wins: two people renaming the same stage of the evening
// were not told apart, and the one who lost never found out.
type Phase struct {
	ID        uuid.UUID  `db:"id"`
	Name      string     `db:"name"`
	Position  int32      `db:"position"`
	Revision  int64      `db:"revision"`
	UpdatedAt time.Time  `db:"updated_at"`
	UpdatedBy *uuid.UUID `db:"updated_by"`
}

const phaseColumns = `id, name, position, revision, updated_at, updated_by`

// CreatePhase inserts a phase. A zero ID lets the database generate one. actor
// is the account making the change, or nil where there is no session.
func (s *Store) CreatePhase(ctx context.Context, in Phase, actor *uuid.UUID) (Phase, error) {
	who := resolveActor(ctx, actor)
	return createAudited(ctx, s, EntityPhases, who, func(tx pgx.Tx) (Phase, error) {
		return queryOne[Phase](ctx, tx, "phases",
			`INSERT INTO phases (id, name, position, updated_by)
			 VALUES (COALESCE($1, gen_random_uuid()), $2, $3, $4)
			 RETURNING `+phaseColumns,
			newID(in.ID), in.Name, in.Position, actor)
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

// UpdatePhase renames or reorders a phase, refusing the write if in.Revision is
// no longer current.
func (s *Store) UpdatePhase(ctx context.Context, in Phase, actor *uuid.UUID) (Phase, error) {
	who := resolveActor(ctx, actor)
	out, err := updateAudited(ctx, s, EntityPhases, phaseColumns, in.ID, who,
		func(tx pgx.Tx, _ Phase) (Phase, error) {
			return queryOne[Phase](ctx, tx, "phases",
				`UPDATE phases
				    SET name = $1, position = $2,
				        revision = revision + 1, updated_at = now(), updated_by = $3
				  WHERE id = $4 AND revision = $5
				RETURNING `+phaseColumns,
				in.Name, in.Position, actor, in.ID, in.Revision)
		})
	if err == nil {
		return out, nil
	}
	if !isNotFound(err) {
		return Phase{}, err
	}

	current, err := s.Phase(ctx, in.ID)
	return Phase{}, conflict("phases", in.ID, in.Revision, current, err)
}

// DeletePhase removes a phase, refusing if revision is no longer current. Its
// budget items survive with a null phase_id rather than being deleted with it:
// dropping a stage of the evening must not silently drop what it was going to
// cost.
//
// What the items lose — their phase_id — is set to null by the cascade and
// appears in no item's history; see the note in audit.go.
func (s *Store) DeletePhase(ctx context.Context, id uuid.UUID, revision int64) error {
	err := deleteAudited[Phase](ctx, s, EntityPhases, phaseColumns, id, revision)
	if err == nil {
		return nil
	}
	if !isNotFound(err) {
		return err
	}

	current, err := s.Phase(ctx, id)
	return conflict("phases", id, revision, current, err)
}
