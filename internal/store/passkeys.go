package store

// Passkey storage: the credential records that outlive a ceremony, and the
// challenges that must not.
//
// Nothing in this file interprets WebAuthn. It stores bytes the httpd layer
// hands it and gives them back unchanged; the protocol — attestation, COSE,
// signature verification — belongs to the library, and the boundary is here on
// purpose so that a change of library is a change to one package.
//
// The one rule this layer does enforce is ownership. A credential belongs to
// exactly one account, and every read and write that is not the login lookup is
// scoped by user_id in the SQL rather than checked afterwards in Go — a filter
// that lives in the statement cannot be forgotten at a call site.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// PasskeyCeremony names which half of the protocol a challenge was minted for.
// A challenge is single-use *and* single-purpose: the login endpoint refuses a
// registration challenge, or the two ceremonies share one pool and stop being
// distinguishable.
type PasskeyCeremony string

const (
	PasskeyCeremonyRegister PasskeyCeremony = "register"
	PasskeyCeremonyLogin    PasskeyCeremony = "login"
)

// MaxSignCount is the largest value the authenticator's use counter can take.
// It is a uint32 on the wire and a bigint in the column, because Postgres has
// no unsigned integer type; the CHECK constraint and this constant say the same
// thing in the two places a value can arrive from.
const MaxSignCount = int64(1<<32 - 1)

// PasskeyCredential is one registered authenticator.
//
// A person has several — a phone and a laptop are separate key pairs — so this
// is a list per account rather than a column on users. PublicKey verifies
// signatures and cannot produce them, so unlike password_hash there is nothing
// here that a copy of the database turns into a way in.
type PasskeyCredential struct {
	ID           uuid.UUID `db:"id"`
	UserID       uuid.UUID `db:"user_id"`
	CredentialID []byte    `db:"credential_id"`
	PublicKey    []byte    `db:"public_key"`
	// SignCount is the authenticator's own use counter as of the last
	// assertion. Zero is both the initial value and the permanent answer from
	// an authenticator that does not count; the two are indistinguishable here
	// and the handler is what knows that they are.
	SignCount         int64      `db:"sign_count"`
	AAGUID            []byte     `db:"aaguid"`
	Transports        []string   `db:"transports"`
	AttestationType   string     `db:"attestation_type"`
	AttestationFormat string     `db:"attestation_format"`
	BackupEligible    bool       `db:"backup_eligible"`
	BackupState       bool       `db:"backup_state"`
	UserPresent       bool       `db:"user_present"`
	UserVerified      bool       `db:"user_verified"`
	Label             string     `db:"label"`
	CreatedAt         time.Time  `db:"created_at"`
	LastUsedAt        *time.Time `db:"last_used_at"`
}

const passkeyCredentialColumns = `id, user_id, credential_id, public_key, sign_count, aaguid,
	transports, attestation_type, attestation_format, backup_eligible, backup_state,
	user_present, user_verified, label, created_at, last_used_at`

// recordCredentialChange appends the account's history entry for a credential
// arriving or going.
//
// The shape is the one RevokeCredentials writes, so that the three things that
// can happen to somebody's credentials read as one kind of event: a single
// field that is no column of `users`, saying what the act did and nothing
// about which credential it was, and no revision, because the account's own
// revision does not move. Null on the old side for the same reason it is there
// — the entry makes no claim about what the account had before.
func recordCredentialChange(ctx context.Context, tx pgx.Tx, userID uuid.UUID, what json.RawMessage, actor Actor) error {
	return insertChange(ctx, tx, EntityUsers, userID, ChangeUpdate, nil,
		changeSet{"credentials": {Old: jsonNull, New: what}}, actor)
}

// CreatePasskeyCredential records a registered authenticator.
//
// A credential id that is already registered — to this account or to any other
// — is a unique violation rather than a second row. Use IsUniqueViolation to
// tell that case from a real failure.
//
// A second way into an account is a change to the account, so the account's
// history gets an entry in the same transaction. It is worth having precisely
// because registering needs a live session and nothing else: a credential can
// appear on an account whose owner never registered one, and the activity is
// the only screen that would say so. What the entry says is that one appeared,
// never which — there is no admin view of anybody's passkeys, and an entry
// naming the device would be one.
func (s *Store) CreatePasskeyCredential(ctx context.Context, in PasskeyCredential) (PasskeyCredential, error) {
	// The account the credential is being added to, which is whose session it
	// was rather than a claim about who was sitting at it.
	who := resolveActor(ctx, &in.UserID)
	return inTx(ctx, s, func(tx pgx.Tx) (PasskeyCredential, error) {
		row, err := queryOne[PasskeyCredential](ctx, tx, "passkey_credentials",
			`INSERT INTO passkey_credentials
			   (user_id, credential_id, public_key, sign_count, aaguid, transports,
			    attestation_type, attestation_format, backup_eligible, backup_state,
			    user_present, user_verified, label)
			 VALUES ($1, $2, $3, $4,
			         -- An authenticator may decline to identify its model, and one
			         -- may report no transports at all. Both are absences rather
			         -- than nulls, and the columns say so; COALESCE is what lets the
			         -- caller pass the nil slice it was handed without translating.
			         COALESCE($5::bytea, '\x'::bytea), COALESCE($6::text[], '{}'::text[]),
			         $7, $8, $9, $10, $11, $12, $13)
			 RETURNING `+passkeyCredentialColumns,
			in.UserID, in.CredentialID, in.PublicKey, in.SignCount, in.AAGUID, in.Transports,
			in.AttestationType, in.AttestationFormat, in.BackupEligible, in.BackupState,
			in.UserPresent, in.UserVerified, in.Label)
		if err != nil {
			return PasskeyCredential{}, err
		}
		if err := recordCredentialChange(ctx, tx, in.UserID, json.RawMessage(`"added"`), who); err != nil {
			return PasskeyCredential{}, err
		}
		return row, nil
	})
}

// PasskeyCredentials lists one account's credentials, oldest first so the list
// a person sees does not reorder itself as they use their devices.
func (s *Store) PasskeyCredentials(ctx context.Context, userID uuid.UUID) ([]PasskeyCredential, error) {
	return queryAll[PasskeyCredential](ctx, s.pool, "passkey_credentials",
		`SELECT `+passkeyCredentialColumns+`
		   FROM passkey_credentials
		  WHERE user_id = $1
		  ORDER BY created_at, id`, userID)
}

// PasskeyCredentialByCredentialID finds a credential by the identifier the
// authenticator reports.
//
// The only lookup not scoped by account, because it is the one that answers
// "whose is this?" — which is the question a usernameless login asks.
func (s *Store) PasskeyCredentialByCredentialID(ctx context.Context, credentialID []byte) (PasskeyCredential, error) {
	return queryOne[PasskeyCredential](ctx, s.pool, "passkey_credentials",
		`SELECT `+passkeyCredentialColumns+`
		   FROM passkey_credentials WHERE credential_id = $1`, credentialID)
}

// DeletePasskeyCredential removes one of an account's own credentials.
//
// Scoped by user_id in the statement, not checked afterwards: a credential
// somebody else owns matches no rows and comes back as ErrNotFound, which is
// also the answer for an id that never existed. One response for both, so this
// cannot be used to find out which of the two it was.
//
// Recorded like the registration, and in the same transaction, so that a way
// into an account arriving and leaving are both in the history rather than
// only the arrival. A refusal records nothing: there was no change to report,
// and an entry would say a credential left an account that still has it.
func (s *Store) DeletePasskeyCredential(ctx context.Context, userID, id uuid.UUID) error {
	who := resolveActor(ctx, &userID)
	_, err := inTx(ctx, s, func(tx pgx.Tx) (struct{}, error) {
		var done struct{}
		tag, err := tx.Exec(ctx,
			`DELETE FROM passkey_credentials WHERE id = $1 AND user_id = $2`, id, userID)
		if err != nil {
			return done, fmt.Errorf("passkey_credentials: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return done, notFoundErr("passkey_credentials")
		}
		return done, recordCredentialChange(ctx, tx, userID, json.RawMessage(`"removed"`), who)
	})
	return err
}

// TouchPasskeyCredential records a successful assertion: the counter the
// authenticator reported, and the moment it was used.
//
// Called only after the assertion verified and the counter was found
// acceptable, so this never has to decide anything — it writes what the handler
// concluded.
func (s *Store) TouchPasskeyCredential(ctx context.Context, id uuid.UUID, signCount int64, flags PasskeyFlags) error {
	n, err := s.exec(ctx, "passkey_credentials",
		`UPDATE passkey_credentials
		    SET sign_count = $1, backup_state = $2, user_present = $3, user_verified = $4,
		        last_used_at = now()
		  WHERE id = $5`,
		signCount, flags.BackupState, flags.UserPresent, flags.UserVerified, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return notFoundErr("passkey_credentials")
	}
	return nil
}

// PasskeyFlags are the credential record flags that change over a credential's
// life. BackupEligible is not among them: it is fixed at registration, and an
// assertion that disagrees with it is refused rather than written down.
type PasskeyFlags struct {
	BackupState  bool
	UserPresent  bool
	UserVerified bool
}

// PasskeyChallenge is one ceremony in flight.
type PasskeyChallenge struct {
	ID        uuid.UUID       `db:"id"`
	Challenge string          `db:"challenge"`
	Ceremony  PasskeyCeremony `db:"ceremony"`
	// UserID is the account a registration was begun by, and null for a login —
	// which is begun by nobody in particular, because a discoverable credential
	// names its owner only in the response.
	UserID      *uuid.UUID `db:"user_id"`
	SessionData []byte     `db:"session_data"`
	ExpiresAt   time.Time  `db:"expires_at"`
	CreatedAt   time.Time  `db:"created_at"`
}

const passkeyChallengeColumns = `id, challenge, ceremony, user_id, session_data, expires_at, created_at`

// CreatePasskeyChallenge records a ceremony the server has just begun, and
// clears out the ones nobody finished.
//
// The sweep rides along with the insert rather than living in a ticker, because
// the only thing that creates these rows is the thing that would have to run
// the ticker, and one statement cannot drift out of step with itself. Expired
// rows are already refused by ConsumePasskeyChallenge, so this is housekeeping:
// it keeps a table that a public endpoint writes to from growing without bound.
func (s *Store) CreatePasskeyChallenge(ctx context.Context, in PasskeyChallenge) (PasskeyChallenge, error) {
	return queryOne[PasskeyChallenge](ctx, s.pool, "passkey_challenges",
		`WITH swept AS (
		   DELETE FROM passkey_challenges WHERE expires_at < now()
		 )
		 INSERT INTO passkey_challenges (challenge, ceremony, user_id, session_data, expires_at)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING `+passkeyChallengeColumns,
		in.Challenge, string(in.Ceremony), in.UserID, in.SessionData, in.ExpiresAt)
}

// ConsumePasskeyChallenge redeems a challenge, exactly once.
//
// A DELETE ... RETURNING against a unique column, so two requests presenting the
// same challenge cannot both win: the row is gone by the time the second one
// looks, and it gets ErrNotFound. A read followed by a delete would leave a
// window in which both succeed, and an assertion that can be replayed is an
// assertion that proves nothing.
//
// Expiry is in the same predicate and measured by the database's clock, so a
// challenge that has aged out is refused for the same reason and with the same
// error as one that was never issued. The expired row is left for the next
// insert to sweep; leaving it is safe precisely because this refuses it.
func (s *Store) ConsumePasskeyChallenge(ctx context.Context, challenge string) (PasskeyChallenge, error) {
	if challenge == "" {
		return PasskeyChallenge{}, notFoundErr("passkey_challenges")
	}
	return queryOne[PasskeyChallenge](ctx, s.pool, "passkey_challenges",
		`DELETE FROM passkey_challenges
		  WHERE challenge = $1 AND expires_at > now()
		 RETURNING `+passkeyChallengeColumns, challenge)
}

// DeleteExpiredPasskeyChallenges clears out ceremonies nobody finished, and
// reports how many went. The insert path already does this; this exists for a
// deployment that has stopped taking logins and for the tests.
func (s *Store) DeleteExpiredPasskeyChallenges(ctx context.Context) (int64, error) {
	return s.exec(ctx, "passkey_challenges",
		`DELETE FROM passkey_challenges WHERE expires_at < now()`)
}
