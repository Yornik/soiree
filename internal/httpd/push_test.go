package httpd

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Yornik/soiree/internal/config"
	"github.com/Yornik/soiree/internal/migrate"
	"github.com/Yornik/soiree/internal/pgtest"
	"github.com/Yornik/soiree/internal/store"
	"github.com/Yornik/soiree/web"
)

// A real browser subscription is a URL at a push service plus two base64url
// keys. These are the right shapes and mean nothing; the encryption is tested
// in internal/push, against a local server, and never against a real service.
const (
	adaPhone  = "https://push.example.test/subscriptions/ada-phone"
	adaLaptop = "https://push.example.test/subscriptions/ada-laptop"

	p256dhKey = "BDrP4x8mS5nCq9pTt0YvQfVc2kXwLb7nHm4JsRgAe1oZ3dNiUyWpKcFvBt6xQaMlEr9SsHjDnPu2Gk5Yb8VwTcI"
	authKey   = "R1dGaHc5akxtUHFYdw"
)

// newPushFixture is the accounts surface and the API over one database, which
// is what the subscription endpoints need: they are mounted by routeAPI and
// guarded by the middleware the accounts surface owns.
func newPushFixture(t *testing.T) *fixture {
	t.Helper()

	pool := pgtest.Pool(t)
	if _, err := migrate.Run(t.Context(), pool, nil); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(pool)

	a := NewAuth(AuthOptions{Store: st, BaseURL: "https://soiree.example.test"})
	a.params = cheapParams
	a.background = func(fn func(context.Context)) { fn(context.Background()) }

	s, err := New(
		config.Config{EventName: "Ada's Leaving Do", Currency: "EUR", Locale: "en-US"},
		web.FS(),
		WithStore(st),
	)
	if err != nil {
		t.Fatalf("New(): %v", err)
	}

	return &fixture{h: s.WithAuth(a).Handler(), a: a, store: st, mail: &fakeMailer{}}
}

// subscription is the object a browser hands the page, including the
// expirationTime field that is part of it and is nearly always null. The client
// posts what it has rather than transcribing it, so this shape has to be
// accepted as it stands.
func subscription(endpoint string) map[string]any {
	return map[string]any{
		"endpoint":       endpoint,
		"expirationTime": nil,
		"keys":           map[string]string{"p256dh": p256dhKey, "auth": authKey},
	}
}

func unsubscribePath(endpoint string) string {
	return "/api/v1/push/subscriptions?endpoint=" + url.QueryEscape(endpoint)
}

func (f *fixture) devices(t *testing.T, user store.User) []store.PushSubscription {
	t.Helper()
	subs, err := f.store.PushSubscriptionsForUser(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("read subscriptions: %v", err)
	}
	return subs
}

// The browser re-sends the same subscription on every page load, because the
// Push API hands it back whatever already exists rather than minting a new one.
// A row per visit would be the result of getting this wrong, and the endpoint
// is also how a rotated key gets through.
func TestSubscribeIsIdempotentOnTheEndpoint(t *testing.T) {
	f := newPushFixture(t)
	ada := f.seed(t, "ada@example.test", store.RoleAdmin, goodPassword)
	cookie := f.login(t, "ada@example.test", goodPassword)

	for range 3 {
		if rec := f.do(t, http.MethodPost, "/api/v1/push/subscriptions", subscription(adaPhone), cookie); rec.Code != http.StatusNoContent {
			t.Fatalf("subscribe: status %d, body %s", rec.Code, rec.Body)
		}
	}

	subs := f.devices(t, ada)
	if len(subs) != 1 {
		t.Fatalf("three posts of one subscription produced %d rows, want 1", len(subs))
	}
	if subs[0].Endpoint != adaPhone || subs[0].P256dh != p256dhKey || subs[0].Auth != authKey {
		t.Errorf("stored subscription = %+v", subs[0])
	}
	if subs[0].UserID != ada.ID {
		t.Errorf("subscription belongs to %s, want the logged-in account %s", subs[0].UserID, ada.ID)
	}

	// A refresh must also carry a rotated key through, which is the other
	// reason a client re-posts.
	rotated := subscription(adaPhone)
	rotated["keys"] = map[string]string{"p256dh": p256dhKey, "auth": "TmV3QXV0aFNlY3JldA"}
	if rec := f.do(t, http.MethodPost, "/api/v1/push/subscriptions", rotated, cookie); rec.Code != http.StatusNoContent {
		t.Fatalf("refresh: status %d, body %s", rec.Code, rec.Body)
	}
	subs = f.devices(t, ada)
	if len(subs) != 1 {
		t.Fatalf("a refresh produced %d rows, want 1", len(subs))
	}
	if subs[0].Auth != "TmV3QXV0aFNlY3JldA" {
		t.Errorf("auth = %q, want the rotated key", subs[0].Auth)
	}
	if subs[0].LastSeenAt.Before(subs[0].CreatedAt) {
		t.Error("last_seen_at went backwards on a refresh")
	}
}

// A phone and a laptop are different subscriptions belonging to one person.
func TestOnePersonMayHaveSeveralDevices(t *testing.T) {
	f := newPushFixture(t)
	ada := f.seed(t, "ada@example.test", store.RoleAdmin, goodPassword)
	cookie := f.login(t, "ada@example.test", goodPassword)

	for _, endpoint := range []string{adaPhone, adaLaptop} {
		if rec := f.do(t, http.MethodPost, "/api/v1/push/subscriptions", subscription(endpoint), cookie); rec.Code != http.StatusNoContent {
			t.Fatalf("subscribe %s: status %d, body %s", endpoint, rec.Code, rec.Body)
		}
	}
	if subs := f.devices(t, ada); len(subs) != 2 {
		t.Errorf("two devices produced %d rows, want 2", len(subs))
	}
}

func TestUnsubscribeRemovesTheSubscription(t *testing.T) {
	f := newPushFixture(t)
	ada := f.seed(t, "ada@example.test", store.RoleAdmin, goodPassword)
	cookie := f.login(t, "ada@example.test", goodPassword)

	if rec := f.do(t, http.MethodPost, "/api/v1/push/subscriptions", subscription(adaPhone), cookie); rec.Code != http.StatusNoContent {
		t.Fatalf("subscribe: status %d, body %s", rec.Code, rec.Body)
	}
	if rec := f.do(t, http.MethodDelete, unsubscribePath(adaPhone), nil, cookie); rec.Code != http.StatusNoContent {
		t.Fatalf("unsubscribe: status %d, body %s", rec.Code, rec.Body)
	}
	if subs := f.devices(t, ada); len(subs) != 0 {
		t.Errorf("unsubscribe left %d rows", len(subs))
	}

	// Idempotent. The browser may well have dropped the subscription locally
	// before this request arrived, and turning something off twice is not a
	// failure worth reporting.
	if rec := f.do(t, http.MethodDelete, unsubscribePath(adaPhone), nil, cookie); rec.Code != http.StatusNoContent {
		t.Errorf("a second unsubscribe: status %d, body %s", rec.Code, rec.Body)
	}
}

// An endpoint is an unguessable URL, not a secret — and "unguessable" is not an
// authorisation model. Holding somebody else's must not be enough to switch
// their notifications off.
func TestUnsubscribeIsScopedToTheOwner(t *testing.T) {
	f := newPushFixture(t)
	ada := f.seed(t, "ada@example.test", store.RoleAdmin, goodPassword)
	f.seed(t, "grace@example.test", store.RoleAdmin, otherPassword)

	adaCookie := f.login(t, "ada@example.test", goodPassword)
	graceCookie := f.login(t, "grace@example.test", otherPassword)

	if rec := f.do(t, http.MethodPost, "/api/v1/push/subscriptions", subscription(adaPhone), adaCookie); rec.Code != http.StatusNoContent {
		t.Fatalf("subscribe: status %d, body %s", rec.Code, rec.Body)
	}
	// Answered the same way as a successful delete: a different status would
	// say whether that endpoint belongs to somebody.
	if rec := f.do(t, http.MethodDelete, unsubscribePath(adaPhone), nil, graceCookie); rec.Code != http.StatusNoContent {
		t.Fatalf("grace's unsubscribe: status %d, body %s", rec.Code, rec.Body)
	}
	if subs := f.devices(t, ada); len(subs) != 1 {
		t.Errorf("somebody else's unsubscribe removed ada's device: %d rows left", len(subs))
	}
}

// The same device, used by two people. The second login owns it, or the digest
// for one account would arrive on a device somebody else is now holding.
func TestASharedDeviceFollowsWhoeverIsLoggedIn(t *testing.T) {
	f := newPushFixture(t)
	ada := f.seed(t, "ada@example.test", store.RoleAdmin, goodPassword)
	grace := f.seed(t, "grace@example.test", store.RoleAdmin, otherPassword)

	for _, c := range []struct {
		email, password string
	}{{"ada@example.test", goodPassword}, {"grace@example.test", otherPassword}} {
		cookie := f.login(t, c.email, c.password)
		if rec := f.do(t, http.MethodPost, "/api/v1/push/subscriptions", subscription(adaPhone), cookie); rec.Code != http.StatusNoContent {
			t.Fatalf("subscribe as %s: status %d, body %s", c.email, rec.Code, rec.Body)
		}
	}

	if subs := f.devices(t, ada); len(subs) != 0 {
		t.Errorf("the device still belongs to the account that no longer holds it: %d rows", len(subs))
	}
	if subs := f.devices(t, grace); len(subs) != 1 {
		t.Errorf("the device did not follow the account that logged in second: %d rows", len(subs))
	}
}

// Both endpoints need a session. A subscription is recorded against an
// account, and an unauthenticated POST here would be an open invitation to
// fill the table.
func TestSubscriptionEndpointsNeedASession(t *testing.T) {
	f := newPushFixture(t)

	for _, c := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/api/v1/push/subscriptions", subscription(adaPhone)},
		{http.MethodDelete, unsubscribePath(adaPhone), nil},
	} {
		rec := f.do(t, c.method, c.path, c.body, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s with no session: status %d, want 401", c.method, rec.Code)
		}
	}
}

func TestMalformedSubscriptionsAreRefused(t *testing.T) {
	f := newPushFixture(t)
	ada := f.seed(t, "ada@example.test", store.RoleAdmin, goodPassword)
	cookie := f.login(t, "ada@example.test", goodPassword)

	for name, body := range map[string]map[string]any{
		"no endpoint": {
			"keys": map[string]string{"p256dh": p256dhKey, "auth": authKey},
		},
		"an endpoint that is not a URL": {
			"endpoint": "not a url at all",
			"keys":     map[string]string{"p256dh": p256dhKey, "auth": authKey},
		},
		// Every real push endpoint is https.
		"a plain http endpoint": {
			"endpoint": "http://169.254.169.254/latest/meta-data/",
			"keys":     map[string]string{"p256dh": p256dhKey, "auth": authKey},
		},
		// The same address over https. Storing it would have the digest run
		// post to it from inside the network this server runs in.
		"an address inside the network": {
			"endpoint": "https://169.254.169.254/latest/meta-data/",
			"keys":     map[string]string{"p256dh": p256dhKey, "auth": authKey},
		},
		"no keys at all": {
			"endpoint": adaPhone,
		},
		"only one key": {
			"endpoint": adaPhone,
			"keys":     map[string]string{"p256dh": p256dhKey},
		},
	} {
		t.Run(name, func(t *testing.T) {
			rec := f.do(t, http.MethodPost, "/api/v1/push/subscriptions", body, cookie)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("%s: status %d, want 400 (body %s)", name, rec.Code, rec.Body)
			}
		})
	}

	if subs := f.devices(t, ada); len(subs) != 0 {
		t.Errorf("a refused subscription was stored anyway: %+v", subs)
	}

	if rec := f.do(t, http.MethodDelete, "/api/v1/push/subscriptions", nil, cookie); rec.Code != http.StatusBadRequest {
		t.Errorf("unsubscribe with no endpoint: status %d, want 400", rec.Code)
	}
}

// An endpoint is not only stored: the digest run posts to it, from wherever
// this server happens to run. So the host is as much a part of "is this a push
// subscription?" as the scheme is, and the address forms that name this
// deployment's own network are refused before a row exists.
//
// No database here — the rule is the one function, and checking it directly is
// what lets the awkward spellings of the same address be covered cheaply.
func TestAnEndpointInsideTheNetworkIsRefused(t *testing.T) {
	for _, raw := range []string{
		"https://127.0.0.1/send/abc",
		"https://[::1]:8443/send/abc",
		"https://localhost/send/abc",
		"https://metadata.localhost./send/abc",
		"https://10.0.0.5/send/abc",
		// A port must not carry the address past the check.
		"https://192.168.1.7:8443/send/abc",
		// The same private address, written as IPv6.
		"https://[::ffff:10.0.0.5]/send/abc",
		"https://169.254.169.254/latest/meta-data/",
		"https://[fd00::1]/send/abc",
		"https://[fe80::1]/send/abc",
		"https://0.0.0.0/send/abc",
	} {
		if endpoint, problem := checkEndpoint(raw); problem == "" {
			t.Errorf("checkEndpoint(%q) stored %q, so the digest would post there", raw, endpoint)
		}
	}

	// And what a real push service looks like still goes through, port and all.
	for _, raw := range []string{
		"https://push.example.test/send/abc",
		"https://push.example.test:8443/send/abc",
		"https://198.51.100.7/send/abc",
	} {
		if _, problem := checkEndpoint(raw); problem != "" {
			t.Errorf("checkEndpoint(%q) refused a push service: %s", raw, problem)
		}
	}
}

// A viewer may subscribe. Who *receives* the digest is decided at send time
// from the account's role, so somebody later promoted to admin starts getting
// notifications without having to subscribe again.
func TestAnyoneSignedInMaySubscribe(t *testing.T) {
	f := newPushFixture(t)
	linus := f.seed(t, "linus@example.test", store.RoleViewer, goodPassword)
	cookie := f.login(t, "linus@example.test", goodPassword)

	if rec := f.do(t, http.MethodPost, "/api/v1/push/subscriptions", subscription(adaPhone), cookie); rec.Code != http.StatusNoContent {
		t.Fatalf("a viewer subscribing: status %d, body %s", rec.Code, rec.Body)
	}
	if subs := f.devices(t, linus); len(subs) != 1 {
		t.Errorf("a viewer's subscription was not stored: %d rows", len(subs))
	}

	// …and it is not in the digest's audience while they are a viewer.
	notifiable, err := f.store.NotifiablePushSubscriptions(t.Context())
	if err != nil {
		t.Fatalf("read notifiable subscriptions: %v", err)
	}
	if len(notifiable) != 0 {
		t.Errorf("a viewer's device is in the digest's audience: %+v", notifiable)
	}
}

// Which release is running is for people who can sign in, and nobody else: it
// is the first thing anybody probing a deployment asks.
func TestTheVersionIsToldOnlyToSomebodySignedIn(t *testing.T) {
	f := newPushFixture(t)
	f.seed(t, "viewer@example.test", store.RoleViewer, "correct horse battery staple")

	if rec := f.do(t, http.MethodGet, "/api/v1/version", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("with no session: %d %s, want 401", rec.Code, rec.Body)
	}

	old := Version
	Version = "1.2.3"
	t.Cleanup(func() { Version = old })

	// The least privileged account there is. It is a read.
	cookie := f.login(t, "viewer@example.test", "correct horse battery staple")
	rec := f.do(t, http.MethodGet, "/api/v1/version", nil, cookie)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"version":"1.2.3"}` {
		t.Fatalf("signed in: %d %s", rec.Code, rec.Body)
	}
}
