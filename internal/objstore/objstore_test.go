package objstore

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The example AWS publishes for query-string authentication, with its own
// answer. Every input below is from that page, and so is the signature:
// https://docs.aws.amazon.com/AmazonS3/latest/API/sigv4-query-string-auth.html
//
// This is the test that says the signer is Signature Version 4 and not
// something that merely agrees with itself. A round trip through a test bucket
// cannot say that about the pieces a test bucket is lenient about.
func TestPresignMatchesThePublishedExample(t *testing.T) {
	const want = "aeeed9bbccd4d02ee5c0109b86d86835f995330da4c265957d157751f604d404"

	got := presignURL(http.MethodGet, "https", "examplebucket.s3.amazonaws.com", "/test.txt",
		"us-east-1", "AKIAIOSFODNN7EXAMPLE", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC), 24*time.Hour, nil, nil)

	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("not a URL: %v", err)
	}
	if sig := u.Query().Get("X-Amz-Signature"); sig != want {
		t.Fatalf("signature\n got %s\nwant %s\n url %s", sig, want, got)
	}
	// The rest of the URL is part of the contract too: the example's, verbatim.
	const wantURL = "https://examplebucket.s3.amazonaws.com/test.txt" +
		"?X-Amz-Algorithm=AWS4-HMAC-SHA256" +
		"&X-Amz-Credential=AKIAIOSFODNN7EXAMPLE%2F20130524%2Fus-east-1%2Fs3%2Faws4_request" +
		"&X-Amz-Date=20130524T000000Z&X-Amz-Expires=86400&X-Amz-SignedHeaders=host" +
		"&X-Amz-Signature=" + want
	if got != wantURL {
		t.Fatalf("url\n got %s\nwant %s", got, wantURL)
	}
}

// A signature covers what it was made for and nothing else. If any of these
// produced the same signature as the baseline, that input would not be signed
// at all — and the size limit, the forced download and the expiry are each one
// of these inputs.
func TestEverySignedInputChangesTheSignature(t *testing.T) {
	at := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	sign := func(method, path string, ttl time.Duration, q url.Values, h map[string]string) string {
		u, err := url.Parse(presignURL(method, "https", "s3.example.test", path, "nbg1", "id", "secret", at, ttl, q, h))
		if err != nil {
			t.Fatal(err)
		}
		return u.Query().Get("X-Amz-Signature")
	}
	put := map[string]string{"content-length": "100", "content-type": "image/png"}
	base := sign(http.MethodPut, "/b/attachments/x", time.Minute, nil, put)

	for name, other := range map[string]string{
		"method": sign(http.MethodGet, "/b/attachments/x", time.Minute, nil, put),
		"key":    sign(http.MethodPut, "/b/attachments/y", time.Minute, nil, put),
		"expiry": sign(http.MethodPut, "/b/attachments/x", time.Hour, nil, put),
		"length": sign(http.MethodPut, "/b/attachments/x", time.Minute, nil,
			map[string]string{"content-length": "101", "content-type": "image/png"}),
		"type": sign(http.MethodPut, "/b/attachments/x", time.Minute, nil,
			map[string]string{"content-length": "100", "content-type": "text/html"}),
		"disposition": sign(http.MethodPut, "/b/attachments/x", time.Minute,
			url.Values{"response-content-disposition": {"inline"}}, put),
	} {
		if other == base {
			t.Errorf("changing the %s left the signature unchanged", name)
		}
	}
}

func TestURIEncodeIsTheAWSDefinition(t *testing.T) {
	for _, c := range []struct {
		in    string
		slash bool
		want  string
	}{
		{"AZaz09-_.~", true, "AZaz09-_.~"},
		{"a b", true, "a%20b"}, // never "+"
		{"a+b", true, "a%2Bb"},
		{"a/b", true, "a%2Fb"},
		{"a/b", false, "a/b"},
		{`attachment; filename="q.pdf"`, true, "attachment%3B%20filename%3D%22q.pdf%22"},
		{"filename*=UTF-8''caf%C3%A9", true, "filename%2A%3DUTF-8%27%27caf%25C3%25A9"},
		{"é", true, "%C3%A9"}, // bytes, upper-case hex
	} {
		if got := uriEncode(c.in, c.slash); got != c.want {
			t.Errorf("uriEncode(%q, %v) = %q, want %q", c.in, c.slash, got, c.want)
		}
	}
}

// A file name with a space in it is the ordinary case, and it is the one that
// url.Values.Encode breaks: it writes "+", S3 reads a literal plus, and the
// signature no longer matches.
func TestTheQueryIsSentAsItWasSigned(t *testing.T) {
	s := newTestStore(t, "https://s3.example.test")
	got := s.PresignGet("attachments/x", time.Minute, `attachment; filename="floor plan.pdf"`, "application/octet-stream")
	if strings.Contains(got, "+") {
		t.Fatalf("a space was written as +: %s", got)
	}
	if !strings.Contains(got, "response-content-disposition=attachment%3B%20filename%3D%22floor%20plan.pdf%22") {
		t.Fatalf("disposition not in the URL as signed: %s", got)
	}
	if !strings.HasPrefix(got, "https://s3.example.test/bucket/attachments/x?") {
		t.Fatalf("not path-style: %s", got)
	}
}

// The host is signed, so it has to be the one a browser sends — and a browser
// drops a default port.
func TestADefaultPortIsNotSigned(t *testing.T) {
	for endpoint, want := range map[string]string{
		"https://s3.example.test":      "https://s3.example.test",
		"https://s3.example.test:443":  "https://s3.example.test",
		"https://s3.example.test:443/": "https://s3.example.test",
		"http://s3.example.test:80":    "http://s3.example.test",
		"http://127.0.0.1:9000":        "http://127.0.0.1:9000",
		"https://s3.example.test:8443": "https://s3.example.test:8443",
	} {
		if got := newTestStore(t, endpoint).Origin(); got != want {
			t.Errorf("Origin(%q) = %q, want %q", endpoint, got, want)
		}
	}
}

func TestPresignPutSignsLengthAndType(t *testing.T) {
	s := newTestStore(t, "https://s3.example.test")
	up := s.PresignPut("attachments/x", 1234, "image/jpeg", 15*time.Minute)

	u, err := url.Parse(up.URL)
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Query().Get("X-Amz-SignedHeaders"); got != "content-length;content-type;host" {
		t.Fatalf("SignedHeaders = %q", got)
	}
	if got := u.Query().Get("X-Amz-Expires"); got != "900" {
		t.Fatalf("Expires = %q", got)
	}
	// Content-Length is signed but not handed to the browser: a script may not
	// set it, and one that tried would have the whole request refused.
	if len(up.Headers) != 1 || up.Headers["Content-Type"] != "image/jpeg" {
		t.Fatalf("Headers = %v", up.Headers)
	}
}

func TestNewRefusesWhatCannotWork(t *testing.T) {
	good := Config{Endpoint: "https://s3.example.test", Region: "r", Bucket: "b", AccessKeyID: "i", SecretAccessKey: "s"}
	if _, err := New(good); err != nil {
		t.Fatalf("good config refused: %v", err)
	}
	for name, mutate := range map[string]func(*Config){
		"no scheme":        func(c *Config) { c.Endpoint = "s3.example.test" },
		"not http":         func(c *Config) { c.Endpoint = "s3://bucket" },
		"query":            func(c *Config) { c.Endpoint = "https://s3.example.test?x=1" },
		"credentials":      func(c *Config) { c.Endpoint = "https://u:p@s3.example.test" },
		"no region":        func(c *Config) { c.Region = "" },
		"no bucket":        func(c *Config) { c.Bucket = "" },
		"bucket is a path": func(c *Config) { c.Bucket = "a/b" },
		"no key id":        func(c *Config) { c.AccessKeyID = "" },
		"no secret":        func(c *Config) { c.SecretAccessKey = "" },
	} {
		c := good
		mutate(&c)
		if _, err := New(c); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func newTestStore(t *testing.T, endpoint string) *Store {
	t.Helper()
	s, err := New(Config{Endpoint: endpoint, Region: "nbg1", Bucket: "bucket", AccessKeyID: "id", SecretAccessKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC) }
	return s
}
