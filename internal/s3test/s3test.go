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
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Yornik/soiree/internal/objstore"
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

// How long Get waits for a started container to serve its bucket, and how
// often it asks. The wait is normally over on the first or second try; the
// minute is for a runner busy enough to have needed the wait at all. The key
// is under attachments/ like everything else here, and is only ever read.
const (
	readyPatience = time.Minute
	readyInterval = 200 * time.Millisecond
	probeKey      = "attachments/s3test-ready-probe"
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
// A container it started is handed out only once it serves; see awaitServing.
// A bucket named by the environment is taken as it is: somebody else runs it,
// and the first test says soon enough if it is not there.
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
		bucket, stop, failure = open(context.Background(), start, readyPatience)
	})
	if failure != nil {
		t.Fatalf("s3test: start: %v", failure)
	}
	return bucket
}

// open starts the container and returns its bucket once that bucket serves.
func open(ctx context.Context, start Starter, patience time.Duration) (Bucket, func(), error) {
	endpoint, s, err := start(ctx)
	if err != nil {
		return Bucket{}, func() {}, err
	}
	b := Bucket{
		Endpoint:        endpoint,
		Region:          ContainerRegion,
		Name:            ContainerBucket,
		AccessKeyID:     ContainerAccessKey,
		SecretAccessKey: ContainerSecretKey,
	}
	if err := awaitServing(ctx, b, patience); err != nil {
		// Get never hands this bucket out, so nothing would call Stop for it.
		s()
		return Bucket{}, func() {}, err
	}
	return b, s, nil
}

// awaitServing returns once the bucket answers a signed request as S3 would.
//
// The starters wait for /minio/health/ready, and that alone is not enough.
// MinIO answers it 200 as soon as the process is listening, while the object
// layer is still initialising behind it; all that says so is an
// `X-Minio-Server-Status: offline` header, which a wait on the status never
// reads. Until the object layer is up every S3 call is a 503, and on a machine
// that is starting every other package's containers at the same moment the
// first tests of a package land in that gap and fail with nothing wrong in
// the code.
//
// So the probe is the call the tests are about to make. A signed HEAD for an
// object that is not there comes back as ErrNotFound only after the signature
// has been verified and the object layer has been asked. An anonymous request
// is turned away before either, and a health endpoint is MinIO's to redefine.
func awaitServing(ctx context.Context, b Bucket, patience time.Duration) error {
	s, err := objstore.New(objstore.Config{
		Endpoint: b.Endpoint, Region: b.Region, Bucket: b.Name,
		AccessKeyID: b.AccessKeyID, SecretAccessKey: b.SecretAccessKey,
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, patience)
	defer cancel()

	var last error
	for {
		_, err := s.Head(ctx, probeKey)
		if err == nil || errors.Is(err, objstore.ErrNotFound) {
			return nil
		}
		// Once the patience has run out the error is only "deadline exceeded".
		// The answer before it is the one that says what was wrong.
		if last == nil || ctx.Err() == nil {
			last = err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("the bucket was still not serving after %s: %w", patience, last)
		case <-time.After(readyInterval):
		}
	}
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
