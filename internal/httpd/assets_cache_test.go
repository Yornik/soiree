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
//
// Asserted on the bytes rather than on how long the second build took. The
// cache hands back the very slice it stored, so a variant that starts at the
// same address as the first server's came out of the cache, while a fresh
// compression writes a buffer of its own. This used to be a bound on the
// second build's wall clock, which measures the machine rather than the cache:
// a loaded one can cross it with nothing compressed at all, and the failure
// then blames a compression that did not happen.
func TestASecondServerDoesNotCompressAgain(t *testing.T) {
	cfg := config.Config{EventName: "Ada's Leaving Do", Currency: "EUR", Locale: "en-US"}
	build := func() (*Server, time.Duration) {
		start := time.Now()
		s, err := New(cfg, web.FS())
		if err != nil {
			t.Fatal(err)
		}
		return s, time.Since(start)
	}
	first, firstTook := build()
	second, secondTook := build()
	t.Logf("first %v, second %v", firstTook, secondTook)

	// Counted, because a variant the encoder made no smaller is dropped and
	// one for an image or the font was never built at all: neither says
	// anything about the cache, and a check left comparing nothing would pass
	// in silence.
	var checked int
	same := func(name, encoding string, a, b []byte) {
		t.Helper()
		if len(a) == 0 {
			return
		}
		checked++
		if len(b) == 0 {
			t.Errorf("%s: the first server has %s bytes for it and the second none", name, encoding)
			return
		}
		if &a[0] != &b[0] {
			t.Errorf("%s: the second server's %s bytes are a buffer of their own; it compressed the asset again", name, encoding)
		}
	}
	for name, as := range first.assets.byName {
		again, ok := second.assets.byName[name]
		if !ok {
			t.Errorf("%s: the second server did not build it at all", name)
			continue
		}
		same(name, "gzip", as.Gzip, again.Gzip)
		same(name, "brotli", as.Brotli, again.Brotli)
	}
	// The files New() renders from the configuration are compressed there
	// rather than in BuildAssets, so they are the part of the startup cost the
	// map above cannot see.
	for name, pair := range map[string][2]*Asset{
		"the shell":          {first.index, second.index},
		"the service worker": {first.sw, second.sw},
		"robots.txt":         {first.robots, second.robots},
	} {
		same(name, "gzip", pair[0].Gzip, pair[1].Gzip)
		same(name, "brotli", pair[0].Brotli, pair[1].Brotli)
	}
	// Fourteen variants as the assets stand, both encodings of the scripts,
	// the stylesheet and the shell among them. Those are the files the seconds
	// were spent on, so a count that falls much below it means the check has
	// stopped looking at them.
	if checked < 12 {
		t.Errorf("only %d compressed variants compared; this test is no longer looking at the shell", checked)
	}
}
