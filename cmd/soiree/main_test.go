package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
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

// A shutdown can take a while: http.Server.Shutdown waits for connections to go
// idle, and the deferred reminder stop that runs after it waits for a digest
// already in flight. The signal context stayed armed through all of that, so
// the second Ctrl-C of somebody watching a shutdown that is going nowhere did
// nothing at all — os/signal keeps a signal captured until its stop function
// runs, and this one only ran on the way out of main.
//
// main() is what is under test, so it runs in a child copy of this test binary,
// the same way the bind test above does.
func TestASecondSignalStopsAShutdownThatIsTakingTooLong(t *testing.T) {
	if os.Getenv("SOIREE_TEST_RUN_MAIN") == "1" {
		main()
		return
	}

	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locate this test binary: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, self, "-test.run="+t.Name())
	// Built from nothing rather than inherited, for the reason the bind test
	// gives. Both listeners ask for port 0 and the two strings still differ,
	// which is the comparison config makes.
	child.Env = []string{
		"SOIREE_TEST_RUN_MAIN=1",
		"SOIREE_LISTEN_ADDR=127.0.0.1:0",
		"SOIREE_METRICS_ADDR=localhost:0",
	}
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatalf("read the child's log: %v", err)
	}
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		t.Fatalf("start the child: %v", err)
	}
	defer func() { _ = child.Process.Kill() }()

	scanner := bufio.NewScanner(stdout)
	var seen strings.Builder
	// The log is the only thing this process says about where it has got to, so
	// it is also how this test waits: no sleep long enough to go flaky under
	// load, and a failure carries what the child actually said.
	awaitLog := func(msg string) map[string]any {
		t.Helper()
		for scanner.Scan() {
			line := scanner.Text()
			seen.WriteString(line + "\n")
			var entry map[string]any
			if err := json.Unmarshal([]byte(line), &entry); err != nil {
				continue
			}
			if entry["msg"] == msg {
				return entry
			}
		}
		t.Fatalf("the child never logged %q:\n%s", msg, seen.String())
		return nil
	}

	addr, _ := awaitLog("soiree listening")["addr"].(string)
	if addr == "" {
		t.Fatalf("the child did not say which address it took:\n%s", seen.String())
	}

	// A connection that is accepted and then says nothing is what holds the
	// shutdown open long enough to signal into: net/http reaps one only once it
	// has been silent for five seconds, and until then the shutdown is waiting
	// on it exactly as it waits on a request that is still being served.
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("connect to the child: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if err := child.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("ask the child to stop: %v", err)
	}
	// Logged after the signals are handed back, so waiting for this line is
	// what puts the second one below on a process that is already shutting
	// down rather than on one that has not noticed the first yet.
	awaitLog("shutting down")

	if err := child.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("insist: %v", err)
	}

	err = child.Wait()
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("the child sat out its whole shutdown and exited successfully (err=%v), so the second signal was ignored:\n%s",
			err, seen.String())
	}
	status, ok := exit.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGTERM {
		t.Errorf("the child left through exit status %v rather than being killed by the second signal:\n%s",
			exit, seen.String())
	}
}
