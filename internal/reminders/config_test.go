package reminders

import (
	"testing"
	"time"
)

// clearEnv blanks every setting this package reads, so a test never inherits
// one from the machine it runs on.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"SOIREE_REMINDER_ENABLED", "SOIREE_REMINDER_SCHEDULE", "SOIREE_REMINDER_WINDOW_DAYS",
		"SOIREE_REMINDER_TO", "SOIREE_REMINDER_TZ", "SOIREE_EVENT_NAME", "SOIREE_CURRENCY",
		// Both of these reach the rendered mail, so a machine that has them
		// set would otherwise write its own language and its own origin into
		// what these tests read.
		"SOIREE_LOCALE", "SOIREE_BASE_URL",
	} {
		t.Setenv(k, "")
	}
}

// Nothing set means nothing sent. Mail that starts arriving because somebody
// deployed a new version is not a feature.
func TestRemindersAreOffByDefault(t *testing.T) {
	clearEnv(t)

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("an empty environment must not be an error: %v", err)
	}
	if cfg.Enabled {
		t.Error("reminders are on with nothing configured")
	}
	if cfg.Schedule != 7*24*time.Hour {
		t.Errorf("schedule = %s, want a week", cfg.Schedule)
	}
	if cfg.WindowDays != DefaultWindowDays {
		t.Errorf("window = %d days, want %d", cfg.WindowDays, DefaultWindowDays)
	}
	if cfg.Location != time.UTC {
		t.Errorf("location = %v, want UTC", cfg.Location)
	}
	if len(cfg.To) != 0 {
		t.Errorf("recipients = %v, want none", cfg.To)
	}
	if cfg.Language != fallbackLanguage {
		t.Errorf("language = %q, want %q", cfg.Language, fallbackLanguage)
	}
}

// The digest is written in the deployment's language, read off the same
// variable and by the same rule as the invitation nobody chose a language for.
func TestTheLocaleChoosesTheDigestLanguage(t *testing.T) {
	for locale, want := range map[string]string{
		"nl-NL": "nl",
		"nl":    "nl",
		"id_ID": "id",
		"en-GB": "en",
		// A locale this binary has no digest in is not a startup failure: it
		// is a formatting locale first, and English is a readable answer.
		"fr-FR":     "en",
		"gibberish": "en",
	} {
		clearEnv(t)
		t.Setenv("SOIREE_LOCALE", locale)

		cfg, err := LoadConfig()
		if err != nil {
			t.Fatalf("SOIREE_LOCALE=%q: %v", locale, err)
		}
		if cfg.Language != want {
			t.Errorf("SOIREE_LOCALE=%q gives language %q, want %q", locale, cfg.Language, want)
		}
	}
}

func TestScheduleAcceptsWordsAndDurations(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"daily":       24 * time.Hour,
		"weekly":      7 * 24 * time.Hour,
		"WEEKLY":      7 * 24 * time.Hour,
		"fortnightly": 14 * 24 * time.Hour,
		"168h":        7 * 24 * time.Hour,
		"72h30m":      72*time.Hour + 30*time.Minute,
	} {
		got, err := ParseSchedule(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("%q = %s, want %s", in, got, want)
		}
	}

	// Anything shorter than a day cannot say anything new, because the dates
	// it reports are calendar days.
	for _, in := range []string{"fortnighty", "", "5m", "6h", "hourly", "-24h", "every week"} {
		if _, err := ParseSchedule(in); err == nil {
			t.Errorf("%q was accepted as a schedule", in)
		}
	}
}

func TestRecipientsParsing(t *testing.T) {
	got, err := ParseRecipients("ada@example.test, Grace Hopper <grace@example.test>;\nADA@example.test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []string{"<ada@example.test>", `"Grace Hopper" <grace@example.test>`}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %d addresses", got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("recipient %d = %q, want %q", i, got[i], want[i])
		}
	}

	if _, err := ParseRecipients("ada at example.test"); err == nil {
		t.Error("a malformed address was accepted")
	}
	// Caught at load rather than at send: an address typo found three weeks
	// later, in a mail nobody got, looks exactly like the feature not working.
	if _, err := ParseRecipients("ada@example.test\r\nBcc: eve@example.test"); err == nil {
		t.Error("an address carrying a header break was accepted")
	}
}

func TestLoadConfigReadsTheEnvironment(t *testing.T) {
	clearEnv(t)
	t.Setenv("SOIREE_REMINDER_ENABLED", "true")
	t.Setenv("SOIREE_REMINDER_SCHEDULE", "daily")
	t.Setenv("SOIREE_REMINDER_WINDOW_DAYS", "21")
	t.Setenv("SOIREE_REMINDER_TZ", "Europe/Amsterdam")
	t.Setenv("SOIREE_REMINDER_TO", "ada@example.test,grace@example.test")
	t.Setenv("SOIREE_EVENT_NAME", "Ada's Leaving Do")
	t.Setenv("SOIREE_CURRENCY", "idr")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.Enabled || cfg.Schedule != 24*time.Hour || cfg.WindowDays != 21 {
		t.Errorf("config = %+v", cfg)
	}
	if cfg.Zone != "Europe/Amsterdam" || cfg.Location.String() != "Europe/Amsterdam" {
		t.Errorf("zone = %q / %v", cfg.Zone, cfg.Location)
	}
	if len(cfg.To) != 2 {
		t.Errorf("recipients = %v, want 2", cfg.To)
	}
	if cfg.EventName != "Ada's Leaving Do" || cfg.Currency != "IDR" {
		t.Errorf("event = %q, currency = %q", cfg.EventName, cfg.Currency)
	}
}

// A timezone name is validated here rather than at the first send, a week
// later, in a goroutine nobody is watching. The embedded tzdata is what makes
// this work in a FROM-scratch image at all.
func TestLoadConfigRejectsMalformedSettings(t *testing.T) {
	for name, env := range map[string][2]string{
		"enabled":  {"SOIREE_REMINDER_ENABLED", "yes please"},
		"schedule": {"SOIREE_REMINDER_SCHEDULE", "fortnighty"},
		"window":   {"SOIREE_REMINDER_WINDOW_DAYS", "a fortnight"},
		"negative": {"SOIREE_REMINDER_WINDOW_DAYS", "-1"},
		"timezone": {"SOIREE_REMINDER_TZ", "Europe/Amsterdaam"},
		"to":       {"SOIREE_REMINDER_TO", "ada at example.test"},
	} {
		t.Run(name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv(env[0], env[1])
			if _, err := LoadConfig(); err == nil {
				t.Errorf("%s=%q was accepted", env[0], env[1])
			}
		})
	}
}

func TestZoneDatabaseIsEmbedded(t *testing.T) {
	// The shipped image is FROM scratch and has no /usr/share/zoneinfo. If
	// this ever fails in CI it will already have failed in production.
	if _, err := time.LoadLocation("Asia/Bangkok"); err != nil {
		t.Fatalf("no embedded zone database: %v", err)
	}
}
