-- Passkeys: a second way in, never a replacement for the first.
--
-- A passkey is a key pair whose private half never leaves the authenticator —
-- a phone's secure element, a laptop's TPM, a USB key. This database therefore
-- holds nothing that can be used to log in: the public key verifies signatures
-- and cannot produce them, so a copy of these tables is not a way into any
-- account, which is the same property migration 0008 gave sessions and links by
-- storing only hashes.
--
-- Everything here is additive. `users.password_hash` is untouched, password
-- login is untouched, and an account with no row in `passkey_credentials`
-- behaves exactly as it did before. That is what makes a lost phone a lost
-- credential rather than a lost account: the password still works, and an admin
-- can still issue a fresh set-password link.

-- One row per authenticator, not per person. Phone and laptop are two separate
-- credentials with two separate key pairs, and somebody who registers both and
-- then loses one deletes a row rather than starting over.
CREATE TABLE passkey_credentials (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id       uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,

  -- The authenticator's own identifier for the key pair, as raw bytes.
  --
  -- UNIQUE across the whole table rather than per user: a credential belongs to
  -- exactly one account, and the constraint is what makes that true rather than
  -- hoped for. Without it, registering an existing credential against a second
  -- account would make a discoverable login ambiguous — the authenticator hands
  -- back one credential id and the server would have two answers for whose it
  -- is.
  --
  -- bytea rather than the base64url text the browser sends. It is a byte string
  -- that happens to be transported as text; storing the transport encoding
  -- invites a lookup that misses because one side padded it and the other did
  -- not.
  credential_id bytea NOT NULL UNIQUE,

  -- The COSE-encoded public key, exactly as the library re-encoded it at
  -- registration. Opaque here; only the WebAuthn library reads inside it.
  public_key    bytea NOT NULL,

  -- The authenticator's use counter, and the reason it is stored at all.
  --
  -- An authenticator that reports a counter increments it on every assertion,
  -- so a value that fails to advance means two things holding the same private
  -- key have been used in parallel — a cloned authenticator. The check lives in
  -- the handler; this column is the memory it compares against.
  --
  -- bigint for a uint32 wire value, because Postgres has no unsigned integer
  -- and 4294967295 does not fit in an int4. Many authenticators — most passkeys
  -- synced through a platform keychain — report 0 forever, which is legal and
  -- means "I do not count"; the handler treats a stored 0 and a reported 0 as
  -- the normal case rather than as a clone, or every such person is locked out
  -- on their second login.
  sign_count    bigint NOT NULL DEFAULT 0
                  CHECK (sign_count >= 0 AND sign_count <= 4294967295),

  -- The authenticator model's identifier, 16 bytes, or empty when it declines
  -- to say. Not used for any decision here; kept so "which device is this?" has
  -- an answer better than the label somebody typed.
  aaguid        bytea NOT NULL DEFAULT '\x'::bytea,

  -- How the authenticator can be reached ('internal', 'usb', 'nfc', 'ble',
  -- 'hybrid'). Handed back to the browser at the next ceremony so it can point
  -- at the right device instead of offering all of them.
  transports    text[] NOT NULL DEFAULT '{}',

  attestation_type   text NOT NULL DEFAULT '',
  attestation_format text NOT NULL DEFAULT '',

  -- The credential record flags §4 of the specification requires be kept.
  --
  -- backup_eligible is load-bearing rather than informational: it is fixed for
  -- the life of a credential, and an assertion that disagrees with it is a
  -- different credential wearing the same id. The library refuses such an
  -- assertion, and can only do so because this column survives.
  backup_eligible boolean NOT NULL DEFAULT false,
  backup_state    boolean NOT NULL DEFAULT false,
  user_present    boolean NOT NULL DEFAULT false,
  user_verified   boolean NOT NULL DEFAULT false,

  -- What its owner calls it: "my phone", "the yubikey in the drawer". Chosen by
  -- the person registering it, never by an admin, and the only reason a list of
  -- three credentials is something a human can act on.
  label         text NOT NULL DEFAULT '',

  created_at    timestamptz NOT NULL DEFAULT now(),
  -- Null until first use. "Which of these have I actually used?" is the
  -- question somebody asks before deleting one.
  last_used_at  timestamptz
);

-- Every read that is not the login lookup is "this person's credentials".
CREATE INDEX passkey_credentials_user_idx ON passkey_credentials (user_id);

-- The server's half of a ceremony in progress.
--
-- A WebAuthn challenge is single-use by definition: the whole mechanism is that
-- the authenticator signs something the server has never asked for before, so a
-- challenge that can be presented twice is a replayable assertion and the
-- signature proves nothing. Holding it server-side rather than in a cookie is
-- what makes "delete on use" an operation the client cannot decline to perform.
CREATE TABLE passkey_challenges (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),

  -- The base64url challenge itself, which is also how a response finds its
  -- ceremony: the value comes back inside the signed client data, so the client
  -- needs no ceremony handle and cannot present one belonging to a ceremony it
  -- did not begin. UNIQUE so redemption is a DELETE against exactly one row,
  -- which is what makes single use atomic between concurrent requests rather
  -- than a read followed by a hopeful write.
  challenge    text NOT NULL UNIQUE,

  -- Which ceremony this challenge was minted for. A registration challenge
  -- presented to the login endpoint is refused: without this the two ceremonies
  -- share one pool of challenges, and the endpoints stop being distinguishable
  -- from each other's point of view.
  ceremony     text NOT NULL CHECK (ceremony IN ('register', 'login')),

  -- The account a registration was begun by. Null for a login, which is begun
  -- by nobody in particular — that is the point of a discoverable credential,
  -- and it is also why login/begin cannot be used to find out whether an
  -- address has an account.
  user_id      uuid REFERENCES users(id) ON DELETE CASCADE,

  -- The library's session data for this ceremony, verbatim. It carries the
  -- challenge, the RP ID, the user handle and which extensions were asked for,
  -- and the finish step must be given back exactly what the begin step
  -- produced or verification fails.
  session_data jsonb NOT NULL,

  expires_at   timestamptz NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now()
);

-- login/begin is public and inserts a row per call, and each insert sweeps the
-- expired rows in the same statement. That predicate runs on the hot path, so
-- it is indexed — the same reason sessions_expires_at_idx exists in 0008, and
-- the same failure if it does not: invisible until it is not.
CREATE INDEX passkey_challenges_expires_at_idx ON passkey_challenges (expires_at);
