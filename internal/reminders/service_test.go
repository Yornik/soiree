package reminders

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Yornik/soiree/internal/mailer"
	"github.com/Yornik/soiree/internal/migrate"
	"github.com/Yornik/soiree/internal/pgtest"
	"github.com/Yornik/soiree/internal/store"
)

func TestMain(m *testing.M) { pgtest.Main(m, startPostgres) }

// fakeSender records what would have been sent. Nothing in this package's
// tests can reach a mail server: a test suite that sends real mail sends it to
// real people the first time somebody runs it with production settings in the
// environment.
type fakeSender struct {
	mu   sync.Mutex
	sent []mailer.Message
	err  error
}

func (f *fakeSender) Send(_ context.Context, m mailer.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, m)
	return nil
}

func (f *fakeSender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

func (f *fakeSender) last(t *testing.T) mailer.Message {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		t.Fatal("no mail was sent")
	}
	return f.sent[len(f.sent)-1]
}

// fixture is one migrated database with a plan in it.
type fixture struct {
	pool  *pgxpool.Pool
	store *store.Store
	cfg   Config
	now   time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	pool := pgtest.Pool(t)
	if _, err := migrate.Run(t.Context(), pool, nil); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	cfg := testConfig(t, "Europe/Amsterdam", 14)
	cfg.To = []string{"ada@example.test", "grace@example.test"}

	return &fixture{
		pool:  pool,
		store: store.New(pool),
		cfg:   cfg,
		now:   at(t, "2027-02-25T09:00:00Z"),
	}
}

// service builds a service as a freshly started process would: same
// configuration, same database, a sender of its own.
func (f *fixture) service(t *testing.T, sender mailer.Sender) *Service {
	t.Helper()
	s := New(f.store, sender, f.cfg, nil)
	s.now = func() time.Time { return f.now }
	return s
}

func (f *fixture) seedDeadline(t *testing.T, name, vendor, lockBy string, unit, paid int64, position int32) {
	t.Helper()
	in := store.BudgetItem{Item: name, Vendor: vendor, Unit: unit, Qty: 1, Paid: paid, Position: position}
	if lockBy != "" {
		in.LockBy = day(t, lockBy)
	}
	if _, err := f.store.CreateBudgetItem(t.Context(), in, nil); err != nil {
		t.Fatalf("seed budget item: %v", err)
	}
}

func (f *fixture) seedTask(t *testing.T, name, owner, due string, status store.TaskStatus, position int32) {
	t.Helper()
	in := store.Task{Name: name, Owner: owner, Status: status, Position: position}
	if due != "" {
		in.Due = day(t, due)
	}
	if _, err := f.store.CreateTask(t.Context(), in, nil); err != nil {
		t.Fatalf("seed task: %v", err)
	}
}

// ledger reads the whole reminders_sent table.
func (f *fixture) ledger(t *testing.T) []struct {
	Key    string
	Sent   bool
	Items  int
	People int
} {
	t.Helper()
	rows, err := f.pool.Query(t.Context(),
		`SELECT period_key, sent_at IS NOT NULL, item_count, recipients
		   FROM reminders_sent ORDER BY period_key`)
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	defer rows.Close()

	var out []struct {
		Key    string
		Sent   bool
		Items  int
		People int
	}
	for rows.Next() {
		var r struct {
			Key    string
			Sent   bool
			Items  int
			People int
		}
		if err := rows.Scan(&r.Key, &r.Sent, &r.Items, &r.People); err != nil {
			t.Fatalf("scan ledger: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	return out
}

func TestRunOnceSendsTheDigest(t *testing.T) {
	f := newFixture(t)
	f.seedDeadline(t, "Venue deposit", "Grand Hall", "2027-02-20", 250000, 0, 1)
	f.seedDeadline(t, "Florist", "Linus Flowers", "2027-03-02", 42050, 0, 2)
	f.seedDeadline(t, "Photographer", "Grace Optics", "2027-08-01", 90000, 0, 3) // outside the window
	f.seedTask(t, "Send invitations", "Grace", "2027-02-26", store.TaskNotStarted, 1)

	sender := &fakeSender{}
	if err := f.service(t, sender).RunOnce(t.Context()); err != nil {
		t.Fatalf("run: %v", err)
	}

	if sender.count() != 1 {
		t.Fatalf("sent %d mails, want 1", sender.count())
	}
	msg := sender.last(t)
	if len(msg.To) != 2 {
		t.Errorf("recipients = %v, want both", msg.To)
	}
	if msg.Text == "" || msg.HTML == "" {
		t.Error("a digest went out without both a text and an HTML part")
	}
	if !strings.Contains(msg.Text, "Venue deposit") || !strings.Contains(msg.Text, "Send invitations") {
		t.Errorf("digest is missing the things that are due:\n%s", msg.Text)
	}
	if strings.Contains(msg.Text, "Photographer") {
		t.Errorf("digest includes a deadline beyond the window:\n%s", msg.Text)
	}

	led := f.ledger(t)
	if len(led) != 1 {
		t.Fatalf("ledger has %d rows, want 1", len(led))
	}
	if !led[0].Sent {
		t.Error("the period was claimed but never marked sent")
	}
	if led[0].Items != 3 || led[0].People != 2 {
		t.Errorf("ledger row = %+v, want 3 items and 2 recipients", led[0])
	}
}

// The thing this whole feature turns on: a restart, a redeploy or a second
// replica must not say it again.
func TestDigestIsNotResentAcrossARestart(t *testing.T) {
	f := newFixture(t)
	f.seedDeadline(t, "Venue deposit", "Grand Hall", "2027-02-27", 250000, 0, 1)

	first := &fakeSender{}
	if err := f.service(t, first).RunOnce(t.Context()); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if first.count() != 1 {
		t.Fatalf("first run sent %d mails, want 1", first.count())
	}

	// A new process, a new scheduler, the same database and the same week.
	// Its first act is to run the digest immediately, because a weekly ticker
	// in a pod that restarts daily would otherwise never fire.
	second := &fakeSender{}
	restarted := f.service(t, second)
	f.now = f.now.Add(3 * time.Hour)
	if err := restarted.RunOnce(t.Context()); err != nil {
		t.Fatalf("second run: %v", err)
	}
	// And again, a dozen restarts later.
	for range 3 {
		f.now = f.now.Add(20 * time.Minute)
		if err := f.service(t, second).RunOnce(t.Context()); err != nil {
			t.Fatalf("later run: %v", err)
		}
	}

	if second.count() != 0 {
		t.Errorf("a restart within the same period sent %d more digests", second.count())
	}
	if led := f.ledger(t); len(led) != 1 {
		t.Errorf("ledger has %d rows, want the one period", len(led))
	}
}

// The next period is a different digest, and does go out.
func TestTheNextPeriodSendsAgain(t *testing.T) {
	f := newFixture(t)
	// Inside the window on both sides of the period boundary.
	f.seedDeadline(t, "Venue deposit", "Grand Hall", "2027-03-08", 250000, 0, 1)

	sender := &fakeSender{}
	if err := f.service(t, sender).RunOnce(t.Context()); err != nil {
		t.Fatalf("first run: %v", err)
	}
	f.now = f.now.Add(f.cfg.Schedule)
	if err := f.service(t, sender).RunOnce(t.Context()); err != nil {
		t.Fatalf("second run: %v", err)
	}

	if sender.count() != 2 {
		t.Errorf("sent %d digests over two periods, want 2", sender.count())
	}
	if led := f.ledger(t); len(led) != 2 {
		t.Errorf("ledger has %d rows, want 2", len(led))
	}
}

// A period boundary is not on its own a reason to send. Two runs minutes
// apart, on either side of one, are the double-send a bucketed ledger cannot
// see by itself — the same digest, twice, at midnight on a Sunday.
func TestABoundaryStraddleDoesNotSendTwice(t *testing.T) {
	f := newFixture(t)
	f.seedDeadline(t, "Venue deposit", "Grand Hall", "2027-03-08", 250000, 0, 1)

	// 00:58 Monday in Amsterdam is 23:58 Sunday UTC: two minutes before the
	// week rolls over in the configured zone.
	f.now = at(t, "2027-02-28T22:58:00Z")
	sender := &fakeSender{}
	if err := f.service(t, sender).RunOnce(t.Context()); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if sender.count() != 1 {
		t.Fatalf("first run sent %d mails, want 1", sender.count())
	}

	f.now = f.now.Add(4 * time.Minute) // over the boundary, into the next week
	if err := f.service(t, sender).RunOnce(t.Context()); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if sender.count() != 1 {
		t.Errorf("crossing a period boundary four minutes later sent %d digests", sender.count())
	}

	// And the week after is a genuinely new digest, so the floor has not
	// turned the feature off.
	f.now = f.now.Add(f.cfg.Schedule)
	if err := f.service(t, sender).RunOnce(t.Context()); err != nil {
		t.Fatalf("third run: %v", err)
	}
	if sender.count() != 2 {
		t.Errorf("sent %d digests over two weeks, want 2", sender.count())
	}
}

// An empty digest is worse than silence, and it must not consume the period
// either — something added on Tuesday should still be able to go out.
func TestNothingDueSendsNothingAtAll(t *testing.T) {
	f := newFixture(t)
	f.seedDeadline(t, "Venue deposit", "Grand Hall", "2027-02-27", 250000, 5000, 1) // deposit paid
	f.seedDeadline(t, "Photographer", "Grace Optics", "2027-08-01", 90000, 0, 2)    // far off
	f.seedDeadline(t, "Cake", "Linus Bakes", "", 12000, 0, 3)                       // no deadline
	f.seedTask(t, "Book the band", "Ada", "2027-02-26", store.TaskDone, 1)

	sender := &fakeSender{}
	if err := f.service(t, sender).RunOnce(t.Context()); err != nil {
		t.Fatalf("run: %v", err)
	}

	if sender.count() != 0 {
		t.Errorf("sent %d mails with nothing due:\n%s", sender.count(), sender.last(t).Text)
	}
	if led := f.ledger(t); len(led) != 0 {
		t.Errorf("an empty digest claimed the period: %+v", led)
	}

	// The week is not used up: a deadline entered later still goes out.
	f.seedDeadline(t, "Florist", "Linus Flowers", "2027-02-28", 42050, 0, 4)
	if err := f.service(t, sender).RunOnce(t.Context()); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if sender.count() != 1 {
		t.Fatalf("sent %d mails after something became due, want 1", sender.count())
	}
}

// Two replicas start at the same instant. Only one sends.
func TestLeaderGuardStopsASecondReplica(t *testing.T) {
	f := newFixture(t)
	f.seedDeadline(t, "Venue deposit", "Grand Hall", "2027-02-27", 250000, 0, 1)

	// Stand in for the replica that got there first: hold the advisory lock on
	// a session of its own, exactly as RunOnce does.
	held, err := f.pool.Acquire(t.Context())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer held.Release()
	if _, err := held.Exec(t.Context(), "SELECT pg_advisory_lock($1)", leaderLockKey); err != nil {
		t.Fatalf("hold lock: %v", err)
	}

	sender := &fakeSender{}
	if err := f.service(t, sender).RunOnce(t.Context()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if sender.count() != 0 {
		t.Errorf("the replica that lost the lock sent %d digests", sender.count())
	}
	if led := f.ledger(t); len(led) != 0 {
		t.Errorf("the replica that lost the lock wrote to the ledger: %+v", led)
	}

	// Once the first replica is done, the lock comes back and the next run
	// works normally.
	if _, err := held.Exec(t.Context(), "SELECT pg_advisory_unlock($1)", leaderLockKey); err != nil {
		t.Fatalf("release lock: %v", err)
	}
	if err := f.service(t, sender).RunOnce(t.Context()); err != nil {
		t.Fatalf("run after release: %v", err)
	}
	if sender.count() != 1 {
		t.Errorf("sent %d digests after the lock came free, want 1", sender.count())
	}
}

// The lock must not be left on the pooled connection: pgxpool does not reset
// the session, so a leaked lock would block every digest for the life of the
// process.
func TestTheLockIsReleasedAfterEveryRun(t *testing.T) {
	f := newFixture(t)
	f.seedDeadline(t, "Venue deposit", "Grand Hall", "2027-02-27", 250000, 0, 1)

	sender := &fakeSender{}
	if err := f.service(t, sender).RunOnce(t.Context()); err != nil {
		t.Fatalf("run: %v", err)
	}

	var locks int
	if err := f.pool.QueryRow(t.Context(),
		`SELECT count(*) FROM pg_locks
		    WHERE locktype = 'advisory'
		      AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`).Scan(&locks); err != nil {
		t.Fatalf("read pg_locks: %v", err)
	}
	if locks != 0 {
		t.Errorf("%d advisory locks still held after the run", locks)
	}
}

// A send that failed before the server had the message is safe to try again,
// so the claim comes back off.
func TestARefusedSendReleasesThePeriod(t *testing.T) {
	f := newFixture(t)
	f.seedDeadline(t, "Venue deposit", "Grand Hall", "2027-02-27", 250000, 0, 1)

	broken := &fakeSender{err: &mailer.SendError{Err: errors.New("connection refused")}}
	if err := f.service(t, broken).RunOnce(t.Context()); err == nil {
		t.Fatal("expected the failed send to be reported")
	}
	if led := f.ledger(t); len(led) != 0 {
		t.Fatalf("a retryable failure left the period claimed: %+v", led)
	}

	working := &fakeSender{}
	if err := f.service(t, working).RunOnce(t.Context()); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if working.count() != 1 {
		t.Errorf("the retry sent %d digests, want 1", working.count())
	}
}

// A send that died at the terminating dot may or may not have been delivered.
// The claim stays, so it is never sent a second time, and the row is left
// without a sent_at for somebody to find.
func TestAnUncertainSendIsNeverRetried(t *testing.T) {
	f := newFixture(t)
	f.seedDeadline(t, "Venue deposit", "Grand Hall", "2027-02-27", 250000, 0, 1)

	uncertain := &fakeSender{err: &mailer.SendError{Err: errors.New("connection reset"), Ambiguous: true}}
	if err := f.service(t, uncertain).RunOnce(t.Context()); err == nil {
		t.Fatal("expected the uncertain send to be reported")
	}

	led := f.ledger(t)
	if len(led) != 1 {
		t.Fatalf("ledger has %d rows, want the claim to have survived", len(led))
	}
	if led[0].Sent {
		t.Error("an unacknowledged send was recorded as sent")
	}

	retry := &fakeSender{}
	if err := f.service(t, retry).RunOnce(t.Context()); err != nil {
		t.Fatalf("later run: %v", err)
	}
	if retry.count() != 0 {
		t.Errorf("a period whose delivery was uncertain was sent again %d times", retry.count())
	}
}

// Start is the scheduler as main would use it: it runs once immediately,
// because a weekly ticker in a pod that restarts daily never fires.
func TestStartRunsImmediatelyAndStops(t *testing.T) {
	f := newFixture(t)
	f.seedDeadline(t, "Venue deposit", "Grand Hall", "2027-02-27", 250000, 0, 1)

	sender := &fakeSender{}
	stop := f.service(t, sender).Start(t.Context())

	deadline := time.Now().Add(20 * time.Second)
	for sender.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	stop()

	if sender.count() != 1 {
		t.Errorf("the scheduler sent %d digests on start, want 1", sender.count())
	}
}
