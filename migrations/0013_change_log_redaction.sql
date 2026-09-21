-- Erasure reaches the change log.
--
-- Migration 0007 left this table with one mutation allowed by name: blanking
-- `actor_id` when the account it pointed at is deleted. Its comment says a
-- second one is a later migration's job, to be added on purpose rather than by
-- accident. This is that migration, and the second mutation is a redaction.
--
-- Why it has to exist. The log records what each write altered, field by
-- field, so it holds every value a row ever carried: a contributor's name, an
-- owner cell, a sentence in a note, the address on an account. The erasure in
-- `internal/store/privacy.go` rewrites the rows and could reach none of those
-- copies, and the activity screen reads its labels straight back out of here.
-- An erasure that leaves the name in an append-only table has moved it rather
-- than removed it.
--
-- What is deliberately not here is a way for the writer to say "trust me, this
-- is an erasure". A session variable it sets would be the bypass 0007 argues
-- against: whoever can set it can then write anything, and the trigger is
-- reduced to taking somebody's word. So the rule is about the rows themselves,
-- and the trigger decides from them alone:
--
--   * every column but `changes` is unchanged, so an entry cannot be moved to
--     another row, another entity or another actor while it is being cleaned;
--   * the set of keys in `changes` is unchanged, so no field's record of
--     having changed can be added or dropped;
--   * each recorded value is either unchanged or the tombstone, one side at a
--     time, and a side that recorded nothing stays null.
--
-- The last rule is what makes this destruction and never revision: the only
-- value an entry can be moved to is the one that says the value is gone. It
-- also keeps a create entry a create entry, which the activity feed depends
-- on: it reads a label as the new value or, failing that, the old one.
--
-- The tombstone is '(erased)', which is `store.Tombstone` in Go. The same
-- string in two languages, and it has to stay that way.
--
-- Deleting entries is still refused outright, the retention purge included.
-- `PurgeEvent` redacts these entries instead of removing them, because
-- `GET /plan` answers "has this plan ever been written to" by asking this
-- table: a purge that emptied it would leave the plan reading as one nobody
-- had filled in yet, and the first browser still holding a copy would put
-- every purged row back. 0007's comment above the trigger says deletion is a
-- later migration's job; this migration is the later migration, and the answer
-- is that it stays refused.
CREATE FUNCTION change_log_is_redaction(before jsonb, after jsonb) RETURNS boolean
  LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE
  field text;
  kept  jsonb;
  side  text;
BEGIN
  IF jsonb_typeof(before) <> 'object' OR jsonb_typeof(after) <> 'object' THEN
    RETURN false;
  END IF;
  IF ARRAY(SELECT jsonb_object_keys(before) ORDER BY 1)
     IS DISTINCT FROM ARRAY(SELECT jsonb_object_keys(after) ORDER BY 1) THEN
    RETURN false;
  END IF;

  FOR field, kept IN SELECT * FROM jsonb_each(before) LOOP
    CONTINUE WHEN after -> field = kept;

    -- One field's record is {"old": ..., "new": ...}. Both sides are examined,
    -- because a value somebody later corrected names the person twice.
    IF jsonb_typeof(kept) <> 'object'
       OR jsonb_typeof(after -> field) <> 'object'
       OR ARRAY(SELECT jsonb_object_keys(kept) ORDER BY 1)
          IS DISTINCT FROM ARRAY(SELECT jsonb_object_keys(after -> field) ORDER BY 1) THEN
      RETURN false;
    END IF;

    FOR side IN SELECT jsonb_object_keys(kept) LOOP
      CONTINUE WHEN after -> field -> side = kept -> side;
      IF kept -> side = 'null'::jsonb
         OR after -> field -> side <> '"(erased)"'::jsonb THEN
        RETURN false;
      END IF;
    END LOOP;
  END LOOP;

  RETURN true;
END;
$$;

COMMENT ON FUNCTION change_log_is_redaction(jsonb, jsonb) IS
  'Whether one change_log diff is the other with values struck out: the same keys, and each recorded value either unchanged or the tombstone "(erased)" on a side that held a value. See migration 0013.';

CREATE OR REPLACE FUNCTION change_log_append_only() RETURNS trigger
  LANGUAGE plpgsql AS $$
BEGIN
  -- Blanking the actor when their account is deleted (migration 0007).
  IF tg_op = 'UPDATE'
     AND new.actor_id IS NULL
     AND to_jsonb(new) - 'actor_id' = to_jsonb(old) - 'actor_id' THEN
    RETURN new;
  END IF;

  -- Striking an erased person out of what was recorded about them.
  IF tg_op = 'UPDATE'
     AND to_jsonb(new) - 'changes' = to_jsonb(old) - 'changes'
     AND change_log_is_redaction(old.changes, new.changes) THEN
    RETURN new;
  END IF;

  RAISE EXCEPTION 'change_log is append-only: % is not allowed', tg_op;
END;
$$;

-- The comment above the trigger in 0007 says one mutation is allowed and only
-- one. That stopped being true three statements ago, and 0007 cannot be
-- corrected: migrations are append-only and a checksum guard refuses to start
-- against a file that has changed since it was applied. The function's own
-- comment is the copy a reader of the live schema sees, so the correction goes
-- there.
COMMENT ON FUNCTION change_log_append_only() IS
  'Append-only, with two mutations allowed by name: blanking actor_id when the account it pointed at is deleted (migration 0007), and a redaction, where every column but changes is unchanged and change_log_is_redaction() holds (migration 0013). Every DELETE is refused, the retention purge included, which redacts entries rather than removing them.';
