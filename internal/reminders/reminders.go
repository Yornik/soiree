// Package reminders mails a digest of the deadlines that are about to bite.
//
// `budget_items.lock_by` is the date a decision has to be made or the price or
// the slot is gone, and a date column that reminds nobody is decoration. The
// predecessor to this application ended up with those dates hand-written into
// prose paragraphs that nobody reread; this package is the answer to that.
// Tasks with a due date are in the same digest, because "the florist must be
// confirmed by Friday" and "somebody has to phone the florist by Friday" are
// the same obligation to the person reading their mail on Sunday.
//
// # Off unless asked for
//
// The scheduler does not run unless SOIREE_REMINDER_ENABLED is true, there are
// recipients, and SMTP is configured. Missing SMTP is not a startup failure:
// most ways of running this application do not send mail at all, and an
// application that refuses to boot without a mail server is an application
// that cannot be run locally.
//
// # Sending exactly once
//
// Three things would otherwise send the same digest twice: a second replica, a
// redeploy, and a crash loop. The defences are layered.
//
// A Postgres advisory lock, taken the same way internal/migrate takes its own,
// makes one replica the sender for the duration of a run. That handles the
// simultaneous case and nothing else — a pod that restarts an hour later takes
// the lock uncontested and would happily send again.
//
// So the real guard is the reminders_sent ledger, keyed by a period computed
// from the schedule rather than from the clock the process started at. Every
// replica, in every process, computes the same key for the same week. The row
// goes in before the mail goes out, which makes this at-most-once: a crash
// between the claim and the acknowledgement loses that period's digest rather
// than duplicating it. That is the right way round, and not a compromise — an
// unresolved deadline is still unresolved next period and comes back marked
// overdue, whereas a digest that arrives twice is how a mail becomes noise.
//
// A bucket, though, still has an edge. Two runs either side of one — a restart
// at one minute to midnight and the ticker at one minute past — are different
// periods by the ledger's own rules, and would each send. So a run also
// refuses to go out within half a period of the last one. That floor is what
// closes the boundary, and it is measured on the scheduler's clock against a
// claimed_at the scheduler wrote, not against the database's now().
//
// An empty digest is never claimed and never sent. A weekly mail that usually
// says nothing is a weekly mail nobody opens, and the week it matters is the
// week it gets ignored.
//
// # Recipients
//
// For now, a configured address list. The `users` table is being built in
// parallel and this deliberately does not depend on it. Moving to per-user
// delivery is a change in one place: Service.run composes one digest and sends
// it to Config.To, and per-user means iterating over subscribers and sending
// the same Digest to each — Compose and Render take no recipient at all. The
// ledger's primary key would gain the subscriber id alongside the period key,
// so one person's bounce cannot suppress everyone else's digest, and Config.To
// stays as the fallback for deployments with no accounts.
package reminders

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Yornik/soiree/internal/mailer"
	"github.com/Yornik/soiree/internal/store"
)

// runTimeout bounds one digest: reading the plan, composing, and the whole
// SMTP conversation. Without it a mail server that accepts a connection and
// then says nothing holds the advisory lock until the process is restarted,
// and nobody gets a digest until it is.
const runTimeout = 2 * time.Minute

// Service is the scheduler.
type Service struct {
	store  *store.Store
	sender mailer.Sender
	cfg    Config
	log    *slog.Logger

	// now is the clock, injected so the window boundaries and the period key
	// are testable without waiting a week.
	now func() time.Time
}

// New builds a service. A nil logger discards.
func New(st *store.Store, sender mailer.Sender, cfg Config, log *slog.Logger) *Service {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Service{store: st, sender: sender, cfg: cfg, log: log, now: time.Now}
}

// Start runs the digest now and then once per schedule, and returns a function
// that stops it and waits for the run in flight.
//
// Running immediately rather than waiting out the first period is deliberate.
// The schedule is weekly and pods restart far more often than that, so a
// ticker that only fires after 168 uninterrupted hours would never fire at
// all. It is safe precisely because the period key makes a second run within
// the same week a no-op.
func (s *Service) Start(ctx context.Context) func() {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})

	// A Config built by hand rather than by LoadConfig can carry a zero
	// schedule, and time.NewTicker(0) panics.
	schedule := s.cfg.Schedule
	if schedule <= 0 {
		schedule = DefaultSchedule
	}

	go func() {
		defer close(done)

		ticker := time.NewTicker(schedule)
		defer ticker.Stop()

		s.runLogged(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.runLogged(ctx)
			}
		}
	}()

	return func() {
		cancel()
		<-done
	}
}

// runLogged runs one digest and reports a failure rather than propagating it.
// A mail server being down is not a reason to stop reminding people next week.
func (s *Service) runLogged(ctx context.Context) {
	runCtx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()

	if err := s.RunOnce(runCtx); err != nil {
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			return // shutting down
		}
		s.log.Error("reminder digest failed", "err", err)
	}
}

// RunOnce composes and sends the digest for the current period, if this
// replica is the one sending and the period has not already gone out.
//
// The order of the steps is load-bearing:
//
//  1. take the advisory lock, or do nothing at all;
//  2. check the ledger, so a restart inside the same period is a no-op;
//  3. check the gap since the last digest, so crossing a period boundary
//     moments after one went out is a no-op too;
//  4. compose, and stop here if there is nothing to say — which is what lets
//     "never send an empty digest" and "claim before sending" both be true;
//  5. claim the period;
//  6. send;
//  7. confirm.
func (s *Service) RunOnce(ctx context.Context) error {
	now := s.now()
	key := periodKey(s.cfg.Schedule, dayIn(now, s.cfg.location()))

	conn, err := s.store.Pool().Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	leader, err := takeLeaderLock(ctx, conn)
	if err != nil {
		return err
	}
	if !leader {
		s.log.Info("reminder digest skipped: another replica is sending it", "period", key)
		return nil
	}
	defer releaseLeaderLock(ctx, conn, s.log)

	p, err := lookupPeriod(ctx, conn, key)
	if err != nil {
		return err
	}
	if p.found {
		if p.sentAt == nil {
			// Claimed, never confirmed: something died between handing the
			// message over and hearing back. Deliberately not retried, and
			// deliberately loud, because this is the one case where a digest
			// is lost and somebody should know.
			s.log.Warn("reminder digest for this period was claimed but never confirmed sent; not retrying", "period", key)
		}
		return nil
	}

	// A new period beginning is not on its own a reason to send: a run just
	// before a day boundary and one just after are minutes apart, and two
	// digests minutes apart is the duplicate this whole file exists to avoid.
	last, err := lastClaimed(ctx, conn)
	if err != nil {
		return err
	}
	if gap := cooloff(s.cfg.Schedule); last != nil && now.Sub(*last) < gap {
		s.log.Info("reminder digest skipped: one went out too recently",
			"period", key, "lastSent", last, "minimumGap", gap.String())
		return nil
	}

	items, err := s.store.BudgetItems(ctx)
	if err != nil {
		return fmt.Errorf("read budget items: %w", err)
	}
	tasks, err := s.store.Tasks(ctx)
	if err != nil {
		return fmt.Errorf("read tasks: %w", err)
	}

	digest := Compose(s.cfg, now, items, tasks)
	if digest.Empty() {
		s.log.Info("nothing due; no reminder sent", "period", key, "windowDays", s.cfg.WindowDays)
		return nil
	}

	msg, err := Render(digest)
	if err != nil {
		return err
	}
	msg.To = s.cfg.To

	claimed, err := claimPeriod(ctx, conn, key, now, len(s.cfg.To), digest.Count())
	if err != nil {
		return err
	}
	if !claimed {
		// Unreachable while the advisory lock is held, and kept because the
		// ledger — not the lock — is what this feature's correctness rests on.
		s.log.Info("reminder digest already claimed by another sender", "period", key)
		return nil
	}

	if err := s.sender.Send(ctx, msg); err != nil {
		if mailer.Ambiguous(err) {
			// The server may or may not have taken it. The claim stays, so
			// this period is never sent twice; it is reported here and visible
			// in the ledger as a row with no sent_at.
			s.log.Error("reminder digest delivery uncertain; not retrying this period", "period", key, "err", err)
			return err
		}
		if rerr := releaseClaim(ctx, conn, key); rerr != nil {
			return errors.Join(err, rerr)
		}
		return fmt.Errorf("send digest: %w", err)
	}

	if err := confirmSent(ctx, conn, key, s.now()); err != nil {
		// The mail is out; only the bookkeeping failed. Reporting this as a
		// failed run would invite a retry of something that already happened.
		s.log.Error("reminder digest was sent but could not be marked sent", "period", key, "err", err)
		return nil
	}

	s.log.Info("reminder digest sent",
		"period", key, "recipients", len(s.cfg.To), "items", digest.Count(), "overdue", digest.Overdue())
	return nil
}

// Start loads the configuration from the environment and starts the scheduler
// if it is switched on and able to send.
//
// The returned stop function is always safe to call. An error means a setting
// is malformed — a duration nobody can parse, an address that is not one —
// which is worth telling an operator about; it never means "no SMTP", because
// that is a normal way to run this.
//
// Wiring it up, once cmd/soiree has a pool:
//
//	stopReminders, err := reminders.Start(ctx, st, log)
//	if err != nil {
//		log.Error("reminder configuration is invalid", "err", err)
//		os.Exit(1)
//	}
//	defer stopReminders()
func Start(ctx context.Context, st *store.Store, log *slog.Logger) (func(), error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	noop := func() {}

	cfg, err := LoadConfig()
	if err != nil {
		return noop, err
	}
	smtp, err := mailer.LoadConfig()
	if err != nil {
		return noop, err
	}

	switch {
	case !cfg.Enabled:
		log.Info("deadline reminders are off (SOIREE_REMINDER_ENABLED is not true)")
		return noop, nil
	case !smtp.Configured():
		log.Warn("deadline reminders are on but SMTP is not configured; no digest will be sent",
			"missing", "SOIREE_SMTP_HOST and SOIREE_SMTP_FROM")
		return noop, nil
	case len(cfg.To) == 0:
		log.Warn("deadline reminders are on but there are no recipients; no digest will be sent",
			"missing", "SOIREE_REMINDER_TO")
		return noop, nil
	}

	log.Info("deadline reminders on",
		"schedule", cfg.Schedule.String(), "windowDays", cfg.WindowDays,
		"recipients", len(cfg.To), "timezone", cfg.Zone)

	return New(st, mailer.New(smtp), cfg, log).Start(ctx), nil
}
