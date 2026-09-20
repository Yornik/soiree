// Package push delivers short notifications to browsers over Web Push.
//
// It is the second channel for the deadline digest. internal/mailer sends the
// whole thing to a mailbox; this sends a sentence to a lock screen, and the
// click opens the planner. That division is forced rather than chosen: a push
// payload is a few kilobytes at most, encrypted end to end, and the browser
// shows it in two lines. Trying to fit the digest into one would produce
// something unreadable in a place nobody reads carefully.
//
// # Off unless asked for
//
// A deployment with no VAPID key pair simply has no push, in the same way a
// deployment with no SMTP host has no mail. Nothing here is a startup failure
// and nothing here is required.
//
// # A gone subscription is gone
//
// This is the one piece of behaviour worth reading the code for. A push
// service answers 404 or 410 when a subscription no longer exists — the
// browser's storage was cleared, the app was uninstalled, permission was
// revoked, the endpoint was retired. That answer is final. Every other failure
// (a 500, a timeout, a refused connection) is the service or the network
// having a bad minute and says nothing about the subscription.
//
// Send reports the two differently, and the caller deletes on the first and
// keeps on the second. Without that, the table accumulates endpoints that will
// never accept another notification, and every digest for the rest of the
// deployment's life pays a round trip for each of them.
//
// # On iOS
//
// Safari only grants push to a site that has been added to the Home Screen.
// That is an Apple platform decision; no amount of correctness here changes
// it, and some recipients will therefore never receive a notification however
// well this works. It is the reason mail remains the primary channel.
package push

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
)

// Config is the VAPID identity this deployment signs with.
//
// This is what the transport needs, and it is deliberately a different struct
// from config.VAPIDConfig, which is the environment surface the browser's half
// is published from. Same split as mailer.Config against config.SMTPConfig,
// and for the same reason: one is checked against what a deployment needs, the
// other is what the protocol needs.
type Config struct {
	PublicKey  string
	PrivateKey string
	// Subject is where a push service complains to. Held in the form the
	// operator wrote it; normalising happens at the point of use.
	Subject string
}

// Configured reports whether there is enough here to send anything.
//
// All three: a push service may reject a signature whose JWT carries no `sub`,
// and discovering that one notification at a time is worse than the feature
// being visibly off.
func (c Config) Configured() bool {
	return c.PublicKey != "" && c.PrivateKey != "" && c.Subject != ""
}

// Partial reports a configuration that was started and not finished. It is not
// an error — nothing here refuses to start — but it is the one state an
// operator wants told to them, because from the outside it is indistinguishable
// from having set nothing at all.
func (c Config) Partial() bool {
	set := 0
	for _, v := range []string{c.PublicKey, c.PrivateKey, c.Subject} {
		if v != "" {
			set++
		}
	}
	return set > 0 && set < 3
}

// LoadConfig reads the VAPID identity from the environment.
//
// No error return, because the pair is checked once at startup rather than
// here: config.Load decodes both keys and refuses to boot unless they are two
// halves of one P-256 pair, so a process that reaches this has either nothing
// configured or something it can sign with. Reading the same variables that
// internal/config reads follows the mailer, which does the same — the two
// answer to different things, and the alternative is threading configuration
// through a scheduler that otherwise needs none.
func LoadConfig() Config {
	return Config{
		PublicKey:  strings.TrimSpace(os.Getenv("SOIREE_VAPID_PUBLIC_KEY")),
		PrivateKey: strings.TrimSpace(os.Getenv("SOIREE_VAPID_PRIVATE_KEY")),
		Subject:    strings.TrimSpace(os.Getenv("SOIREE_VAPID_SUBJECT")),
	}
}

// GenerateKeys makes a VAPID key pair, base64url, for
// SOIREE_VAPID_PUBLIC_KEY and SOIREE_VAPID_PRIVATE_KEY.
//
// A deployment needs a pair and there is nowhere to get one from: unlike an
// SMTP relay, no push service issues credentials, so the operator generates
// their own. Exported so that doing it needs nothing beyond this repository.
//
// Generate once and keep it. Every subscription a browser has made is bound to
// the public key it was made against, so a new pair silently invalidates all of
// them — the sends keep being accepted and nothing ever arrives.
func GenerateKeys() (publicKey, privateKey string, err error) {
	priv, pub, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return "", "", fmt.Errorf("push: generate VAPID keys: %w", err)
	}
	// Returned public-first, matching the order the environment variables are
	// documented in; webpush-go returns them the other way round, which is
	// exactly the kind of thing that gets transposed once and not noticed.
	return pub, priv, nil
}

// Subscription is one device, as the Push API described it to the page.
type Subscription struct {
	Endpoint string
	P256dh   string
	Auth     string
}

// Notification is what the service worker receives, verbatim, as the JSON body
// of its `push` event.
//
// Four short fields, and the shape is a contract with web/src/sw.js: adding a
// field is safe, renaming one is not. Everything the reader might want beyond
// this is behind the click, which is the whole design — see the package comment
// on why the payload cannot simply carry the digest.
type Notification struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	// URL is what a click opens. Absolute when the deployment knows its own
	// origin, "/" otherwise; a service worker resolves a relative one against
	// its scope either way.
	URL string `json:"url"`
	// Tag collapses notifications: a second one with the same tag replaces the
	// first on the lock screen rather than stacking beneath it. A phone that
	// was off for a fortnight should show this week's digest, not both.
	Tag string `json:"tag"`
}

// ErrGone reports a subscription the push service says no longer exists.
// Match it with errors.Is; it is the signal to delete the row.
var ErrGone = errors.New("push: subscription no longer exists")

const (
	// maxPayloadBytes bounds the encrypted body. The real ceiling imposed by
	// the push services is around 4 KB and webpush-go pads every message to a
	// 4096-byte record, so anything approaching it fails at encryption time
	// with a message about padding. Refusing earlier, here, names the actual
	// problem: whatever built this notification wrote too much.
	maxPayloadBytes = 3 << 10

	// notificationTTL is how long a push service should hold a notification
	// for a device that is offline. A digest describes the week it was sent
	// in; delivering a fortnight-old one would say something already answered
	// by the digest that followed it.
	notificationTTL = 24 * time.Hour

	// requestTimeout bounds one notification. Belt and braces over the
	// caller's context: a digest sending to a dozen devices must not be able
	// to spend its whole budget on the first one.
	requestTimeout = 10 * time.Second
)

// Sender delivers notifications. Safe for concurrent use.
type Sender struct {
	cfg Config

	// client is the HTTP client the notification is posted with. A field so
	// that this package's own tests can point it at an httptest server — no
	// test here may ever reach a real push service.
	client webpush.HTTPClient
}

// New builds a sender.
func New(cfg Config) *Sender {
	return &Sender{
		cfg:    cfg,
		client: &http.Client{Timeout: requestTimeout},
	}
}

// Send delivers one notification to one device.
//
// The returned error is the caller's whole decision procedure:
//
//   - nil — accepted by the push service, which is not the same as seen by a
//     person, and is as much as this protocol ever tells anybody;
//   - errors.Is(err, ErrGone) — the subscription is finished, delete it;
//   - anything else — transient; log it, keep the subscription, try next time.
func (s *Sender) Send(ctx context.Context, sub Subscription, n Notification) error {
	if !s.cfg.Configured() {
		// Reachable only through a programming mistake — the caller is meant
		// to check Configured — so it is an error rather than a silent no-op,
		// which would present as notifications that never arrive.
		return errors.New("push: no VAPID keys configured")
	}

	payload, err := json.Marshal(n)
	if err != nil {
		return fmt.Errorf("push: encode notification: %w", err)
	}
	if len(payload) > maxPayloadBytes {
		return fmt.Errorf("push: notification is %d bytes, over the %d-byte limit; send a summary and put the detail behind the click",
			len(payload), maxPayloadBytes)
	}

	res, err := webpush.SendNotificationWithContext(ctx, payload, &webpush.Subscription{
		Endpoint: sub.Endpoint,
		Keys:     webpush.Keys{P256dh: sub.P256dh, Auth: sub.Auth},
	}, &webpush.Options{
		HTTPClient:      s.client,
		Subscriber:      subscriber(s.cfg.Subject),
		VAPIDPublicKey:  s.cfg.PublicKey,
		VAPIDPrivateKey: s.cfg.PrivateKey,
		TTL:             int(notificationTTL.Seconds()),
		Urgency:         webpush.UrgencyNormal,
	})
	if err != nil {
		// No response at all: a refused connection, a timeout, or keys that do
		// not decode. None of those say the subscription is gone.
		return fmt.Errorf("push: send to %s: %w", service(sub.Endpoint), redactEndpoint(err))
	}
	// The body is never read — a push service has nothing to say beyond the
	// status — but it must be closed or the connection is not reused.
	defer func() { _ = res.Body.Close() }()

	return classify(service(sub.Endpoint), res.StatusCode)
}

// classify turns the push service's status into the caller's decision.
//
// The library reports a delivered request as (response, nil) whatever the
// status is, so this is where a 410 stops being an HTTP detail and becomes
// "delete this row". Getting the sense of it backwards is the failure mode
// worth guarding: prune on everything and one bad minute at the push service
// unsubscribes everybody; prune on nothing and the table never shrinks.
func classify(service string, status int) error {
	switch {
	case status >= 200 && status < 300:
		// 201 in practice, and 202 from some services. Accepted for delivery;
		// whether a person ever sees it is not something anybody is told.
		return nil
	case status == http.StatusNotFound || status == http.StatusGone:
		// 404: this endpoint never existed or has been retired. 410: it
		// existed and has been revoked. Both are final — the RFC 8030 answer
		// to "is this subscription still real?" is no, and it will not become
		// yes again.
		return fmt.Errorf("push: %s returned %d: %w", service, status, ErrGone)
	default:
		// 429 (backing off), 413 (payload too large — a bug, but not this
		// subscription's fault), 500, 502, 503. Keep the subscription.
		return fmt.Errorf("push: %s returned %d", service, status)
	}
}

// subscriber normalises the VAPID `sub` claim.
//
// webpush-go prefixes anything that is not an https URL with "mailto:", so an
// operator who sets SOIREE_VAPID_SUBJECT to the mailto: URL the specification
// asks for would otherwise sign every request with "mailto:mailto:…" — which
// the stricter push services refuse, and which is invisible until one does.
// Both forms are accepted here and mean the same thing.
func subscriber(subject string) string {
	return strings.TrimPrefix(subject, "mailto:")
}

// redactEndpoint strips the subscription URL out of a transport failure.
//
// net/http wraps every one of them in a *url.Error carrying the full request
// URL, and the path of a push endpoint is the capability to notify that device.
// The cause underneath — "connection refused", "context deadline exceeded" —
// says everything an operator needs without republishing it into a log file.
// Unwrapping to the cause also keeps errors.Is working against it.
func redactEndpoint(err error) error {
	var uerr *url.Error
	if errors.As(err, &uerr) && uerr.Err != nil {
		return uerr.Err
	}
	return err
}

// service names the push service an endpoint belongs to, for a log line or an
// error.
//
// The host and nothing else. A full endpoint URL is the capability to send
// that device a notification; copying it into a log file hands it to everyone
// who can read the logs, and "fcm.googleapis.com said 503" is the whole of
// what an operator needs anyway.
func service(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return "the push service"
	}
	return u.Host
}
