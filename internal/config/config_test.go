package config

import (
	"encoding/json"
	"strings"
	"testing"
)

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

func TestEventDateNormalisedToUTC(t *testing.T) {
	t.Setenv("SOIREE_EVENT_DATE", "2030-01-13T05:30:00+05:30")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if c.EventDate != "2030-01-13T00:00:00Z" {
		t.Errorf("EventDate = %q, want the UTC equivalent", c.EventDate)
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
	const (
		publicKey  = "BOnlyThisHalfMayEverReachABrowser"
		privateKey = "this-private-half-is-the-signing-key"
	)
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
