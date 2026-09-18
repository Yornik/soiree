package mailer

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// Sender is the seam between deciding what to say and saying it. Tests
// substitute a recording implementation; nothing in a test suite should be
// able to reach a real mail server by accident.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// SendError reports a failed send, and — the part callers act on — whether a
// retry could deliver the same message twice.
//
// Ambiguous is false for everything up to and including RCPT: the server
// refused before it had the message, so nothing was delivered and trying again
// is free. It is true from the terminating dot onwards, where a dropped
// connection means either "rejected" or "accepted, acknowledgement lost" and
// there is no way to tell which.
type SendError struct {
	Err       error
	Ambiguous bool
}

func (e *SendError) Error() string {
	if e.Ambiguous {
		return "mailer: delivery uncertain: " + e.Err.Error()
	}
	return "mailer: " + e.Err.Error()
}

func (e *SendError) Unwrap() error { return e.Err }

// Ambiguous reports whether err left delivery undecided, and so whether a
// retry risks sending twice.
func Ambiguous(err error) bool {
	var se *SendError
	return errors.As(err, &se) && se.Ambiguous
}

// dialTimeout bounds the TCP connect and the TLS handshake when the caller's
// context has no deadline of its own.
const dialTimeout = 30 * time.Second

// SMTP sends over a real server.
type SMTP struct {
	cfg Config

	// dial is swapped in tests for one that hands back a pipe to a fake
	// server. Nil means a real TCP dial.
	dial func(ctx context.Context, addr string) (net.Conn, error)
	// now and newID are the two impure inputs to a rendered message, injected
	// for the same reason.
	now   func() time.Time
	newID func() (string, error)
}

// New builds a sender for cfg.
//
// A zero Port means the caller did not set one, and is filled in with
// DefaultPort rather than dialled. LoadConfig already defaults it; this covers
// a Config assembled by hand, where the alternative is a dial to port 0 and an
// error that says nothing about the missing setting. It also keeps the default
// on the implicit-TLS side: falling back to a STARTTLS port would mean a relay
// that declines the upgrade gets the conversation in the clear.
func New(cfg Config) *SMTP {
	if cfg.Port == 0 {
		cfg.Port = DefaultPort
	}
	return &SMTP{cfg: cfg}
}

// Send delivers one message.
//
// Port 465 is implicit TLS — the connection is encrypted from the first byte,
// which is why this does not use smtp.SendMail: that helper speaks plaintext
// first and upgrades with STARTTLS, and against a 465 listener it talks
// cleartext at a TLS server and hangs. Anything else is treated as submission
// (587) and upgraded with STARTTLS, which is mandatory here rather than
// best-effort: sending a password to a server that declined to encrypt is how
// credentials leak.
func (s *SMTP) Send(ctx context.Context, m Message) error {
	if !s.cfg.Configured() {
		return &SendError{Err: errors.New("no SMTP host configured")}
	}

	from, err := s.cfg.envelopeFrom()
	if err != nil {
		return &SendError{Err: err}
	}
	rcpts := make([]string, 0, len(m.To))
	for _, raw := range m.To {
		a, err := mail.ParseAddress(raw)
		if err != nil {
			return &SendError{Err: fmt.Errorf("recipient %q: %w", raw, err)}
		}
		rcpts = append(rcpts, a.Address)
	}

	id, err := s.messageID(from)
	if err != nil {
		return &SendError{Err: err}
	}
	body, err := s.cfg.Build(m, s.clock(), id)
	if err != nil {
		return &SendError{Err: err}
	}

	conn, err := s.connect(ctx)
	if err != nil {
		return &SendError{Err: err}
	}

	c, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		if cerr := conn.Close(); cerr != nil {
			err = errors.Join(err, cerr)
		}
		return &SendError{Err: fmt.Errorf("smtp greeting: %w", err)}
	}
	// Close is the blunt teardown for every path that did not reach a clean
	// QUIT; on a connection already closed by Quit it is a no-op error we do
	// not care about.
	defer func() { _ = c.Close() }()

	if err := s.handshake(c); err != nil {
		return &SendError{Err: err}
	}

	if err := c.Mail(from); err != nil {
		return &SendError{Err: fmt.Errorf("MAIL FROM: %w", err)}
	}
	for _, rcpt := range rcpts {
		if err := c.Rcpt(rcpt); err != nil {
			return &SendError{Err: fmt.Errorf("RCPT TO: %w", err)}
		}
	}

	w, err := c.Data()
	if err != nil {
		return &SendError{Err: fmt.Errorf("DATA: %w", err)}
	}
	if _, err := w.Write(body); err != nil {
		// The terminating dot has not been sent, so the server discards what
		// it has. Still retryable.
		if cerr := w.Close(); cerr != nil {
			err = errors.Join(err, cerr)
		}
		return &SendError{Err: fmt.Errorf("write body: %w", err)}
	}
	// The point of no return: this writes the terminating dot and reads the
	// server's verdict. A failure here may mean the mail was accepted and the
	// reply was lost, so the caller must not retry.
	if err := w.Close(); err != nil {
		return &SendError{Err: fmt.Errorf("end of message: %w", err), Ambiguous: true}
	}

	// The message is accepted by now. A server that mishandles QUIT has not
	// un-sent it, so a failure here is not the caller's problem.
	_ = c.Quit()
	return nil
}

// connect opens the transport, encrypted before anything is written when the
// port says so.
func (s *SMTP) connect(ctx context.Context) (net.Conn, error) {
	addr := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))

	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, dialTimeout)
		defer cancel()
	}

	conn, err := s.dialer()(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}
	// The SMTP conversation is line-at-a-time with no deadlines of its own, so
	// without this a server that accepts the connection and then says nothing
	// holds the scheduler goroutine forever.
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return nil, errors.Join(fmt.Errorf("set deadline: %w", err), conn.Close())
		}
	}

	if !s.implicitTLS() {
		return conn, nil
	}

	tc := tls.Client(conn, s.tlsConfig())
	if err := tc.HandshakeContext(ctx); err != nil {
		return nil, errors.Join(tlsHint(addr, err), conn.Close())
	}
	return tc, nil
}

// handshake authenticates, upgrading the connection first if it is not already
// encrypted.
//
// STARTTLS is taken whenever the server offers it, and required whenever there
// is a password to send: a credential handed to a server that declined to
// encrypt is a credential in somebody's packet capture. A relay that wants no
// credentials at all — the local MTA sidecar pattern — is allowed to stay in
// the clear, because there is nothing to leak and refusing would rule the
// deployment out.
func (s *SMTP) handshake(c *smtp.Client) error {
	if !s.implicitTLS() {
		ok, _ := c.Extension("STARTTLS")
		switch {
		case ok:
			if err := c.StartTLS(s.tlsConfig()); err != nil {
				return tlsHint(s.cfg.Host, err)
			}
		case s.cfg.Username != "":
			return errors.New("server does not offer STARTTLS; refusing to send credentials in the clear")
		}
	}
	if s.cfg.Username == "" {
		return nil
	}
	if err := c.Auth(smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)); err != nil {
		return fmt.Errorf("authenticate: %w", err)
	}
	return nil
}

func (s *SMTP) implicitTLS() bool { return s.cfg.Port == DefaultPort }

func (s *SMTP) tlsConfig() *tls.Config {
	return &tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12}
}

func (s *SMTP) dialer() func(context.Context, string) (net.Conn, error) {
	if s.dial != nil {
		return s.dial
	}
	return func(ctx context.Context, addr string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	}
}

func (s *SMTP) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// messageID builds a globally unique id in the sender's own domain, which is
// what lets a reader's client thread a reply and a postmaster find this
// message in a log.
func (s *SMTP) messageID(from string) (string, error) {
	if s.newID != nil {
		return s.newID()
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("message id: %w", err)
	}
	domain := "localhost"
	if _, d, ok := strings.Cut(from, "@"); ok && d != "" {
		domain = d
	}
	return "<" + hex.EncodeToString(b[:]) + "@" + domain + ">", nil
}

// tlsHint names the most likely cause of a certificate failure in this
// deployment. The image is FROM scratch and carries no CA bundle, so the first
// outbound TLS connection this application ever makes fails with an error that
// says nothing about the real problem.
func tlsHint(addr string, err error) error {
	var unknown x509.UnknownAuthorityError
	var hostname x509.HostnameError
	if errors.As(err, &unknown) || errors.As(err, &hostname) {
		return fmt.Errorf("tls %s: %w (a scratch-based image carries no CA bundle — check one was copied into the final stage)", addr, err)
	}
	return fmt.Errorf("tls %s: %w", addr, err)
}
