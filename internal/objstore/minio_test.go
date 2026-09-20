package objstore_test

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/Yornik/soiree/internal/s3test"
)

// startMinIO brings up the throwaway bucket for this package's tests.
//
// In a _test.go file rather than in package s3test for the reason
// startPostgres is: the Docker client's dependency tree stays out of reach of
// `govulncheck ./...`. See the note on package pgtest.
func startMinIO(ctx context.Context) (string, func(), error) {
	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        s3test.Image,
			Entrypoint:   s3test.Entrypoint,
			ExposedPorts: []string{"9000/tcp"},
			Env: map[string]string{
				"MINIO_ROOT_USER":     s3test.ContainerAccessKey,
				"MINIO_ROOT_PASSWORD": s3test.ContainerSecretKey,
			},
			// That the process is up, and no more than that: MinIO answers
			// this before it can serve S3. s3test.Get waits for the rest.
			WaitingFor: wait.ForHTTP("/minio/health/ready").WithPort("9000/tcp").
				WithStartupTimeout(3 * time.Minute),
		},
		Started: true,
	})

	// As with postgres.Run: a container can come back alongside an error, and
	// it is still running.
	stop := func() {}
	if ctr != nil {
		stop = func() {
			if err := testcontainers.TerminateContainer(ctr); err != nil {
				log.Printf("terminate minio: %v", err)
			}
		}
	}
	if err != nil {
		stop()
		return "", nil, fmt.Errorf("run minio: %w", err)
	}
	endpoint, err := ctr.PortEndpoint(ctx, "9000/tcp", "http")
	if err != nil {
		stop()
		return "", nil, fmt.Errorf("minio endpoint: %w", err)
	}
	return endpoint, stop, nil
}
