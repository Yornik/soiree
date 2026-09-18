// Package httpd serves the soiree frontend.
//
// The whole site is held in memory: assets are hashed, pre-compressed and
// served with immutable cache headers, and the HTML shell is rendered once at
// startup. There is no disk I/O on the request path.
package httpd

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/Yornik/soiree/internal/config"
	"github.com/Yornik/soiree/internal/store"
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

	// store is nil when no DSN is configured. That is a supported deployment
	// rather than a broken one — the binary then serves the frontend alone —
	// so every use of it is guarded rather than assumed.
	store *store.Store

	// Served at fixed paths, so they carry an ETag and are revalidated
	// rather than cached hard — but still pre-compressed.
	index *Asset
	sw    *Asset

	// auth is the accounts surface, nil when the deployment has no database.
	auth *Auth
}

// WithAuth attaches the accounts, sessions and roles surface.
//
// Separate from New because it is optional: a process with no DATABASE_URL
// serves the static shell and nothing else, which is what `docker run` with no
// arguments does and what the image smoke test checks.
func (s *Server) WithAuth(a *Auth) *Server {
	s.auth = a
	return s
}

// Option adjusts a Server as it is built.
type Option func(*Server)

// WithStore attaches the data layer, which is what turns the API on. Without
// it there are no /api/v1 routes at all.
func WithStore(st *store.Store) Option {
	return func(s *Server) { s.store = st }
}

// New builds a Server from the embedded source tree and the configuration.
func New(cfg config.Config, srcFS fs.FS, opts ...Option) (*Server, error) {
	assets, err := BuildAssets(srcFS)
	if err != nil {
		return nil, err
	}

	s := &Server{cfg: cfg, assets: assets, metrics: NewMetrics(Version, Commit)}
	for _, opt := range opts {
		opt(s)
	}

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

	// Readiness: this instance can serve traffic, which with a database
	// configured means the database is reachable.
	mux.HandleFunc("/readyz", s.serveReadyz)

	// /metrics is deliberately absent: it lives on MetricsHandler, behind its
	// own listener. The ingress route in front of this handler has no path
	// constraint, so anything registered here is world-readable, and
	// soiree_build_info would hand out the running version and commit. A
	// request for it lands on the 404 below — and is still counted under
	// route="metrics", which is what makes a stale ServiceMonitor or a scanner
	// probing the public path visible rather than silent.

	// The API exists only when there is something behind it. With no DSN these
	// paths are never registered, so /api/v1/... falls through to the 404 below
	// and the frontend is served exactly as it was before this milestone.
	if s.store != nil {
		s.routeAPI(mux)
	}

	// /api/v1/auth/... and /api/v1/users/...; the rest of /api/v1 belongs to
	// the REST API and registers on this same mux.
	if s.auth != nil {
		s.auth.Register(mux)
	}

	mux.HandleFunc("/", s.serveIndex)

	return securityHeaders(s.metrics.instrument(mux))
}

// MetricsHandler returns the handler for the private metrics listener.
//
// Separate from Handler so the exposition is reachable only on the port
// cfg.MetricsAddr binds, which the public ingress does not route to. The path
// stays inside this package rather than becoming main's business: a
// ServiceMonitor scrapes /metrics, and it should not be possible to move it by
// editing the wrong file.
//
// Not instrumented and not wrapped in securityHeaders. Counting scrapes would
// have the exposition report on the act of reading it, and a scraper is not a
// browser — there is no framing or sniffing to defend against on a port nothing
// else can reach.
func (s *Server) MetricsHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/metrics", s.metrics.Handler())
	return mux
}

// serveReadyz reports whether this instance can serve traffic.
//
// Unlike /healthz it does check the database, because an instance that cannot
// reach it cannot answer an API request — and the point of readiness is to take
// such an instance out of rotation without restarting it.
func (s *Server) serveReadyz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")

	if s.store != nil {
		ctx, cancel := context.WithTimeout(r.Context(), readyzTimeout)
		defer cancel()
		if err := s.store.Ping(ctx); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("database unreachable\n"))
			return
		}
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ready\n"))
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
