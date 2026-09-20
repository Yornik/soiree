package migrate

import (
	"context"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Yornik/soiree/internal/pgtest"
)

// seedThroughVersion is the schema seedFile was written against. The
// migrations up to here are applied, the fixture is loaded, and everything
// after it then runs over rows that already exist.
//
// It stays where it is. The fixture is frozen for the same reason the
// migrations behind it are, so raising this number would mean rewriting it
// against a newer schema and losing the thing it is for. A later migration
// that needs rows in a table schema 0011 does not have gets a second fixture
// at its own point instead.
const seedThroughVersion = 11

const seedFile = "testdata/seed_schema_0011.sql"

// TestRunUpgradesAPopulatedDatabase migrates a database that already holds
// rows, which is the only kind a deployment ever has and the one kind nothing
// else here covers: every database-backed test in this repository hands Run a
// database created moments earlier. A NOT NULL without a default, a unique
// index over values that already repeat, a backfill that reads its input
// wrongly: each of them applies perfectly to an empty schema and is met for
// the first time by the single database that matters.
func TestRunUpgradesAPopulatedDatabase(t *testing.T) {
	pool := pgtest.Pool(t)
	ctx := t.Context()

	remaining := seedThrough(t, pool)
	before := snapshot(t, pool)

	ran, err := Run(ctx, pool, nil)
	if err != nil {
		t.Fatalf("upgrade over existing rows: %v", err)
	}
	if !slices.Equal(ran, remaining) {
		t.Errorf("upgrade applied %v, want %v", ran, remaining)
	}

	// Not row counts alone. A backfill that succeeds with the wrong values
	// keeps every count exactly where it was, and that is the failure this
	// project cannot notice any other way: nobody compares the one production
	// database with what it held yesterday.
	for _, changed := range before.changed(t, pool) {
		t.Errorf("the upgrade altered rows it was not migrating: %s", changed)
	}

	assertLedgerComplete(t, pool)
}

// TestTheSeededRowsCatchWhatAnEmptySchemaCannot is the control for the test
// above. Both probes are migrations an empty database accepts without a
// murmur, and the fixture is only worth its upkeep for as long as it still
// refuses them.
func TestTheSeededRowsCatchWhatAnEmptySchemaCannot(t *testing.T) {
	t.Run("a constraint the existing rows violate", func(t *testing.T) {
		// The commonest shape of the accident: a column every row must have a
		// value for, added by somebody whose development database was empty.
		probe := migration{
			version:  9101,
			name:     "task_owner_id",
			filename: "9101_task_owner_id.sql",
			sql:      "ALTER TABLE tasks ADD COLUMN owner_id uuid NOT NULL",
			checksum: "probe",
		}

		empty := pgtest.Pool(t)
		if _, err := Run(t.Context(), empty, nil); err != nil {
			t.Fatalf("migrate an empty database: %v", err)
		}
		if err := applyProbe(t, empty, probe); err != nil {
			t.Fatalf("the probe has to be one an empty database accepts, or it proves nothing about the rows: %v", err)
		}

		if err := applyProbe(t, populatedPool(t), probe); err == nil {
			t.Error("a NOT NULL column with no default was accepted by a table that already has rows")
		}
	})

	t.Run("a backfill that succeeds with the wrong values", func(t *testing.T) {
		pool := populatedPool(t)
		before := snapshot(t, pool)

		// Succeeds everywhere, and leaves every row count where it was. Only
		// the values moved, which is what makes this the class of failure that
		// reaches production with a green suite behind it.
		probe := migration{
			version:  9102,
			name:     "vendor_backfill",
			filename: "9102_vendor_backfill.sql",
			sql:      "UPDATE budget_items SET vendor = ''",
			checksum: "probe",
		}
		if err := applyProbe(t, pool, probe); err != nil {
			t.Fatalf("apply the probe: %v", err)
		}

		changed := before.changed(t, pool)
		if !slices.ContainsFunc(changed, func(s string) bool { return strings.HasPrefix(s, "budget_items ") }) {
			t.Errorf("rewriting every vendor went unnoticed, reported %v", changed)
		}
	})
}

// populatedPool returns a pool onto a database at the current schema whose
// tables hold the fixture's rows, reached the way a deployment reaches it:
// an older schema, some use, and then an upgrade.
func populatedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := pgtest.Pool(t)
	seedThrough(t, pool)
	if _, err := Run(t.Context(), pool, nil); err != nil {
		t.Fatalf("upgrade over existing rows: %v", err)
	}
	return pool
}

// seedThrough applies the migrations up to seedThroughVersion, loads the
// fixture over them, and returns the versions it left for Run.
func seedThrough(t *testing.T, pool *pgxpool.Pool) []int64 {
	t.Helper()
	ctx := t.Context()

	all, err := load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	// Reaching past Run into load() and apply(), because Run deliberately has
	// no "up to version N": a replica applies everything it carries, and an
	// option to stop halfway would be a way to deploy half a schema.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if _, err := conn.Exec(ctx, ledgerDDL); err != nil {
		conn.Release()
		t.Fatalf("create the ledger: %v", err)
	}
	var remaining []int64
	for _, m := range all {
		if m.version > seedThroughVersion {
			remaining = append(remaining, m.version)
			continue
		}
		if err := apply(ctx, conn, m); err != nil {
			conn.Release()
			t.Fatalf("apply %s: %v", m.filename, err)
		}
	}
	conn.Release()

	if len(remaining) == 0 {
		t.Fatalf("nothing is newer than schema %d, so this upgrades nothing: add a fixture at a later point", seedThroughVersion)
	}

	seed, err := os.ReadFile(seedFile)
	if err != nil {
		t.Fatalf("read %s: %v", seedFile, err)
	}
	// No arguments, so pgx sends this over the simple protocol, which is what
	// lets one file hold many statements. It is the same reason apply() sends
	// a migration that way.
	if _, err := pool.Exec(ctx, string(seed)); err != nil {
		t.Fatalf("load %s: %v", seedFile, err)
	}

	// A fixture that quietly stopped filling a table would leave the upgrade
	// running over an empty one again, and every assertion in this file would
	// still pass.
	for table, s := range snapshot(t, pool) {
		if s.rows == 0 {
			t.Errorf("%s has no rows after %s, so nothing upgrades over it", table, seedFile)
		}
	}

	return remaining
}

// applyProbe runs one synthetic migration, the way Run would.
//
// A probe stands for the next migration somebody writes, so it is a value
// rather than a file: an embedded one would be checksummed, released and
// applied to every database there is.
func applyProbe(t *testing.T, pool *pgxpool.Pool, m migration) error {
	t.Helper()
	conn, err := pool.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer conn.Release()
	return apply(context.Background(), conn, m)
}

// tableSnapshot is what one table held at a moment: the columns it had then,
// how many rows, and a digest over the values in those columns.
type tableSnapshot struct {
	columns []string
	rows    int
	digest  string
}

type schemaSnapshot map[string]tableSnapshot

// snapshot records every table except the ledger, which Run writes to by
// design.
func snapshot(t *testing.T, pool *pgxpool.Pool) schemaSnapshot {
	t.Helper()
	ctx := context.Background()

	rows, err := pool.Query(ctx,
		`SELECT table_name FROM information_schema.tables
		  WHERE table_schema = 'public' AND table_type = 'BASE TABLE'
		    AND table_name <> 'schema_migrations'
		  ORDER BY table_name`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("list tables: %v", err)
	}

	out := make(schemaSnapshot, len(tables))
	for _, table := range tables {
		s := tableSnapshot{columns: columnsOf(t, pool, table)}
		s.rows, s.digest = digestOf(t, pool, table, s.columns)
		out[table] = s
	}
	return out
}

// changed re-reads what the snapshot recorded and describes every table whose
// rows are no longer the same.
//
// It reads the columns the snapshot was taken over rather than whatever the
// table has now, so a migration that adds a column is not reported as having
// rewritten every row. The next person would answer that noise by weakening
// the assertion, and the assertion is the whole test.
func (before schemaSnapshot) changed(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()

	var out []string
	for _, table := range slices.Sorted(maps.Keys(before)) {
		was := before[table]
		rows, digest := digestOf(t, pool, table, was.columns)
		switch {
		case rows != was.rows:
			out = append(out, fmt.Sprintf("%s holds %d rows, held %d", table, rows, was.rows))
		case digest != was.digest:
			out = append(out, fmt.Sprintf("%s holds its %d rows still, but their values changed", table, rows))
		}
	}
	return out
}

func columnsOf(t *testing.T, pool *pgxpool.Pool, table string) []string {
	t.Helper()

	rows, err := pool.Query(context.Background(),
		`SELECT column_name FROM information_schema.columns
		  WHERE table_schema = 'public' AND table_name = $1
		  ORDER BY ordinal_position`, table)
	if err != nil {
		t.Fatalf("columns of %s: %v", table, err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan column of %s: %v", table, err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("columns of %s: %v", table, err)
	}
	return out
}

// digestOf counts the rows in one table and hashes their values.
//
// Ordered by the digest rather than by a key, because not every table here has
// one worth ordering by: budget_item_sponsors is two foreign keys and nothing
// else, and a migration is free to rewrite a table in any order it likes.
func digestOf(t *testing.T, pool *pgxpool.Pool, table string, columns []string) (int, string) {
	t.Helper()

	// Identifiers cannot be parameterised. These come from the catalogue of
	// this test's own database, never from test input.
	quoted := make([]string, 0, len(columns))
	for _, column := range columns {
		quoted = append(quoted, pgx.Identifier{column}.Sanitize())
	}
	query := fmt.Sprintf(
		`SELECT count(*), coalesce(md5(string_agg(d, '' ORDER BY d)), '')
		   FROM (SELECT md5(to_jsonb(r)::text) AS d
		           FROM (SELECT %s FROM %s) AS r) AS digests`,
		strings.Join(quoted, ", "), pgx.Identifier{table}.Sanitize())

	var rows int
	var digest string
	if err := pool.QueryRow(context.Background(), query).Scan(&rows, &digest); err != nil {
		t.Fatalf("digest %s: %v", table, err)
	}
	return rows, digest
}
