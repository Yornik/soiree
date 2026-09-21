package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Change fan-out.
//
// Two people editing the same plan each see a correct view right up until the
// other one changes something, at which point they are looking at stale figures
// with no way to know it. Polling would find out eventually and cost a request
// per client per interval to find out nothing most of the time; this announces
// the change instead.
//
// The announcement is issued from insertChange, inside the transaction that
// made the change. That placement is the whole design:
//
//   - NOTIFY is transactional in PostgreSQL — the payload is queued and only
//     delivered when the transaction commits — so a write that rolls back
//     announces nothing, with no compensating logic to get wrong.
//   - insertChange is the single funnel every write in this package already
//     goes through to record its history. Emitting from there means a change
//     cannot be announced without also being recorded, and cannot be recorded
//     without also being announced.
//
// Erasure is the one exception, and it goes the one way that is safe: it
// announces without recording. An entry for it would hold the value being
// erased, so announceErasure in privacy.go calls notifyChange directly. The
// rule that matters is unbroken, because nothing is recorded in silence.
//
// What travels is identifiers, never the row. PostgreSQL caps a NOTIFY payload
// at 8000 bytes and a budget note alone can approach that, but the more
// important reason is that a listener is not an authorisation boundary: the
// payload says *what changed*, and the client re-reads it through the API,
// which is where the rules about who may see what live.

// ChangeChannel is the LISTEN/NOTIFY channel every change is announced on.
//
// One channel rather than one per entity: a listener wants all of them, and
// PostgreSQL's LISTEN state is per connection, so a channel per entity would
// mean either several connections or several LISTEN statements to keep in step
// with the entity list.
const ChangeChannel = "soiree_changes"

// maxNotifyPayload is PostgreSQL's hard limit on a NOTIFY payload.
const maxNotifyPayload = 8000

// listenCloseTimeout bounds the polite close of a listening connection. Kept
// short: the alternative to a clean goodbye is the socket going away, which the
// server handles perfectly well.
const listenCloseTimeout = 2 * time.Second

// ErrUnknownNotice reports a notification on ChangeChannel that this package
// did not write. Nothing in the application produces one — a human at a psql
// prompt does — so it is reported rather than treated as a broken connection.
var ErrUnknownNotice = errors.New("store: unrecognised notification")

// ChangeNotice is what one change announces about itself: enough to know which
// row to re-read, and nothing more.
//
// Revision is what makes the notice actionable rather than merely interesting.
// A client that already holds this revision — because it is the one who just
// wrote it — can ignore its own echo without the server having to say who
// caused the change.
type ChangeNotice struct {
	// Entity is the table, one of the Entity* constants, so the notice, the
	// change log and the schema all use the same word for the same thing.
	Entity string `json:"entity"`
	// ID is nil for the settings singleton, which has no id of its own.
	ID     *uuid.UUID   `json:"id"`
	Action ChangeAction `json:"action"`
	// Revision as the row now stands, or nil for a row that carries none.
	Revision *int64 `json:"revision"`
}

// unannouncedEntities are recorded in the history but not broadcast.
//
// `users` is the only one. The stream is the plan's change feed and an account
// is not plan state; more to the point, /api/v1/users is admin-only while this
// stream is as open as the rest of /api/v1, and announcing "user <id> changed"
// to anyone who connects would hand out the existence and count of accounts
// through a door the users API keeps shut.
var unannouncedEntities = map[string]bool{
	EntityUsers: true,
}

// notifyChange announces one recorded change on ChangeChannel.
//
// It takes the pgx.Tx rather than the pool because that is the point: the
// notification is queued inside the transaction that made the change and
// delivered only if that transaction commits.
func notifyChange(ctx context.Context, tx pgx.Tx, notice ChangeNotice) error {
	if unannouncedEntities[notice.Entity] {
		return nil
	}

	payload, err := json.Marshal(notice)
	if err != nil {
		return fmt.Errorf("%s: %w", ChangeChannel, err)
	}
	// Bounded by construction — a table name, a uuid, one of three verbs and an
	// integer come to about a hundred bytes — so this can only fire if a future
	// field carries something unbounded. Failing the write is the right answer
	// when it does: pg_notify would raise on the same condition anyway, and a
	// row that changes with nobody told is precisely the silent staleness this
	// whole feature exists to prevent.
	if len(payload) > maxNotifyPayload {
		return fmt.Errorf("%s: payload is %d bytes, over the %d-byte limit — send identifiers, not rows",
			ChangeChannel, len(payload), maxNotifyPayload)
	}

	// pg_notify rather than NOTIFY, because NOTIFY takes a literal and cannot
	// be given a bind parameter.
	if _, err := tx.Exec(ctx, `SELECT pg_notify($1, $2)`, ChangeChannel, string(payload)); err != nil {
		return fmt.Errorf("%s: %w", ChangeChannel, err)
	}
	return nil
}

// Listener is one connection listening for changes.
//
// It is deliberately not taken from the pool. LISTEN registers interest on the
// connection itself, so a pooled connection would carry that registration back
// into the pool and hand it to an unrelated query — and this connection spends
// its life blocked waiting, which is exactly what a pooled connection must
// never do. One of these per process is enough: it is the single reader that
// the in-memory fan-out sits behind.
//
// Not safe for concurrent use, and deliberately not made so: a pgx connection
// is one conversation, and a Close that overlaps a read is a data race on the
// connection itself. Next and Close belong to the same goroutine — which is
// also the natural shape, since the only reason to close one of these is that
// the read it was blocked in has just failed.
type Listener struct {
	conn *pgx.Conn
}

// Listen opens that connection and registers for changes.
//
// The connection settings are copied from the pool's, so there is one DSN in
// the process rather than two that can disagree about which database, user or
// TLS mode is meant.
func (s *Store) Listen(ctx context.Context) (*Listener, error) {
	conn, err := pgx.ConnectConfig(ctx, s.pool.Config().ConnConfig.Copy())
	if err != nil {
		return nil, fmt.Errorf("listen: connect: %w", err)
	}
	// The channel name is a constant in this package and can never be caller
	// input, which is what makes the interpolation safe; LISTEN takes an
	// identifier and cannot be parameterised.
	if _, err := conn.Exec(ctx, "LISTEN "+ChangeChannel); err != nil {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), listenCloseTimeout)
		defer cancel()
		_ = conn.Close(closeCtx)
		return nil, fmt.Errorf("listen: %w", err)
	}
	return &Listener{conn: conn}, nil
}

// PID is the backend process id of this connection.
//
// Exposed so an operator can see which session is listening — and so a test can
// terminate it and prove the caller reconnects rather than going quiet.
func (l *Listener) PID() uint32 { return l.conn.PgConn().PID() }

// Next blocks until a change is announced, ctx is done, or the connection
// fails. An unparseable payload comes back as ErrUnknownNotice, which the
// caller should log and ignore: the connection is fine, the message was not
// ours.
func (l *Listener) Next(ctx context.Context) (ChangeNotice, error) {
	n, err := l.conn.WaitForNotification(ctx)
	if err != nil {
		return ChangeNotice{}, err
	}
	var notice ChangeNotice
	if err := json.Unmarshal([]byte(n.Payload), &notice); err != nil {
		return ChangeNotice{}, fmt.Errorf("%w: %v", ErrUnknownNotice, err)
	}
	if notice.Entity == "" {
		return ChangeNotice{}, fmt.Errorf("%w: no entity", ErrUnknownNotice)
	}
	return notice, nil
}

// Close releases the connection.
func (l *Listener) Close(ctx context.Context) error { return l.conn.Close(ctx) }
