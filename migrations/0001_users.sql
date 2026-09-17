-- Accounts come first because every shared table carries an `updated_by`
-- attribution that references this one. The foreign keys are the reason this
-- migration exists now; logging in is a later milestone.
CREATE TABLE users (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  email         text NOT NULL,
  role          text NOT NULL DEFAULT 'viewer'
                  CHECK (role IN ('admin', 'editor', 'viewer')),
  -- Null until the invited person follows their one-time link and chooses a
  -- password. An admin creates the account but never picks the password, so
  -- there is a real window where an account exists without one.
  password_hash text,
  status        text NOT NULL DEFAULT 'invited'
                  CHECK (status IN ('invited', 'active', 'disabled')),
  -- Null for the first admin, who was created by nobody, and for accounts
  -- whose creator has since been deleted.
  created_by    uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at    timestamptz NOT NULL DEFAULT now(),
  revision      bigint NOT NULL DEFAULT 1,
  updated_at    timestamptz NOT NULL DEFAULT now()
);

-- Case-insensitive uniqueness. A plain UNIQUE would let Ada@example.test and
-- ada@example.test both exist: two accounts for one person, and a login that
-- works only when they type it the way they first did. citext expresses the
-- same thing but needs an extension, and extensions are not something to
-- assume on a managed cluster.
CREATE UNIQUE INDEX users_email_lower_key ON users (lower(email));
