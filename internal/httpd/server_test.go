package httpd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/Yornik/soiree/internal/config"
	"github.com/Yornik/soiree/web"
)

func newTestServer(t *testing.T, cfg config.Config) http.Handler {
	t.Helper()
	if cfg.Currency == "" {
		cfg.Currency = "EUR"
	}
	if cfg.Locale == "" {
		cfg.Locale = "en-US"
	}
	s, err := New(cfg, web.FS())
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	return s.Handler()
}

func get(t *testing.T, h http.Handler, path string, headers map[string]string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result()
}

func TestIndexRendersConfig(t *testing.T) {
	h := newTestServer(t, config.Config{
		EventName: "Ada's Retirement",
		Tagline:   "Dinner and speeches",
	})
	res := get(t, h, "/", nil)
	defer res.Body.Close()

	body, _ := io.ReadAll(res.Body)
	s := string(body)

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if !strings.Contains(s, "<title>Ada&#39;s Retirement</title>") && !strings.Contains(s, "Ada&#39;s Retirement") {
		t.Error("event name from config not rendered into the shell")
	}
	if !strings.Contains(s, "Dinner and speeches") {
		t.Error("tagline not rendered")
	}
	if strings.Contains(s, "{{") {
		t.Error("unrendered template directive left in the output")
	}
	if got := res.Header.Get("Cache-Control"); got != "no-cache" {
		t.Errorf("shell Cache-Control = %q, want no-cache so deploys land", got)
	}
}

// The config block must be a non-executable data block; a strict script-src
// CSP would otherwise refuse to run the page.
func TestConfigIsJSONDataBlock(t *testing.T) {
	h := newTestServer(t, config.Config{EventName: "X"})
	res := get(t, h, "/", nil)
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)

	if !strings.Contains(string(body), `<script type="application/json" id="soiree-config">`) {
		t.Error("config is not served as an application/json data block")
	}
}

// The page must never reach out to a third-party origin: an extra TLS
// handshake to a font CDN costs about a second for a distant client.
func TestNoExternalOrigins(t *testing.T) {
	h := newTestServer(t, config.Config{EventName: "X"})
	for _, path := range []string{"/", "/sw.js"} {
		res := get(t, h, path, nil)
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if m := regexp.MustCompile(`https?://[^"')\s]+`).FindString(string(body)); m != "" {
			t.Errorf("%s references an external origin: %s", path, m)
		}
	}
	// And the stylesheet, which is where a font import would hide.
	res := get(t, h, "/", nil)
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	cssURL := regexp.MustCompile(`/assets/styles\.[a-f0-9]+\.css`).FindString(string(body))
	if cssURL == "" {
		t.Fatal("no stylesheet URL in the shell")
	}
	cres := get(t, h, cssURL, nil)
	css, _ := io.ReadAll(cres.Body)
	cres.Body.Close()
	if strings.Contains(string(css), "http://") || strings.Contains(string(css), "https://") {
		t.Error("stylesheet references an external origin")
	}
}

func TestAssetsAreContentAddressedAndImmutable(t *testing.T) {
	h := newTestServer(t, config.Config{EventName: "X"})
	res := get(t, h, "/", nil)
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()

	urls := regexp.MustCompile(`/assets/[A-Za-z0-9._-]+`).FindAllString(string(body), -1)
	if len(urls) == 0 {
		t.Fatal("shell references no assets")
	}
	for _, u := range urls {
		ares := get(t, h, u, nil)
		ares.Body.Close()
		if ares.StatusCode != http.StatusOK {
			t.Errorf("%s -> %d", u, ares.StatusCode)
			continue
		}
		if cc := ares.Header.Get("Cache-Control"); !strings.Contains(cc, "immutable") {
			t.Errorf("%s Cache-Control = %q, want immutable", u, cc)
		}
		if ares.Header.Get("ETag") == "" {
			t.Errorf("%s has no ETag", u)
		}
	}
}

func TestConditionalRequestReturns304(t *testing.T) {
	h := newTestServer(t, config.Config{EventName: "X"})
	res := get(t, h, "/", nil)
	etag := res.Header.Get("ETag")
	res.Body.Close()
	if etag == "" {
		t.Fatal("shell has no ETag")
	}

	again := get(t, h, "/", map[string]string{"If-None-Match": etag})
	again.Body.Close()
	if again.StatusCode != http.StatusNotModified {
		t.Errorf("status = %d, want 304", again.StatusCode)
	}

	weak := get(t, h, "/", map[string]string{"If-None-Match": "W/" + etag})
	weak.Body.Close()
	if weak.StatusCode != http.StatusNotModified {
		t.Errorf("weak ETag: status = %d, want 304", weak.StatusCode)
	}
}

func TestCompressionNegotiation(t *testing.T) {
	h := newTestServer(t, config.Config{EventName: "X"})
	res := get(t, h, "/", nil)
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	cssURL := regexp.MustCompile(`/assets/styles\.[a-f0-9]+\.css`).FindString(string(body))

	plain := get(t, h, cssURL, nil)
	plainBody, _ := io.ReadAll(plain.Body)
	plain.Body.Close()
	if enc := plain.Header.Get("Content-Encoding"); enc != "" {
		t.Errorf("no Accept-Encoding should mean no encoding, got %q", enc)
	}

	for _, enc := range []string{"br", "gzip"} {
		cres := get(t, h, cssURL, map[string]string{"Accept-Encoding": enc})
		cbody, _ := io.ReadAll(cres.Body)
		cres.Body.Close()
		if got := cres.Header.Get("Content-Encoding"); got != enc {
			t.Errorf("Accept-Encoding %q -> Content-Encoding %q", enc, got)
		}
		if len(cbody) >= len(plainBody) {
			t.Errorf("%s body (%d) not smaller than identity (%d)", enc, len(cbody), len(plainBody))
		}
		if !strings.Contains(cres.Header.Get("Vary"), "Accept-Encoding") {
			t.Errorf("%s response missing Vary: Accept-Encoding", enc)
		}
	}
}

// woff2 is already compressed; re-encoding it wastes memory for nothing.
func TestFontIsNotRecompressed(t *testing.T) {
	h := newTestServer(t, config.Config{EventName: "X"})
	res := get(t, h, "/", nil)
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()

	fontURL := regexp.MustCompile(`/assets/fraunces-display\.[a-f0-9]+\.woff2`).FindString(string(body))
	if fontURL == "" {
		t.Fatal("no font URL in the shell")
	}
	fres := get(t, h, fontURL, map[string]string{"Accept-Encoding": "br, gzip"})
	fres.Body.Close()
	if enc := fres.Header.Get("Content-Encoding"); enc != "" {
		t.Errorf("font Content-Encoding = %q, want none", enc)
	}
	if ct := fres.Header.Get("Content-Type"); ct != "font/woff2" {
		t.Errorf("font Content-Type = %q", ct)
	}
}

// The stylesheet's url() must point at the hashed font, or the font 404s.
func TestStylesheetFontReferenceIsHashed(t *testing.T) {
	h := newTestServer(t, config.Config{EventName: "X"})
	res := get(t, h, "/", nil)
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()

	cssURL := regexp.MustCompile(`/assets/styles\.[a-f0-9]+\.css`).FindString(string(body))
	cres := get(t, h, cssURL, nil)
	css, _ := io.ReadAll(cres.Body)
	cres.Body.Close()

	ref := regexp.MustCompile(`url\('([^']+)'\)`).FindStringSubmatch(string(css))
	if ref == nil {
		t.Fatal("no url() in the stylesheet")
	}
	if !regexp.MustCompile(`^fraunces-display\.[a-f0-9]+\.woff2$`).MatchString(ref[1]) {
		t.Errorf("font reference %q is not content-addressed", ref[1])
	}
	if r := get(t, h, "/assets/"+ref[1], nil); r.StatusCode != http.StatusOK {
		r.Body.Close()
		t.Errorf("font referenced by the stylesheet 404s: %s", ref[1])
	}
}

func TestServiceWorkerPrecachesRealURLs(t *testing.T) {
	h := newTestServer(t, config.Config{EventName: "X"})
	res := get(t, h, "/sw.js", nil)
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()

	if res.Header.Get("Cache-Control") != "no-cache" {
		t.Error("sw.js must be revalidated, or a bad worker stays pinned forever")
	}
	urls := regexp.MustCompile(`/assets/[A-Za-z0-9._-]+`).FindAllString(string(body), -1)
	if len(urls) == 0 {
		t.Fatal("service worker precaches nothing")
	}
	for _, u := range urls {
		r := get(t, h, u, nil)
		r.Body.Close()
		if r.StatusCode != http.StatusOK {
			t.Errorf("precached %s -> %d", u, r.StatusCode)
		}
	}
}

func TestHealthzAndNotFound(t *testing.T) {
	h := newTestServer(t, config.Config{EventName: "X"})

	res := get(t, h, "/healthz", nil)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("/healthz -> %d", res.StatusCode)
	}

	for _, p := range []string{"/nope", "/assets/missing.deadbeef.js"} {
		r := get(t, h, p, nil)
		r.Body.Close()
		if r.StatusCode != http.StatusNotFound {
			t.Errorf("%s -> %d, want 404", p, r.StatusCode)
		}
	}
}

func TestSecurityHeaders(t *testing.T) {
	h := newTestServer(t, config.Config{EventName: "X"})
	res := get(t, h, "/", nil)
	res.Body.Close()
	for k, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	} {
		if got := res.Header.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}
