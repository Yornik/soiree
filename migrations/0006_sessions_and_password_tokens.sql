-- The two short-lived secrets that logging in needs: the one-time link that
-- lets an invited person choose a password, and the server-side record of a
-- browser that has already done so.
--
-- Neither table stores a secret it could leak. Both store the SHA-256 of one,
-- so a copy of this database — a backup, a replica, a stray pg_dump — contains
-- nothing that can be replayed. bytea rather than text: it is 32 fixed bytes,
-- and hex-in-text would be half again as large and invite a case-sensitivity
-- bug in the lookup.

-- Set-password and reset links. One table for both, because they are the same
-- object: a single-use capability to choose the password on one account. The
-- `purpose` column exists only so the mail can say the right thing.
CREATE TABLE password_tokens (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  -- UNIQUE is not about collisions — 256 random bits do not collide — but
  -- about the redemption being a single UPDATE against one row.
  token_hash  bytea NOT NULL UNIQUE,
  purpose     text NOT NULL DEFAULT 'invite'
                CHECK (purpose IN ('invite', 'reset')),
  expires_at  timestamptz NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  -- Set rather than deleted, so a redeemed link stays refusable for as long as
  -- the row lives. Deleting it would make "already used" indistinguishable
  -- from "never existed" at the storage layer; the API deliberately makes them
  -- indistinguishable to the *client*, which is a different decision and
  -- belongs a layer up.
  consumed_at timestamptz
);

-- Issuing a new link invalidates the outstanding ones for that person, which
-- is a lookup by user rather than by hash.
CREATE INDEX password_tokens_user_idx ON password_tokens (user_id);

-- Sessions are server-side records, not signed cookies. The cookie carries a
-- random value and nothing else: no user id, no role, no expiry the client
-- could edit. That is what makes logging out, disabling an account, and
-- "everywhere else, now" possible at all — with a self-contained token there
-- is nothing to revoke.
CREATE TABLE sessions (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id      uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash   bytea NOT NULL UNIQUE,
  created_at   timestamptz NOT NULL DEFAULT now(),
  -- Idle expiry slides forward with use; created_at is what the absolute cap
  -- is measured from, so a session that is kept warm forever still ends.
  last_seen_at timestamptz NOT NULL DEFAULT now(),
  expires_at   timestamptz NOT NULL
);

CREATE INDEX sessions_user_idx ON sessions (user_id);
-- The sweeper's predicate. Small table, but an unindexed periodic scan is the
-- kind of thing that is invisible until it is not.
CREATE INDEX sessions_expires_at_idx ON sessions (expires_at);
