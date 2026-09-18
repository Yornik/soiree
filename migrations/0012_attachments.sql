-- Attachments: files on budget lines and tasks.
--
-- The bytes are not here. They live in an S3 bucket, and the browser moves them
-- to and from that bucket itself, over URLs this server signs and hands out.
-- What this database holds is the part that has to be transactional with the
-- rest of the plan: which row a file belongs to, what it is called, how large
-- it claimed to be, and whether the upload was ever confirmed.
--
-- That split decides the shape of everything below. A row can exist with no
-- object behind it (an upload somebody abandoned), and an object can outlive
-- its row (a delete that reached Postgres and not the bucket). Neither can be
-- prevented, because two systems do not commit together; both are made
-- harmless instead, by `status` and by `attachment_garbage`.

CREATE TABLE attachments (
  -- Also the object's name in the bucket: `attachments/<id>`. Never the file
  -- name. A bucket listing therefore says nothing about what anybody uploaded,
  -- and no key ever needs escaping in a signed URL, which is where hand-written
  -- S3 signing goes wrong.
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),

  -- Exactly one parent; the CHECK at the bottom is what makes that true. Two
  -- nullable columns rather than a (kind, id) pair because only real foreign
  -- keys can cascade, and a file whose budget line is gone is a file nobody can
  -- reach, find, or delete.
  budget_item_id uuid REFERENCES budget_items(id) ON DELETE CASCADE,
  task_id        uuid REFERENCES tasks(id)        ON DELETE CASCADE,

  -- What the person's device called it, after the server cleaned it: no path,
  -- no control characters, a bounded length. Shown in the list and offered as
  -- the name on download. It is never part of a path anywhere.
  name           text NOT NULL CHECK (name <> ''),

  -- As claimed by the uploading browser. Trusted for nothing: it is signed into
  -- the upload URL so the stored object carries it, and the download decides
  -- for itself, from a short allow-list, whether a type may open in the browser
  -- or must be saved.
  content_type   text NOT NULL CHECK (content_type <> ''),

  -- The size declared before the upload, in bytes. The quota is computed from
  -- this column, so it has to be right: the server compares it with the stored
  -- object before it ever marks a row ready, and a mismatch deletes both.
  size           bigint NOT NULL CHECK (size > 0),

  -- 'uploading' from the moment a URL is issued, 'ready' once the server has
  -- seen the object in the bucket at the declared size. Only ready rows are
  -- ever shown. Uploading rows still count toward the quota - otherwise the cap
  -- is a race anybody wins by starting ten uploads at once - and the sweeper
  -- removes the ones that were never finished.
  status         text NOT NULL DEFAULT 'uploading'
                   CHECK (status IN ('uploading', 'ready')),

  -- SET NULL, as change_log.actor_id does: deleting an account must not delete
  -- the caterer's quote that person happened to upload.
  uploaded_by    uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at     timestamptz NOT NULL DEFAULT now(),

  CHECK (num_nonnulls(budget_item_id, task_id) = 1)
);

-- No `revision` column, unlike every plan table. A revision protects an edit
-- from overwriting an edit it never saw, and a file is never edited: it is
-- added, and it is removed. There is nothing for two people to disagree about.

-- The plan is read with every attachment stitched onto its row, so both
-- lookups are by parent. Partial, because each row has only one of the two.
CREATE INDEX attachments_budget_item_idx ON attachments (budget_item_id)
  WHERE budget_item_id IS NOT NULL;
CREATE INDEX attachments_task_idx ON attachments (task_id)
  WHERE task_id IS NOT NULL;

-- The sweeper's question: which uploads were started and never finished?
CREATE INDEX attachments_uploading_idx ON attachments (created_at)
  WHERE status = 'uploading';

-- Object keys whose row is gone and whose object may not be.
--
-- Deleting a budget line cascades to its attachments inside Postgres, where no
-- application code runs, so the server cannot delete the objects "at the same
-- time" - it never learns which rows went. The trigger below writes each key
-- down in the same transaction that removes the row, and the server works
-- through this table afterwards. A crash, a restart or an unreachable bucket
-- between the two leaves a key here, not an object nobody knows about.
--
-- Deleting an object that does not exist succeeds in S3, so a key for an
-- upload that never happened costs one harmless request.
CREATE TABLE attachment_garbage (
  object_key text PRIMARY KEY,
  since      timestamptz NOT NULL DEFAULT now()
);

CREATE FUNCTION attachments_queue_object() RETURNS trigger
  LANGUAGE plpgsql AS $$
BEGIN
  INSERT INTO attachment_garbage (object_key)
  VALUES ('attachments/' || OLD.id::text)
  ON CONFLICT DO NOTHING;
  RETURN OLD;
END;
$$;

CREATE TRIGGER attachments_queue_object
  AFTER DELETE ON attachments
  FOR EACH ROW EXECUTE FUNCTION attachments_queue_object();
