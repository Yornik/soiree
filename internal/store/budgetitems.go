package store

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// BudgetItem is one line of the budget.
//
// Unit and Paid are minor units (see money.go). Qty is a float64 because the
// column is numeric(12,3): every value that fits in twelve digits with three
// decimals converts to a float64 and back exactly, which is not true of the
// money columns and is why those are integers.
//
// ParentID points at the line this one rolls up into. A caterer quotes a
// per-dish breakdown that is one number in the budget; the children exist so
// the breakdown is not lost, and a total that counted them alongside the
// parent would be double the truth.
type BudgetItem struct {
	ID       uuid.UUID  `db:"id"`
	PhaseID  *uuid.UUID `db:"phase_id"`
	ParentID *uuid.UUID `db:"parent_id"`
	Item     string     `db:"item"`
	Vendor   string     `db:"vendor"`
	Unit     int64      `db:"unit"`
	Qty      float64    `db:"qty"`
	Paid     int64      `db:"paid"`
	// The date a decision has to be made or the price or the slot is gone —
	// not a task due date. Stored as a date, so it is the same day for
	// everyone regardless of where they read it.
	LockBy    *time.Time `db:"lock_by"`
	Note      string     `db:"note"`
	Position  int32      `db:"position"`
	Revision  int64      `db:"revision"`
	UpdatedAt time.Time  `db:"updated_at"`
	UpdatedBy *uuid.UUID `db:"updated_by"`

	// Who is covering this line. Its own table, so a sponsor reference cannot
	// outlive the sponsor; `db:"-"` because it is not a column.
	//
	// The `audit` tag opts it into the change log anyway. It is not a column,
	// but changing who is covering a line is a change to who owes what, which
	// is the one thing this history exists to answer.
	SponsorIDs []uuid.UUID `db:"-" audit:"sponsor_ids"`
}

const budgetItemColumns = `id, phase_id, parent_id, item, vendor, unit, qty, paid, lock_by, note, position, revision, updated_at, updated_by`

// CreateBudgetItem inserts a line, its sponsor attributions and its history
// entry together, so a failure halfway leaves none of them.
func (s *Store) CreateBudgetItem(ctx context.Context, in BudgetItem, actor *uuid.UUID) (BudgetItem, error) {
	who := resolveActor(ctx, actor)
	return createAudited(ctx, s, EntityBudgetItems, who, func(tx pgx.Tx) (BudgetItem, error) {
		out, err := queryOne[BudgetItem](ctx, tx, "budget_items",
			`INSERT INTO budget_items
			    (id, phase_id, parent_id, item, vendor, unit, qty, paid, lock_by, note, position, updated_by)
			 VALUES (COALESCE($1, gen_random_uuid()), $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
			 RETURNING `+budgetItemColumns,
			newID(in.ID), in.PhaseID, in.ParentID, in.Item, in.Vendor, in.Unit,
			in.Qty, in.Paid, in.LockBy, in.Note, in.Position, actor)
		if err != nil {
			return BudgetItem{}, err
		}
		if err := attachSponsors(ctx, tx, out.ID, in.SponsorIDs); err != nil {
			return BudgetItem{}, err
		}
		out.SponsorIDs = normaliseSponsors(in.SponsorIDs)
		return out, nil
	})
}

// BudgetItem reads one line with its sponsors.
func (s *Store) BudgetItem(ctx context.Context, id uuid.UUID) (BudgetItem, error) {
	item, err := queryOne[BudgetItem](ctx, s.pool, "budget_items",
		`SELECT `+budgetItemColumns+` FROM budget_items WHERE id = $1`, id)
	if err != nil {
		return BudgetItem{}, err
	}
	item.SponsorIDs, err = queryScalars[uuid.UUID](ctx, s.pool, "budget_item_sponsors",
		`SELECT sponsor_id FROM budget_item_sponsors WHERE budget_item_id = $1 ORDER BY sponsor_id`, id)
	if err != nil {
		return BudgetItem{}, err
	}
	return item, nil
}

// BudgetItems lists every line with its sponsors, in two queries rather than
// one per row.
func (s *Store) BudgetItems(ctx context.Context) ([]BudgetItem, error) {
	items, err := queryAll[BudgetItem](ctx, s.pool, "budget_items",
		`SELECT `+budgetItemColumns+` FROM budget_items ORDER BY position, id`)
	if err != nil {
		return nil, err
	}
	links, err := sponsorLinks(ctx, s.pool)
	if err != nil {
		return nil, err
	}
	for i := range items {
		items[i].SponsorIDs = links[items[i].ID]
	}
	return items, nil
}

// UpdateBudgetItem writes every mutable field, refusing the write if
// in.Revision is no longer current.
//
// SponsorIDs is part of "every mutable field": the attributions are replaced
// with exactly what is passed, so a caller that built the struct by hand
// instead of reading the row first will clear them. The revision check is what
// stops that happening behind somebody else's back, not this method.
// The line's history is written last, after the attributions, so the entry
// describes the row as it ends up — and so a failure attaching a sponsor rolls
// the history back with the write it describes.
func (s *Store) UpdateBudgetItem(ctx context.Context, in BudgetItem, actor *uuid.UUID) (BudgetItem, error) {
	who := resolveActor(ctx, actor)
	out, err := inTx(ctx, s, func(tx pgx.Tx) (BudgetItem, error) {
		// Not the shared updateAudited path: a budget line's state spans two
		// tables, and an entry that missed the sponsor attributions would be
		// silent about who stopped covering a cost.
		before, err := lockBudgetItem(ctx, tx, in.ID)
		if err != nil {
			return BudgetItem{}, err
		}
		out, err := queryOne[BudgetItem](ctx, tx, "budget_items",
			`UPDATE budget_items
			    SET phase_id = $1, parent_id = $2, item = $3, vendor = $4, unit = $5,
			        qty = $6, paid = $7, lock_by = $8, note = $9, position = $10,
			        revision = revision + 1, updated_at = now(), updated_by = $11
			  WHERE id = $12 AND revision = $13
			RETURNING `+budgetItemColumns,
			in.PhaseID, in.ParentID, in.Item, in.Vendor, in.Unit, in.Qty, in.Paid,
			in.LockBy, in.Note, in.Position, actor, in.ID, in.Revision)
		if err != nil {
			return BudgetItem{}, err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM budget_item_sponsors WHERE budget_item_id = $1`, in.ID); err != nil {
			return BudgetItem{}, err
		}
		if err := attachSponsors(ctx, tx, in.ID, in.SponsorIDs); err != nil {
			return BudgetItem{}, err
		}
		out.SponsorIDs = normaliseSponsors(in.SponsorIDs)
		if err := recordUpdate(ctx, tx, EntityBudgetItems, out.ID, &out.Revision, before, out, who); err != nil {
			return BudgetItem{}, err
		}
		return out, nil
	})
	if err == nil {
		return out, nil
	}
	if !isNotFound(err) {
		return BudgetItem{}, err
	}

	current, err := s.BudgetItem(ctx, in.ID)
	return BudgetItem{}, conflict("budget_items", in.ID, in.Revision, current, err)
}

// DeleteBudgetItem removes a line, refusing if revision is no longer current.
// Its children go with it, by cascade: a component of a quote that outlived
// the quote would start counting towards the total on its own.
//
// Every one of them gets a history entry, not just the line that was asked for.
// The children are budget rows carrying real amounts, and the cascade takes
// them without this layer issuing a statement — so they are read and recorded
// first, or a caterer's whole breakdown would leave no trace of what it said.
func (s *Store) DeleteBudgetItem(ctx context.Context, id uuid.UUID, revision int64) error {
	actor := resolveActor(ctx, nil)
	_, err := inTx(ctx, s, func(tx pgx.Tx) (struct{}, error) {
		doomed, err := lockBudgetItemTree(ctx, tx, id)
		if err != nil {
			return struct{}{}, err
		}
		// The files on every line of the subtree go too, by cascade. Read them
		// while they are still there; see lockAttachmentsOf.
		ids := make([]uuid.UUID, len(doomed))
		for i, item := range doomed {
			ids[i] = item.ID
		}
		files, err := lockAttachmentsOf(ctx, tx, attachmentsOfBudgetItems, ids)
		if err != nil {
			return struct{}{}, err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM budget_items WHERE id = $1 AND revision = $2`, id, revision)
		if err != nil {
			return struct{}{}, fmt.Errorf("budget_items: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return struct{}{}, notFoundErr("budget_items")
		}
		for _, item := range doomed {
			if err := recordDelete(ctx, tx, EntityBudgetItems, item.ID, &item.Revision, item, actor); err != nil {
				return struct{}{}, err
			}
		}
		return struct{}{}, recordAttachmentsLost(ctx, tx, files, actor)
	})
	if err == nil {
		return nil
	}
	if !isNotFound(err) {
		return err
	}

	current, err := s.BudgetItem(ctx, id)
	return conflict("budget_items", id, revision, current, err)
}

// lockBudgetItem reads one line with its sponsors and holds it for the rest of
// the transaction. This is the "before" a change is recorded against.
func lockBudgetItem(ctx context.Context, tx pgx.Tx, id uuid.UUID) (BudgetItem, error) {
	item, err := lockRow[BudgetItem](ctx, tx, EntityBudgetItems, budgetItemColumns, id)
	if err != nil {
		return BudgetItem{}, err
	}
	item.SponsorIDs, err = queryScalars[uuid.UUID](ctx, tx, "budget_item_sponsors",
		`SELECT sponsor_id FROM budget_item_sponsors WHERE budget_item_id = $1 ORDER BY sponsor_id`, id)
	if err != nil {
		return BudgetItem{}, err
	}
	return item, nil
}

// lockBudgetItemTree reads a line and every line that rolls up into it, however
// deep, and holds them all.
//
// Two statements rather than one: FOR UPDATE cannot be applied to a recursive
// query, so the recursion collects ids and the second read locks them. Without
// the lock a child edited between the read and the delete would be recorded
// with the wrong final amount — and the amount is the whole point.
func lockBudgetItemTree(ctx context.Context, tx pgx.Tx, root uuid.UUID) ([]BudgetItem, error) {
	ids, err := queryScalars[uuid.UUID](ctx, tx, "budget_items",
		`WITH RECURSIVE subtree AS (
		     SELECT id FROM budget_items WHERE id = $1
		     UNION ALL
		     SELECT child.id FROM budget_items child JOIN subtree ON child.parent_id = subtree.id
		 )
		 SELECT id FROM subtree`, root)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, notFoundErr("budget_items")
	}

	items, err := queryAll[BudgetItem](ctx, tx, "budget_items",
		`SELECT `+budgetItemColumns+` FROM budget_items WHERE id = ANY($1) ORDER BY position, id FOR UPDATE`, ids)
	if err != nil {
		return nil, err
	}
	links, err := sponsorLinks(ctx, tx)
	if err != nil {
		return nil, err
	}
	for i := range items {
		items[i].SponsorIDs = links[items[i].ID]
	}
	return items, nil
}

// BudgetItemChildren lists the components that roll up into one line.
func (s *Store) BudgetItemChildren(ctx context.Context, parent uuid.UUID) ([]BudgetItem, error) {
	return queryAll[BudgetItem](ctx, s.pool, "budget_items",
		`SELECT `+budgetItemColumns+` FROM budget_items WHERE parent_id = $1 ORDER BY position, id`, parent)
}

// attachSponsors records who is covering a line. ON CONFLICT because the same
// sponsor listed twice by a caller is a harmless mistake, not a failed write.
func attachSponsors(ctx context.Context, tx pgx.Tx, itemID uuid.UUID, sponsors []uuid.UUID) error {
	if len(sponsors) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx,
		`INSERT INTO budget_item_sponsors (budget_item_id, sponsor_id)
		 SELECT $1, unnest($2::uuid[])
		 ON CONFLICT DO NOTHING`,
		itemID, normaliseSponsors(sponsors))
	if err != nil {
		return err
	}
	return nil
}

// sponsorLinks reads the whole join table at once, keyed by item. One query
// for every item's sponsors is the difference between this layer and the N+1
// it would otherwise be.
func sponsorLinks(ctx context.Context, q querier) (map[uuid.UUID][]uuid.UUID, error) {
	rows, err := q.Query(ctx,
		`SELECT budget_item_id, sponsor_id FROM budget_item_sponsors ORDER BY budget_item_id, sponsor_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[uuid.UUID][]uuid.UUID{}
	for rows.Next() {
		var item, sponsor uuid.UUID
		if err := rows.Scan(&item, &sponsor); err != nil {
			return nil, err
		}
		out[item] = append(out[item], sponsor)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// normaliseSponsors drops repeats and sorts, matching the order reads come
// back in. Without it the slice returned by a write and the slice returned by
// the next read would differ for the same data, and every caller comparing the
// two would have to sort first.
//
// Byte order is Postgres's uuid order, so this is the same sequence
// ORDER BY sponsor_id produces.
func normaliseSponsors(ids []uuid.UUID) []uuid.UUID {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[uuid.UUID]struct{}, len(ids))
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	slices.SortFunc(out, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	return out
}
