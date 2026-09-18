package httpd

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/andybalholm/brotli"
)

// Asset is one immutable, content-addressed file held in memory.
//
// Everything is prepared once at startup: hashing names the file, and the
// compressed variants are built ahead of time so no request ever pays for
// compression. The payloads are small enough that holding three copies is
// cheaper than the CPU, and far cheaper than the round trips we are trying
// to avoid for distant clients.
type Asset struct {
	URL         string
	ContentType string
	ETag        string
	Raw         []byte
	Gzip        []byte
	Brotli      []byte
}

// Assets is the built asset set, keyed by logical name (e.g. "app.js").
type Assets struct {
	byName map[string]*Asset
	byURL  map[string]*Asset
}

// URL returns the served path for a logical asset name. It returns the name
// itself if unknown, which shows up as an obvious 404 rather than a silent
// blank page.
func (a *Assets) URL(name string) string {
	if as, ok := a.byName[name]; ok {
		return as.URL
	}
	return "/assets/" + name
}

// Lookup finds an asset by its served URL.
func (a *Assets) Lookup(url string) (*Asset, bool) {
	as, ok := a.byURL[url]
	return as, ok
}

// Names returns every served asset URL, for the service worker precache list.
func (a *Assets) Names() []string {
	out := make([]string, 0, len(a.byURL))
	for u := range a.byURL {
		out = append(out, u)
	}
	return out
}

var contentTypes = map[string]string{
	".js":          "text/javascript; charset=utf-8",
	".css":         "text/css; charset=utf-8",
	".woff2":       "font/woff2",
	".svg":         "image/svg+xml",
	".webmanifest": "application/manifest+json",
	".json":        "application/json",
	".html":        "text/html; charset=utf-8",
}

// compressible reports whether pre-compressing is worth it. woff2 and most
// image formats are already compressed; running them through gzip costs
// memory and gains nothing.
func compressible(ct string) bool {
	return strings.HasPrefix(ct, "text/") ||
		strings.Contains(ct, "javascript") ||
		strings.Contains(ct, "json") ||
		strings.Contains(ct, "svg")
}

func contentType(name string) string {
	if ct, ok := contentTypes[path.Ext(name)]; ok {
		return ct
	}
	return "application/octet-stream"
}

// NewDocument hashes and pre-compresses a body without giving it a
// content-addressed URL. Used for the files served at a fixed path — the HTML
// shell and the service worker — which must stay revalidatable but should
// still go over the wire compressed.
func NewDocument(name string, body []byte) *Asset {
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])[:8]
	ct := contentType(name)

	as := &Asset{
		ContentType: ct,
		ETag:        `"` + hash + `"`,
		Raw:         body,
	}
	as.compress()
	return as
}

// compress builds the encoded variants, keeping one only when it is smaller.
func (as *Asset) compress() {
	if !compressible(as.ContentType) {
		return
	}
	as.Gzip = gzipBytes(as.Raw)
	as.Brotli = brotliBytes(as.Raw)
	if len(as.Gzip) >= len(as.Raw) {
		as.Gzip = nil
	}
	if len(as.Brotli) >= len(as.Raw) {
		as.Brotli = nil
	}
}

// buildAsset hashes, compresses and registers one file under a
// content-addressed URL.
func (a *Assets) buildAsset(name string, body []byte) *Asset {
	as := NewDocument(name, body)
	hash := strings.Trim(as.ETag, `"`)
	ext := path.Ext(name)
	base := strings.TrimSuffix(name, ext)
	as.URL = fmt.Sprintf("/assets/%s.%s%s", base, hash, ext)

	a.byName[name] = as
	a.byURL[as.URL] = as
	return as
}

// BuildAssets prepares every static asset from the embedded source tree.
//
// Order matters: the font must be hashed before the stylesheet, because the
// stylesheet's url() reference is rewritten to the font's hashed path.
func BuildAssets(srcFS fs.FS) (*Assets, error) {
	a := &Assets{
		byName: make(map[string]*Asset),
		byURL:  make(map[string]*Asset),
	}

	// 1. Leaf assets that nothing else references by name.
	leaves := []string{"fonts/fraunces-display.woff2", "favicon.svg"}
	for _, name := range leaves {
		body, err := fs.ReadFile(srcFS, name)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		// Flatten the fonts/ prefix so the stylesheet's relative url()
		// resolves against /assets/ without a subdirectory.
		a.buildAsset(path.Base(name), body)
	}

	// 2. Stylesheet, with its font reference rewritten to the hashed URL.
	css, err := fs.ReadFile(srcFS, "styles.css")
	if err != nil {
		return nil, fmt.Errorf("read styles.css: %w", err)
	}
	css = bytes.ReplaceAll(css,
		[]byte("fraunces-display.woff2"),
		[]byte(path.Base(a.URL("fraunces-display.woff2"))),
	)
	a.buildAsset("styles.css", css)

	// 3. Scripts. Two of them, hashed and served the same way: the planner, and
	// the accounts surface that shares its page. Separate files because they
	// are separate things — a deployment with no database has no accounts at
	// all, and auth.js is then a script that finds a 404 and draws nothing,
	// rather than a branch inside the planner.
	for _, name := range []string{"app.js", "auth.js"} {
		js, err := fs.ReadFile(srcFS, name)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		a.buildAsset(name, js)
	}

	return a, nil
}

func gzipBytes(b []byte) []byte {
	var buf bytes.Buffer
	w, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil
	}
	if _, err := w.Write(b); err != nil {
		return nil
	}
	if err := w.Close(); err != nil {
		return nil
	}
	return buf.Bytes()
}

func brotliBytes(b []byte) []byte {
	var buf bytes.Buffer
	w := brotli.NewWriterLevel(&buf, brotli.BestCompression)
	if _, err := w.Write(b); err != nil {
		return nil
	}
	if err := w.Close(); err != nil {
		return nil
	}
	return buf.Bytes()
}
