package reminders

import "github.com/prometheus/client_golang/prometheus"

// outcome is how one run of the digest ended.
//
// A closed set, because it is a metric label: an unbounded one on a series
// that is scraped forever is how a Prometheus runs out of memory. The values
// are the log lines' own vocabulary, so a graph and the log read the same way.
type outcome string

const (
	outcomeSent         outcome = "sent"
	outcomeNothingDue   outcome = "nothing_due"
	outcomeAlreadySent  outcome = "already_sent"
	outcomeCooloff      outcome = "cooloff"
	outcomeNotLeader    outcome = "not_leader"
	outcomeNoRecipients outcome = "no_recipients"
	outcomeUnrecorded   outcome = "unrecorded"
	outcomeUncertain    outcome = "uncertain"
	outcomeLost         outcome = "lost"
	outcomeFailed       outcome = "failed"
)

// outcomes is every value the label can take, so each series exists at zero
// from startup. The scheduler's first act is a run, and a counter first seen
// at 1 has no increase() for an alert to read: without this, a failure on the
// very first run is the one nobody hears about.
var outcomes = []outcome{
	outcomeSent, outcomeNothingDue, outcomeAlreadySent, outcomeCooloff,
	outcomeNotLeader, outcomeNoRecipients, outcomeUnrecorded, outcomeUncertain,
	outcomeLost, outcomeFailed,
}

// ok reports whether a run ended in something nobody has to act on.
//
// Not sending is usually right: the period already went out, one went out too
// recently, another replica is sending it, or nothing was due. The four that
// are not ok all mean the same thing in the end, which is that a deadline
// somebody has to act on did not reach them and no run will try again by
// itself. `failed` covers the copy the relay refused while the others went
// out, for that reason: the person it was addressed to is owed a digest that
// nothing will now send.
//
// no_recipients is the odd one out, and counts as ok deliberately. That digest
// reaches nobody either, but the cause is a deployment with no active admin
// and no configured address, which no later run resolves and no restart
// clears: it would hold this at 0 until somebody edits the configuration, and
// an alert nothing can clear is an alert that gets muted. The counter still
// names it, which is where an alert for that belongs.
func (o outcome) ok() bool {
	switch o {
	case outcomeUnrecorded, outcomeUncertain, outcomeLost, outcomeFailed:
		return false
	default:
		return true
	}
}

// metrics is the two series the scheduler exports.
type metrics struct {
	runs      *prometheus.CounterVec
	lastRunOK prometheus.Gauge
}

// WithMetrics counts runs on the registry behind the metrics listener.
//
// Registered from here rather than declared with the HTTP series, following
// the live sync hub: a deployment that never starts the scheduler then
// declares neither series, which is what keeps "nothing is scheduled here"
// and "the last digest did not go out" apart on a dashboard that can only see
// the exposition.
func (s *Service) WithMetrics(reg prometheus.Registerer) *Service {
	s.metrics = newMetrics(reg)
	return s
}

func newMetrics(reg prometheus.Registerer) *metrics {
	m := &metrics{
		runs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "soiree_reminder_runs_total",
			Help: "Deadline digest runs, by how each one ended.",
		}, []string{"outcome"}),
		lastRunOK: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "soiree_reminder_last_run_ok",
			Help: "1 when the last digest run ended in an outcome nobody has to act on, 0 otherwise.",
		}),
	}
	for _, o := range outcomes {
		m.runs.WithLabelValues(string(o))
	}
	reg.MustRegister(m.runs, m.lastRunOK)
	return m
}

// record files one run. A nil receiver is a deployment nothing scrapes, which
// is every way of running this outside a cluster; the scheduler is the same
// scheduler either way, so the check lives here rather than at the call site.
func (m *metrics) record(o outcome) {
	if m == nil {
		return
	}
	m.runs.WithLabelValues(string(o)).Inc()
	if o.ok() {
		m.lastRunOK.Set(1)
		return
	}
	m.lastRunOK.Set(0)
}
