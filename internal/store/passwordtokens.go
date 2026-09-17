package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrInvalidToken reports a set-password link that cannot be redeemed.
//
// One error for unknown, expired and already-used, because the caller must not
// be able to tell them apart: "expired" confirms the link was once real, and
// "already used" confirms somebody used it. Both are answers to questions an
// attacker holding a guessed token would like answered.
var ErrInvalidToken = errors.New("store: invalid or expired token")

// TokenPurpose is why a link was issued. It changes the wording of the mail
// and nothing else — redemption is identical either way.
type TokenPurpose string

const (
	// PurposeInvite is the first link, sent when an admin creates the account.
	PurposeInvite TokenPurpose = "invite"
	// PurposeReset is every later one.
	PurposeReset TokenPurpose = "reset"
)

// PasswordToken is one single-use capability to set one account's password.
// TokenHash is the SHA-256 of the secret; the secret itself exists only in the
// link, and this layer never sees it.
type PasswordToken struct {
	ID         uuid.UUID    `db:"id"`
	UserID     uuid.UUID    `db:"user_id"`
	TokenHash  []byte       `db:"token_hash"`
	Purpose    TokenPurpose `db:"purpose"`
	ExpiresAt  time.Time    `db:"expires_at"`
	CreatedAt  time.Time    `db:"created_at"`
	ConsumedAt *time.Time   `db:"consumed_at"`
}

const passwordTokenColumns = `id, user_id, token_hash, purpose, expires_at, created_at, consumed_at`

// IssuePasswordToken records a new link and kills every outstanding one for
// the same person.
//
// Superseding matters: without it, an account that has been re-invited three
// times has three live links, and revoking the one that leaked means revoking
// all of them by hand. One live link per account is the property worth having.
func (s *Store) IssuePasswordToken(ctx context.Context, in PasswordToken) (PasswordToken, error) {
	return inTx(ctx, s, func(tx pgx.Tx) (PasswordToken, error) {
		if _, err := tx.Exec(ctx,
			`UPDATE password_tokens SET consumed_at = now()
			  WHERE user_id = $1 AND consumed_at IS NULL`, in.UserID); err != nil {
			return PasswordToken{}, fmt.Errorf("password_tokens: %w", err)
		}
		return queryOne[PasswordToken](ctx, tx, "password_tokens",
			`INSERT INTO password_tokens (user_id, token_hash, purpose, expires_at)
			 VALUES ($1, $2, COALESCE($3::text, 'invite'), $4)
			 RETURNING `+passwordTokenColumns,
			in.UserID, in.TokenHash, nullString(string(in.Purpose)), in.ExpiresAt)
	})
}

// PasswordTokenLive reports whether a token could be redeemed right now.
//
// Advisory only — the redemption below is the authority, and something can
// change between the two. It exists so that the handler can refuse an obvious
// forgery before spending 19 MiB and two Argon2 passes hashing the password
// that came with it, which would otherwise make the set-password endpoint a
// cheap way to burn this process's memory bandwidth.
func (s *Store) PasswordTokenLive(ctx context.Context, tokenHash []byte) (bool, error) {
	var live bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (
		   SELECT 1 FROM password_tokens
		    WHERE token_hash = $1 AND consumed_at IS NULL AND expires_at > now())`,
		tokenHash).Scan(&live)
	if err != nil {
		return false, fmt.Errorf("password_tokens: %w", err)
	}
	return live, nil
}

// ConsumePasswordToken redeems a link and sets the account's password, or
// fails without doing either.
//
// The single-use guarantee is the UPDATE's WHERE clause, not a read followed
// by a write. Two requests arriving with the same token at the same moment
// both try to update the same row; one takes the row lock, the other blocks.
// When the first commits, the second re-evaluates its predicate against the
// row as it now stands, finds consumed_at is no longer null, and matches
// nothing. Checking first and updating second would let both through the gap
// between the two statements.
//
// Everything else the redemption implies happens in the same transaction:
// other outstanding links for this person die, and so do their existing
// sessions. Setting a password is what somebody does when they think the old
// one leaked, and leaving the sessions it protected alive would make the act
// pointless.
func (s *Store) ConsumePasswordToken(ctx context.Context, tokenHash []byte, passwordHash string) (User, error) {
	return inTx(ctx, s, func(tx pgx.Tx) (User, error) {
		var userID uuid.UUID
		err := tx.QueryRow(ctx,
			`UPDATE password_tokens
			    SET consumed_at = now()
			  WHERE token_hash = $1 AND consumed_at IS NULL AND expires_at > now()
			RETURNING user_id`, tokenHash).Scan(&userID)
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, ErrInvalidToken
		}
		if err != nil {
			return User{}, fmt.Errorf("password_tokens: %w", err)
		}

		// `invited` becomes `active`; anything else keeps the status it had.
		// A blanket 'active' would let a redeemed link resurrect an account
		// somebody deliberately disabled — and a disabled account's link is
		// refused outright, which is what the status check in the WHERE does.
		user, err := queryOne[User](ctx, tx, "users",
			`UPDATE users
			    SET password_hash = $1,
			        status = CASE WHEN status = 'invited' THEN 'active' ELSE status END,
			        revision = revision + 1,
			        updated_at = now()
			  WHERE id = $2 AND status <> 'disabled'
			RETURNING `+userColumns,
			passwordHash, userID)
		if isNotFound(err) {
			// Disabled, or deleted between the two statements. The token stays
			// unconsumed because this transaction rolls back, which is
			// harmless: it is attached to an account that cannot be logged in
			// to either way.
			return User{}, ErrInvalidToken
		}
		if err != nil {
			return User{}, err
		}

		if _, err := tx.Exec(ctx,
			`UPDATE password_tokens SET consumed_at = now()
			  WHERE user_id = $1 AND consumed_at IS NULL`, userID); err != nil {
			return User{}, fmt.Errorf("password_tokens: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, userID); err != nil {
			return User{}, fmt.Errorf("sessions: %w", err)
		}

		return user, nil
	})
}

// DeleteExpiredPasswordTokens removes links that are spent or long past their
// expiry, and reports how many went.
//
// Kept a week beyond expiry rather than deleted on the spot: a redeemed row is
// the only evidence that a link was used, and an account that has been taken
// over is investigated after the fact, not during.
func (s *Store) DeleteExpiredPasswordTokens(ctx context.Context, grace time.Duration) (int64, error) {
	return s.exec(ctx, "password_tokens",
		`DELETE FROM password_tokens
		  WHERE expires_at < now() - make_interval(secs => $1)
		     OR consumed_at < now() - make_interval(secs => $1)`,
		grace.Seconds())
}

// SetPasswordHash rewrites only the stored hash.
//
// No revision check, unlike every other write in this package, and
// deliberately: this runs during a successful login to re-encode a correct
// password at raised cost parameters. The password has not changed — only its
// encoding has — so there is no concurrent edit for the caller to reconcile
// with, and refusing the write would leave the account on weak parameters
// forever because of a race that changed nothing.
func (s *Store) SetPasswordHash(ctx context.Context, id uuid.UUID, hash string) error {
	n, err := s.exec(ctx, "users",
		`UPDATE users SET password_hash = $1, revision = revision + 1, updated_at = now()
		  WHERE id = $2`, hash, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return notFoundErr("users")
	}
	return nil
}

// EnsureBootstrapAdmin creates the first admin if the deployment has none, and
// reports whether it created one.
//
// No password and no link: the account lands in `invited` exactly like every
// other, and the person named picks their own password through the normal
// flow. This exists only to break the circularity of "accounts are created by
// an admin" in a database where no admin exists yet.
//
// The whole decision is one statement, so two replicas starting together
// cannot both create one. ON CONFLICT covers the other order of events — the
// address already exists as a viewer — where the right answer is to leave the
// existing account alone rather than to fail startup.
func (s *Store) EnsureBootstrapAdmin(ctx context.Context, email string) (bool, error) {
	n, err := s.exec(ctx, "users",
		`INSERT INTO users (email, role, status)
		 SELECT $1, 'admin', 'invited'
		  WHERE NOT EXISTS (SELECT 1 FROM users WHERE role = 'admin')
		 ON CONFLICT DO NOTHING`, email)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// IsUniqueViolation reports whether err is Postgres refusing a duplicate.
//
// Exposed so a handler can answer "that address already has an account" with a
// 409 instead of a 500, without importing pgconn and knowing SQLSTATE codes.
func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
