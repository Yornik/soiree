// Package migrate applies the embedded SQL migrations at startup.
//
// The Deployment runs more than one replica and they start at the same time,
// so the interesting part of this package is not "run some SQL in order" but
// "run it exactly once across a set of processes that know nothing about each
// other". A session-level advisory lock is what makes that true: the first
// replica to take it applies the schema, the rest block and then find there
// is nothing left to do.
package migrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Yornik/soiree/migrations"
)

// advisoryLockKey identifies the migration lock. The value is arbitrary and
// meaningless; all that matters is that every replica of this application
// uses the same one and that nothing else in the database picks it too.
const advisoryLockKey int64 = 0x5010_1EE0_0001

// unlockTimeout bounds the release of the advisory lock. It runs on a context
// detached from the caller's, so a cancelled startup still gives it a moment
// to finish.
const unlockTimeout = 5 * time.Second

// lockTimeoutSQL bounds how long one statement in a migration waits for a
// lock. The new replica migrates while the previous release is still serving,
// so an ALTER TABLE queued behind a long reader — a pg_dump, a forgotten psql
// transaction — does not wait alone: every query the old release makes on that
// table queues behind the pending ACCESS EXCLUSIVE request and the site hangs
// until something kills the new pod. A migration that cannot have its lock
// promptly should give up and be retried by the restart instead.
//
// It is SET LOCAL, so it reverts with the transaction. At session level it
// would also bound the pg_advisory_lock wait above, which a replica
// legitimately sits in for the length of another replica's whole run.
const lockTimeoutSQL = "SET LOCAL lock_timeout = '10s'"

// ledgerDDL creates the record of what has already been applied.
//
// It is not itself a migration file, because it has to exist before the first
// file can be recorded — and it is created while holding the advisory lock,
// because CREATE TABLE IF NOT EXISTS is not atomic against a concurrent
// identical call: two of them race in the catalogue and one fails on a
// duplicate key in pg_type.
const ledgerDDL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
	version    bigint PRIMARY KEY,
	name       text NOT NULL,
	checksum   text NOT NULL,
	applied_at timestamptz NOT NULL DEFAULT now()
)`

// migration is one file, ready to apply.
type migration struct {
	version  int64
	name     string
	filename string
	sql      string
	checksum string
}

// Run applies every migration that has not been applied yet and returns the
// versions it applied, in order. It is safe to call concurrently from any
// number of processes.
//
// A nil logger is allowed; it discards.
func Run(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) ([]int64, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	pending, err := load()
	if err != nil {
		return nil, err
	}

	// Everything below runs on one connection. The advisory lock is
	// session-scoped, so taking it on the pool and then running the
	// migrations on whatever connection came next would lock nothing.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", advisoryLockKey); err != nil {
		return nil, fmt.Errorf("take migration lock: %w", err)
	}
	defer func() {
		// pgxpool does not reset the session when a connection goes back to
		// the pool, so a lock left behind here outlives this function and
		// blocks the next migration run for the life of the connection.
		// Detach from the caller's context: if startup was cancelled mid-run
		// the lock still has to come off.
		unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), unlockTimeout)
		defer cancel()
		if _, err := conn.Exec(unlockCtx, "SELECT pg_advisory_unlock($1)", advisoryLockKey); err != nil {
			log.Error("failed to release migration lock", "err", err)
		}
	}()

	if _, err := conn.Exec(ctx, ledgerDDL); err != nil {
		return nil, fmt.Errorf("create schema_migrations: %w", err)
	}

	applied, err := appliedMigrations(ctx, conn)
	if err != nil {
		return nil, err
	}

	var ran []int64
	for _, m := range pending {
		if row, ok := applied[m.version]; ok {
			// An applied migration whose file has since changed means two
			// databases now disagree about their schema. Refusing to start is
			// the only honest response; silently skipping hides it until
			// something else breaks far from the cause.
			if row.checksum != m.checksum {
				return ran, fmt.Errorf("migration %s was modified after it was applied (recorded %s, file %s)", m.filename, row.checksum, m.checksum)
			}
			continue
		}
		if err := apply(ctx, conn, m); err != nil {
			return ran, err
		}
		log.Info("migration applied", "version", m.version, "name", m.name)
		ran = append(ran, m.version)
	}

	warnIfSchemaAhead(log, pending, applied)

	return ran, nil
}

// warnIfSchemaAhead reports ledger rows this binary has no file for.
//
// That is what a rollback looks like from in here: a newer release migrated
// the database, and then this older binary started against it. It cannot
// maintain whatever that release added — writes made here skip it — and the
// only thing it has to say for itself otherwise is migrationsApplied: 0, which
// reads exactly like an ordinary restart.
//
// A warning and not a refusal, because rolling back is the emergency tool and
// every migration so far has been additive, so the older binary does serve.
// Taking the tool away would be worse than the silence it replaces.
func warnIfSchemaAhead(log *slog.Logger, pending []migration, applied map[int64]appliedMigration) {
	embedded := make(map[int64]struct{}, len(pending))
	var highest int64
	for _, m := range pending {
		embedded[m.version] = struct{}{}
		if m.version > highest {
			highest = m.version
		}
	}

	var unknown []int64
	for version := range applied {
		if _, ok := embedded[version]; !ok {
			unknown = append(unknown, version)
		}
	}
	if len(unknown) == 0 {
		return
	}
	sort.Slice(unknown, func(i, j int) bool { return unknown[i] < unknown[j] })

	names := make([]string, 0, len(unknown))
	var earliest time.Time
	for _, version := range unknown {
		row := applied[version]
		names = append(names, row.name)
		if earliest.IsZero() || row.appliedAt.Before(earliest) {
			earliest = row.appliedAt
		}
	}

	log.Warn("database schema is ahead of this binary",
		"unknownVersions", unknown,
		"unknownNames", names,
		"binaryHighest", highest,
		"earliestAppliedAt", earliest)
}

// apply runs one migration and records it in the same transaction, so a
// crash between the two is not possible.
func apply(ctx context.Context, conn *pgxpool.Conn, m migration) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin %s: %w", m.filename, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, lockTimeoutSQL); err != nil {
		return fmt.Errorf("bound lock wait for %s: %w", m.filename, err)
	}

	// No arguments, so pgx sends this over the simple protocol — which is
	// what lets one file hold several statements.
	if _, err := tx.Exec(ctx, m.sql); err != nil {
		return fmt.Errorf("apply %s: %w", m.filename, err)
	}
	if _, err := tx.Exec(ctx,
		"INSERT INTO schema_migrations (version, name, checksum) VALUES ($1, $2, $3)",
		m.version, m.name, m.checksum,
	); err != nil {
		return fmt.Errorf("record %s: %w", m.filename, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit %s: %w", m.filename, err)
	}
	return nil
}

// appliedMigration is one row of the ledger. The name and the time are here
// for the rows this binary has no file for, which is all it can say about a
// release it does not contain.
type appliedMigration struct {
	name      string
	checksum  string
	appliedAt time.Time
}

func appliedMigrations(ctx context.Context, conn *pgxpool.Conn) (map[int64]appliedMigration, error) {
	rows, err := conn.Query(ctx, "SELECT version, name, checksum, applied_at FROM schema_migrations")
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer rows.Close()

	out := make(map[int64]appliedMigration)
	for rows.Next() {
		var version int64
		var row appliedMigration
		if err := rows.Scan(&version, &row.name, &row.checksum, &row.appliedAt); err != nil {
			return nil, fmt.Errorf("scan schema_migrations: %w", err)
		}
		out[version] = row
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	return out, nil
}

// load parses and checksums the embedded migration files, in version order.
func load() ([]migration, error) {
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}

	out := make([]migration, 0, len(entries))
	seen := make(map[int64]string, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		m, err := parse(e.Name())
		if err != nil {
			return nil, err
		}
		if other, dup := seen[m.version]; dup {
			return nil, fmt.Errorf("migrations %s and %s share version %d", other, m.filename, m.version)
		}
		seen[m.version] = m.filename
		out = append(out, m)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

// parse reads one `NNNN_name.sql` file and checksums it.
func parse(filename string) (migration, error) {
	base := strings.TrimSuffix(filename, ".sql")
	num, name, ok := strings.Cut(base, "_")
	if !ok || name == "" {
		return migration{}, fmt.Errorf("migration %q must be named NNNN_name.sql", filename)
	}
	version, err := strconv.ParseInt(num, 10, 64)
	if err != nil || version <= 0 {
		return migration{}, fmt.Errorf("migration %q must start with a positive version number", filename)
	}

	body, err := migrations.FS.ReadFile(filename)
	if err != nil {
		return migration{}, fmt.Errorf("read %s: %w", filename, err)
	}
	sum := sha256.Sum256(body)

	return migration{
		version:  version,
		name:     name,
		filename: filename,
		sql:      string(body),
		checksum: hex.EncodeToString(sum[:]),
	}, nil
}
