-- Where a browser asked to be told about deadlines.
--
-- The deadline digest already goes out as mail (migration 0006). Mail is read
-- when somebody opens their mail, which for a decision that has to be made
-- this week is often the wrong day. This table is the second channel: a push
-- subscription is a capability handed over by one browser on one device, and
-- the only thing that makes "tell me on my phone" possible at all.
--
-- Not a column on `users`. A person has a phone and a laptop, and each is a
-- separate subscription with its own keys; one row per person would mean the
-- second device silently replaced the first.
CREATE TABLE push_subscriptions (
  id      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  -- CASCADE, like `sessions`. A subscription is a device belonging to an
  -- account; when the account goes, so does any claim its devices had on
  -- being notified about this event's finances.
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,

  -- The push service's URL for this device, e.g. https://fcm.googleapis.com/…
  -- It is the subscription's identity, which is why it is UNIQUE rather than
  -- merely indexed: the browser re-sends the same endpoint on every visit, and
  -- the write has to be an upsert rather than a fifth copy of one device. It
  -- is also the only handle the sender has when a push service reports that a
  -- subscription is gone, so pruning is a delete by this column.
  --
  -- A capability, not a secret in the credential sense: possession of it lets
  -- anyone who also holds the keys below send this device a notification. It
  -- is stored as it arrived because it has to be replayed verbatim; there is
  -- no hashed form that could still be POSTed to.
  endpoint text NOT NULL UNIQUE,

  -- The two halves of RFC 8291 message encryption, base64url as the Push API
  -- hands them over. p256dh is the device's public key and auth is a shared
  -- 16-byte secret; together they are what makes the payload readable by this
  -- device and by nothing in between, including the push service itself.
  p256dh text NOT NULL,
  auth   text NOT NULL,

  created_at timestamptz NOT NULL DEFAULT now(),
  -- Refreshed every time the browser re-sends this subscription. A push
  -- service can retire an endpoint without ever telling us — it only says so
  -- when something is sent to it — so this is the one signal that separates a
  -- device still in use from one last seen a year ago.
  last_seen_at timestamptz NOT NULL DEFAULT now()
);

-- "Which devices does this person have?" — the unsubscribe path and anything
-- that later wants to show somebody their own devices.
CREATE INDEX push_subscriptions_user_idx ON push_subscriptions (user_id);
