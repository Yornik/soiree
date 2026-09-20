package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Sponsor is somebody contributing to the cost. Code is the short label the
// grid puts in a cell; Name is who that is.
type Sponsor struct {
	ID        uuid.UUID  `db:"id"`
	Code      string     `db:"code"`
	Name      string     `db:"name"`
	Position  int32      `db:"position"`
	Revision  int64      `db:"revision"`
	UpdatedAt time.Time  `db:"updated_at"`
	UpdatedBy *uuid.UUID `db:"updated_by"`
}

const sponsorColumns = `id, code, name, position, revision, updated_at, updated_by`

// CreateSponsor inserts a sponsor. actor names the account making the change
// for a caller that has no session to put in the context, and is nil for the
// API, whose session is already there. resolveActor settles the two, and its
// answer is what both `updated_by` and the history entry record.
func (s *Store) CreateSponsor(ctx context.Context, in Sponsor, actor *uuid.UUID) (Sponsor, error) {
	who := resolveActor(ctx, actor)
	return createAudited(ctx, s, EntitySponsors, who, func(tx pgx.Tx) (Sponsor, error) {
		return queryOne[Sponsor](ctx, tx, "sponsors",
			`INSERT INTO sponsors (id, code, name, position, updated_by)
			 VALUES (COALESCE($1, gen_random_uuid()), $2, $3, $4, $5)
			 RETURNING `+sponsorColumns,
			newID(in.ID), in.Code, in.Name, in.Position, who.ID)
	})
}

// Sponsor reads one sponsor.
func (s *Store) Sponsor(ctx context.Context, id uuid.UUID) (Sponsor, error) {
	return queryOne[Sponsor](ctx, s.pool, "sponsors",
		`SELECT `+sponsorColumns+` FROM sponsors WHERE id = $1`, id)
}

// Sponsors lists sponsors in display order.
func (s *Store) Sponsors(ctx context.Context) ([]Sponsor, error) {
	return queryAll[Sponsor](ctx, s.pool, "sponsors",
		`SELECT `+sponsorColumns+` FROM sponsors ORDER BY position, id`)
}

// UpdateSponsor writes every mutable field, refusing the write if in.Revision
// is no longer current.
func (s *Store) UpdateSponsor(ctx context.Context, in Sponsor, actor *uuid.UUID) (Sponsor, error) {
	who := resolveActor(ctx, actor)
	out, err := updateAudited(ctx, s, EntitySponsors, sponsorColumns, in.ID, who,
		func(tx pgx.Tx, _ Sponsor) (Sponsor, error) {
			return queryOne[Sponsor](ctx, tx, "sponsors",
				`UPDATE sponsors
				    SET code = $1, name = $2, position = $3,
				        revision = revision + 1, updated_at = now(), updated_by = $4
				  WHERE id = $5 AND revision = $6
				RETURNING `+sponsorColumns,
				in.Code, in.Name, in.Position, who.ID, in.ID, in.Revision)
		})
	if err == nil {
		return out, nil
	}
	if !isNotFound(err) {
		return Sponsor{}, err
	}

	current, err := s.Sponsor(ctx, in.ID)
	return Sponsor{}, conflict("sponsors", in.ID, in.Revision, current, err)
}

// DeleteSponsor removes a sponsor and, by cascade, every attribution naming
// them. That cascade is the reason this is a table rather than an array of
// ids in a JSON blob: a reference that cannot dangle cannot be wrong.
//
// The sponsor's own history survives them. The attributions the cascade
// removes do not appear in any budget item's history, because the database
// removes them without this layer issuing a statement — see the note on what
// the change log does not see, in audit.go.
func (s *Store) DeleteSponsor(ctx context.Context, id uuid.UUID, revision int64) error {
	err := deleteAudited[Sponsor](ctx, s, EntitySponsors, sponsorColumns, id, revision)
	if err == nil {
		return nil
	}
	if !isNotFound(err) {
		return err
	}

	current, err := s.Sponsor(ctx, id)
	return conflict("sponsors", id, revision, current, err)
}
