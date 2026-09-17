-- The plan itself: who is paying, what is being bought, what is still to do.

-- Named stages of the event that items group under ("guests arrive",
-- "dinner", "speeches"). Real planning spreadsheets organise costs by the run
-- of the evening, not as a flat list.
CREATE TABLE phases (
  id       uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name     text NOT NULL,
  position integer NOT NULL
);

CREATE TABLE sponsors (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  code        text NOT NULL,
  name        text NOT NULL DEFAULT '',
  position    integer NOT NULL,
  revision    bigint NOT NULL DEFAULT 1,
  updated_at  timestamptz NOT NULL DEFAULT now(),
  updated_by  uuid REFERENCES users(id) ON DELETE SET NULL
);

CREATE TABLE budget_items (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  phase_id    uuid REFERENCES phases(id) ON DELETE SET NULL,
  -- Self-reference for quotes that break down into components: a catering
  -- line is one number in the budget but a dozen rows in the caterer's quote.
  -- Children roll up into the parent rather than being counted separately,
  -- which is also why deleting the parent takes them with it — an orphaned
  -- component would suddenly start counting on its own.
  parent_id   uuid REFERENCES budget_items(id) ON DELETE CASCADE,
  item        text   NOT NULL DEFAULT '',
  -- Its own column rather than a word in the note: the vendor is the thing
  -- people filter by and chase, and free text cannot be queried.
  vendor      text   NOT NULL DEFAULT '',
  unit        bigint NOT NULL DEFAULT 0,  -- minor units
  qty         numeric(12,3) NOT NULL DEFAULT 1,
  paid        bigint NOT NULL DEFAULT 0,  -- minor units, deposits included
  -- A decision deadline, distinct from a task due date: the day this vendor
  -- has to be committed to or the price or the slot is gone. Planners track
  -- this by hand in prose, which is exactly the kind of note that goes stale.
  lock_by     date,
  note        text   NOT NULL DEFAULT '',
  position    integer NOT NULL,
  revision    bigint NOT NULL DEFAULT 1,
  updated_at  timestamptz NOT NULL DEFAULT now(),
  updated_by  uuid REFERENCES users(id) ON DELETE SET NULL,
  CONSTRAINT no_self_parent CHECK (parent_id IS DISTINCT FROM id)
);

CREATE INDEX budget_items_phase_position_idx ON budget_items (phase_id, position);
CREATE INDEX budget_items_parent_idx ON budget_items (parent_id);
-- Partial, because the interesting query is "what has to be decided soon" and
-- the rows without a deadline are never part of the answer.
CREATE INDEX budget_items_lock_by_idx ON budget_items (lock_by) WHERE lock_by IS NOT NULL;

-- The relational win over the JSON array the browser keeps today: a sponsor
-- reference cannot dangle, because deleting a sponsor cascades here rather
-- than leaving an id nothing resolves.
CREATE TABLE budget_item_sponsors (
  budget_item_id uuid NOT NULL REFERENCES budget_items(id) ON DELETE CASCADE,
  sponsor_id     uuid NOT NULL REFERENCES sponsors(id)     ON DELETE CASCADE,
  PRIMARY KEY (budget_item_id, sponsor_id)
);

-- Walked from the sponsor side ("what is Ada covering?") as often as from the
-- item side, and the primary key only serves the latter.
CREATE INDEX budget_item_sponsors_sponsor_idx ON budget_item_sponsors (sponsor_id);

CREATE TABLE tasks (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name        text NOT NULL DEFAULT '',
  owner       text NOT NULL DEFAULT '',
  due         date,
  status      text NOT NULL DEFAULT 'not-started'
                CHECK (status IN ('not-started', 'in-progress', 'done')),
  position    integer NOT NULL,
  revision    bigint NOT NULL DEFAULT 1,
  updated_at  timestamptz NOT NULL DEFAULT now(),
  updated_by  uuid REFERENCES users(id) ON DELETE SET NULL
);

CREATE TABLE notes (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  text        text NOT NULL DEFAULT '',
  position    integer NOT NULL,
  revision    bigint NOT NULL DEFAULT 1,
  updated_at  timestamptz NOT NULL DEFAULT now()
);
