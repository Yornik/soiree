// Package store is the typed data-access layer over PostgreSQL.
//
// Two rules shape the whole package.
//
// Money is a bigint in minor units, never a float. The browser works in major
// units, so the API converts at its own boundary using the currency's ISO 4217
// exponent; nothing below that boundary ever sees anything but the integer. See
// money.go for the conversions.
//
// Every shared row carries a `revision`, and every update names the revision
// the caller last saw. A write against a stale revision affects no rows and is
// reported as a conflict carrying the row as it now stands, rather than
// overwriting whoever got there first. That is the whole point of the layer:
// two people edit this at the same time, and last-write-wins loses one of
// their edits without telling either of them.
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the data-access layer. It is safe for concurrent use.
type Store struct {
	pool *pgxpool.Pool
}

// New wraps a pool. The pool's lifetime belongs to the caller, because the
// same pool is handed to the migration runner at startup.
func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Pool exposes the underlying pool, for the migration runner and for the
// LISTEN connection that the change fan-out will need.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Ping checks the database is reachable. This is what /readyz will call —
// and deliberately not what /healthz calls, since a liveness probe that fails
// on a database outage turns it into a restart loop.
func (s *Store) Ping(ctx context.Context) error {
	if err := s.pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping: %w", err)
	}
	return nil
}

// ErrNotFound reports a row that does not exist, or no longer does.
var ErrNotFound = errors.New("store: not found")

// ErrStaleRevision reports a write refused because someone else wrote first.
// Match it with errors.Is; recover the current row with errors.As on
// *StaleRevisionError.
var ErrStaleRevision = errors.New("store: stale revision")

// StaleRevisionError carries the row as it now stands, so the caller can
// reconcile against it instead of refetching everything. This is what the API
// will return as the body of a 409.
type StaleRevisionError struct {
	Entity   string    // table name, e.g. "budget_items"
	ID       uuid.UUID // zero for the settings singleton, which has no id
	Revision int64     // the revision the caller believed was current
	Current  any       // the row as stored, of the entity's own type
}

func (e *StaleRevisionError) Error() string {
	return fmt.Sprintf("store: %s %s changed since revision %d", e.Entity, e.ID, e.Revision)
}

// Unwrap makes errors.Is(err, ErrStaleRevision) work.
func (e *StaleRevisionError) Unwrap() error { return ErrStaleRevision }

// conflict explains a write that matched no rows: either the row is gone, or
// somebody else got to it first. Re-reading is what tells the two apart, and
// it doubles as the payload the caller reconciles against.
//
// current and err are the result of re-reading the row.
func conflict[T any](entity string, id uuid.UUID, revision int64, current T, err error) error {
	if err != nil {
		return err
	}
	return &StaleRevisionError{Entity: entity, ID: id, Revision: revision, Current: current}
}

// notFound converts pgx's no-rows sentinel into this package's, and leaves
// every other error alone.
func notFound(err error, entity string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w", entity, ErrNotFound)
	}
	return err
}

// notFoundErr builds the not-found error for a statement that matched no rows
// and had nothing to re-read.
func notFoundErr(entity string) error {
	return fmt.Errorf("%s: %w", entity, ErrNotFound)
}

// isNotFound reports whether an update's RETURNING clause produced nothing,
// which for an update means the revision check failed or the row is gone.
func isNotFound(err error) bool { return errors.Is(err, ErrNotFound) }

// newID returns nil for the zero UUID, so the INSERT can let the database
// generate one. Callers that already have an id — an importer replaying a
// previous export — keep it.
func newID(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

// querier is the part of pgx every read here needs, so the same helpers work
// against the pool and inside the transaction LoadPlan opens.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// queryOne runs a statement expected to produce exactly one row and maps it
// onto T by column name. Every read, create and update in this package goes
// through here or through queryAll, which is what keeps the scan boilerplate
// out of nine entities.
func queryOne[T any](ctx context.Context, q querier, entity, sql string, args ...any) (T, error) {
	var zero T
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return zero, fmt.Errorf("%s: %w", entity, err)
	}
	row, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByName[T])
	if err != nil {
		return zero, notFound(err, entity)
	}
	return row, nil
}

// queryAll runs a statement and maps every row onto T by column name.
func queryAll[T any](ctx context.Context, q querier, entity, sql string, args ...any) ([]T, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", entity, err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByName[T])
	if err != nil {
		return nil, fmt.Errorf("%s: %w", entity, err)
	}
	return out, nil
}

// queryScalars runs a statement returning a single column and collects it.
func queryScalars[T any](ctx context.Context, q querier, entity, sql string, args ...any) ([]T, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", entity, err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[T])
	if err != nil {
		return nil, fmt.Errorf("%s: %w", entity, err)
	}
	return out, nil
}

// inTx runs fn in a transaction and commits it if fn succeeds. Writes that
// touch more than one table use it, so a half-written row is never visible.
//
// Rollback after a successful commit is a no-op, which is why the deferred
// rollback can be unconditional — and it has to be, or an early return inside
// fn leaves the transaction open until the connection is reaped.
func inTx[T any](ctx context.Context, s *Store, fn func(pgx.Tx) (T, error)) (T, error) {
	var zero T

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return zero, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	out, err := fn(tx)
	if err != nil {
		return zero, err
	}
	if err := tx.Commit(ctx); err != nil {
		return zero, fmt.Errorf("commit: %w", err)
	}
	return out, nil
}

// exec runs a statement and reports how many rows it touched, which is how
// every revision check in this package decides whether it won.
func (s *Store) exec(ctx context.Context, entity, sql string, args ...any) (int64, error) {
	tag, err := s.pool.Exec(ctx, sql, args...)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", entity, err)
	}
	return tag.RowsAffected(), nil
}
