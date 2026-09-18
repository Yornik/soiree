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
	"strings"
	"time"

	"github.com/Yornik/soiree/internal/mailer"
	"github.com/Yornik/soiree/internal/push"
	"github.com/Yornik/soiree/internal/store"
)

// Budgets for one digest. runTimeout bounds the whole of it — reading the
// plan, composing, the SMTP conversation and the notifications — because
// without it a mail server that accepts a connection and then says nothing
// holds the advisory lock until the process is restarted, and nobody gets a
// digest until it is.
//
// The two channels then get separate sub-budgets that add up to it. That split
// is not tidiness: with one shared deadline, a relay that stalls spends the
// entire run before push is reached, and a broken relay would switch off
// notifications too. Neither channel is allowed to do that to the other.
const (
	runTimeout  = 2 * time.Minute
	mailTimeout = 90 * time.Second
	pushTimeout = 30 * time.Second
)

// Service is the scheduler.
type Service struct {
	store  *store.Store
	sender mailer.Sender
	cfg    Config
	log    *slog.Logger

	// pusher is the notification channel, nil when this deployment has no
	// VAPID keys. Nil rather than a flag, so that "there is no push here" is a
	// state the type system carries rather than one every call site checks.
	pusher *push.Sender

	// now is the clock, injected so the window boundaries and the period key
	// are testable without waiting a week.
	now func() time.Time
}

// New builds a service. A nil logger discards, and a nil sender means this
// deployment has no relay — legal, and the digest then goes out over push
// alone or not at all.
func New(st *store.Store, sender mailer.Sender, cfg Config, log *slog.Logger) *Service {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Service{store: st, sender: sender, cfg: cfg, log: log, now: time.Now}
}

// WithPush attaches the notification channel.
//
// Separate from New because it is optional, exactly as httpd.WithAuth is: a
// deployment with no VAPID keys sends the digest by mail alone, which is how
// every deployment worked before this existed.
func (s *Service) WithPush(p *push.Sender) *Service {
	s.pusher = p
	return s
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

	to, err := s.recipients(ctx)
	if err != nil {
		return err
	}
	if len(to) == 0 {
		// Every admin could have been disabled since startup. Nothing to do,
		// and no claim, so the digest resumes when somebody can receive it.
		//
		// Still the right place to stop now that push exists, and the reason is
		// an invariant rather than an oversight: a notifiable subscription
		// belongs to an active admin, and every active admin is in `to`. No
		// recipients here therefore means no devices either, so this cannot
		// silently suppress the other channel. If that ever stops being true —
		// if subscriptions are opened to editors, say — this check has to move
		// below the push.
		s.log.Warn("deadline digest has nothing due to nobody: no active admin and no configured recipient")
		return nil
	}

	msg, err := Render(digest)
	if err != nil {
		return err
	}
	msg.To = to

	claimed, err := claimPeriod(ctx, conn, key, now, len(to), digest.Count())
	if err != nil {
		return err
	}
	if !claimed {
		// Unreachable while the advisory lock is held, and kept because the
		// ledger — not the lock — is what this feature's correctness rests on.
		s.log.Info("reminder digest already claimed by another sender", "period", key)
		return nil
	}

	// Both channels are attempted, and neither is allowed to decide the
	// other's fate. Mail goes first and its error is carried rather than
	// returned, so that no failure in the notification path — an error, a
	// timeout, a push service having a bad minute — can happen before the mail
	// has been handed over.
	mailErr := s.sendMail(ctx, msg)
	delivered := s.pushDigest(ctx, digest)

	if mailErr != nil {
		if mailer.Ambiguous(mailErr) {
			// The server may or may not have taken it. The claim stays, so
			// this period is never sent twice; it is reported here and visible
			// in the ledger as a row with no sent_at.
			s.log.Error("reminder digest delivery uncertain; not retrying this period", "period", key, "err", mailErr)
			return mailErr
		}
		// The mail definitely did not go out, so the period may be tried
		// again — but only if nothing else went out either. Releasing the
		// claim asserts that this period reached nobody, and that stops being
		// true the moment a notification lands on somebody's phone: a retry
		// would then push the same digest at them a second time, which is
		// precisely what the ledger exists to prevent.
		if delivered == 0 {
			if rerr := releaseClaim(ctx, conn, key); rerr != nil {
				return errors.Join(mailErr, rerr)
			}
			return fmt.Errorf("send digest: %w", mailErr)
		}
		s.log.Error("reminder digest could not be mailed, but reached some devices; the period stays claimed and will not be retried",
			"period", key, "devicesReached", delivered, "err", mailErr)
		return fmt.Errorf("send digest: %w", mailErr)
	}

	if err := confirmSent(ctx, conn, key, s.now()); err != nil {
		// The mail is out; only the bookkeeping failed. Reporting this as a
		// failed run would invite a retry of something that already happened.
		s.log.Error("reminder digest was sent but could not be marked sent", "period", key, "err", err)
		return nil
	}

	s.log.Info("reminder digest sent",
		"period", key, "recipients", len(to), "devicesReached", delivered,
		"items", digest.Count(), "overdue", digest.Overdue())
	return nil
}

// sendMail hands the digest to the relay, within a budget of its own.
//
// A nil sender is a deployment with no SMTP at all, which since push arrived is
// a supported way to run this rather than a reason to have the scheduler switch
// itself off. It reports success because the mail channel did everything it
// could: there was none.
func (s *Service) sendMail(ctx context.Context, msg mailer.Message) error {
	if s.sender == nil {
		return nil
	}
	// Bounded below the run, so that a relay which accepts a connection and
	// then says nothing leaves time for the notifications. Without this the
	// two channels share one deadline and the slower one eats it.
	mailCtx, cancel := context.WithTimeout(ctx, mailTimeout)
	defer cancel()
	return s.sender.Send(mailCtx, msg)
}

// pushDigest notifies every subscribed device and returns how many took it.
//
// It never returns an error and never propagates one. Push is the second
// channel for something that has already been mailed; a push service being
// unreachable is worth a log line and nothing more, and must not turn a digest
// that went out perfectly well into a failed run.
//
// Pruning is the part worth reading. A push service answers 404 or 410 when a
// subscription no longer exists — cleared storage, revoked permission, a
// retired endpoint — and that answer is final. Those rows are deleted here.
// Everything else (a 500, a timeout, a refused connection) is the service
// having a bad minute and says nothing about the subscription, so those rows
// stay. Get that backwards in the lenient direction and the table fills with
// endpoints that will never accept another notification, each costing a round
// trip on every digest for the life of the deployment; get it backwards in the
// strict direction and one bad minute unsubscribes everybody.
func (s *Service) pushDigest(ctx context.Context, d Digest) int {
	if s.pusher == nil {
		return 0
	}

	// A budget of its own, and a cancelled context is replaced rather than
	// obeyed. The period is already claimed by the time this runs, so giving
	// up here does not defer the notification — it loses it, and the ledger
	// will not offer this digest again. The window is short enough to sit
	// inside a termination grace period.
	base := ctx
	if base.Err() != nil {
		base = context.WithoutCancel(ctx)
	}
	pushCtx, cancel := context.WithTimeout(base, pushTimeout)
	defer cancel()

	subs, err := s.store.NotifiablePushSubscriptions(pushCtx)
	if err != nil {
		s.log.Error("could not read push subscriptions; the digest goes out by mail only", "err", err)
		return 0
	}
	if len(subs) == 0 {
		return 0
	}

	n := RenderPush(d, s.cfg.BaseURL)

	var gone []string
	delivered := 0
	for _, sub := range subs {
		err := s.pusher.Send(pushCtx, push.Subscription{
			Endpoint: sub.Endpoint,
			P256dh:   sub.P256dh,
			Auth:     sub.Auth,
		}, n)
		switch {
		case err == nil:
			delivered++
		case errors.Is(err, push.ErrGone):
			gone = append(gone, sub.Endpoint)
		default:
			// The account id, never the endpoint: an endpoint is the
			// capability to notify that device, and a log file is read by more
			// people than a database is.
			s.log.Warn("could not notify a device; keeping the subscription", "user", sub.UserID, "err", err)
		}
	}

	if len(gone) > 0 {
		// Detached, because this is the cleanup for what just happened and a
		// budget that has run out is the likeliest reason to be here with rows
		// to delete. Leaving them costs every future digest.
		delCtx, delCancel := context.WithTimeout(context.WithoutCancel(ctx), pushTimeout)
		defer delCancel()
		removed, err := s.store.DeletePushSubscriptionsByEndpoint(delCtx, gone)
		if err != nil {
			s.log.Error("could not remove push subscriptions the push service says are gone", "count", len(gone), "err", err)
		} else {
			s.log.Info("push subscriptions removed: the push service says they no longer exist", "count", removed)
		}
	}

	s.log.Info("deadline digest pushed",
		"devices", len(subs), "delivered", delivered, "gone", len(gone))
	return delivered
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
	// Read here rather than taken from internal/config, following the mailer:
	// the scheduler is handed a store and a logger and nothing else, and
	// threading a configuration struct through it to carry three strings would
	// be a worse trade than reading the same three variables twice.
	vapid := push.LoadConfig()

	switch {
	case !cfg.Enabled:
		log.Info("deadline reminders are off (SOIREE_REMINDER_ENABLED is not true)")
		return noop, nil
	case !smtp.Configured() && !vapid.Configured():
		// Neither channel, so there is nothing to schedule. One of the two is
		// enough: a deployment that notifies and does not mail is as complete
		// a deployment as the other way round, and refusing to run because the
		// *other* channel is missing would be one channel suppressing the one
		// that works.
		log.Warn("deadline reminders are on but neither mail nor push is configured; no digest will be sent",
			"missing", "SOIREE_SMTP_HOST and SOIREE_SMTP_FROM, or the three SOIREE_VAPID_* keys")
		return noop, nil
	}

	// Never a startup failure — but an operator who set one VAPID variable and
	// stopped believes they have notifications and does not. From the outside
	// that is indistinguishable from having configured nothing.
	if vapid.Partial() {
		log.Warn("Web Push is half-configured, so notifications are off",
			"need", "SOIREE_VAPID_PUBLIC_KEY, SOIREE_VAPID_PRIVATE_KEY and SOIREE_VAPID_SUBJECT")
	}
	if !smtp.Configured() {
		log.Warn("SMTP is not configured; the deadline digest goes out as a notification only")
	}

	log.Info("deadline reminders on",
		"schedule", cfg.Schedule.String(), "windowDays", cfg.WindowDays,
		"recipients", len(cfg.To), "timezone", cfg.Zone,
		"mail", smtp.Configured(), "push", vapid.Configured())

	// A nil sender rather than a mailer built from nothing: the send path
	// reads nil as "this deployment has no relay", where a configured-looking
	// mailer with no host would fail once per period forever.
	var sender mailer.Sender
	if smtp.Configured() {
		sender = mailer.New(smtp)
	}

	svc := New(st, sender, cfg, log)
	if vapid.Configured() {
		svc = svc.WithPush(push.New(vapid))
	}
	return svc.Start(ctx), nil
}

// recipients is every active admin, plus anything SOIREE_REMINDER_TO names.
//
// Resolved per send rather than at startup, so an admin added or disabled
// between digests is respected without restarting anything. The static list
// stays supported for two cases the database cannot answer: a deployment with
// no accounts at all, and somebody who should read the digest without being
// given a login to the event's finances.
//
// One message to everyone rather than one each. The ledger then still records
// a single claim per period, which is what makes a restart mid-send unable to
// mail anybody twice; sending individually would need the claim keyed per
// recipient to keep that property.
func (s *Service) recipients(ctx context.Context) ([]string, error) {
	admins, err := s.store.NotifiableAdmins(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve admin recipients: %w", err)
	}

	seen := make(map[string]bool, len(admins)+len(s.cfg.To))
	out := make([]string, 0, len(admins)+len(s.cfg.To))
	for _, addr := range append(admins, s.cfg.To...) {
		key := strings.ToLower(strings.TrimSpace(addr))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, addr)
	}
	return out, nil
}
