package main

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Yornik/soiree/internal/config"
	"github.com/Yornik/soiree/internal/httpd"
	"github.com/Yornik/soiree/internal/mailer"
	"github.com/Yornik/soiree/internal/push"
)

// These cover the seam between the accounts surface and the SMTP client, which
// is where password-reset delivery now passes through. Nothing else in this
// process would notice if it broke: a link that is never delivered looks
// exactly like a link nobody clicked, and account recovery would fail silently.

// recordingSender stands in for a relay. Opening a socket here would be testing
// somebody else's mail server.
type recordingSender struct {
	got mailer.Message
	err error
}

func (r *recordingSender) Send(_ context.Context, m mailer.Message) error {
	r.got = m
	return r.err
}

func TestAccountMailerSendsOneTextOnlyMessage(t *testing.T) {
	rec := &recordingSender{}
	var m httpd.Mailer = accountMailer{send: rec}

	const link = "https://soiree.example.test/#/set-password?token=abc"
	if err := m.Send(t.Context(), "ada@example.test", "Your account is ready",
		"An account has been created for you. Choose a password here:\n\n"+link+"\n"); err != nil {
		t.Fatalf("send: %v", err)
	}

	if len(rec.got.To) != 1 || rec.got.To[0] != "ada@example.test" {
		t.Errorf("To = %v, want the one recipient it was given", rec.got.To)
	}
	if rec.got.Subject != "Your account is ready" {
		t.Errorf("Subject = %q", rec.got.Subject)
	}
	if !strings.Contains(rec.got.Text, link) {
		t.Errorf("the link did not reach the message:\n%s", rec.got.Text)
	}
	// An HTML part would be a second copy of a credential, and the only reason
	// to render one is to make it pretty enough to be worth phishing.
	if rec.got.HTML != "" {
		t.Errorf("account mail grew an HTML part: %q", rec.got.HTML)
	}
}

// internal/mailer is kept over the deleted internal/mail precisely because it
// says whether a failed send might still have been delivered. The adapter must
// hand the error back whole, or that verdict is lost at the boundary — which is
// what would bite the next caller that keeps a sent-ledger.
func TestAccountMailerPreservesTheDeliveryVerdict(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"ambiguous": {&mailer.SendError{Err: errors.New("end of message"), Ambiguous: true}, true},
		"retryable": {&mailer.SendError{Err: errors.New("dial")}, false},
	} {
		t.Run(name, func(t *testing.T) {
			m := accountMailer{send: &recordingSender{err: tc.err}}
			err := m.Send(t.Context(), "grace@example.test", "Set a new password", "link")
			if err == nil {
				t.Fatal("expected the failure to come back")
			}
			if got := mailer.Ambiguous(err); got != tc.want {
				t.Errorf("mailer.Ambiguous = %v, want %v — the adapter flattened the error", got, tc.want)
			}
		})
	}
}

// The two structs are deliberately separate — one is the environment surface,
// the other the transport — so the mapping between them is the thing that can
// silently go wrong. In particular the default port: on a STARTTLS port a relay
// that declines the upgrade would be handed a set-password link in the clear.
func TestSMTPConfigMapsOntoTheTransport(t *testing.T) {
	got := smtpConfig(config.SMTPConfig{
		Host:     "smtp.example.test",
		Port:     587,
		Username: "soiree",
		Password: "not-a-real-key",
		From:     "Soirée <plans@example.test>",
	})
	want := mailer.Config{
		Host:     "smtp.example.test",
		Port:     587,
		Username: "soiree",
		Password: "not-a-real-key",
		From:     "Soirée <plans@example.test>",
	}
	if got != want {
		t.Errorf("smtpConfig() = %+v, want %+v", got, want)
	}
	if !got.Configured() {
		t.Error("a fully configured relay did not map to a configured mailer")
	}

	if config.DefaultSMTPPort != mailer.DefaultPort {
		t.Errorf("config.DefaultSMTPPort = %d but mailer.DefaultPort = %d; the default relay port drifted",
			config.DefaultSMTPPort, mailer.DefaultPort)
	}
}

// internal/config and internal/push read the same three SOIREE_VAPID_*
// variables independently: one publishes the public half to the browser, the
// other signs with the private half. That is the same split internal/config and
// internal/mailer already have for SMTP, and it carries the same risk — two
// readers of one setting are two chances to disagree, and this particular
// disagreement is silent. A page that offers to subscribe against a key the
// sender does not have, and a sender holding keys the page never publishes,
// both present as notifications that simply never arrive.
//
// A real pair rather than two labelled strings, because a complete set is now
// checked at startup — which also means this pins the order GenerateKeys hands
// them back in, the thing that gets transposed once and not noticed.
func TestBothReadersOfTheVAPIDKeysAgree(t *testing.T) {
	publicKey, privateKey, err := push.GenerateKeys()
	if err != nil {
		t.Fatalf("generate a VAPID pair: %v", err)
	}

	for name, env := range map[string]map[string]string{
		"nothing set": {},
		"complete": {
			"SOIREE_VAPID_PUBLIC_KEY":  publicKey,
			"SOIREE_VAPID_PRIVATE_KEY": privateKey,
			"SOIREE_VAPID_SUBJECT":     "mailto:ada@example.test",
		},
		"a public key alone": {
			"SOIREE_VAPID_PUBLIC_KEY": publicKey,
		},
		"a pair with no subject": {
			"SOIREE_VAPID_PUBLIC_KEY":  publicKey,
			"SOIREE_VAPID_PRIVATE_KEY": privateKey,
		},
	} {
		t.Run(name, func(t *testing.T) {
			// Every variable is set explicitly, including to "": the developer
			// running this may have a real pair in their shell, and a test that
			// passes only on a clean environment is not a test.
			for _, key := range []string{"SOIREE_VAPID_PUBLIC_KEY", "SOIREE_VAPID_PRIVATE_KEY", "SOIREE_VAPID_SUBJECT"} {
				t.Setenv(key, env[key])
			}

			c, err := config.Load()
			if err != nil {
				t.Fatalf("config.Load() refused to start on %s: %v", name, err)
			}
			p := push.LoadConfig()

			if c.VAPID.Enabled() != p.Configured() {
				t.Fatalf("%s: config says enabled=%v, push says configured=%v",
					name, c.VAPID.Enabled(), p.Configured())
			}
			if c.VAPID.PublicKey != p.PublicKey || c.VAPID.PrivateKey != p.PrivateKey || c.VAPID.Subject != p.Subject {
				t.Errorf("%s: the two readers disagree: config=%+v push=%+v", name, c.VAPID, p)
			}
			// Whatever the state, the public half reaches the browser exactly
			// when the sender can actually use it.
			if got := c.Client().VAPIDPublicKey; (got != "") != p.Configured() {
				t.Errorf("%s: published key = %q with a sender that is configured=%v", name, got, p.Configured())
			}
		})
	}
}

// With no SOIREE_SMTP_HOST the accounts surface must be handed a nil Mailer,
// which is what makes it respond with the set-password link for the admin to
// pass on instead of failing. A typed nil in an interface is not nil, so the
// assignment in main is guarded rather than unconditional; this pins the
// condition that guards it.
func TestNoRelayMeansNoMailer(t *testing.T) {
	if (config.SMTPConfig{}).Enabled() {
		t.Error("an empty relay configuration reported itself enabled")
	}
	if smtpConfig(config.SMTPConfig{}).Configured() {
		t.Error("an empty relay mapped to a configured mailer, so mail would be attempted and fail")
	}
}

// docs/operating.md lists "Either listener cannot bind" under "Refuses to
// start", and every other row there ends in a non-zero exit. This one did not:
// the bind failure cancelled the signal context, the ordinary shutdown ran and
// main returned 0, so `restart: on-failure`, systemd and a CI step all read a
// process that never served a request as a successful run. The announcement
// came out first as well, naming a listener that did not exist.
//
// main() is what is under test, so it runs in a child copy of this test binary.
// That is the standard way to assert on a process that ends in os.Exit.
func TestAListenerThatCannotBindStopsTheProcessWithAFailure(t *testing.T) {
	if os.Getenv("SOIREE_TEST_RUN_MAIN") == "1" {
		main()
		return
	}

	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("take a port for the child to collide with: %v", err)
	}
	defer func() { _ = occupied.Close() }()

	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locate this test binary: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, self, "-test.run="+t.Name())
	// An environment built from nothing rather than inherited: a DATABASE_URL
	// in the shell that ran the tests would make the child exit 1 on "database
	// unavailable", and this would pass without the listener ever being the
	// reason.
	child.Env = []string{
		"SOIREE_TEST_RUN_MAIN=1",
		"SOIREE_LISTEN_ADDR=" + occupied.Addr().String(),
		"SOIREE_METRICS_ADDR=127.0.0.1:0",
	}
	out, err := child.CombinedOutput()

	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Errorf("the process reported success with its port already taken (err=%v):\n%s", err, out)
	}
	if strings.Contains(string(out), "soiree listening") {
		t.Errorf("the log announced a listener that was never bound:\n%s", out)
	}
}
