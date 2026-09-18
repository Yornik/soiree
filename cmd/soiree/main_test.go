package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Yornik/soiree/internal/config"
	"github.com/Yornik/soiree/internal/httpd"
	"github.com/Yornik/soiree/internal/mailer"
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
