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
// thing. They are per-user because they are not shared data — in the browser's
// state blob today, one person dragging a column resizes it for everyone.
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

// DeleteUIPrefs resets somebody to the default layout.
func (s *Store) DeleteUIPrefs(ctx context.Context, userID uuid.UUID) error {
	_, err := s.exec(ctx, "user_ui_prefs", `DELETE FROM user_ui_prefs WHERE user_id = $1`, userID)
	return err
}
