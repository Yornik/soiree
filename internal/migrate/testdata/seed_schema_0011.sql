-- A database that is already being used, as of schema 0011.
--
-- TestRunUpgradesAPopulatedDatabase stops the migration run here, loads this,
-- and then lets the remaining migrations run over it. Every other
-- database-backed test in this repository migrates an empty schema, which is
-- the one shape a live deployment never has: a NOT NULL without a default, a
-- unique index over data that already violates it, and a backfill that reads
-- its input wrongly all apply perfectly well to no rows at all.
--
-- Frozen, like the migrations behind it. Files 0001 to 0011 have shipped and
-- cannot change, so the schema these statements are written against cannot
-- change either. When a later migration needs rows in a table that schema 0011
-- does not have, add a second seed for that point rather than editing this one.
--
-- The rows are awkward on purpose. The nulls, the self references, the
-- repeated values and the half finished records are the whole value of the
-- fixture: a tidy row passes the migration that a real database refuses.
--
-- Everybody here is invented, and the hashes are placeholders rather than the
-- digest of anything. No migration reads inside one, and a fixture carrying a
-- credential that looks real invites somebody to try it.

-- The three account states the column allows. The first admin was created by
-- nobody, and the invited editor has no password yet, which is the row a later
-- NOT NULL on password_hash would meet first.
INSERT INTO users (id, email, role, password_hash, status, created_by, revision) VALUES
  ('11111111-1111-4111-8111-111111111111', 'ada@example.org',  'admin',  'argon2id-placeholder-admin',  'active',   NULL,                                   3),
  ('11111111-1111-4111-8111-111111111112', 'ben@example.org',  'editor', NULL,                          'invited',  '11111111-1111-4111-8111-111111111111', 1),
  ('11111111-1111-4111-8111-111111111113', 'cleo@example.org', 'viewer', 'argon2id-placeholder-viewer', 'disabled', '11111111-1111-4111-8111-111111111111', 5);

-- The settings singleton already exists from 0002 and its boolean primary key
-- forbids a second row, so this is an update.
UPDATE settings
   SET ceiling = 1250000, inflation_pct = 3.50, fx_rate = 1.084500,
       split_evenly = true, revision = 4;

INSERT INTO phases (id, name, position, revision, updated_at, updated_by) VALUES
  ('22222222-2222-4222-8222-222222222201', 'Guests arrive', 0, 2, '2026-02-01 18:00:00+00', '11111111-1111-4111-8111-111111111111'),
  -- Never touched since it was created, so it still carries what 0009 gave it:
  -- revision 1 and no author at all.
  ('22222222-2222-4222-8222-222222222202', 'Dinner',        1, 1, '2026-01-20 12:00:00+00', NULL);

INSERT INTO sponsors (id, code, name, position, revision, updated_by) VALUES
  ('33333333-3333-4333-8333-333333333301', 'A', 'Ada', 0, 2, '11111111-1111-4111-8111-111111111111'),
  -- Name left at its default, which is what a sponsor looks like between being
  -- added and being filled in.
  ('33333333-3333-4333-8333-333333333302', 'B', '',    1, 1, NULL);

INSERT INTO budget_items (id, phase_id, parent_id, item, vendor, unit, qty, paid, lock_by, note, position, revision, updated_by) VALUES
  ('44444444-4444-4444-8444-444444444401', '22222222-2222-4222-8222-222222222202', NULL, 'Catering', 'A caterer', 450000, 1.000, 100000, '2026-03-01', 'Deposit paid', 0, 7, '11111111-1111-4111-8111-111111111111'),
  -- A component of the line above. Deleting the parent has to take it along,
  -- and a rollup that counted it separately would charge the catering twice.
  ('44444444-4444-4444-8444-444444444402', '22222222-2222-4222-8222-222222222202', '44444444-4444-4444-8444-444444444401', 'Drinks', 'A caterer', 12550, 40.000, 0, NULL, '', 1, 1, NULL),
  -- No phase and no deadline: the two columns a later NOT NULL would find
  -- empty.
  ('44444444-4444-4444-8444-444444444403', NULL, NULL, 'Insurance', '', 0, 1.000, 0, NULL, '', 2, 1, '11111111-1111-4111-8111-111111111113');

INSERT INTO budget_item_sponsors (budget_item_id, sponsor_id) VALUES
  ('44444444-4444-4444-8444-444444444401', '33333333-3333-4333-8333-333333333301'),
  ('44444444-4444-4444-8444-444444444401', '33333333-3333-4333-8333-333333333302'),
  ('44444444-4444-4444-8444-444444444403', '33333333-3333-4333-8333-333333333301');

INSERT INTO tasks (id, name, owner, due, status, position, revision, updated_by) VALUES
  ('55555555-5555-4555-8555-555555555501', 'Book the room', 'Ada', '2026-02-14', 'done', 0, 4, '11111111-1111-4111-8111-111111111111'),
  -- No due date and nobody on it, which is most of a task list most of the
  -- time.
  ('55555555-5555-4555-8555-555555555502', 'Decide on music', '', NULL, 'not-started', 1, 1, NULL);

INSERT INTO notes (id, text, position, revision) VALUES
  ('66666666-6666-4666-8666-666666666601', 'The hall is booked until midnight.', 0, 2),
  ('66666666-6666-4666-8666-666666666602', '', 1, 1);

INSERT INTO user_ui_prefs (user_id, prefs) VALUES
  ('11111111-1111-4111-8111-111111111111', '{"budget": {"item": 260, "vendor": 180}}'::jsonb),
  ('11111111-1111-4111-8111-111111111112', '{}'::jsonb);

INSERT INTO programme_entries (id, title, note, position, budget_item_id, revision) VALUES
  -- Two entries with no cost at all. The partial unique index tolerates both
  -- nulls, and a later index that forgot to stay partial would not.
  ('77777777-7777-4777-8777-777777777701', 'Doors open', '',              0, NULL, 1),
  ('77777777-7777-4777-8777-777777777702', 'Speeches',   'Keep it short', 1, NULL, 3),
  ('77777777-7777-4777-8777-777777777703', 'Dinner',     '',              2, '44444444-4444-4444-8444-444444444401', 1);

INSERT INTO reminders_sent (period_key, claimed_at, sent_at, recipients, item_count) VALUES
  ('digest/7d/2026-01-05', '2026-01-05 07:00:00+00', '2026-01-05 07:00:02+00', 3, 4),
  -- Claimed and never confirmed: the crash between writing the row and handing
  -- the mail over. sent_at stays null for good.
  ('digest/7d/2026-01-12', '2026-01-12 07:00:00+00', NULL,                     0, 0);

-- The audit trail, including the phase entries a backfill modelled on 0009
-- would read. These were written after 0009 ran, so they are input for the next
-- such migration rather than a test of that one.
INSERT INTO change_log (entity, entity_id, action, revision, changes, actor_id, actor_label, at) VALUES
  ('phases', '22222222-2222-4222-8222-222222222201', 'create', NULL, '{"name": {"old": null, "new": "Arrival"}}'::jsonb,           '11111111-1111-4111-8111-111111111111', 'user', '2026-01-10 09:00:00+00'),
  ('phases', '22222222-2222-4222-8222-222222222201', 'update', 2,    '{"name": {"old": "Arrival", "new": "Guests arrive"}}'::jsonb, '11111111-1111-4111-8111-111111111111', 'user', '2026-02-01 18:00:00+00'),
  -- Two entries written by one transaction share `at` to the microsecond, so
  -- only the log's own identity tells them apart. A backfill that ordered by
  -- `at` would take either of them.
  ('budget_items', '44444444-4444-4444-8444-444444444401', 'update', 6, '{"unit": {"old": 400000, "new": 450000}}'::jsonb, '11111111-1111-4111-8111-111111111111', 'user', '2026-02-02 10:00:00+00'),
  ('budget_items', '44444444-4444-4444-8444-444444444401', 'update', 7, '{"paid": {"old": 0, "new": 100000}}'::jsonb,       '11111111-1111-4111-8111-111111111111', 'user', '2026-02-02 10:00:00+00'),
  -- The settings singleton has no id, and the reminder job has no account.
  ('settings', NULL, 'update', 4, '{"ceiling": {"old": 1000000, "new": 1250000}}'::jsonb, NULL, 'system', '2026-02-03 11:00:00+00'),
  -- History outliving the row it describes, which is why the entity column is
  -- not a foreign key of any kind.
  ('tasks', '55555555-5555-4555-8555-555555555599', 'delete', 2, '{"name": {"old": "Hire a photographer", "new": null}}'::jsonb, NULL, 'unknown', '2026-02-04 12:00:00+00');

INSERT INTO password_tokens (id, user_id, token_hash, purpose, expires_at, created_at, consumed_at) VALUES
  -- Redeemed, and kept rather than deleted so the link stays refusable.
  ('88888888-8888-4888-8888-888888888801', '11111111-1111-4111-8111-111111111111', '\x00000000000000000000000000000000000000000000000000000000000000a1'::bytea, 'invite', '2026-01-02 09:00:00+00', '2026-01-01 09:00:00+00', '2026-01-01 09:30:00+00'),
  -- Outstanding, and already expired, which is the pair a retention migration
  -- would have to tell apart.
  ('88888888-8888-4888-8888-888888888802', '11111111-1111-4111-8111-111111111112', '\x00000000000000000000000000000000000000000000000000000000000000a2'::bytea, 'reset',  '2026-01-03 09:00:00+00', '2026-01-02 09:00:00+00', NULL);

INSERT INTO sessions (id, user_id, token_hash, created_at, last_seen_at, expires_at) VALUES
  ('99999999-9999-4999-8999-999999999901', '11111111-1111-4111-8111-111111111111', '\x00000000000000000000000000000000000000000000000000000000000000b1'::bytea, '2026-02-01 08:00:00+00', '2026-02-01 20:00:00+00', '2026-03-01 08:00:00+00'),
  -- Long expired, and still here: the sweeper runs on a timer, not at the
  -- instant a session lapses.
  ('99999999-9999-4999-8999-999999999902', '11111111-1111-4111-8111-111111111113', '\x00000000000000000000000000000000000000000000000000000000000000b2'::bytea, '2026-01-01 08:00:00+00', '2026-01-01 08:05:00+00', '2026-01-02 08:00:00+00');

-- One person, two devices, which is the whole reason this is not a column on
-- users.
INSERT INTO push_subscriptions (id, user_id, endpoint, p256dh, auth, created_at, last_seen_at) VALUES
  ('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaa01', '11111111-1111-4111-8111-111111111111', 'https://push.example.org/subscription/one', 'p256dh-placeholder-one', 'auth-placeholder-one', '2026-01-05 09:00:00+00', '2026-02-05 09:00:00+00'),
  ('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaa02', '11111111-1111-4111-8111-111111111111', 'https://push.example.org/subscription/two', 'p256dh-placeholder-two', 'auth-placeholder-two', '2026-01-06 09:00:00+00', '2026-01-06 09:00:00+00');

INSERT INTO passkey_credentials (id, user_id, credential_id, public_key, sign_count, aaguid, transports, attestation_type, attestation_format, backup_eligible, backup_state, user_present, user_verified, label, created_at, last_used_at) VALUES
  -- A passkey synced through a platform keychain: it reports 0 forever, which
  -- is legal and is not a clone.
  ('bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbb01', '11111111-1111-4111-8111-111111111111', '\xc0ffee01'::bytea, '\xa5010203'::bytea, 0, '\x'::bytea, '{internal,hybrid}', 'none', 'packed', true, true, true, true, 'A phone', '2026-01-07 09:00:00+00', '2026-02-07 09:00:00+00'),
  -- A counting authenticator at the top of the uint32 range the CHECK allows,
  -- unused since it was registered.
  ('bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbb02', '11111111-1111-4111-8111-111111111111', '\xc0ffee02'::bytea, '\xa5040506'::bytea, 4294967295, '\x00112233445566778899aabbccddeeff'::bytea, '{usb}', '', '', false, false, true, false, '', '2026-01-08 09:00:00+00', NULL);

INSERT INTO passkey_challenges (id, challenge, ceremony, user_id, session_data, expires_at, created_at) VALUES
  ('cccccccc-cccc-4ccc-8ccc-cccccccccc01', 'challenge-placeholder-register', 'register', '11111111-1111-4111-8111-111111111112', '{"challenge": "challenge-placeholder-register"}'::jsonb, '2026-02-08 09:05:00+00', '2026-02-08 09:00:00+00'),
  -- Begun by nobody in particular, which is what a discoverable login is and
  -- why the column is nullable.
  ('cccccccc-cccc-4ccc-8ccc-cccccccccc02', 'challenge-placeholder-login',    'login',    NULL,                                   '{"challenge": "challenge-placeholder-login"}'::jsonb,    '2026-02-08 09:05:00+00', '2026-02-08 09:00:00+00');
