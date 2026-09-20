package reminders

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// leaderLockKey is the advisory lock that makes one replica the sender for the
// duration of a run. The value is arbitrary and means nothing; what matters is
// that every replica picks the same one and that it is not the migration
// runner's key (see internal/migrate), which would deadlock a startup against
// a digest.
const leaderLockKey int64 = 0x5010_1EE0_0002

// unlockTimeout bounds the release of the advisory lock, on a context detached
// from the caller's so that a cancelled shutdown still gives it a moment.
const unlockTimeout = 5 * time.Second

// periodKey names the digest a run belongs to.
//
// today is the calendar day the run falls on in the event's timezone, pinned to
// UTC midnight (see dayIn). Buckets are whole calendar days wide, counted from
// the epoch, so every replica, in every process, on either side of a restart,
// computes the same string for the same week. That string is the primary key of
// reminders_sent, and it is the bulk of the idempotence: the advisory lock only
// stops two replicas colliding within one instant, while this stops a pod that
// restarted an hour later from starting the week again.
//
// Calendar days rather than time.Truncate on the instant. Truncate buckets from
// the zero time, which puts the boundary at an arbitrary hour derived from the
// epoch: two runs a minute apart, on either side of it, would land in different
// weeks and send two digests. A day boundary in the event's own zone is both
// predictable and the coarsest unit the underlying data has — lock_by and due
// are dates, so nothing a digest reports can change within a day.
//
// The width is part of the key because changing the schedule changes what a
// period means; the cost is one extra digest after an operator edits
// SOIREE_REMINDER_SCHEDULE, which is the honest outcome.
func periodKey(schedule time.Duration, today time.Time) string {
	width := int(schedule / (24 * time.Hour))
	if width < 1 {
		width = 1
	}
	const secondsPerDay = 24 * 60 * 60
	days := today.UTC().Unix() / secondsPerDay
	bucket := floorDiv(days-mondayOffset, int64(width))
	start := time.Unix((bucket*int64(width)+mondayOffset)*secondsPerDay, 0).UTC()
	return fmt.Sprintf("digest/%dd/%s", width, start.Format(time.DateOnly))
}

// mondayOffset shifts the day counting so that bucket zero starts on Monday
// 5 January 1970 rather than on Thursday 1 January. Without it a weekly digest
// would arrive on Thursdays for no reason anybody could explain — an artefact
// of the epoch — and "the weekly mail comes out on Monday" is a thing people
// can hold in their heads.
const mondayOffset = 4

// floorDiv divides rounding towards negative infinity, so bucket boundaries do
// not bunch up either side of the epoch. Go's / truncates towards zero, which
// would make the two days on either side of 1970 share a bucket.
func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

// lastClaimed is when a digest was last taken on, for any period.
//
// A bucket boundary is still a boundary: a run at one minute to midnight and
// another at one minute past are in different periods and would each send. The
// ledger cannot tell them apart, because by its own rules they are different
// digests — so the floor below is what makes them behave like the same one.
func lastClaimed(ctx context.Context, conn *pgxpool.Conn) (*time.Time, error) {
	var at *time.Time
	if err := conn.QueryRow(ctx, `SELECT max(claimed_at) FROM reminders_sent`).Scan(&at); err != nil {
		return nil, fmt.Errorf("read reminders_sent: %w", err)
	}
	return at, nil
}

// cooloff is the shortest gap allowed between two digests, whatever the
// buckets say. Half a period: far enough apart that crossing a boundary
// moments after a send cannot produce a second mail, close enough that it
// never suppresses the next period's digest, which arrives a whole period
// later.
func cooloff(schedule time.Duration) time.Duration { return schedule / 2 }

// takeLeaderLock tries, without blocking, to become the replica that sends.
//
// Try rather than wait: a replica that loses the race has nothing useful to do
// afterwards — by the time the lock came free the winner would already have
// recorded the period — so blocking would only park a goroutine until the
// other one finished.
func takeLeaderLock(ctx context.Context, conn *pgxpool.Conn) (bool, error) {
	var got bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", leaderLockKey).Scan(&got); err != nil {
		return false, fmt.Errorf("take reminder lock: %w", err)
	}
	return got, nil
}

// releaseLeaderLock gives the lock back.
//
// pgxpool does not reset the session when a connection is returned, so a lock
// left behind here outlives the function and blocks every later digest for the
// life of that connection — which, in a pool, is the life of the process.
func releaseLeaderLock(ctx context.Context, conn *pgxpool.Conn, log *slog.Logger) {
	unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), unlockTimeout)
	defer cancel()
	if _, err := conn.Exec(unlockCtx, "SELECT pg_advisory_unlock($1)", leaderLockKey); err != nil {
		log.Error("failed to release reminder lock", "err", err)
	}
}

// period is what the ledger knows about one digest.
type period struct {
	found  bool
	sentAt *time.Time
}

// lookupPeriod reads the ledger row for a period, if there is one.
func lookupPeriod(ctx context.Context, conn *pgxpool.Conn, key string) (period, error) {
	var sentAt *time.Time
	err := conn.QueryRow(ctx, `SELECT sent_at FROM reminders_sent WHERE period_key = $1`, key).Scan(&sentAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return period{}, nil
	case err != nil:
		return period{}, fmt.Errorf("read reminders_sent: %w", err)
	}
	return period{found: true, sentAt: sentAt}, nil
}

// claimPeriod records the intent to send, before anything is sent.
//
// Claiming first is what makes a crash mid-send safe. The alternative —
// recording afterwards — loses the record in exactly the window where the mail
// did go out, and the next start of the process sends it again. A digest that
// arrives twice is worse than one that does not arrive: the second one teaches
// people the mail is noise, and this feature only works while they still read
// it.
//
// It returns false when somebody else already holds the period. Under the
// advisory lock that cannot happen, which is the point of doing it with ON
// CONFLICT anyway: the correctness does not rest on the lock being held.
//
// claimed_at is written from the scheduler's own clock rather than left to the
// column default. The gap between two digests is measured against that same
// clock, and a value measured on one clock and compared against another is not
// a duration at all — which is a thing a test can prove and production quietly
// cannot.
func claimPeriod(ctx context.Context, conn *pgxpool.Conn, key string, claimedAt time.Time, recipients, items int) (bool, error) {
	tag, err := conn.Exec(ctx,
		`INSERT INTO reminders_sent (period_key, claimed_at, recipients, item_count)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (period_key) DO NOTHING`,
		key, claimedAt, recipients, items)
	if err != nil {
		return false, fmt.Errorf("claim reminder period: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// confirmSent marks the claim as delivered. A period with a claim and no
// confirmation is the trace of a process that died between handing the message
// to the server and hearing back.
//
// Detached from the caller's context for the same reason releaseClaim is, and
// with more at stake: by the time this runs the digest is irrevocably out, so a
// context that died during the send — a SIGTERM mid-conversation, or the run's
// two minutes spent on a slow relay and a slow push service — would leave the
// ledger reporting a digest everybody received as the one case it calls lost.
func confirmSent(ctx context.Context, conn *pgxpool.Conn, key string, sentAt time.Time) error {
	updCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), unlockTimeout)
	defer cancel()
	if _, err := conn.Exec(updCtx,
		`UPDATE reminders_sent SET sent_at = $2 WHERE period_key = $1`, key, sentAt); err != nil {
		return fmt.Errorf("confirm reminder sent: %w", err)
	}
	return nil
}

// releaseClaim undoes a claim for a send that definitely did not happen —
// the server refused the connection, the credentials, or a recipient. Nothing
// was delivered, so the next tick may try again.
//
// Detached from the caller's context because the reason the send failed is
// sometimes that the context was cancelled, and a claim that cannot be undone
// costs a period.
func releaseClaim(ctx context.Context, conn *pgxpool.Conn, key string) error {
	delCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), unlockTimeout)
	defer cancel()
	if _, err := conn.Exec(delCtx, `DELETE FROM reminders_sent WHERE period_key = $1`, key); err != nil {
		return fmt.Errorf("release reminder claim: %w", err)
	}
	return nil
}
