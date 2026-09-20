package s3test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Yornik/soiree/internal/objstore"
)

// startingBucket stands in for a MinIO that is up but not yet serving: the
// first `unready` requests are answered 503, the way every S3 call is until
// the object layer has initialised, and the ones after that 404, which is S3
// for "no such object". No Docker, so these run under -short as well.
//
// It also refuses anything that is not a signed HEAD inside the bucket. What
// open has to prove is that the call the tests are about to make will be
// served, and a probe that went anywhere else would prove something easier.
func startingBucket(t *testing.T, unready int64) (Starter, *atomic.Int64, *atomic.Int64) {
	t.Helper()
	var asked, stopped atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead ||
			!strings.HasPrefix(r.URL.Path, "/"+ContainerBucket+"/attachments/") ||
			r.URL.Query().Get("X-Amz-Signature") == "" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if asked.Add(1) <= unready {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	start := func(context.Context) (string, func(), error) {
		return srv.URL, func() { stopped.Add(1) }, nil
	}
	return start, &asked, &stopped
}

func TestOpenWaitsUntilTheBucketServes(t *testing.T) {
	start, asked, stopped := startingBucket(t, 3)

	b, _, err := open(t.Context(), start, 30*time.Second)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	// What the first bucket test does with what Get hands it.
	s, err := objstore.New(objstore.Config{
		Endpoint: b.Endpoint, Region: b.Region, Bucket: b.Name,
		AccessKeyID: b.AccessKeyID, SecretAccessKey: b.SecretAccessKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Head(t.Context(), "attachments/test-first-call"); !errors.Is(err, objstore.ErrNotFound) {
		t.Fatalf("Head straight after open: %v, want ErrNotFound", err)
	}
	if n := asked.Load(); n != 5 {
		t.Errorf("the bucket was asked %d times, want 5: three refusals, the probe that got through, and the Head above", n)
	}
	if n := stopped.Load(); n != 0 {
		t.Errorf("a bucket that came up was stopped %d time(s)", n)
	}
}

func TestOpenGivesUpOnABucketThatNeverServes(t *testing.T) {
	start, _, stopped := startingBucket(t, 1<<62)

	_, _, err := open(t.Context(), start, 300*time.Millisecond)
	if err == nil {
		t.Fatal("open returned a bucket that answers 503 to everything")
	}
	// The status is the only clue to what went wrong, so it has to survive
	// into the message rather than be replaced by "context deadline exceeded".
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("error = %q, want the status the bucket kept answering", err)
	}
	// Get never hands this bucket out, so nothing would ever call Stop for it.
	if n := stopped.Load(); n != 1 {
		t.Errorf("the container was stopped %d time(s), want 1", n)
	}
}
