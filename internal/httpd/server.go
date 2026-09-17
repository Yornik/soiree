// Package httpd serves the soiree frontend.
//
// The whole site is held in memory: assets are hashed, pre-compressed and
// served with immutable cache headers, and the HTML shell is rendered once at
// startup. There is no disk I/O on the request path.
package httpd

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/Yornik/soiree/internal/config"
)

// Build information, overridden at link time with -ldflags.
var (
	Version = "dev"
	Commit  = "none"
)

// Server is the HTTP handler set.
type Server struct {
	cfg     config.Config
	assets  *Assets
	metrics *Metrics

	// Served at fixed paths, so they carry an ETag and are revalidated
	// rather than cached hard — but still pre-compressed.
	index *Asset
	sw    *Asset
}

// New builds a Server from the embedded source tree and the configuration.
func New(cfg config.Config, srcFS fs.FS) (*Server, error) {
	assets, err := BuildAssets(srcFS)
	if err != nil {
		return nil, err
	}

	s := &Server{cfg: cfg, assets: assets, metrics: NewMetrics(Version, Commit)}

	if err := s.renderManifest(srcFS); err != nil {
		return nil, err
	}
	if err := s.renderIndex(srcFS); err != nil {
		return nil, err
	}
	if err := s.renderServiceWorker(srcFS); err != nil {
		return nil, err
	}
	return s, nil
}

// indexData is the template context for the HTML shell.
type indexData struct {
	EventName  string
	Tagline    string
	ConfigJSON template.JS
	assets     *Assets
}

// Asset resolves a logical asset name to its hashed URL from the template.
func (d indexData) Asset(name string) string { return d.assets.URL(name) }

func (s *Server) renderManifest(srcFS fs.FS) error {
	raw, err := fs.ReadFile(srcFS, "manifest.webmanifest")
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	tmpl, err := template.New("manifest").Parse(string(raw))
	if err != nil {
		return fmt.Errorf("parse manifest: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, indexData{
		EventName: s.cfg.EventName,
		assets:    s.assets,
	}); err != nil {
		return fmt.Errorf("render manifest: %w", err)
	}
	s.assets.buildAsset("manifest.webmanifest", buf.Bytes())
	return nil
}

func (s *Server) renderIndex(srcFS fs.FS) error {
	raw, err := fs.ReadFile(srcFS, "index.html")
	if err != nil {
		return fmt.Errorf("read index.html: %w", err)
	}
	tmpl, err := template.New("index").Parse(string(raw))
	if err != nil {
		return fmt.Errorf("parse index.html: %w", err)
	}
	cfgJSON, err := s.cfg.ClientJSON()
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, indexData{
		EventName: s.cfg.EventName,
		Tagline:   s.cfg.Tagline,
		// Safe as template.JS: this is our own marshalled struct, and it
		// lands inside a non-executable application/json data block.
		ConfigJSON: template.JS(cfgJSON),
		assets:     s.assets,
	}); err != nil {
		return fmt.Errorf("render index.html: %w", err)
	}
	s.index = NewDocument("index.html", buf.Bytes())
	return nil
}

func (s *Server) renderServiceWorker(srcFS fs.FS) error {
	raw, err := fs.ReadFile(srcFS, "sw.js")
	if err != nil {
		return fmt.Errorf("read sw.js: %w", err)
	}
	tmpl, err := template.New("sw").Parse(string(raw))
	if err != nil {
		return fmt.Errorf("parse sw.js: %w", err)
	}
	// The cache name is derived from the shell's ETag, so a new build
	// invalidates the old cache automatically.
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, struct {
		Version string
		Assets  []string
	}{
		Version: strings.Trim(s.index.ETag, `"`),
		Assets:  s.assets.Names(),
	}); err != nil {
		return fmt.Errorf("render sw.js: %w", err)
	}
	s.sw = NewDocument("sw.js", buf.Bytes())
	return nil
}

// Handler returns the routed handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/assets/", s.serveAsset)
	mux.HandleFunc("/sw.js", s.serveServiceWorker)

	// Liveness: the process is up. Deliberately checks nothing else — a
	// liveness probe that depends on a downstream turns that downstream's
	// outage into a restart loop here.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	// Readiness: this instance can serve traffic. Identical to liveness while
	// everything is in memory; once the database lands this is where the
	// connection check belongs, so it is split now rather than later.
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready\n"))
	})

	mux.Handle("/metrics", s.metrics.Handler())
	mux.HandleFunc("/", s.serveIndex)

	return securityHeaders(s.metrics.instrument(mux))
}

// securityHeaders sets the headers that do not depend on the reverse proxy.
// HSTS and CSP are applied at the ingress in the deployed setup, but a bare
// `docker run` should not be wide open either.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	// The shell is never cached hard: it carries the hashed asset URLs, so it
	// is the one file that must be revalidated for a deploy to take effect.
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", s.index.ETag)
	serveBody(w, r, s.index.ContentType, s.index.ETag, s.index.Raw, s.index.Gzip, s.index.Brotli)
}

func (s *Server) serveServiceWorker(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", s.sw.ETag)
	serveBody(w, r, s.sw.ContentType, s.sw.ETag, s.sw.Raw, s.sw.Gzip, s.sw.Brotli)
}

func (s *Server) serveAsset(w http.ResponseWriter, r *http.Request) {
	as, ok := s.assets.Lookup(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	// Content-addressed: the URL changes whenever the bytes do, so this can
	// be cached for a year. This is what makes repeat visits cost nothing,
	// which matters most for clients far from the origin.
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("ETag", as.ETag)
	serveBody(w, r, as.ContentType, as.ETag, as.Raw, as.Gzip, as.Brotli)
}

// serveBody writes the best-encoded variant the client accepts, handling
// conditional requests and HEAD.
func serveBody(w http.ResponseWriter, r *http.Request, ct, etag string, raw, gz, br []byte) {
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Add("Vary", "Accept-Encoding")

	if match := r.Header.Get("If-None-Match"); match != "" && etagMatches(match, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	body := raw
	accept := r.Header.Get("Accept-Encoding")
	switch {
	case br != nil && strings.Contains(accept, "br"):
		h.Set("Content-Encoding", "br")
		body = br
	case gz != nil && strings.Contains(accept, "gzip"):
		h.Set("Content-Encoding", "gzip")
		body = gz
	}

	h.Set("Content-Length", fmt.Sprint(len(body)))
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// etagMatches implements the If-None-Match comparison, including the list
// form and the weak prefix.
func etagMatches(header, etag string) bool {
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "*" || part == etag || strings.TrimPrefix(part, "W/") == etag {
			return true
		}
	}
	return false
}

// ReadTimeout and friends, kept here so main stays small.
const (
	ReadHeaderTimeout = 10 * time.Second
	ReadTimeout       = 30 * time.Second
	WriteTimeout      = 60 * time.Second
	IdleTimeout       = 120 * time.Second
)
