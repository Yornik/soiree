// Package config loads runtime configuration from the environment.
//
// Everything event-specific lives here rather than in the source, which is
// what lets one image serve any event without a rebuild.
package config

import (
	"bytes"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the full runtime configuration.
type Config struct {
	ListenAddr string

	// MetricsAddr is a listener of its own, carrying /metrics and nothing else.
	//
	// A separate port rather than a path on the public listener because the
	// ingress route has no path constraint: anything the main listener serves
	// is world-readable. The exposition carries no personal data, but
	// soiree_build_info hands out the exact version and commit, which is free
	// reconnaissance. A second port is the shape a ServiceMonitor expects
	// anyway, and it is the only version of "private" that does not depend on
	// somebody remembering to write a path exclusion.
	//
	// The probes deliberately stay on the main listener: kubelet reaches the
	// container's main port, and the deployment manifests already point there.
	MetricsAddr string

	// DatabaseURL is the PostgreSQL DSN. Empty is a supported configuration
	// rather than a missing one: with no DSN the binary serves the frontend
	// alone and the API is not mounted at all, which is exactly what a bare
	// `docker run` — and the image smoke test in CI — does.
	//
	// Unprefixed, unlike everything else here, because DATABASE_URL is the
	// name every Postgres tool and the CloudNativePG connection secret already
	// uses. It is also not part of the event's identity, so it does not belong
	// to the SOIREE_* de-personalisation boundary.
	DatabaseURL string

	EventName string
	Tagline   string
	EventDate string // RFC3339, with the offset it was written in. Empty means no countdown.

	Currency          string
	Locale            string
	SecondaryCurrency string
	SecondaryLocale   string

	Ceiling  int64
	DemoData bool

	// AllowIndexing opts this deployment in to search engines. Off by default,
	// and the default is the interesting part: a planner holds people's names
	// against amounts of money they owe each other, and none of them chose to
	// publish that. Opting in should be a decision somebody made, not the
	// consequence of never having thought about it.
	AllowIndexing bool

	// DisableCSP stops the binary sending its own Content-Security-Policy,
	// for a deployment whose proxy sends one instead. Negative so that the
	// zero value sends the policy: a Config built by anything other than Load
	// should get the safe answer without having to know this field exists.
	DisableCSP bool

	// BaseURL is the origin this deployment is reached at, e.g.
	// https://soiree.example.test. Set-password links are built from it and
	// never from the request's Host header: a Host header is attacker-supplied,
	// and a reset link built from one points the recipient's one-time secret at
	// whatever host the attacker asked for.
	BaseURL string

	// TrustProxyHeaders says whether X-Forwarded-For may be believed. Off by
	// default: with it on and no proxy in front, anyone can pick their own
	// client address and the per-IP rate limits stop meaning anything.
	TrustProxyHeaders bool

	// BootstrapAdmin is an address that becomes the first admin if the
	// deployment has none. It breaks the circularity of "every account is
	// created by an admin" on an empty database and does nothing thereafter.
	BootstrapAdmin string

	// PasskeysEnabled says whether this deployment offers WebAuthn passkeys
	// alongside the password. Derived rather than simply read: a passkey
	// ceremony is bound to a Relying Party ID, and the only trustworthy source
	// for that is BaseURL, so with no BaseURL there is nothing to bind to and
	// the answer is no whatever the environment says. See loadPasskeys.
	//
	// The browser is told this in ClientConfig, and decides from it whether to
	// offer the passkey button — so it has to agree with whether the routes are
	// actually mounted. cmd/soiree clears it when there is no database, which is
	// the one condition this package cannot see.
	PasskeysEnabled bool

	// PasskeyRPID is the WebAuthn Relying Party ID: the domain a credential is
	// scoped to, and the value hashed into every authenticator response.
	//
	// It comes from BaseURL and never from a request's Host header, which is
	// attacker-supplied — a ceremony bound to a Host header is a ceremony an
	// attacker chose the scope of. It is also the field with the quietest
	// failure mode in the whole protocol: a credential registered under the
	// wrong RP ID simply never matches again, and the browser reports nothing
	// more useful than "no credentials available".
	PasskeyRPID string

	// PasskeyOrigin is the one origin a ceremony's collected client data may
	// declare. Exactly the origin of BaseURL, with no siblings added: widening
	// this is how a host somebody else controls becomes a way to mint
	// assertions this server accepts.
	PasskeyOrigin string

	// BootstrapPassword, when set, gives that first admin a password so they
	// can log in straight away.
	//
	// Without it the first account is reachable only through a mailed
	// set-password link, and for the *first* account that is a dead end when
	// SMTP is wrong: password-reset hands the link to the mailer and discards
	// it, and every route that returns the link instead is admin-only. This is
	// the way in that does not depend on mail working.
	//
	// It is consumed only while no admin exists, so it is safe to leave set.
	BootstrapPassword string

	SMTP SMTPConfig

	// VAPID is the Web Push signing identity. The zero value means no push
	// notifications, which is a supported deployment rather than a broken one.
	VAPID VAPIDConfig

	// Attachments is the bucket files are kept in. The zero value means no
	// attachments, which is also a supported deployment.
	Attachments AttachmentsConfig
}

// AttachmentsConfig is where uploaded files live and how large they may be.
//
// The bytes are in an S3 bucket and never pass through this server: the browser
// uploads to the bucket and downloads from it, over URLs signed here. So the
// endpoint has to be reachable from wherever the people are, not merely from
// the server, and the bucket needs a CORS rule that lets this site's pages PUT
// to it. docs/operating.md has the rule.
//
// This is the environment surface; internal/objstore is what uses it, and
// cmd/soiree maps one onto the other, as it does for mail and push.
type AttachmentsConfig struct {
	Endpoint        string
	Region          string
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string

	// MaxBytes is the most one file may be, and TotalBytes the most all of
	// them together may be. Both are enforced before an upload starts: the
	// size is signed into the upload URL, so the bucket itself refuses a body
	// of any other length.
	MaxBytes   int64
	TotalBytes int64
}

// Defaults that fit a phone photograph of a receipt or a scanned contract with
// room to spare, and a whole event inside a small bucket.
const (
	DefaultAttachmentMaxMB    = 25
	DefaultAttachmentsTotalMB = 2048
)

// Enabled reports whether files can be attached.
func (a AttachmentsConfig) Enabled() bool { return a.Endpoint != "" }

// ClientAttachments is what the page needs to know about attachments: that
// they exist, and the size past which it should not bother asking.
type ClientAttachments struct {
	MaxBytes int64 `json:"maxBytes"`
}

// VAPIDConfig identifies this deployment to the browsers' push services.
//
// Web Push has no API key and no account: the server proves who it is by
// signing each request with a P-256 key pair it generated itself, and the
// browser pins the public half at subscribe time. That is why the public key
// has to reach the page — a subscription made against one key cannot be sent
// to with another.
//
// This is the environment surface. The transport that uses it is
// internal/push, and cmd/soiree maps one onto the other, exactly as
// config.SMTPConfig maps onto mailer.Config.
type VAPIDConfig struct {
	// PublicKey and PrivateKey are the base64url halves of one P-256 pair,
	// generated once per deployment (webpush.GenerateVAPIDKeys produces both).
	// Rotating them invalidates every existing subscription.
	PublicKey string
	// PrivateKey is a signing key. It must never leave the server, which is
	// what ClientConfig below is careful about.
	PrivateKey string
	// Subject is the contact a push service can reach the operator at when
	// this deployment misbehaves — an address or an https URL. Written bare
	// ("ada@example.test") or as a "mailto:" URL; internal/push normalises it.
	Subject string
}

// Enabled reports whether push notifications can be sent.
//
// All three parts, not just the pair: a push service is entitled to refuse a
// request whose JWT has no `sub`, and finding that out one notification at a
// time is worse than not offering the feature.
func (v VAPIDConfig) Enabled() bool {
	return v.PublicKey != "" && v.PrivateKey != "" && v.Subject != ""
}

// SMTPConfig is the outgoing mail relay. The zero value means no mail, which
// is a supported deployment rather than a broken one.
//
// This is the environment surface, validated here; the transport it is handed
// to is internal/mailer, and cmd/soiree maps one onto the other. Port is a
// number rather than the string the environment carries, so the parse happens
// once, where the variable it came from can still be named in the error.
type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
}

// MinPasswordLen is the floor for any password this deployment accepts,
// including the bootstrap one. Kept here so the environment and the HTTP
// surface cannot drift into disagreeing about what is acceptable.
const MinPasswordLen = 12

// DefaultSMTPPort is implicit TLS. It matches mailer.DefaultPort, which is the
// value that actually decides how the connection is made; cmd/soiree's tests
// pin the two together rather than leaving the pair to drift.
const DefaultSMTPPort = 465

// Enabled reports whether mail can be sent.
func (s SMTPConfig) Enabled() bool { return s.Host != "" }

// ClientConfig is the subset handed to the browser. It is marshalled into a
// JSON data block in the page, so it must contain nothing secret.
type ClientConfig struct {
	EventName         string `json:"eventName"`
	Tagline           string `json:"tagline"`
	EventDate         string `json:"eventDate"`
	Currency          string `json:"currency"`
	Locale            string `json:"locale"`
	SecondaryCurrency string `json:"secondaryCurrency"`
	SecondaryLocale   string `json:"secondaryLocale"`
	Ceiling           int64  `json:"ceiling"`
	DemoData          bool   `json:"demoData"`

	// VAPIDPublicKey is the browser's half of the Web Push identity, and is
	// public by design — a subscription is made against it, and a browser that
	// has not seen it cannot subscribe at all. The private half is the secret,
	// and it is deliberately absent from this struct.
	//
	// Omitted entirely when push is off, so that "is there a key here?" is the
	// single question the client asks before offering to turn notifications on.
	VAPIDPublicKey string `json:"vapidPublicKey,omitempty"`
	// Passkeys tells the browser whether to offer "sign in with a passkey".
	// A capability flag and nothing more: it names no domain, carries no key,
	// and reveals nothing a request to the login page would not.
	Passkeys bool `json:"passkeys"`
	// Attachments is present only when files can actually be attached, so that
	// "is this here?" is the one question the page asks before drawing the
	// control. It names no bucket and no endpoint: the browser learns where to
	// send a file from the signed URL it is handed, one upload at a time.
	Attachments *ClientAttachments `json:"attachments,omitempty"`
}

// Client returns the browser-facing view of the configuration.
func (c Config) Client() ClientConfig {
	return ClientConfig{
		EventName:         c.EventName,
		Tagline:           c.Tagline,
		EventDate:         c.EventDate,
		Currency:          c.Currency,
		Locale:            c.Locale,
		SecondaryCurrency: c.SecondaryCurrency,
		SecondaryLocale:   c.SecondaryLocale,
		Ceiling:           c.Ceiling,
		DemoData:          c.DemoData,
		VAPIDPublicKey:    c.publishablePushKey(),
		Passkeys:          c.PasskeysEnabled,
		Attachments:       c.publishableAttachments(),
	}
}

// publishableAttachments is the limit, and only when an upload could succeed.
//
// A file's record lives in the database, so a bucket with no database behind
// it is not a feature that is half on; it is one that is off. Publishing it
// anyway would draw a control whose every use fails.
func (c Config) publishableAttachments() *ClientAttachments {
	if !c.Attachments.Enabled() || c.DatabaseURL == "" {
		return nil
	}
	return &ClientAttachments{MaxBytes: c.Attachments.MaxBytes}
}

// publishablePushKey is the public key, and only when the server could
// actually send with it.
//
// Publishing the public half of a pair whose private half is missing is worse
// than publishing nothing: the browser subscribes, the permission prompt is
// spent, the UI reports success, and not one notification ever arrives. An
// absent key is a feature that is visibly off.
func (c Config) publishablePushKey() string {
	if !c.VAPID.Enabled() {
		return ""
	}
	return c.VAPID.PublicKey
}

// ClientJSON returns the configuration as compact JSON for embedding.
func (c Config) ClientJSON() (string, error) {
	b, err := json.Marshal(c.Client())
	if err != nil {
		return "", fmt.Errorf("marshal client config: %w", err)
	}
	return string(b), nil
}

// Load reads configuration from the environment, applying defaults and
// validating anything that would otherwise fail confusingly at runtime.
func Load() (Config, error) {
	c := Config{
		ListenAddr:        env("SOIREE_LISTEN_ADDR", ":8080"),
		MetricsAddr:       env("SOIREE_METRICS_ADDR", ":9090"),
		DatabaseURL:       strings.TrimSpace(os.Getenv("DATABASE_URL")),
		EventName:         env("SOIREE_EVENT_NAME", "A Celebration"),
		Tagline:           env("SOIREE_EVENT_TAGLINE", ""),
		EventDate:         strings.TrimSpace(os.Getenv("SOIREE_EVENT_DATE")),
		Currency:          strings.ToUpper(env("SOIREE_CURRENCY", "EUR")),
		Locale:            env("SOIREE_LOCALE", "en-US"),
		SecondaryCurrency: strings.ToUpper(strings.TrimSpace(os.Getenv("SOIREE_SECONDARY_CURRENCY"))),
		SecondaryLocale:   env("SOIREE_SECONDARY_LOCALE", ""),
	}

	if c.SecondaryLocale == "" {
		c.SecondaryLocale = c.Locale
	}

	if v := strings.TrimSpace(os.Getenv("SOIREE_BUDGET_CEILING")); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return Config{}, fmt.Errorf("SOIREE_BUDGET_CEILING must be a whole number, got %q", v)
		}
		if n < 0 {
			return Config{}, fmt.Errorf("SOIREE_BUDGET_CEILING must not be negative, got %d", n)
		}
		c.Ceiling = n
	}

	if v := strings.TrimSpace(os.Getenv("SOIREE_ALLOW_INDEXING")); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("SOIREE_ALLOW_INDEXING must be a boolean, got %q", v)
		}
		c.AllowIndexing = b
	}

	// Spelled on/off rather than as a boolean because there is nothing to be
	// true about: the question is whether this binary or the proxy in front of
	// it owns the policy. An unparseable value is a typo, and a typo here
	// silently drops a defence, so it refuses to start.
	if v := strings.TrimSpace(os.Getenv("SOIREE_CSP")); v != "" {
		switch strings.ToLower(v) {
		case "on":
		case "off":
			c.DisableCSP = true
		default:
			return Config{}, fmt.Errorf("SOIREE_CSP must be \"on\" or \"off\", got %q", v)
		}
	}

	if v := strings.TrimSpace(os.Getenv("SOIREE_DEMO_DATA")); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("SOIREE_DEMO_DATA must be a boolean, got %q", v)
		}
		c.DemoData = b
	}

	// Reject a date the browser would silently misinterpret. Requiring an
	// explicit offset is the whole point: without one the countdown differs
	// by a day depending on where the viewer is.
	if c.EventDate != "" {
		t, err := time.Parse(time.RFC3339, c.EventDate)
		if err != nil {
			return Config{}, fmt.Errorf("SOIREE_EVENT_DATE must be RFC3339 with a timezone (e.g. 2027-06-12T00:00:00Z), got %q", c.EventDate)
		}
		// Kept in the offset it was written in, deliberately. It used to be
		// converted to UTC here, which is the same instant and a different
		// calendar day: an evening that begins at midnight at +09:00 is
		// 15:00 UTC the day before, so the page announced the wrong date to
		// everybody, wherever they were. The offset is the only thing that
		// says which day the event is on, and the page needs it for that.
		c.EventDate = t.Format(time.RFC3339)
	}

	// The whole point of the second listener is that the public one cannot
	// reach it. Exact string equality only: :8080 and 0.0.0.0:8080 are the same
	// socket and this will not catch that, but the case worth catching is the
	// operator who set one variable and forgot the other, and the alternative
	// is a bind failure at startup that names a port without saying why.
	if c.MetricsAddr == c.ListenAddr {
		return Config{}, fmt.Errorf("SOIREE_METRICS_ADDR must differ from SOIREE_LISTEN_ADDR, or /metrics is served on the public port after all; both are %q", c.ListenAddr)
	}

	if len(c.Currency) != 3 {
		return Config{}, fmt.Errorf("SOIREE_CURRENCY must be a 3-letter ISO 4217 code, got %q", c.Currency)
	}
	if c.SecondaryCurrency != "" && len(c.SecondaryCurrency) != 3 {
		return Config{}, fmt.Errorf("SOIREE_SECONDARY_CURRENCY must be a 3-letter ISO 4217 code, got %q", c.SecondaryCurrency)
	}

	if err := c.loadAccounts(); err != nil {
		return Config{}, err
	}
	if err := c.loadPush(); err != nil {
		return Config{}, err
	}
	if err := c.loadAttachments(); err != nil {
		return Config{}, err
	}

	return c, nil
}

// loadAttachments reads the bucket, and refuses a half-configured one.
//
// Push tolerates a missing part and this does not, for the reason SMTP does
// not: the failure would otherwise surface late and to the wrong person. A
// bucket with no secret starts cleanly, draws the upload control, and then
// fails every upload in somebody's hand, on a phone, with an error from a
// storage service they have never heard of. All five or none.
func (c *Config) loadAttachments() error {
	vars := []struct {
		name string
		into *string
	}{
		{"SOIREE_S3_ENDPOINT", &c.Attachments.Endpoint},
		{"SOIREE_S3_REGION", &c.Attachments.Region},
		{"SOIREE_S3_BUCKET", &c.Attachments.Bucket},
		{"SOIREE_S3_ACCESS_KEY_ID", &c.Attachments.AccessKeyID},
		{"SOIREE_S3_SECRET_ACCESS_KEY", &c.Attachments.SecretAccessKey},
	}
	var set, missing []string
	for _, v := range vars {
		*v.into = strings.TrimSpace(os.Getenv(v.name))
		if *v.into == "" {
			missing = append(missing, v.name)
		} else {
			set = append(set, v.name)
		}
	}
	if len(set) > 0 && len(missing) > 0 {
		return fmt.Errorf("attachments need all five SOIREE_S3_* variables or none of them; %s set, %s missing",
			strings.Join(set, ", "), strings.Join(missing, ", "))
	}

	for _, limit := range []struct {
		name string
		def  int64
		into *int64
	}{
		{"SOIREE_ATTACHMENT_MAX_MB", DefaultAttachmentMaxMB, &c.Attachments.MaxBytes},
		{"SOIREE_ATTACHMENTS_TOTAL_MB", DefaultAttachmentsTotalMB, &c.Attachments.TotalBytes},
	} {
		mb := limit.def
		if v := strings.TrimSpace(os.Getenv(limit.name)); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n <= 0 {
				return fmt.Errorf("%s must be a whole number of megabytes above zero, got %q", limit.name, v)
			}
			mb = n
		}
		*limit.into = mb << 20
	}
	if c.Attachments.MaxBytes > c.Attachments.TotalBytes {
		return fmt.Errorf("SOIREE_ATTACHMENT_MAX_MB (%d) is larger than SOIREE_ATTACHMENTS_TOTAL_MB (%d): no file could ever be that large",
			c.Attachments.MaxBytes>>20, c.Attachments.TotalBytes>>20)
	}
	return nil
}

// loadPush reads the Web Push identity.
//
// Unset means push is off, exactly as an unset SOIREE_SMTP_HOST means mail is
// off, and the binary has to start with neither — that is what a bare
// `docker run` and the image smoke test do.
//
// A half-configured pair is not refused, which is the opposite of the rule
// SMTP follows two functions down. The reason the two differ: a sender with no
// relay *looks* configured and silently sends nothing, whereas Enabled() is
// false unless all three of these are present, so a half-configured pair never
// reaches the browser and never has anything subscribed against it. The
// operator hears about it from the startup log instead of from a refusal to
// boot — see reminders.Start.
//
// A complete set is checked, because that reasoning stops covering it: three
// values that are present but do not form a pair do reach the browser.
func (c *Config) loadPush() error {
	c.VAPID = VAPIDConfig{
		PublicKey:  strings.TrimSpace(os.Getenv("SOIREE_VAPID_PUBLIC_KEY")),
		PrivateKey: strings.TrimSpace(os.Getenv("SOIREE_VAPID_PRIVATE_KEY")),
		Subject:    strings.TrimSpace(os.Getenv("SOIREE_VAPID_SUBJECT")),
	}
	if !c.VAPID.Enabled() {
		return nil
	}
	return c.VAPID.validate()
}

// validate checks that the two keys are halves of one P-256 pair.
//
// Two mistakes are worth catching here, and here is the only place either can
// be caught: nothing between this and the first send looks at the keys at all,
// and the library decodes them per send.
//
// Transposing the two variables is the mistake docs/operating.md warns about,
// because webpush.GenerateVAPIDKeys returns the pair the other way round. It
// publishes the signing key in the config block of a page served without a
// session to anyone, and nothing afterwards reports it: a browser refuses to
// subscribe against a 32-byte applicationServerKey, so no subscription is ever
// made, no digest is ever sent, and no line appears in any log. The length
// rules below are what make that impossible — a 32-byte scalar can never be
// published as a 65-byte point.
//
// A key that is merely malformed fails at the first weekly send instead, a week
// after the browser's permission prompt was spent, and a push service's 401 is
// treated as transient, so it recurs every week after that.
//
// Nothing here may put a key in its error: this message goes to stdout and into
// whatever collects it, and in the transposed case the public variable holds
// the private half. Variable names and byte lengths only.
func (v VAPIDConfig) validate() error {
	pub, err := decodePushKey(v.PublicKey)
	if err != nil {
		return fmt.Errorf("SOIREE_VAPID_PUBLIC_KEY is not base64url: %w", err)
	}
	priv, err := decodePushKey(v.PrivateKey)
	if err != nil {
		return fmt.Errorf("SOIREE_VAPID_PRIVATE_KEY is not base64url: %w", err)
	}

	if len(pub) == vapidPrivateLen && len(priv) == vapidPublicLen {
		return fmt.Errorf("the two SOIREE_VAPID keys are the wrong way round: the public one is %d bytes and the private one %d, which is the pair transposed (webpush.GenerateVAPIDKeys returns the private key first)",
			len(pub), len(priv))
	}
	if len(pub) != vapidPublicLen || pub[0] != 4 {
		return fmt.Errorf("SOIREE_VAPID_PUBLIC_KEY must be an uncompressed P-256 point: %d bytes, want %d beginning 0x04",
			len(pub), vapidPublicLen)
	}
	if len(priv) != vapidPrivateLen {
		return fmt.Errorf("SOIREE_VAPID_PRIVATE_KEY must be a P-256 scalar: %d bytes, want %d",
			len(priv), vapidPrivateLen)
	}

	key, err := ecdh.P256().NewPrivateKey(priv)
	if err != nil {
		return fmt.Errorf("SOIREE_VAPID_PRIVATE_KEY is not a usable P-256 key: %w", err)
	}
	if !bytes.Equal(key.PublicKey().Bytes(), pub) {
		return errors.New("SOIREE_VAPID_PUBLIC_KEY is not the public half of SOIREE_VAPID_PRIVATE_KEY; a browser would subscribe against a key this deployment cannot sign for")
	}
	return nil
}

// The two halves of a VAPID pair, in bytes: an uncompressed P-256 point and
// the scalar it was derived from.
const (
	vapidPublicLen  = 65
	vapidPrivateLen = 32
)

// decodePushKey accepts padded base64url and then raw, in that order, which is
// what webpush-go's own decoder does. Accepting less than the sender accepts
// would refuse a deployment that works.
func decodePushKey(key string) ([]byte, error) {
	if b, err := base64.URLEncoding.DecodeString(key); err == nil {
		return b, nil
	}
	return base64.RawURLEncoding.DecodeString(key)
}

// loadAccounts reads everything the accounts milestone added and rejects the
// half-configured states.
//
// The failures worth catching here are the ones that otherwise surface as a
// mail that never arrives or a link that goes nowhere — days later, to someone
// who cannot see the logs.
func (c *Config) loadAccounts() error {
	// DATABASE_URL rather than SOIREE_DATABASE_URL: it is the name the
	// development compose file already uses, and the one every Postgres tool
	// reads.
	c.DatabaseURL = strings.TrimSpace(os.Getenv("DATABASE_URL"))
	c.BootstrapAdmin = strings.TrimSpace(os.Getenv("SOIREE_BOOTSTRAP_ADMIN"))
	// Deliberately not trimmed: a password's leading or trailing space is part
	// of it, and silently removing one would lock the operator out of the
	// account this variable exists to let them into.
	c.BootstrapPassword = os.Getenv("SOIREE_BOOTSTRAP_PASSWORD")

	if v := strings.TrimSpace(os.Getenv("SOIREE_TRUST_PROXY_HEADERS")); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("SOIREE_TRUST_PROXY_HEADERS must be a boolean, got %q", v)
		}
		c.TrustProxyHeaders = b
	}

	if c.BootstrapAdmin != "" {
		addr, err := mail.ParseAddress(c.BootstrapAdmin)
		if err != nil {
			return fmt.Errorf("SOIREE_BOOTSTRAP_ADMIN must be an email address, got %q", c.BootstrapAdmin)
		}
		c.BootstrapAdmin = addr.Address
	}

	if c.BootstrapPassword != "" {
		// A password with nobody to be the password of is a mistake worth
		// naming, not something to ignore: the operator believes they have
		// configured a way in and they have not.
		if c.BootstrapAdmin == "" {
			return fmt.Errorf("SOIREE_BOOTSTRAP_PASSWORD is set but SOIREE_BOOTSTRAP_ADMIN is not, so there is no account for it to belong to")
		}
		// The same floor the set-password endpoint enforces. An initial
		// password that the app would refuse from a form has no business being
		// accepted from the environment.
		if len([]rune(c.BootstrapPassword)) < MinPasswordLen {
			return fmt.Errorf("SOIREE_BOOTSTRAP_PASSWORD must be at least %d characters", MinPasswordLen)
		}
	}

	if raw := strings.TrimSpace(os.Getenv("SOIREE_BASE_URL")); raw != "" {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("SOIREE_BASE_URL must be an absolute http(s) URL, e.g. https://soiree.example.test, got %q", raw)
		}
		c.BaseURL = strings.TrimRight(u.String(), "/")
	}

	c.loadPasskeys()

	c.SMTP = SMTPConfig{
		Host:     strings.TrimSpace(os.Getenv("SOIREE_SMTP_HOST")),
		Port:     DefaultSMTPPort,
		Username: os.Getenv("SOIREE_SMTP_USER"),
		Password: os.Getenv("SOIREE_SMTP_PASSWORD"),
		From:     strings.TrimSpace(os.Getenv("SOIREE_SMTP_FROM")),
	}
	if !c.SMTP.Enabled() {
		// Half a relay is worse than none: it looks configured and silently
		// sends nothing.
		for _, f := range []struct{ name, value string }{
			{"SOIREE_SMTP_FROM", c.SMTP.From},
			{"SOIREE_SMTP_USER", c.SMTP.Username},
			{"SOIREE_SMTP_PASSWORD", c.SMTP.Password},
		} {
			if f.value != "" {
				return fmt.Errorf("%s is set but SOIREE_SMTP_HOST is not, so no mail can be sent", f.name)
			}
		}
		return nil
	}

	if v := strings.TrimSpace(os.Getenv("SOIREE_SMTP_PORT")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("SOIREE_SMTP_PORT must be a port number, got %q", v)
		}
		c.SMTP.Port = n
	}
	if c.SMTP.From == "" {
		return fmt.Errorf("SOIREE_SMTP_FROM is required when SOIREE_SMTP_HOST is set")
	}
	if _, err := mail.ParseAddress(c.SMTP.From); err != nil {
		return fmt.Errorf("SOIREE_SMTP_FROM must be an email address, got %q", c.SMTP.From)
	}
	// One without the other is always a mistake, and the failure mode is an
	// authentication error against the relay at the worst possible moment.
	if (c.SMTP.Username == "") != (c.SMTP.Password == "") {
		return fmt.Errorf("SOIREE_SMTP_USER and SOIREE_SMTP_PASSWORD must be set together")
	}
	// A mail whose link is relative is a mail that cannot be clicked.
	if c.BaseURL == "" {
		return fmt.Errorf("SOIREE_BASE_URL is required when SOIREE_SMTP_HOST is set, or the links in outgoing mail point nowhere")
	}

	return nil
}

// loadPasskeys decides whether this deployment offers passkeys, and derives the
// Relying Party identity a ceremony is bound to.
//
// It returns nothing, on purpose. Passkeys are additive — an account reached by
// a passkey can always be reached by its password, and an admin can always
// re-invite it — so a deployment that cannot offer them is not broken, it is a
// deployment with one way in instead of two. Refusing to start over that would
// turn a missing convenience into an outage, and the CI image smoke test boots
// this binary with neither a database nor a BaseURL.
//
// The default is on whenever a BaseURL is set, because unlike SMTP this needs
// no relay, no key and no account anywhere: everything it requires is already
// in the configuration. It is off when BaseURL is unset, because the RP ID
// cannot be derived from anything else. A request's Host header is not an
// alternative — see Config.PasskeyRPID.
func (c *Config) loadPasskeys() {
	// Off unless there is an origin to bind to, and then off again if the
	// operator says so. A value of true cannot turn it on without a BaseURL:
	// there would be nothing to scope the credential to.
	c.PasskeysEnabled = c.BaseURL != ""
	if v := strings.TrimSpace(os.Getenv("SOIREE_PASSKEYS_ENABLED")); v != "" {
		// A value this cannot parse reads as off rather than as an error. Off is
		// the state in which nothing is lost — password login is untouched — and
		// the alternative is a process that refuses to start over a feature it
		// could simply have declined to offer.
		b, err := strconv.ParseBool(v)
		c.PasskeysEnabled = c.PasskeysEnabled && err == nil && b
	}
	if !c.PasskeysEnabled {
		return
	}

	// BaseURL parsed cleanly above, so this cannot fail; the check is here so
	// that a later edit to the parsing above cannot turn it into a panic.
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Host == "" {
		c.PasskeysEnabled = false
		return
	}

	// Hostname() drops the port, which an RP ID must not carry: an RP ID is a
	// domain, and localhost:8080 is not one.
	host := u.Hostname()
	if host == "" || net.ParseIP(host) != nil {
		// A bare IP cannot be an RP ID — a credential is scoped to a domain and
		// an address is not one — so a deployment reached by address gets
		// password login and nothing else.
		c.PasskeysEnabled = false
		return
	}

	// The registrable domain, as far as it can be known without a public suffix
	// list. Only a leading `www.` is stripped, which is the case that actually
	// occurs here: the deployment serves an apex with `www` redirecting to it,
	// and an RP ID of the apex is a registrable domain suffix of both, so one
	// credential works at either. Nothing further is stripped, because guessing
	// at the suffix boundary is how a deployment at soiree.example.test ends up
	// registering credentials scoped to example.test — a wider scope than the
	// operator asked for, and a wrong one wherever the guess misses.
	rpID := strings.TrimPrefix(host, "www.")
	if rpID == "" {
		c.PasskeysEnabled = false
		return
	}

	c.PasskeyRPID = rpID
	// Scheme and host only. BaseURL may carry a path; an origin never does.
	c.PasskeyOrigin = u.Scheme + "://" + u.Host
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}
