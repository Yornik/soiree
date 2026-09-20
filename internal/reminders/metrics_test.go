package reminders

import (
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/Yornik/soiree/internal/mailer"
)

// watched builds a service with a registry of its own, the way Start wires one
// to the registry behind the metrics listener.
func (f *fixture) watched(t *testing.T, sender mailer.Sender) (*Service, *prometheus.Registry) {
	t.Helper()

	reg := prometheus.NewRegistry()
	return f.service(t, sender).WithMetrics(reg), reg
}

// runs reads soiree_reminder_runs_total out of a scrape, keyed by outcome.
//
// Gathered rather than read off the collector, because a value that is
// recorded and never exported is not something anybody can alert on.
func runs(t *testing.T, reg *prometheus.Registry) map[string]float64 {
	t.Helper()

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}

	out := map[string]float64{}
	for _, mf := range families {
		if mf.GetName() != "soiree_reminder_runs_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "outcome" {
					out[l.GetValue()] = m.GetCounter().GetValue()
				}
			}
		}
	}
	return out
}

func lastRunOK(t *testing.T, reg *prometheus.Registry) float64 {
	t.Helper()

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range families {
		if mf.GetName() != "soiree_reminder_last_run_ok" {
			continue
		}
		for _, m := range mf.GetMetric() {
			return m.GetGauge().GetValue()
		}
	}
	t.Fatal("soiree_reminder_last_run_ok is not in the exposition")
	return 0
}

// Every outcome is exported at zero before anything has run, because the
// scheduler's first act is a run: a counter first seen at 1 has no increase()
// for an alert to read, which would make a failure on the very first run the
// one nobody hears about.
func TestEveryOutcomeIsExportedBeforeAnythingHasRun(t *testing.T) {
	reg := prometheus.NewRegistry()
	newMetrics(reg)

	got := runs(t, reg)
	if len(got) != len(outcomes) {
		t.Errorf("%d outcome series exported, want %d", len(got), len(outcomes))
	}
	for _, o := range outcomes {
		v, ok := got[string(o)]
		switch {
		case !ok:
			t.Errorf("outcome %q is not exported until it has happened once", o)
		case v != 0:
			t.Errorf("outcome %q starts at %v, want 0", o, v)
		}
	}
}

// A digest that goes out and the restart that must not send it again are
// different outcomes, and neither is anything to be woken for.
func TestASentDigestAndTheRestartAfterItAreBothCleanRuns(t *testing.T) {
	f := newFixture(t)
	f.seedDeadline(t, "Venue deposit", "Grand Hall", "2030-01-19", 250000, 0, 1)

	svc, reg := f.watched(t, &fakeSender{})
	svc.runLogged(t.Context())

	if got := runs(t, reg)["sent"]; got != 1 {
		t.Errorf(`runs{outcome="sent"} = %v after a digest went out, want 1`, got)
	}
	if got := lastRunOK(t, reg); got != 1 {
		t.Errorf("last_run_ok = %v after a digest went out, want 1", got)
	}

	f.now = f.now.Add(3 * time.Hour)
	svc.runLogged(t.Context())

	got := runs(t, reg)
	if got["sent"] != 1 || got["already_sent"] != 1 {
		t.Errorf("after a restart inside the period: %v, want one sent and one already_sent", got)
	}
	if v := lastRunOK(t, reg); v != 1 {
		t.Errorf("last_run_ok = %v after a period that had already gone out, want 1", v)
	}
}

// A period with nothing due is the ordinary case, not a failure. An alert that
// fires on every quiet week is one nobody reads on the week it matters.
func TestAPeriodWithNothingDueIsACleanRun(t *testing.T) {
	f := newFixture(t)

	svc, reg := f.watched(t, &fakeSender{})
	svc.runLogged(t.Context())

	if got := runs(t, reg)["nothing_due"]; got != 1 {
		t.Errorf(`runs{outcome="nothing_due"} = %v, want 1`, got)
	}
	if got := lastRunOK(t, reg); got != 1 {
		t.Errorf("last_run_ok = %v with nothing due, want 1", got)
	}
}

// Nobody to send to is counted and is still a clean run: no later run resolves
// it and no restart clears it, so in the gauge it would be a 0 that stays until
// somebody edits the configuration.
func TestADigestWithNobodyToSendItToIsCountedButStillClean(t *testing.T) {
	f := newFixture(t)
	f.cfg.To = nil
	f.seedDeadline(t, "Venue deposit", "Grand Hall", "2030-01-19", 250000, 0, 1)

	svc, reg := f.watched(t, &fakeSender{})
	svc.runLogged(t.Context())

	if got := runs(t, reg)["no_recipients"]; got != 1 {
		t.Errorf(`runs{outcome="no_recipients"} = %v with nobody to send to, want 1`, got)
	}
	if got := lastRunOK(t, reg); got != 1 {
		t.Errorf("last_run_ok = %v with nobody to send to, want 1: that is the counter's business, not the gauge's", got)
	}
}

// The failure this series exists for: a relay that refuses the digest loses
// the period unless somebody acts, and until now the only trace was a log line
// nobody is paged for.
func TestARefusedDigestIsCountedAndTheGaugeRecoversWithIt(t *testing.T) {
	f := newFixture(t)
	f.seedDeadline(t, "Venue deposit", "Grand Hall", "2030-01-19", 250000, 0, 1)

	broken := &fakeSender{err: &mailer.SendError{Err: errors.New("connection refused")}}
	svc, reg := f.watched(t, broken)
	svc.runLogged(t.Context())

	if got := runs(t, reg)["failed"]; got != 1 {
		t.Errorf(`runs{outcome="failed"} = %v after a refused send, want 1`, got)
	}
	if got := lastRunOK(t, reg); got != 0 {
		t.Errorf("last_run_ok = %v after a refused send, want 0", got)
	}

	// The claim came off, so the next run sends the same period. The gauge
	// has to come back with it, or it stays red until the pod restarts.
	broken.err = nil
	svc.runLogged(t.Context())

	if got := runs(t, reg)["sent"]; got != 1 {
		t.Errorf(`runs{outcome="sent"} = %v after the retry, want 1`, got)
	}
	if got := lastRunOK(t, reg); got != 1 {
		t.Errorf("last_run_ok = %v after the retry went out, want 1", got)
	}
}

// A send the relay never acknowledged is the one the documentation says must
// never be ignored, and the run after it is no better: the period stays
// claimed and is deliberately never retried, so both are not-ok.
func TestAnUnacknowledgedSendStaysNotOKOnTheRunAfterIt(t *testing.T) {
	f := newFixture(t)
	f.seedDeadline(t, "Venue deposit", "Grand Hall", "2030-01-19", 250000, 0, 1)

	uncertain := &fakeSender{err: &mailer.SendError{Err: errors.New("connection reset"), Ambiguous: true}}
	svc, reg := f.watched(t, uncertain)
	svc.runLogged(t.Context())

	if got := runs(t, reg)["uncertain"]; got != 1 {
		t.Errorf(`runs{outcome="uncertain"} = %v, want 1`, got)
	}
	if got := lastRunOK(t, reg); got != 0 {
		t.Errorf("last_run_ok = %v after an unacknowledged send, want 0", got)
	}

	f.now = f.now.Add(3 * time.Hour)
	svc.runLogged(t.Context())

	if got := runs(t, reg)["lost"]; got != 1 {
		t.Errorf(`runs{outcome="lost"} = %v on the run after it, want 1`, got)
	}
	if got := lastRunOK(t, reg); got != 0 {
		t.Errorf("last_run_ok = %v with a period claimed and never confirmed, want 0", got)
	}
}
