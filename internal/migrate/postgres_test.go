package migrate

import (
	"context"
	"fmt"
	"log"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/Yornik/soiree/internal/pgtest"
)

// startPostgres brings up the throwaway database for this package's tests.
//
// It lives in a _test.go file rather than in package pgtest so that the Docker
// client's dependency tree is not reachable from `govulncheck ./...`. See the
// note on package pgtest. The cost is this function existing twice, which is
// the cheaper half of the trade.
func startPostgres(ctx context.Context) (string, func(), error) {
	ctr, err := postgres.Run(ctx, pgtest.Image,
		postgres.WithDatabase("soiree"),
		postgres.WithUsername("soiree"),
		postgres.WithPassword("soiree"),
		postgres.BasicWaitStrategies(),
		// The data lives as long as the test binary does, so durability costs
		// real time and buys nothing.
		testcontainers.WithCmd("postgres", "-c", "fsync=off",
			"-c", "full_page_writes=off", "-c", "synchronous_commit=off"),
	)

	// Run can hand back a container alongside an error, and that container is
	// still running.
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
