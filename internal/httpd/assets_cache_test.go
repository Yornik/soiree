package httpd

import (
	"bytes"
	"testing"
	"time"

	"github.com/Yornik/soiree/internal/config"
	"github.com/Yornik/soiree/web"
)

// The cache must be invisible: what it hands back is, byte for byte, what
// compressing again would have produced, and one input never answers for
// another. The reproducible-build check rests on the first of those.
func TestCompressedOutputIsWhatCompressingAgainWouldGive(t *testing.T) {
	a := bytes.Repeat([]byte("a planner is mostly the same words over and over. "), 200)
	b := bytes.Repeat([]byte("a different file, of the same length as the first. "), 200)[:len(a)]

	for name, c := range map[string]struct {
		cached, fresh func([]byte) []byte
	}{
		"brotli": {brotliBytes, brotliOnce},
		"gzip":   {gzipBytes, gzipOnce},
	} {
		first, again := c.cached(a), c.cached(a)
		if !bytes.Equal(first, c.fresh(a)) {
			t.Errorf("%s: the cached output differs from a fresh compression", name)
		}
		if !bytes.Equal(first, again) {
			t.Errorf("%s: the same input gave two answers", name)
		}
		if bytes.Equal(c.cached(b), first) {
			t.Errorf("%s: a different input of the same length got the first one's bytes", name)
		}
		if len(first) == 0 || len(first) >= len(a) {
			t.Errorf("%s: %d bytes in, %d out", name, len(a), len(first))
		}
	}
}

// The point of it. Building the first server pays for the compression; every
// one after it must not, or this package is back to ten minutes under -race.
func TestASecondServerDoesNotCompressAgain(t *testing.T) {
	cfg := config.Config{EventName: "Ada's Leaving Do", Currency: "EUR", Locale: "en-US"}
	build := func() time.Duration {
		start := time.Now()
		if _, err := New(cfg, web.FS()); err != nil {
			t.Fatal(err)
		}
		return time.Since(start)
	}
	first := build() // may already be warm if another test ran first; then both are fast
	second := build()
	t.Logf("first %v, second %v", first, second)

	// Generous: the rest of New() is parsing a template and hashing a few
	// files. Compressing again costs hundreds of milliseconds, seconds under
	// the race detector.
	if second > 250*time.Millisecond {
		t.Errorf("a second server took %v to build; it is compressing its assets again", second)
	}
}
