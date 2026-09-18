package push

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	webpush "github.com/SherClockHolmes/webpush-go"
)

// Nothing in this package's tests may reach a real push service. Every
// endpoint below is an httptest server on localhost: a suite that posted to
// fcm.googleapis.com would, the first time somebody ran it with production
// values in the environment, send notifications to real people's phones.

// fakeService is a push service that answers with whatever status it is told
// to and records what it was sent.
type fakeService struct {
	mu     sync.Mutex
	status int
	calls  int
	auth   string // the VAPID Authorization header of the last request
}

func newFakeService(t *testing.T, status int) (*fakeService, string) {
	t.Helper()
	f := &fakeService{status: status}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls++
		f.auth = r.Header.Get("Authorization")
		code := f.status
		f.mu.Unlock()
		w.WriteHeader(code)
	}))
	t.Cleanup(srv.Close)
	return f, srv.URL + "/push/AdaPhone"
}

func (f *fakeService) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeService) header() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.auth
}

// testConfig is a VAPID pair generated per test. Never a hardcoded one: a key
// pair committed to a repository is a key pair somebody eventually deploys.
func testConfig(t *testing.T) Config {
	t.Helper()
	priv, pub, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		t.Fatalf("generate vapid keys: %v", err)
	}
	return Config{PublicKey: pub, PrivateKey: priv, Subject: "ada@example.test"}
}

// testSubscription builds a subscription whose keys are real.
//
// p256dh has to be an actual point on the P-256 curve: the library unmarshals
// it before it makes any HTTP request at all, so a placeholder string fails at
// encryption time and the fake service never sees a thing — which would make
// every test here pass or fail for the wrong reason. A freshly generated VAPID
// public key is exactly the encoding wanted: a marshalled P-256 point in
// base64url.
func testSubscription(t *testing.T, endpoint string) Subscription {
	t.Helper()
	_, pub, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		t.Fatalf("generate device keys: %v", err)
	}
	secret := make([]byte, 16) // the auth secret is 16 bytes by RFC 8291
	if _, err := rand.Read(secret); err != nil {
		t.Fatalf("generate auth secret: %v", err)
	}
	return Subscription{
		Endpoint: endpoint,
		P256dh:   pub,
		Auth:     base64.RawURLEncoding.EncodeToString(secret),
	}
}

func testNotification() Notification {
	return Notification{
		Title: "Deadlines — Ada's Leaving Do",
		Body:  "1 overdue, 2 coming up",
		URL:   "https://soiree.example.test/",
		Tag:   "soiree-deadlines",
	}
}

func TestAcceptedNotification(t *testing.T) {
	svc, endpoint := newFakeService(t, http.StatusCreated)

	if err := New(testConfig(t)).Send(t.Context(), testSubscription(t, endpoint), testNotification()); err != nil {
		t.Fatalf("Send() to a service that accepted it: %v", err)
	}
	if svc.count() != 1 {
		t.Errorf("the push service was called %d times, want 1", svc.count())
	}
}

// The whole reason this package reports two kinds of failure. A push service
// answering 404 or 410 has said the subscription is finished; anything else has
// said nothing about it at all.
func TestGoneIsPermanentAndEverythingElseIsNot(t *testing.T) {
	permanent := []int{http.StatusNotFound, http.StatusGone}
	transient := []int{
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusTooManyRequests,
		http.StatusRequestEntityTooLarge,
		http.StatusUnauthorized,
	}

	for _, status := range permanent {
		svc, endpoint := newFakeService(t, status)
		err := New(testConfig(t)).Send(t.Context(), testSubscription(t, endpoint), testNotification())
		if !errors.Is(err, ErrGone) {
			t.Errorf("a %d must report ErrGone so the caller deletes the row, got %v", status, err)
		}
		if svc.count() != 1 {
			t.Errorf("a %d: the service was called %d times, want 1", status, svc.count())
		}
	}

	for _, status := range transient {
		_, endpoint := newFakeService(t, status)
		err := New(testConfig(t)).Send(t.Context(), testSubscription(t, endpoint), testNotification())
		switch {
		case err == nil:
			t.Errorf("a %d was reported as success", status)
		case errors.Is(err, ErrGone):
			t.Errorf("a %d was reported as permanent; one bad minute at a push service would unsubscribe everybody", status)
		}
	}
}

// A service that cannot be reached at all is transient too. It is the other
// half of the same decision, and the one a network blip produces.
func TestAnUnreachableServiceIsTransient(t *testing.T) {
	// Port 1 on the loopback: nothing is listening, and nothing can be.
	const dead = "https://127.0.0.1:1/push/AdaPhone"

	err := New(testConfig(t)).Send(t.Context(), testSubscription(t, dead), testNotification())
	if err == nil {
		t.Fatal("a refused connection was reported as success")
	}
	if errors.Is(err, ErrGone) {
		t.Errorf("a refused connection was treated as a gone subscription: %v", err)
	}
	// The endpoint is a capability to notify that device; an error string ends
	// up in a log file, which is read by more people than a database is.
	if strings.Contains(err.Error(), "/push/AdaPhone") {
		t.Errorf("the error carries the full endpoint: %v", err)
	}
}

// An operator who writes the mailto: URL the specification asks for must not
// end up signing every request with "mailto:mailto:…", which the stricter push
// services refuse and which nothing else would reveal.
func TestSubjectAcceptsBothFormsOfMailto(t *testing.T) {
	for _, subject := range []string{"ada@example.test", "mailto:ada@example.test"} {
		svc, endpoint := newFakeService(t, http.StatusCreated)
		cfg := testConfig(t)
		cfg.Subject = subject

		if err := New(cfg).Send(t.Context(), testSubscription(t, endpoint), testNotification()); err != nil {
			t.Fatalf("Send() with subject %q: %v", subject, err)
		}
		if got := vapidSubject(t, svc.header()); got != "mailto:ada@example.test" {
			t.Errorf("subject %q signed as sub=%q", subject, got)
		}
	}
}

// An https subject is a legal `sub` and must be passed through untouched.
func TestAnHTTPSSubjectIsLeftAlone(t *testing.T) {
	const url = "https://soiree.example.test/contact"
	svc, endpoint := newFakeService(t, http.StatusCreated)
	cfg := testConfig(t)
	cfg.Subject = url

	if err := New(cfg).Send(t.Context(), testSubscription(t, endpoint), testNotification()); err != nil {
		t.Fatalf("Send(): %v", err)
	}
	if got := vapidSubject(t, svc.header()); got != url {
		t.Errorf("sub = %q, want the URL unchanged", got)
	}
}

// vapidSubject pulls the `sub` claim out of a "vapid t=<jwt>, k=<key>" header.
func vapidSubject(t *testing.T, header string) string {
	t.Helper()
	_, rest, ok := strings.Cut(header, "t=")
	if !ok {
		t.Fatalf("no VAPID token in %q", header)
	}
	token, _, _ := strings.Cut(rest, ",")

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("not a JWT: %q", token)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode JWT claims: %v", err)
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatalf("parse JWT claims: %v", err)
	}
	return claims.Sub
}

// Sending with no keys must fail loudly rather than quietly doing nothing. The
// quiet version presents as notifications that never arrive, which is the
// hardest kind of bug to be told about.
func TestSendingWithNoKeysIsRefused(t *testing.T) {
	svc, endpoint := newFakeService(t, http.StatusCreated)
	if err := New(Config{}).Send(t.Context(), testSubscription(t, endpoint), testNotification()); err == nil {
		t.Fatal("Send() with no VAPID keys reported success")
	}
	if svc.count() != 0 {
		t.Errorf("an unconfigured sender still made %d requests", svc.count())
	}
}

func TestConfigured(t *testing.T) {
	full := Config{PublicKey: "pub", PrivateKey: "priv", Subject: "ada@example.test"}
	if !full.Configured() {
		t.Error("a complete configuration reports itself unconfigured")
	}
	if full.Partial() {
		t.Error("a complete configuration reports itself partial")
	}

	// The zero value is the default deployment: push is simply off, and that
	// is neither an error nor something to warn about.
	var none Config
	if none.Configured() || none.Partial() {
		t.Error("an empty configuration is neither configured nor partial")
	}

	for name, c := range map[string]Config{
		"no private key": {PublicKey: "pub", Subject: "ada@example.test"},
		"no public key":  {PrivateKey: "priv", Subject: "ada@example.test"},
		"no subject":     {PublicKey: "pub", PrivateKey: "priv"},
		"only a subject": {Subject: "ada@example.test"},
	} {
		t.Run(name, func(t *testing.T) {
			if c.Configured() {
				t.Error("a half-configured pair reports itself usable")
			}
			if !c.Partial() {
				t.Error("a half-configured pair is not reported as partial, so nobody is told")
			}
		})
	}
}

// The payload is small by necessity. Refusing early names the real problem —
// something wrote too much — rather than failing inside the encryption with a
// message about record padding.
func TestAnOversizedNotificationIsRefused(t *testing.T) {
	svc, endpoint := newFakeService(t, http.StatusCreated)
	n := testNotification()
	n.Body = strings.Repeat("a deadline, ", 500)

	err := New(testConfig(t)).Send(t.Context(), testSubscription(t, endpoint), n)
	if err == nil {
		t.Fatal("an oversized notification was accepted")
	}
	if errors.Is(err, ErrGone) {
		t.Error("an oversized payload was blamed on the subscription")
	}
	if svc.count() != 0 {
		t.Error("an oversized notification was still posted to the push service")
	}
}

// The service worker parses this, so the field names are a contract with
// web/src/sw.js rather than an implementation detail.
func TestNotificationWireShape(t *testing.T) {
	raw, err := json.Marshal(testNotification())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, field := range []string{"title", "body", "url", "tag"} {
		if _, ok := m[field]; !ok {
			t.Errorf("the notification payload has no %q field: %s", field, raw)
		}
	}
}
