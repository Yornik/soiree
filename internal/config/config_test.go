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
