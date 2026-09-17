package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Note is a free-text line on the plan. No attribution column, because the
// schema gives it none — notes are the one shared thing nobody is asked to
// own.
type Note struct {
	ID        uuid.UUID `db:"id"`
	Text      string    `db:"text"`
	Position  int32     `db:"position"`
	Revision  int64     `db:"revision"`
	UpdatedAt time.Time `db:"updated_at"`
}

const noteColumns = `id, text, position, revision, updated_at`

// CreateNote inserts a note. Notes carry no attribution column, so who wrote
// one is answerable only from the change log — see audit.go.
func (s *Store) CreateNote(ctx context.Context, in Note) (Note, error) {
	actor := resolveActor(ctx, nil)
	return createAudited(ctx, s, EntityNotes, actor, func(tx pgx.Tx) (Note, error) {
		return queryOne[Note](ctx, tx, "notes",
			`INSERT INTO notes (id, text, position)
			 VALUES (COALESCE($1, gen_random_uuid()), $2, $3)
			 RETURNING `+noteColumns,
			newID(in.ID), in.Text, in.Position)
	})
}

// Note reads one note.
func (s *Store) Note(ctx context.Context, id uuid.UUID) (Note, error) {
	return queryOne[Note](ctx, s.pool, "notes",
		`SELECT `+noteColumns+` FROM notes WHERE id = $1`, id)
}

// Notes lists notes in display order.
func (s *Store) Notes(ctx context.Context) ([]Note, error) {
	return queryAll[Note](ctx, s.pool, "notes",
		`SELECT `+noteColumns+` FROM notes ORDER BY position, id`)
}

// UpdateNote writes the text and position, refusing the write if in.Revision
// is no longer current.
func (s *Store) UpdateNote(ctx context.Context, in Note) (Note, error) {
	actor := resolveActor(ctx, nil)
	out, err := updateAudited(ctx, s, EntityNotes, noteColumns, in.ID, actor,
		func(tx pgx.Tx, _ Note) (Note, error) {
			return queryOne[Note](ctx, tx, "notes",
				`UPDATE notes
				    SET text = $1, position = $2, revision = revision + 1, updated_at = now()
				  WHERE id = $3 AND revision = $4
				RETURNING `+noteColumns,
				in.Text, in.Position, in.ID, in.Revision)
		})
	if err == nil {
		return out, nil
	}
	if !isNotFound(err) {
		return Note{}, err
	}

	current, err := s.Note(ctx, in.ID)
	return Note{}, conflict("notes", in.ID, in.Revision, current, err)
}

// DeleteNote removes a note, refusing if revision is no longer current. Its
// history stays.
func (s *Store) DeleteNote(ctx context.Context, id uuid.UUID, revision int64) error {
	err := deleteAudited[Note](ctx, s, EntityNotes, noteColumns, id, revision)
	if err == nil {
		return nil
	}
	if !isNotFound(err) {
		return err
	}

	current, err := s.Note(ctx, id)
	return conflict("notes", id, revision, current, err)
}
