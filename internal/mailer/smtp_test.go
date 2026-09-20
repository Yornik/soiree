package mailer

import (
	"context"
	"net"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeSMTP is a scripted SMTP server on a local port.
//
// A real listener rather than a mock: the thing worth testing here is the
// conversation — that DATA is terminated, that a failure before the final dot
// is retryable and a failure at it is not — and a mock of net/smtp would only
// assert that this file calls the methods this file calls.
type fakeSMTP struct {
	t *testing.T

	// dropAtDot closes the connection instead of answering the terminating
	// dot, which is the one failure mode a caller must never retry.
	dropAtDot bool
	// rejectAtDot is a reply line answered to the terminating dot instead of
	// the acceptance — what a relay says when it has read the message and
	// decided against it.
	rejectAtDot string
	// offerStartTLS advertises an extension this fake cannot actually perform;
	// used only to check that a client with credentials insists on it.
	offerStartTLS bool

	mu       sync.Mutex
	received string
	rcpts    []string
}

func (f *fakeSMTP) start() (host string, port int) {
	f.t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		f.t.Fatalf("listen: %v", err)
	}
	f.t.Cleanup(func() {
		if err := ln.Close(); err != nil && !strings.Contains(err.Error(), "use of closed") {
			f.t.Logf("close listener: %v", err)
		}
	})

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(conn)
		}
	}()

	addr := ln.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port
}

func (f *fakeSMTP) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	tp := textproto.NewConn(conn)

	say := func(format string, args ...any) bool {
		return tp.PrintfLine(format, args...) == nil
	}
	if !say("220 fake.example.test ESMTP") {
		return
	}

	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		verb, rest, _ := strings.Cut(line, " ")
		switch strings.ToUpper(verb) {
		case "EHLO", "HELO":
			if !say("250-fake.example.test") {
				return
			}
			if f.offerStartTLS && !say("250-STARTTLS") {
				return
			}
			if !say("250 SIZE 10485760") {
				return
			}
		case "MAIL":
			if !say("250 2.1.0 sender ok") {
				return
			}
		case "RCPT":
			f.mu.Lock()
			f.rcpts = append(f.rcpts, rest)
			f.mu.Unlock()
			if !say("250 2.1.5 recipient ok") {
				return
			}
		case "DATA":
			if !say("354 go ahead") {
				return
			}
			var body strings.Builder
			for {
				l, err := tp.ReadLine()
				if err != nil {
					return
				}
				if l == "." {
					break
				}
				body.WriteString(l)
				body.WriteString("\n")
			}
			f.mu.Lock()
			f.received = body.String()
			f.mu.Unlock()
			if f.dropAtDot {
				// The message is in; the acknowledgement never arrives. The
				// caller cannot tell delivered from not delivered.
				return
			}
			if f.rejectAtDot != "" {
				if !say("%s", f.rejectAtDot) {
					return
				}
				continue
			}
			if !say("250 2.0.0 accepted") {
				return
			}
		case "QUIT":
			_ = say("221 2.0.0 bye")
			return
		default:
			if !say("250 2.0.0 ok") {
				return
			}
		}
	}
}

func (f *fakeSMTP) sender(host string, port int) *SMTP {
	return New(Config{Host: host, Port: port, From: "Soirée <plans@example.test>"})
}

func TestSendDelivers(t *testing.T) {
	f := &fakeSMTP{t: t}
	host, port := f.start()

	err := f.sender(host, port).Send(t.Context(), Message{
		To:      []string{"ada@example.test", "grace@example.test"},
		Subject: "Venue deposit",
		Text:    "Venue deposit is due on Friday.",
		HTML:    "<p>Venue deposit is due on Friday.</p>",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.rcpts) != 2 {
		t.Errorf("server saw %d recipients, want 2: %v", len(f.rcpts), f.rcpts)
	}
	for _, want := range []string{"Subject:", "Venue deposit", "multipart/alternative"} {
		if !strings.Contains(f.received, want) {
			t.Errorf("delivered message does not contain %q:\n%s", want, f.received)
		}
	}
	// Dot-stuffing and the terminating dot are net/smtp's job, but a body that
	// swallowed the last line would still be this package's bug.
	if !strings.Contains(f.received, "--") {
		t.Error("delivered message has no multipart boundary")
	}
}

// A connection that dies at the terminating dot may or may not have delivered
// the message, and a caller that retries on it sends the digest twice.
func TestSendAtTheDotIsAmbiguous(t *testing.T) {
	f := &fakeSMTP{t: t, dropAtDot: true}
	host, port := f.start()

	err := f.sender(host, port).Send(t.Context(), Message{
		To: []string{"ada@example.test"}, Subject: "x", Text: "y",
	})
	if err == nil {
		t.Fatal("expected an error when the server never acknowledged the message")
	}
	if !Ambiguous(err) {
		t.Errorf("error %v reported as safe to retry; it is not", err)
	}
}

// A server that answers the terminating dot with a refusal has stated that it
// took nothing, whatever the reason: spam scoring and rate limits are applied
// exactly there, because that is the first moment the message exists. Nothing
// was delivered, so a retry cannot deliver it twice, and a caller keeping a
// ledger may try again rather than burn the period.
func TestSendRefusedAtTheDotIsNotAmbiguous(t *testing.T) {
	for _, reply := range []string{
		"554 5.7.1 message rejected as spam",
		"451 4.7.1 greylisted, try again later",
	} {
		t.Run(reply, func(t *testing.T) {
			f := &fakeSMTP{t: t, rejectAtDot: reply}
			host, port := f.start()

			err := f.sender(host, port).Send(t.Context(), Message{
				To: []string{"ada@example.test"}, Subject: "x", Text: "y",
			})
			if err == nil {
				t.Fatal("expected the refusal to be reported")
			}
			if Ambiguous(err) {
				t.Errorf("an explicit refusal was reported as an uncertain delivery: %v", err)
			}
			if !strings.Contains(err.Error(), reply) {
				t.Errorf("error = %v, want it to carry the server's reply", err)
			}
		})
	}
}

// A refusal before the server has the message is safe to try again.
func TestSendBeforeTheMessageIsRetryable(t *testing.T) {
	// Nothing listening: the dial itself fails.
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := free.Addr().(*net.TCPAddr)
	if err := free.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s := New(Config{Host: addr.IP.String(), Port: addr.Port, From: "plans@example.test"})
	err = s.Send(t.Context(), Message{To: []string{"ada@example.test"}, Subject: "x", Text: "y"})
	if err == nil {
		t.Fatal("expected a dial failure")
	}
	if Ambiguous(err) {
		t.Errorf("a refused connection was reported as ambiguous: %v", err)
	}
}

// A password handed to a server that declined to encrypt is a password in
// somebody's packet capture.
func TestSendRefusesCredentialsInTheClear(t *testing.T) {
	f := &fakeSMTP{t: t}
	host, port := f.start()

	s := New(Config{
		Host: host, Port: port,
		Username: "plans@example.test", Password: "hunter2",
		From: "plans@example.test",
	})
	err := s.Send(t.Context(), Message{To: []string{"ada@example.test"}, Subject: "x", Text: "y"})
	if err == nil {
		t.Fatal("expected a refusal to authenticate over an unencrypted connection")
	}
	if !strings.Contains(err.Error(), "STARTTLS") {
		t.Errorf("error = %v, want it to name STARTTLS", err)
	}
	if Ambiguous(err) {
		t.Error("refusing to authenticate is not an ambiguous delivery")
	}
}

func TestSendWithoutConfigurationFails(t *testing.T) {
	err := New(Config{}).Send(context.Background(), Message{To: []string{"ada@example.test"}})
	if err == nil {
		t.Fatal("expected an unconfigured sender to refuse")
	}
}

// Every rejection below has to happen before a socket is opened. Build refuses
// them already, but Build is called from Send, and the order of the steps
// inside Send is the thing worth pinning: a version that dialled first would
// still pass every test in message_test.go.
//
// Neither field is attacker-controlled today — the recipient is an
// admin-entered address and the subject a constant — which is exactly the
// assumption that stops being true later.
func TestSendRefusesBadInputBeforeDialling(t *testing.T) {
	// A host under .example.test, which is reserved and never resolves, so a
	// dial would fail loudly and in different words.
	cfg := Config{Host: "smtp.nowhere.example.test", Port: 465, From: "plans@example.test"}

	for name, m := range map[string]Message{
		"newline in recipient": {
			To: []string{"ada@example.test\r\nBcc: mallory@example.test"}, Subject: "hello", Text: "body",
		},
		"newline in subject": {
			To: []string{"ada@example.test"}, Subject: "hello\r\nBcc: mallory@example.test", Text: "body",
		},
		"recipient is not an address": {
			To: []string{"not an address"}, Subject: "hello", Text: "body",
		},
		"no recipients": {Subject: "hello", Text: "body"},
	} {
		t.Run(name, func(t *testing.T) {
			err := New(cfg).Send(t.Context(), m)
			if err == nil {
				t.Fatal("Send() accepted it")
			}
			if strings.Contains(err.Error(), "dial") || strings.Contains(err.Error(), "tls") {
				t.Errorf("Send() reached the network before refusing: %v", err)
			}
			// Nothing left this process, so nothing could have been sent twice.
			if Ambiguous(err) {
				t.Errorf("a refusal before the connection was reported as ambiguous: %v", err)
			}
		})
	}
}

// Port 465 is TLS from the first byte, and the default belongs on that side: on
// a STARTTLS port a relay that declines the upgrade and asks for no credentials
// is talked to in the clear, and the body of an account mail is a set-password
// link — which is a credential.
func TestNewDefaultsToImplicitTLS(t *testing.T) {
	if got := New(Config{Host: "smtp.resend.com", From: "plans@example.test"}).cfg.Port; got != DefaultPort {
		t.Errorf("default port = %d, want %d", got, DefaultPort)
	}
	if got := New(Config{Host: "smtp.example.test", Port: 587, From: "plans@example.test"}).cfg.Port; got != 587 {
		t.Errorf("explicit port = %d, want 587", got)
	}
}

// Port 465 is implicit TLS and must not be spoken to in the clear.
func TestImplicitTLSIsChosenByPort(t *testing.T) {
	for port, want := range map[int]bool{465: true, 587: false, 25: false} {
		s := New(Config{Host: "smtp.example.test", Port: port, From: "plans@example.test"})
		if got := s.implicitTLS(); got != want {
			t.Errorf("port %s: implicitTLS = %v, want %v", strconv.Itoa(port), got, want)
		}
	}
}
