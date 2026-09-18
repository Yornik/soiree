package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// The Relying Party ID is the field in the whole passkey feature with the
// quietest failure mode: get it wrong and every credential registered under it
// silently stops matching, with nothing more useful from the browser than "no
// credentials available". So it is derived from configuration and pinned here.

func TestPasskeyRelyingPartyIsDerivedFromBaseURL(t *testing.T) {
	for name, tc := range map[string]struct {
		baseURL string
		enabled bool
		rpID    string
		origin  string
	}{
		"an apex": {
			baseURL: "https://soiree.example.test",
			enabled: true,
			rpID:    "soiree.example.test",
			origin:  "https://soiree.example.test",
		},
		"a www host covers the apex too": {
			// The deployment serves the apex with www redirecting to it. An RP
			// ID of the apex is a registrable domain suffix of both, so one
			// credential works wherever the person lands.
			baseURL: "https://www.example.test",
			enabled: true,
			rpID:    "example.test",
			origin:  "https://www.example.test",
		},
		"a port is not part of a domain": {
			baseURL: "http://localhost:8080",
			enabled: true,
			rpID:    "localhost",
			origin:  "http://localhost:8080",
		},
		"a path is not part of an origin": {
			baseURL: "https://example.test/soiree",
			enabled: true,
			rpID:    "example.test",
			origin:  "https://example.test",
		},
		"no base url, nothing to bind to": {
			baseURL: "",
			enabled: false,
		},
		"an address is not a domain": {
			baseURL: "https://192.0.2.10:8443",
			enabled: false,
		},
		"an IPv6 address is not a domain either": {
			baseURL: "https://[2001:db8::1]:8443",
			enabled: false,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if tc.baseURL != "" {
				t.Setenv("SOIREE_BASE_URL", tc.baseURL)
			}
			c, err := Load()
			if err != nil {
				t.Fatalf("Load(): %v", err)
			}
			if c.PasskeysEnabled != tc.enabled {
				t.Fatalf("PasskeysEnabled = %v, want %v", c.PasskeysEnabled, tc.enabled)
			}
			if !tc.enabled {
				// Nothing half-derived is left behind for a later edit to pick
				// up and believe.
				if c.PasskeyRPID != "" || c.PasskeyOrigin != "" {
					t.Fatalf("passkeys are off but rpID = %q, origin = %q", c.PasskeyRPID, c.PasskeyOrigin)
				}
				return
			}
			if c.PasskeyRPID != tc.rpID {
				t.Errorf("PasskeyRPID = %q, want %q", c.PasskeyRPID, tc.rpID)
			}
			if c.PasskeyOrigin != tc.origin {
				t.Errorf("PasskeyOrigin = %q, want %q", c.PasskeyOrigin, tc.origin)
			}
		})
	}
}

// A subdomain keeps its own name. Stripping further would need a public suffix
// list, and guessing at the boundary registers credentials scoped more widely
// than the operator asked for.
func TestPasskeyRPIDDoesNotGuessAtTheRegistrableDomain(t *testing.T) {
	t.Setenv("SOIREE_BASE_URL", "https://plan.soiree.example.test")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if c.PasskeyRPID != "plan.soiree.example.test" {
		t.Errorf("PasskeyRPID = %q, want the host itself", c.PasskeyRPID)
	}
}

func TestPasskeysEnabledEnv(t *testing.T) {
	for name, tc := range map[string]struct {
		baseURL string
		value   string
		want    bool
	}{
		"off by request":                   {baseURL: "https://soiree.example.test", value: "false", want: false},
		"on by request":                    {baseURL: "https://soiree.example.test", value: "true", want: true},
		"on by default":                    {baseURL: "https://soiree.example.test", value: "", want: true},
		"cannot be turned on without a RP": {baseURL: "", value: "true", want: false},
		// A value nobody can parse reads as off. The alternative is a process
		// that refuses to start over a feature it could have declined to offer.
		"a typo reads as off": {baseURL: "https://soiree.example.test", value: "yes please", want: false},
	} {
		t.Run(name, func(t *testing.T) {
			if tc.baseURL != "" {
				t.Setenv("SOIREE_BASE_URL", tc.baseURL)
			}
			if tc.value != "" {
				t.Setenv("SOIREE_PASSKEYS_ENABLED", tc.value)
			}
			c, err := Load()
			if err != nil {
				t.Fatalf("Load(): %v", err)
			}
			if c.PasskeysEnabled != tc.want {
				t.Errorf("PasskeysEnabled = %v, want %v", c.PasskeysEnabled, tc.want)
			}
		})
	}
}

// The binary has to boot with nothing configured — that is what `docker run`
// with no arguments does, and what the image smoke test in CI checks. Asking
// for passkeys without the one thing they need must not change that.
func TestPasskeysAreNeverAStartupFailure(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"nothing at all":             {},
		"asked for, nothing to bind": {"SOIREE_PASSKEYS_ENABLED": "true"},
		"asked for, reached by IP":   {"SOIREE_PASSKEYS_ENABLED": "true", "SOIREE_BASE_URL": "http://192.0.2.10:8080"},
		"unparseable":                {"SOIREE_PASSKEYS_ENABLED": "definitely", "SOIREE_BASE_URL": "https://soiree.example.test"},
	} {
		t.Run(name, func(t *testing.T) {
			for k, v := range env {
				t.Setenv(k, v)
			}
			c, err := Load()
			if err != nil {
				t.Fatalf("Load() refused to start: %v", err)
			}
			if name != "unparseable" && c.PasskeysEnabled {
				t.Errorf("PasskeysEnabled = true with no derivable relying party")
			}
		})
	}
}

// The client config is marshalled into the page. The passkey flag is a
// capability bit and must stay one: no domain, no origin, nothing that names
// the deployment beyond what the page already announces by existing.
func TestClientConfigCarriesTheFlagAndNotTheRelyingParty(t *testing.T) {
	t.Setenv("SOIREE_BASE_URL", "https://soiree.example.test")

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
	if m["passkeys"] != true {
		t.Errorf("client config passkeys = %v, want true", m["passkeys"])
	}
	for _, leak := range []string{"soiree.example.test", "rpId", "PasskeyRPID", "baseUrl", "origin"} {
		if strings.Contains(raw, leak) {
			t.Errorf("client config carries %q: %s", leak, raw)
		}
	}
}

func TestClientConfigSaysFalseWhenPasskeysAreOff(t *testing.T) {
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
	// Present and false, not absent: the browser reads a missing key as
	// undefined, and a page that cannot tell "off" from "this build is older
	// than the feature" will guess.
	if v, ok := m["passkeys"]; !ok || v != false {
		t.Errorf("client config passkeys = %v (present %v), want false", v, ok)
	}
}
