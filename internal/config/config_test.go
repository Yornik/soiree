package config

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// vapidPair is a real P-256 pair in the shape the browser and the push library
// expect: the public key an uncompressed point, the private key the bare
// scalar, both base64url without padding.
//
// Generated rather than written down. A private key in a repository is a
// private key in a repository however clearly it is labelled a fixture, and
// the placeholder strings these tests used to carry are exactly what the
// startup check now refuses.
func vapidPair(t *testing.T) (publicKey, privateKey string) {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate a VAPID pair: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()),
		base64.RawURLEncoding.EncodeToString(priv.Bytes())
}

func TestLoadDefaults(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatalf("Load() with no env: %v", err)
	}
	if c.EventName != "A Celebration" {
		t.Errorf("EventName = %q, want the generic default", c.EventName)
	}
	if c.Currency != "EUR" {
		t.Errorf("Currency = %q, want EUR", c.Currency)
	}
	if c.EventDate != "" {
		t.Errorf("EventDate = %q, want empty when unset", c.EventDate)
	}
	if c.DemoData {
		t.Error("DemoData should be off unless explicitly enabled")
	}
	if c.ListenAddr != ":8080" {
		t.Errorf("ListenAddr = %q, want :8080", c.ListenAddr)
	}
	// The exposition has to land somewhere other than the public port without
	// anyone configuring it, or the default deployment is the leaky one.
	if c.MetricsAddr != ":9090" {
		t.Errorf("MetricsAddr = %q, want :9090", c.MetricsAddr)
	}
	// The policy is a defence an operator should have to switch off on
	// purpose, not one they have to know about to get.
	if c.DisableCSP {
		t.Error("DisableCSP should be off unless SOIREE_CSP says so")
	}
}

// SOIREE_CSP=off is for a deployment whose proxy sends a policy of its own.
func TestCSPCanBeTurnedOff(t *testing.T) {
	for _, v := range []string{"off", "OFF", "  off  "} {
		t.Run(v, func(t *testing.T) {
			t.Setenv("SOIREE_CSP", v)
			c, err := Load()
			if err != nil {
				t.Fatalf("Load(): %v", err)
			}
			if !c.DisableCSP {
				t.Errorf("SOIREE_CSP=%q still sends the policy", v)
			}
		})
	}

	t.Run("on", func(t *testing.T) {
		t.Setenv("SOIREE_CSP", "on")
		c, err := Load()
		if err != nil {
			t.Fatalf("Load(): %v", err)
		}
		if c.DisableCSP {
			t.Error("SOIREE_CSP=on sends no policy")
		}
	})
}

func TestMetricsAddrIsSeparateFromTheListenAddr(t *testing.T) {
	t.Setenv("SOIREE_METRICS_ADDR", "127.0.0.1:9999")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if c.MetricsAddr != "127.0.0.1:9999" {
		t.Errorf("MetricsAddr = %q", c.MetricsAddr)
	}

	// Serving both on one socket puts /metrics back on the public route, which
	// is the thing the second listener exists to prevent. It would also simply
	// fail to bind, naming a port without saying why.
	t.Setenv("SOIREE_LISTEN_ADDR", ":9999")
	t.Setenv("SOIREE_METRICS_ADDR", ":9999")
	_, err = Load()
	if err == nil {
		t.Fatal("Load() accepted the metrics listener on the public port")
	}
	// Load has a dozen error paths and this test leaves several variables set.
	// Naming the one that failed is what stops a reordering from turning this
	// into a test that passes for an unrelated reason.
	if !strings.Contains(err.Error(), "SOIREE_METRICS_ADDR") {
		t.Errorf("Load() failed for some other reason: %v", err)
	}
}

func TestLoadReadsEnv(t *testing.T) {
	t.Setenv("SOIREE_EVENT_NAME", "Ada's Retirement")
	t.Setenv("SOIREE_EVENT_TAGLINE", "Dinner and speeches")
	t.Setenv("SOIREE_CURRENCY", "sek")
	t.Setenv("SOIREE_BUDGET_CEILING", "25000")
	t.Setenv("SOIREE_DEMO_DATA", "true")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if c.EventName != "Ada's Retirement" {
		t.Errorf("EventName = %q", c.EventName)
	}
	if c.Currency != "SEK" {
		t.Errorf("Currency = %q, want upper-cased SEK", c.Currency)
	}
	if c.Ceiling != 25000 {
		t.Errorf("Ceiling = %d, want 25000", c.Ceiling)
	}
	if !c.DemoData {
		t.Error("DemoData = false, want true")
	}
}

// A date without a timezone is the bug that makes the countdown differ by a
// day between viewers in different places, so it must be rejected outright.
func TestEventDateRequiresTimezone(t *testing.T) {
	t.Setenv("SOIREE_EVENT_DATE", "2030-01-13T00:00:00")
	if _, err := Load(); err == nil {
		t.Fatal("expected an error for a date with no timezone offset")
	}
}

// The date reaches the page in the offset it was written in. Converting it to
// UTC keeps the instant and loses the day: midnight on the 13th at +09:00 is
// 15:00 on the 12th in UTC, and a page handed that announces the 12th.
func TestEventDateKeepsTheOffsetItWasWrittenIn(t *testing.T) {
	for written, want := range map[string]string{
		"2030-01-13T00:00:00+09:00": "2030-01-13T00:00:00+09:00",
		"2030-01-13T19:30:00-05:00": "2030-01-13T19:30:00-05:00",
		"2030-01-13T00:00:00Z":      "2030-01-13T00:00:00Z",
	} {
		t.Setenv("SOIREE_EVENT_DATE", written)
		c, err := Load()
		if err != nil {
			t.Fatalf("Load(%q): %v", written, err)
		}
		if c.EventDate != want {
			t.Errorf("EventDate = %q, want %q", c.EventDate, want)
		}
		if got := c.Client().EventDate; got != want {
			t.Errorf("the page is handed %q, want %q", got, want)
		}
	}
}

// The shell is served to anybody who has the URL, so a deployment can ask for
// it to name nothing. What the browser is handed then carries no event at all
// (not the name, not the tagline, not the day, not the figure the budget is
// measured against), while the signals that decide what the page can draw stay
// exactly where they were.
func TestTheEventCanBeKeptOutOfThePage(t *testing.T) {
	publicKey, privateKey := vapidPair(t)
	t.Setenv("DATABASE_URL", "postgres://soiree@db.example.test/soiree")
	t.Setenv("SOIREE_EVENT_NAME", "Ada's Retirement")
	t.Setenv("SOIREE_EVENT_TAGLINE", "Dinner and speeches")
	t.Setenv("SOIREE_EVENT_DATE", "2030-01-13T00:00:00+09:00")
	t.Setenv("SOIREE_BUDGET_CEILING", "25000")
	t.Setenv("SOIREE_VAPID_PUBLIC_KEY", publicKey)
	t.Setenv("SOIREE_VAPID_PRIVATE_KEY", privateKey)
	t.Setenv("SOIREE_VAPID_SUBJECT", "ada@example.test")
	t.Setenv("SOIREE_PUBLIC_EVENT_DETAILS", "false")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}

	raw, err := c.ClientJSON()
	if err != nil {
		t.Fatalf("ClientJSON(): %v", err)
	}
	for _, said := range []string{"Ada's Retirement", "Dinner and speeches", "2030-01-13", "25000"} {
		if strings.Contains(raw, said) {
			t.Errorf("the page still tells every visitor %q: %s", said, raw)
		}
	}

	cc := c.Client()
	if cc.Currency != "EUR" || cc.Locale != "en-US" {
		t.Errorf("the deployment's own settings went with the event: %+v", cc)
	}
	if cc.VAPIDPublicKey != publicKey {
		t.Error("the push key went with the event, so nothing can subscribe")
	}

	// The shell and the manifest are one rendering for everybody, so they are
	// titled with the product rather than with the event.
	if c.ShellEventName() != "soiree" || c.ShellTagline() != "" {
		t.Errorf("the shell is titled %q / %q, want it to name nobody", c.ShellEventName(), c.ShellTagline())
	}

	// And the page is told whose evening it is once it has a session.
	ev := c.SessionEventDetails()
	if ev == nil {
		t.Fatal("nothing is sent on the session, so a signed-in page never learns the date")
	}
	if ev.Name != "Ada's Retirement" || ev.Tagline != "Dinner and speeches" || ev.Date != "2030-01-13T00:00:00+09:00" {
		t.Errorf("the session carries %+v, want the event as configured", *ev)
	}
}

// The documented default, which every deployment before this switch existed
// has: the page names its event, and a session adds nothing to it.
func TestTheEventIsInThePageByDefault(t *testing.T) {
	t.Setenv("SOIREE_EVENT_NAME", "Ada's Retirement")
	t.Setenv("SOIREE_EVENT_TAGLINE", "Dinner and speeches")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if c.Client().EventName != "Ada's Retirement" || c.ShellEventName() != "Ada's Retirement" {
		t.Error("the default deployment stopped naming its event")
	}
	if c.ShellTagline() != "Dinner and speeches" {
		t.Errorf("ShellTagline() = %q, want the configured tagline", c.ShellTagline())
	}
	if c.SessionEventDetails() != nil {
		t.Error("the session repeats what the page already says")
	}
}

// Asking for it where it cannot be carried out is refused rather than ignored.
// With no database nobody signs in, so there is no session to put the details
// on: a page that did not get them from its shell would have no name and no
// countdown for ever.
func TestKeepingTheEventOutOfThePageNeedsADatabase(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("SOIREE_PUBLIC_EVENT_DETAILS", "false")

	_, err := Load()
	if err == nil {
		t.Fatal("accepted with no database, so the page would never learn the event")
	}
	if !strings.Contains(err.Error(), "SOIREE_PUBLIC_EVENT_DETAILS") || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Errorf("Load() failed for some other reason: %v", err)
	}
}

func TestInvalidValuesRejected(t *testing.T) {
	cases := []struct {
		name, key, val string
	}{
		{"currency too short", "SOIREE_CURRENCY", "EU"},
		{"secondary currency too long", "SOIREE_SECONDARY_CURRENCY", "EURO"},
		{"ceiling not a number", "SOIREE_BUDGET_CEILING", "lots"},
		{"ceiling negative", "SOIREE_BUDGET_CEILING", "-5"},
		{"demo data not a bool", "SOIREE_DEMO_DATA", "yes please"},
		{"date unparseable", "SOIREE_EVENT_DATE", "next spring"},
		{"csp neither on nor off", "SOIREE_CSP", "report-only"},
		{"public event details not a bool", "SOIREE_PUBLIC_EVENT_DETAILS", "private"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.key, tc.val)
			if _, err := Load(); err == nil {
				t.Fatalf("%s=%q was accepted, want an error", tc.key, tc.val)
			}
		})
	}
}

func TestSecondaryLocaleFallsBackToPrimary(t *testing.T) {
	t.Setenv("SOIREE_LOCALE", "nl-NL")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if c.SecondaryLocale != "nl-NL" {
		t.Errorf("SecondaryLocale = %q, want it to default to the primary locale", c.SecondaryLocale)
	}
}

// A missing DATABASE_URL is a supported configuration, not an error: with no
// DSN the binary serves the frontend alone, which is what a bare `docker run`
// with no database does and what the image smoke test in CI depends on.
func TestDatabaseURLIsOptional(t *testing.T) {
	t.Setenv("DATABASE_URL", "")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load() with no DATABASE_URL: %v", err)
	}
	if c.DatabaseURL != "" {
		t.Errorf("DatabaseURL = %q, want empty when unset", c.DatabaseURL)
	}
}

func TestDatabaseURLIsReadAndNeverPublished(t *testing.T) {
	const dsn = "postgres://soiree:soiree@db.example.test:5432/soiree?sslmode=require"
	t.Setenv("DATABASE_URL", "  "+dsn+"  ")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if c.DatabaseURL != dsn {
		t.Errorf("DatabaseURL = %q, want it trimmed to %q", c.DatabaseURL, dsn)
	}

	// The client config is embedded in the page. A DSN carries a password.
	raw, err := c.ClientJSON()
	if err != nil {
		t.Fatalf("ClientJSON(): %v", err)
	}
	for _, leak := range []string{dsn, "db.example.test", "soiree:soiree"} {
		if strings.Contains(raw, leak) {
			t.Fatalf("the client config leaks the database credentials: %s", raw)
		}
	}
}

// The client payload is embedded in the page, so it must stay free of
// anything that is not meant to be public.
func TestClientJSONShape(t *testing.T) {
	t.Setenv("SOIREE_EVENT_NAME", "Test")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	raw, err := c.ClientJSON()
	if err != nil {
		t.Fatalf("ClientJSON(): %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("ClientJSON is not valid JSON: %v", err)
	}
	if _, ok := m["listenAddr"]; ok {
		t.Error("client config leaks the listen address")
	}
	if strings.Contains(raw, "ListenAddr") {
		t.Error("client config leaks server-only fields")
	}
}

func TestAccountsConfigDefaultsToNoMailAndNoDatabase(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatalf("Load() with no env: %v", err)
	}
	// A process with neither is the documented degraded deployment: it serves
	// the shell, which is what a bare `docker run` and the image smoke test do.
	if c.DatabaseURL != "" {
		t.Errorf("DatabaseURL = %q, want empty when unset", c.DatabaseURL)
	}
	if c.SMTP.Enabled() {
		t.Error("SMTP reports itself enabled with nothing configured")
	}
	if c.TrustProxyHeaders {
		t.Error("X-Forwarded-For is trusted by default, so anyone can choose their own rate-limit bucket")
	}
}

func TestAccountsConfigReadsEnv(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://soiree@localhost/soiree")
	t.Setenv("SOIREE_BASE_URL", "https://soiree.example.test/")
	t.Setenv("SOIREE_TRUST_PROXY_HEADERS", "true")
	t.Setenv("SOIREE_BOOTSTRAP_ADMIN", "Ada <ada@example.test>")
	t.Setenv("SOIREE_SMTP_HOST", "smtp.example.test")
	t.Setenv("SOIREE_SMTP_USER", "resend")
	t.Setenv("SOIREE_SMTP_PASSWORD", "not-a-real-key")
	t.Setenv("SOIREE_SMTP_FROM", "soiree@example.test")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	// Trailing slash stripped, or every link built from it has two.
	if c.BaseURL != "https://soiree.example.test" {
		t.Errorf("BaseURL = %q, want the trailing slash gone", c.BaseURL)
	}
	if c.BootstrapAdmin != "ada@example.test" {
		t.Errorf("BootstrapAdmin = %q, want the bare address", c.BootstrapAdmin)
	}
	if !c.TrustProxyHeaders {
		t.Error("SOIREE_TRUST_PROXY_HEADERS=true was not read")
	}
	if !c.SMTP.Enabled() || c.SMTP.Port != DefaultSMTPPort {
		t.Errorf("SMTP = %+v, want enabled on the default implicit-TLS port", c.SMTP)
	}

	// The relay's password is a secret, and the browser gets this struct.
	raw, err := c.ClientJSON()
	if err != nil {
		t.Fatalf("ClientJSON(): %v", err)
	}
	for _, secret := range []string{"not-a-real-key", "smtp.example.test", "postgres://", "resend"} {
		if strings.Contains(raw, secret) {
			t.Errorf("client config leaks %q", secret)
		}
	}
}

// Push is off unless all three VAPID variables are set, and a deployment that
// sets none of them must start exactly as it does today — which is what a bare
// `docker run` and the image smoke test in CI do.
func TestPushIsOffWhenUnconfigured(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatalf("Load() with no VAPID variables: %v", err)
	}
	if c.VAPID.Enabled() {
		t.Error("push reports itself enabled with nothing configured")
	}
	if c.Client().VAPIDPublicKey != "" {
		t.Error("an unconfigured deployment publishes a push key")
	}

	raw, err := c.ClientJSON()
	if err != nil {
		t.Fatalf("ClientJSON(): %v", err)
	}
	// Absent, not present-and-empty: "is there a key?" is the single question
	// the client asks before offering to turn notifications on.
	if strings.Contains(raw, "vapidPublicKey") {
		t.Errorf("the client config carries a push key field with push off: %s", raw)
	}
}

// A half-configured pair is never a startup failure — but it must not reach
// the browser either, or the page offers to subscribe against a key the server
// cannot send with, and every notification after that is silently lost.
//
// Still true now that a complete set is checked against itself: what is
// missing is not what is wrong, and these values are deliberately nonsense.
func TestHalfConfiguredPushNeitherFailsNorPublishes(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"a public key alone": {
			"SOIREE_VAPID_PUBLIC_KEY": "BPublicKeyThatIsNotHalfOfAnythingUsable",
		},
		"a pair with no subject": {
			"SOIREE_VAPID_PUBLIC_KEY":  "BPublicKeyThatIsNotHalfOfAnythingUsable",
			"SOIREE_VAPID_PRIVATE_KEY": "a-private-key-for-a-test",
		},
		"a subject alone": {
			"SOIREE_VAPID_SUBJECT": "mailto:ada@example.test",
		},
	} {
		t.Run(name, func(t *testing.T) {
			for k, v := range env {
				t.Setenv(k, v)
			}
			c, err := Load()
			if err != nil {
				t.Fatalf("Load() refused to start on %s: %v", name, err)
			}
			if c.VAPID.Enabled() {
				t.Errorf("%s reports push as usable", name)
			}
			if c.Client().VAPIDPublicKey != "" {
				t.Errorf("%s published a public key the server cannot send with", name)
			}
		})
	}
}

// The public key is meant to reach the page — a browser that has not seen it
// cannot subscribe at all. The private key is a signing key and must never get
// anywhere near it.
func TestPushKeysAreReadAndOnlyThePublicOneIsPublished(t *testing.T) {
	publicKey, privateKey := vapidPair(t)
	t.Setenv("SOIREE_VAPID_PUBLIC_KEY", "  "+publicKey+"  ")
	t.Setenv("SOIREE_VAPID_PRIVATE_KEY", privateKey)
	t.Setenv("SOIREE_VAPID_SUBJECT", "mailto:ada@example.test")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if c.VAPID.PublicKey != publicKey {
		t.Errorf("PublicKey = %q, want it trimmed to %q", c.VAPID.PublicKey, publicKey)
	}
	if c.VAPID.PrivateKey != privateKey {
		t.Errorf("PrivateKey = %q, want it read", c.VAPID.PrivateKey)
	}
	if !c.VAPID.Enabled() {
		t.Error("a complete VAPID configuration does not report itself enabled")
	}

	raw, err := c.ClientJSON()
	if err != nil {
		t.Fatalf("ClientJSON(): %v", err)
	}
	if !strings.Contains(raw, publicKey) {
		t.Errorf("the public key never reaches the browser, so nothing can subscribe: %s", raw)
	}
	if strings.Contains(raw, privateKey) {
		t.Fatalf("the client config leaks the VAPID private key: %s", raw)
	}

	// The struct, not just its rendering: a field added to ClientConfig later
	// must not be able to smuggle the private half in under another name.
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("ClientJSON is not valid JSON: %v", err)
	}
	for key, value := range m {
		if s, ok := value.(string); ok && s == privateKey {
			t.Errorf("the client config publishes the private key as %q", key)
		}
	}
}

// Three values that are present but do not form a pair are a different matter
// from a half-configured set, because they do reach the browser.
//
// Transposing the two variables is the mistake docs/operating.md warns about,
// and it publishes the signing key in the config block of a page served to
// anyone. Nothing afterwards reports it: a browser refuses to subscribe against
// a 32-byte applicationServerKey, so no subscription exists, no digest is ever
// sent, and no line appears in any log. The other mistakes fail at the first
// weekly send instead — a week after the permission prompt was spent.
func TestACompleteButUnusableVAPIDSetRefusesToStart(t *testing.T) {
	publicKey, privateKey := vapidPair(t)
	strangerPublic, _ := vapidPair(t)

	for name, pair := range map[string][2]string{
		"transposed":                       {privateKey, publicKey},
		"a public key from another pair":   {strangerPublic, privateKey},
		"a truncated public key":           {publicKey[:40], privateKey},
		"a private key that is not base64": {publicKey, "not a key at all"},
		"a private key of the wrong length": {publicKey,
			base64.RawURLEncoding.EncodeToString(make([]byte, 31))},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("SOIREE_VAPID_PUBLIC_KEY", pair[0])
			t.Setenv("SOIREE_VAPID_PRIVATE_KEY", pair[1])
			t.Setenv("SOIREE_VAPID_SUBJECT", "mailto:ada@example.test")

			c, err := Load()
			if err == nil {
				if c.Client().VAPIDPublicKey == privateKey {
					t.Fatal("started with the two variables transposed, publishing the signing key to anyone who asks for the page")
				}
				t.Fatal("started with a VAPID set that cannot work")
			}
			// This message goes to stdout and into the cluster's log pipeline,
			// and in the transposed case the variable it is about holds the
			// signing key. It may name variables and lengths, never a value.
			for _, secret := range []string{privateKey, publicKey} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("the refusal prints key material: %v", err)
				}
			}
		})
	}
}

// webpush-go decodes a key as padded base64url first and raw second, so a pair
// that was written down with padding sends perfectly well. The check that reads
// the same two variables has to accept exactly what the sender accepts, or it
// refuses a deployment that works.
func TestAPaddedVAPIDPairIsAccepted(t *testing.T) {
	publicKey, privateKey := vapidPair(t)
	repad := func(raw string) string {
		b, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil {
			t.Fatalf("decode the generated key: %v", err)
		}
		return base64.URLEncoding.EncodeToString(b)
	}

	t.Setenv("SOIREE_VAPID_PUBLIC_KEY", repad(publicKey))
	t.Setenv("SOIREE_VAPID_PRIVATE_KEY", repad(privateKey))
	t.Setenv("SOIREE_VAPID_SUBJECT", "mailto:ada@example.test")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load() refused a padded pair: %v", err)
	}
	if !c.VAPID.Enabled() {
		t.Error("a padded pair does not report itself enabled")
	}
}

func TestAccountsConfigRejectsHalfConfigurations(t *testing.T) {
	// Each of these otherwise fails days later, as a mail that never arrives
	// or a link that goes nowhere, to somebody who cannot see the logs.
	for name, env := range map[string]map[string]string{
		"a relay with no sender": {
			"SOIREE_SMTP_HOST": "smtp.example.test",
			"SOIREE_BASE_URL":  "https://soiree.example.test",
		},
		"a sender with no relay": {
			"SOIREE_SMTP_FROM": "soiree@example.test",
		},
		"a relay password with no relay": {
			"SOIREE_SMTP_PASSWORD": "not-a-real-key",
		},
		"a user with no password": {
			"SOIREE_SMTP_HOST": "smtp.example.test",
			"SOIREE_SMTP_FROM": "soiree@example.test",
			"SOIREE_SMTP_USER": "resend",
			"SOIREE_BASE_URL":  "https://soiree.example.test",
		},
		"mail with no base url": {
			"SOIREE_SMTP_HOST": "smtp.example.test",
			"SOIREE_SMTP_FROM": "soiree@example.test",
		},
		"a relative base url": {
			"SOIREE_BASE_URL": "/soiree",
		},
		"a base url with no scheme": {
			"SOIREE_BASE_URL": "soiree.example.test",
		},
		"a port that is not a number": {
			"SOIREE_SMTP_HOST": "smtp.example.test",
			"SOIREE_SMTP_FROM": "soiree@example.test",
			"SOIREE_SMTP_PORT": "smtps",
			"SOIREE_BASE_URL":  "https://soiree.example.test",
		},
		"a sender that is not an address": {
			"SOIREE_SMTP_HOST": "smtp.example.test",
			"SOIREE_SMTP_FROM": "soiree at example dot test",
			"SOIREE_BASE_URL":  "https://soiree.example.test",
		},
		"a bootstrap admin that is not an address": {
			"SOIREE_BOOTSTRAP_ADMIN": "ada",
		},
		"a non-boolean proxy setting": {
			"SOIREE_TRUST_PROXY_HEADERS": "sometimes",
		},
	} {
		t.Run(name, func(t *testing.T) {
			for k, v := range env {
				t.Setenv(k, v)
			}
			if _, err := Load(); err == nil {
				t.Errorf("Load() accepted %s", name)
			}
		})
	}
}

// The table above only asks that Load fails. A relay password is the last of
// the three that has no relay of its own to belong to, so this one also checks
// which variable the refusal names: without that, a reordering that made
// something else fatal would keep this passing for the wrong reason.
func TestSMTPPasswordNeedsARelay(t *testing.T) {
	t.Setenv("SOIREE_SMTP_PASSWORD", "not-a-real-key")
	_, err := Load()
	if err == nil {
		t.Fatal("Load() accepted a relay password with no SOIREE_SMTP_HOST")
	}
	if !strings.Contains(err.Error(), "SOIREE_SMTP_PASSWORD") {
		t.Errorf("Load() failed for some other reason: %v", err)
	}
}

// A bootstrap password with no bootstrap admin is a misconfiguration worth
// naming: the operator believes they have configured a way in, and they have
// not.
func TestBootstrapPasswordNeedsAnAdmin(t *testing.T) {
	t.Setenv("SOIREE_BOOTSTRAP_PASSWORD", "correct horse battery")
	if _, err := Load(); err == nil {
		t.Fatal("expected an error when a bootstrap password names no account")
	}
}

// The environment must not be a way around the password floor the HTTP
// surface enforces.
func TestBootstrapPasswordHasAFloor(t *testing.T) {
	t.Setenv("SOIREE_BOOTSTRAP_ADMIN", "ada@example.test")
	t.Setenv("SOIREE_BOOTSTRAP_PASSWORD", "short")
	if _, err := Load(); err == nil {
		t.Fatalf("a %d-character password was accepted", len("short"))
	}
}

// A floor is a number and a way of counting, and the way of counting is the
// half that drifts unnoticed: measured in bytes, four characters clear a floor
// of twelve that the same four characters miss when counted the way the person
// choosing them counts.
func TestPasswordFloorCountsCharactersNotBytes(t *testing.T) {
	// Four characters in a script that takes three bytes each. The check
	// keeps the case honest: edited to something else, it would stop telling
	// the two measures apart and would pass either way.
	const twelveBytes = "秘密の鍵"
	if len(twelveBytes) != MinPasswordLen || utf8.RuneCountInString(twelveBytes) >= MinPasswordLen {
		t.Fatalf("%q is %d bytes and %d characters; the case no longer tells the two apart",
			twelveBytes, len(twelveBytes), utf8.RuneCountInString(twelveBytes))
	}

	for _, tc := range []struct {
		name  string
		pass  string
		short bool
	}{
		{"twelve ascii characters", "twelve chars", false},
		{"eleven ascii characters", "eleven char", true},
		{"four three-byte characters", twelveBytes, true},
		{"twelve accented characters", strings.Repeat("é", MinPasswordLen), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := PasswordTooShort(tc.pass); got != tc.short {
				t.Errorf("PasswordTooShort(%q) = %v, want %v (%d bytes, %d characters)",
					tc.pass, got, tc.short, len(tc.pass), utf8.RuneCountInString(tc.pass))
			}
		})
	}
}

// And the environment is held to the measure, not only to the number.
func TestBootstrapPasswordFloorCountsCharacters(t *testing.T) {
	t.Setenv("SOIREE_BOOTSTRAP_ADMIN", "ada@example.test")
	t.Setenv("SOIREE_BOOTSTRAP_PASSWORD", "秘密の鍵")
	if _, err := Load(); err == nil {
		t.Fatal("a four-character password was accepted for being twelve bytes")
	}
}

func TestBootstrapPasswordAccepted(t *testing.T) {
	t.Setenv("SOIREE_BOOTSTRAP_ADMIN", "Ada <ada@example.test>")
	t.Setenv("SOIREE_BOOTSTRAP_PASSWORD", "correct horse battery staple")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if c.BootstrapAdmin != "ada@example.test" {
		t.Errorf("BootstrapAdmin = %q, want the bare address", c.BootstrapAdmin)
	}
	if c.BootstrapPassword != "correct horse battery staple" {
		t.Error("BootstrapPassword did not survive Load")
	}
	// It is a credential; the page must never carry it.
	raw, err := c.ClientJSON()
	if err != nil {
		t.Fatalf("ClientJSON(): %v", err)
	}
	if strings.Contains(raw, "correct horse") || strings.Contains(raw, "ada@example.test") {
		t.Fatalf("the client config leaks the bootstrap credentials: %s", raw)
	}
}

// Surrounding whitespace is part of a password. Trimming it would lock the
// operator out of the account the variable exists to let them into.
func TestBootstrapPasswordKeepsItsSpaces(t *testing.T) {
	t.Setenv("SOIREE_BOOTSTRAP_ADMIN", "ada@example.test")
	t.Setenv("SOIREE_BOOTSTRAP_PASSWORD", "  padded password  ")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if c.BootstrapPassword != "  padded password  " {
		t.Errorf("BootstrapPassword = %q, want the spaces kept", c.BootstrapPassword)
	}
}

func setBucket(t *testing.T) {
	t.Helper()
	t.Setenv("SOIREE_S3_ENDPOINT", "https://s3.example.test")
	t.Setenv("SOIREE_S3_REGION", "nbg1")
	t.Setenv("SOIREE_S3_BUCKET", "files")
	t.Setenv("SOIREE_S3_ACCESS_KEY_ID", "id")
	t.Setenv("SOIREE_S3_SECRET_ACCESS_KEY", "secret")
}

// With nothing set the binary still starts, attachments are off, and the page
// is told nothing about them: the same rule as mail and push, and what a bare
// `docker run` depends on.
func TestAttachmentsAreOffByDefault(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Attachments.Enabled() {
		t.Error("attachments are on with no bucket configured")
	}
	if c.Client().Attachments != nil {
		t.Error("the page is told about attachments that do not exist")
	}
	if c.Attachments.MaxBytes != 25<<20 || c.Attachments.TotalBytes != 2048<<20 {
		t.Errorf("default limits = %d / %d", c.Attachments.MaxBytes, c.Attachments.TotalBytes)
	}
}

// A bucket with no secret would start, draw the control, and fail every upload
// in somebody's hand. It has to fail here instead, and say which variable.
func TestAHalfConfiguredBucketIsRefused(t *testing.T) {
	setBucket(t)
	t.Setenv("SOIREE_S3_SECRET_ACCESS_KEY", "")
	_, err := Load()
	if err == nil {
		t.Fatal("a bucket with no secret was accepted")
	}
	if msg := err.Error(); !strings.Contains(msg, "SOIREE_S3_SECRET_ACCESS_KEY missing") {
		t.Errorf("the error does not name what is missing: %v", err)
	}
}

// The page learns the limit, and only on a deployment where an upload could
// work: a file's record is a database row, so a bucket alone is not enough.
func TestThePageIsToldTheLimitOnlyWhenUploadsCanWork(t *testing.T) {
	setBucket(t)
	t.Setenv("SOIREE_ATTACHMENT_MAX_MB", "10")

	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !c.Attachments.Enabled() {
		t.Fatal("attachments are off with a whole bucket configured")
	}
	if c.Client().Attachments != nil {
		t.Error("no database, and the page is still offered attachments")
	}

	c.DatabaseURL = "postgres://example.test/soiree"
	got := c.Client().Attachments
	if got == nil || got.MaxBytes != 10<<20 {
		t.Fatalf("client attachments = %+v, want maxBytes %d", got, 10<<20)
	}

	// And nothing that identifies the bucket reaches the page.
	js, err := c.ClientJSON()
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"s3.example.test", "files", "secret", "nbg1"} {
		if strings.Contains(js, leak) {
			t.Errorf("client config contains %q: %s", leak, js)
		}
	}
}

func TestAttachmentLimitsAreValidated(t *testing.T) {
	for name, env := range map[string][2]string{
		"not a number":          {"SOIREE_ATTACHMENT_MAX_MB", "lots"},
		"zero":                  {"SOIREE_ATTACHMENT_MAX_MB", "0"},
		"negative total":        {"SOIREE_ATTACHMENTS_TOTAL_MB", "-1"},
		"one file over the lot": {"SOIREE_ATTACHMENT_MAX_MB", "4096"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(env[0], env[1])
			if _, err := Load(); err == nil {
				t.Errorf("%s=%s was accepted", env[0], env[1])
			}
		})
	}
}
