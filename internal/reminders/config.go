package reminders

import (
	"fmt"
	"net/mail"
	"os"
	"strconv"
	"strings"
	"time"

	// The image is built FROM scratch, so there is no /usr/share/zoneinfo in
	// it and time.LoadLocation("Europe/Amsterdam") fails at runtime while
	// working perfectly on a developer's machine. Embedding the database costs
	// about 450 KB of binary and removes a class of bug that only ever appears
	// in production.
	_ "time/tzdata"
)

// Defaults. Weekly because that is the cadence the roadmap asks for and the
// cadence a person can absorb; a fortnight of lookahead because a decision
// deadline that first appears the week it lands leaves no time to decide.
const (
	DefaultSchedule   = 7 * 24 * time.Hour
	DefaultWindowDays = 14
)

// Config is the reminder scheduler's own configuration.
//
// It is loaded here rather than in internal/config because none of it belongs
// in ClientConfig: that struct is marshalled into the page, and a recipient
// list published to every visitor is an address book handed to a scraper.
type Config struct {
	// Enabled is the master switch and defaults to false. Mail that starts
	// sending itself because someone deployed a new version is not a feature.
	Enabled bool

	// Schedule is how wide one digest period is, and so both how often the
	// ticker fires and what counts as "the same digest" for idempotence.
	Schedule time.Duration

	// WindowDays is the lookahead, in whole days rather than a duration.
	// Deadlines are calendar days; adding 336h to a wall clock across a DST
	// boundary lands on the wrong one.
	WindowDays int

	// To is the recipients beyond the active admins the digest already goes
	// to: a deployment with no accounts, or somebody who should read it
	// without being given a login. Each of them gets a copy of their own; see
	// the package documentation.
	To []string

	// Location decides which day "today" is. Everything else about a date is
	// read from the stored calendar day and never converted.
	Location *time.Location
	Zone     string

	// EventName and Currency are borrowed from the application's own settings
	// so the digest reads like it belongs to this event.
	EventName string
	Currency  string

	// Language is what the digest and the notification are written in: the
	// primary subtag of SOIREE_LOCALE when this binary has words for it, and
	// English otherwise. The same rule an invitation follows when nobody chose
	// a language for it.
	//
	// The deployment's language rather than the reader's, because nothing
	// about language is stored against an account and a digest is composed
	// once for everybody it goes to. On a deployment whose readers do not all
	// share that language (the operating guide's Dutch half of a family on an
	// id-ID instance) this is the language they all get, which is the price of
	// the mail matching the screens it talks about.
	Language string

	// BaseURL is the origin a notification's click opens, e.g.
	// https://soiree.example.test. Empty produces a relative URL, which a
	// service worker still resolves against its own scope — so this being
	// unset costs nothing for the notification, unlike the mailed links that
	// internal/config refuses to build without it.
	BaseURL string
}

// LoadConfig reads the reminder settings from the environment.
//
// Absent settings are not errors — the whole feature is off by default, and a
// deployment that never sets any of this must start exactly as it does today.
// Malformed settings are errors, on the same principle as SOIREE_EVENT_DATE:
// SOIREE_REMINDER_SCHEDULE=fortnightly is a typo whose only other outcome is a
// digest that silently never arrives.
func LoadConfig() (Config, error) {
	c := Config{
		Schedule:   DefaultSchedule,
		WindowDays: DefaultWindowDays,
		Location:   time.UTC,
		Zone:       "UTC",
		EventName:  strings.TrimSpace(os.Getenv("SOIREE_EVENT_NAME")),
		Currency:   strings.ToUpper(strings.TrimSpace(os.Getenv("SOIREE_CURRENCY"))),
		// Not an error when it names a language this binary has no digest in:
		// SOIREE_LOCALE is a formatting locale first, it has a sensible answer
		// for every one of them, and refusing to start over fr-FR would take
		// the whole application down for a mail it can still write in English.
		Language: languageOfLocale(os.Getenv("SOIREE_LOCALE")),
		// Not validated here. internal/config has already refused to start on
		// a SOIREE_BASE_URL that is not an absolute http(s) URL, so by the
		// time this runs it is either empty or usable; the trailing slash is
		// stripped so that appending one does not produce two.
		BaseURL: strings.TrimRight(strings.TrimSpace(os.Getenv("SOIREE_BASE_URL")), "/"),
	}
	if c.Currency == "" {
		c.Currency = "EUR"
	}

	if v := strings.TrimSpace(os.Getenv("SOIREE_REMINDER_ENABLED")); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("SOIREE_REMINDER_ENABLED must be a boolean, got %q", v)
		}
		c.Enabled = b
	}

	if v := strings.TrimSpace(os.Getenv("SOIREE_REMINDER_SCHEDULE")); v != "" {
		d, err := ParseSchedule(v)
		if err != nil {
			return Config{}, err
		}
		c.Schedule = d
	}

	if v := strings.TrimSpace(os.Getenv("SOIREE_REMINDER_WINDOW_DAYS")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return Config{}, fmt.Errorf("SOIREE_REMINDER_WINDOW_DAYS must be a whole number of days, got %q", v)
		}
		c.WindowDays = n
	}

	if v := strings.TrimSpace(os.Getenv("SOIREE_REMINDER_TZ")); v != "" {
		loc, err := time.LoadLocation(v)
		if err != nil {
			return Config{}, fmt.Errorf("SOIREE_REMINDER_TZ must be an IANA timezone name (e.g. Europe/Amsterdam), got %q", v)
		}
		c.Location = loc
		c.Zone = v
	}

	to, err := ParseRecipients(os.Getenv("SOIREE_REMINDER_TO"))
	if err != nil {
		return Config{}, err
	}
	c.To = to

	return c, nil
}

// ParseSchedule reads a period, accepting the words an operator is likely to
// write as well as a Go duration.
func ParseSchedule(s string) (time.Duration, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "daily":
		return 24 * time.Hour, nil
	case "weekly":
		return 7 * 24 * time.Hour, nil
	case "fortnightly":
		return 14 * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("SOIREE_REMINDER_SCHEDULE must be daily, weekly, fortnightly or a duration like 168h, got %q", s)
	}
	// Anything shorter than a day cannot say anything new. lock_by and due are
	// date columns, so two digests on the same calendar day carry the same
	// words; the only difference between them is that the second one is the
	// point at which people stop reading.
	if d < 24*time.Hour {
		return 0, fmt.Errorf("SOIREE_REMINDER_SCHEDULE must be at least 24h — deadlines are calendar days — got %q", s)
	}
	return d, nil
}

// location is the configured zone, defaulting to UTC. A Config built by hand
// rather than by LoadConfig has a nil one, and time.Time.In(nil) panics.
func (c Config) location() *time.Location {
	if c.Location == nil {
		return time.UTC
	}
	return c.Location
}

// ParseRecipients splits an address list on commas, semicolons or newlines and
// checks each one.
//
// Not on spaces, so `Ada Lovelace <ada@example.test>, grace@example.test`
// works: a digest addressed to a name reads like mail from a person, and
// people ignore mail from a machine.
//
// Checking at load rather than at send is the point: a typo'd address found
// three weeks later, in a mail nobody received, is indistinguishable from the
// feature not working.
func ParseRecipients(s string) ([]string, error) {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n'
	})

	out := make([]string, 0, len(fields))
	seen := make(map[string]struct{}, len(fields))
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		addr, err := mail.ParseAddress(f)
		if err != nil {
			return nil, fmt.Errorf("SOIREE_REMINDER_TO contains %q, which is not an email address", f)
		}
		key := strings.ToLower(addr.Address)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, addr.String())
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}
