package store

import (
	"context"
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
	// was called at the time rather than what it is called today.
	Label *string `db:"label"`
}

const (
	defaultActivityLimit = 50
	maxActivityLimit     = 200
)

// Activity reads one page of the feed.
//
// before is the id of the oldest entry already held, or 0 for the newest page.
// Paging is by id, never by offset: entries are appended while somebody is
// reading, and an offset into a list that grows at its head repeats rows.
//
// The name is looked up per entry in the log, through the index the per-row
// history already uses: the most recent entry for the same row, at or before
// this one, that carries the naming field. On a delete that field's `new` is
// null and its `old` is the name, hence the COALESCE.
func (s *Store) Activity(ctx context.Context, before int64, limit int) ([]ActivityEntry, error) {
	if limit <= 0 {
		limit = defaultActivityLimit
	}
	if limit > maxActivityLimit {
		limit = maxActivityLimit
	}
	return queryAll[ActivityEntry](ctx, s.pool, "change_log",
		`SELECT c.id, c.entity, c.entity_id, c.action, c.revision, c.changes,
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
		  WHERE ($1 = 0 OR c.id < $1)
		  ORDER BY c.id DESC
		  LIMIT $2`,
		before, limit)
}
