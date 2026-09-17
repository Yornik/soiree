// Package config loads runtime configuration from the environment.
//
// Everything event-specific lives here rather than in the source, which is
// what lets one image serve any event without a rebuild.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the full runtime configuration.
type Config struct {
	ListenAddr string

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
}

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

	if len(c.Currency) != 3 {
		return Config{}, fmt.Errorf("SOIREE_CURRENCY must be a 3-letter ISO 4217 code, got %q", c.Currency)
	}
	if c.SecondaryCurrency != "" && len(c.SecondaryCurrency) != 3 {
		return Config{}, fmt.Errorf("SOIREE_SECONDARY_CURRENCY must be a 3-letter ISO 4217 code, got %q", c.SecondaryCurrency)
	}

	return c, nil
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}
