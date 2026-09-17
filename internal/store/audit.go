package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Change history.
//
// Every write in this package records what it changed, in the same transaction
// as the write itself. That is the whole design: a change and the record of it
// commit together, so nothing can be written without being recorded and a write
// that rolls back takes its history entry with it.
//
// Why it matters more than it looks. Several people edit shared money here and
// end up owing each other real amounts. `updated_by` and `updated_at` answer
// "who touched this last", which is not the question that causes arguments.
// "Who moved the venue figure from 2,500 to 3,000, and when" can only be
// answered from a record made at the time; there is nothing to reconstruct it
// from afterwards, which is why this exists before the editing does.
//
// What this does not see. The database performs some changes on its own behalf,
// and a write path in Go cannot record a change it never issued:
//
//   - deleting a phase leaves its budget items with a null phase_id;
//   - deleting a sponsor removes their attribution from every line;
//   - deleting a budget item clears the programme entry that pointed at it.
//
// Each of those is a cascade declared in the schema. Catching them would mean
// recording from database triggers instead — and a trigger cannot see who is
// making the request. It would have to be told through a session variable that
// every caller remembers to set, which is exactly the bypass this design exists
// to prevent: a caller who forgets loses the actor, a caller who lies picks
// one. The one cascade that loses money data is closed here in Go instead:
// deleting a budget item records its children too, because they are budget rows
// and their amounts would otherwise vanish unrecorded.
//
// One table is left out on purpose rather than missed: user_ui_prefs. Column
// widths are one person's own layout, shared with nobody and owed to nobody, so
// there is no argument for a history of them to settle.

// Entity names, matching the table each change happened to. They are what
// ChangeHistory is asked for, so they live as constants rather than as string
// literals scattered through nine files.
const (
	EntityBudgetItems      = "budget_items"
	EntityNotes            = "notes"
	EntityPhases           = "phases"
	EntityProgrammeEntries = "programme_entries"
	EntitySettings         = "settings"
	EntitySponsors         = "sponsors"
	EntityTasks            = "tasks"
	EntityUsers            = "users"
)

// ChangeAction is what happened to the row.
type ChangeAction string

const (
	ChangeCreate ChangeAction = "create"
	ChangeUpdate ChangeAction = "update"
	ChangeDelete ChangeAction = "delete"
)

// Actor is who made a change.
//
// ID is the account, once there is one. Label is what to call them when there
// is not — "system", "import", "unknown" — and is deliberately a description of
// the kind of actor rather than a name or an address: a copy of somebody's
// email in an append-only table would survive the deletion of their account,
// which is the one thing erasure must not leave behind.
type Actor struct {
	ID    *uuid.UUID
	Label string
}

// SystemActor attributes a change to the deployment itself: migrations,
// imports, the reminder job. Naming it is better than recording nobody, since
// "unknown" then keeps meaning what it says — a change whose origin was
// genuinely not established.
var SystemActor = Actor{Label: "system"}

// normalise fills in a label, because the column will not accept an empty one
// and because failing a write over a missing label would trade an answerable
// question for an unwritten edit.
func (a Actor) normalise() Actor {
	if a.Label == "" {
		if a.ID != nil {
			a.Label = "user"
		} else {
			a.Label = "unknown"
		}
	}
	return a
}

// actorKey is the context key. Its own unexported type, so nothing outside this
// package can collide with it.
type actorKey struct{}

// WithActor puts the person making the request into the context, which is how
// every write in this package learns who to record.
//
// It is a context value rather than an argument because the actor belongs to
// the request, not to the call: it has to reach nine entities' worth of write
// methods through whatever calls them, and an argument would have to be
// threaded through — and could be forgotten — at every one of them.
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, a.normalise())
}

// ActorFromContext returns who the context says is acting, or the unknown
// actor. It never fails: accounts are a separate milestone and are not finished
// yet, so most callers today carry no actor at all. Refusing their writes would
// hold the whole audit trail hostage to work that has not landed.
func ActorFromContext(ctx context.Context) Actor {
	if a, ok := ctx.Value(actorKey{}).(Actor); ok {
		return a.normalise()
	}
	return Actor{Label: "unknown"}
}

// resolveActor decides who to record for one write.
//
// The context wins when it names an account, because that is the authenticated
// session and the explicit argument is only ever the caller's own claim. Where
// the context is silent — every caller until accounts land — the id destined
// for `updated_by` fills the gap, so the log and the row agree about who was
// last here instead of one of them saying "unknown".
func resolveActor(ctx context.Context, updatedBy *uuid.UUID) Actor {
	actor := ActorFromContext(ctx)
	if actor.ID == nil && updatedBy != nil {
		actor.ID = updatedBy
		actor.Label = "user"
	}
	return actor
}

// FieldChange is one field's value before and after, as raw JSON exactly as it
// was stored.
//
// Raw rather than decoded, because decoding is where money dies: unmarshalling
// a bigint into an `any` gives a float64, and a float64 is not a reliable
// container for a whole number of minor units. Callers that want a number
// unmarshal Old and New into a type they have chosen.
type FieldChange struct {
	Old json.RawMessage `json:"old"`
	New json.RawMessage `json:"new"`
}

// ChangeEntry is one recorded change.
type ChangeEntry struct {
	ID       int64        `db:"id"`
	Entity   string       `db:"entity"`
	EntityID *uuid.UUID   `db:"entity_id"` // nil for the settings singleton
	Action   ChangeAction `db:"action"`
	// The row's revision after the change; nil for phases, which carry none.
	Revision *int64                 `db:"revision"`
	Changes  map[string]FieldChange `db:"changes"`
	// Nil where there was no account, or where the account has since been
	// deleted — the entry survives either way, which is the point.
	ActorID    *uuid.UUID `db:"actor_id"`
	ActorLabel string     `db:"actor_label"`
	At         time.Time  `db:"at"`
}

const changeColumns = `id, entity, entity_id, action, revision, changes, actor_id, actor_label, at`

// HistoryPage bounds one page of history.
type HistoryPage struct {
	Limit  int
	Offset int
}

const (
	defaultHistoryLimit = 50
	maxHistoryLimit     = 500
)

// normalise applies the default and the ceiling. A caller asking for
// everything gets a page instead, because an unbounded read of a table that
// only ever grows is a slow query waiting for its moment.
func (p HistoryPage) normalise() HistoryPage {
	if p.Limit <= 0 {
		p.Limit = defaultHistoryLimit
	}
	if p.Limit > maxHistoryLimit {
		p.Limit = maxHistoryLimit
	}
	if p.Offset < 0 {
		p.Offset = 0
	}
	return p
}

// ChangeHistory reads one row's history, newest first.
//
// entity is one of the Entity constants; id is the row, or uuid.Nil for the
// settings singleton, which has no id of its own.
//
// Ordering is by the log's own id rather than by `at`: `at` is the transaction
// timestamp, so every entry written by a single change shares it to the
// microsecond and would come back in an arbitrary order.
//
// The row does not have to exist. History outliving its row is the entire
// reason this table is here, so a deleted budget item still answers for itself.
func (s *Store) ChangeHistory(ctx context.Context, entity string, id uuid.UUID, page HistoryPage) ([]ChangeEntry, error) {
	page = page.normalise()
	// IS NOT DISTINCT FROM rather than =, so the settings singleton's null
	// entity_id is matched by the same statement instead of a second one.
	return queryAll[ChangeEntry](ctx, s.pool, "change_log",
		`SELECT `+changeColumns+`
		   FROM change_log
		  WHERE entity = $1 AND entity_id IS NOT DISTINCT FROM $2
		  ORDER BY id DESC
		  LIMIT $3 OFFSET $4`,
		entity, newID(id), page.Limit, page.Offset)
}

// changeSet is the jsonb diff: one entry per field that changed.
type changeSet map[string]FieldChange

// redactedValue stands in for a field whose value must never be recorded. The
// entry still says the field changed, which is the part worth knowing.
var redactedValue = json.RawMessage(`"redacted"`)

// jsonNull is what a field is worth before it was created and after it was
// deleted.
var jsonNull = json.RawMessage(`null`)

// skippedFields are recorded as columns of the entry itself, so repeating them
// inside the diff would say the same thing twice — and `updated_by` would say
// it less accurately, since it holds the actor of the change before this one.
var skippedFields = map[string]bool{
	"id":         true,
	"revision":   true,
	"updated_at": true,
	"updated_by": true,
}

// redactedFields never have their value written to the log, only the fact that
// they changed. A password hash in an append-only table would outlive every
// password change meant to retire it, and the point of changing a password is
// that the old one stops existing.
//
// The address is deliberately not on this list. It is the login identity, so
// "somebody changed Ada's email to their own" is an account takeover, and a
// history that recorded it as "a field changed" would not tell it apart from
// fixing a typo. Erasing it when an account is deleted (roadmap item 10) is a
// later migration's job: the append-only trigger already permits one mutation
// by name, and that is where a second one would go.
var redactedFields = map[string]bool{
	"password_hash": true,
}

// auditField is one struct field, and the name it is recorded under.
type auditField struct {
	name   string
	index  int
	redact bool
}

// auditedFields works out which fields of an entity are recorded.
//
// The name comes from the `db` tag, so history keys are the column names — a
// reader of the log and a reader of the schema see the same words. A field with
// no `db` tag, or one that is not a column at all, is skipped unless it opts in
// with an `audit` tag: guessing a name for it would put changes in the log
// under a key nothing else in the system uses.
//
// The effect of driving this from tags rather than from a hand-written list is
// that a field added to an entity is audited the day it is added, instead of
// the day somebody remembers to add it here too.
func auditedFields(t reflect.Type) []auditField {
	out := make([]auditField, 0, t.NumField())
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name := f.Tag.Get("audit")
		if name == "" {
			name = f.Tag.Get("db")
		}
		if name == "" || name == "-" || skippedFields[name] {
			continue
		}
		out = append(out, auditField{name: name, index: i, redact: redactedFields[name]})
	}
	return out
}

// diffRows reports the fields that differ between two versions of a row.
//
// Comparison is on the marshalled bytes, which is the same form the values are
// stored in: two things that serialise identically are, for the purposes of a
// change log, the same value.
func diffRows[T any](before, after T) (changeSet, error) {
	beforeV := reflect.ValueOf(before)
	afterV := reflect.ValueOf(after)

	out := changeSet{}
	for _, f := range auditedFields(beforeV.Type()) {
		old, err := marshalField(beforeV.Field(f.index))
		if err != nil {
			return nil, fmt.Errorf("change_log: field %s: %w", f.name, err)
		}
		fresh, err := marshalField(afterV.Field(f.index))
		if err != nil {
			return nil, fmt.Errorf("change_log: field %s: %w", f.name, err)
		}
		if bytes.Equal(old, fresh) {
			continue
		}
		// After the comparison, never before it: redacting first would make
		// every password change look like no change at all.
		if f.redact {
			old, fresh = redactedValue, redactedValue
		}
		out[f.name] = FieldChange{Old: old, New: fresh}
	}
	return out, nil
}

// snapshotRow records every field of a row, on one side or the other: a create
// has no `old`, a delete has no `new`.
func snapshotRow[T any](row T, action ChangeAction) (changeSet, error) {
	v := reflect.ValueOf(row)

	out := changeSet{}
	for _, f := range auditedFields(v.Type()) {
		value, err := marshalField(v.Field(f.index))
		if err != nil {
			return nil, fmt.Errorf("change_log: field %s: %w", f.name, err)
		}
		if f.redact {
			value = redactedValue
		}
		if action == ChangeDelete {
			out[f.name] = FieldChange{Old: value, New: jsonNull}
			continue
		}
		out[f.name] = FieldChange{Old: jsonNull, New: value}
	}
	return out, nil
}

// marshalField renders one field as the JSON that goes into the log. Integers
// stay integers: minor units are recorded as the whole numbers they are, so a
// later change of currency or locale cannot rewrite what was agreed.
func marshalField(v reflect.Value) (json.RawMessage, error) {
	// An empty list and a missing list are the same fact — a budget line nobody
	// is covering. They marshal differently ([] against null), and without this
	// every line read from the database would appear to have changed when
	// compared with the same line built in Go.
	switch v.Kind() {
	case reflect.Slice, reflect.Map:
		if v.Len() == 0 {
			return jsonNull, nil
		}
	}

	b, err := json.Marshal(v.Interface())
	if err != nil {
		return nil, err
	}
	return b, nil
}

// recordCreate writes the history entry for a new row.
func recordCreate[T any](ctx context.Context, tx pgx.Tx, entity string, id uuid.UUID, revision *int64, row T, actor Actor) error {
	changes, err := snapshotRow(row, ChangeCreate)
	if err != nil {
		return err
	}
	return insertChange(ctx, tx, entity, id, ChangeCreate, revision, changes, actor)
}

// recordUpdate writes the history entry for an edited row, and writes nothing
// at all when nothing actually changed.
//
// A no-op update is a real thing — a client resending a field it did not touch,
// a form submitted twice — and recording it would fill the history people are
// meant to read with entries that say nothing. The row's revision still
// advances, so revisions in the log can skip a number; that is documented on
// the table rather than papered over here.
func recordUpdate[T any](ctx context.Context, tx pgx.Tx, entity string, id uuid.UUID, revision *int64, before, after T, actor Actor) error {
	changes, err := diffRows(before, after)
	if err != nil {
		return err
	}
	if len(changes) == 0 {
		return nil
	}
	return insertChange(ctx, tx, entity, id, ChangeUpdate, revision, changes, actor)
}

// recordDelete writes the history entry for a row being removed, holding its
// final state. This is the entry that has to survive the row, and the reason
// the table carries no foreign key to anything it describes.
func recordDelete[T any](ctx context.Context, tx pgx.Tx, entity string, id uuid.UUID, revision *int64, row T, actor Actor) error {
	changes, err := snapshotRow(row, ChangeDelete)
	if err != nil {
		return err
	}
	return insertChange(ctx, tx, entity, id, ChangeDelete, revision, changes, actor)
}

// insertChange appends one entry. It takes a pgx.Tx rather than the pool
// because every caller is inside the transaction that made the change, which is
// what makes the write and its record atomic.
func insertChange(ctx context.Context, tx pgx.Tx, entity string, id uuid.UUID, action ChangeAction, revision *int64, changes changeSet, actor Actor) error {
	payload, err := json.Marshal(changes)
	if err != nil {
		return fmt.Errorf("change_log: %w", err)
	}
	actor = actor.normalise()
	if _, err := tx.Exec(ctx,
		`INSERT INTO change_log (entity, entity_id, action, revision, changes, actor_id, actor_label)
		 VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7)`,
		entity, newID(id), string(action), revision, payload, actor.ID, actor.Label,
	); err != nil {
		return fmt.Errorf("change_log: %w", err)
	}
	return nil
}

// lockRow reads the row a write is about to change, as it stands, and holds it
// until the transaction ends.
//
// The read is what gives the log its "from" value. The lock is what makes that
// value honest for `phases`, the one shared table with no revision to prove
// nobody slipped in between the read and the write — everywhere else the
// revision check already proves it, since revisions only ever go up and a write
// that matched one is a write nothing intervened in.
func lockRow[T any](ctx context.Context, tx pgx.Tx, entity, columns string, id uuid.UUID) (T, error) {
	return queryOne[T](ctx, tx, entity,
		`SELECT `+columns+` FROM `+entity+` WHERE id = $1 FOR UPDATE`, id)
}

// idOf and revisionOf read the two columns the log keeps outside the diff, by
// the same `db` tags the rest of this package maps rows with. Reading them off
// the struct rather than asking each caller for them is what stops a write path
// recording one row's change against another row's id.
func idOf[T any](row T) uuid.UUID {
	v := reflect.ValueOf(row)
	t := v.Type()
	for i := range t.NumField() {
		if t.Field(i).Tag.Get("db") != "id" {
			continue
		}
		if id, ok := v.Field(i).Interface().(uuid.UUID); ok {
			return id
		}
	}
	// The settings singleton, which has no id worth exposing.
	return uuid.Nil
}

// revisionOf returns nil for an entity that carries no revision, which is
// `phases` and only `phases`.
func revisionOf[T any](row T) *int64 {
	v := reflect.ValueOf(row)
	t := v.Type()
	for i := range t.NumField() {
		if t.Field(i).Tag.Get("db") != "revision" || !v.Field(i).CanInt() {
			continue
		}
		rev := v.Field(i).Int()
		return &rev
	}
	return nil
}

// createAudited runs an INSERT and records it in the same transaction.
//
// insert returns the row as stored, which is what gets recorded: defaults the
// database filled in are part of what the row was created as.
func createAudited[T any](ctx context.Context, s *Store, entity string, actor Actor, insert func(tx pgx.Tx) (T, error)) (T, error) {
	return inTx(ctx, s, func(tx pgx.Tx) (T, error) {
		var zero T
		out, err := insert(tx)
		if err != nil {
			return zero, err
		}
		if err := recordCreate(ctx, tx, entity, idOf(out), revisionOf(out), out, actor); err != nil {
			return zero, err
		}
		return out, nil
	})
}

// updateAudited reads the row as it stands, runs the write, and records the
// difference — one transaction, so the three either all happen or none do.
//
// The error it returns on a write that matched no rows is ErrNotFound, exactly
// as the single-statement version did, because the caller still has to tell "no
// such row" from "somebody got there first" and only a re-read can do that.
func updateAudited[T any](ctx context.Context, s *Store, entity, columns string, id uuid.UUID, actor Actor, write func(tx pgx.Tx, before T) (T, error)) (T, error) {
	return inTx(ctx, s, func(tx pgx.Tx) (T, error) {
		var zero T
		before, err := lockRow[T](ctx, tx, entity, columns, id)
		if err != nil {
			return zero, err
		}
		after, err := write(tx, before)
		if err != nil {
			return zero, err
		}
		if err := recordUpdate(ctx, tx, entity, id, revisionOf(after), before, after, actor); err != nil {
			return zero, err
		}
		return after, nil
	})
}

// deleteAudited removes a row at the revision the caller last saw and records
// its final state on the way out. The entry outlives the row, which is the
// single property this whole table exists for.
func deleteAudited[T any](ctx context.Context, s *Store, entity, columns string, id uuid.UUID, revision int64) error {
	actor := resolveActor(ctx, nil)
	_, err := inTx(ctx, s, func(tx pgx.Tx) (struct{}, error) {
		before, err := lockRow[T](ctx, tx, entity, columns, id)
		if err != nil {
			return struct{}{}, err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM `+entity+` WHERE id = $1 AND revision = $2`, id, revision)
		if err != nil {
			return struct{}{}, fmt.Errorf("%s: %w", entity, err)
		}
		if tag.RowsAffected() == 0 {
			return struct{}{}, notFoundErr(entity)
		}
		return struct{}{}, recordDelete(ctx, tx, entity, id, revisionOf(before), before, actor)
	})
	return err
}
