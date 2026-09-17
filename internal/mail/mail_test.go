package mail

import (
	"context"
	"strings"
	"testing"
)

func TestBuildProducesACompleteMessage(t *testing.T) {
	msg := build("soiree@example.test", "ada@example.test", "Your account is ready",
		"Choose a password here:\n\nhttps://soiree.example.test/#/set-password?token=xyz\n")

	head, body, ok := strings.Cut(msg, "\r\n\r\n")
	if !ok {
		t.Fatalf("no blank line between headers and body:\n%q", msg)
	}
	for _, want := range []string{
		"From: soiree@example.test",
		"To: ada@example.test",
		"Subject: Your account is ready",
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		// Stops a recipient's out-of-office reply from bouncing back at the
		// sending address forever.
		"Auto-Submitted: auto-generated",
		"Date: ",
	} {
		if !strings.Contains(head, want) {
			t.Errorf("headers are missing %q:\n%s", want, head)
		}
	}

	// Bare newlines in a body are the sort of thing a strict relay rejects at
	// the worst moment.
	for _, line := range strings.Split(body, "\r\n") {
		if strings.ContainsAny(line, "\r\n") {
			t.Errorf("body line %q is not CRLF-terminated", line)
		}
	}
	if !strings.Contains(body, "https://soiree.example.test/#/set-password?token=xyz") {
		t.Errorf("the link did not survive into the body:\n%s", body)
	}
}

func TestSendRefusesHeaderInjection(t *testing.T) {
	s := New(Config{Host: "smtp.example.test", From: "soiree@example.test"})
	ctx := context.Background()

	// A newline in either field would let the caller append headers of their
	// own — a Bcc, a second recipient. Neither field is attacker-controlled
	// today, which is exactly the assumption that stops being true later.
	for name, tc := range map[string]struct{ to, subject string }{
		"newline in recipient":        {"ada@example.test\r\nBcc: mallory@example.test", "hello"},
		"newline in subject":          {"ada@example.test", "hello\r\nBcc: mallory@example.test"},
		"recipient is not an address": {"not an address", "hello"},
	} {
		t.Run(name, func(t *testing.T) {
			// Fails before any socket is opened, which is also why this test
			// needs no relay.
			err := s.Send(ctx, tc.to, tc.subject, "body")
			if err == nil {
				t.Fatal("Send() accepted it")
			}
			if strings.Contains(err.Error(), "dial") {
				t.Errorf("Send() got as far as dialling before refusing: %v", err)
			}
		})
	}
}

func TestNewDefaultsToImplicitTLS(t *testing.T) {
	// Port 465 is TLS from the first byte. The default matters: on a
	// STARTTLS port a relay that does not offer the upgrade would otherwise
	// be a plaintext credential handover, which connect() refuses outright.
	if got := New(Config{Host: "smtp.resend.com"}).cfg.Port; got != "465" {
		t.Errorf("default port = %q, want 465", got)
	}
	if got := New(Config{Host: "smtp.example.test", Port: "587"}).cfg.Port; got != "587" {
		t.Errorf("explicit port = %q, want 587", got)
	}
}
