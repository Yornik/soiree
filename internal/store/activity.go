package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// Activity: the whole plan's history as one feed, newest first.
//
// ChangeHistory answers "what happened to this row". This answers "what has
// everybody been doing", which is the question people actually ask of a shared
// plan, and which migration 0007 left an index for before there was anything to
// use it.

// ActivityEntry is one change, with the two things a reader needs that the log
// does not hold in a readable form: who, and what it was called.
type ActivityEntry struct {
	ChangeEntry

	// ActorEmail is the account's address as it is now, or nil where there was
	// no account or it has since been deleted. Joined at read time rather than
	// recorded, for the reason Actor gives: an address written into an
	// append-only table would survive the erasure meant to remove it.
	ActorEmail *string `db:"actor_email"`

	// Label is what the row was called once this change had been made — a
	// budget line's item, a task's name, a file's name — or nil for settings,
	// which is one row with no name. It comes from the log itself, so a row
	// that no longer exists still has one, and a renamed row reads as what it
	// was called at the time rather than what it is called today. Where the
	// name was somebody's and they have been erased, it reads as the tombstone
	// (see redactLog in privacy.go), because the log no longer holds the name
	// either.
	Label *string `db:"label"`
}

const (
	defaultActivityLimit = 50
	maxActivityLimit     = 200
)

// ActivityFilter narrows the feed to one part of the plan. The zero value is
// the whole feed, which is what it was before there was anything to narrow it
// with.
type ActivityFilter struct {
	// Entity is one of the Entity constants, or "" for every entity.
	Entity string

	// EntityID is one row of that entity, or nil for all of its rows. A
	// pointer rather than uuid.Nil, because uuid.Nil already means something
	// else next door: ChangeHistory reads it as the settings singleton, the
	// one row with no id of its own. Settings are asked for here by naming the
	// entity and no row, which is the same answer by a shorter road.
	EntityID *uuid.UUID
}

// The filter's own clauses, and the tail every read ends with. They are
// constants: what the caller asked for reaches the statement as $3 and $4 and
// never as text.
//
// Added to the statement rather than folded into one predicate that tests the
// parameter itself, with an empty $3 standing for "no filter at all". A
// predicate of that shape has to be planned once for both calls, and a plan
// that fits both reads the table instead of change_log_entity_idx, the index
// one row's history exists to use.
const (
	activityOfEntity = `
		    AND c.entity = $3`
	activityOfRow = `
		    AND c.entity_id = $4`
	activityNewestFirst = `
		  ORDER BY c.id DESC
		  LIMIT $2`
)

// Activity reads one page of the feed, narrowed by filter.
//
// before is the id of the oldest entry already held, or 0 for the newest page.
// Paging is by id, never by offset: entries are appended while somebody is
// reading, and an offset into a list that grows at its head repeats rows. The
// filter belongs to the same query, so a page of a narrowed feed is as full as
// any other page and `before` keeps meaning what it means everywhere else.
// Sifting a page after reading it would instead return four entries of fifty,
// and an id to go on with that belongs to somebody else's row.
//
// The name is looked up per entry in the log, through the index the per-row
// history already uses: the most recent entry for the same row, at or before
// this one, that carries the naming field. On a delete that field's `new` is
// null and its `old` is the name, hence the COALESCE.
func (s *Store) Activity(ctx context.Context, filter ActivityFilter, before int64, limit int) ([]ActivityEntry, error) {
	// A row id with no entity beside it names no row, the log being read by
	// the pair. Refused here rather than dropped, because dropping it answers
	// a question about one line with every account's history, and the caller
	// that asked the narrower question would never know.
	if filter.EntityID != nil && filter.Entity == "" {
		return nil, errors.New("change_log: a row id with no entity to read it in")
	}
	if limit <= 0 {
		limit = defaultActivityLimit
	}
	if limit > maxActivityLimit {
		limit = maxActivityLimit
	}
	sql := `SELECT c.id, c.entity, c.entity_id, c.action, c.revision, c.changes,
		        c.actor_id, c.actor_label, c.at,
		        u.email   AS actor_email,
		        named.label
		   FROM change_log c
		   LEFT JOIN users u ON u.id = c.actor_id
		   LEFT JOIN (VALUES ('attachments', 'name'),
		                     ('budget_items', 'item'),
		                     ('notes', 'text'),
		                     ('phases', 'name'),
		                     ('programme_entries', 'title'),
		                     ('sponsors', 'name'),
		                     ('tasks', 'name'),
		                     ('users', 'email')) AS naming(entity, field)
		          ON naming.entity = c.entity
		   LEFT JOIN LATERAL (
		         SELECT COALESCE(l.changes -> naming.field ->> 'new',
		                         l.changes -> naming.field ->> 'old') AS label
		           FROM change_log l
		          WHERE l.entity = c.entity
		            AND l.entity_id = c.entity_id
		            AND l.id <= c.id
		            AND l.changes ? naming.field
		          ORDER BY l.id DESC
		          LIMIT 1) named ON true
		  WHERE ($1 = 0 OR c.id < $1)`
	args := []any{before, limit}
	if filter.Entity != "" {
		sql += activityOfEntity
		args = append(args, filter.Entity)
		if filter.EntityID != nil {
			sql += activityOfRow
			args = append(args, *filter.EntityID)
		}
	}
	return queryAll[ActivityEntry](ctx, s.pool, "change_log", sql+activityNewestFirst, args...)
}
