-- Phases join every other shared table in carrying a revision.
--
-- `phases` was the one exception: no `revision`, no `updated_at`, no
-- `updated_by`. Two people renaming the same stage of the evening could not be
-- told apart — the second write simply won, and neither of them heard about
-- it. Everywhere else in this schema that is a 409 the client reconciles
-- against, and "Dinner" is no less shared than the line for what dinner costs.
--
-- The declarations are copied from `sponsors` and `tasks` rather than invented,
-- so there is one shape to remember rather than one per table.
ALTER TABLE phases
  ADD COLUMN revision   bigint      NOT NULL DEFAULT 1,
  ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now(),
  ADD COLUMN updated_by uuid REFERENCES users(id) ON DELETE SET NULL;

-- Backfill.
--
-- The column defaults already give every existing phase a usable state:
-- revision 1, and an `updated_at` of the moment this migration ran. Honest, but
-- not the truth — `change_log` (migration 0007) has recorded every phase write
-- since it landed, so where there is an entry for a phase, that entry says when
-- it was last changed and by whom. Taking the answer from there beats stamping
-- the whole table with the deployment time of this release.
--
-- `revision` stays at 1 for everybody. It is a counter whose absolute value
-- means nothing; all that matters is that it moves on the next write, and
-- inventing a history for it would put a number in the column that no recorded
-- change corresponds to.
--
-- A no-op on a database whose change_log holds no phase entries, which is every
-- database that has not written a phase since 0007.
UPDATE phases p
   SET updated_at = latest.at,
       updated_by = latest.actor_id
  FROM (
    SELECT DISTINCT ON (entity_id) entity_id, at, actor_id
      FROM change_log
     WHERE entity = 'phases' AND entity_id IS NOT NULL
     -- id, not at: every entry written by one transaction shares `at` to the
     -- microsecond, and only the log's own identity breaks that tie.
     ORDER BY entity_id, id DESC
  ) AS latest
 WHERE p.id = latest.entity_id;

-- Migration 0007 documents `change_log.revision` as "Null for `phases`, the one
-- shared table carrying no revision". That stopped being true three statements
-- ago, and 0007 cannot be corrected: migrations are append-only and a checksum
-- guard refuses to start if one that has already been applied is edited. The
-- column comment is the copy a reader of the live schema actually sees, so the
-- correction goes there.
COMMENT ON COLUMN change_log.revision IS
  'The row''s revision after the change. Non-null for every entity from migration 0009 onwards, which is where `phases` gained a revision; phase entries written before then carry null. Revisions can skip a number: a write that changes no field still bumps the row''s revision and records nothing here.';
