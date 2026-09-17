// Package pgtest runs a package's tests against a throwaway PostgreSQL.
//
// The store is mostly SQL, and SQL is exactly the part a mock cannot check: a
// fake would happily accept a cascade that does not exist and a CHECK that
// never fires. So the tests talk to the real thing, at the same major version
// the cluster runs.
//
// One database is started per test binary and each test gets a freshly created
// database of its own inside it, which is what keeps the tests independent
// without paying for a container each.
//
// Note what this package does *not* import: the container library itself. That
// pulls in the Docker client and its whole dependency tree, and this is an
// ordinary package rather than a _test.go file, so anything it imports is
// reachable from `govulncheck ./...` and a CVE in the Docker client would fail
// CI on code that never ships. Starting the container is therefore the
// caller's job, passed in from a _test.go file where it belongs; everything
// fiddly about per-test isolation stays here and stays shared.
package pgtest

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Image is the container image the tests expect. It matches the major version
// of the CloudNativePG cluster in production, so the tests cannot pass on a
// version the real database would reject.
const Image = "postgres:18-alpine"

// Starter brings up a database and returns a connection string for it, along
// with a function that tears it down.
type Starter func(ctx context.Context) (dsn string, stop func(), err error)

var (
	adminDSN  string
	adminPool *pgxpool.Pool
	dbCounter atomic.Int64
)

// Main runs m against a database from start, or skips starting anything at all
// under -short so `go test -short ./...` needs no Docker. Call it from
// TestMain.
func Main(m *testing.M, start Starter) {
	// testing.Short panics if the flags have not been parsed, and TestMain
	// runs before the testing package would do it.
	flag.Parse()

	if testing.Short() {
		os.Exit(m.Run())
	}

	ctx := context.Background()
	dsn, stop, err := start(ctx)
	if err != nil {
		log.Fatalf("pgtest: start postgres: %v", err)
	}

	code := run(ctx, dsn, m)

	stop()
	os.Exit(code)
}

// run is split out so the pool closes before Main calls os.Exit, which skips
// deferred functions.
func run(ctx context.Context, dsn string, m *testing.M) int {
	adminDSN = dsn

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Printf("pgtest: connect: %v", err)
		return 1
	}
	defer pool.Close()
	adminPool = pool

	return m.Run()
}

// Pool returns a pool onto an empty database of its own, dropped when the test
// ends. It skips the test under -short.
func Pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("needs Docker; skipped under -short")
	}
	if adminPool == nil {
		t.Fatal("pgtest: no database — the package needs TestMain calling pgtest.Main")
	}

	ctx := t.Context()
	name := fmt.Sprintf("soiree_test_%d", dbCounter.Add(1))

	// Identifiers cannot be parameterised, hence the interpolation. The name
	// is generated here from a counter, never from test input.
	if _, err := adminPool.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("pgtest: create database: %v", err)
	}
	t.Cleanup(func() {
		// Detached from the test's context, which is already cancelled by the
		// time cleanups run. FORCE because a leaked connection would
		// otherwise keep the database alive for the rest of the run.
		dropCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := adminPool.Exec(dropCtx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Logf("pgtest: drop database %s: %v", name, err)
		}
	})

	cfg, err := pgxpool.ParseConfig(adminDSN)
	if err != nil {
		t.Fatalf("pgtest: parse dsn: %v", err)
	}
	cfg.ConnConfig.Database = name
	// Enough for the concurrent-migration test to hold several sessions at
	// once, and small enough that a leak shows up as a hang rather than as
	// drift.
	cfg.MaxConns = 8

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("pgtest: connect to %s: %v", name, err)
	}
	// Registered after the drop, so it runs before it: cleanups are LIFO and
	// DROP DATABASE wants the connections gone.
	t.Cleanup(pool.Close)

	return pool
}
