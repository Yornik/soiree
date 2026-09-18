// Package config loads runtime configuration from the environment.
//
// Everything event-specific lives here rather than in the source, which is
// what lets one image serve any event without a rebuild.
package config

import (
	"encoding/json"
	"fmt"
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
	EventDate string // RFC3339, UTC. Empty means no countdown.

	Currency          string
	Locale            string
	SecondaryCurrency string
	SecondaryLocale   string

	Ceiling  int64
	DemoData bool

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
	}
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
			return Config{}, fmt.Errorf("SOIREE_EVENT_DATE must be RFC3339 with a timezone (e.g. 2027-02-21T00:00:00Z), got %q", c.EventDate)
		}
		c.EventDate = t.UTC().Format(time.RFC3339)
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

	return c, nil
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

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}
