package objstore_test

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Yornik/soiree/internal/objstore"
	"github.com/Yornik/soiree/internal/s3test"
)

// These tests are the ones to run against a provider before trusting it; see
// package s3test for how. Each is one behaviour the attachment design leans
// on, and each is a behaviour an S3-compatible service is free to get wrong.

func TestMain(m *testing.M) {
	flag.Parse()
	code := m.Run()
	s3test.Stop()
	os.Exit(code)
}

func newBucket(t *testing.T) *objstore.Store {
	t.Helper()
	b := s3test.Get(t, startMinIO)
	s, err := objstore.New(objstore.Config{
		Endpoint: b.Endpoint, Region: b.Region, Bucket: b.Name,
		AccessKeyID: b.AccessKeyID, SecretAccessKey: b.SecretAccessKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// newKey names an object nothing else uses, and removes it afterwards whether
// or not the test got as far as creating it.
func newKey(t *testing.T, s *objstore.Store) string {
	t.Helper()
	key := "attachments/test-" + uuid.NewString()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.Delete(ctx, key); err != nil {
			t.Logf("cleanup %s: %v", key, err)
		}
	})
	return key
}

// put does what the browser does: one PUT, the given headers, that body.
func put(t *testing.T, up objstore.Upload, headers map[string]string, body []byte) int {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPut, up.URL, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	_, _ = io.Copy(io.Discard, res.Body)
	return res.StatusCode
}

func TestBucketRoundTrip(t *testing.T) {
	s := newBucket(t)
	key := newKey(t, s)
	body := []byte("a caterer's quote, or near enough\n")

	if _, err := s.Head(t.Context(), key); !errors.Is(err, objstore.ErrNotFound) {
		t.Fatalf("Head before upload: %v, want ErrNotFound", err)
	}

	up := s.PresignPut(key, int64(len(body)), "application/pdf", time.Minute)
	if code := put(t, up, up.Headers, body); code != http.StatusOK {
		t.Fatalf("PUT = %d, want 200", code)
	}

	size, err := s.Head(t.Context(), key)
	if err != nil || size != int64(len(body)) {
		t.Fatalf("Head = %d, %v; want %d", size, err, len(body))
	}

	// The download is served as the URL says, not as the object was stored:
	// that is the whole of the defence against a file that claims to be a web
	// page. The name has a space and a quote-worthy character on purpose.
	const disposition = `attachment; filename="floor plan.pdf"; filename*=UTF-8''floor%20plan%20caf%C3%A9.pdf`
	res, err := http.Get(s.PresignGet(key, time.Minute, disposition, "application/octet-stream"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	got, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || !bytes.Equal(got, body) {
		t.Fatalf("GET = %d, %q", res.StatusCode, got)
	}
	if h := res.Header.Get("Content-Disposition"); h != disposition {
		t.Errorf("Content-Disposition = %q, want %q", h, disposition)
	}
	if h := res.Header.Get("Content-Type"); h != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want the override, not the stored application/pdf", h)
	}

	if err := s.Delete(t.Context(), key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Head(t.Context(), key); !errors.Is(err, objstore.ErrNotFound) {
		t.Fatalf("Head after delete: %v, want ErrNotFound", err)
	}
	// The sweeper may ask twice.
	if err := s.Delete(t.Context(), key); err != nil {
		t.Fatalf("second Delete: %v", err)
	}
}

// The quota rests on this. A URL signed for ten bytes must not store eleven,
// or the declared size is a suggestion and the cap is whatever people choose.
func TestBucketRefusesAnotherLength(t *testing.T) {
	s := newBucket(t)
	for name, body := range map[string][]byte{
		"longer":  []byte("12345678901"),
		"shorter": []byte("123456789"),
	} {
		t.Run(name, func(t *testing.T) {
			key := newKey(t, s)
			up := s.PresignPut(key, 10, "text/plain", time.Minute)
			if code := put(t, up, up.Headers, body); code/100 == 2 {
				t.Fatalf("PUT of %d bytes to a URL signed for 10 = %d, want a refusal", len(body), code)
			}
			if _, err := s.Head(t.Context(), key); !errors.Is(err, objstore.ErrNotFound) {
				t.Fatalf("an object exists after the refused upload: %v", err)
			}
		})
	}
}

func TestBucketRefusesAnotherType(t *testing.T) {
	s := newBucket(t)
	key := newKey(t, s)
	up := s.PresignPut(key, 4, "image/png", time.Minute)
	if code := put(t, up, map[string]string{"Content-Type": "text/html"}, []byte("<p>x")); code/100 == 2 {
		t.Fatalf("PUT as text/html to a URL signed for image/png = %d, want a refusal", code)
	}
}

// Whoever holds a download URL must not be able to turn "save this" into
// "render this".
func TestBucketRefusesATamperedDisposition(t *testing.T) {
	s := newBucket(t)
	key := newKey(t, s)
	up := s.PresignPut(key, 4, "text/html", time.Minute)
	if code := put(t, up, up.Headers, []byte("<p>x")); code != http.StatusOK {
		t.Fatalf("PUT = %d", code)
	}

	good := s.PresignGet(key, time.Minute, "attachment", "application/octet-stream")
	bad := strings.Replace(good, "response-content-disposition=attachment", "response-content-disposition=inline", 1)
	if bad == good {
		t.Fatal("the URL did not contain the disposition to tamper with")
	}
	for name, c := range map[string]struct {
		url  string
		want bool
	}{"as signed": {good, true}, "tampered": {bad, false}} {
		res, err := http.Get(c.url)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if ok := res.StatusCode == http.StatusOK; ok != c.want {
			t.Errorf("%s: GET = %d", name, res.StatusCode)
		}
	}
}

func TestBucketRefusesAnExpiredURL(t *testing.T) {
	s := newBucket(t)
	key := newKey(t, s)
	up := s.PresignPut(key, 1, "text/plain", time.Minute)
	if code := put(t, up, up.Headers, []byte("x")); code != http.StatusOK {
		t.Fatalf("PUT = %d", code)
	}

	s.SetClock(func() time.Time { return time.Now().Add(-time.Hour) })
	res, err := http.Get(s.PresignGet(key, time.Minute, "attachment", ""))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode/100 == 2 {
		t.Fatalf("GET with a URL that expired 59 minutes ago = %d, want a refusal", res.StatusCode)
	}
}
