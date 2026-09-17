-- Append-only change history: who changed what, from what, to what, and when.
--
-- Rows already carry `updated_by` and `updated_at`, which answer "who touched
-- this last". That is not the question that causes arguments. Several people
-- edit shared money here and end up owing each other real amounts, and the
-- question is "who moved the venue figure from 2,500 to 3,000, and when" —
-- answerable only from a record made at the time. There is nothing to
-- reconstruct it from later, which is why this lands before the editing does.

-- One table rather than one per entity.
--
-- Nine near-identical history tables would mean a migration in nine places
-- every time a column is added, and "what happened to this plan last week"
-- would be a nine-way UNION. The cost is that the diff is jsonb instead of
-- typed columns, and that is the right trade here: this log is read by row and
-- by time, never by "find every change where the amount exceeded X".
CREATE TABLE change_log (
  -- A plain identity rather than a uuid: this is the only table with an
  -- intrinsic order, and that order is what "newest first" reads by. It also
  -- breaks ties within a single transaction, whose entries all share `at`.
  id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,

  -- The table the change happened to, e.g. 'budget_items'.
  --
  -- Deliberately not a foreign key of any kind, and deliberately without a
  -- CHECK listing the known tables: history has to outlive the row it
  -- describes — that is the entire point — and a CHECK would have to be
  -- rewritten by a later migration every time an entity is added.
  entity      text NOT NULL,
  -- Null for the settings singleton, which has no id.
  entity_id   uuid,
  action      text NOT NULL CHECK (action IN ('create', 'update', 'delete')),

  -- The row's revision after the change. Null for `phases`, the one shared
  -- table carrying no revision. Revisions can skip a number: a write that
  -- changes no field still bumps the row's revision and records nothing here.
  revision    bigint,

  -- {"unit": {"old": 250000, "new": 300000}} — one key per field that changed,
  -- raw values rather than formatted strings. Money is bigint minor units and
  -- is stored as the integer it is: a history of "€2,500.00" would be rewritten
  -- by a later change of currency or locale, and a history that can be
  -- rewritten is not evidence of anything. jsonb stores numbers as `numeric`,
  -- so a bigint survives the round trip exactly.
  --
  -- A create records every field with a null `old`; a delete records every
  -- field with a null `new`, which is what keeps a row's final state readable
  -- after the row itself is gone.
  changes     jsonb NOT NULL,

  -- Who did it. Null where there was no account: the accounts milestone is not
  -- finished, and background work — imports, reminders — never has a session.
  -- ON DELETE SET NULL matches `updated_by` on the shared tables, so deleting a
  -- person degrades the answer to "somebody" rather than destroying the
  -- evidence, and leaves erasure (roadmap item 10) actually possible.
  actor_id    uuid REFERENCES users(id) ON DELETE SET NULL,
  -- What to call the actor when there is no account id, or once one has been
  -- erased: 'system', 'import', 'unknown'. A label for the kind of actor, not a
  -- name or an address — a copy of somebody's email here would survive the
  -- deletion of their account, which is exactly what must not happen.
  actor_label text NOT NULL CHECK (actor_label <> ''),

  -- Transaction start time, so every entry written by one change shares it.
  at          timestamptz NOT NULL DEFAULT now()
);

-- The read this table exists for: one row's history, newest first.
CREATE INDEX change_log_entity_idx ON change_log (entity, entity_id, id DESC);
-- And the whole plan's, for a "what changed this week" feed.
CREATE INDEX change_log_at_idx ON change_log (at DESC, id DESC);

-- Append-only, enforced rather than promised.
--
-- The application owns this database, so REVOKE buys nothing: whatever can
-- INSERT here could also UPDATE. A trigger is the only thing standing between
-- an accidental `UPDATE change_log SET ...` and a history that quietly agrees
-- with whoever edited it last. Evidence nobody can alter is the only kind
-- worth keeping.
--
-- One mutation is allowed, and only one: blanking actor_id when the account it
-- pointed at is deleted. That is the ON DELETE SET NULL above — erasing a
-- person must stay possible, and it is the reason this cannot simply forbid
-- every UPDATE. The comparison checks that nothing else moved with it.
--
-- Deleting entries is refused outright. A retention policy for the log itself
-- is a later migration's job, and it will have to drop this trigger on purpose
-- — which is the point: it cannot happen by accident.
CREATE FUNCTION change_log_append_only() RETURNS trigger
  LANGUAGE plpgsql AS $$
BEGIN
  IF tg_op = 'UPDATE'
     AND new.actor_id IS NULL
     AND to_jsonb(new) - 'actor_id' = to_jsonb(old) - 'actor_id' THEN
    RETURN new;
  END IF;
  RAISE EXCEPTION 'change_log is append-only: % is not allowed', tg_op;
END;
$$;

CREATE TRIGGER change_log_append_only
  BEFORE UPDATE OR DELETE ON change_log
  FOR EACH ROW EXECUTE FUNCTION change_log_append_only();
