package mailer

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// trustedCA signs the certificates the fake presents where the handshake is
// meant to succeed; trustedRoots says whether this platform let us decide what
// is trusted at all.
//
// Both are settled here rather than in a test because crypto/x509 reads
// SSL_CERT_FILE once per process and keeps the pool it builds, so the first
// handshake fixes the answer for the whole binary. Going through the
// environment rather than through a field on SMTP is deliberate: tlsConfig()
// is the configuration production uses, and a test that injected a pool into
// it would no longer be evidence that production verifies anything.
var (
	trustedCA    *certAuthority
	trustedRoots bool
)

func TestMain(m *testing.M) { os.Exit(runTests(m)) }

// runTests exists so the temporary CA bundle is removed on the way out; os.Exit
// runs no deferred function.
func runTests(m *testing.M) int {
	ca, err := newCertAuthority("soiree test ca")
	if err != nil {
		log.Printf("test ca: %v", err)
		return 1
	}
	dir, err := os.MkdirTemp("", "soiree-mailer-roots")
	if err != nil {
		log.Printf("temp dir: %v", err)
		return 1
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			log.Printf("remove temp dir: %v", err)
		}
	}()

	roots := filepath.Join(dir, "roots.pem")
	if err := os.WriteFile(roots, ca.pem, 0o600); err != nil {
		log.Printf("write roots: %v", err)
		return 1
	}
	if err := os.Setenv("SSL_CERT_FILE", roots); err != nil {
		log.Printf("set SSL_CERT_FILE: %v", err)
		return 1
	}

	trustedCA = ca
	trustedRoots = systemRootsOverridden(ca)
	return m.Run()
}

// systemRootsOverridden reports whether SSL_CERT_FILE decides what this
// process trusts. It does where the default pool is read off disk; where the
// operating system's own verifier is asked instead (macOS, Windows), a
// certificate a test just minted can never be made trustworthy, and the tests
// that need a completed handshake say so and skip rather than failing on
// somebody's laptop.
func systemRootsOverridden(ca *certAuthority) bool {
	cert, err := ca.issue("probe.example.test")
	if err != nil {
		return false
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return false
	}
	// Nil roots is what tlsConfig() hands the handshake, and this is the call
	// the handshake makes with it, so the answer here is the answer there.
	_, err = leaf.Verify(x509.VerifyOptions{DNSName: "probe.example.test"})
	return err == nil
}

func requireTrustedRoots(t *testing.T) {
	t.Helper()
	if !trustedRoots {
		t.Skip("this platform ignores SSL_CERT_FILE, so a throwaway CA cannot be made trustworthy")
	}
}

// certAuthority is a throwaway CA, generated per test binary. Only the root
// reaches the disk, because that is the one thing SSL_CERT_FILE wants as a
// path; the keys live and die in memory.
type certAuthority struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newCertAuthority(name string) (*certAuthority, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("ca key: %w", err)
	}
	serial, err := serialNumber()
	if err != nil {
		return nil, err
	}
	tpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("ca certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parse ca certificate: %w", err)
	}
	return &certAuthority{
		cert: cert,
		key:  key,
		pem:  pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
	}, nil
}

// issue signs a server certificate naming host and nothing else.
func (ca *certAuthority) issue(host string) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("server key: %w", err)
	}
	serial, err := serialNumber()
	if err != nil {
		return tls.Certificate{}, err
	}
	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("server certificate: %w", err)
	}
	return tls.Certificate{Certificate: [][]byte{der, ca.cert.Raw}, PrivateKey: key}, nil
}

func serialNumber() (*big.Int, error) {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 63))
	if err != nil {
		return nil, fmt.Errorf("serial number: %w", err)
	}
	return n, nil
}

func issueFor(t *testing.T, ca *certAuthority, host string) tls.Certificate {
	t.Helper()
	cert, err := ca.issue(host)
	if err != nil {
		t.Fatalf("issue certificate for %s: %v", host, err)
	}
	return cert
}

// tlsSender points a sender at cfg.Host and cfg.Port and sends every
// connection to the fake whatever address it asked for.
//
// The dial seam is the only way into the implicit-TLS branch: it is chosen by
// the port being 465, and the fake listens on an ephemeral one. Keeping a name
// under .example.test as the host rather than 127.0.0.1 is what makes the
// credential tests mean anything: net/smtp permits a PLAIN credential in the
// clear to localhost, so a loopback host would pass whether the connection was
// encrypted or not.
func (f *fakeSMTP) tlsSender(t *testing.T, cfg Config) *SMTP {
	t.Helper()
	host, port := f.start()
	addr := net.JoinHostPort(host, strconv.Itoa(port))

	s := New(cfg)
	s.dial = func(ctx context.Context, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	}
	return s
}

// credentials for the fake to accept, and to report back as it saw them.
const (
	testUsername = "plans@example.test"
	testPassword = "hunter2"
)

// Port 465 is the path every mail a deployment sends takes, and the relay
// credential goes out on it. net/smtp sends a PLAIN credential only when it
// can see the connection is encrypted, and for implicit TLS that depends on
// Send handing smtp.NewClient the *tls.Conn rather than the socket beneath it:
// a wrapper added later for deadlines or metrics would break authentication
// against every real relay while every other test here stayed green.
func TestSendOverImplicitTLSAuthenticates(t *testing.T) {
	requireTrustedRoots(t)

	f := &fakeSMTP{t: t, implicitTLS: true}
	f.tlsConfig = f.serverTLS(issueFor(t, trustedCA, testConfig.Host))

	cfg := testConfig
	cfg.Username, cfg.Password = testUsername, testPassword
	err := f.tlsSender(t, cfg).Send(t.Context(), Message{
		To:      []string{"ada@example.test"},
		Subject: "Venue deposit",
		Text:    "Venue deposit is due on Friday.",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	// The name the certificate is checked against reaches the server as SNI,
	// so this is also what a sender that stopped setting ServerName loses.
	if f.sni != testConfig.Host {
		t.Errorf("client asked for server name %q, want %q", f.sni, testConfig.Host)
	}
	if len(f.auths) != 1 {
		t.Fatalf("server saw %d AUTH commands, want 1: %+v", len(f.auths), f.auths)
	}
	if got := f.auths[0]; got.username != testUsername || got.password != testPassword {
		t.Errorf("credentials = %q/%q, want %q/%q", got.username, got.password, testUsername, testPassword)
	}
	if !strings.Contains(f.received, "Venue deposit") {
		t.Errorf("delivered message does not carry the subject:\n%s", f.received)
	}
}

// On a submission port the credential must not leave before the upgrade, which
// is the whole reason the sender insists on STARTTLS when it has one to send.
func TestSendUpgradesBeforeAuthenticating(t *testing.T) {
	requireTrustedRoots(t)

	f := &fakeSMTP{t: t, offerStartTLS: true}
	f.tlsConfig = f.serverTLS(issueFor(t, trustedCA, testConfig.Host))

	cfg := testConfig
	cfg.Port = 587
	cfg.Username, cfg.Password = testUsername, testPassword
	err := f.tlsSender(t, cfg).Send(t.Context(), Message{
		To: []string{"ada@example.test"}, Subject: "Venue deposit", Text: "Due on Friday.",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.auths) != 1 {
		t.Fatalf("server saw %d AUTH commands, want 1: %+v", len(f.auths), f.auths)
	}
	got := f.auths[0]
	if !got.encrypted {
		t.Error("the credential arrived before the connection was upgraded")
	}
	if got.username != testUsername || got.password != testPassword {
		t.Errorf("credentials = %q/%q, want %q/%q", got.username, got.password, testUsername, testPassword)
	}
}

// A certificate that cannot be verified is the first failure an operator of
// this image meets, because the image is FROM scratch and a CA bundle has to be
// copied into it deliberately. So the error has to name that rather than the
// certificate, and nothing may be sent to a server whose identity was never
// established. That is also what fails here if somebody later switches
// certificate verification off to make a handshake problem go away.
func TestSendNamesTheMissingCABundle(t *testing.T) {
	other, err := newCertAuthority("an authority nobody trusts")
	if err != nil {
		t.Fatalf("second ca: %v", err)
	}

	for name, tc := range map[string]struct {
		cert     tls.Certificate
		port     int
		startTLS bool
	}{
		"unknown authority on the implicit-TLS port": {
			cert: issueFor(t, other, testConfig.Host), port: DefaultPort,
		},
		"certificate naming another server": {
			cert: issueFor(t, trustedCA, "elsewhere.example.test"), port: DefaultPort,
		},
		"unknown authority after the STARTTLS upgrade": {
			cert: issueFor(t, other, testConfig.Host), port: 587, startTLS: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeSMTP{t: t, implicitTLS: !tc.startTLS, offerStartTLS: tc.startTLS}
			f.tlsConfig = f.serverTLS(tc.cert)

			cfg := testConfig
			cfg.Port = tc.port
			cfg.Username, cfg.Password = testUsername, testPassword
			err := f.tlsSender(t, cfg).Send(t.Context(), Message{
				To: []string{"ada@example.test"}, Subject: "Venue deposit", Text: "Due on Friday.",
			})
			if err == nil {
				t.Fatal("Send() accepted a certificate it could not verify")
			}
			if !strings.Contains(err.Error(), "CA bundle") {
				t.Errorf("error = %v, want it to name the missing CA bundle", err)
			}
			if !strings.Contains(err.Error(), testConfig.Host) {
				t.Errorf("error = %v, want it to name the server it was talking to", err)
			}
			// Nothing was delivered, so nothing can be delivered twice by
			// trying again once the bundle is there.
			if Ambiguous(err) {
				t.Errorf("a refused certificate was reported as an uncertain delivery: %v", err)
			}

			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.auths) != 0 {
				t.Errorf("the credential went to an unverified server: %+v", f.auths)
			}
		})
	}
}
