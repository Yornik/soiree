// Package mailer sends mail over SMTP, using nothing but the standard
// library.
//
// It exists as its own package because the thing that sends mail and the thing
// that decides what to say are not the same concern, and because a mail client
// is the kind of code that grows a dependency for no reason. `net/smtp` plus
// `mime/multipart` is the whole of it.
//
// Two decisions are worth knowing about before reading further.
//
// Delivery failures are split into retryable and ambiguous. Everything up to
// and including RCPT is retryable: the server never saw the message. From the
// moment the body is closed off with the terminating dot, a failure could mean
// either "not delivered" or "delivered and the acknowledgement was lost", and
// a caller that retries on that turns one digest into two. Callers keeping a
// sent-ledger need to know which they are holding, so Send says.
//
// Nothing here fetches anything. There is no tracking pixel, no remote image,
// no link to a stylesheet; the HTML part is self-contained and inline-styled,
// because a mail that phones home reports on the reader.
package mailer

import (
	"fmt"
	"net/mail"
	"os"
	"strconv"
	"strings"
)

// DefaultPort is implicit TLS. The real deployment is smtp.resend.com:465, and
// a submission port that is not 587 is nearly always 465.
const DefaultPort = 465

// Config is everything needed to talk to an SMTP server.
type Config struct {
	Host     string
	Port     int
	Username string
	Password string
	// From is the envelope sender and the From header. It may carry a display
	// name ("Soirée <plans@example.test>"); the envelope uses just the address.
	From string
}

// Configured reports whether there is enough here to send anything. An
// unconfigured mailer is not an error — features that send mail switch
// themselves off rather than taking the process down with them.
func (c Config) Configured() bool {
	return c.Host != "" && c.From != ""
}

// LoadConfig reads SMTP settings from the environment.
//
// An empty configuration comes back without an error, because "no SMTP here"
// is a normal way to run this application. A *malformed* setting is an error:
// SOIREE_SMTP_PORT=cinq is a typo somebody wants to hear about, not a reason to
// quietly fall back to 465.
func LoadConfig() (Config, error) {
	c := Config{
		Host:     strings.TrimSpace(os.Getenv("SOIREE_SMTP_HOST")),
		Port:     DefaultPort,
		Username: os.Getenv("SOIREE_SMTP_USER"),
		Password: os.Getenv("SOIREE_SMTP_PASSWORD"),
		From:     strings.TrimSpace(os.Getenv("SOIREE_SMTP_FROM")),
	}

	if v := strings.TrimSpace(os.Getenv("SOIREE_SMTP_PORT")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 65535 {
			return Config{}, fmt.Errorf("SOIREE_SMTP_PORT must be a port number, got %q", v)
		}
		c.Port = n
	}

	if c.From != "" {
		if _, err := mail.ParseAddress(c.From); err != nil {
			return Config{}, fmt.Errorf("SOIREE_SMTP_FROM is not an email address: %q", c.From)
		}
	}

	return c, nil
}
