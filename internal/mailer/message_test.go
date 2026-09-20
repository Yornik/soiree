package mailer

import (
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"testing"
	"time"
)

// Every address here is at example.test, a name reserved for exactly this and
// guaranteed never to resolve.
var testConfig = Config{
	Host: "smtp.example.test",
	Port: 465,
	From: "Soirée <plans@example.test>",
}

var testTime = time.Date(2027, time.February, 25, 9, 30, 0, 0, time.UTC)

func buildMessage(t *testing.T, cfg Config, m Message) *mail.Message {
	t.Helper()
	raw, err := cfg.Build(m, testTime, "<abc@example.test>")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("parse built message: %v\n%s", err, raw)
	}
	return msg
}

func TestBuildIsAMultipartAlternative(t *testing.T) {
	msg := buildMessage(t, testConfig, Message{
		To:      []string{"ada@example.test", "Grace Hopper <grace@example.test>"},
		Subject: "Venue deposit",
		Text:    "Venue deposit is due on Friday.",
		HTML:    "<p>Venue deposit is due on Friday.</p>",
	})

	mediaType, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("content type: %v", err)
	}
	if mediaType != "multipart/alternative" {
		t.Fatalf("content type = %q, want multipart/alternative", mediaType)
	}
	if got := msg.Header.Get("To"); !strings.Contains(got, "ada@example.test") || !strings.Contains(got, "grace@example.test") {
		t.Errorf("To = %q, want both recipients", got)
	}

	mr := multipart.NewReader(msg.Body, params["boundary"])
	var types []string
	var bodies []string
	for {
		p, err := mr.NextPart()
		if err != nil {
			break
		}
		types = append(types, p.Header.Get("Content-Type"))
		body, err := io.ReadAll(quotedprintable.NewReader(p))
		if err != nil {
			t.Fatalf("read part: %v", err)
		}
		bodies = append(bodies, string(body))
	}

	// Text first, HTML second: a client renders the last alternative it
	// understands, so this order means "HTML if you can, text if you cannot".
	if len(types) != 2 {
		t.Fatalf("got %d parts, want 2: %v", len(types), types)
	}
	if !strings.HasPrefix(types[0], "text/plain") {
		t.Errorf("first part is %q, want text/plain", types[0])
	}
	if !strings.HasPrefix(types[1], "text/html") {
		t.Errorf("second part is %q, want text/html", types[1])
	}
	if !strings.Contains(bodies[0], "due on Friday") || !strings.Contains(bodies[1], "<p>") {
		t.Errorf("part bodies did not survive the round trip: %q / %q", bodies[0], bodies[1])
	}
}

// setPasswordLink is the shape internal/httpd builds: a 43-character
// base64url token — 32 bytes, RawURLEncoding — in the URL fragment. Its length
// is the point of the test below, so it is spelled out rather than shortened.
const setPasswordLink = "https://soiree.example.test/#/set-password?token=" +
	"Zm91cnRlZW4tYnl0ZXMtbm90LXJlYWxseS1hLXRva2Vu"

// Every header the account mail relies on, and the body arriving intact.
//
// Carried over from internal/mail, which was deleted in favour of this package
// and whose test asserted the same set on a simpler message. The headers are
// not decoration: Auto-Submitted is what stops a recipient's out-of-office
// responder answering the mail, and answering it again next week.
func TestBuildCarriesTheHeadersTheAccountMailNeeds(t *testing.T) {
	raw, err := testConfig.Build(Message{
		To:      []string{"ada@example.test"},
		Subject: "Your account is ready",
		Text:    "An account has been created for you. Choose a password here:\n\n" + setPasswordLink + "\n",
	}, testTime, "<abc@example.test>")
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	head, body, ok := strings.Cut(string(raw), "\r\n\r\n")
	if !ok {
		t.Fatalf("no blank line between headers and body:\n%s", raw)
	}
	for _, want := range []string{
		"From: ",
		"To: ",
		"Subject: Your account is ready",
		"Date: ",
		"Message-ID: <abc@example.test>",
		"MIME-Version: 1.0",
		"Auto-Submitted: auto-generated",
		"Content-Type: multipart/alternative",
	} {
		if !strings.Contains(head, want) {
			t.Errorf("headers are missing %q:\n%s", want, head)
		}
	}

	// A bare newline is the sort of thing a strict relay rejects at the worst
	// possible moment, and net/textproto's dot-writer only fixes what it sees.
	for _, line := range strings.Split(body, "\r\n") {
		if strings.ContainsAny(line, "\r\n") {
			t.Errorf("body line %q is not CRLF-terminated", line)
		}
	}
}

// A set-password link is 91 characters, so quoted-printable wraps it with a
// soft break and encodes the `=` in `?token=` as `=3D`. Neither is a problem —
// every mail client decodes both — but "the reset link arrives broken" is a
// silent account-recovery failure, so the round trip is asserted rather than
// assumed.
func TestSetPasswordLinkSurvivesQuotedPrintable(t *testing.T) {
	raw, err := testConfig.Build(Message{
		To:      []string{"ada@example.test"},
		Subject: "Set a new password",
		Text:    "Someone asked to set a new password on your account.\n\n" + setPasswordLink + "\n",
	}, testTime, "<abc@example.test>")
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	// Encoded on the wire: a reader of the raw bytes must not find the link
	// sitting there unwrapped, or this test is checking nothing.
	if strings.Contains(string(raw), setPasswordLink) {
		t.Error("the link was not quoted-printable encoded; this test no longer proves anything")
	}

	msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	_, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("content type: %v", err)
	}
	part, err := multipart.NewReader(msg.Body, params["boundary"]).NextPart()
	if err != nil {
		t.Fatalf("first part: %v", err)
	}
	decoded, err := io.ReadAll(quotedprintable.NewReader(part))
	if err != nil {
		t.Fatalf("decode part: %v", err)
	}
	if !strings.Contains(string(decoded), setPasswordLink) {
		t.Errorf("the link did not survive the round trip:\n%s", decoded)
	}
}

// The account mail has no HTML part. A password-reset link is a credential and
// nothing about it wants rendering, so the message is text and nothing else —
// the one shape the reminder digest never exercises.
func TestBuildWithoutHTMLHasOneTextPart(t *testing.T) {
	msg := buildMessage(t, testConfig, Message{
		To:      []string{"ada@example.test"},
		Subject: "Your account is ready",
		Text:    "Choose a password here:\n\n" + setPasswordLink + "\n",
	})

	_, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("content type: %v", err)
	}
	mr := multipart.NewReader(msg.Body, params["boundary"])
	var types []string
	for {
		p, err := mr.NextPart()
		if err != nil {
			break
		}
		types = append(types, p.Header.Get("Content-Type"))
	}
	if len(types) != 1 || !strings.HasPrefix(types[0], "text/plain") {
		t.Errorf("parts = %v, want a single text/plain", types)
	}
}

// The event names this serves are Dutch and Indonesian, so a subject is not
// ASCII and a raw 8-bit header is not portable.
func TestSubjectIsEncoded(t *testing.T) {
	msg := buildMessage(t, testConfig, Message{
		To:      []string{"ada@example.test"},
		Subject: "Soirée: 2 items past their date",
		Text:    "body",
	})

	raw := msg.Header.Get("Subject")
	if strings.ContainsAny(raw, "é") {
		t.Errorf("Subject header carries a raw non-ASCII byte: %q", raw)
	}
	decoded, err := new(mime.WordDecoder).DecodeHeader(raw)
	if err != nil {
		t.Fatalf("decode subject: %v", err)
	}
	if decoded != "Soirée: 2 items past their date" {
		t.Errorf("decoded subject = %q", decoded)
	}
}

// subjectLines returns the Subject field as it goes out on the wire: its own
// line, and every folded continuation after it.
func subjectLines(t *testing.T, raw string) []string {
	t.Helper()
	head, _, ok := strings.Cut(raw, "\r\n\r\n")
	if !ok {
		t.Fatalf("no blank line between headers and body:\n%s", raw)
	}
	var out []string
	for _, line := range strings.Split(head, "\r\n") {
		switch {
		case strings.HasPrefix(line, "Subject:"):
			out = append(out, line)
		case len(out) == 0:
			continue
		case strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t"):
			out = append(out, line)
		default:
			return out
		}
	}
	if len(out) == 0 {
		t.Fatalf("no Subject header:\n%s", head)
	}
	return out
}

// One accent Q-encodes the whole subject, and mime.QEncoding then splits it
// into encoded-words of up to 75 characters — counting nothing for the
// "Subject: " in front of the first one. RFC 2047 section 2 allows a line
// holding an encoded-word 76, so an event name with an é in it and the digest
// headline after it goes out over that limit unless the header is folded.
//
// Only the subject's own lines are measured. Content-Type carries a
// 60-character multipart boundary and is past 76 by construction, and it holds
// no encoded-word, so its length is a different question from this one.
func TestLongEncodedSubjectIsFolded(t *testing.T) {
	const subject = "Célébration: 3 items need a decision before the weekend, 2 already late"

	raw, err := testConfig.Build(Message{
		To:      []string{"ada@example.test"},
		Subject: subject,
		Text:    "body",
	}, testTime, "<abc@example.test>")
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	for _, line := range subjectLines(t, string(raw)) {
		if len(line) > 76 {
			t.Errorf("subject line of %d octets exceeds the 76 RFC 2047 allows: %q", len(line), line)
		}
	}

	// Folding is only worth something if it unfolds: the words go back
	// together with the space between them, which a decoder drops.
	msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	decoded, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if err != nil {
		t.Fatalf("decode subject: %v", err)
	}
	if decoded != subject {
		t.Errorf("decoded subject = %q, want %q", decoded, subject)
	}
}

// A subject that fits stays on one line, encoded or not: folding every encoded
// subject would cost a raw transcript its readability and buy nothing.
func TestShortEncodedSubjectStaysOnOneLine(t *testing.T) {
	raw, err := testConfig.Build(Message{
		To:      []string{"ada@example.test"},
		Subject: "Soirée: 2 items past their date",
		Text:    "body",
	}, testTime, "<abc@example.test>")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if lines := subjectLines(t, string(raw)); len(lines) != 1 {
		t.Errorf("subject was folded into %d lines: %q", len(lines), lines)
	}
}

// A newline in the subject is header injection: everything after it is read by
// the server as a header of its own, which is how a subject becomes a Bcc.
func TestBuildRejectsHeaderInjection(t *testing.T) {
	_, err := testConfig.Build(Message{
		To:      []string{"ada@example.test"},
		Subject: "Deadlines\r\nBcc: someone@example.test",
		Text:    "body",
	}, testTime, "<abc@example.test>")
	if err == nil {
		t.Fatal("expected a subject with a line break to be refused")
	}
}

func TestBuildRejectsBadAddresses(t *testing.T) {
	for name, m := range map[string]Message{
		"no recipients": {Subject: "x", Text: "y"},
		"not an address": {
			To: []string{"ada at example.test"}, Subject: "x", Text: "y",
		},
		"injected recipient": {
			To: []string{"ada@example.test\r\nRCPT TO:<eve@example.test>"}, Subject: "x", Text: "y",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := testConfig.Build(m, testTime, "<abc@example.test>"); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

// Long lines are what quoted-printable is here for: RFC 5321 caps a line at
// 998 octets and a planner's note in a budget row is not bounded by anything.
func TestLongLinesAreWrapped(t *testing.T) {
	raw, err := testConfig.Build(Message{
		To:      []string{"ada@example.test"},
		Subject: "x",
		Text:    strings.Repeat("deadline ", 400),
	}, testTime, "<abc@example.test>")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	for _, line := range strings.Split(string(raw), "\r\n") {
		if len(line) > 998 {
			t.Fatalf("line of %d octets exceeds the SMTP limit", len(line))
		}
	}
}

func TestLoadConfigIsEmptyWithoutEnv(t *testing.T) {
	for _, k := range []string{"SOIREE_SMTP_HOST", "SOIREE_SMTP_PORT", "SOIREE_SMTP_USER", "SOIREE_SMTP_PASSWORD", "SOIREE_SMTP_FROM"} {
		t.Setenv(k, "")
	}
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("an unconfigured mailer must not be an error: %v", err)
	}
	if cfg.Configured() {
		t.Error("an empty environment reported as configured")
	}
}

func TestLoadConfigRejectsMalformedValues(t *testing.T) {
	t.Run("port", func(t *testing.T) {
		t.Setenv("SOIREE_SMTP_PORT", "cinq")
		if _, err := LoadConfig(); err == nil {
			t.Error("expected an error for a non-numeric port")
		}
	})
	t.Run("from", func(t *testing.T) {
		t.Setenv("SOIREE_SMTP_FROM", "plans at example.test")
		if _, err := LoadConfig(); err == nil {
			t.Error("expected an error for a malformed from address")
		}
	})
}

func TestLoadConfigDefaultsToImplicitTLS(t *testing.T) {
	t.Setenv("SOIREE_SMTP_HOST", "smtp.example.test")
	t.Setenv("SOIREE_SMTP_FROM", "plans@example.test")
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Port != DefaultPort {
		t.Errorf("port = %d, want %d", cfg.Port, DefaultPort)
	}
	if !cfg.Configured() {
		t.Error("host and from set, but not reported as configured")
	}
}
