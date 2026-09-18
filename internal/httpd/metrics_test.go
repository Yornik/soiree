package httpd

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

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
