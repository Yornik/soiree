package mailer

import (
	"bytes"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"strings"
	"time"
)

// Message is one mail, in both the forms a reader might render.
//
// Text is not optional. A plain-text alternative is what makes the mail
// readable in a terminal client, in a screen reader, and in the preview line
// of a phone's notification — and an HTML-only mail is also the shape spam
// filters like least.
type Message struct {
	To      []string
	Subject string
	Text    string
	HTML    string
}

// Build renders the message as RFC 5322 bytes, ready to hand to DATA.
//
// date and messageID are parameters rather than read from the clock and the
// random source inside, so that the rendered bytes are a pure function of the
// inputs and a test can assert on all of them.
func (c Config) Build(m Message, date time.Time, messageID string) ([]byte, error) {
	from, err := mail.ParseAddress(c.From)
	if err != nil {
		return nil, fmt.Errorf("from address %q: %w", c.From, err)
	}
	if len(m.To) == 0 {
		return nil, fmt.Errorf("mailer: no recipients")
	}
	to := make([]string, 0, len(m.To))
	for _, raw := range m.To {
		addr, err := mail.ParseAddress(raw)
		if err != nil {
			return nil, fmt.Errorf("recipient %q: %w", raw, err)
		}
		to = append(to, addr.String())
	}
	// A newline in a header value is header injection: everything after it is
	// read by the server as a header of its own, which is how a subject becomes
	// a Bcc. mail.ParseAddress already refuses them in addresses; the subject
	// is the field nobody checks.
	if strings.ContainsAny(m.Subject, "\r\n") {
		return nil, fmt.Errorf("mailer: subject contains a line break")
	}

	var buf bytes.Buffer
	mp := multipart.NewWriter(&buf)

	header(&buf, "From", from.String())
	header(&buf, "To", strings.Join(to, ", "))
	// Q-encoded, because the event names this serves are Dutch and Indonesian
	// and a raw é in a header is not portable. mime.QEncoding leaves pure
	// ASCII alone, so the common case stays readable in a raw transcript.
	header(&buf, "Subject", mime.QEncoding.Encode("utf-8", m.Subject))
	header(&buf, "Date", date.Format(time.RFC1123Z))
	header(&buf, "Message-ID", messageID)
	header(&buf, "MIME-Version", "1.0")
	// Auto-Submitted marks this as machine-generated so that an out-of-office
	// responder does not answer it, and answer it again next week.
	header(&buf, "Auto-Submitted", "auto-generated")
	header(&buf, "Content-Type", `multipart/alternative; boundary="`+mp.Boundary()+`"`)
	// The blank line that ends the headers and starts the body.
	_, _ = buf.WriteString("\r\n")

	// Plain text first, HTML second: a client picks the last alternative it can
	// render, so this order means "HTML if you can, text if you cannot".
	if err := part(mp, "text/plain; charset=utf-8", m.Text); err != nil {
		return nil, err
	}
	if m.HTML != "" {
		if err := part(mp, "text/html; charset=utf-8", m.HTML); err != nil {
			return nil, err
		}
	}
	if err := mp.Close(); err != nil {
		return nil, fmt.Errorf("mailer: close multipart: %w", err)
	}

	return buf.Bytes(), nil
}

// header writes one header line. bytes.Buffer documents its write error as
// always nil, so this returns nothing and discards it explicitly.
func header(buf *bytes.Buffer, name, value string) {
	_, _ = buf.WriteString(name + ": " + value + "\r\n")
}

// part writes one alternative, quoted-printable encoded.
//
// Quoted-printable rather than 8bit or base64: it keeps the text readable in a
// raw transcript, survives a server that only speaks 7 bits, and — the part
// that actually matters — wraps lines, so a long note in a budget row cannot
// push a line past the 998 octets RFC 5321 allows.
func part(mp *multipart.Writer, contentType, body string) error {
	h := textproto.MIMEHeader{}
	h.Set("Content-Type", contentType)
	h.Set("Content-Transfer-Encoding", "quoted-printable")

	w, err := mp.CreatePart(h)
	if err != nil {
		return fmt.Errorf("mailer: create part: %w", err)
	}
	qp := quotedprintable.NewWriter(w)
	if _, err := qp.Write([]byte(crlf(body))); err != nil {
		return fmt.Errorf("mailer: write part: %w", err)
	}
	if err := qp.Close(); err != nil {
		return fmt.Errorf("mailer: close part: %w", err)
	}
	return nil
}

// crlf normalises line endings. Go string literals carry bare newlines and
// mail wants CRLF; net/textproto's dot-writer fixes this too, but only for the
// bytes it sees, and the quoted-printable encoder runs first.
func crlf(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n")
}

// envelopeFrom is the bare address for MAIL FROM, with any display name
// stripped — the envelope is a routing instruction, not something a person
// reads.
func (c Config) envelopeFrom() (string, error) {
	a, err := mail.ParseAddress(c.From)
	if err != nil {
		return "", fmt.Errorf("from address %q: %w", c.From, err)
	}
	return a.Address, nil
}
