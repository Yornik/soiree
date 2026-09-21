package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	yaml "go.yaml.in/yaml/v3"
)

// composeEnv reads the environment the development stack hands the app.
//
// It reads the file rather than a copy of its values, because drift between
// the two is the whole thing being guarded against: compose.yaml is the
// README's first command and the one a newcomer runs, and it went several
// releases behind the accounts it has to configure.
func composeEnv(t *testing.T) map[string]string {
	t.Helper()

	path := filepath.Join("..", "..", "compose.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	var doc struct {
		Services struct {
			Soiree struct {
				Environment map[string]string `yaml:"environment"`
			} `yaml:"soiree"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if len(doc.Services.Soiree.Environment) == 0 {
		t.Fatalf("%s gives the soiree service no environment", path)
	}
	return doc.Services.Soiree.Environment
}

// TestComposeStackCanBeSignedInTo holds the development stack to the rule the
// API has. With a DSN configured every /api/v1 route needs a session, accounts
// are created by admins, and the only thing that makes the first admin is
// SOIREE_BOOTSTRAP_ADMIN. A stack that sets the DSN and not the bootstrap pair
// therefore comes up as a planner nobody can ever sign in to, which is what
// `docker compose up` produced for several releases.
func TestComposeStackCanBeSignedInTo(t *testing.T) {
	env := composeEnv(t)

	// Read from the file, not from the process: an exported variable on
	// whoever's machine runs the tests would otherwise stand in for a line
	// compose.yaml does not have.
	required := []struct{ key, why string }{
		{"DATABASE_URL", "the stack is documented as the shared planner, which is the mode a DSN turns on"},
		{"SOIREE_BOOTSTRAP_ADMIN", "nothing else creates a first account, so there would be nobody to sign in as"},
		{"SOIREE_BOOTSTRAP_PASSWORD", "without it that account is invited with no password, and the stack has no mailer to send a link"},
		{"SOIREE_BASE_URL", "set-password links and the passkey relying party are built from it"},
	}
	for _, r := range required {
		if env[r.key] == "" {
			t.Errorf("compose.yaml sets no %s: %s", r.key, r.why)
		}
	}
	if t.Failed() {
		// What follows asks whether the app accepts those values. With one
		// missing there is nothing to ask.
		return
	}

	// Present is not the same as accepted. Load refuses a bootstrap password
	// under MinPasswordLen, an address that is not one, and a base URL that is
	// not absolute http(s); a stack the binary exits on is as unreachable as
	// one with no admin in it.
	for _, r := range required {
		t.Setenv(r.key, "")
	}
	for key, value := range env {
		t.Setenv(key, value)
	}

	c, err := Load()
	if err != nil {
		t.Fatalf("Load() with the compose environment: %v", err)
	}
	if c.BootstrapAdmin == "" || c.BootstrapPassword == "" || c.BaseURL == "" {
		t.Errorf("Load() read BootstrapAdmin=%q, BaseURL=%q and a password of %d characters from compose.yaml",
			c.BootstrapAdmin, c.BaseURL, len(c.BootstrapPassword))
	}
}

// composePorts reads what each service of the development stack publishes on
// the host. It reads the file for the same reason composeEnv does: a copy of
// these values kept here would agree with itself while compose.yaml drifted.
func composePorts(t *testing.T) map[string][]string {
	t.Helper()

	path := filepath.Join("..", "..", "compose.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	// Short syntax, which is what the file uses. A long-form entry fails to
	// unmarshal here rather than passing unchecked, so whoever adds one is
	// sent to this test to say where it binds.
	var doc struct {
		Services map[string]struct {
			Ports []string `yaml:"ports"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if len(doc.Services) == 0 {
		t.Fatalf("%s defines no services", path)
	}

	ports := make(map[string][]string, len(doc.Services))
	for name, svc := range doc.Services {
		ports[name] = svc.Ports
	}
	return ports
}

// TestComposeStackIsPublishedToThisMachineOnly pins the address the stack is
// published on, which is what makes writing its password down safe. Compose's
// short "8080:8080" form binds 0.0.0.0, so an entry with no host address
// answers on every interface the machine has, including wifi it did not
// choose. Behind these ports sit an admin whose password is printed in this
// repository and a Postgres superuser called soiree/soiree. A host firewall is
// no answer either: on Linux the rules that publish a port are evaluated
// before it.
func TestComposeStackIsPublishedToThisMachineOnly(t *testing.T) {
	for service, ports := range composePorts(t) {
		for _, published := range ports {
			if !strings.HasPrefix(published, "127.0.0.1:") {
				t.Errorf("compose.yaml publishes %q of service %s on every interface; write it as \"127.0.0.1:%s\"",
					published, service, published)
			}
		}
	}
}
