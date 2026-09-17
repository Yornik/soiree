-- Grid column widths and row heights are per-person preferences, not shared
-- data. They live in the state blob today, which means one person dragging a
-- column resizes it for everyone.
--
-- Deliberately jsonb and deliberately unvalidated: the shape is owned by the
-- frontend, changes whenever the grid does, and nothing on the server reads
-- it. A column per preference would be a migration per stylistic change.
CREATE TABLE user_ui_prefs (
  user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  prefs   jsonb NOT NULL DEFAULT '{}'::jsonb
);
