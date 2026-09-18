package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Attachments: the database's half of a file.
//
// The bytes are in a bucket; see migration 0012 for why, and for what that
// costs. This file is the part that has to agree with the rest of the plan:
// which row a file hangs off, whether the upload was ever confirmed, how much
// room is left, and which objects are waiting to be removed.

// EntityAttachments names the table in the change log and on the event stream.
const EntityAttachments = "attachments"

// Attachment statuses. A row is 'uploading' from the moment a URL is issued
// and 'ready' once the server has seen the object at its declared size.
const (
	AttachmentUploading = "uploading"
	AttachmentReady     = "ready"
)

// ErrQuotaExceeded is BeginAttachment's answer when the file would take the
// deployment past its total.
var ErrQuotaExceeded = errors.New("store: attachment quota exceeded")

// Attachment is one file's record.
type Attachment struct {
	ID uuid.UUID `db:"id"`
	// Exactly one of the two is set; the table's CHECK enforces it.
	BudgetItemID *uuid.UUID `db:"budget_item_id"`
	TaskID       *uuid.UUID `db:"task_id"`
	Name         string     `db:"name"`
	ContentType  string     `db:"content_type"`
	Size         int64      `db:"size"`

	// Not in the history. A recorded row is always a ready one, the entry has
	// its own timestamp, and the entry's actor already says who: a second copy
	// of the uploader's id inside the JSON would be the one that survives the
	// deletion of their account, which the column itself is careful not to.
	Status     string     `db:"status" audit:"-"`
	UploadedBy *uuid.UUID `db:"uploaded_by" audit:"-"`
	CreatedAt  time.Time  `db:"created_at" audit:"-"`
}

const attachmentColumns = `id, budget_item_id, task_id, name, content_type, size, status, uploaded_by, created_at`

// ObjectKey is where an attachment's bytes live in the bucket.
//
// The same expression exists once more, in SQL, in the trigger that queues a
// deleted row's object for removal. A test holds the two together: if they
// drifted, every delete would queue a key that names nothing and the real
// objects would stay forever.
func ObjectKey(id uuid.UUID) string { return "attachments/" + id.String() }

// quotaLock serialises BeginAttachment. The cap is "the sum of every row plus
// this one", and two uploads that each read the sum before either inserts both
// fit under a limit that together they break. An advisory lock rather than a
// table lock, so reading the plan never waits on somebody choosing a file.
// The number is arbitrary and only has to be this application's alone.
const quotaLock int64 = 0x736f69726565_01 // "soiree", 1

// BeginAttachment reserves room for a file and returns its row, 'uploading'.
//
// Rows still uploading count toward the total. Otherwise the cap is a race
// anybody wins by starting ten uploads before finishing one; the price is that
// an abandoned upload holds its space until the sweeper takes it back.
//
// A parent that does not exist surfaces as the foreign-key violation it is,
// which the API already reports as a 400.
func (s *Store) BeginAttachment(ctx context.Context, in Attachment, totalCap int64) (Attachment, error) {
	return inTx(ctx, s, func(tx pgx.Tx) (Attachment, error) {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, quotaLock); err != nil {
			return Attachment{}, fmt.Errorf("attachments: quota lock: %w", err)
		}
		var used int64
		if err := tx.QueryRow(ctx, `SELECT COALESCE(SUM(size), 0)::bigint FROM attachments`).Scan(&used); err != nil {
			return Attachment{}, fmt.Errorf("attachments: usage: %w", err)
		}
		if used+in.Size > totalCap {
			return Attachment{}, ErrQuotaExceeded
		}
		return queryOne[Attachment](ctx, tx, EntityAttachments,
			`INSERT INTO attachments (budget_item_id, task_id, name, content_type, size, uploaded_by)
			 VALUES ($1, $2, $3, $4, $5, $6)
			 RETURNING `+attachmentColumns,
			in.BudgetItemID, in.TaskID, in.Name, in.ContentType, in.Size, in.UploadedBy)
	})
}

// Attachment reads one row, in either status.
func (s *Store) Attachment(ctx context.Context, id uuid.UUID) (Attachment, error) {
	return queryOne[Attachment](ctx, s.pool, EntityAttachments,
		`SELECT `+attachmentColumns+` FROM attachments WHERE id = $1`, id)
}

// CompleteAttachment marks an upload confirmed. This is the moment the file
// exists as far as anybody else is concerned, so this is where it is recorded
// and announced — not when the URL was issued, which may come to nothing.
//
// Completing a row that is already ready returns it and records nothing: the
// browser's confirmation can be retried after a dropped response, and the
// second attempt must not be an error or a second history entry.
func (s *Store) CompleteAttachment(ctx context.Context, id uuid.UUID) (Attachment, error) {
	actor := resolveActor(ctx, nil)
	return inTx(ctx, s, func(tx pgx.Tx) (Attachment, error) {
		row, err := lockRow[Attachment](ctx, tx, EntityAttachments, attachmentColumns, id)
		if err != nil {
			return Attachment{}, err
		}
		if row.Status == AttachmentReady {
			return row, nil
		}
		row, err = queryOne[Attachment](ctx, tx, EntityAttachments,
			`UPDATE attachments SET status = 'ready' WHERE id = $1 RETURNING `+attachmentColumns, id)
		if err != nil {
			return Attachment{}, err
		}
		if err := recordCreate(ctx, tx, EntityAttachments, row.ID, nil, row, actor); err != nil {
			return Attachment{}, err
		}
		return row, nil
	})
}

// DeleteAttachment removes a row and returns what it was, so the caller knows
// which object to remove. The trigger has queued that object already; deleting
// it straight away is a courtesy, not the mechanism.
//
// Only a ready file's removal is recorded. An upload that never finished was
// never in the history to begin with, and an entry saying it was deleted would
// be the only evidence it had existed.
func (s *Store) DeleteAttachment(ctx context.Context, id uuid.UUID) (Attachment, error) {
	actor := resolveActor(ctx, nil)
	return inTx(ctx, s, func(tx pgx.Tx) (Attachment, error) {
		row, err := lockRow[Attachment](ctx, tx, EntityAttachments, attachmentColumns, id)
		if err != nil {
			return Attachment{}, err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM attachments WHERE id = $1`, id); err != nil {
			return Attachment{}, fmt.Errorf("attachments: %w", err)
		}
		if row.Status == AttachmentReady {
			if err := recordDelete(ctx, tx, EntityAttachments, row.ID, nil, row, actor); err != nil {
				return Attachment{}, err
			}
		}
		return row, nil
	})
}

// lockAttachmentsOf reads the confirmed files hanging off the given rows, and
// holds them, so they can be recorded once their parents have gone.
//
// The delete itself needs no help: the foreign key cascades and the trigger
// queues the objects. But a cascade happens inside the database, where the
// change log cannot see it, and "the caterer's signed quote was on that line"
// is precisely what somebody will want to know afterwards. This is the same
// reasoning, and the same shape, as DeleteBudgetItem recording the child lines
// that go with a parent.
//
// Two steps because the read has to come before the parent's DELETE — after
// it there is nothing left to read — and the record has to come after, once
// the revision check has said the delete is really happening.
func lockAttachmentsOf(ctx context.Context, tx pgx.Tx, column string, parents []uuid.UUID) ([]Attachment, error) {
	// column is one of the two constants below, never caller input.
	return queryAll[Attachment](ctx, tx, EntityAttachments,
		`SELECT `+attachmentColumns+` FROM attachments
		  WHERE `+column+` = ANY($1) AND status = 'ready'
		  ORDER BY created_at, id
		    FOR UPDATE`, parents)
}

func recordAttachmentsLost(ctx context.Context, tx pgx.Tx, lost []Attachment, actor Actor) error {
	for _, row := range lost {
		if err := recordDelete(ctx, tx, EntityAttachments, row.ID, nil, row, actor); err != nil {
			return err
		}
	}
	return nil
}

const (
	attachmentsOfBudgetItems = "budget_item_id"
	attachmentsOfTasks       = "task_id"
)

// readyAttachments is the plan's view: confirmed files only, oldest first, so a
// list in the page does not reorder itself when somebody adds to it.
func readyAttachments(ctx context.Context, q querier) ([]Attachment, error) {
	return queryAll[Attachment](ctx, q, EntityAttachments,
		`SELECT `+attachmentColumns+` FROM attachments
		  WHERE status = 'ready'
		  ORDER BY created_at, id`)
}

// SweepStaleUploads removes rows whose upload was started and never confirmed,
// giving their reserved space back. It returns how many went. Their objects,
// if any part of one arrived, are queued by the trigger like any other.
func (s *Store) SweepStaleUploads(ctx context.Context, olderThan time.Duration) (int64, error) {
	return s.exec(ctx, EntityAttachments,
		`DELETE FROM attachments
		  WHERE status = 'uploading' AND created_at < now() - make_interval(secs => $1)`,
		olderThan.Seconds())
}

// AttachmentGarbage lists object keys whose row is gone, oldest first.
func (s *Store) AttachmentGarbage(ctx context.Context, limit int) ([]string, error) {
	return queryScalars[string](ctx, s.pool, "attachment_garbage",
		`SELECT object_key FROM attachment_garbage ORDER BY since, object_key LIMIT $1`, limit)
}

// ForgetAttachmentGarbage drops a key once its object is confirmed gone — and
// only then, which is the caller's promise to keep.
func (s *Store) ForgetAttachmentGarbage(ctx context.Context, key string) error {
	_, err := s.exec(ctx, "attachment_garbage",
		`DELETE FROM attachment_garbage WHERE object_key = $1`, key)
	return err
}
