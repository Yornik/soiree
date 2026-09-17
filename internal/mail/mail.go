// Package mail sends the few messages this application has to send.
//
// It is net/smtp and nothing else. A mail library would bring a dependency
// tree and a CVE surface for what amounts to one plain-text message with five
// headers, and the supply-chain work elsewhere in this repository would be odd
// alongside that.
//
// Mail is optional. A deployment with no SMTP configuration is degraded, not
// broken: account creation still works and hands the set-password link back to
// the admin to pass on. That is why this package's absence is representable —
// the caller holds a nil Mailer — rather than being a mode threaded through
// every handler.
package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

// Config is an SMTP relay. Username and Password may both be empty for a relay
// that authenticates by network position; setting only one is a configuration
// error and is rejected before it gets here.
type Config struct {
	Host     string
	Port     string
	Username string
	Password string
	From     string
}

// implicitTLSPort is the submission port that is TLS from the first byte.
// Anything else is assumed to be the STARTTLS style, which is upgraded before
// credentials move.
const implicitTLSPort = "465"

// dialTimeout bounds the whole conversation when the caller's context has no
// deadline of its own.
const dialTimeout = 30 * time.Second

// Sender delivers over SMTP. Safe for concurrent use: it holds configuration
// and opens a connection per message.
type Sender struct {
	cfg Config
}

// New builds a Sender.
func New(cfg Config) *Sender {
	if cfg.Port == "" {
		cfg.Port = implicitTLSPort
	}
	return &Sender{cfg: cfg}
}

// Send delivers one plain-text message.
//
// Nothing about the message is logged by this package, by either the caller's
// convention or its own: the body of an account mail is a link, and a link is
// a credential.
func (s *Sender) Send(ctx context.Context, to, subject, body string) error {
	addr, err := mail.ParseAddress(to)
	if err != nil {
		return fmt.Errorf("mail: bad recipient: %w", err)
	}
	// A newline in a header value lets the caller append headers of their own
	// — a Bcc, another recipient. The recipient comes from an admin-entered
	// address and the subject from a constant, so this should never fire; it
	// is here because "should never" is how header injection keeps happening.
	if strings.ContainsAny(subject, "\r\n") {
		return errors.New("mail: subject contains a line break")
	}

	msg := build(s.cfg.From, addr.Address, subject, body)

	c, err := s.connect(ctx)
	if err != nil {
		return err
	}
	// Quit below closes the connection on the happy path; this covers every
	// early return, where the error being returned matters more than this one.
	defer func() { _ = c.Close() }()

	if s.cfg.Username != "" {
		auth := smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("mail: authenticate: %w", err)
		}
	}
	if err := c.Mail(s.cfg.From); err != nil {
		return fmt.Errorf("mail: sender rejected: %w", err)
	}
	if err := c.Rcpt(addr.Address); err != nil {
		return fmt.Errorf("mail: recipient rejected: %w", err)
	}

	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("mail: start message: %w", err)
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		_ = w.Close()
		return fmt.Errorf("mail: write message: %w", err)
	}
	// This is the call that actually flushes the message and reads the
	// server's verdict, so its error is the one that says whether anything was
	// delivered. Discarding it would turn every rejection into a silent
	// success.
	if err := w.Close(); err != nil {
		return fmt.Errorf("mail: send message: %w", err)
	}
	if err := c.Quit(); err != nil {
		return fmt.Errorf("mail: quit: %w", err)
	}
	return nil
}

// connect dials the relay and gets it to TLS before anything worth reading
// crosses it.
func (s *Sender) connect(ctx context.Context) (*smtp.Client, error) {
	addr := net.JoinHostPort(s.cfg.Host, s.cfg.Port)

	d := &net.Dialer{Timeout: dialTimeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("mail: dial %s: %w", addr, err)
	}

	// net/smtp predates context, so the deadline is applied to the socket. A
	// caller without one still gets a bound, or a relay that accepts the
	// connection and then says nothing holds a goroutine forever.
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(dialTimeout)
	}
	if err := conn.SetDeadline(deadline); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("mail: set deadline: %w", err)
	}

	tlsCfg := &tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12}

	if s.cfg.Port == implicitTLSPort {
		tc := tls.Client(conn, tlsCfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("mail: tls handshake with %s: %w", addr, err)
		}
		// smtp.NewClient detects a *tls.Conn and reports the session as
		// already encrypted, which is what lets PlainAuth run over it —
		// PlainAuth refuses to hand credentials to an unencrypted connection,
		// and that refusal is the point.
		c, err := smtp.NewClient(tc, s.cfg.Host)
		if err != nil {
			_ = tc.Close()
			return nil, fmt.Errorf("mail: greet %s: %w", addr, err)
		}
		return c, nil
	}

	c, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("mail: greet %s: %w", addr, err)
	}
	if ok, _ := c.Extension("STARTTLS"); !ok {
		_ = c.Close()
		return nil, fmt.Errorf("mail: %s offers no STARTTLS and is not port %s; refusing to send in the clear", addr, implicitTLSPort)
	}
	if err := c.StartTLS(tlsCfg); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("mail: starttls with %s: %w", addr, err)
	}
	return c, nil
}

// build assembles the RFC 5322 message. CRLF throughout, because some relays
// are strict about it and the ones that are not are not worth finding out
// about in production.
func build(from, to, subject, body string) string {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + to + "\r\n")
	b.WriteString("Subject: " + subject + "\r\n")
	b.WriteString("Date: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("Auto-Submitted: auto-generated\r\n")
	b.WriteString("\r\n")
	b.WriteString(strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n"))
	return b.String()
}
