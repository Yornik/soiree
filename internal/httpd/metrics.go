package httpd

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics holds the application's Prometheus collectors.
//
// A private registry rather than the default one: it keeps the exposed series
// to what this application actually declares, and makes the metrics
// independently testable.
type Metrics struct {
	registry *prometheus.Registry

	requests  *prometheus.CounterVec
	duration  *prometheus.HistogramVec
	inFlight  prometheus.Gauge
	buildInfo *prometheus.GaugeVec
}

// NewMetrics builds the collector set and registers the Go runtime and process
// collectors alongside it.
func NewMetrics(version, commit string) *Metrics {
	reg := prometheus.NewRegistry()

	m := &Metrics{
		registry: reg,
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "soiree_http_requests_total",
			Help: "Total HTTP requests, by route class, method and status.",
		}, []string{"route", "method", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "soiree_http_request_duration_seconds",
			Help: "HTTP request duration, by route class.",
			// Everything is served from memory, so the interesting range is
			// sub-millisecond. The default buckets start far too coarse to
			// show anything useful here.
			Buckets: []float64{0.0001, 0.00025, 0.0005, 0.001, 0.0025, 0.005, 0.01, 0.05, 0.1, 0.5, 1},
		}, []string{"route", "method"}),
		inFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "soiree_http_requests_in_flight",
			Help: "HTTP requests currently being served.",
		}),
		buildInfo: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "soiree_build_info",
			Help: "Build information. Always 1; the value carries no meaning, the labels do.",
		}, []string{"version", "commit"}),
	}

	reg.MustRegister(
		m.requests, m.duration, m.inFlight, m.buildInfo,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	m.buildInfo.WithLabelValues(version, commit).Set(1)

	return m
}

// Handler serves the metrics endpoint.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{
		Registry: m.registry,
	})
}

// statusRecorder captures the status code for metrics and logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wrote {
		r.status = code
		r.wrote = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wrote {
		r.status = http.StatusOK
		r.wrote = true
	}
	return r.ResponseWriter.Write(b)
}

// instrument records metrics for each request, and is where a handler panic
// stops being invisible.
//
// The route label is deliberately a small fixed set rather than the raw path.
// Asset URLs contain a content hash, so labelling by path would mint a new
// time series on every deploy — a textbook cardinality leak.
//
// This is also the outermost wrapper that sees a request, which makes it the
// place to recover. A panic left alone reaches neither of the two channels this
// deployment is watched through: net/http catches it, and its own "panic
// serving" line goes through the std log bridge, which slog emits at INFO,
// while the request itself is never counted at all. The one fault that is
// entirely the server's own is then the one the 5xx panel and a level=ERROR
// query both miss.
func (m *Metrics) instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route := routeClass(r.URL.Path)

		m.inFlight.Inc()
		defer m.inFlight.Dec()

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()

		defer func() {
			v := recover()
			// A response that has already begun cannot be corrected, and a
			// handler that panicked with ErrAbortHandler asked for its
			// connection to be dropped. Both end the same way, below.
			abort := v != nil && (rec.wrote || v == http.ErrAbortHandler)

			if v != nil && v != http.ErrAbortHandler {
				// The process logger is the default, which is what puts this
				// in the same JSON stream as the API's own 500 path rather
				// than in plain text on stderr.
				slog.Error("handler panic", "route", route, "method", r.Method,
					"panic", v, "stack", string(debug.Stack()))
				if !rec.wrote {
					writeError(rec, http.StatusInternalServerError, errInternal, "something went wrong")
				}
				// Recorded as the failure it was even when a partial body has
				// already gone out under a 200: nothing about this request
				// succeeded, and the sample is what an alert reads.
				rec.status = http.StatusInternalServerError
			}

			m.requests.WithLabelValues(route, r.Method, strconv.Itoa(rec.status)).Inc()
			m.duration.WithLabelValues(route, r.Method).Observe(time.Since(start).Seconds())

			if abort {
				// ErrAbortHandler is how net/http is told to drop a connection
				// without logging a stack trace of its own on top of the one
				// above. A truncated body that ends in a clean close reads as
				// a complete one, which is the thing to prevent here.
				panic(http.ErrAbortHandler)
			}
		}()

		next.ServeHTTP(rec, r)
	})
}

func routeClass(path string) string {
	switch {
	case path == "/":
		return "shell"
	case path == "/sw.js":
		return "service-worker"
	case path == "/healthz":
		return "healthz"
	case path == "/readyz":
		return "readyz"
	case path == "/metrics":
		return "metrics"
	case len(path) >= 8 && path[:8] == "/assets/":
		return "asset"
	case strings.HasPrefix(path, apiPrefix):
		return apiRouteClass(path)
	default:
		return "other"
	}
}

// apiRoutes maps an API collection to its metrics label. A map lookup rather
// than the URL segment itself, because every API path after the collection is
// a row id: labelling by path would mint a time series per budget line, and the
// segment is caller-controlled, so an unknown one must never become a label.
var apiRoutes = map[string]string{
	"plan":              "api-plan",
	"settings":          "api-settings",
	"events":            "api-events",
	"budget-items":      "api-budget-items",
	"sponsors":          "api-sponsors",
	"tasks":             "api-tasks",
	"notes":             "api-notes",
	"phases":            "api-phases",
	"programme-entries": "api-programme-entries",
}

func apiRouteClass(path string) string {
	collection, _, _ := strings.Cut(strings.TrimPrefix(path, apiPrefix), "/")
	if label, ok := apiRoutes[collection]; ok {
		return label
	}
	return "api-other"
}
