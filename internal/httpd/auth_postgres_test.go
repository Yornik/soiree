package httpd

import (
	"context"
	"fmt"
	"log"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/Yornik/soiree/internal/pgtest"
)

// The accounts surface is mostly about what the database refuses — a token
// redeemed twice, a session that belongs to a disabled account — so its HTTP
// tests talk to a real Postgres rather than to a fake store that would agree
// with whatever the handler asked of it.
func TestMain(m *testing.M) { pgtest.Main(m, startPostgres) }

// startPostgres brings up the throwaway database for this package's tests.
//
// It lives in a _test.go file rather than in package pgtest so that the Docker
// client's dependency tree is not reachable from `govulncheck ./...`. See the
// note on package pgtest, and the identical function in package store.
func startPostgres(ctx context.Context) (string, func(), error) {
	ctr, err := postgres.Run(ctx, pgtest.Image,
		postgres.WithDatabase("soiree"),
		postgres.WithUsername("soiree"),
		postgres.WithPassword("soiree"),
		postgres.BasicWaitStrategies(),
		testcontainers.WithCmd("postgres", "-c", "fsync=off",
			"-c", "full_page_writes=off", "-c", "synchronous_commit=off"),
	)

	stop := func() {}
	if ctr != nil {
		stop = func() {
			if err := testcontainers.TerminateContainer(ctr); err != nil {
				log.Printf("terminate postgres: %v", err)
			}
		}
	}

	if err != nil {
		stop()
		return "", nil, fmt.Errorf("run postgres: %w", err)
	}
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		stop()
		return "", nil, fmt.Errorf("connection string: %w", err)
	}
	return dsn, stop, nil
}
