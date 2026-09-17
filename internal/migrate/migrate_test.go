package migrate

import (
	"context"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Yornik/soiree/internal/pgtest"
)

func TestMain(m *testing.M) { pgtest.Main(m, startPostgres) }

// TestLoad needs no database: a malformed filename should fail the build's
// tests, not the first production startup after a deploy.
func TestLoad(t *testing.T) {
	ms, err := load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(ms) == 0 {
		t.Fatal("no migrations embedded")
	}

	var prev int64
	for _, m := range ms {
		if m.version <= prev {
			t.Errorf("versions out of order at %s: %d after %d", m.filename, m.version, prev)
		}
		if m.checksum == "" || m.sql == "" {
			t.Errorf("%s: empty checksum or body", m.filename)
		}
		prev = m.version
	}
}

func TestRunAppliesSchema(t *testing.T) {
	pool := pgtest.Pool(t)
	ctx := t.Context()

	applied, err := Run(ctx, pool, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	want, err := load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(applied) != len(want) {
		t.Fatalf("applied %d migrations, want %d", len(applied), len(want))
	}

	for _, table := range []string{
		"users", "settings", "phases", "sponsors", "budget_items",
		"budget_item_sponsors", "tasks", "notes", "user_ui_prefs",
		"programme_entries", "schema_migrations",
	} {
		var exists bool
		err := pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables
			                 WHERE table_schema = 'public' AND table_name = $1)`, table,
		).Scan(&exists)
		if err != nil {
			t.Fatalf("check %s: %v", table, err)
		}
		if !exists {
			t.Errorf("table %s was not created", table)
		}
	}

	// The settings singleton has to be seeded, or every read of it fails on a
	// database that was never written to.
	var settings int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM settings").Scan(&settings); err != nil {
		t.Fatalf("count settings: %v", err)
	}
	if settings != 1 {
		t.Errorf("settings has %d rows, want exactly 1", settings)
	}

	if held := advisoryLocksHeld(t, pool); held != 0 {
		t.Errorf("%d advisory locks still held after a successful run", held)
	}
}

func TestRunIsIdempotent(t *testing.T) {
	pool := pgtest.Pool(t)
	ctx := t.Context()

	if _, err := Run(ctx, pool, nil); err != nil {
		t.Fatalf("first run: %v", err)
	}
	second, err := Run(ctx, pool, nil)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(second) != 0 {
		t.Errorf("second run applied %v, want nothing", second)
	}

	assertLedgerComplete(t, pool)
}

// TestRunConcurrently is the reason the advisory lock exists: replicas start
// together, and without the lock two of them apply the same CREATE TABLE.
func TestRunConcurrently(t *testing.T) {
	pool := pgtest.Pool(t)
	ctx := t.Context()

	const replicas = 3
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		errs    []error
		applied int
		start   = make(chan struct{})
	)

	for range replicas {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // released together, so they race on the ledger too
			ran, err := Run(ctx, pool, nil)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
			}
			applied += len(ran)
		}()
	}
	close(start)
	wg.Wait()

	for _, err := range errs {
		t.Errorf("concurrent run failed: %v", err)
	}

	want, err := load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// Each migration is applied by exactly one of the racing replicas.
	if applied != len(want) {
		t.Errorf("migrations applied %d times in total, want %d", applied, len(want))
	}
	assertLedgerComplete(t, pool)
}

// TestRunRejectsModifiedMigration covers the case where a file that has
// already been applied somewhere gets edited: two databases would silently
// end up with different schemas.
func TestRunRejectsModifiedMigration(t *testing.T) {
	pool := pgtest.Pool(t)
	ctx := t.Context()

	if _, err := Run(ctx, pool, nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, err := pool.Exec(ctx,
		"UPDATE schema_migrations SET checksum = 'tampered' WHERE version = 1",
	); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	if _, err := Run(ctx, pool, nil); err == nil {
		t.Fatal("expected a checksum mismatch to refuse to start")
	}

	if held := advisoryLocksHeld(t, pool); held != 0 {
		t.Errorf("%d advisory locks still held after a failed run", held)
	}
}

func assertLedgerComplete(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	want, err := load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	rows, err := pool.Query(context.Background(),
		"SELECT version, checksum FROM schema_migrations ORDER BY version")
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	defer rows.Close()

	got := map[int64]string{}
	for rows.Next() {
		var v int64
		var sum string
		if err := rows.Scan(&v, &sum); err != nil {
			t.Fatalf("scan ledger: %v", err)
		}
		got[v] = sum
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read ledger: %v", err)
	}

	if len(got) != len(want) {
		t.Fatalf("ledger has %d rows, want %d", len(got), len(want))
	}
	for _, m := range want {
		if got[m.version] != m.checksum {
			t.Errorf("version %d recorded as %q, want %q", m.version, got[m.version], m.checksum)
		}
	}
}

// advisoryLocksHeld counts advisory locks in this test's own database. A lock
// that survives the run would block the next one for the life of the pooled
// connection, which is a hang in production and nothing at all in a test that
// does not look.
func advisoryLocksHeld(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM pg_locks
		  WHERE locktype = 'advisory'
		    AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`,
	).Scan(&n)
	if err != nil {
		t.Fatalf("count advisory locks: %v", err)
	}
	return n
}
