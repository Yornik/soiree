package httpd

// Passkeys: a second way in, alongside the password and never instead of it.
//
// A passkey is a key pair the authenticator holds — a phone's secure element, a
// laptop's TPM, a USB key. Logging in is a challenge the server minted being
// signed by a private key that never leaves the device, which is why it cannot
// be phished, reused across sites, or read out of a database.
//
// Three properties do all the work, and each of them is somewhere specific:
//
//   - The **Relying Party ID** scopes a credential to a domain. It comes from
//     SOIREE_BASE_URL through config, never from the request's Host header,
//     which the client chooses. See config.Config.PasskeyRPID.
//   - The **challenge** is single-use and expires. It lives in
//     passkey_challenges and is redeemed by a DELETE, so two requests
//     presenting the same one cannot both win. See
//     store.ConsumePasskeyChallenge.
//   - The **signature counter**, where an authenticator keeps one, must
//     advance. A value that does not is the cloned-authenticator signal, and
//     this file refuses the login. Many authenticators report 0 forever, which
//     is legal; that case is the normal one, not a clone.
//
// No cryptography is implemented here. The library parses CBOR, verifies
// attestation and checks signatures; what is left — whose ceremony this is,
// whether the challenge is still good, and turning a verified assertion into a
// session — is what this file does.
//
// The refusals are deliberately uniform. Every way a passkey login can fail
// answers with the same 401 and the same code, for the same reason the rest of
// this surface answers identically to a stranger and to a user: the difference
// between "no such credential" and "that account is disabled" is an answer to a
// question nobody logged in should be able to ask.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"

	"github.com/Yornik/soiree/internal/config"
	"github.com/Yornik/soiree/internal/store"
)

const (
	// passkeyChallengeTTL is how long a ceremony may stay in flight.
	//
	// Generous next to the browser's own timeout, because the slow part is a
	// person finding their phone and putting a thumb on it. Short next to
	// anything else here: a challenge is the one thing whose whole value is
	// that it has never been seen before.
	passkeyChallengeTTL = 5 * time.Minute

	// maxPasskeyLabel bounds what a person may call a credential. Long enough
	// for "the yubikey in the kitchen drawer", short enough that the list stays
	// a list.
	maxPasskeyLabel = 64

	// maxPasskeysPerUser bounds the rows one account can create. Registration
	// needs a session, so this is not a public write — it is here so a bug in a
	// client cannot turn a retry loop into an unbounded table.
	maxPasskeysPerUser = 20
)

// passkeys is the WebAuthn surface's own state: an configured Relying Party,
// and nothing else. Its presence on Auth is what decides whether the routes
// exist at all.
type passkeys struct {
	wa *webauthn.WebAuthn
}

// WithPasskeys turns the passkey surface on from the configuration.
//
// Separate from NewAuth and returning an error rather than taking one, because
// this is optional in the way mail is optional: a deployment that cannot derive
// a Relying Party ID keeps password login and loses nothing else. The caller
// logs the error and carries on — but it must also stop telling the browser
// that passkeys are available, or the page offers a button whose route is not
// mounted. See cmd/soiree.
//
// Call it before Register; Register mounts what it finds.
func (a *Auth) WithPasskeys(cfg config.Config) error {
	if !cfg.PasskeysEnabled {
		return nil
	}
	if cfg.PasskeyRPID == "" || cfg.PasskeyOrigin == "" {
		return errors.New("passkeys: no relying party could be derived from SOIREE_BASE_URL")
	}

	name := strings.TrimSpace(cfg.EventName)
	if name == "" {
		// The library refuses an empty display name, and this is a label shown
		// in a system prompt rather than anything the protocol depends on.
		name = "soiree"
	}

	wa, err := webauthn.New(&webauthn.Config{
		RPID:          cfg.PasskeyRPID,
		RPDisplayName: name,
		// Exactly one origin. Every additional entry is a host whose pages can
		// mint assertions this server accepts.
		RPOrigins: []string{cfg.PasskeyOrigin},
		// No attestation. It would tell this deployment which make of
		// authenticator somebody carries, which is a fact about a person that
		// nothing here would act on, and verifying it properly means the FIDO
		// metadata service and a trust store to keep current.
		AttestationPreference: protocol.PreferNoAttestation,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			// Discoverable, always. A credential the authenticator can find
			// without being told who to look for is what makes "sign in" a
			// single tap with no address typed first — and it is also what lets
			// login/begin answer identically for an address that has an account
			// and one that does not, because it never has to look.
			ResidentKey:        protocol.ResidentKeyRequirementRequired,
			RequireResidentKey: protocol.ResidentKeyRequired(),
			// Preferred rather than required: this is a second factor's worth of
			// assurance on top of possession, and requiring it turns an
			// authenticator with no biometric and no PIN into no passkey at all.
			UserVerification: protocol.VerificationPreferred,
		},
	})
	if err != nil {
		return fmt.Errorf("passkeys: %w", err)
	}

	a.passkeys = &passkeys{wa: wa}
	return nil
}

// registerPasskeyRoutes mounts the WebAuthn surface, or does not.
//
// A no-op when passkeys are off, so "disabled" means the paths are not routes —
// they fall through to the frontend's 404 — rather than routes that answer with
// a refusal. There is nothing to get wrong in a handler that was never mounted.
func (a *Auth) registerPasskeyRoutes(mux *http.ServeMux) {
	if a.passkeys == nil {
		return
	}

	// Registering a passkey is something an already-authenticated person does.
	// There is no way to bootstrap an account from a passkey alone, by design:
	// accounts are created by an admin, and adding a second way into an account
	// requires already being in it.
	mux.Handle("POST /api/v1/auth/passkeys/register/begin",
		a.RequireAuth(http.HandlerFunc(a.handlePasskeyRegisterBegin)))
	mux.Handle("POST /api/v1/auth/passkeys/register/finish",
		a.RequireAuth(http.HandlerFunc(a.handlePasskeyRegisterFinish)))

	// Public, and behind the same per-IP bucket as the password login. One
	// budget for "attempts to get in from this address", rather than a second
	// mechanism that a client can alternate with to get twice the allowance.
	mux.Handle("POST /api/v1/auth/passkeys/login/begin",
		a.limitIP(a.loginIP, http.HandlerFunc(a.handlePasskeyLoginBegin)))
	mux.Handle("POST /api/v1/auth/passkeys/login/finish",
		a.limitIP(a.loginIP, http.HandlerFunc(a.handlePasskeyLoginFinish)))

	// Managing one's own credentials. Never anybody else's: there is no admin
	// view of somebody's passkeys, because an admin has no use for the list and
	// the person who does is the one holding the devices.
	mux.Handle("GET /api/v1/auth/passkeys", a.RequireAuth(http.HandlerFunc(a.handlePasskeyList)))
	mux.Handle("DELETE /api/v1/auth/passkeys/{id}", a.RequireAuth(http.HandlerFunc(a.handlePasskeyDelete)))
}

// --- the account, as the library sees it ------------------------------------

// passkeyUser adapts an account onto the library's User interface.
//
// WebAuthnID is the account's uuid, and it matters that it is not the address:
// the user handle is stored on the authenticator and handed back by the browser
// at every login, so an address here would put somebody's email on their phone's
// passkey list and into a value that travels with the assertion. A uuid names
// the same account and says nothing about who they are.
type passkeyUser struct {
	user  store.User
	rows  []store.PasskeyCredential
	creds []webauthn.Credential
}

func newPasskeyUser(u store.User, rows []store.PasskeyCredential) passkeyUser {
	creds := make([]webauthn.Credential, 0, len(rows))
	for _, row := range rows {
		creds = append(creds, toWebauthnCredential(row))
	}
	return passkeyUser{user: u, rows: rows, creds: creds}
}

func (u passkeyUser) WebAuthnID() []byte {
	id := u.user.ID
	return id[:]
}

// WebAuthnName and WebAuthnDisplayName are what the authenticator shows in its
// own account picker, on the person's own device. The address is the only name
// this application knows.
func (u passkeyUser) WebAuthnName() string        { return u.user.Email }
func (u passkeyUser) WebAuthnDisplayName() string { return u.user.Email }

func (u passkeyUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

// descriptors names the credentials already registered, so the browser can tell
// an authenticator not to make a second key for an account it already has one
// for — which is what turns "register" on a device that is already registered
// into a clear refusal instead of a duplicate nobody asked for.
func (u passkeyUser) descriptors() []protocol.CredentialDescriptor {
	out := make([]protocol.CredentialDescriptor, 0, len(u.creds))
	for i := range u.creds {
		out = append(out, u.creds[i].Descriptor())
	}
	return out
}

// toWebauthnCredential rebuilds the library's credential record from the row.
//
// Every field here is one the verification actually reads. BackupEligible in
// particular is not decoration: it is fixed for the life of a credential, and
// an assertion that disagrees with the stored value is refused — which is only
// possible because the value survived in the column.
func toWebauthnCredential(row store.PasskeyCredential) webauthn.Credential {
	transports := make([]protocol.AuthenticatorTransport, 0, len(row.Transports))
	for _, t := range row.Transports {
		transports = append(transports, protocol.AuthenticatorTransport(t))
	}
	return webauthn.Credential{
		ID:                row.CredentialID,
		PublicKey:         row.PublicKey,
		AttestationType:   row.AttestationType,
		AttestationFormat: row.AttestationFormat,
		Transport:         transports,
		Flags: webauthn.CredentialFlags{
			UserPresent:    row.UserPresent,
			UserVerified:   row.UserVerified,
			BackupEligible: row.BackupEligible,
			BackupState:    row.BackupState,
		},
		Authenticator: webauthn.Authenticator{
			AAGUID: row.AAGUID,
			// The column is a bigint because Postgres has no unsigned integer;
			// the CHECK constraint keeps it inside a uint32, so this cannot
			// wrap.
			SignCount: uint32(row.SignCount), //nolint:gosec // bounded by the column's CHECK
		},
	}
}

// --- registering a passkey --------------------------------------------------

func (a *Auth) handlePasskeyRegisterBegin(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "")
		return
	}
	// The body is optional and nothing is read from it; draining it keeps a
	// client that sends one from being surprised. The label arrives at finish,
	// where the credential it names exists.
	if !drainPasskeyBody(w, r) {
		return
	}

	rows, err := a.store.PasskeyCredentials(r.Context(), user.ID)
	if err != nil {
		a.log.Error("could not read passkeys", "user", user.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "")
		return
	}
	if len(rows) >= maxPasskeysPerUser {
		writeError(w, http.StatusConflict, "too_many_passkeys",
			fmt.Sprintf("an account may hold at most %d passkeys; remove one first", maxPasskeysPerUser))
		return
	}

	pu := newPasskeyUser(user, rows)
	creation, session, err := a.passkeys.wa.BeginRegistration(pu,
		webauthn.WithExclusions(pu.descriptors()))
	if err != nil {
		a.log.Error("could not begin a passkey registration", "user", user.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "")
		return
	}

	// Bound to this account. The finish step checks that the caller is the
	// person the ceremony was begun by, so a challenge lifted from somebody
	// else's begin response is not a way to attach a credential to their
	// account.
	owner := user.ID
	if !a.storePasskeyChallenge(w, r, session, store.PasskeyCeremonyRegister, &owner) {
		return
	}

	writeJSON(w, http.StatusOK, creation)
}

// passkeyRegisterRequest is the finish body.
//
// The credential is nested rather than spread across the top level so that the
// label cannot collide with a member of the PublicKeyCredential the browser
// produced, and so the browser's own `credential.toJSON()` can be dropped
// straight in without being reshaped.
type passkeyRegisterRequest struct {
	Label      string          `json:"label"`
	Credential json.RawMessage `json:"credential"`
}

func (a *Auth) handlePasskeyRegisterFinish(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "")
		return
	}

	var req passkeyRegisterRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if len(req.Credential) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_passkey",
			"send the credential from navigator.credentials.create() as the `credential` member")
		return
	}

	parsed, err := protocol.ParseCredentialCreationResponseBytes(req.Credential)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_passkey", "that is not a usable registration response")
		return
	}

	ch, ok := a.consumePasskeyChallenge(w, r, parsed.Response.CollectedClientData.Challenge,
		store.PasskeyCeremonyRegister, func() { writePasskeyChallengeRefused(w) })
	if !ok {
		return
	}
	// The ceremony belongs to whoever began it. Without this, a signed-in
	// person could finish a registration somebody else started and end up with
	// a credential attached to the wrong account.
	if ch.UserID == nil || *ch.UserID != user.ID {
		writePasskeyChallengeRefused(w)
		return
	}

	var session webauthn.SessionData
	if err := json.Unmarshal(ch.SessionData, &session); err != nil {
		a.log.Error("could not read a stored passkey ceremony", "user", user.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "")
		return
	}

	rows, err := a.store.PasskeyCredentials(r.Context(), user.ID)
	if err != nil {
		a.log.Error("could not read passkeys", "user", user.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "")
		return
	}

	// Everything cryptographic happens inside this call: the attestation is
	// checked, the client data is checked against the challenge, the RP ID and
	// the origin, and the public key is extracted.
	cred, err := a.passkeys.wa.CreateCredential(newPasskeyUser(user, rows), session, parsed)
	if err != nil {
		a.log.Info("passkey registration refused", "user", user.ID, "err", err)
		writeError(w, http.StatusBadRequest, "invalid_passkey", "that registration could not be verified")
		return
	}

	transports := make([]string, 0, len(cred.Transport))
	for _, t := range cred.Transport {
		if t != "" {
			transports = append(transports, string(t))
		}
	}

	row, err := a.store.CreatePasskeyCredential(r.Context(), store.PasskeyCredential{
		UserID:            user.ID,
		CredentialID:      cred.ID,
		PublicKey:         cred.PublicKey,
		SignCount:         int64(cred.Authenticator.SignCount),
		AAGUID:            cred.Authenticator.AAGUID,
		Transports:        transports,
		AttestationType:   cred.AttestationType,
		AttestationFormat: cred.AttestationFormat,
		BackupEligible:    cred.Flags.BackupEligible,
		BackupState:       cred.Flags.BackupState,
		UserPresent:       cred.Flags.UserPresent,
		UserVerified:      cred.Flags.UserVerified,
		Label:             passkeyLabel(req.Label),
	})
	if err != nil {
		if store.IsUniqueViolation(err) {
			// Registered already — to this account, or to another one. Which of
			// the two is not said: it is the same question login/begin refuses
			// to answer.
			writeError(w, http.StatusConflict, "passkey_exists", "that passkey is already registered")
			return
		}
		a.log.Error("could not store a passkey", "user", user.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "")
		return
	}

	a.log.Info("passkey registered", "user", user.ID, "credential", row.ID)
	writeJSON(w, http.StatusCreated, toPasskeyDTO(row))
}

// --- logging in with a passkey ----------------------------------------------

// passkeyLoginBeginRequest is accepted and deliberately not acted upon.
//
// A login form has an address field in it, and a client that sends what the
// person typed should not be met with an error. But the response must not
// depend on it — see handlePasskeyLoginBegin.
type passkeyLoginBeginRequest struct {
	Email string `json:"email"`
}

// handlePasskeyLoginBegin hands out a challenge, and learns nothing.
//
// This endpoint performs no lookup at all. It does not matter whether an
// address was sent, whether it names an account, or whether that account has
// any passkeys: the response is a fresh random challenge and a fixed set of
// options, byte-for-byte the same shape every time. That is the same property
// password-reset has, arrived at the same way — by making the work independent
// of the answer rather than by making two answers look alike.
//
// The alternative, naming the account's credentials in allowCredentials, is
// what a non-discoverable login needs, and it would publish exactly what this
// is refusing to publish: a populated list and an empty one are distinguishable
// at a glance. Credentials here are registered as discoverable, so the
// authenticator finds them without being told, and the list is never needed.
func (a *Auth) handlePasskeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	var req passkeyLoginBeginRequest
	if !decodeOptionalJSON(w, r, &req) {
		return
	}
	_ = req.Email // read, and not used; see the doc comment.

	assertion, session, err := a.passkeys.wa.BeginDiscoverableLogin()
	if err != nil {
		a.log.Error("could not begin a passkey login", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "")
		return
	}
	if !a.storePasskeyChallenge(w, r, session, store.PasskeyCeremonyLogin, nil) {
		return
	}

	writeJSON(w, http.StatusOK, assertion)
}

// passkeyLoginRequest is the finish body. Same shape as registration's, minus
// the label, so the client has one rule rather than two.
type passkeyLoginRequest struct {
	Credential json.RawMessage `json:"credential"`
}

func (a *Auth) handlePasskeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	var req passkeyLoginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if len(req.Credential) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_passkey",
			"send the credential from navigator.credentials.get() as the `credential` member")
		return
	}

	parsed, err := protocol.ParseCredentialRequestResponseBytes(req.Credential)
	if err != nil {
		a.refusePasskeyLogin(w, "the assertion could not be parsed", err)
		return
	}

	ch, ok := a.consumePasskeyChallenge(w, r, parsed.Response.CollectedClientData.Challenge,
		store.PasskeyCeremonyLogin, func() {
			a.refusePasskeyLogin(w, "challenge unknown, expired or already spent", nil)
		})
	if !ok {
		return
	}
	// A login challenge is begun by nobody, so a row carrying an account is a
	// registration challenge being presented here.
	if ch.UserID != nil {
		a.refusePasskeyLogin(w, "challenge was not minted for a login", nil)
		return
	}

	var session webauthn.SessionData
	if err := json.Unmarshal(ch.SessionData, &session); err != nil {
		a.log.Error("could not read a stored passkey ceremony", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "")
		return
	}

	// Resolving the account is this application's job, not the library's: the
	// assertion names a user handle and a credential, and only this side knows
	// what those mean. The closure records what it found so the handler below
	// has the row to write back to.
	var (
		owner   store.User
		matched store.PasskeyCredential
		lookup  error
	)
	discover := func(rawID, userHandle []byte) (webauthn.User, error) {
		id, err := uuid.FromBytes(userHandle)
		if err != nil {
			return nil, errors.New("the user handle is not an account id")
		}
		u, err := a.store.User(r.Context(), id)
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				lookup = err
			}
			return nil, errors.New("no such account")
		}
		// The same rule the session lookup applies: a disabled or never-active
		// account cannot log in, and a passkey is not a way around that.
		if u.Status != store.StatusActive {
			return nil, errors.New("the account is not active")
		}
		rows, err := a.store.PasskeyCredentials(r.Context(), u.ID)
		if err != nil {
			lookup = err
			return nil, errors.New("could not read the account's passkeys")
		}
		for _, row := range rows {
			if bytes.Equal(row.CredentialID, rawID) {
				matched = row
				break
			}
		}
		if matched.ID == uuid.Nil {
			return nil, errors.New("that credential is not registered to that account")
		}
		owner = u
		return newPasskeyUser(u, rows), nil
	}

	// Everything cryptographic happens inside this call: the challenge, the RP
	// ID, the origin, the flags and the signature over the authenticator data.
	cred, err := a.passkeys.wa.ValidateDiscoverableLogin(discover, session, parsed)
	if lookup != nil {
		// A database failure is this server's problem and must not be reported
		// as a refusal — a 401 would tell the person their passkey is broken
		// when it is not.
		a.log.Error("passkey login lookup failed", "err", lookup)
		writeError(w, http.StatusInternalServerError, "internal", "")
		return
	}
	if err != nil {
		a.refusePasskeyLogin(w, "the assertion did not verify", err)
		return
	}

	// The cloned-authenticator check.
	//
	// An authenticator that keeps a counter increments it on every assertion,
	// so a value that fails to advance means two things holding the same
	// private key are in use. The library sets this flag and leaves the
	// decision here; the decision is to refuse, because the alternative is to
	// accept a login from a copy of somebody's key.
	//
	// An authenticator that always reports 0 — which most synced passkeys do —
	// never trips it: the library treats a reported 0 against a stored 0 as "no
	// counter" rather than as a failure to advance, so those accounts are not
	// locked out on their second login.
	if cred.Authenticator.CloneWarning {
		a.log.Warn("passkey signature counter did not advance",
			"user", owner.ID, "credential", matched.ID,
			"stored", matched.SignCount, "presented", parsed.Response.AuthenticatorData.Counter)
		a.refusePasskeyLogin(w, "the signature counter did not advance", nil)
		return
	}

	// Recorded before the session is issued, so a counter that was accepted is
	// never left unwritten — the next assertion would then be compared against
	// a stale value and a replay of this one would pass.
	if err := a.store.TouchPasskeyCredential(r.Context(), matched.ID,
		int64(cred.Authenticator.SignCount), store.PasskeyFlags{
			BackupState:  cred.Flags.BackupState,
			UserPresent:  cred.Flags.UserPresent,
			UserVerified: cred.Flags.UserVerified,
		}); err != nil {
		a.log.Error("could not record passkey use", "user", owner.ID, "credential", matched.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "")
		return
	}

	// The same session, by the same path, as a password login. Not a parallel
	// one: a second way to mint a session is a second place for the cookie's
	// attributes, the idle window and the absolute cap to drift.
	if !a.startSession(w, r, owner, "passkey") {
		return
	}
	writeJSON(w, http.StatusOK, toDTO(owner))
}

// refusePasskeyLogin answers every failed passkey login identically.
//
// One status, one code, one empty body, whatever went wrong: an unparseable
// assertion, a spent challenge, an unknown credential, a disabled account, a
// bad signature and a counter that did not advance are indistinguishable to the
// caller. The reason goes to the log, where an operator can see it and a
// stranger cannot.
func (a *Auth) refusePasskeyLogin(w http.ResponseWriter, why string, err error) {
	if err != nil {
		a.log.Info("passkey login refused", "reason", why, "err", err)
	} else {
		a.log.Info("passkey login refused", "reason", why)
	}
	writeError(w, http.StatusUnauthorized, "invalid_credentials", "")
}

// --- managing one's own passkeys --------------------------------------------

// passkeyDTO is the wire form of a credential.
//
// A separate type, for the same reason userDTO is one: the row carries a public
// key, an AAGUID and a counter, and none of those are the browser's business.
// What is left is what a person needs to recognise a device and decide whether
// to keep it.
type passkeyDTO struct {
	ID    uuid.UUID `json:"id"`
	Label string    `json:"label"`
	// Transports is how the authenticator can be reached — "internal",
	// "hybrid", "usb". Present so the list can say "phone" rather than
	// "passkey" three times.
	Transports []string   `json:"transports"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
}

func toPasskeyDTO(row store.PasskeyCredential) passkeyDTO {
	transports := row.Transports
	if transports == nil {
		transports = []string{}
	}
	return passkeyDTO{
		ID:         row.ID,
		Label:      row.Label,
		Transports: transports,
		CreatedAt:  row.CreatedAt,
		LastUsedAt: row.LastUsedAt,
	}
}

func (a *Auth) handlePasskeyList(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "")
		return
	}
	rows, err := a.store.PasskeyCredentials(r.Context(), user.ID)
	if err != nil {
		a.log.Error("could not list passkeys", "user", user.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "")
		return
	}
	out := make([]passkeyDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, toPasskeyDTO(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{"passkeys": out})
}

// handlePasskeyDelete removes one of the caller's own credentials.
//
// Scoped by the caller's account in the statement. Somebody else's credential
// is a 404, which is also the answer for an id that never existed — deleting is
// refused and the refusal says nothing about whose it was.
func (a *Auth) handlePasskeyDelete(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "")
		return
	}
	id, ok := authPathID(w, r)
	if !ok {
		return
	}

	if err := a.store.DeletePasskeyCredential(r.Context(), user.ID, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "")
			return
		}
		a.log.Error("could not delete a passkey", "user", user.ID, "credential", id, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "")
		return
	}

	// Worth a line: removing the credential for a phone somebody no longer has
	// is a security action, and the log is where "when did that happen?" is
	// answered.
	a.log.Info("passkey removed", "user", user.ID, "credential", id)
	w.WriteHeader(http.StatusNoContent)
}

// --- shared plumbing --------------------------------------------------------

// storePasskeyChallenge records a ceremony the server has just begun.
//
// The whole SessionData goes in, verbatim: the finish step has to be given back
// exactly what the begin step produced — including which extensions were asked
// for — or verification fails on a response that was perfectly good.
func (a *Auth) storePasskeyChallenge(w http.ResponseWriter, r *http.Request, session *webauthn.SessionData, ceremony store.PasskeyCeremony, userID *uuid.UUID) bool {
	raw, err := json.Marshal(session)
	if err != nil {
		a.log.Error("could not encode a passkey ceremony", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "")
		return false
	}
	if _, err := a.store.CreatePasskeyChallenge(r.Context(), store.PasskeyChallenge{
		Challenge:   session.Challenge,
		Ceremony:    ceremony,
		UserID:      userID,
		SessionData: raw,
		ExpiresAt:   a.now().Add(passkeyChallengeTTL),
	}); err != nil {
		a.log.Error("could not record a passkey ceremony", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "")
		return false
	}
	return true
}

// consumePasskeyChallenge redeems the challenge a response carries.
//
// refuse is called for every way the challenge can be unusable — unknown,
// expired, already spent, or minted for the other ceremony — so that each
// endpoint answers those in its own voice while none of them can tell the four
// apart. A database failure is not one of them: it is a 500, because it is this
// server's fault and not the caller's.
func (a *Auth) consumePasskeyChallenge(w http.ResponseWriter, r *http.Request, challenge string, want store.PasskeyCeremony, refuse func()) (store.PasskeyChallenge, bool) {
	ch, err := a.store.ConsumePasskeyChallenge(r.Context(), challenge)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			a.log.Error("could not redeem a passkey challenge", "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "")
			return store.PasskeyChallenge{}, false
		}
		refuse()
		return store.PasskeyChallenge{}, false
	}
	if ch.Ceremony != want {
		refuse()
		return store.PasskeyChallenge{}, false
	}
	return ch, true
}

func writePasskeyChallengeRefused(w http.ResponseWriter) {
	writeError(w, http.StatusBadRequest, "invalid_challenge",
		"that ceremony is unknown, has expired, or has already been completed; start again")
}

// passkeyLabel normalises what somebody called their device.
//
// Truncated by runes rather than bytes, so a label in a script whose characters
// are three bytes each is cut at a character boundary and not through one.
func passkeyLabel(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > maxPasskeyLabel {
		s = strings.TrimSpace(string(r[:maxPasskeyLabel]))
	}
	return s
}

// decodeOptionalJSON reads a bounded JSON body, treating an empty one as the
// zero value. decodeJSON cannot: a request with nothing in it is a decode
// error there, and these are endpoints where sending nothing is correct.
func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	body, ok := readPasskeyBody(w, r)
	if !ok {
		return false
	}
	if len(body) == 0 {
		return true
	}
	if err := json.Unmarshal(body, v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "")
		return false
	}
	return true
}

// drainPasskeyBody reads and discards a body that carries nothing this endpoint
// needs, refusing one that is impossibly large.
func drainPasskeyBody(w http.ResponseWriter, r *http.Request) bool {
	_, ok := readPasskeyBody(w, r)
	return ok
}

func readPasskeyBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "")
		return nil, false
	}
	return bytes.TrimSpace(body), true
}
