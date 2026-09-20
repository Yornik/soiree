package httpd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/Yornik/soiree/internal/auth"
	"github.com/Yornik/soiree/internal/store"
)

// Live sync: GET /api/v1/events.
//
// The API alone gets two people a correct view of the plan each, right up until
// one of them changes something — after which the other is silently looking at
// stale money. This is the part that closes that window.
//
// The shape is one LISTEN connection per process fanning out to the connected
// clients in memory, not one database connection per client. A twenty-person
// planning session is twenty sockets and one database connection, and it is
// cheap enough that it costs nothing to leave open for the whole evening.
//
// Three properties everything below is arranged around:
//
//   - **Bounded.** A subscriber that cannot keep up is dropped, not queued for.
//     The browser reconnects and refetches, which is cheaper for everyone than
//     one stuck laptop growing a queue inside the server.
//   - **Degrading.** With no DATABASE_URL the route is never mounted, exactly
//     like the rest of /api/v1. With a database that goes away, the listener
//     retries with backoff and tells every client to refetch once it is back;
//     it never wedges the process and never takes the site down with it.
//   - **Honest.** No event ids and no replay. A client that was disconnected
//     missed changes and is told to refetch on connect, rather than being given
//     a cursor that implies a gap can be filled.

const (
	// sseHeartbeat is how often a comment is written into an idle stream.
	//
	// Nothing in this application needs it; the proxies between here and the
	// browser do. Traefik sits in front of this deployment and an idle stream
	// looks exactly like a dead one to anything counting seconds since the last
	// byte. Twenty seconds is comfortably under the usual sixty-second idle
	// timeouts and costs eight bytes.
	sseHeartbeat = 20 * time.Second

	// sseRetry is the reconnect delay browsers are asked to use. The default is
	// three seconds in most of them anyway; sending it makes the value ours
	// rather than the browser's.
	sseRetry = 3 * time.Second

	// sseWriteTimeout bounds one frame's write.
	//
	// Two jobs. It replaces the server's WriteTimeout, which is armed for the
	// whole request and would otherwise kill every stream a minute in. And it
	// is the half of the backpressure story that actually evicts anybody: the
	// buffered channel bounds memory, but a client whose receive window is full
	// blocks inside Write — it never drains its channel, so the hub stops
	// sending to it, and without a deadline the goroutine and the socket would
	// sit there indefinitely regardless.
	sseWriteTimeout = 10 * time.Second

	// sseBuffer is how many frames one subscriber may fall behind by before it
	// is dropped. Deep enough to absorb a burst — a delete cascading through a
	// quote's whole breakdown is one frame per row — and shallow enough that a
	// client which has genuinely stopped reading is noticed in seconds.
	sseBuffer = 64

	// sseMaxSubscribers caps concurrent streams. Each costs a goroutine and a
	// socket, so this is not about memory so much as about there being a number
	// at all: without one, anything that can open connections can open all of
	// them. Far above any real planning session.
	sseMaxSubscribers = 256

	// sseMaxPerUser caps what one account may hold of that. The cap above is
	// about this process; this one is about no single caller being able to
	// take the whole of it and leave everybody else without live sync. Sixteen
	// because several tabs, a phone and a laptop are an ordinary evening and
	// still nowhere near it.
	sseMaxPerUser = 16

	// sseSessionChecks is how many heartbeats pass between two reads of the
	// caller's session.
	//
	// A stream is authorised once, when it is opened, and is then meant to
	// stay open for the evening. Everything that ends a session — a sign-out,
	// an admin disabling the account, the idle window, the absolute lifetime —
	// reaches every other route on the next request and would otherwise reach
	// an open stream only when its socket happened to drop. Fifteen beats is
	// about five minutes, which is one indexed SELECT per client per five
	// minutes.
	sseSessionChecks = 15

	// sseSessionCheckTimeout bounds that read. The request context has no
	// deadline — that is what a stream is — so a database that has stopped
	// answering would block this goroutine, and a goroutine blocked here is a
	// client that has stopped draining its buffer and is dropped as a slow
	// reader. A database fault must not disconnect anybody.
	sseSessionCheckTimeout = 5 * time.Second

	// Reconnect backoff for the LISTEN connection. It starts short because a
	// failover takes seconds and the cost of retrying is one connection
	// attempt, and it stops at half a minute because past that the database is
	// not coming back on this attempt either.
	listenBackoffMin = 200 * time.Millisecond
	listenBackoffMax = 30 * time.Second

	// listenCloseTimeout bounds the polite close of a spent connection.
	listenCloseTimeout = 2 * time.Second
)

// The refusals, each its own error rather than a bare bool so the handler can
// tell them apart — they do not mean the same thing to a caller and are not
// answered with the same status.
var (
	errTooManySubscribers    = errors.New("too many live-sync subscribers")
	errTooManyStreamsForUser = errors.New("too many live-sync streams for this account")
	errHubClosed             = errors.New("live sync is shutting down")
)

// Frames, pre-encoded because they never vary.
//
// `resync` carries `data: {}` rather than no data at all: EventSource discards
// an event whose data buffer is empty, so an event with only a name is one the
// browser silently never delivers.
var (
	retryFrame     = fmt.Appendf(nil, "retry: %d\n\n", sseRetry.Milliseconds())
	resyncFrame    = []byte("event: resync\ndata: {}\n\n")
	heartbeatFrame = []byte(": ping\n\n")
)

// Unwrap exposes the ResponseWriter that instrument() wrapped.
//
// http.ResponseController reaches Flush, SetWriteDeadline and SetReadDeadline
// by walking Unwrap() down to a writer that implements them. statusRecorder
// embeds a ResponseWriter but does not forward those, so without this every one
// of the three returns ErrNotSupported and the stream neither flushes nor
// escapes the server's ReadTimeout — it would look fine in a test that reads
// the whole body and be dead in a browser.
//
// It lives here rather than in metrics.go because metrics.go belongs to another
// change in flight; a method may be declared in any file of its package.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// changeListener is the part of store.Listener this package uses. An interface
// so the hub can be exercised without a database, and so nothing about pgx
// leaks into the HTTP layer.
type changeListener interface {
	Next(ctx context.Context) (store.ChangeNotice, error)
	PID() uint32
	Close(ctx context.Context) error
}

// subscriber is one connected stream.
//
// dropped is closed by the hub and never by the handler, so "the hub has given
// up on you" is a signal the handler selects on alongside the client going
// away — which is what lets the fan-out abandon a slow reader without ever
// blocking on it.
//
// user is the account that opened it, which is the whole of what the hub knows
// about who is listening — enough to count what one caller holds, and nothing
// that would be worth reading out of this map.
type subscriber struct {
	user    uuid.UUID
	frames  chan []byte
	dropped chan struct{}
}

// changeHub is the process's single reader of database changes and the
// in-memory fan-out in front of it.
type changeHub struct {
	open  func(context.Context) (changeListener, error)
	log   *slog.Logger
	gauge prometheus.Gauge

	// Sized at construction rather than read from the constants, so a test can
	// build a hub with a buffer of one and prove the drop policy in
	// milliseconds instead of by writing sixty-five rows — and can watch a
	// stream survive a deadline without waiting twenty seconds for a heartbeat.
	buffer     int
	maxClients int
	maxPerUser int
	heartbeat  time.Duration

	mu      sync.Mutex
	subs    map[*subscriber]struct{}
	started bool
	closed  bool
	cancel  context.CancelFunc
	stopped chan struct{}
	pid     uint32
}

// newChangeHub builds the fan-out for a store. It opens nothing: the LISTEN
// connection is established on the first subscriber and kept afterwards, so a
// deployment nobody is watching — and every test that never calls /events —
// holds no connection at all.
func newChangeHub(st *store.Store, m *Metrics, log *slog.Logger) *changeHub {
	if log == nil {
		log = slog.Default()
	}
	return &changeHub{
		open: func(ctx context.Context) (changeListener, error) {
			return st.Listen(ctx)
		},
		log:        log,
		gauge:      subscriberGauge(m),
		buffer:     sseBuffer,
		maxClients: sseMaxSubscribers,
		maxPerUser: sseMaxPerUser,
		heartbeat:  sseHeartbeat,
		subs:       map[*subscriber]struct{}{},
		stopped:    make(chan struct{}),
	}
}

// subscriberGauge registers the one series this feature adds.
//
// Registered from here rather than declared in metrics.go so that file's only
// change is the route label. Each Server owns its own registry, so this cannot
// collide with another.
func subscriberGauge(m *Metrics) prometheus.Gauge {
	g := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "soiree_sse_subscribers",
		Help: "Live-sync clients currently connected to /api/v1/events.",
	})
	if m != nil {
		m.registry.MustRegister(g)
	}
	return g
}

// subscribe registers a stream for one account, starting the listener if this
// is the first one.
func (h *changeHub) subscribe(user uuid.UUID) (*subscriber, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		return nil, errHubClosed
	}
	// The whole instance first, because a full instance is full whoever is
	// asking. Then one account's share of it: without that, a single caller
	// holds every slot and everybody else's live sync is off until the process
	// restarts — which is also the state an admin cannot clear by disabling
	// the account, since disabling frees nothing the account is holding.
	if len(h.subs) >= h.maxClients {
		return nil, errTooManySubscribers
	}
	if h.streamsForLocked(user) >= h.maxPerUser {
		return nil, errTooManyStreamsForUser
	}
	if !h.started {
		h.started = true
		ctx, cancel := context.WithCancel(context.Background())
		h.cancel = cancel
		go func() {
			defer close(h.stopped)
			h.listen(ctx)
		}()
	}

	sub := &subscriber{
		user:    user,
		frames:  make(chan []byte, h.buffer),
		dropped: make(chan struct{}),
	}
	h.subs[sub] = struct{}{}
	h.gauge.Set(float64(len(h.subs)))
	return sub, nil
}

// streamsForLocked counts what one account is already holding. A scan rather
// than a second map kept alongside: the map it would have to agree with is
// emptied from three different paths, and at most maxClients entries are ever
// walked.
func (h *changeHub) streamsForLocked(user uuid.UUID) int {
	n := 0
	for sub := range h.subs {
		if sub.user == user {
			n++
		}
	}
	return n
}

// release deregisters a stream whose handler is returning.
func (h *changeHub) release(sub *subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.dropLocked(sub)
}

// dropLocked removes a subscriber and tells it so. Membership in the map is
// what makes this idempotent: the handler releasing a subscriber the hub has
// already dropped finds nothing to do, so `dropped` is closed exactly once.
func (h *changeHub) dropLocked(sub *subscriber) {
	if _, ok := h.subs[sub]; !ok {
		return
	}
	delete(h.subs, sub)
	h.gauge.Set(float64(len(h.subs)))
	close(sub.dropped)
}

// broadcast hands a frame to every subscriber, and drops any that is not
// keeping up.
//
// The send is non-blocking on purpose. One stalled client must not hold up the
// fan-out to the other nineteen, and growing its queue instead would move the
// same failure into memory and let it take the process with it. Deleting from
// the map while ranging over it is defined behaviour in Go.
func (h *changeHub) broadcast(frame []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for sub := range h.subs {
		select {
		case sub.frames <- frame:
		default:
			h.log.Warn("live sync dropped a subscriber that fell behind", "buffered", len(sub.frames))
			h.dropLocked(sub)
		}
	}
}

// Subscribers reports how many streams are connected. For tests and for the
// gauge's own sanity.
func (h *changeHub) Subscribers() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// backendPID is the database session currently listening, or zero if none is.
// It changes when the connection is re-established, which is exactly what makes
// it worth reading under the lock.
func (h *changeHub) backendPID() uint32 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pid
}

// Close stops the listener and ends every stream.
//
// It waits for the listener goroutine, so that by the time this returns the
// database connection is closed rather than merely condemned — which is what a
// test dropping its database next needs, and what keeps a shutdown from leaving
// a session behind.
func (h *changeHub) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	started := h.started
	if h.cancel != nil {
		h.cancel()
	}
	for sub := range h.subs {
		h.dropLocked(sub)
	}
	h.mu.Unlock()

	if started {
		<-h.stopped
	}
}

// listen keeps a connection to the database's change channel, for as long as
// ctx lives.
//
// The loop is the requirement that the process must not be wedged by a database
// that went away: every failure is a log line and a wait, never a return, and
// the site keeps serving throughout. Backoff grows only while connections keep
// failing and resets as soon as one works, so a failover costs a fraction of a
// second and a long outage costs one attempt every half minute.
func (h *changeHub) listen(ctx context.Context) {
	backoff := listenBackoffMin
	for ctx.Err() == nil {
		connected, err := h.session(ctx)
		if ctx.Err() != nil {
			return
		}
		if connected {
			backoff = listenBackoffMin
			h.log.Warn("live sync lost its connection to the database", "err", err, "retryIn", backoff)
		} else {
			h.log.Warn("live sync could not reach the database", "err", err, "retryIn", backoff)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(withJitter(backoff)):
		}
		backoff = min(backoff*2, listenBackoffMax)
	}
}

// session holds one connection until it fails. The bool reports whether it ever
// got as far as listening, which is what tells "the database refused us" from
// "the database went away mid-stream" — and is what resets the backoff.
func (h *changeHub) session(ctx context.Context) (bool, error) {
	l, err := h.open(ctx)
	if err != nil {
		return false, err
	}
	defer func() {
		// Detached from ctx, which is usually already cancelled by the time
		// this runs: a close on a dead context skips the goodbye entirely.
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), listenCloseTimeout)
		defer cancel()
		if err := l.Close(closeCtx); err != nil {
			h.log.Debug("live sync listener did not close cleanly", "err", err)
		}
	}()

	h.established(l.PID())

	for {
		notice, err := l.Next(ctx)
		if err != nil {
			if errors.Is(err, store.ErrUnknownNotice) {
				// Somebody typed NOTIFY at a psql prompt. Not our message, not
				// a broken connection, and not a reason to reconnect.
				h.log.Warn("ignoring an unrecognised notification on the change channel", "err", err)
				continue
			}
			return true, err
		}
		if frame, ok := changeFrame(notice); ok {
			h.broadcast(frame)
		} else {
			h.log.Error("could not encode a change notice", "entity", notice.Entity)
		}
	}
}

// established records the new backend and tells everyone already connected to
// refetch.
//
// The resync is the part that is easy to leave out and expensive to leave out.
// While the connection was down, changes happened that nobody was told about;
// without this every client that stayed connected through a failover keeps a
// stale plan indefinitely, which is the precise failure this feature exists to
// prevent.
func (h *changeHub) established(pid uint32) {
	h.mu.Lock()
	h.pid = pid
	h.mu.Unlock()

	h.log.Info("live sync is listening for database changes", "backendPID", pid)
	h.broadcast(resyncFrame)
}

// changeFrame renders one notice as an SSE event.
//
// A named event rather than the default `message`, so a client registers for
// `change` and `resync` separately and an unknown future event name is ignored
// by an old client instead of being mistaken for a change.
func changeFrame(n store.ChangeNotice) ([]byte, bool) {
	body, err := json.Marshal(n)
	if err != nil {
		return nil, false
	}
	return fmt.Appendf(nil, "event: change\ndata: %s\n\n", body), true
}

// withJitter spreads retries so that several replicas losing the database at
// the same moment do not all reconnect on the same tick.
func withJitter(d time.Duration) time.Duration {
	return d/2 + time.Duration(rand.Int64N(int64(d)))
}

// serveEvents streams changes to one client.
//
// Mounted only when a store is configured, like everything else under /api/v1,
// so a binary started with no DATABASE_URL does not have this path at all.
func (s *Server) serveEvents(w http.ResponseWriter, r *http.Request) {
	// The guard in front of this handler decides once that the caller may
	// read, and this response then outlives that decision by hours. Two things
	// below need to know whose stream it is: the per-account cap, and the
	// re-read that ends a stream whose session has gone. A caller with no
	// session cookie cannot get past that guard, so the second half of this is
	// the reading that fails closed rather than a case anybody meets.
	user, ok := UserFrom(r.Context())
	token, signedIn := sessionToken(r)
	if !ok || !signedIn {
		refuseUnidentified(w, r)
		return
	}
	tokenHash := auth.HashToken(token)

	sub, err := s.live.subscribe(user.ID)
	if err != nil {
		// Two refusals that do not mean the same thing. The instance being
		// full is about this instance's capacity rather than this caller's
		// behaviour, so it stays a 503; one account asking for more than its
		// share *is* the caller's behaviour, so that one is a 429. Retry-After
		// on both, because a moment later is worth trying either way — an
		// EventSource cannot read a status, so it is the page's own reopen
		// logic that acts on this.
		status, code := http.StatusServiceUnavailable, "unavailable"
		if errors.Is(err, errTooManyStreamsForUser) {
			status, code = http.StatusTooManyRequests, "too_many_streams"
		}
		w.Header().Set("Retry-After", "5")
		writeError(w, status, code, err.Error())
		return
	}
	defer s.live.release(sub)

	rc := http.NewResponseController(w)
	// The server arms ReadTimeout on every request and this one is meant to
	// outlive it by hours. net/http usually handles that for us: for a request
	// with no body it starts its background read immediately and clears the
	// deadline itself. For a request that *does* carry one — a curl with a
	// stray body rather than an EventSource — that background read is deferred
	// until the body hits EOF, which never happens here, and the whole-request
	// deadline stays armed and aborts the stream. This is the case net/http
	// does not cover, and it costs nothing to close.
	ignoreUnsupported(rc.SetReadDeadline(time.Time{}))

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	// Also set by the noStore middleware over the whole subtree; stated here
	// too because for this handler it is part of the contract rather than a
	// default, and setting the same value twice is a no-op.
	h.Set("Cache-Control", "no-store")
	// For proxies that buffer responses by default. Traefik does not, but this
	// image is meant to run behind whatever somebody puts in front of it, and a
	// buffered event stream is an event stream that never arrives.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	if err := rc.Flush(); err != nil {
		// Unreachable unless a middleware between here and the socket stops
		// forwarding Unwrap. Logged loudly because the symptom otherwise is a
		// stream that works in every test and delivers nothing in a browser.
		s.live.log.Error("live sync cannot stream: the response writer will not flush", "err", err)
		return
	}

	// Two frames before anything else: the reconnect interval, and a standing
	// instruction to refetch. Every connection starts by assuming the client's
	// copy of the plan is stale, because it usually is — and because that one
	// rule is the whole of what a client has to do about missed events.
	if !writeFrame(rc, w, retryFrame) || !writeFrame(rc, w, resyncFrame) {
		return
	}

	beat := time.NewTicker(s.live.heartbeat)
	defer beat.Stop()

	// Beats since the session was last looked at. The re-read rides the
	// heartbeat this stream already has rather than a timer of its own, so
	// there is one thing to slow down in a test and one thing to reason about
	// here.
	beats := 0

	ctx := r.Context()
	for {
		// The hub giving up on this subscriber wins over frames still sitting
		// in its buffer: those are exactly the frames it was too slow to take,
		// and draining them would delay the teardown that being dropped means.
		select {
		case <-ctx.Done():
			return
		case <-sub.dropped:
			return
		default:
		}

		select {
		case <-ctx.Done():
			return
		case <-sub.dropped:
			return
		case frame := <-sub.frames:
			if !writeFrame(rc, w, frame) {
				return
			}
		case <-beat.C:
			if !writeFrame(rc, w, heartbeatFrame) {
				return
			}
			// After the frame, never before it: the beat is what holds the
			// connection open through a proxy, and a database taking its time
			// must not delay it.
			beats++
			if beats >= sseSessionChecks {
				beats = 0
				if sessionEnded(ctx, s.store, tokenHash, s.live.log) {
					s.live.log.Info("live sync ended a stream whose session is gone", "user", user.ID)
					return
				}
			}
		}
	}
}

// sessionReader is the part of store.Store a stream re-reads itself against.
// An interface for the same reason changeListener is one: what this has to get
// right is which answer ends a stream, and that is worth stating without a
// database in the room.
type sessionReader interface {
	SessionByToken(ctx context.Context, tokenHash []byte, maxLifetime time.Duration) (store.Session, store.User, error)
}

// sessionEnded reports whether the session a stream was opened with is gone.
//
// Only ErrNotFound says so. SessionByToken collapses revoked, past its idle
// window, past its absolute lifetime and "the account is no longer active"
// into that one answer, which is exactly the set of reasons every other route
// refuses the same cookie — so a stream ending on it is this handler agreeing
// with the rest of the application rather than deciding anything of its own.
//
// Any other error is the database not answering, which says nothing about the
// session. The stream stays open: a failover of a few seconds must not
// disconnect a party, and that is the same distinction the accounts middleware
// draws for the same lookup.
func sessionEnded(ctx context.Context, sessions sessionReader, tokenHash []byte, log *slog.Logger) bool {
	ctx, cancel := context.WithTimeout(ctx, sseSessionCheckTimeout)
	defer cancel()

	_, _, err := sessions.SessionByToken(ctx, tokenHash, sessionMaxLifetime)
	switch {
	case err == nil:
		return false
	case errors.Is(err, store.ErrNotFound):
		return true
	default:
		log.Error("live sync could not re-read a session", "err", err)
		return false
	}
}

// writeFrame writes one frame and pushes it out, reporting whether the stream
// is still worth writing to.
//
// The deadline is set per frame rather than cleared once, and rolling it is
// what lets the stream outlive the server's WriteTimeout without going
// unbounded. Cleared instead, a client that has stopped reading would block
// this goroutine inside Write forever; rolling, it gets ten seconds to accept
// eight bytes and is otherwise disconnected, which is the thing that actually
// reclaims a stuck connection.
func writeFrame(rc *http.ResponseController, w http.ResponseWriter, frame []byte) bool {
	ignoreUnsupported(rc.SetWriteDeadline(time.Now().Add(sseWriteTimeout)))
	if _, err := w.Write(frame); err != nil {
		return false
	}
	return rc.Flush() == nil
}

// ignoreUnsupported swallows the one error a deadline call is allowed to
// return: an httptest.ResponseRecorder has no connection to set a deadline on,
// and refusing to stream to it would make the tests untestable rather than the
// handler safer.
func ignoreUnsupported(err error) {
	if err != nil && !errors.Is(err, http.ErrNotSupported) {
		slog.Debug("could not set a stream deadline", "err", err)
	}
}

// StopLiveSync ends every stream and closes the listening connection.
//
// Registered with http.Server.RegisterOnShutdown, because Shutdown waits for
// connections to go idle and a stream blocked on its request context never
// does: without this, one connected browser turns every SIGTERM into a hung
// shutdown. A no-op on a server with no database, which has no streams.
func (s *Server) StopLiveSync() {
	if s.live != nil {
		s.live.Close()
	}
}
