package httpd

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/Yornik/soiree/internal/config"
	"github.com/Yornik/soiree/internal/migrate"
	"github.com/Yornik/soiree/internal/pgtest"
	"github.com/Yornik/soiree/internal/store"
	"github.com/Yornik/soiree/web"
)

// newAPIServerWithMetrics is the API server plus the handler behind the private
// metrics listener.
//
// Its own constructor rather than api_test.go's newAPIServer, which hands back
// only the public handler: since the exposition moved off the public mux, a
// metrics assertion needs the *Server itself. The rest of this package's
// fixtures — call, created, str, TestMain — resolve across files.
func newAPIServerWithMetrics(t *testing.T) (public, metrics http.Handler, pool *pgxpool.Pool) {
	t.Helper()

	pool = pgtest.Pool(t)
	if _, err := migrate.Run(t.Context(), pool, nil); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(pool)
	s, err := New(
		config.Config{EventName: "Ada's Retirement", Currency: "EUR", Locale: "en-US"},
		web.FS(),
		WithStore(st),
	)
	if err != nil {
		t.Fatalf("New(): %v", err)
	}

	// The API is behind RequireWrite, so a request without a session never
	// reaches a handler and never gets classified — which would make the label
	// assertions below pass for the wrong reason.
	a := NewAuth(AuthOptions{Store: st, BaseURL: "https://soiree.example.test/"})
	a.params = cheapParams
	a.background = func(fn func(context.Context)) { fn(context.Background()) }
	h := s.WithAuth(a).Handler()

	return authedAs(t, h, st, a, store.RoleEditor), s.MetricsHandler(), pool
}

// TestAPIMetricsRouteLabelIsBounded: every API path after the collection is a
// row id, so labelling by path would mint a time series per budget line.
func TestAPIMetricsRouteLabelIsBounded(t *testing.T) {
	h, mh, _ := newAPIServerWithMetrics(t)

	item := created(t, h, "budget-items", `{"item":"Venue deposit","unit":"250.00"}`)
	id := str(t, item, "id")
	call(t, h, http.MethodPatch, "/api/v1/budget-items/"+id, `{"revision":1,"unit":"260.00"}`)
	call(t, h, http.MethodDelete, "/api/v1/budget-items/"+id+"?revision=2", "")
	call(t, h, http.MethodGet, "/api/v1/plan", "")
	// An unrecognised collection is caller-controlled, so it must not become a
	// label of its own either.
	call(t, h, http.MethodGet, "/api/v1/"+uuid.New().String(), "")

	m := call(t, mh, http.MethodGet, "/metrics", "")
	out := string(m.body)

	if strings.Contains(out, id) {
		t.Error("a row id leaked into a metric label")
	}
	if strings.Contains(out, `route="/api/`) {
		t.Error("raw API paths leaked into the route label")
	}
	for _, want := range []string{`route="api-budget-items"`, `route="api-plan"`, `route="api-other"`} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics output missing %s", want)
		}
	}
}

// A handler panic used to be the one server fault that reached neither of the
// channels this deployment is operated through: net/http's own "panic serving"
// line arrives through the std log bridge, which slog emits at INFO, and the
// request never reached the counter, so the 5xx rate stayed flat while the
// browser retried a write every thirty seconds. The JSON body is a nicety —
// the page treats a reset, a 502 and a 500 alike — the log line and the sample
// are the point.
func TestAHandlerPanicIsLoggedAndCountedAsA500(t *testing.T) {
	var logged bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	m := NewMetrics("test", "none")
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/plan", func(http.ResponseWriter, *http.Request) {
		panic("a handler that could not cope")
	})

	rec := httptest.NewRecorder()
	m.instrument(mux).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/plan", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"error":"internal"`) {
		t.Errorf("body = %q, want the API's own internal error", body)
	}
	if line := logged.String(); !strings.Contains(line, `"level":"ERROR"`) ||
		!strings.Contains(line, `"route":"api-plan"`) {
		t.Errorf("the panic did not reach the log as an error naming the route:\n%s", line)
	}

	scrape := httptest.NewRecorder()
	m.Handler().ServeHTTP(scrape, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if want := `soiree_http_requests_total{method="GET",route="api-plan",status="500"} 1`; !strings.Contains(scrape.Body.String(), want) {
		t.Errorf("no %s in the exposition, so a 5xx panel would show nothing", want)
	}
}

// Once the first byte is out there is no 500 to send, and a truncated body that
// ends in a clean close reads as a complete one. ErrAbortHandler is how
// net/http is asked to drop the connection instead — and a handler that panics
// with it has asked for exactly that itself, so it passes through untouched.
func TestAPanicAfterTheResponseStartedAbortsTheConnection(t *testing.T) {
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.DiscardHandler))
	t.Cleanup(func() { slog.SetDefault(prev) })

	for name, h := range map[string]http.HandlerFunc{
		"a half-written body": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"items":[`))
			panic("a handler that could not cope")
		},
		"a handler that aborted on purpose": func(http.ResponseWriter, *http.Request) {
			panic(http.ErrAbortHandler)
		},
	} {
		t.Run(name, func(t *testing.T) {
			m := NewMetrics("test", "none")
			defer func() {
				if v := recover(); v != http.ErrAbortHandler {
					t.Errorf("recovered %v, want http.ErrAbortHandler so the client does not read a truncated body as whole", v)
				}
			}()
			m.instrument(h).ServeHTTP(httptest.NewRecorder(),
				httptest.NewRequest(http.MethodGet, "/api/v1/plan", nil))
		})
	}
}

// TestRouteClassIsAFixedSet checks the classifier directly, including the paths
// a caller can invent.
//
// /metrics is still in the set even though the public mux no longer serves it:
// a probe of the public path is a 404 that should be counted as such, not
// buried in "other".
func TestRouteClassIsAFixedSet(t *testing.T) {
	known := map[string]struct{}{
		"shell": {}, "asset": {}, "service-worker": {}, "healthz": {},
		"readyz": {}, "metrics": {}, "other": {}, "api-other": {},
	}
	for _, label := range apiRoutes {
		known[label] = struct{}{}
	}

	for _, path := range []string{
		"/", "/sw.js", "/healthz", "/readyz", "/metrics", "/assets/app.deadbeef.js",
		"/api/v1/plan", "/api/v1/budget-items", "/api/v1/budget-items/" + uuid.New().String(),
		"/api/v1/programme-entries/" + uuid.New().String(), "/api/v1/" + uuid.New().String(),
		"/api/v1/", "/api/v1", "/api/v2/plan", "/nope/" + uuid.New().String(),
	} {
		if _, ok := known[routeClass(path)]; !ok {
			t.Errorf("routeClass(%q) = %q, which is not in the fixed set", path, routeClass(path))
		}
	}
}

// The registry is private, so a feature that lives outside this package can
// only declare a series of its own through MetricsRegistry, and a series that
// does not reach the exposition is one nobody can alert on. The deadline
// digest's run counters are registered exactly this way, from
// internal/reminders.
func TestASeriesRegisteredFromOutsideThePackageIsServed(t *testing.T) {
	s, err := New(config.Config{Currency: "EUR", Locale: "en-US"}, web.FS())
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	s.MetricsRegistry().MustRegister(prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "soiree_a_feature_of_its_own",
		Help: "Registered the way a collector outside this package registers one.",
	}))

	scrape := httptest.NewRecorder()
	s.MetricsHandler().ServeHTTP(scrape, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(scrape.Body.String(), "soiree_a_feature_of_its_own") {
		t.Error("a series registered through MetricsRegistry is not in the exposition the listener serves")
	}
}
