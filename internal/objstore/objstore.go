// Package objstore is the little of S3 that attachments need: a URL the browser
// can upload to, a URL it can download from, and a way for the server to ask
// whether an object arrived and to remove it.
//
// It has no SDK behind it, on purpose. All four operations are one algorithm —
// AWS Signature Version 4 in its query-string form, a "presigned URL" — and
// that algorithm is a chain of HMAC-SHA256 over a carefully spelled string. The
// browser is given the PUT and GET URLs. The server uses the HEAD and DELETE
// URLs itself, through net/http, so there is exactly one signing routine to get
// right and one published test vector that proves it.
//
// The file bytes never pass through this process. That is the point of the
// design: the origin may be a small machine behind a home connection, the
// people using it may be on the far side of the world, and a server that
// proxies every photograph pays for each one twice on its slowest link.
package objstore

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Config is where the bucket is and who this server is to it.
type Config struct {
	// Endpoint is the S3 service's base URL, e.g.
	// https://nbg1.your-objectstorage.com. Browsers connect to it directly, so
	// it has to be reachable from wherever the people are, not merely from the
	// server.
	Endpoint string
	// Region is part of the signature. Providers that have no regions still
	// require the string they document (Hetzner: the location, e.g. "nbg1").
	Region string
	Bucket string

	AccessKeyID     string
	SecretAccessKey string
}

// ErrNotFound is Head's answer for an object that is not there.
var ErrNotFound = errors.New("objstore: no such object")

// Store signs URLs for one bucket.
type Store struct {
	cfg    Config
	scheme string
	host   string
	prefix string // path before the bucket, for an endpoint served under one
	client *http.Client
	now    func() time.Time
}

// requestTimeout bounds the server's own HEAD and DELETE calls. They carry no
// body in either direction, so a bucket that has not answered in this long is
// not going to.
const requestTimeout = 15 * time.Second

// New validates cfg and returns a Store.
func New(cfg Config) (*Store, error) {
	u, err := url.Parse(strings.TrimSpace(cfg.Endpoint))
	if err != nil {
		return nil, fmt.Errorf("objstore: endpoint: %w", err)
	}
	if (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("objstore: endpoint %q must be an http(s) URL", cfg.Endpoint)
	}
	if u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, fmt.Errorf("objstore: endpoint %q must be a bare URL", cfg.Endpoint)
	}
	if cfg.Region == "" || cfg.Bucket == "" || cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" {
		return nil, errors.New("objstore: region, bucket and both halves of the credential are required")
	}
	if strings.ContainsAny(cfg.Bucket, "/?#% ") {
		return nil, fmt.Errorf("objstore: bucket %q is not a bucket name", cfg.Bucket)
	}
	return &Store{
		cfg:    cfg,
		scheme: u.Scheme,
		host:   canonicalHost(u),
		prefix: strings.TrimSuffix(u.Path, "/"),
		client: &http.Client{
			Timeout: requestTimeout,
			// A redirect would be followed with a signature made for another
			// URL. It cannot succeed, so report where it happened instead.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		now: time.Now,
	}, nil
}

// canonicalHost is the Host header a client will actually send.
//
// The host is signed, so the string here has to be the one on the wire. A
// browser drops a default port from Host; Go keeps whatever the URL says. An
// endpoint written as https://s3.example:443 would therefore sign for one name
// and be called by another, and every browser upload would be refused with a
// signature error that no amount of staring at the credentials explains.
func canonicalHost(u *url.URL) string {
	port := u.Port()
	if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		return u.Hostname()
	}
	return u.Host
}

// Origin is the scheme and host browsers will connect to. A deployment with a
// Content-Security-Policy has to allow it in connect-src, so the server logs it
// at startup rather than leaving the operator to derive it.
func (s *Store) Origin() string { return s.scheme + "://" + s.host }

// Upload is everything a browser needs to send one file.
type Upload struct {
	URL string
	// Headers must be sent exactly as given: they are part of the signature.
	// Content-Length is signed too, but a browser sets that one itself, from
	// the body, and does not let a script touch it — which is what makes it
	// worth signing.
	Headers map[string]string
}

// PresignPut returns a URL that accepts one PUT of exactly size bytes.
//
// Size and type are signed, not merely suggested. With the length in the
// signature, a request carrying any other number of bytes no longer matches
// what was signed and the bucket refuses it, so the quota is decided before the
// upload rather than discovered after it.
func (s *Store) PresignPut(key string, size int64, contentType string, ttl time.Duration) Upload {
	headers := map[string]string{
		"content-length": strconv.FormatInt(size, 10),
		"content-type":   contentType,
	}
	return Upload{
		URL:     s.presign(http.MethodPut, key, ttl, nil, headers),
		Headers: map[string]string{"Content-Type": contentType},
	}
}

// PresignGet returns a URL that serves the object with the given
// Content-Disposition and Content-Type, whatever it was stored with.
//
// Both overrides are inside the signature, so whoever holds the URL cannot
// change "attachment" into "inline" or a download into a web page.
func (s *Store) PresignGet(key string, ttl time.Duration, disposition, contentType string) string {
	q := url.Values{}
	if disposition != "" {
		q.Set("response-content-disposition", disposition)
	}
	if contentType != "" {
		q.Set("response-content-type", contentType)
	}
	return s.presign(http.MethodGet, key, ttl, q, nil)
}

// Head reports the stored size of an object, or ErrNotFound.
func (s *Store) Head(ctx context.Context, key string) (int64, error) {
	res, err := s.do(ctx, http.MethodHead, key)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == http.StatusNotFound:
		return 0, ErrNotFound
	case res.StatusCode != http.StatusOK:
		return 0, fmt.Errorf("objstore: HEAD %s: %s", key, res.Status)
	}
	if res.ContentLength < 0 {
		return 0, fmt.Errorf("objstore: HEAD %s: no Content-Length", key)
	}
	return res.ContentLength, nil
}

// Delete removes an object. Removing one that is not there is not an error:
// S3 answers 204 either way, and the callers — a sweeper working through a
// queue it may have half-finished before — depend on being able to ask twice.
func (s *Store) Delete(ctx context.Context, key string) error {
	res, err := s.do(ctx, http.MethodDelete, key)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 && res.StatusCode != http.StatusNotFound {
		return fmt.Errorf("objstore: DELETE %s: %s", key, res.Status)
	}
	return nil
}

func (s *Store) do(ctx context.Context, method, key string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, s.presign(method, key, time.Minute, nil, nil), nil)
	if err != nil {
		return nil, fmt.Errorf("objstore: %s %s: %w", method, key, err)
	}
	res, err := s.client.Do(req)
	if err != nil {
		// The URL carries the signature. It is short-lived, but it has no
		// business in a log line either.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("objstore: %s %s: %w", method, key, err)
	}
	return res, nil
}

// presign addresses the object path-style — https://endpoint/bucket/key — so
// the only host a browser ever talks to is the endpoint itself, and a
// Content-Security-Policy can name it without naming the bucket.
func (s *Store) presign(method, key string, ttl time.Duration, query url.Values, headers map[string]string) string {
	path := s.prefix + "/" + s.cfg.Bucket + "/" + key
	return presignURL(method, s.scheme, s.host, path, s.cfg.Region, s.cfg.AccessKeyID, s.cfg.SecretAccessKey,
		s.now().UTC(), ttl, query, headers)
}

const (
	algorithm = "AWS4-HMAC-SHA256"
	service   = "s3"
	// A presigned URL cannot carry a hash of a body that does not exist yet.
	// This literal is what S3 defines for that case.
	unsignedPayload = "UNSIGNED-PAYLOAD"
)

// presignURL is Signature Version 4, query-string form.
//
// Kept free of Store so the test can feed it AWS's own published example —
// which is addressed virtual-host-style, unlike anything Store produces — and
// compare signatures. Every step below is one paragraph of
// https://docs.aws.amazon.com/AmazonS3/latest/API/sigv4-query-string-auth.html.
func presignURL(method, scheme, host, path, region, accessKey, secretKey string,
	now time.Time, ttl time.Duration, query url.Values, headers map[string]string) string {

	date := now.Format("20060102")
	stamp := now.Format("20060102T150405Z")
	scope := date + "/" + region + "/" + service + "/aws4_request"

	// host is always signed; the caller's headers join it, lower-cased.
	signed := map[string]string{"host": host}
	for name, value := range headers {
		signed[strings.ToLower(name)] = strings.Join(strings.Fields(value), " ")
	}
	names := make([]string, 0, len(signed))
	for name := range signed {
		names = append(names, name)
	}
	sort.Strings(names)
	var canonicalHeaders strings.Builder
	for _, name := range names {
		canonicalHeaders.WriteString(name + ":" + signed[name] + "\n")
	}
	signedHeaders := strings.Join(names, ";")

	q := url.Values{}
	for name, values := range query {
		q[name] = values
	}
	q.Set("X-Amz-Algorithm", algorithm)
	q.Set("X-Amz-Credential", accessKey+"/"+scope)
	q.Set("X-Amz-Date", stamp)
	q.Set("X-Amz-Expires", strconv.Itoa(int(ttl/time.Second)))
	q.Set("X-Amz-SignedHeaders", signedHeaders)
	canonicalQuery := canonicalQueryString(q)

	canonicalRequest := strings.Join([]string{
		method,
		uriEncode(path, false),
		canonicalQuery,
		canonicalHeaders.String(),
		signedHeaders,
		unsignedPayload,
	}, "\n")

	sum := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := strings.Join([]string{algorithm, stamp, scope, hex.EncodeToString(sum[:])}, "\n")

	k := hmacSHA256([]byte("AWS4"+secretKey), date)
	k = hmacSHA256(k, region)
	k = hmacSHA256(k, service)
	k = hmacSHA256(k, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(k, stringToSign))

	// The query goes out spelled exactly as it was signed. url.Values.Encode
	// would write a space as "+", S3 reads that as a literal plus, and the
	// signature stops matching for the first file name with a space in it.
	return scheme + "://" + host + uriEncode(path, false) + "?" + canonicalQuery + "&X-Amz-Signature=" + signature
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

func canonicalQueryString(q url.Values) string {
	names := make([]string, 0, len(q))
	for name := range q {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		values := append([]string(nil), q[name]...)
		sort.Strings(values)
		for _, value := range values {
			parts = append(parts, uriEncode(name, true)+"="+uriEncode(value, true))
		}
	}
	return strings.Join(parts, "&")
}

// uriEncode is AWS's definition, which is neither url.QueryEscape nor
// url.PathEscape: everything but A-Z a-z 0-9 - _ . ~ becomes %XX in upper-case
// hex, a space is %20 and never "+", and "/" survives only in a path.
func uriEncode(s string, encodeSlash bool) string {
	const upperHex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		case c == '/' && !encodeSlash:
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(upperHex[c>>4])
			b.WriteByte(upperHex[c&0x0F])
		}
	}
	return b.String()
}
