package httpd

// The passkey surface, driven end to end by a software authenticator.
//
// There is no way to test this halfway. A mock that hands back a signature the
// library does not check is testing the mock; the properties worth asserting —
// that a challenge cannot be replayed, that a counter that fails to advance is
// refused, that a credential belongs to one account — are all properties of the
// real ceremony. So softAuthenticator below is a real one: it holds a P-256 key,
// builds authenticator data and an attestation object, and signs. Everything it
// produces goes through the same parser and the same verification as a browser's
// output would, because it is the same code.
//
// What it is not is a browser. It never calls navigator.credentials, so nothing
// here covers the JavaScript side of the contract — the base64url encoding and
// decoding at the boundary is asserted from the server's side only.

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"

	"github.com/Yornik/soiree/internal/config"
	"github.com/Yornik/soiree/internal/migrate"
	"github.com/Yornik/soiree/internal/pgtest"
	"github.com/Yornik/soiree/internal/store"
)

// The deployment under test. An apex with a www that redirects to it, which is
// the shape the RP ID derivation exists for.
const (
	passkeyRPID   = "soiree.example.test"
	passkeyOrigin = "https://soiree.example.test"
)

// Authenticator data flags, §6.1. Named rather than inlined because a wrong bit
// here produces a verification failure three layers away.
const (
	flagUserPresent    byte = 0x01
	flagUserVerified   byte = 0x04
	flagBackupEligible byte = 0x08
	flagBackupState    byte = 0x10
	flagAttestedData   byte = 0x40
)

// --- a software authenticator ------------------------------------------------

// softAuthenticator is a phone, or a security key, or a laptop's TPM.
//
// It holds one credential: a P-256 key pair, an identifier, and a counter. The
// counter is a plain field rather than something this increments on its own,
// because half these tests are about what happens when it does not advance.
type softAuthenticator struct {
	key    *ecdsa.PrivateKey
	credID []byte
	aaguid []byte

	// signCount is what the next assertion will report. A real authenticator
	// either increments it or reports 0 forever; both are legal, and both are
	// exercised below.
	signCount uint32

	backupEligible bool
	backupState    bool
	userVerified   bool

	// clientExtensions is what the browser reports as clientExtensionResults.
	// Nil is the empty object every browser sends when it has nothing to say;
	// the interesting values are the ones a browser sends without being asked.
	clientExtensions map[string]any
}

func newSoftAuthenticator(t *testing.T, credID string) *softAuthenticator {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate authenticator key: %v", err)
	}
	return &softAuthenticator{
		key:    key,
		credID: []byte(credID),
		// Sixteen bytes, as the specification requires. Obviously not a real
		// authenticator model.
		aaguid: []byte("soiree-testauth\x00"),
		// A synced passkey: eligible for backup and backed up. The pair has to
		// stay consistent across registration and every later assertion or the
		// library refuses the login, which is the whole reason the flags are
		// stored rather than recomputed.
		backupEligible: true,
		backupState:    true,
		userVerified:   true,
	}
}

func (s *softAuthenticator) flags(attested bool) byte {
	f := flagUserPresent
	if s.userVerified {
		f |= flagUserVerified
	}
	if s.backupEligible {
		f |= flagBackupEligible
		if s.backupState {
			f |= flagBackupState
		}
	}
	if attested {
		f |= flagAttestedData
	}
	return f
}

// coseKey encodes the public key as the COSE_Key the specification puts in the
// attested credential data: kty EC2, alg ES256, curve P-256, and the two
// coordinates as fixed 32-byte strings.
func (s *softAuthenticator) coseKey(t *testing.T) []byte {
	t.Helper()
	em, err := cbor.CanonicalEncOptions().EncMode()
	if err != nil {
		t.Fatalf("cbor encoder: %v", err)
	}
	// Bytes() is the uncompressed point: 0x04, then the two 32-byte
	// coordinates. Reading them out of it rather than off the big.Int fields,
	// which are deprecated and are not a safe way to handle a key.
	point, err := s.key.PublicKey.Bytes()
	if err != nil {
		t.Fatalf("encode public key: %v", err)
	}
	if len(point) != 65 || point[0] != 4 {
		t.Fatalf("public key is %d bytes starting %#x, want an uncompressed P-256 point", len(point), point[0])
	}

	out, err := em.Marshal(map[int]any{
		1:  2,            // kty: EC2
		3:  -7,           // alg: ES256
		-1: 1,            // crv: P-256
		-2: point[1:33],  // x
		-3: point[33:65], // y
	})
	if err != nil {
		t.Fatalf("encode cose key: %v", err)
	}
	return out
}

// authData builds the authenticator data: the RP ID hash the server will
// compare against its own, the flags, the counter, and — at registration only —
// the attested credential data carrying the new public key.
func (s *softAuthenticator) authData(t *testing.T, attested bool) []byte {
	t.Helper()
	h := sha256.Sum256([]byte(passkeyRPID))

	out := append([]byte{}, h[:]...)
	out = append(out, s.flags(attested))

	var counter [4]byte
	binary.BigEndian.PutUint32(counter[:], s.signCount)
	out = append(out, counter[:]...)

	if attested {
		out = append(out, s.aaguid...)
		var idLen [2]byte
		binary.BigEndian.PutUint16(idLen[:], uint16(len(s.credID)))
		out = append(out, idLen[:]...)
		out = append(out, s.credID...)
		out = append(out, s.coseKey(t)...)
	}
	return out
}

// clientData is what the browser collects and the authenticator signs over.
func clientData(t *testing.T, ceremony, challenge, origin string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"type":        ceremony,
		"challenge":   challenge,
		"origin":      origin,
		"crossOrigin": false,
	})
	if err != nil {
		t.Fatalf("encode client data: %v", err)
	}
	return raw
}

// register produces what navigator.credentials.create() would hand back.
//
// Attestation format "none": no attestation statement, which is what this
// deployment asks for and what a platform authenticator gives by default.
func (s *softAuthenticator) register(t *testing.T, challenge, origin string) json.RawMessage {
	t.Helper()
	em, err := cbor.CanonicalEncOptions().EncMode()
	if err != nil {
		t.Fatalf("cbor encoder: %v", err)
	}
	attestation, err := em.Marshal(map[string]any{
		"fmt":      "none",
		"attStmt":  map[string]any{},
		"authData": s.authData(t, true),
	})
	if err != nil {
		t.Fatalf("encode attestation object: %v", err)
	}

	return rawJSON(t, map[string]any{
		"id":                      b64(s.credID),
		"rawId":                   b64(s.credID),
		"type":                    "public-key",
		"authenticatorAttachment": "platform",
		"clientExtensionResults":  s.extensionResults(),
		"response": map[string]any{
			"clientDataJSON":     b64(clientData(t, "webauthn.create", challenge, origin)),
			"attestationObject":  b64(attestation),
			"transports":         []string{"internal", "hybrid"},
			"publicKeyAlgorithm": -7,
		},
	})
}

// assert produces what navigator.credentials.get() would hand back: the
// authenticator data and a signature over it and the hash of the client data.
func (s *softAuthenticator) assert(t *testing.T, challenge, origin string, userHandle []byte) json.RawMessage {
	t.Helper()

	cd := clientData(t, "webauthn.get", challenge, origin)
	ad := s.authData(t, false)

	cdHash := sha256.Sum256(cd)
	digest := sha256.Sum256(append(append([]byte{}, ad...), cdHash[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, s.key, digest[:])
	if err != nil {
		t.Fatalf("sign assertion: %v", err)
	}

	return rawJSON(t, map[string]any{
		"id":                      b64(s.credID),
		"rawId":                   b64(s.credID),
		"type":                    "public-key",
		"authenticatorAttachment": "platform",
		"clientExtensionResults":  s.extensionResults(),
		"response": map[string]any{
			"clientDataJSON":    b64(cd),
			"authenticatorData": b64(ad),
			"signature":         b64(sig),
			"userHandle":        b64(userHandle),
		},
	})
}

func (s *softAuthenticator) extensionResults() map[string]any {
	if s.clientExtensions == nil {
		return map[string]any{}
	}
	return s.clientExtensions
}

// b64 is the one encoding the whole protocol uses on the wire: base64url,
// unpadded.
func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func unb64(t *testing.T, s string) []byte {
	t.Helper()
	// Tolerant of padding on the way in, as the server is.
	out, err := base64.RawURLEncoding.DecodeString(trimPadding(s))
	if err != nil {
		t.Fatalf("decode %q: %v", s, err)
	}
	return out
}

func trimPadding(s string) string {
	for len(s) > 0 && s[len(s)-1] == '=' {
		s = s[:len(s)-1]
	}
	return s
}

func rawJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

// --- the fixture -------------------------------------------------------------

type passkeyFixture struct {
	h     http.Handler
	a     *Auth
	store *store.Store
}

func newPasskeyFixture(t *testing.T) *passkeyFixture {
	t.Helper()

	pool := pgtest.Pool(t)
	if _, err := migrate.Run(t.Context(), pool, nil); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(pool)

	a := NewAuth(AuthOptions{Store: st, BaseURL: passkeyOrigin})
	a.params = cheapParams
	a.background = func(fn func(context.Context)) { fn(context.Background()) }
	if err := a.WithPasskeys(passkeyConfig()); err != nil {
		t.Fatalf("enable passkeys: %v", err)
	}

	mux := http.NewServeMux()
	a.Register(mux)
	return &passkeyFixture{h: mux, a: a, store: st}
}

func passkeyConfig() config.Config {
	return config.Config{
		EventName:       "A Celebration",
		BaseURL:         passkeyOrigin,
		PasskeysEnabled: true,
		PasskeyRPID:     passkeyRPID,
		PasskeyOrigin:   passkeyOrigin,
	}
}

func (f *passkeyFixture) do(t *testing.T, method, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()

	var r *http.Request
	if body == nil {
		r = httptest.NewRequest(method, path, nil)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		r = httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}

	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, r)
	return rec
}

func (f *passkeyFixture) seed(t *testing.T, email string, role store.Role, password string) store.User {
	t.Helper()
	hash, err := f.a.params.Hash(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	u, err := f.store.CreateUser(t.Context(), store.User{
		Email: email, Role: role, Status: store.StatusActive, PasswordHash: &hash,
	})
	if err != nil {
		t.Fatalf("seed %s: %v", email, err)
	}
	return u
}

func (f *passkeyFixture) login(t *testing.T, email, password string) *http.Cookie {
	t.Helper()
	rec := f.do(t, http.MethodPost, "/api/v1/auth/login",
		map[string]string{"email": email, "password": password}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("login as %s: status %d, body %s", email, rec.Code, rec.Body)
	}
	return sessionCookieFrom(t, rec)
}

func sessionCookieFrom(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName && c.Value != "" {
			return c
		}
	}
	t.Fatalf("no session cookie was set; body %s", rec.Body)
	return nil
}

// creationOptions is the register/begin response as the browser reads it.
type creationOptions struct {
	PublicKey struct {
		Challenge string `json:"challenge"`
		RP        struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"rp"`
		User struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
		} `json:"user"`
		PubKeyCredParams []struct {
			Type string `json:"type"`
			Alg  int    `json:"alg"`
		} `json:"pubKeyCredParams"`
		ExcludeCredentials []struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		} `json:"excludeCredentials"`
		AuthenticatorSelection struct {
			ResidentKey        string `json:"residentKey"`
			RequireResidentKey bool   `json:"requireResidentKey"`
			UserVerification   string `json:"userVerification"`
		} `json:"authenticatorSelection"`
		Attestation string `json:"attestation"`
		Timeout     int    `json:"timeout"`
	} `json:"publicKey"`
	Mediation string `json:"mediation"`
}

// requestOptions is the login/begin response.
type requestOptions struct {
	PublicKey struct {
		Challenge        string          `json:"challenge"`
		RPID             string          `json:"rpId"`
		UserVerification string          `json:"userVerification"`
		Timeout          int             `json:"timeout"`
		AllowCredentials json.RawMessage `json:"allowCredentials"`
	} `json:"publicKey"`
	Mediation string `json:"mediation"`
}

// registerPasskey runs a whole registration ceremony and returns the credential
// as the API reports it, along with the account's user handle.
func (f *passkeyFixture) registerPasskey(t *testing.T, cookie *http.Cookie, auth *softAuthenticator, label string) (passkeyDTO, []byte) {
	t.Helper()

	rec := f.do(t, http.MethodPost, "/api/v1/auth/passkeys/register/begin", nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("register/begin: status %d, body %s", rec.Code, rec.Body)
	}
	opts := decodeTestBody[creationOptions](t, rec)

	rec = f.do(t, http.MethodPost, "/api/v1/auth/passkeys/register/finish", map[string]any{
		"label":      label,
		"credential": auth.register(t, opts.PublicKey.Challenge, passkeyOrigin),
	}, cookie)
	if rec.Code != http.StatusCreated {
		t.Fatalf("register/finish: status %d, body %s", rec.Code, rec.Body)
	}
	return decodeTestBody[passkeyDTO](t, rec), unb64(t, opts.PublicKey.User.ID)
}

// loginWithPasskey runs a whole usernameless login ceremony.
func (f *passkeyFixture) loginWithPasskey(t *testing.T, auth *softAuthenticator, handle []byte) *httptest.ResponseRecorder {
	t.Helper()

	rec := f.do(t, http.MethodPost, "/api/v1/auth/passkeys/login/begin", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("login/begin: status %d, body %s", rec.Code, rec.Body)
	}
	opts := decodeTestBody[requestOptions](t, rec)

	return f.do(t, http.MethodPost, "/api/v1/auth/passkeys/login/finish", map[string]any{
		"credential": auth.assert(t, opts.PublicKey.Challenge, passkeyOrigin, handle),
	}, nil)
}

// --- routes exist, or do not -------------------------------------------------

// Disabled means the paths are not routes. A handler that was never mounted has
// nothing to get wrong, and the browser is told the same thing by the client
// config, so it never offers the button in the first place.
func TestPasskeyRoutesAreUnmountedWhenDisabled(t *testing.T) {
	a := NewAuth(AuthOptions{})
	mux := http.NewServeMux()
	a.Register(mux)

	for _, req := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/auth/passkeys/register/begin"},
		{http.MethodPost, "/api/v1/auth/passkeys/register/finish"},
		{http.MethodPost, "/api/v1/auth/passkeys/login/begin"},
		{http.MethodPost, "/api/v1/auth/passkeys/login/finish"},
		{http.MethodGet, "/api/v1/auth/passkeys"},
		{http.MethodDelete, "/api/v1/auth/passkeys/" + "00000000-0000-0000-0000-000000000001"},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(req.method, req.path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d with passkeys off, want 404", req.method, req.path, rec.Code)
		}
	}
}

// And the password login is untouched by any of this: the routes it needs are
// still there whether or not passkeys are.
func TestPasswordRoutesSurvivePasskeysBeingOff(t *testing.T) {
	a := NewAuth(AuthOptions{})
	mux := http.NewServeMux()
	a.Register(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/v1/auth/session = %d, want 401 — the accounts surface should be unchanged", rec.Code)
	}
}

func TestPasskeyRoutesAreMountedWhenEnabled(t *testing.T) {
	a := NewAuth(AuthOptions{})
	if err := a.WithPasskeys(passkeyConfig()); err != nil {
		t.Fatalf("enable passkeys: %v", err)
	}
	mux := http.NewServeMux()
	a.Register(mux)

	// Unauthenticated, so this stops in the middleware and never reaches the
	// database — which is what makes it a 401 rather than a 404, and lets this
	// run without Docker.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/passkeys/register/begin", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("register/begin unauthenticated = %d, want 401 (mounted, and refusing)", rec.Code)
	}
}

func TestPasskeysStayOffWithoutARelyingParty(t *testing.T) {
	a := NewAuth(AuthOptions{})
	if err := a.WithPasskeys(config.Config{PasskeysEnabled: false}); err != nil {
		t.Fatalf("WithPasskeys with passkeys off: %v", err)
	}
	if a.passkeys != nil {
		t.Fatal("passkeys were built even though the configuration says they are off")
	}

	// Enabled but with nothing derived is a caller bug, and it is reported
	// rather than silently producing a relying party of "".
	if err := a.WithPasskeys(config.Config{PasskeysEnabled: true}); err == nil {
		t.Fatal("passkeys were enabled with no relying party id")
	}
}

// --- the ceremonies ----------------------------------------------------------

func TestPasskeyRegistrationAndLogin(t *testing.T) {
	f := newPasskeyFixture(t)
	ada := f.seed(t, "ada@example.test", store.RoleEditor, goodPassword)
	cookie := f.login(t, "ada@example.test", goodPassword)

	device := newSoftAuthenticator(t, "ada-phone")
	dto, handle := f.registerPasskey(t, cookie, device, "Ada's phone")

	if dto.Label != "Ada's phone" {
		t.Errorf("label = %q, want what she called it", dto.Label)
	}
	if dto.LastUsedAt != nil {
		t.Errorf("lastUsedAt = %v, want null before first use", dto.LastUsedAt)
	}
	if len(dto.Transports) == 0 {
		t.Error("transports came back empty; the browser reported two")
	}
	// The user handle is the account id and not the address: it is stored on
	// the authenticator and travels with every assertion.
	if !bytes.Equal(handle, ada.ID[:]) {
		t.Errorf("user handle = %x, want the account id %x", handle, ada.ID[:])
	}

	// Now log in from a cold start — no session cookie anywhere, no address
	// typed. This is the ceremony passkeys exist for.
	device.signCount = 1
	rec := f.loginWithPasskey(t, device, handle)
	if rec.Code != http.StatusOK {
		t.Fatalf("login/finish: status %d, body %s", rec.Code, rec.Body)
	}

	// The session it issues is the same session a password issues, because it
	// is made by the same function.
	cookie = sessionCookieFrom(t, rec)
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("passkey session cookie = %+v, want the same attributes a password login sets", cookie)
	}
	who := decodeTestBody[userDTO](t, rec)
	if who.ID != ada.ID || who.Email != "ada@example.test" {
		t.Errorf("login returned %+v, want Ada", who)
	}

	// And it works: the cookie authenticates.
	rec = f.do(t, http.MethodGet, "/api/v1/auth/session", nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("the passkey session does not authenticate: status %d, body %s", rec.Code, rec.Body)
	}

	// The use was recorded.
	rows, err := f.store.PasskeyCredentials(t.Context(), ada.ID)
	if err != nil {
		t.Fatalf("read passkeys: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("Ada has %d passkeys, want 1", len(rows))
	}
	if rows[0].LastUsedAt == nil {
		t.Error("lastUsedAt was not recorded")
	}
	if rows[0].SignCount != 1 {
		t.Errorf("sign count = %d, want the 1 the authenticator reported", rows[0].SignCount)
	}
}

// A passkey login answers with the body a password login answers with, down
// to the event a deployment keeps out of the page it serves everybody. A page
// that has just signed in does not ask again, so without this it would stay
// nameless and without a countdown until somebody reloaded it.
func TestAPasskeyLoginSaysWhoseEveningThisIs(t *testing.T) {
	f := newPasskeyFixture(t)
	f.a.event = &config.EventDetails{
		Name:    "Ada's Retirement",
		Tagline: "Dinner and speeches",
		Date:    "2030-01-13T00:00:00+09:00",
	}
	f.seed(t, "ada@example.test", store.RoleEditor, goodPassword)
	cookie := f.login(t, "ada@example.test", goodPassword)

	device := newSoftAuthenticator(t, "ada-phone")
	_, handle := f.registerPasskey(t, cookie, device, "Ada's phone")

	device.signCount = 1
	rec := f.loginWithPasskey(t, device, handle)
	if rec.Code != http.StatusOK {
		t.Fatalf("login/finish: status %d, body %s", rec.Code, rec.Body)
	}
	ev := decodeTestBody[sessionJSON](t, rec).Event
	if ev == nil {
		t.Fatal("a passkey login says nothing about the event; a password login does")
	}
	if *ev != *f.a.event {
		t.Errorf("it carries %+v, want the event as configured", *ev)
	}
}

// The options handed to the browser are the contract the frontend is written
// from. Pin the parts a frontend would break on.
func TestPasskeyRegisterBeginOptions(t *testing.T) {
	f := newPasskeyFixture(t)
	f.seed(t, "ada@example.test", store.RoleEditor, goodPassword)
	cookie := f.login(t, "ada@example.test", goodPassword)

	rec := f.do(t, http.MethodPost, "/api/v1/auth/passkeys/register/begin", nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("register/begin: status %d, body %s", rec.Code, rec.Body)
	}
	opts := decodeTestBody[creationOptions](t, rec)

	if opts.PublicKey.RP.ID != passkeyRPID {
		t.Errorf("rp.id = %q, want the derived relying party %q", opts.PublicKey.RP.ID, passkeyRPID)
	}
	// A challenge shorter than 16 bytes is one the specification refuses.
	if got := unb64(t, opts.PublicKey.Challenge); len(got) < 16 {
		t.Errorf("challenge is %d bytes, want at least 16", len(got))
	}
	// Discoverable, or usernameless login cannot work.
	if opts.PublicKey.AuthenticatorSelection.ResidentKey != "required" ||
		!opts.PublicKey.AuthenticatorSelection.RequireResidentKey {
		t.Errorf("authenticatorSelection = %+v, want a discoverable credential",
			opts.PublicKey.AuthenticatorSelection)
	}
	if opts.PublicKey.Attestation != "none" {
		t.Errorf("attestation = %q, want none", opts.PublicKey.Attestation)
	}
	var hasES256 bool
	for _, p := range opts.PublicKey.PubKeyCredParams {
		if p.Alg == -7 {
			hasES256 = true
		}
	}
	if !hasES256 {
		t.Error("pubKeyCredParams does not offer ES256")
	}
	// Mediation is a sibling of publicKey and is absent by default. A frontend
	// that finds it inside publicKey has been given the wrong shape.
	if opts.Mediation != "" {
		t.Errorf("mediation = %q, want it absent", opts.Mediation)
	}
}

// Registering the same device twice is refused by the browser, which needs to
// be told what is already there to do it.
func TestPasskeyRegisterBeginExcludesWhatIsAlreadyRegistered(t *testing.T) {
	f := newPasskeyFixture(t)
	f.seed(t, "ada@example.test", store.RoleEditor, goodPassword)
	cookie := f.login(t, "ada@example.test", goodPassword)

	device := newSoftAuthenticator(t, "ada-phone")
	f.registerPasskey(t, cookie, device, "phone")

	rec := f.do(t, http.MethodPost, "/api/v1/auth/passkeys/register/begin", nil, cookie)
	opts := decodeTestBody[creationOptions](t, rec)
	if len(opts.PublicKey.ExcludeCredentials) != 1 {
		t.Fatalf("excludeCredentials has %d entries, want the one already registered",
			len(opts.PublicKey.ExcludeCredentials))
	}
	if got := unb64(t, opts.PublicKey.ExcludeCredentials[0].ID); !bytes.Equal(got, device.credID) {
		t.Errorf("excludeCredentials[0].id = %x, want %x", got, device.credID)
	}
}

// A credential id is registered once, to one account. A second registration is
// refused without saying whose the first one was.
func TestPasskeyCannotBeRegisteredTwice(t *testing.T) {
	f := newPasskeyFixture(t)
	f.seed(t, "ada@example.test", store.RoleEditor, goodPassword)
	f.seed(t, "grace@example.test", store.RoleEditor, otherPassword)

	adaCookie := f.login(t, "ada@example.test", goodPassword)
	device := newSoftAuthenticator(t, "shared-device")
	f.registerPasskey(t, adaCookie, device, "phone")

	graceCookie := f.login(t, "grace@example.test", otherPassword)
	rec := f.do(t, http.MethodPost, "/api/v1/auth/passkeys/register/begin", nil, graceCookie)
	opts := decodeTestBody[creationOptions](t, rec)

	rec = f.do(t, http.MethodPost, "/api/v1/auth/passkeys/register/finish", map[string]any{
		"label":      "stolen",
		"credential": device.register(t, opts.PublicKey.Challenge, passkeyOrigin),
	}, graceCookie)
	if rec.Code != http.StatusConflict {
		t.Fatalf("registering Ada's credential to Grace: status %d, body %s", rec.Code, rec.Body)
	}
}

// --- challenges --------------------------------------------------------------

// The single most important property here. A replayed assertion is a recording
// of somebody logging in, and if it works then the signature proves nothing.
func TestPasskeyLoginChallengeIsSingleUse(t *testing.T) {
	f := newPasskeyFixture(t)
	f.seed(t, "ada@example.test", store.RoleEditor, goodPassword)
	cookie := f.login(t, "ada@example.test", goodPassword)

	device := newSoftAuthenticator(t, "ada-phone")
	_, handle := f.registerPasskey(t, cookie, device, "phone")

	rec := f.do(t, http.MethodPost, "/api/v1/auth/passkeys/login/begin", nil, nil)
	opts := decodeTestBody[requestOptions](t, rec)

	device.signCount = 1
	body := map[string]any{
		"credential": device.assert(t, opts.PublicKey.Challenge, passkeyOrigin, handle),
	}

	first := f.do(t, http.MethodPost, "/api/v1/auth/passkeys/login/finish", body, nil)
	if first.Code != http.StatusOK {
		t.Fatalf("first login: status %d, body %s", first.Code, first.Body)
	}

	// Byte for byte the same request again.
	replay := f.do(t, http.MethodPost, "/api/v1/auth/passkeys/login/finish", body, nil)
	if replay.Code != http.StatusUnauthorized {
		t.Fatalf("replaying the assertion: status %d, body %s — the challenge must be spent",
			replay.Code, replay.Body)
	}
	if sessionCookieName != "" {
		for _, c := range replay.Result().Cookies() {
			if c.Name == sessionCookieName && c.Value != "" {
				t.Fatal("a replayed assertion issued a session")
			}
		}
	}
}

func TestPasskeyRegistrationChallengeIsSingleUse(t *testing.T) {
	f := newPasskeyFixture(t)
	f.seed(t, "ada@example.test", store.RoleEditor, goodPassword)
	cookie := f.login(t, "ada@example.test", goodPassword)

	rec := f.do(t, http.MethodPost, "/api/v1/auth/passkeys/register/begin", nil, cookie)
	opts := decodeTestBody[creationOptions](t, rec)

	device := newSoftAuthenticator(t, "ada-phone")
	body := map[string]any{
		"label":      "phone",
		"credential": device.register(t, opts.PublicKey.Challenge, passkeyOrigin),
	}

	if rec := f.do(t, http.MethodPost, "/api/v1/auth/passkeys/register/finish", body, cookie); rec.Code != http.StatusCreated {
		t.Fatalf("first finish: status %d, body %s", rec.Code, rec.Body)
	}
	replay := f.do(t, http.MethodPost, "/api/v1/auth/passkeys/register/finish", body, cookie)
	if replay.Code != http.StatusBadRequest {
		t.Fatalf("replaying the registration: status %d, body %s", replay.Code, replay.Body)
	}
}

// A challenge that has aged out is refused for the same reason and with the
// same answer as one that was never issued.
func TestPasskeyChallengeExpires(t *testing.T) {
	f := newPasskeyFixture(t)
	f.seed(t, "ada@example.test", store.RoleEditor, goodPassword)
	cookie := f.login(t, "ada@example.test", goodPassword)

	device := newSoftAuthenticator(t, "ada-phone")
	_, handle := f.registerPasskey(t, cookie, device, "phone")

	// Begin the ceremony as though it had happened long enough ago that its
	// challenge has expired. The expiry is written from this clock and checked
	// against the database's, so this is the real path and not a shortcut.
	f.a.now = func() time.Time { return time.Now().Add(-2 * passkeyChallengeTTL) }
	rec := f.do(t, http.MethodPost, "/api/v1/auth/passkeys/login/begin", nil, nil)
	opts := decodeTestBody[requestOptions](t, rec)
	f.a.now = time.Now

	device.signCount = 1
	rec = f.do(t, http.MethodPost, "/api/v1/auth/passkeys/login/finish", map[string]any{
		"credential": device.assert(t, opts.PublicKey.Challenge, passkeyOrigin, handle),
	}, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("an expired challenge was accepted: status %d, body %s", rec.Code, rec.Body)
	}
}

// The two ceremonies do not share a pool of challenges, or each endpoint could
// be finished with the other's.
func TestPasskeyChallengesAreBoundToTheirCeremony(t *testing.T) {
	f := newPasskeyFixture(t)
	f.seed(t, "ada@example.test", store.RoleEditor, goodPassword)
	cookie := f.login(t, "ada@example.test", goodPassword)

	device := newSoftAuthenticator(t, "ada-phone")
	_, handle := f.registerPasskey(t, cookie, device, "phone")

	// A registration challenge, presented to the login endpoint.
	rec := f.do(t, http.MethodPost, "/api/v1/auth/passkeys/register/begin", nil, cookie)
	creation := decodeTestBody[creationOptions](t, rec)

	device.signCount = 1
	rec = f.do(t, http.MethodPost, "/api/v1/auth/passkeys/login/finish", map[string]any{
		"credential": device.assert(t, creation.PublicKey.Challenge, passkeyOrigin, handle),
	}, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("a registration challenge logged somebody in: status %d, body %s", rec.Code, rec.Body)
	}
}

// A registration belongs to whoever began it. Without that, somebody signed in
// could finish a ceremony another person started and end up with a credential
// attached to the wrong account.
func TestPasskeyRegistrationIsBoundToWhoBeganIt(t *testing.T) {
	f := newPasskeyFixture(t)
	f.seed(t, "ada@example.test", store.RoleEditor, goodPassword)
	grace := f.seed(t, "grace@example.test", store.RoleEditor, otherPassword)

	adaCookie := f.login(t, "ada@example.test", goodPassword)
	graceCookie := f.login(t, "grace@example.test", otherPassword)

	// Ada begins.
	rec := f.do(t, http.MethodPost, "/api/v1/auth/passkeys/register/begin", nil, adaCookie)
	opts := decodeTestBody[creationOptions](t, rec)

	// Grace finishes, with her own session and Ada's challenge.
	device := newSoftAuthenticator(t, "grace-phone")
	rec = f.do(t, http.MethodPost, "/api/v1/auth/passkeys/register/finish", map[string]any{
		"label":      "grace's",
		"credential": device.register(t, opts.PublicKey.Challenge, passkeyOrigin),
	}, graceCookie)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("Grace finished Ada's ceremony: status %d, body %s", rec.Code, rec.Body)
	}

	rows, err := f.store.PasskeyCredentials(t.Context(), grace.ID)
	if err != nil {
		t.Fatalf("read passkeys: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("Grace ended up with %d passkeys from somebody else's ceremony", len(rows))
	}
}

// --- the signature counter ---------------------------------------------------

// The cloned-authenticator signal: a counter that fails to advance means two
// things holding the same private key are in use.
func TestPasskeyNonIncreasingSignCounterIsRejected(t *testing.T) {
	f := newPasskeyFixture(t)
	ada := f.seed(t, "ada@example.test", store.RoleEditor, goodPassword)
	cookie := f.login(t, "ada@example.test", goodPassword)

	device := newSoftAuthenticator(t, "ada-phone")
	_, handle := f.registerPasskey(t, cookie, device, "phone")

	// Two honest logins, counting up.
	for _, count := range []uint32{1, 2} {
		device.signCount = count
		if rec := f.loginWithPasskey(t, device, handle); rec.Code != http.StatusOK {
			t.Fatalf("login at counter %d: status %d, body %s", count, rec.Code, rec.Body)
		}
	}

	// A third, from something reporting a counter it has already used. Every
	// signature is valid; the counter is the only thing wrong with it.
	for _, count := range []uint32{2, 1, 0} {
		device.signCount = count
		rec := f.loginWithPasskey(t, device, handle)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("login at counter %d after 2: status %d, body %s — a clone got in",
				count, rec.Code, rec.Body)
		}
	}

	// And the stored counter did not move, so a later honest login at 3 still
	// works.
	rows, err := f.store.PasskeyCredentials(t.Context(), ada.ID)
	if err != nil {
		t.Fatalf("read passkeys: %v", err)
	}
	if rows[0].SignCount != 2 {
		t.Fatalf("stored counter = %d after refused logins, want 2", rows[0].SignCount)
	}
	device.signCount = 3
	if rec := f.loginWithPasskey(t, device, handle); rec.Code != http.StatusOK {
		t.Fatalf("the real authenticator was locked out: status %d, body %s", rec.Code, rec.Body)
	}
}

// Most synced passkeys report 0 forever, which is legal and means "I do not
// count". Treating that as a clone would lock those people out on their second
// login — which is nearly everybody.
func TestPasskeyAuthenticatorThatAlwaysReportsZeroKeepsWorking(t *testing.T) {
	f := newPasskeyFixture(t)
	ada := f.seed(t, "ada@example.test", store.RoleEditor, goodPassword)
	cookie := f.login(t, "ada@example.test", goodPassword)

	device := newSoftAuthenticator(t, "ada-phone")
	device.signCount = 0
	_, handle := f.registerPasskey(t, cookie, device, "phone")

	for i := range 3 {
		device.signCount = 0
		rec := f.loginWithPasskey(t, device, handle)
		if rec.Code != http.StatusOK {
			t.Fatalf("login %d with a counterless authenticator: status %d, body %s", i+1, rec.Code, rec.Body)
		}
	}

	rows, err := f.store.PasskeyCredentials(t.Context(), ada.ID)
	if err != nil {
		t.Fatalf("read passkeys: %v", err)
	}
	if rows[0].SignCount != 0 {
		t.Errorf("stored counter = %d, want it left at 0", rows[0].SignCount)
	}
	if rows[0].LastUsedAt == nil {
		t.Error("use was not recorded for a counterless authenticator")
	}
}

// --- enumeration --------------------------------------------------------------

// login/begin must not tell a stranger whether an address has an account, for
// the same reason password-reset must not: an address list is the first half of
// a credential-stuffing run.
//
// It does not answer identically by making two answers look alike. It answers
// identically because it never looks anything up — there is nothing in the
// response that could depend on the address, because the address is never read.
func TestPasskeyLoginBeginDoesNotEnumerate(t *testing.T) {
	f := newPasskeyFixture(t)
	f.seed(t, "ada@example.test", store.RoleEditor, goodPassword)
	cookie := f.login(t, "ada@example.test", goodPassword)
	f.registerPasskey(t, cookie, newSoftAuthenticator(t, "ada-phone"), "phone")

	// Grace exists and has no passkey at all; the third address has no account.
	f.seed(t, "grace@example.test", store.RoleEditor, otherPassword)

	shapes := map[string]string{}
	challenges := map[string]bool{}

	for _, name := range []string{
		"an account with a passkey",
		"an account with none",
		"no account at all",
		"no address at all",
	} {
		var body any
		switch name {
		case "an account with a passkey":
			body = map[string]string{"email": "ada@example.test"}
		case "an account with none":
			body = map[string]string{"email": "grace@example.test"}
		case "no account at all":
			body = map[string]string{"email": "linus@example.test"}
		}

		rec := f.do(t, http.MethodPost, "/api/v1/auth/passkeys/login/begin", body, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d, body %s", name, rec.Code, rec.Body)
		}
		opts := decodeTestBody[requestOptions](t, rec)

		// allowCredentials is never sent. A populated list and an empty one are
		// distinguishable at a glance, which is exactly the disclosure this
		// endpoint exists to avoid.
		if len(opts.PublicKey.AllowCredentials) != 0 {
			t.Errorf("%s: allowCredentials = %s, want it absent", name, opts.PublicKey.AllowCredentials)
		}
		if opts.PublicKey.RPID != passkeyRPID {
			t.Errorf("%s: rpId = %q, want %q", name, opts.PublicKey.RPID, passkeyRPID)
		}

		// Everything but the challenge must match across all four.
		if challenges[opts.PublicKey.Challenge] {
			t.Errorf("%s: the challenge repeated a previous one", name)
		}
		challenges[opts.PublicKey.Challenge] = true
		opts.PublicKey.Challenge = ""
		shapes[name] = string(rawJSON(t, opts))
	}

	var first string
	for name, shape := range shapes {
		if first == "" {
			first = shape
			continue
		}
		if shape != first {
			t.Errorf("%s produced a different response shape:\n %s\nvs\n %s", name, shape, first)
		}
	}
}

// Every way a passkey login can fail answers the same way. An assertion for an
// account that does not exist, one for a disabled account, and one with a
// broken signature are one status and one code.
func TestPasskeyLoginRefusalsAreIndistinguishable(t *testing.T) {
	f := newPasskeyFixture(t)
	ada := f.seed(t, "ada@example.test", store.RoleEditor, goodPassword)
	cookie := f.login(t, "ada@example.test", goodPassword)
	device := newSoftAuthenticator(t, "ada-phone")
	_, handle := f.registerPasskey(t, cookie, device, "phone")

	stranger := newSoftAuthenticator(t, "unknown-device")
	unknownHandle := make([]byte, 16)

	bodies := map[string]func() *httptest.ResponseRecorder{
		"a credential nobody registered": func() *httptest.ResponseRecorder {
			return f.loginWithPasskey(t, stranger, handle)
		},
		"an account that does not exist": func() *httptest.ResponseRecorder {
			return f.loginWithPasskey(t, device, unknownHandle)
		},
		"a user handle that is not an id": func() *httptest.ResponseRecorder {
			return f.loginWithPasskey(t, device, []byte("nonsense"))
		},
	}

	var want string
	for name, run := range bodies {
		device.signCount++
		rec := run()
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status %d, body %s — want 401", name, rec.Code, rec.Body)
		}
		if want == "" {
			want = rec.Body.String()
			continue
		}
		if rec.Body.String() != want {
			t.Errorf("%s answered %q, want the same %q as every other refusal",
				name, rec.Body.String(), want)
		}
	}

	// A disabled account is the same answer again, and is checked last because
	// it changes the account.
	current, err := f.store.User(t.Context(), ada.ID)
	if err != nil {
		t.Fatalf("read ada: %v", err)
	}
	current.Status = store.StatusDisabled
	if _, err := f.store.UpdateUser(t.Context(), current); err != nil {
		t.Fatalf("disable ada: %v", err)
	}

	device.signCount++
	rec := f.loginWithPasskey(t, device, handle)
	if rec.Code != http.StatusUnauthorized || rec.Body.String() != want {
		t.Fatalf("a disabled account answered %d %q, want %q — a passkey is not a way around a disable",
			rec.Code, rec.Body.String(), want)
	}
}

// The signature is the whole mechanism, so it is worth proving that this suite
// would notice if it stopped being checked. A second authenticator claiming the
// same credential id, holding a different private key: every field lines up, the
// credential is found, the account is found — and the signature does not verify.
func TestPasskeyLoginRefusesAForgedSignature(t *testing.T) {
	f := newPasskeyFixture(t)
	f.seed(t, "ada@example.test", store.RoleEditor, goodPassword)
	cookie := f.login(t, "ada@example.test", goodPassword)

	real := newSoftAuthenticator(t, "ada-phone")
	_, handle := f.registerPasskey(t, cookie, real, "phone")

	// Same identifier, same flags, same everything — a different key.
	forger := newSoftAuthenticator(t, "ada-phone")
	forger.signCount = 1
	if bytes.Equal(forger.credID, real.credID) != true {
		t.Fatal("the test is not testing what it says it is")
	}

	rec := f.loginWithPasskey(t, forger, handle)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("a forged signature logged in: status %d, body %s", rec.Code, rec.Body)
	}

	// And the real one still works, so the refusal above was about the
	// signature and not about something this test broke on the way there.
	real.signCount = 1
	if rec := f.loginWithPasskey(t, real, handle); rec.Code != http.StatusOK {
		t.Fatalf("the real authenticator was refused too: status %d, body %s", rec.Code, rec.Body)
	}
}

// Browsers report extension outputs nobody asked for, and the ceremony has to
// survive them.
//
// This is the one that locked Safari out. WebKit puts `appid: false` on every
// assertion that comes from a security key, requested or not, and the library's
// default is to fail the whole ceremony over an unrequested output — after the
// signature has verified. Password managers do the same at registration with
// `credProps`. This server asks for no extensions and reads none, so both are
// noise, and noise must not be a refusal.
func TestPasskeyCeremoniesSurviveExtensionOutputsNobodyAskedFor(t *testing.T) {
	f := newPasskeyFixture(t)
	f.seed(t, "ada@example.test", store.RoleEditor, goodPassword)
	cookie := f.login(t, "ada@example.test", goodPassword)

	// Registered plainly, so that the assertion below is the first thing here
	// to carry an output, and a refusal of it cannot hide behind a refused
	// registration.
	device := newSoftAuthenticator(t, "ada-key")
	_, handle := f.registerPasskey(t, cookie, device, "Ada's key")

	device.clientExtensions = map[string]any{"appid": false}
	device.signCount = 1
	if rec := f.loginWithPasskey(t, device, handle); rec.Code != http.StatusOK {
		t.Fatalf("an assertion carrying WebKit's unrequested `appid: false` was refused: status %d, body %s",
			rec.Code, rec.Body)
	}

	// Ignored means ignored, not trusted. `appid: true` asks the verifier to
	// accept the hash of a legacy U2F AppID in place of the RP ID's; there is no
	// AppID here, the authenticator data still carries the RP ID's hash, and the
	// login stands or falls on that alone.
	device.clientExtensions = map[string]any{"appid": true}
	device.signCount = 2
	if rec := f.loginWithPasskey(t, device, handle); rec.Code != http.StatusOK {
		t.Fatalf("`appid: true` on a credential that is not a U2F one changed the outcome: status %d, body %s",
			rec.Code, rec.Body)
	}

	// And at registration, where registerPasskey fails the test on a refusal.
	manager := newSoftAuthenticator(t, "ada-password-manager")
	manager.clientExtensions = map[string]any{"credProps": map[string]any{"rk": true}}
	f.registerPasskey(t, cookie, manager, "Ada's password manager")
}

// The page asks for a challenge every time the sign-in screen is drawn, so that
// a tap finds one waiting. Looking at the screen must not spend the allowance
// for using it: only login/finish is an attempt.
func TestPasskeyLoginBeginDoesNotSpendLoginAttempts(t *testing.T) {
	f := newPasskeyFixture(t)
	f.seed(t, "ada@example.test", store.RoleEditor, goodPassword)

	var limited bool
	for range passkeyBeginIPBurst + 2 {
		rec := f.do(t, http.MethodPost, "/api/v1/auth/passkeys/login/begin", nil, nil)
		if rec.Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Error("login/begin is a public endpoint that writes a row, and it was never limited")
	}

	// More begins than the login bucket holds, and the password still works.
	if passkeyBeginIPBurst <= loginIPBurst {
		t.Fatalf("this test needs more begins (%d) than the login bucket holds (%d)", passkeyBeginIPBurst, loginIPBurst)
	}
	f.login(t, "ada@example.test", goodPassword)
}

// A refusal tells the caller nothing, so the log has to tell the operator
// everything — and a failure that happens on somebody else's phone is only ever
// going to be diagnosed from this line.
func TestPasskeyRefusalsAreLoggedWithTheStepThatFailed(t *testing.T) {
	f := newPasskeyFixture(t)
	var logged bytes.Buffer
	f.a.log = slog.New(slog.NewJSONHandler(&logged, nil))

	ada := f.seed(t, "ada@example.test", store.RoleEditor, goodPassword)
	cookie := f.login(t, "ada@example.test", goodPassword)
	device := newSoftAuthenticator(t, "ada-phone")
	_, handle := f.registerPasskey(t, cookie, device, "phone")

	// lastLine is the most recent refusal, decoded, with the buffer cleared so
	// the next case reads only its own.
	lastLine := func(t *testing.T, msg string) map[string]any {
		t.Helper()
		var found map[string]any
		for _, line := range bytes.Split(bytes.TrimSpace(logged.Bytes()), []byte("\n")) {
			var entry map[string]any
			if err := json.Unmarshal(line, &entry); err == nil && entry["msg"] == msg {
				found = entry
			}
		}
		if found == nil {
			t.Fatalf("no %q line was logged; the log holds:\n%s", msg, logged.String())
		}
		if bytes.Contains(logged.Bytes(), []byte("ada@example.test")) {
			t.Errorf("the log names the person:\n%s", logged.String())
		}
		logged.Reset()
		return found
	}

	// A synced passkey that stops claiming to be one. Backup eligibility is
	// fixed for the life of a credential, so the library refuses the assertion;
	// this is the failure a server that stored the flag wrongly at registration
	// would produce for every Apple passkey, on every login.
	device.signCount = 1
	device.backupEligible = false
	if rec := f.loginWithPasskey(t, device, handle); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a changed backup-eligible flag logged in: status %d", rec.Code)
	}
	entry := lastLine(t, "passkey login refused")
	if entry["step"] != "verify" {
		t.Errorf("step = %v, want verify", entry["step"])
	}
	if got, _ := entry["err"].(string); !strings.Contains(got, "Backup Eligible") {
		t.Errorf("err = %q, want it to name the backup-eligible flag", got)
	}
	device.backupEligible = true

	// Another origin: the library's own sentence is "Error validating origin",
	// and which origin is only in the part Error() leaves out.
	rec := f.do(t, http.MethodPost, "/api/v1/auth/passkeys/login/begin", nil, nil)
	opts := decodeTestBody[requestOptions](t, rec)
	rec = f.do(t, http.MethodPost, "/api/v1/auth/passkeys/login/finish", map[string]any{
		"credential": device.assert(t, opts.PublicKey.Challenge, "https://elsewhere.example.test", handle),
	}, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("an assertion from another origin logged in: status %d", rec.Code)
	}
	entry = lastLine(t, "passkey login refused")
	if got, _ := entry["detail"].(string); !strings.Contains(got, "elsewhere.example.test") {
		t.Errorf("detail = %q, want the origin that was presented", got)
	}

	// A challenge this server never issued.
	rec = f.do(t, http.MethodPost, "/api/v1/auth/passkeys/login/finish", map[string]any{
		"credential": device.assert(t, b64([]byte("never-issued-by-this-server-0000")), passkeyOrigin, handle),
	}, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("an unknown challenge logged in: status %d", rec.Code)
	}
	if entry = lastLine(t, "passkey login refused"); entry["step"] != "challenge" {
		t.Errorf("step = %v, want challenge", entry["step"])
	}

	// Registration's refusals used to be silent on two of their three paths.
	rec = f.do(t, http.MethodPost, "/api/v1/auth/passkeys/register/finish", map[string]any{
		"credential": map[string]any{"id": "AAAA", "rawId": "AAAA", "type": "public-key"},
	}, cookie)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("an unparseable registration = %d, want 400", rec.Code)
	}
	entry = lastLine(t, "passkey registration refused")
	if entry["step"] != "parse" || entry["user"] != ada.ID.String() {
		t.Errorf("logged %v, want step parse for Ada's account id", entry)
	}
}

// Client data is signed over, so an assertion collected at another origin
// cannot be replayed here — which is what stops a phishing page from
// forwarding one.
func TestPasskeyLoginRefusesAnotherOrigin(t *testing.T) {
	f := newPasskeyFixture(t)
	f.seed(t, "ada@example.test", store.RoleEditor, goodPassword)
	cookie := f.login(t, "ada@example.test", goodPassword)

	device := newSoftAuthenticator(t, "ada-phone")
	_, handle := f.registerPasskey(t, cookie, device, "phone")

	rec := f.do(t, http.MethodPost, "/api/v1/auth/passkeys/login/begin", nil, nil)
	opts := decodeTestBody[requestOptions](t, rec)

	device.signCount = 1
	rec = f.do(t, http.MethodPost, "/api/v1/auth/passkeys/login/finish", map[string]any{
		"credential": device.assert(t, opts.PublicKey.Challenge, "https://soiree.example.test.evil.test", handle),
	}, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("an assertion collected elsewhere was accepted: status %d, body %s", rec.Code, rec.Body)
	}
}

// And the same for registration: a credential created at another origin is not
// attached to the account.
func TestPasskeyRegistrationRefusesAnotherOrigin(t *testing.T) {
	f := newPasskeyFixture(t)
	ada := f.seed(t, "ada@example.test", store.RoleEditor, goodPassword)
	cookie := f.login(t, "ada@example.test", goodPassword)

	rec := f.do(t, http.MethodPost, "/api/v1/auth/passkeys/register/begin", nil, cookie)
	opts := decodeTestBody[creationOptions](t, rec)

	device := newSoftAuthenticator(t, "ada-phone")
	rec = f.do(t, http.MethodPost, "/api/v1/auth/passkeys/register/finish", map[string]any{
		"label":      "phone",
		"credential": device.register(t, opts.PublicKey.Challenge, "http://soiree.example.test"),
	}, cookie)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a registration from another origin: status %d, body %s", rec.Code, rec.Body)
	}
	rows, err := f.store.PasskeyCredentials(t.Context(), ada.ID)
	if err != nil {
		t.Fatalf("read passkeys: %v", err)
	}
	if len(rows) != 0 {
		t.Fatal("the credential was stored anyway")
	}
}

// --- managing credentials ------------------------------------------------------

func TestPasskeyListIsScopedToTheCaller(t *testing.T) {
	f := newPasskeyFixture(t)
	f.seed(t, "ada@example.test", store.RoleEditor, goodPassword)
	f.seed(t, "grace@example.test", store.RoleAdmin, otherPassword)

	adaCookie := f.login(t, "ada@example.test", goodPassword)
	graceCookie := f.login(t, "grace@example.test", otherPassword)

	adaPhone, _ := f.registerPasskey(t, adaCookie, newSoftAuthenticator(t, "ada-phone"), "phone")
	f.registerPasskey(t, adaCookie, newSoftAuthenticator(t, "ada-laptop"), "laptop")
	f.registerPasskey(t, graceCookie, newSoftAuthenticator(t, "grace-key"), "yubikey")

	list := func(cookie *http.Cookie) []passkeyDTO {
		rec := f.do(t, http.MethodGet, "/api/v1/auth/passkeys", nil, cookie)
		if rec.Code != http.StatusOK {
			t.Fatalf("list: status %d, body %s", rec.Code, rec.Body)
		}
		return decodeTestBody[struct {
			Passkeys []passkeyDTO `json:"passkeys"`
		}](t, rec).Passkeys
	}

	if got := list(adaCookie); len(got) != 2 {
		t.Fatalf("Ada sees %d passkeys, want her own 2", len(got))
	}
	// Grace is an admin, and an admin does not get to see somebody else's
	// devices either: this is not an administrative surface.
	graces := list(graceCookie)
	if len(graces) != 1 {
		t.Fatalf("Grace sees %d passkeys, want only her own", len(graces))
	}
	for _, p := range graces {
		if p.ID == adaPhone.ID {
			t.Fatal("Grace can see Ada's phone")
		}
	}
}

func TestPasskeyCannotBeDeletedByAnotherUser(t *testing.T) {
	f := newPasskeyFixture(t)
	ada := f.seed(t, "ada@example.test", store.RoleEditor, goodPassword)
	f.seed(t, "grace@example.test", store.RoleAdmin, otherPassword)

	adaCookie := f.login(t, "ada@example.test", goodPassword)
	graceCookie := f.login(t, "grace@example.test", otherPassword)

	phone, _ := f.registerPasskey(t, adaCookie, newSoftAuthenticator(t, "ada-phone"), "phone")

	// An admin's session is not a way in either.
	rec := f.do(t, http.MethodDelete, "/api/v1/auth/passkeys/"+phone.ID.String(), nil, graceCookie)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("Grace deleting Ada's passkey: status %d, body %s, want 404", rec.Code, rec.Body)
	}
	rows, err := f.store.PasskeyCredentials(t.Context(), ada.ID)
	if err != nil {
		t.Fatalf("read passkeys: %v", err)
	}
	if len(rows) != 1 {
		t.Fatal("Ada's passkey went anyway")
	}

	// Her own, she may remove.
	rec = f.do(t, http.MethodDelete, "/api/v1/auth/passkeys/"+phone.ID.String(), nil, adaCookie)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("Ada deleting her own passkey: status %d, body %s", rec.Code, rec.Body)
	}
	rows, err = f.store.PasskeyCredentials(t.Context(), ada.ID)
	if err != nil {
		t.Fatalf("read passkeys: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("Ada still has %d passkeys after removing the only one", len(rows))
	}

	// And it is gone for good: the same id is now a 404 for her too, which is
	// the answer somebody else's id gets.
	rec = f.do(t, http.MethodDelete, "/api/v1/auth/passkeys/"+phone.ID.String(), nil, adaCookie)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("deleting it twice: status %d, want 404", rec.Code)
	}
}

func TestPasskeyManagementNeedsASession(t *testing.T) {
	f := newPasskeyFixture(t)

	for _, req := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/auth/passkeys/register/begin"},
		{http.MethodPost, "/api/v1/auth/passkeys/register/finish"},
		{http.MethodGet, "/api/v1/auth/passkeys"},
		{http.MethodDelete, "/api/v1/auth/passkeys/00000000-0000-0000-0000-000000000001"},
	} {
		rec := f.do(t, req.method, req.path, nil, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without a session = %d, want 401", req.method, req.path, rec.Code)
		}
	}
}

// --- recovery -----------------------------------------------------------------

// The property that makes a lost phone survivable: passkeys are additive.
// Registering one changes nothing about the password, and removing every one of
// them leaves the account exactly as it was.
func TestPasskeysNeverReplaceThePassword(t *testing.T) {
	f := newPasskeyFixture(t)
	f.seed(t, "ada@example.test", store.RoleEditor, goodPassword)
	cookie := f.login(t, "ada@example.test", goodPassword)

	phone, handle := f.registerPasskey(t, cookie, newSoftAuthenticator(t, "ada-phone"), "phone")

	// The password still works with a passkey registered.
	f.login(t, "ada@example.test", goodPassword)

	// The phone is lost. Removing it — which she can do from her laptop, or an
	// admin can route around entirely with a fresh set-password link — leaves
	// the password login untouched.
	if rec := f.do(t, http.MethodDelete, "/api/v1/auth/passkeys/"+phone.ID.String(), nil, cookie); rec.Code != http.StatusNoContent {
		t.Fatalf("removing the lost phone: status %d, body %s", rec.Code, rec.Body)
	}
	f.login(t, "ada@example.test", goodPassword)

	// And the credential that went with it no longer opens anything.
	device := newSoftAuthenticator(t, "ada-phone")
	device.signCount = 1
	if rec := f.loginWithPasskey(t, device, handle); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a removed passkey still logs in: status %d, body %s", rec.Code, rec.Body)
	}
}

// --- malformed input ------------------------------------------------------------

func TestPasskeyFinishRefusesRubbish(t *testing.T) {
	f := newPasskeyFixture(t)
	f.seed(t, "ada@example.test", store.RoleEditor, goodPassword)
	cookie := f.login(t, "ada@example.test", goodPassword)

	for name, body := range map[string]any{
		"no credential at all":  map[string]any{"label": "phone"},
		"a credential of null":  map[string]any{"credential": nil},
		"a credential of text":  map[string]any{"credential": "not an object"},
		"an empty object":       map[string]any{"credential": map[string]any{}},
		"the wrong member type": map[string]any{"credential": []int{1, 2, 3}},
	} {
		rec := f.do(t, http.MethodPost, "/api/v1/auth/passkeys/register/finish", body, cookie)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("register/finish with %s = %d, want 400", name, rec.Code)
		}

		rec = f.do(t, http.MethodPost, "/api/v1/auth/passkeys/login/finish", body, nil)
		if rec.Code != http.StatusBadRequest && rec.Code != http.StatusUnauthorized {
			t.Errorf("login/finish with %s = %d, want 400 or 401", name, rec.Code)
		}
	}
}

// begin takes no body, and a client that sends one anyway is not an error.
func TestPasskeyBeginAcceptsAnEmptyBody(t *testing.T) {
	f := newPasskeyFixture(t)
	f.seed(t, "ada@example.test", store.RoleEditor, goodPassword)
	cookie := f.login(t, "ada@example.test", goodPassword)

	for _, body := range []any{nil, map[string]any{}, map[string]string{"email": "ada@example.test"}} {
		if rec := f.do(t, http.MethodPost, "/api/v1/auth/passkeys/login/begin", body, nil); rec.Code != http.StatusOK {
			t.Errorf("login/begin with body %v = %d, want 200", body, rec.Code)
		}
		if rec := f.do(t, http.MethodPost, "/api/v1/auth/passkeys/register/begin", body, cookie); rec.Code != http.StatusOK {
			t.Errorf("register/begin with body %v = %d, want 200", body, rec.Code)
		}
	}
}

// A label somebody pasted a paragraph into is truncated, not refused.
func TestPasskeyLabelIsBounded(t *testing.T) {
	f := newPasskeyFixture(t)
	f.seed(t, "ada@example.test", store.RoleEditor, goodPassword)
	cookie := f.login(t, "ada@example.test", goodPassword)

	long := ""
	for range maxPasskeyLabel + 50 {
		long += "é"
	}
	dto, _ := f.registerPasskey(t, cookie, newSoftAuthenticator(t, "ada-phone"), long)
	if got := len([]rune(dto.Label)); got != maxPasskeyLabel {
		t.Errorf("label is %d runes, want it cut to %d", got, maxPasskeyLabel)
	}
}

// A refusal in the browser never reaches login/finish, so without this the log
// of a failed sign-in is an empty log. The line has to say what the browser
// said — and nothing that somebody posting to a public endpoint chose to make
// it say.
func TestABrowsersRefusalIsLoggedAndCannotForgeALine(t *testing.T) {
	f := newPasskeyFixture(t)
	var logged bytes.Buffer
	f.a.log = slog.New(slog.NewJSONHandler(&logged, nil))

	rec := f.do(t, http.MethodPost, "/api/v1/auth/passkeys/report", map[string]any{
		"ceremony":  "login",
		"kind":      "NotAllowedError",
		"message":   "The operation is not allowed at this time because the page does not have focus.",
		"elapsedMs": 12,
		"prepared":  true,
		"focused":   false,
	}, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("report: %d, want 204", rec.Code)
	}
	var line map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(logged.Bytes()), &line); err != nil {
		t.Fatalf("the report did not make exactly one log line: %v\n%s", err, logged.String())
	}
	for key, want := range map[string]any{
		"msg":       "passkey refused by the browser",
		"ceremony":  "login",
		"kind":      "NotAllowedError",
		"message":   "The operation is not allowed at this time because the page does not have focus.",
		"elapsedMs": float64(12),
		"prepared":  true,
		"focused":   false,
	} {
		if line[key] != want {
			t.Errorf("log %q = %v, want %v", key, line[key], want)
		}
	}

	// What an ill-wisher sends: a line break and a second "line", control
	// characters, far too much of everything, and a ceremony that is not one.
	logged.Reset()
	rec = f.do(t, http.MethodPost, "/api/v1/auth/passkeys/report", map[string]any{
		"ceremony":  "login\n{\"level\":\"ERROR\"}",
		"kind":      strings.Repeat("K", 100),
		"message":   "first\n{\"level\":\"ERROR\",\"msg\":\"forged\"}\x1b[2J" + strings.Repeat("m", 600),
		"elapsedMs": -5,
	}, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("hostile report: %d, want 204", rec.Code)
	}
	if n := bytes.Count(bytes.TrimSpace(logged.Bytes()), []byte("\n")); n != 0 {
		t.Errorf("a hostile report made %d extra log lines", n)
	}
	line = map[string]any{}
	if err := json.Unmarshal(bytes.TrimSpace(logged.Bytes()), &line); err != nil {
		t.Fatalf("hostile report: not one JSON line: %v", err)
	}
	if line["ceremony"] != "login" {
		t.Errorf("ceremony = %v, want it read as login", line["ceremony"])
	}
	if got := line["kind"].(string); len(got) != 40 {
		t.Errorf("kind is %d characters, want it cut to 40", len(got))
	}
	message := line["message"].(string)
	if len(message) != 240 {
		t.Errorf("message is %d characters, want it cut to 240", len(message))
	}
	if strings.ContainsAny(message, "\n\r\x1b") {
		t.Errorf("message kept a control character: %q", message)
	}
	if line["elapsedMs"] != float64(0) {
		t.Errorf("elapsedMs = %v, want a negative time read as 0", line["elapsedMs"])
	}

	// More than a report can be is not read at all.
	logged.Reset()
	rec = f.do(t, http.MethodPost, "/api/v1/auth/passkeys/report", map[string]any{
		"message": strings.Repeat("m", 5000),
	}, nil)
	if rec.Code != http.StatusNoContent || logged.Len() != 0 {
		t.Errorf("oversize report: %d with %d bytes logged, want 204 and nothing", rec.Code, logged.Len())
	}

	// A body that is not JSON is the same 204 and no line: nothing to probe.
	logged.Reset()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/passkeys/report", strings.NewReader("not json"))
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent || logged.Len() != 0 {
		t.Errorf("unreadable report: %d with %d bytes logged, want 204 and nothing", w.Code, logged.Len())
	}
}

// Anybody can post a report and each one is a log line, so it has an allowance
// — its own, because a person who keeps failing must still be able to sign in
// with a password afterwards.
func TestReportingARefusalIsBoundedAndSpendsNoLoginAttempts(t *testing.T) {
	f := newPasskeyFixture(t)
	f.seed(t, "ada@example.test", store.RoleEditor, goodPassword)

	var limited bool
	for range passkeyReportIPBurst + 2 {
		rec := f.do(t, http.MethodPost, "/api/v1/auth/passkeys/report", map[string]any{"kind": "NotAllowedError"}, nil)
		if rec.Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Error("the report endpoint writes a log line for anybody, and it was never limited")
	}
	f.login(t, "ada@example.test", goodPassword)
}
