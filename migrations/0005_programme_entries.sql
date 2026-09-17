-- The run of show: guests arrive, speeches, cake, dinner, karaoke, closing.
--
-- This is not a budget with a timestamp column bolted on. In a real planning
-- sheet most of these lines cost nothing at all — they exist so everyone
-- knows what happens when, and several of the ones that do cost something
-- share a single budget line. Modelling the timeline as budget items with a
-- zero price would put a dozen empty rows in the ledger and make the item
-- count meaningless.
--
-- Ordering is `position`, not a clock time. Evenings run late; the order of
-- the evening survives that, and "20:15" written down three weeks earlier
-- does not.
CREATE TABLE programme_entries (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  title          text NOT NULL DEFAULT '',
  note           text NOT NULL DEFAULT '',
  position       integer NOT NULL,
  -- Optional in both directions: most entries have no cost, and plenty of
  -- budget items (the venue deposit, the insurance) never appear on stage.
  -- SET NULL rather than CASCADE, because dropping the cake from the budget
  -- does not drop the cake from the evening.
  budget_item_id uuid REFERENCES budget_items(id) ON DELETE SET NULL,
  revision       bigint NOT NULL DEFAULT 1,
  updated_at     timestamptz NOT NULL DEFAULT now()
);

-- One budget line belongs to at most one moment in the evening. Without this
-- the same cost can be attached twice and any "cost of the evening so far"
-- roll-up double-counts it. Partial only to keep the index off the many rows
-- with no cost attached — Postgres would allow those duplicate NULLs either
-- way, they just have nothing to enforce.
CREATE UNIQUE INDEX programme_entries_budget_item_key
  ON programme_entries (budget_item_id)
  WHERE budget_item_id IS NOT NULL;
