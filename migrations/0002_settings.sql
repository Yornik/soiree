-- One deployment serves one event, so the knobs that apply to the whole plan
-- are a single row rather than a table keyed by event. The boolean primary
-- key with its CHECK is what enforces that: no second row can be inserted.
CREATE TABLE settings (
  id             boolean PRIMARY KEY DEFAULT true CHECK (id),
  ceiling        bigint NOT NULL DEFAULT 0,  -- minor units
  inflation_pct  numeric(5,2) NOT NULL DEFAULT 0,
  fx_rate        numeric(18,6) NOT NULL DEFAULT 0,
  split_evenly   boolean NOT NULL DEFAULT false,
  revision       bigint NOT NULL DEFAULT 1,
  updated_at     timestamptz NOT NULL DEFAULT now()
);

-- The row has to exist before anything can read it, and callers only ever
-- update it. ON CONFLICT keeps the statement replayable against a database
-- that already has it.
INSERT INTO settings (id) VALUES (true) ON CONFLICT (id) DO NOTHING;
