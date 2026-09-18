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
	if _, err := Load(); err == nil {
		t.Error("Load() accepted the metrics listener on the public port")
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
	t.Setenv("SOIREE_EVENT_DATE", "2027-02-21T00:00:00")
	if _, err := Load(); err == nil {
		t.Fatal("expected an error for a date with no timezone offset")
	}
}

func TestEventDateNormalisedToUTC(t *testing.T) {
	t.Setenv("SOIREE_EVENT_DATE", "2027-02-21T07:00:00+07:00")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if c.EventDate != "2027-02-21T00:00:00Z" {
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
