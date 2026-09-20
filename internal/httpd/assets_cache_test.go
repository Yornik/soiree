package httpd

import (
	"bytes"
	"io/fs"
	"testing"
	"time"

	"github.com/Yornik/soiree/internal/config"
	"github.com/Yornik/soiree/web"
	"github.com/andybalholm/brotli"
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

// A window that does not reach back over the whole file costs bytes that the
// compression is there to save, so whatever is picked has to cover the input
// and stay inside the range the encoder is asked for. The sizes worth checking
// are the ones just under a power of two, where the encoder's 16-byte gap is
// what decides it.
func TestTheBrotliWindowCoversTheInput(t *testing.T) {
	for _, n := range []int{0, 1, 1 << 10, (1 << 18) - 17, (1 << 18) - 16, 1 << 18, (1 << 22) - 17, 1 << 22, 1 << 24} {
		lgwin := brotliWindow(n)
		if lgwin < 18 || lgwin > 22 {
			t.Errorf("%d bytes: a window of %d is outside [18, 22]", n, lgwin)
		}
		// Above the ceiling the encoder cannot cover the input at all,
		// which is the one case where a short window is the answer.
		if covers := (1 << lgwin) - 16; covers < n && lgwin != 22 {
			t.Errorf("%d bytes: a window of %d reaches back over only %d of them", n, lgwin, covers)
		}
	}
}

// Sizing the window to the input is for the memory it saves at startup, and
// that is only free while it costs nothing on the wire. So: every asset this
// binary ships compresses to no more than the encoder's own 4 MiB window would
// give it.
func TestSizingTheBrotliWindowCostsNoBytes(t *testing.T) {
	src := web.FS()
	var checked int
	err := fs.WalkDir(src, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !compressible(contentType(name)) {
			return err
		}
		body, err := fs.ReadFile(src, name)
		if err != nil {
			return err
		}
		checked++
		if sized, wide := brotliBytes(body), brotliDefaultWindow(t, body); len(sized) > len(wide) {
			t.Errorf("%s: %d bytes at a window of %d, %d at the encoder's default; raise the floor in brotliWindow or spend the bytes deliberately",
				name, len(sized), brotliWindow(len(body)), len(wide))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk the embedded sources: %v", err)
	}
	// The scripts and the stylesheet are the ones a window can matter for.
	if checked < 3 {
		t.Fatalf("only %d compressible assets found; this test is no longer looking at the shell", checked)
	}
}

// brotliDefaultWindow compresses the way the package used to: at the same
// quality, with whatever window the encoder picks for itself.
func brotliDefaultWindow(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := brotli.NewWriterLevel(&buf, brotli.BestCompression)
	if _, err := w.Write(b); err != nil {
		t.Fatalf("compress: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("compress: %v", err)
	}
	return buf.Bytes()
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
