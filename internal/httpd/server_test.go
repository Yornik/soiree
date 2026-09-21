package httpd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/Yornik/soiree/internal/config"
	"github.com/Yornik/soiree/internal/objstore"
	"github.com/Yornik/soiree/web"
)

func newServer(t *testing.T, cfg config.Config) *Server {
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
	return s
}

func newTestServer(t *testing.T, cfg config.Config) http.Handler {
	t.Helper()
	return newServer(t, cfg).Handler()
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
	defer func() { _ = res.Body.Close() }()

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
	defer func() { _ = res.Body.Close() }()
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
		_ = res.Body.Close()
		if m := regexp.MustCompile(`https?://[^"')\s]+`).FindString(string(body)); m != "" {
			t.Errorf("%s references an external origin: %s", path, m)
		}
	}
	// And the stylesheet, which is where a font import would hide.
	res := get(t, h, "/", nil)
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	cssURL := regexp.MustCompile(`/assets/styles\.[a-f0-9]+\.css`).FindString(string(body))
	if cssURL == "" {
		t.Fatal("no stylesheet URL in the shell")
	}
	cres := get(t, h, cssURL, nil)
	css, _ := io.ReadAll(cres.Body)
	_ = cres.Body.Close()
	if strings.Contains(string(css), "http://") || strings.Contains(string(css), "https://") {
		t.Error("stylesheet references an external origin")
	}
}

func TestAssetsAreContentAddressedAndImmutable(t *testing.T) {
	h := newTestServer(t, config.Config{EventName: "X"})
	res := get(t, h, "/", nil)
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()

	urls := regexp.MustCompile(`/assets/[A-Za-z0-9._-]+`).FindAllString(string(body), -1)
	if len(urls) == 0 {
		t.Fatal("shell references no assets")
	}
	for _, u := range urls {
		ares := get(t, h, u, nil)
		_ = ares.Body.Close()
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
	_ = res.Body.Close()
	if etag == "" {
		t.Fatal("shell has no ETag")
	}

	again := get(t, h, "/", map[string]string{"If-None-Match": etag})
	_ = again.Body.Close()
	if again.StatusCode != http.StatusNotModified {
		t.Errorf("status = %d, want 304", again.StatusCode)
	}

	weak := get(t, h, "/", map[string]string{"If-None-Match": "W/" + etag})
	_ = weak.Body.Close()
	if weak.StatusCode != http.StatusNotModified {
		t.Errorf("weak ETag: status = %d, want 304", weak.StatusCode)
	}
}

func TestCompressionNegotiation(t *testing.T) {
	h := newTestServer(t, config.Config{EventName: "X"})
	res := get(t, h, "/", nil)
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	cssURL := regexp.MustCompile(`/assets/styles\.[a-f0-9]+\.css`).FindString(string(body))

	plain := get(t, h, cssURL, nil)
	plainBody, _ := io.ReadAll(plain.Body)
	_ = plain.Body.Close()
	if enc := plain.Header.Get("Content-Encoding"); enc != "" {
		t.Errorf("no Accept-Encoding should mean no encoding, got %q", enc)
	}

	for _, enc := range []string{"br", "gzip"} {
		cres := get(t, h, cssURL, map[string]string{"Accept-Encoding": enc})
		cbody, _ := io.ReadAll(cres.Body)
		_ = cres.Body.Close()
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
	_ = res.Body.Close()

	fontURL := regexp.MustCompile(`/assets/bricolage-display\.[a-f0-9]+\.woff2`).FindString(string(body))
	if fontURL == "" {
		t.Fatal("no font URL in the shell")
	}
	fres := get(t, h, fontURL, map[string]string{"Accept-Encoding": "br, gzip"})
	_ = fres.Body.Close()
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
	_ = res.Body.Close()

	cssURL := regexp.MustCompile(`/assets/styles\.[a-f0-9]+\.css`).FindString(string(body))
	cres := get(t, h, cssURL, nil)
	css, _ := io.ReadAll(cres.Body)
	_ = cres.Body.Close()

	ref := regexp.MustCompile(`url\('([^']+)'\)`).FindStringSubmatch(string(css))
	if ref == nil {
		t.Fatal("no url() in the stylesheet")
	}
	if !regexp.MustCompile(`^bricolage-display\.[a-f0-9]+\.woff2$`).MatchString(ref[1]) {
		t.Errorf("font reference %q is not content-addressed", ref[1])
	}
	if r := get(t, h, "/assets/"+ref[1], nil); r.StatusCode != http.StatusOK {
		_ = r.Body.Close()
		t.Errorf("font referenced by the stylesheet 404s: %s", ref[1])
	}
}

func TestServiceWorkerPrecachesRealURLs(t *testing.T) {
	h := newTestServer(t, config.Config{EventName: "X"})
	res := get(t, h, "/sw.js", nil)
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()

	if res.Header.Get("Cache-Control") != "no-cache" {
		t.Error("sw.js must be revalidated, or a bad worker stays pinned forever")
	}
	urls := regexp.MustCompile(`/assets/[A-Za-z0-9._-]+`).FindAllString(string(body), -1)
	if len(urls) == 0 {
		t.Fatal("service worker precaches nothing")
	}
	for _, u := range urls {
		r := get(t, h, u, nil)
		_ = r.Body.Close()
		if r.StatusCode != http.StatusOK {
			t.Errorf("precached %s -> %d", u, r.StatusCode)
		}
	}
}

func TestHealthzAndNotFound(t *testing.T) {
	h := newTestServer(t, config.Config{EventName: "X"})

	res := get(t, h, "/healthz", nil)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("/healthz -> %d", res.StatusCode)
	}

	for _, p := range []string{"/nope", "/assets/missing.deadbeef.js"} {
		r := get(t, h, p, nil)
		_ = r.Body.Close()
		if r.StatusCode != http.StatusNotFound {
			t.Errorf("%s -> %d, want 404", p, r.StatusCode)
		}
	}
}

// Readiness stays on the main listener, because that is the port kubelet
// reaches. The metrics exposition does not, and is asserted through the
// handler the private listener is built from.
func TestReadyzAndMetrics(t *testing.T) {
	s := newServer(t, config.Config{EventName: "X"})
	h, mh := s.Handler(), s.MetricsHandler()

	ready := get(t, h, "/readyz", nil)
	_ = ready.Body.Close()
	if ready.StatusCode != http.StatusOK {
		t.Errorf("/readyz -> %d", ready.StatusCode)
	}

	// Drive a request through first so the counters have something in them.
	warm := get(t, h, "/", nil)
	_ = warm.Body.Close()

	res := get(t, mh, "/metrics", nil)
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("/metrics -> %d", res.StatusCode)
	}
	for _, want := range []string{
		"soiree_http_requests_total",
		"soiree_http_request_duration_seconds",
		"soiree_build_info",
		"go_goroutines",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("metrics output missing %s", want)
		}
	}
	if !strings.Contains(string(body), `route="shell"`) {
		t.Error("expected the shell request to be counted under route=shell")
	}
}

// The public ingress route has no path constraint, so anything on the main
// handler is world-readable — and soiree_build_info names the running version
// and commit. This is the assertion that keeps it off.
func TestMetricsIsNotOnThePublicHandler(t *testing.T) {
	s := newServer(t, config.Config{EventName: "X"})

	res := get(t, s.Handler(), "/metrics", nil)
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("/metrics on the public handler -> %d, want 404", res.StatusCode)
	}
	if strings.Contains(string(body), "soiree_build_info") {
		t.Error("the public handler served the metrics exposition")
	}

	// The private handler carries that path and nothing else: an operator who
	// pointed a probe at the metrics port would otherwise get a 200 from it and
	// never learn the port was wrong.
	for _, p := range []string{"/", "/healthz", "/readyz"} {
		r := get(t, s.MetricsHandler(), p, nil)
		_ = r.Body.Close()
		if r.StatusCode != http.StatusNotFound {
			t.Errorf("%s on the metrics handler -> %d, want 404", p, r.StatusCode)
		}
	}
}

// A probe of the public /metrics is a 404, but it must still be counted under
// route="metrics" rather than lumped into "other": that counter is how a stale
// ServiceMonitor, or a scanner walking the well-known paths, becomes visible.
func TestPublicMetricsProbesAreStillCounted(t *testing.T) {
	s := newServer(t, config.Config{EventName: "X"})

	probe := get(t, s.Handler(), "/metrics", nil)
	_ = probe.Body.Close()

	m := get(t, s.MetricsHandler(), "/metrics", nil)
	mb, _ := io.ReadAll(m.Body)
	_ = m.Body.Close()

	// Label order in the exposition is the client library's business, so match
	// the line rather than a fixed rendering of it.
	var counted bool
	for _, line := range strings.Split(string(mb), "\n") {
		if strings.HasPrefix(line, "soiree_http_requests_total{") &&
			strings.Contains(line, `route="metrics"`) &&
			strings.Contains(line, `status="404"`) {
			counted = true
		}
	}
	if !counted {
		t.Errorf("a probe of the public /metrics was not counted as a 404 under route=metrics:\n%s", mb)
	}
}

// Asset URLs carry a content hash. Labelling metrics by raw path would create
// a new time series on every deploy, so the route label must stay bounded.
func TestMetricsRouteLabelIsBounded(t *testing.T) {
	s := newServer(t, config.Config{EventName: "X"})
	h := s.Handler()

	res := get(t, h, "/", nil)
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	for _, u := range regexp.MustCompile(`/assets/[A-Za-z0-9._-]+`).FindAllString(string(body), -1) {
		r := get(t, h, u, nil)
		_ = r.Body.Close()
	}

	m := get(t, s.MetricsHandler(), "/metrics", nil)
	mb, _ := io.ReadAll(m.Body)
	_ = m.Body.Close()

	if strings.Contains(string(mb), `route="/assets/`) {
		t.Error("raw asset paths leaked into the route label")
	}
	if !strings.Contains(string(mb), `route="asset"`) {
		t.Error("asset requests were not classified under route=asset")
	}
}

// wantCSP is the policy a deployment with no bucket gets, spelled out rather
// than built from the same helper the server uses: a policy asserted against
// its own generator asserts nothing, and this is the one header whose exact
// text decides whether the page runs.
const wantCSP = "default-src 'self'; base-uri 'self'; frame-ancestors 'none'; " +
	"form-action 'self'; object-src 'none'; script-src 'self'; style-src 'self'; " +
	"img-src 'self' data:; font-src 'self'; connect-src 'self'"

func TestSecurityHeaders(t *testing.T) {
	h := newTestServer(t, config.Config{EventName: "X"})
	res := get(t, h, "/", nil)
	_ = res.Body.Close()
	for k, want := range map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
		"Referrer-Policy":         "no-referrer",
		"Content-Security-Policy": wantCSP,
	} {
		if got := res.Header.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

// bucketAt is a bucket that signs nothing and is only ever asked where it is.
type bucketAt struct {
	emptyBucket
	origin string
}

func (b bucketAt) Origin() string { return b.origin }

// newServerWithBucket is a server whose attachment routes are mounted, which
// is the only state in which a browser ever uploads anywhere.
func newServerWithBucket(t *testing.T, cfg config.Config, bucket objectStore) http.Handler {
	t.Helper()
	files := NewAttachments(nil, bucket, 1<<20, 1<<24, nil)
	return newServer(t, cfg).WithAttachments(files).Handler()
}

// The bucket is another origin and the browser uploads to it directly, so a
// policy that did not name it would refuse every upload, in the browser, with
// nothing in this server's log to explain it. The binary knows the origin, so
// it names it rather than asking the operator to copy it anywhere.
func TestContentSecurityPolicyNamesTheBucket(t *testing.T) {
	h := newServerWithBucket(t, config.Config{
		EventName:   "X",
		Attachments: config.AttachmentsConfig{Endpoint: "https://s3.example.test"},
	}, bucketAt{origin: "https://s3.example.test"})
	res := get(t, h, "/", nil)
	_ = res.Body.Close()

	want := wantCSP + " https://s3.example.test"
	if got := res.Header.Get("Content-Security-Policy"); got != want {
		t.Errorf("Content-Security-Policy = %q, want %q", got, want)
	}
}

// The policy names the origin the upload is signed for, and not a second
// reading of the same setting. An endpoint written with the port its scheme
// implies is signed for the host without it, because that is the Host header a
// browser sends; a policy naming the other spelling is a policy about an
// origin nothing ever connects to.
func TestContentSecurityPolicyNamesWhatTheBucketSigns(t *testing.T) {
	const endpoint = "https://s3.example.test:443"
	bucket, err := objstore.New(objstore.Config{
		Endpoint: endpoint, Region: "r", Bucket: "b",
		AccessKeyID: "k", SecretAccessKey: "s",
	})
	if err != nil {
		t.Fatalf("objstore.New(): %v", err)
	}

	h := newServerWithBucket(t, config.Config{
		EventName:   "X",
		Attachments: config.AttachmentsConfig{Endpoint: endpoint},
	}, bucket)
	res := get(t, h, "/", nil)
	_ = res.Body.Close()

	want := wantCSP + " " + bucket.Origin()
	if got := res.Header.Get("Content-Security-Policy"); got != want {
		t.Errorf("Content-Security-Policy = %q, want %q", got, want)
	}
}

// A host the bucket accepts is a host the browser is sent to, whatever it
// looks like: `minio_svc` is an ordinary name for a service in a compose file,
// and an internationalised domain is an ordinary domain. A policy that left
// either out would block the upload in the browser and log nothing anywhere,
// which is the failure this header exists to end.
func TestContentSecurityPolicyNamesAnyHostTheBucketAccepts(t *testing.T) {
	for _, origin := range []string{"http://minio_svc:9000", "https://ünicode.example"} {
		t.Run(origin, func(t *testing.T) {
			h := newServerWithBucket(t, config.Config{
				EventName:   "X",
				Attachments: config.AttachmentsConfig{Endpoint: origin},
			}, bucketAt{origin: origin})
			res := get(t, h, "/", nil)
			_ = res.Body.Close()

			want := wantCSP + " " + origin
			if got := res.Header.Get("Content-Security-Policy"); got != want {
				t.Errorf("Content-Security-Policy = %q, want %q", got, want)
			}
		})
	}
}

// Sources in a directive are separated by spaces and directives by semicolons,
// so an origin carrying either would not be a source here but a directive of
// its own, and one this policy does not name above is one the browser would
// obey. No host that resolves carries one, so nothing that could work is lost.
func TestContentSecurityPolicyLeavesOutAnOriginThatWouldSplitIt(t *testing.T) {
	for _, origin := range []string{
		"https://s3.example.test;style-src-attr",
		"https://s3.example.test 'unsafe-inline'",
	} {
		t.Run(origin, func(t *testing.T) {
			h := newServerWithBucket(t, config.Config{EventName: "X"}, bucketAt{origin: origin})
			res := get(t, h, "/", nil)
			_ = res.Body.Close()

			if got := res.Header.Get("Content-Security-Policy"); got != wantCSP {
				t.Errorf("Content-Security-Policy = %q, want the policy with no bucket in it", got)
			}
		})
	}
}

// A bucket with no database behind it is a feature that is off: the routes are
// never mounted and the page draws no control, so nothing uploads and the
// policy has no reason to name an origin.
func TestContentSecurityPolicyNamesNoBucketWithoutTheRoutesToUseIt(t *testing.T) {
	h := newTestServer(t, config.Config{
		EventName:   "X",
		Attachments: config.AttachmentsConfig{Endpoint: "https://s3.example.test"},
	})
	res := get(t, h, "/", nil)
	_ = res.Body.Close()

	if got := res.Header.Get("Content-Security-Policy"); got != wantCSP {
		t.Errorf("Content-Security-Policy = %q, want the policy with no bucket in it", got)
	}
}

// SOIREE_CSP=off is for a deployment whose proxy sends a policy of its own.
func TestContentSecurityPolicyCanBeTurnedOff(t *testing.T) {
	h := newTestServer(t, config.Config{EventName: "X", DisableCSP: true})
	res := get(t, h, "/", nil)
	_ = res.Body.Close()

	if got := res.Header.Get("Content-Security-Policy"); got != "" {
		t.Errorf("Content-Security-Policy = %q, want none", got)
	}
	// The rest of the headers are not part of the bargain.
	if got := res.Header.Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q, want DENY", got)
	}
}

// Not indexed unless somebody says so. A planner holds names against amounts
// of money they owe each other, and none of those people chose to publish it.
func TestNotIndexedByDefault(t *testing.T) {
	h := newTestServer(t, config.Config{EventName: "X"})

	res := get(t, h, "/robots.txt", nil)
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("/robots.txt -> %d", res.StatusCode)
	}
	if !strings.Contains(string(body), "Disallow: /") {
		t.Errorf("robots.txt does not disallow: %q", body)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}

	// robots.txt only asks. The header is what keeps a page that was fetched
	// anyway — from a link, a referrer log, a shared screenshot — out of an
	// index, so it has to be on every response rather than just that one.
	for _, path := range []string{"/", "/robots.txt", "/healthz"} {
		r := get(t, h, path, nil)
		_ = r.Body.Close()
		if got := r.Header.Get("X-Robots-Tag"); got != "noindex, nofollow" {
			t.Errorf("%s X-Robots-Tag = %q, want noindex", path, got)
		}
	}
}

func TestIndexingCanBeAllowed(t *testing.T) {
	h := newTestServer(t, config.Config{EventName: "X", AllowIndexing: true})

	res := get(t, h, "/robots.txt", nil)
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()

	if !strings.Contains(string(body), "Allow: /") {
		t.Errorf("robots.txt does not allow: %q", body)
	}
	if strings.Contains(string(body), "Disallow") {
		t.Errorf("robots.txt still disallows: %q", body)
	}

	r := get(t, h, "/", nil)
	_ = r.Body.Close()
	if got := r.Header.Get("X-Robots-Tag"); got != "" {
		t.Errorf("X-Robots-Tag = %q, want it absent when indexing is allowed", got)
	}
}

// A phone's home screen does not take an SVG. Every icon the manifest and the
// page name has to be a real PNG at a hashed address, of the size it claims:
// Android silently refuses to offer "install" when one is missing or the wrong
// size, and says nothing about why.
func TestThePhoneIconsAreRealAndTheSizeTheyClaim(t *testing.T) {
	h := newTestServer(t, config.Config{EventName: "Ada's Leaving Do"})

	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}

	page := get("/").Body.String()
	manifestURL := regexp.MustCompile(`/assets/manifest\.[a-f0-9]+\.webmanifest`).FindString(page)
	if manifestURL == "" {
		t.Fatal("the page links no manifest")
	}
	var manifest struct {
		Icons []struct{ Src, Sizes, Type, Purpose string }
	}
	if err := json.Unmarshal(get(manifestURL).Body.Bytes(), &manifest); err != nil {
		t.Fatalf("manifest: %v", err)
	}

	want := map[string]bool{"192x192 any": false, "512x512 any": false, "512x512 maskable": false}
	check := func(url string, side int) {
		t.Helper()
		rec := get(url)
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" {
			t.Errorf("%s: %d %q", url, rec.Code, rec.Header().Get("Content-Type"))
			return
		}
		cfg, err := png.DecodeConfig(rec.Body)
		if err != nil || cfg.Width != side || cfg.Height != side {
			t.Errorf("%s is %dx%d (%v), want %dx%d", url, cfg.Width, cfg.Height, err, side, side)
		}
		if !regexp.MustCompile(`\.[a-f0-9]{8,}\.png$`).MatchString(url) {
			t.Errorf("%s is not a hashed address", url)
		}
	}
	for _, icon := range manifest.Icons {
		if icon.Type != "image/png" {
			continue
		}
		key := icon.Sizes + " " + icon.Purpose
		if _, ok := want[key]; ok {
			want[key] = true
		}
		var side int
		if _, err := fmt.Sscanf(icon.Sizes, "%dx", &side); err != nil {
			t.Fatalf("sizes %q: %v", icon.Sizes, err)
		}
		check(icon.Src, side)
	}
	for key, found := range want {
		if !found {
			t.Errorf("the manifest has no %s PNG", key)
		}
	}

	touch := regexp.MustCompile(`<link rel="apple-touch-icon" href="([^"]+)"`).FindStringSubmatch(page)
	if touch == nil {
		t.Fatal("the page names no apple-touch-icon")
	}
	check(touch[1], 180)
}

// The worker is a script and html/template is for pages: rendered through it,
// the `<` of a `for` loop became `&lt;`, the file stopped parsing, and no
// browser had a worker from 1.0.0 to 1.2.0 — no offline shell, no reminders —
// while every test passed, because none of them read what was served. So:
// every line of the source that holds no template action is served as written.
func TestTheServiceWorkerIsServedAsWritten(t *testing.T) {
	h := newTestServer(t, config.Config{EventName: "X"})
	res := get(t, h, "/sw.js", nil)
	served, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()

	source, err := fs.ReadFile(web.FS(), "sw.js")
	if err != nil {
		t.Fatalf("read sw.js: %v", err)
	}
	have := map[string]bool{}
	for _, line := range strings.Split(string(served), "\n") {
		have[line] = true
	}
	var operators int
	for n, line := range strings.Split(string(source), "\n") {
		if strings.Contains(line, "{{") {
			continue
		}
		if strings.ContainsAny(line, "<>&") {
			operators++
		}
		if !have[line] {
			t.Errorf("sw.js line %d is not served as written: %q", n+1, line)
		}
	}
	// Without a line that an HTML escaper would touch, this test would pass
	// over the very mistake it is here for.
	if operators == 0 {
		t.Error("sw.js has no line with <, > or & left; this test no longer proves anything")
	}
	for _, entity := range []string{"&lt;", "&gt;", "&amp;", "&#"} {
		if strings.Contains(string(served), entity) {
			t.Errorf("the served worker contains %q: it was escaped as HTML", entity)
		}
	}
}

// Two starts of the same build have to serve the same worker. The precache
// list is written from a map, and a map hands its keys out in a different
// order every time, so the file and its ETag changed on every restart with
// nothing deployed: a browser byte-compares the worker, so each returning
// client installed a "new" one and re-ran its precache, and the revalidation
// of /sw.js never came back 304 across a restart.
func TestTheServiceWorkerIsTheSameOnEveryStart(t *testing.T) {
	cfg := config.Config{EventName: "A Celebration"}
	first, second := newServer(t, cfg), newServer(t, cfg)

	if first.sw.ETag != second.sw.ETag {
		t.Errorf("two builds of one input gave workers %s and %s", first.sw.ETag, second.sw.ETag)
	}
	if !bytes.Equal(first.sw.Raw, second.sw.Raw) {
		t.Error("two builds of one input gave different worker bytes")
	}
	if first.index.ETag != second.index.ETag {
		t.Errorf("two builds of one input gave shells %s and %s", first.index.ETag, second.index.ETag)
	}

	// With ten assets in the map, two builds could agree by luck once in
	// 10! tries. What rules that out is the order being a defined one.
	urls := regexp.MustCompile(`/assets/[A-Za-z0-9._-]+`).FindAllString(string(first.sw.Raw), -1)
	if len(urls) == 0 {
		t.Fatal("the worker precaches nothing")
	}
	if !sort.StringsAreSorted(urls) {
		t.Errorf("the precache list is in no defined order: %v", urls)
	}
}

// A deployment can serve a shell that names nobody.
//
// The page is served to anybody who has the URL (noindex keeps it out of a
// search engine, not away from whoever has the address), so on a deployment
// that treats the event as personal, every place the shell says whose evening
// this is has to say nothing instead: the tab's title, the masthead, the line
// above the sign-in form, the manifest a phone puts under the icon, and the
// configuration block. A session is what tells the page, and nothing there is
// reachable without one.
func TestTheShellCanBeServedWithoutNamingTheEvent(t *testing.T) {
	h := newTestServer(t, config.Config{
		EventName:        "Ada's Retirement",
		Tagline:          "Dinner and speeches",
		EventDate:        "2030-01-13T00:00:00+09:00",
		Ceiling:          25000,
		DatabaseURL:      "postgres://soiree@db.example.test/soiree",
		HideEventDetails: true,
	})

	res := get(t, h, "/", nil)
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	page := string(body)

	for _, said := range []string{"Retirement", "Dinner and speeches", "2030-01-13", "25000"} {
		if strings.Contains(page, said) {
			t.Errorf("the shell still tells every visitor %q", said)
		}
	}
	if !strings.Contains(page, "<title>soiree</title>") {
		t.Error("the shell has no title of its own to fall back on")
	}
	// What decides whether a control can be drawn at all stays: it says what
	// this deployment can do, not whose evening it is.
	if !strings.Contains(page, `"passkeys"`) || !strings.Contains(page, `"currency":"EUR"`) {
		t.Error("the configuration block lost more than the event")
	}

	manifestURL := regexp.MustCompile(`/assets/manifest\.[a-f0-9]+\.webmanifest`).FindString(page)
	if manifestURL == "" {
		t.Fatal("the page links no manifest")
	}
	res = get(t, h, manifestURL, nil)
	body, _ = io.ReadAll(res.Body)
	_ = res.Body.Close()
	var manifest struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		t.Fatalf("the manifest is not JSON: %v", err)
	}
	// One rendering for everybody, so an installed app is named after the
	// product rather than after the evening. That is a cost of this switch,
	// and it is why it is a switch.
	if manifest.Name != "soiree" {
		t.Errorf("the manifest names the event %q", manifest.Name)
	}
}

// The manifest is JSON, and what it names is what stands under the icon on a
// phone. An event called "Ada's 90th" has to arrive as that, not as
// "Ada&#39;s 90th", and a name with a quote or a backslash in it must not
// cost the site its manifest.
func TestTheManifestNamesTheEventAsWritten(t *testing.T) {
	for _, name := range []string{
		`Ada's 90th`,
		`Ada & Grace <3`,
		`The "big" one`,
		`back\slash`,
	} {
		h := newTestServer(t, config.Config{EventName: name})
		res := get(t, h, "/", nil)
		page, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		manifestURL := regexp.MustCompile(`/assets/manifest\.[a-f0-9]+\.webmanifest`).FindString(string(page))
		if manifestURL == "" {
			t.Fatal("the page links no manifest")
		}
		res = get(t, h, manifestURL, nil)
		body, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()

		var manifest struct {
			Name  string `json:"name"`
			Icons []struct {
				Src string `json:"src"`
			} `json:"icons"`
		}
		if err := json.Unmarshal(body, &manifest); err != nil {
			t.Errorf("event %q: the manifest is not JSON: %v", name, err)
			continue
		}
		if manifest.Name != name {
			t.Errorf("the manifest names the event %q, want %q", manifest.Name, name)
		}
		if len(manifest.Icons) == 0 {
			t.Errorf("event %q: the manifest lost its icons", name)
		}
		for _, icon := range manifest.Icons {
			if !strings.HasPrefix(icon.Src, "/assets/") {
				t.Errorf("event %q: icon address %q", name, icon.Src)
			}
		}
	}
}
