package reminders

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Yornik/soiree/internal/mailer"
	"github.com/Yornik/soiree/internal/push"
	"github.com/Yornik/soiree/internal/store"
)

// Nothing here reaches a real push service. The endpoints below are paths on a
// local httptest server, which is also what makes "a 410 prunes the row" a
// thing a test can state at all: a real service will not produce one on demand.

// fakePushService is a push service that answers with whatever status each
// endpoint has been assigned, and counts what it was asked for.
type fakePushService struct {
	mu     sync.Mutex
	status map[string]int
	hits   map[string]int
	url    string
}

func newFakePushService(t *testing.T) *fakePushService {
	t.Helper()
	f := &fakePushService{status: map[string]int{}, hits: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		device := strings.TrimPrefix(r.URL.Path, "/")
		f.mu.Lock()
		f.hits[device]++
		code, ok := f.status[device]
		f.mu.Unlock()
		if !ok {
			code = http.StatusCreated
		}
		w.WriteHeader(code)
	}))
	t.Cleanup(srv.Close)
	f.url = srv.URL
	return f
}

// answer assigns the status one device's endpoint will produce.
func (f *fakePushService) answer(device string, status int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status[device] = status
	return f.url + "/" + device
}

func (f *fakePushService) hitCount(device string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[device]
}

// vapid is a key pair generated per test run. Never a committed one: a key pair
// in a repository is a key pair somebody eventually deploys.
func vapid(t *testing.T) push.Config {
	t.Helper()
	pub, priv, err := push.GenerateKeys()
	if err != nil {
		t.Fatalf("generate vapid keys: %v", err)
	}
	return push.Config{PublicKey: pub, PrivateKey: priv, Subject: "soiree@example.test"}
}

// seedAdmin creates an active admin, which is who the digest goes to on both
// channels.
func (f *fixture) seedAdmin(t *testing.T, email string) store.User {
	t.Helper()
	u, err := f.store.CreateUser(t.Context(), store.User{
		Email: email, Role: store.RoleAdmin, Status: store.StatusActive,
	})
	if err != nil {
		t.Fatalf("seed admin %s: %v", email, err)
	}
	return u
}

// seedDevice records a subscription directly, bypassing the HTTP endpoint.
//
// Deliberately: the handler requires an https endpoint, and what is wanted here
// is a subscription pointing at a local test server so that the push service's
// answer is something this test chooses.
func (f *fixture) seedDevice(t *testing.T, user store.User, endpoint string) {
	t.Helper()
	// A real p256dh is a marshalled point on the P-256 curve, which the
	// encryption unmarshals before it makes any request at all. A placeholder
	// would fail before the fake service ever saw anything, and every
	// assertion below would then pass or fail for the wrong reason.
	deviceKey, _, err := push.GenerateKeys()
	if err != nil {
		t.Fatalf("generate device key: %v", err)
	}
	secret := make([]byte, 16)
	if _, err := rand.Read(secret); err != nil {
		t.Fatalf("generate auth secret: %v", err)
	}
	if _, err := f.store.SavePushSubscription(t.Context(), store.PushSubscription{
		UserID:   user.ID,
		Endpoint: endpoint,
		P256dh:   deviceKey,
		Auth:     base64.RawURLEncoding.EncodeToString(secret),
	}); err != nil {
		t.Fatalf("seed device: %v", err)
	}
}

// endpoints is every subscription still stored, in no particular order.
func (f *fixture) endpoints(t *testing.T) []string {
	t.Helper()
	subs, err := f.store.NotifiablePushSubscriptions(t.Context())
	if err != nil {
		t.Fatalf("read subscriptions: %v", err)
	}
	out := make([]string, 0, len(subs))
	for _, s := range subs {
		out = append(out, s.Endpoint)
	}
	return out
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// The single most important thing in this feature. A push service answering
// 410 has said the subscription is finished; anything else has said nothing
// about it. Prune the first and keep the second, or the table fills with
// endpoints that will never accept another notification and every digest for
// the rest of the deployment's life pays a round trip for each of them.
func TestAGoneSubscriptionIsPrunedAndATransientFailureIsNot(t *testing.T) {
	f := newFixture(t)
	f.seedDeadline(t, "Venue deposit", "Grand Hall", "2027-02-27", 250000, 0, 1)
	ada := f.seedAdmin(t, "ada@example.test")

	svc := newFakePushService(t)
	revoked := svc.answer("ada-old-phone", http.StatusGone)
	missing := svc.answer("ada-tablet", http.StatusNotFound)
	wobbly := svc.answer("ada-laptop", http.StatusInternalServerError)
	working := svc.answer("grace-phone", http.StatusCreated)
	for _, endpoint := range []string{revoked, missing, wobbly, working} {
		f.seedDevice(t, ada, endpoint)
	}

	sender := &fakeSender{}
	if err := f.service(t, sender).WithPush(push.New(vapid(t))).RunOnce(t.Context()); err != nil {
		t.Fatalf("run: %v", err)
	}

	// Every device was tried exactly once. A dead subscription must not be
	// retried inside one digest either.
	for _, device := range []string{"ada-old-phone", "ada-tablet", "ada-laptop", "grace-phone"} {
		if n := svc.hitCount(device); n != 1 {
			t.Errorf("%s was sent to %d times, want 1", device, n)
		}
	}

	left := f.endpoints(t)
	for _, endpoint := range []string{revoked, missing} {
		if contains(left, endpoint) {
			t.Errorf("a subscription the push service says is gone was kept: %s", endpoint)
		}
	}
	for _, endpoint := range []string{wobbly, working} {
		if !contains(left, endpoint) {
			t.Errorf("a subscription was removed on a transient failure: %s", endpoint)
		}
	}
	if len(left) != 2 {
		t.Errorf("%d subscriptions left, want the two that are still real", len(left))
	}

	// And the mail went out regardless of any of it.
	if sender.count() != 1 {
		t.Errorf("sent %d mails, want 1", sender.count())
	}
}

// A push service having a bad minute is not a failed digest. The mail is the
// primary channel and it went out; reporting a failed run would invite a retry
// of something that already happened.
func TestAFailingPushServiceDoesNotFailTheDigest(t *testing.T) {
	f := newFixture(t)
	f.seedDeadline(t, "Venue deposit", "Grand Hall", "2027-02-27", 250000, 0, 1)
	ada := f.seedAdmin(t, "ada@example.test")

	svc := newFakePushService(t)
	f.seedDevice(t, ada, svc.answer("ada-phone", http.StatusServiceUnavailable))

	sender := &fakeSender{}
	if err := f.service(t, sender).WithPush(push.New(vapid(t))).RunOnce(t.Context()); err != nil {
		t.Fatalf("a push service returning 503 failed the whole digest: %v", err)
	}
	if sender.count() != 1 {
		t.Fatalf("sent %d mails, want 1 — push broke the mail", sender.count())
	}
	led := f.ledger(t)
	if len(led) != 1 || !led[0].Sent {
		t.Errorf("ledger = %+v, want the period claimed and confirmed", led)
	}
	if len(f.endpoints(t)) != 1 {
		t.Error("a 503 removed the subscription")
	}
}

// The other direction. A relay that refuses the connection must not take the
// notifications down with it — and, because something did go out, the period
// stays claimed so a restart cannot notify the same devices twice.
func TestABrokenRelayDoesNotSuppressThePush(t *testing.T) {
	f := newFixture(t)
	f.seedDeadline(t, "Venue deposit", "Grand Hall", "2027-02-27", 250000, 0, 1)
	ada := f.seedAdmin(t, "ada@example.test")

	svc := newFakePushService(t)
	f.seedDevice(t, ada, svc.answer("ada-phone", http.StatusCreated))

	broken := &fakeSender{err: &mailer.SendError{Err: errors.New("connection refused")}}
	pusher := push.New(vapid(t))
	if err := f.service(t, broken).WithPush(pusher).RunOnce(t.Context()); err == nil {
		t.Fatal("expected the failed send to be reported")
	}
	if n := svc.hitCount("ada-phone"); n != 1 {
		t.Fatalf("the device was notified %d times, want 1 — a broken relay suppressed the push", n)
	}

	// The claim survives, because releasing it would assert that this period
	// reached nobody — and a notification is already on somebody's phone.
	led := f.ledger(t)
	if len(led) != 1 {
		t.Fatalf("ledger has %d rows, want the claim to have survived", len(led))
	}

	// So a restart within the same period notifies nobody a second time.
	f.now = f.now.Add(2 * time.Hour)
	if err := f.service(t, broken).WithPush(pusher).RunOnce(t.Context()); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if n := svc.hitCount("ada-phone"); n != 1 {
		t.Errorf("a restart inside the same period notified the device %d times", n)
	}
}

// With no devices subscribed, a retryable mail failure must still release the
// period exactly as it did before push existed: nothing went out on either
// channel, so the digest is owed.
func TestARefusedSendStillReleasesThePeriodWhenNothingWasPushed(t *testing.T) {
	f := newFixture(t)
	f.seedDeadline(t, "Venue deposit", "Grand Hall", "2027-02-27", 250000, 0, 1)
	f.seedAdmin(t, "ada@example.test")

	broken := &fakeSender{err: &mailer.SendError{Err: errors.New("connection refused")}}
	if err := f.service(t, broken).WithPush(push.New(vapid(t))).RunOnce(t.Context()); err == nil {
		t.Fatal("expected the failed send to be reported")
	}
	if led := f.ledger(t); len(led) != 0 {
		t.Fatalf("a retryable failure with nothing pushed left the period claimed: %+v", led)
	}

	working := &fakeSender{}
	if err := f.service(t, working).WithPush(push.New(vapid(t))).RunOnce(t.Context()); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if working.count() != 1 {
		t.Errorf("the retry sent %d digests, want 1", working.count())
	}
}

// A deployment with no VAPID keys is the default one. Push is simply off: the
// digest goes out by mail exactly as it did before this existed, and nothing
// reports an error about a feature nobody asked for.
func TestPushBeingUnconfiguredLeavesTheDigestAlone(t *testing.T) {
	f := newFixture(t)
	f.seedDeadline(t, "Venue deposit", "Grand Hall", "2027-02-27", 250000, 0, 1)
	ada := f.seedAdmin(t, "ada@example.test")

	// Subscriptions can exist — the keys could have been removed from the
	// deployment after somebody subscribed — and must simply be ignored rather
	// than pruned or failed over.
	svc := newFakePushService(t)
	endpoint := svc.answer("ada-phone", http.StatusCreated)
	f.seedDevice(t, ada, endpoint)

	sender := &fakeSender{}
	if err := f.service(t, sender).RunOnce(t.Context()); err != nil {
		t.Fatalf("run with no push configured: %v", err)
	}
	if sender.count() != 1 {
		t.Errorf("sent %d mails, want 1", sender.count())
	}
	if n := svc.hitCount("ada-phone"); n != 0 {
		t.Errorf("an unconfigured deployment made %d push requests", n)
	}
	if !contains(f.endpoints(t), endpoint) {
		t.Error("a subscription was removed by a deployment that cannot push at all")
	}
}

// Only active admins. A subscription made before somebody was disabled or
// demoted must not keep delivering the event's finances to their phone.
func TestOnlyActiveAdminsAreNotified(t *testing.T) {
	f := newFixture(t)
	f.seedDeadline(t, "Venue deposit", "Grand Hall", "2027-02-27", 250000, 0, 1)

	admin := f.seedAdmin(t, "ada@example.test")
	viewer, err := f.store.CreateUser(t.Context(), store.User{
		Email: "linus@example.test", Role: store.RoleViewer, Status: store.StatusActive,
	})
	if err != nil {
		t.Fatalf("seed viewer: %v", err)
	}
	invited, err := f.store.CreateUser(t.Context(), store.User{
		Email: "grace@example.test", Role: store.RoleAdmin, Status: store.StatusInvited,
	})
	if err != nil {
		t.Fatalf("seed invited admin: %v", err)
	}

	svc := newFakePushService(t)
	f.seedDevice(t, admin, svc.answer("ada-phone", http.StatusCreated))
	f.seedDevice(t, viewer, svc.answer("linus-phone", http.StatusCreated))
	f.seedDevice(t, invited, svc.answer("grace-phone", http.StatusCreated))

	if err := f.service(t, &fakeSender{}).WithPush(push.New(vapid(t))).RunOnce(t.Context()); err != nil {
		t.Fatalf("run: %v", err)
	}

	if n := svc.hitCount("ada-phone"); n != 1 {
		t.Errorf("the active admin's device was notified %d times, want 1", n)
	}
	for _, device := range []string{"linus-phone", "grace-phone"} {
		if n := svc.hitCount(device); n != 0 {
			t.Errorf("%s was notified %d times; only active admins receive the digest", device, n)
		}
	}
}

// The notification says the same thing the mail's subject says, and carries a
// URL for the click to open. Everything else is behind that click, which is
// what a payload this size forces and what the service worker relies on.
func TestTheNotificationSaysTheSameThingAsTheSubject(t *testing.T) {
	d, _, _ := renderFixture(t, "UTC")

	n := RenderPush(d, "https://soiree.example.test/")
	if n.Title == "" || n.Body == "" {
		t.Fatalf("notification = %+v, want a title and a body", n)
	}
	if !strings.Contains(n.Title, "Ada's Leaving Do") {
		t.Errorf("title = %q, want the event named", n.Title)
	}
	for _, want := range []string{"1 overdue", "2 coming up"} {
		if !strings.Contains(n.Body, want) {
			t.Errorf("body = %q, want it to contain %q", n.Body, want)
		}
	}
	// One trailing slash, not two: the origin is stored without one and the
	// path adds it.
	if n.URL != "https://soiree.example.test/" {
		t.Errorf("url = %q", n.URL)
	}
	if n.Tag != NotificationTag {
		t.Errorf("tag = %q, want %q so a second digest replaces the first", n.Tag, NotificationTag)
	}

	// With no configured origin the click still works: a service worker
	// resolves a relative URL against its own scope.
	if got := RenderPush(d, "").URL; got != "/" {
		t.Errorf("url with no base URL = %q, want %q", got, "/")
	}
}
