-- What has already been mailed, so a restart never says it twice.
--
-- The scheduler runs in-process and the Deployment runs more than one replica,
-- so "send a weekly digest" has three ways to send it twice: a redeploy, a
-- crash loop, and a second replica. An advisory lock stops two replicas racing
-- at the same instant; it does nothing about a pod that restarts an hour later
-- and starts its ticker again from zero. Only a record in the database can
-- answer "has this one already gone out?", and that is this table.
--
-- period_key is the digest's identity, not a timestamp: it is the schedule
-- width in whole days plus the calendar day the period started on, in the
-- event's timezone, so every replica in every process computes the same string
-- for the same week. Changing SOIREE_REMINDER_SCHEDULE therefore changes the
-- key space and produces one extra digest — which is the right answer, since
-- the operator just changed what "a period" means.
CREATE TABLE reminders_sent (
  period_key text PRIMARY KEY,
  -- Written before the mail is handed to the SMTP server, not after. A crash
  -- between the two leaves a claimed row with sent_at still NULL, and the next
  -- run skips the period and logs it loudly. That is deliberate: this is
  -- at-most-once. A deadline that never got mailed comes back in the next
  -- digest marked overdue, so nothing is lost for good; a duplicate digest,
  -- by contrast, teaches people to ignore the mail.
  --
  -- The default is a safety net. The scheduler supplies this value from its own
  -- clock, because it also enforces a minimum gap between two digests and a
  -- duration measured on one clock against a timestamp taken on another is not
  -- a duration.
  claimed_at timestamptz NOT NULL DEFAULT now(),
  sent_at    timestamptz,
  -- Observability only, and the reason to look here when somebody asks "did
  -- the reminder go out, and to how many people?".
  recipients integer NOT NULL DEFAULT 0,
  item_count integer NOT NULL DEFAULT 0
);

-- "Did anything fail to go out?" is the one query run against this table by
-- hand, and it reads the small tail of unconfirmed claims.
CREATE INDEX reminders_sent_unconfirmed_idx
  ON reminders_sent (claimed_at) WHERE sent_at IS NULL;
