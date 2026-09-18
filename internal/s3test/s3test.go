// Package s3test gives a package's tests a real S3 bucket to talk to.
//
// Presigned URLs are exactly the kind of thing a fake cannot check. Whether a
// bucket refuses an upload of the wrong length, or honours a forced
// Content-Disposition, is decided by the server that verifies the signature —
// and a stub written by the author of the signer agrees with the signer by
// construction. So the tests talk to an S3 implementation that was written by
// somebody else.
//
// By default that is a throwaway MinIO in a container. Setting all five
// SOIREE_TEST_S3_* variables points the same tests at a bucket that already
// exists instead, which is how to find out whether a particular provider
// behaves the way this code needs before trusting it with anybody's files:
//
//	SOIREE_TEST_S3_ENDPOINT=https://nbg1.your-objectstorage.com \
//	SOIREE_TEST_S3_REGION=nbg1 SOIREE_TEST_S3_BUCKET=... \
//	SOIREE_TEST_S3_ACCESS_KEY_ID=... SOIREE_TEST_S3_SECRET_ACCESS_KEY=... \
//	go test ./internal/objstore -run Bucket -v
//
// The tests write only under the attachments/ prefix, with random names, and
// delete what they wrote.
//
// Like pgtest, this package does not import the container library: it is an
// ordinary package, so whatever it imports is reachable from `govulncheck
// ./...`. Starting the container is the caller's job, from a _test.go file.
package s3test

import (
	"context"
	"os"
	"sync"
	"testing"
)

// Image is MinIO, pinned by digest, from quay.io — the project no longer
// publishes to Docker Hub. It is a fixture, not a dependency of anything that
// ships: the claim under test is "this is how S3 behaves", and the published
// AWS signature example in internal/objstore is the other half of that claim.
const Image = "quay.io/minio/minio:RELEASE.2025-09-07T16-13-09Z@sha256:14cea493d9a34af32f524e538b8346cf79f3321eff8e708c1e2960462bd8936e"

// The container's fixed identity. Nothing here is a secret: the bucket lives
// for one test binary, on loopback.
const (
	ContainerAccessKey = "soiree-test"
	ContainerSecretKey = "soiree-test-secret"
	ContainerBucket    = "soiree-test"
	ContainerRegion    = "us-east-1"
)

// Entrypoint starts MinIO with the bucket already there. A directory under the
// data root is a bucket, so this saves the tests from needing a CreateBucket
// call — which the code under test has no reason to be able to make.
var Entrypoint = []string{"sh", "-c", "mkdir -p /data/" + ContainerBucket + " && exec minio server /data"}

// Bucket is everything needed to reach one.
type Bucket struct {
	Endpoint, Region, Name, AccessKeyID, SecretAccessKey string
	// External is true for a bucket named by the environment rather than
	// started here. Tests that would be rude to somebody's real bucket — or
	// that depend on a container's permissive CORS — can ask.
	External bool
}

// Starter brings up the container and returns its endpoint.
type Starter func(ctx context.Context) (endpoint string, stop func(), err error)

var (
	once    sync.Once
	bucket  Bucket
	stop    = func() {}
	failure error
)

// Get returns the bucket for this test binary, starting the container on first
// use. It skips the test under -short, exactly as pgtest.Pool does.
//
// Lazy rather than in TestMain, because most tests in a package that has
// attachment tests are not attachment tests, and should not wait for an image
// pull they will never use.
func Get(t *testing.T, start Starter) Bucket {
	t.Helper()
	if testing.Short() {
		t.Skip("needs Docker or a real bucket; skipped under -short")
	}
	once.Do(func() {
		if b, ok := fromEnv(); ok {
			bucket = b
			return
		}
		endpoint, s, err := start(context.Background())
		if err != nil {
			failure = err
			return
		}
		stop = s
		bucket = Bucket{
			Endpoint:        endpoint,
			Region:          ContainerRegion,
			Name:            ContainerBucket,
			AccessKeyID:     ContainerAccessKey,
			SecretAccessKey: ContainerSecretKey,
		}
	})
	if failure != nil {
		t.Fatalf("s3test: start: %v", failure)
	}
	return bucket
}

// Stop takes the container down, if this binary started one. For a TestMain
// that has the chance; a binary that does not call it is cleaned up by the
// container library's reaper when the process exits.
func Stop() { stop() }

func fromEnv() (Bucket, bool) {
	b := Bucket{
		Endpoint:        os.Getenv("SOIREE_TEST_S3_ENDPOINT"),
		Region:          os.Getenv("SOIREE_TEST_S3_REGION"),
		Name:            os.Getenv("SOIREE_TEST_S3_BUCKET"),
		AccessKeyID:     os.Getenv("SOIREE_TEST_S3_ACCESS_KEY_ID"),
		SecretAccessKey: os.Getenv("SOIREE_TEST_S3_SECRET_ACCESS_KEY"),
		External:        true,
	}
	if b.Endpoint == "" || b.Region == "" || b.Name == "" || b.AccessKeyID == "" || b.SecretAccessKey == "" {
		return Bucket{}, false
	}
	return b, true
}
