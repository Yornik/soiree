package store

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
)

// UIPrefs are one person's grid column widths and row heights.
//
// Opaque jsonb, and deliberately so: the shape belongs to the frontend, it
// changes whenever the grid does, and nothing on the server reads it. A column
// per preference would mean a migration every time somebody adds a resizable
// thing. They are per-user because they are not shared data.
//
// Nothing reaches any of this yet: no route and no page mentions prefs, so no
// row is ever written. Widths and heights live in the browser, and a plan
// adopted from the server leaves them there — one person dragging a column
// must not resize it for everybody. The table is what a layout that follows a
// person between devices would be built on; until then the only other code
// that touches it is the privacy export and erasure, which have no caller
// either.
//
// A reader and a writer are all there is, and the erasure and cascade tests
// are what they are for: putting a row in the table and proving it goes again
// with the account. Deleting one on its own has no method here, because the
// only path that deletes one is erasure, which does it inside its own
// transaction and could not call one hanging off Store.
func (s *Store) UIPrefs(ctx context.Context, userID uuid.UUID) (json.RawMessage, error) {
	var prefs json.RawMessage
	err := s.pool.QueryRow(ctx, `SELECT prefs FROM user_ui_prefs WHERE user_id = $1`, userID).Scan(&prefs)
	if err != nil {
		return nil, notFound(err, "user_ui_prefs")
	}
	return prefs, nil
}

// SetUIPrefs replaces one person's preferences. No revision check: these are
// nobody else's to conflict with, and the last drag of a column really is the
// one that should win.
func (s *Store) SetUIPrefs(ctx context.Context, userID uuid.UUID, prefs json.RawMessage) error {
	if len(prefs) == 0 {
		prefs = json.RawMessage(`{}`)
	}
	_, err := s.exec(ctx, "user_ui_prefs",
		`INSERT INTO user_ui_prefs (user_id, prefs) VALUES ($1, $2)
		 ON CONFLICT (user_id) DO UPDATE SET prefs = EXCLUDED.prefs`,
		userID, prefs)
	return err
}
