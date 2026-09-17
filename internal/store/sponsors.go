package store

import (
	"context"
	"time"

	"github.com/google/uuid"
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

// CreateSponsor inserts a sponsor. actor is the account making the change, or
// nil where there is no session yet — which is every caller until the accounts
// milestone lands.
func (s *Store) CreateSponsor(ctx context.Context, in Sponsor, actor *uuid.UUID) (Sponsor, error) {
	return queryOne[Sponsor](ctx, s.pool, "sponsors",
		`INSERT INTO sponsors (id, code, name, position, updated_by)
		 VALUES (COALESCE($1, gen_random_uuid()), $2, $3, $4, $5)
		 RETURNING `+sponsorColumns,
		newID(in.ID), in.Code, in.Name, in.Position, actor)
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
	out, err := queryOne[Sponsor](ctx, s.pool, "sponsors",
		`UPDATE sponsors
		    SET code = $1, name = $2, position = $3,
		        revision = revision + 1, updated_at = now(), updated_by = $4
		  WHERE id = $5 AND revision = $6
		RETURNING `+sponsorColumns,
		in.Code, in.Name, in.Position, actor, in.ID, in.Revision)
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
func (s *Store) DeleteSponsor(ctx context.Context, id uuid.UUID, revision int64) error {
	n, err := s.exec(ctx, "sponsors", `DELETE FROM sponsors WHERE id = $1 AND revision = $2`, id, revision)
	if err != nil {
		return err
	}
	if n == 0 {
		current, err := s.Sponsor(ctx, id)
		return conflict("sponsors", id, revision, current, err)
	}
	return nil
}
