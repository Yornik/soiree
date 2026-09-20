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
		// The account's status is part of "could be redeemed": a disabled one
		// refuses below, and without this check each attempt would still pay
		// for a full password hash on the way to being told no.
		`SELECT EXISTS (
		   SELECT 1 FROM password_tokens t
		     JOIN users u ON u.id = t.user_id
		    WHERE t.token_hash = $1
		      AND t.consumed_at IS NULL
		      AND t.expires_at > now()
		      AND u.status <> 'disabled')`,
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

		// The account as it stands, held until this transaction ends: the
		// "before" side of the history entry further down, and the reason a
		// second redemption of the same link queues here rather than at the
		// write.
		before, err := lockRow[User](ctx, tx, EntityUsers, userColumns, userID)
		if isNotFound(err) {
			// Deleted between the two statements, which the UPDATE below
			// would have found for itself: one answer for every way a link
			// can fail to be redeemable.
			return User{}, ErrInvalidToken
		}
		if err != nil {
			return User{}, err
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

		// Setting a password is a change to the account like any other, and
		// the one somebody goes looking for after a suspected takeover. The
		// hash is redacted in the entry, so what it records is that the
		// credential changed and that an invited account became active.
		//
		// The actor is the account itself: there is no session at redemption,
		// so the only thing the entry can name is who the link belonged to,
		// which is not the same claim as who was holding it.
		if err := recordUpdate(ctx, tx, EntityUsers, userID, &user.Revision, before, user,
			Actor{ID: &userID}); err != nil {
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
//
// It is also the one write to `users` that records nothing, for the same
// reason: the stored bytes change and the password does not, so there is
// nothing here that a history entry could tell anybody. The revision it bumps
// without an entry is a gap in the log for that reason, not a missed edit.
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
// passwordHash is optional. Empty leaves the account `invited` with no
// password, so the person named picks their own through the normal
// set-password link and no credential is ever written down. That is the better
// shape whenever mail works.
//
// Supplying one creates the account `active`, able to log in immediately. It
// exists because the mail path is a dead end for the *first* account
// specifically: password-reset mints a link and hands it straight to the
// mailer, and every route that would return the link instead is admin-only —
// which is the session you are trying to obtain. Without this, a deployment
// whose SMTP is misconfigured has no way into its own instance short of
// editing password_hash by hand in psql.
//
// Either way the `WHERE NOT EXISTS` is what makes the variable safe to leave
// set forever: once any admin exists this inserts nothing. It cannot resurrect
// an account somebody disabled on purpose, and cannot reset a password that
// has since been changed.
//
// The whole decision is one statement, so two replicas starting together
// cannot both create one. ON CONFLICT covers the other order of events — the
// address already exists as a viewer — where the right answer is to leave the
// existing account alone rather than to fail startup.
func (s *Store) EnsureBootstrapAdmin(ctx context.Context, email, passwordHash string) (bool, error) {
	status := "invited"
	var hash any // NULL unless a password was supplied
	if passwordHash != "" {
		status = "active"
		hash = passwordHash
	}

	return inTx(ctx, s, func(tx pgx.Tx) (bool, error) {
		user, err := queryOne[User](ctx, tx, "users",
			`INSERT INTO users (email, role, status, password_hash)
			 SELECT $1, 'admin', $2, $3
			  WHERE NOT EXISTS (SELECT 1 FROM users WHERE role = 'admin')
			 ON CONFLICT DO NOTHING
			 RETURNING `+userColumns, email, status, hash)
		if isNotFound(err) {
			// An admin already exists, or the address does. Nothing was
			// written, so there is nothing to report and nothing to record.
			return false, nil
		}
		if err != nil {
			return false, err
		}
		// Recorded as the deployment's own doing, because at startup there is
		// nobody else it could be. The entry is also where the account gets
		// its name from: the activity feed reads an address out of the log
		// rather than out of the row, so without this every later change to
		// the first admin would be reported with no name attached.
		if err := recordCreate(ctx, tx, EntityUsers, user.ID, &user.Revision, user, SystemActor); err != nil {
			return false, err
		}
		return true, nil
	})
}

// IsUniqueViolation reports whether err is Postgres refusing a duplicate.
//
// Exposed so a handler can answer "that address already has an account" with a
// 409 instead of a 500, without importing pgconn and knowing SQLSTATE codes.
func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
