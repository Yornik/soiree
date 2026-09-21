package httpd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/Yornik/soiree/internal/config"
	"github.com/Yornik/soiree/internal/migrate"
	"github.com/Yornik/soiree/internal/pgtest"
	"github.com/Yornik/soiree/internal/store"
	"github.com/Yornik/soiree/web"
)

// Live sync is concurrent code talking to a real database, so most of what is
// worth checking here cannot be checked against a fake: whether a rolled-back
// write announces anything, and whether a terminated backend is reconnected to,
// are both properties of PostgreSQL as much as of this package.
//
// The exceptions are the drop policy, the two caps and which lookup error ends
// a stream. A client slow enough to fill its buffer cannot be simulated over a
// loopback socket — the kernel's send buffer absorbs everything an SSE frame
// weighs — and a database that has stopped answering is not something to
// arrange either, so those are exercised against the hub and a stub lookup
// directly, where "this subscriber is not reading" and "the database is not
// answering" are things a test can actually state.

const (
	// frameWait is how long a test waits for a frame that should already be on
	// its way. Only ever spent in full when the test is failing.
	frameWait = 10 * time.Second
	// settleWait bounds a poll for a condition that should hold almost at once.
	settleWait = 5 * time.Second
)

// newLiveServer builds a server with live sync on, backed by a database of its
// own.
//
// tweak runs after both are built and before either is started, which is the
// only point at which the hub's knobs and the listener's timeouts can be
// changed without racing whoever is reading them.
// The fourth return is the API handler with a session attached: the CRUD
// subtree is behind RequireWrite, so a test that writes needs one. The
// httptest.Server carries the same session for the streams themselves.
func newLiveServer(t *testing.T, tweak ...func(*Server, *httptest.Server)) (*Server, *httptest.Server, *pgxpool.Pool, http.Handler) {
	t.Helper()

	pool := pgtest.Pool(t)
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

	// /events sits inside the guarded API subtree, so a stream needs a session
	// like any other request. Without one these tests would assert against a
	// 401 body and never open a stream at all.
	a := NewAuth(AuthOptions{Store: st, BaseURL: "https://soiree.example.test/"})
	a.params = cheapParams
	a.background = func(fn func(context.Context)) { fn(context.Background()) }

	authed := authedAs(t, s.WithAuth(a).Handler(), st, a, store.RoleEditor)
	ts := httptest.NewUnstartedServer(authed)
	for _, fn := range tweak {
		fn(s, ts)
	}
	// Registered before the server starts, and so run after every stream's own
	// cleanup: Close waits for connections to go idle, and a stream that is
	// still open never is.
	t.Cleanup(func() {
		s.StopLiveSync()
		ts.Close()
	})
	ts.Start()
	return s, ts, pool, authed
}

// stream is one open SSE connection, with its frames pumped into a channel so a
// test can wait on them with a timeout rather than blocking on a socket.
type stream struct {
	frames <-chan string
	status int
	header http.Header
}

func openStream(t *testing.T, ts *httptest.Server) *stream {
	t.Helper()
	st := openStreamRaw(t, ts)
	if st.status != http.StatusOK {
		t.Fatalf("GET /api/v1/events -> %d, want 200", st.status)
	}
	if ct := st.header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	return st
}

func openStreamRaw(t *testing.T, ts *httptest.Server) *stream {
	t.Helper()

	// t.Context() is cancelled just before cleanups run, which is what ends the
	// request — and therefore the handler — before the server is closed.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/api/v1/events", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /api/v1/events: %v", err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })

	frames := make(chan string, 256)
	go func() {
		defer close(frames)
		r := bufio.NewReader(res.Body)
		for {
			frame, err := readFrame(r)
			if frame != "" {
				select {
				case frames <- frame:
				case <-req.Context().Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	return &stream{frames: frames, status: res.StatusCode, header: res.Header}
}

// readFrame reads one SSE frame: everything up to the blank line that ends it.
func readFrame(r *bufio.Reader) (string, error) {
	var b strings.Builder
	for {
		line, err := r.ReadString('\n')
		b.WriteString(line)
		if err != nil {
			return b.String(), err
		}
		if line == "\n" {
			return b.String(), nil
		}
	}
}

// await reads frames until one satisfies want, or the wait runs out.
func (st *stream) await(t *testing.T, what string, want func(string) bool) string {
	t.Helper()
	deadline := time.After(frameWait)
	for {
		select {
		case frame, ok := <-st.frames:
			if !ok {
				t.Fatalf("the stream ended before %s arrived", what)
			}
			if want(frame) {
				return frame
			}
		case <-deadline:
			t.Fatalf("no %s arrived within %s", what, frameWait)
		}
	}
}

func (st *stream) awaitChange(t *testing.T) store.ChangeNotice {
	t.Helper()
	frame := st.await(t, "change event", func(f string) bool {
		return strings.HasPrefix(f, "event: change\n")
	})
	_, data, _ := strings.Cut(frame, "data: ")
	var notice store.ChangeNotice
	if err := json.Unmarshal([]byte(strings.TrimSpace(data)), &notice); err != nil {
		t.Fatalf("change data is not a notice: %v\n%s", err, frame)
	}
	return notice
}

func (st *stream) awaitResync(t *testing.T) {
	t.Helper()
	st.await(t, "resync event", func(f string) bool { return strings.HasPrefix(f, "event: resync\n") })
}

// awaitEnd waits for the server to end the stream, which reaches a reader as a
// closed body and so as a closed channel here. Frames still arriving are read
// past: what is being waited for is the end, not silence.
func (st *stream) awaitEnd(t *testing.T, why string) {
	t.Helper()
	deadline := time.After(frameWait)
	for {
		select {
		case _, ok := <-st.frames:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatalf("the stream was still open %s, %s later", why, frameWait)
		}
	}
}

// awaitListening waits until the LISTEN connection is registered.
//
// Not decoration: the hub connects on the first subscriber and does it
// asynchronously, so a write issued in that window is announced to nobody. In
// production that is covered — the hub broadcasts a resync the moment it
// connects, so everyone refetches — but a test asserting a specific change
// event has to wait for the window to close or it is asserting a race.
func awaitListening(t *testing.T, s *Server) {
	t.Helper()
	eventually(t, "the listener to connect", func() bool { return s.live.backendPID() != 0 })
}

// eventually polls until cond holds, which is how a test waits on a goroutine
// it does not own without sleeping for a fixed guess.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(settleWait)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestEventsIsNotMountedWithoutADatabase: with no DSN the API does not exist,
// and live sync is part of the API. The image smoke test runs `docker run` with
// no Postgres, so this has to hold or the binary stops booting.
//
// Runs under -short, because it needs no database — which is the point.
func TestEventsIsNotMountedWithoutADatabase(t *testing.T) {
	s, err := New(config.Config{EventName: "Ada's Retirement", Currency: "EUR", Locale: "en-US"}, web.FS())
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	if s.live != nil {
		t.Error("a server with no store built a change hub")
	}
	// And StopLiveSync is safe on it, since main registers it unconditionally.
	s.StopLiveSync()

	res := call(t, s.Handler(), http.MethodGet, "/api/v1/events", "")
	if res.status != http.StatusNotFound {
		t.Errorf("GET /api/v1/events with no database -> %d, want 404", res.status)
	}
}

// TestTheMetricsWrapperStaysFlushable guards the one seam this feature depends
// on and nothing else notices.
//
// Every request passes through instrument's statusRecorder. If that stops
// forwarding Unwrap, http.ResponseController can no longer reach Flush or the
// connection's deadlines, and an event stream through it delivers nothing while
// every other test in this package goes on passing.
func TestTheMetricsWrapperStaysFlushable(t *testing.T) {
	rc := http.NewResponseController(&statusRecorder{ResponseWriter: httptest.NewRecorder()})
	if err := rc.Flush(); err != nil {
		t.Fatalf("the metrics wrapper cannot be flushed through: %v", err)
	}
}

// TestEventsAnnouncesAWrite is the feature: an edit made through the API turns
// up on somebody else's stream, naming the row to re-read.
func TestEventsAnnouncesAWrite(t *testing.T) {
	s, ts, _, h := newLiveServer(t)

	st := openStream(t, ts)
	if got := st.header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	st.awaitResync(t)
	awaitListening(t, s)

	item := created(t, h, "budget-items", `{"item":"Venue deposit","unit":"250.00"}`)
	id := str(t, item, "id")

	got := st.awaitChange(t)
	if got.Entity != store.EntityBudgetItems {
		t.Errorf("entity = %q, want %q", got.Entity, store.EntityBudgetItems)
	}
	if got.ID == nil || got.ID.String() != id {
		t.Errorf("id = %v, want %s", got.ID, id)
	}
	if got.Action != store.ChangeCreate {
		t.Errorf("action = %q, want create", got.Action)
	}
	if got.Revision == nil || *got.Revision != 1 {
		t.Errorf("revision = %v, want 1", got.Revision)
	}

	// A patch announces the revision the row now carries — the number a client
	// compares against its own copy to tell somebody else's edit from its own.
	call(t, h, http.MethodPatch, "/api/v1/budget-items/"+id, `{"revision":1,"unit":"260.00"}`)
	got = st.awaitChange(t)
	if got.Action != store.ChangeUpdate || got.Revision == nil || *got.Revision != 2 {
		t.Errorf("patch announced %+v, want action=update revision=2", got)
	}

	// And a delete, which is the change a client cannot discover by re-reading.
	call(t, h, http.MethodDelete, "/api/v1/budget-items/"+id+"?revision=2", "")
	got = st.awaitChange(t)
	if got.Action != store.ChangeDelete || got.ID == nil || got.ID.String() != id {
		t.Errorf("delete announced %+v, want action=delete for %s", got, id)
	}
}

// TestAStreamNamesTheBuildItIsAnsweredBy is the version handshake. A planning
// tab is left open for days, so nothing in the page ever learns that the
// deployment changed under it. The stream is the one thing that notices a
// deploy by itself, because it drops with the old process and is reopened
// against the new one, and the build it names on each open is what a page
// compares with the script it is actually running.
func TestAStreamNamesTheBuildItIsAnsweredBy(t *testing.T) {
	previous := Version
	Version = "1.2.3"
	t.Cleanup(func() { Version = previous })

	s, ts, _, _ := newLiveServer(t)

	st := openStream(t, ts)
	var earlier []string
	frame := st.await(t, "hello event", func(f string) bool {
		if strings.HasPrefix(f, "event: hello\n") {
			return true
		}
		earlier = append(earlier, f)
		return false
	})
	for _, f := range earlier {
		if strings.HasPrefix(f, "event: resync\n") {
			t.Error("the resync arrived first, so a page is told to refetch before it is told which build will answer it")
		}
	}

	_, data, _ := strings.Cut(frame, "data: ")
	var hello struct {
		Version string `json:"version"`
		Build   string `json:"build"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(data)), &hello); err != nil {
		t.Fatalf("hello data is not a JSON object: %v\n%s", err, frame)
	}
	if hello.Version != Version {
		t.Errorf("hello names version %q, want %q: the release this binary was linked with", hello.Version, Version)
	}
	// A page has nothing to compare a build id with except the script it
	// loaded, so this has to be the URL the shell asks for rather than merely
	// something that changes per build.
	if !strings.Contains(string(s.index.Raw), `src="`+hello.Build+`"`) {
		t.Errorf("hello names build %q, which is not the script the shell loads", hello.Build)
	}
}

// TestEventsReachesEverySubscriber: one LISTEN connection, many clients. If the
// fan-out only reached the first, a planning session of three would have two
// people quietly out of date.
func TestEventsReachesEverySubscriber(t *testing.T) {
	s, ts, _, h := newLiveServer(t)

	first, second := openStream(t, ts), openStream(t, ts)
	first.awaitResync(t)
	second.awaitResync(t)
	eventually(t, "both subscribers to register", func() bool { return s.live.Subscribers() == 2 })
	awaitListening(t, s)

	item := created(t, h, "budget-items", `{"item":"Venue deposit","unit":"250.00"}`)
	id := str(t, item, "id")

	for name, st := range map[string]*stream{"first": first, "second": second} {
		got := st.awaitChange(t)
		if got.ID == nil || got.ID.String() != id {
			t.Errorf("%s subscriber got %+v, want %s", name, got, id)
		}
	}

}

// TestEventsCleansUpAfterADisconnectingClient: a browser closing a tab must
// leave nothing behind, or a day of people reloading fills the subscriber table
// with streams nobody is reading.
func TestEventsCleansUpAfterADisconnectingClient(t *testing.T) {
	s, ts, _, _ := newLiveServer(t)

	ctx, cancel := context.WithCancel(t.Context())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/v1/events", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /api/v1/events: %v", err)
	}
	eventually(t, "the subscriber to register", func() bool { return s.live.Subscribers() == 1 })
	awaitListening(t, s)

	cancel()
	_ = res.Body.Close()

	eventually(t, "the subscriber to be released", func() bool { return s.live.Subscribers() == 0 })

	// The listener stays up — it is one connection per process, not one per
	// client, and tearing it down between two people's page loads would mean
	// reconnecting to the database every time somebody refreshes.
	if s.live.backendPID() == 0 {
		t.Error("the listening connection was torn down with the last client")
	}
}

// TestEventsCapsConcurrentSubscribers: each stream costs a goroutine and a
// socket, so there has to be a number. Past it the answer is a refusal a client
// can act on, not a connection the server cannot afford.
func TestEventsCapsConcurrentSubscribers(t *testing.T) {
	s, ts, _, _ := newLiveServer(t, func(s *Server, _ *httptest.Server) { s.live.maxClients = 1 })

	first := openStream(t, ts)
	first.awaitResync(t)
	eventually(t, "the first subscriber to register", func() bool { return s.live.Subscribers() == 1 })

	second := openStreamRaw(t, ts)
	if second.status != http.StatusServiceUnavailable {
		t.Fatalf("second stream -> %d, want 503", second.status)
	}
	if retry := second.header.Get("Retry-After"); retry == "" {
		t.Error("a refused stream should say when to try again")
	}
	// The one that got in is unaffected.
	if s.live.Subscribers() != 1 {
		t.Errorf("subscribers = %d, want 1", s.live.Subscribers())
	}
}

// TestAnAccountIsCappedWellBelowTheInstance: the cap above is a number one
// caller can reach on everybody else's behalf. Somebody's own share running
// out is about that caller rather than about this instance, so it is a 429 and
// it leaves the instance with room for everybody else.
func TestAnAccountIsCappedWellBelowTheInstance(t *testing.T) {
	s, ts, _, _ := newLiveServer(t, func(s *Server, _ *httptest.Server) { s.live.maxPerUser = 1 })

	first := openStream(t, ts)
	first.awaitResync(t)
	eventually(t, "the first stream to register", func() bool { return s.live.Subscribers() == 1 })

	second := openStreamRaw(t, ts)
	if second.status != http.StatusTooManyRequests {
		t.Fatalf("a second stream for the same account -> %d, want 429", second.status)
	}
	if retry := second.header.Get("Retry-After"); retry == "" {
		t.Error("a refused stream should say when to try again")
	}
	// The instance is nowhere near full, and the stream that got in is
	// unaffected.
	if s.live.Subscribers() != 1 {
		t.Errorf("subscribers = %d, want 1", s.live.Subscribers())
	}
}

// TestAStreamEndsWhenItsSessionIsRevoked.
//
// A stream is authorised once, when it is opened, and then lives for hours.
// Without a re-read, signing out, an admin disabling the account and both
// session expiries reach every other route on the next request and reach an
// open stream only when its socket happens to drop — leaving a feed of row ids
// and revisions going to somebody who is no longer entitled to one, for as
// long as they care to hold the connection. All four are the same row
// disappearing from under the cookie, which is what this test does directly.
func TestAStreamEndsWhenItsSessionIsRevoked(t *testing.T) {
	s, ts, pool, _ := newLiveServer(t, func(s *Server, _ *httptest.Server) {
		// The re-read rides the heartbeat, so a fast beat is a fast re-read.
		s.live.heartbeat = 20 * time.Millisecond
	})

	st := openStream(t, ts)
	st.awaitResync(t)

	db := store.New(pool)
	user, err := db.UserByEmail(t.Context(), string(store.RoleEditor)+"@example.test")
	if err != nil {
		t.Fatalf("read the account the stream belongs to: %v", err)
	}
	// What disabling an account, deleting one and a password reset all reduce
	// to, and what a sign-out does to one row rather than all of them.
	if _, err := db.DeleteSessionsForUser(t.Context(), user.ID); err != nil {
		t.Fatalf("revoke the sessions: %v", err)
	}

	st.awaitEnd(t, "after its session was revoked")
	// And the slot it was holding is somebody else's again, which is the other
	// half of an admin's only remedy against an account that is misbehaving.
	eventually(t, "the slot to be released", func() bool { return s.live.Subscribers() == 0 })
}

// TestEventsRecoversFromADroppedListenConnection.
//
// A database failover, a connection reaper, a restarted pod on the other side:
// the connection goes away and the process must not go quiet. It must also say
// so — changes happened while it was down and nobody was told, so every client
// is asked to refetch once it is back.
func TestEventsRecoversFromADroppedListenConnection(t *testing.T) {
	s, ts, pool, h := newLiveServer(t)

	st := openStream(t, ts)
	st.awaitResync(t)

	awaitListening(t, s)
	was := s.live.backendPID()

	if _, err := pool.Exec(t.Context(), `SELECT pg_terminate_backend($1)`, was); err != nil {
		t.Fatalf("terminate the listening backend: %v", err)
	}

	// The reconnect announces itself, because the gap it leaves is exactly what
	// a client cannot discover on its own.
	st.awaitResync(t)
	eventually(t, "a new listening backend", func() bool {
		pid := s.live.backendPID()
		return pid != 0 && pid != was
	})

	// And changes flow again.
	item := created(t, h, "budget-items", `{"item":"Venue deposit","unit":"250.00"}`)
	id := str(t, item, "id")
	got := st.awaitChange(t)
	if got.ID == nil || got.ID.String() != id {
		t.Errorf("after reconnecting, got %+v, want %s", got, id)
	}
}

// TestEventsOutlivesTheServersRequestTimeouts.
//
// The server arms ReadTimeout and WriteTimeout on every request, and a stream
// is meant to outlive both by hours. WriteTimeout is the one that bites: it is
// a deadline on the whole response, so without a deadline rolled forward per
// frame the stream dies exactly once, a minute in, in production — and looks
// perfectly healthy in any test that reads a response to completion.
//
// Checked by removing the rolling deadline, at which point this fails after
// six heartbeats.
func TestEventsOutlivesTheServersRequestTimeouts(t *testing.T) {
	_, ts, _, _ := newLiveServer(t, func(s *Server, ts *httptest.Server) {
		// Short enough to observe, against deadlines short enough to have
		// already fired several times over by the end of the test.
		s.live.heartbeat = 50 * time.Millisecond
		ts.Config.ReadTimeout = 300 * time.Millisecond
		ts.Config.WriteTimeout = 300 * time.Millisecond
	})

	st := openStream(t, ts)
	st.awaitResync(t)

	// Four times the server's deadline. A stream that had not cleared it would
	// be gone long before the last of these.
	deadline := time.Now().Add(1200 * time.Millisecond)
	beats := 0
	for time.Now().Before(deadline) {
		select {
		case frame, ok := <-st.frames:
			if !ok {
				t.Fatalf("the stream was closed after %d heartbeats, well inside the server's timeouts", beats)
			}
			if strings.HasPrefix(frame, ":") {
				beats++
			}
		case <-time.After(frameWait):
			t.Fatal("the stream went silent")
		}
	}
	if beats < 4 {
		t.Errorf("saw %d heartbeats in 1.2s at a 50ms interval, want at least 4", beats)
	}
}

// --- The fan-out on its own, with no database behind it. ---

// stubListener stands in for the LISTEN connection. A socket cannot be made
// slow enough to fill a subscriber's buffer, so the drop policy is stated
// directly instead.
type stubListener struct {
	notices chan store.ChangeNotice
	pid     uint32
}

func (l *stubListener) Next(ctx context.Context) (store.ChangeNotice, error) {
	select {
	case n := <-l.notices:
		return n, nil
	case <-ctx.Done():
		return store.ChangeNotice{}, ctx.Err()
	}
}

func (l *stubListener) PID() uint32                 { return l.pid }
func (l *stubListener) Close(context.Context) error { return nil }

// discardLogger keeps the expected warnings of a drop test out of the test
// output, where they would read as failures.
func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func newStubHub(t *testing.T, buffer, maxClients int) *changeHub {
	t.Helper()
	h := &changeHub{
		open: func(context.Context) (changeListener, error) {
			return &stubListener{notices: make(chan store.ChangeNotice), pid: 1}, nil
		},
		log:        discardLogger(),
		gauge:      prometheus.NewGauge(prometheus.GaugeOpts{Name: "test_subscribers"}),
		buffer:     buffer,
		maxClients: maxClients,
		// The per-account cap is the global one unless a test lowers it, so
		// the tests about the global cap keep meeting the global cap.
		maxPerUser: maxClients,
		heartbeat:  time.Hour,
		subs:       map[*subscriber]struct{}{},
		stopped:    make(chan struct{}),
	}
	t.Cleanup(h.Close)
	return h
}

// TestASlowSubscriberIsDroppedWithoutStallingTheOthers.
//
// The buffer bounds how far behind one client may fall; past that it is dropped
// rather than waited for. Queueing instead would move a stuck laptop's problem
// into this process's memory, and blocking would hand it to everybody else in
// the session.
func TestASlowSubscriberIsDroppedWithoutStallingTheOthers(t *testing.T) {
	h := newStubHub(t, 1, 10)

	slow, err := h.subscribe(uuid.New())
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	quick, err := h.subscribe(uuid.New())
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	// The hub broadcasts a resync as soon as the stub connects, which fills a
	// buffer of one. Drain the reader's; leave the slow one's alone.
	<-quick.frames

	for range 5 {
		h.broadcast([]byte("event: change\ndata: {}\n\n"))
		// The reader keeps up, one frame at a time.
		select {
		case <-quick.frames:
		case <-time.After(frameWait):
			t.Fatal("the fan-out stalled on the subscriber that was not reading")
		}
	}

	select {
	case <-slow.dropped:
	case <-time.After(frameWait):
		t.Fatal("a subscriber that never read was not dropped")
	}
	select {
	case <-quick.dropped:
		t.Error("the subscriber that kept up was dropped too")
	default:
	}
	if n := h.Subscribers(); n != 1 {
		t.Errorf("subscribers = %d, want 1", n)
	}
}

// TestTheSubscriberCapIsEnforced states the same rule the HTTP test asserts
// through a socket, without needing 256 of them.
func TestTheSubscriberCapIsEnforced(t *testing.T) {
	h := newStubHub(t, 4, 2)

	for i := range 2 {
		if _, err := h.subscribe(uuid.New()); err != nil {
			t.Fatalf("subscriber %d: %v", i, err)
		}
	}
	if _, err := h.subscribe(uuid.New()); err == nil {
		t.Fatal("the cap let a third subscriber in")
	}

	// A departure frees a slot, so the cap is a ceiling rather than a lifetime
	// budget.
	h.mu.Lock()
	var one *subscriber
	for sub := range h.subs {
		one = sub
		break
	}
	h.mu.Unlock()
	h.release(one)

	if _, err := h.subscribe(uuid.New()); err != nil {
		t.Fatalf("after a departure: %v", err)
	}
}

// TestOneAccountCannotFillTheInstance states the per-account half of the cap
// without opening seventeen sockets: a caller that has used up its own share
// is refused while there is still room for everybody else.
func TestOneAccountCannotFillTheInstance(t *testing.T) {
	h := newStubHub(t, 4, 10)
	h.maxPerUser = 2

	greedy, other := uuid.New(), uuid.New()
	for i := range 2 {
		if _, err := h.subscribe(greedy); err != nil {
			t.Fatalf("stream %d: %v", i, err)
		}
	}
	if _, err := h.subscribe(greedy); !errors.Is(err, errTooManyStreamsForUser) {
		t.Fatalf("a third stream for one account -> %v, want %v", err, errTooManyStreamsForUser)
	}
	if _, err := h.subscribe(other); err != nil {
		t.Fatalf("somebody else, with eight slots free: %v", err)
	}
}

// fakeSessions answers the one lookup a stream makes with whatever the case
// under test needs it to answer.
type fakeSessions struct{ err error }

func (f fakeSessions) SessionByToken(context.Context, []byte, time.Duration) (store.Session, store.User, error) {
	return store.Session{}, store.User{}, f.err
}

// TestOnlyAGoneSessionEndsAStream is the distinction the accounts middleware
// draws for the same lookup, stated here because this is the other place that
// makes it. ErrNotFound is the session being gone; anything else is the
// database not answering, and ending a stream on that would turn a failover of
// a few seconds into a party without live sync.
//
// Runs under -short, because it needs no database — which is the point.
func TestOnlyAGoneSessionEndsAStream(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"a session that still resolves", nil, false},
		{"a session that is gone", store.ErrNotFound, true},
		{"a database that is not answering", errors.New("dial tcp: connection refused"), false},
		{"a lookup that ran out of time", context.DeadlineExceeded, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := sessionEnded(t.Context(), fakeSessions{err: tc.err}, []byte("a token hash"), discardLogger())
			if got != tc.want {
				t.Errorf("sessionEnded = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestClosingTheHubEndsEveryStream: Shutdown waits for connections to go idle
// and a stream never does, so this is what stops one connected browser turning
// every SIGTERM into a hung shutdown.
func TestClosingTheHubEndsEveryStream(t *testing.T) {
	h := newStubHub(t, 4, 10)

	first, err := h.subscribe(uuid.New())
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	second, err := h.subscribe(uuid.New())
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	h.Close()

	for name, sub := range map[string]*subscriber{"first": first, "second": second} {
		select {
		case <-sub.dropped:
		default:
			t.Errorf("%s stream was not ended by Close", name)
		}
	}
	if _, err := h.subscribe(uuid.New()); err == nil {
		t.Error("a closed hub accepted a new subscriber")
	}
	// Idempotent: main registers it on shutdown and a test may call it too.
	h.Close()
}
