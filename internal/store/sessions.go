package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Session is one logged-in browser. TokenHash is the SHA-256 of the cookie's
// value; the value itself is only ever in the cookie.
type Session struct {
	ID         uuid.UUID `db:"id"`
	UserID     uuid.UUID `db:"user_id"`
	TokenHash  []byte    `db:"token_hash"`
	CreatedAt  time.Time `db:"created_at"`
	LastSeenAt time.Time `db:"last_seen_at"`
	ExpiresAt  time.Time `db:"expires_at"`
}

const sessionColumns = `id, user_id, token_hash, created_at, last_seen_at, expires_at`

// CreateSession records a login.
func (s *Store) CreateSession(ctx context.Context, in Session) (Session, error) {
	return queryOne[Session](ctx, s.pool, "sessions",
		`INSERT INTO sessions (user_id, token_hash, expires_at)
		 VALUES ($1, $2, $3)
		 RETURNING `+sessionColumns,
		in.UserID, in.TokenHash, in.ExpiresAt)
}

// SessionByToken resolves a cookie to a session and the account it belongs to,
// in one round trip because every authenticated request needs both.
//
// Four things can make a cookie stop working, and all four are expressed here
// rather than in the caller so that none of them can be forgotten at one call
// site: the session was revoked, its idle window has passed, it is older than
// maxLifetime however much it has been used, or the account is no longer
// active. Every one of them returns ErrNotFound, so a disabled account's
// cookie is indistinguishable from no cookie at all.
func (s *Store) SessionByToken(ctx context.Context, tokenHash []byte, maxLifetime time.Duration) (Session, User, error) {
	var sess Session
	var u User
	err := s.pool.QueryRow(ctx,
		`SELECT s.id, s.user_id, s.token_hash, s.created_at, s.last_seen_at, s.expires_at,
		        u.id, u.email, u.role, u.password_hash, u.status, u.created_by,
		        u.created_at, u.revision, u.updated_at, u.language
		   FROM sessions s
		   JOIN users u ON u.id = s.user_id
		  WHERE s.token_hash = $1
		    AND s.expires_at > now()
		    AND s.created_at > now() - make_interval(secs => $2)
		    AND u.status = 'active'`,
		tokenHash, maxLifetime.Seconds()).
		Scan(&sess.ID, &sess.UserID, &sess.TokenHash, &sess.CreatedAt, &sess.LastSeenAt, &sess.ExpiresAt,
			&u.ID, &u.Email, &u.Role, &u.PasswordHash, &u.Status, &u.CreatedBy,
			&u.CreatedAt, &u.Revision, &u.UpdatedAt, &u.Language)
	if err != nil {
		return Session{}, User{}, notFound(err, "sessions")
	}
	return sess, u, nil
}

// TouchSession slides a session's idle expiry forward.
//
// Called at most once an hour per session rather than on every request: the
// point of the idle window is to notice a browser that has gone quiet for
// days, and a write per request to record that would cost far more than it
// tells anybody.
func (s *Store) TouchSession(ctx context.Context, id uuid.UUID, expiresAt time.Time) error {
	if _, err := s.exec(ctx, "sessions",
		`UPDATE sessions SET last_seen_at = now(), expires_at = $1 WHERE id = $2`,
		expiresAt, id); err != nil {
		return err
	}
	return nil
}

// DeleteSessionByToken logs one browser out. Deleting a session that is
// already gone is not an error: logging out twice is not a failure.
func (s *Store) DeleteSessionByToken(ctx context.Context, tokenHash []byte) error {
	_, err := s.exec(ctx, "sessions", `DELETE FROM sessions WHERE token_hash = $1`, tokenHash)
	return err
}

// DeleteSessionsForUser logs an account out everywhere. This is what a role
// change, a disable and a password reset all reduce to.
func (s *Store) DeleteSessionsForUser(ctx context.Context, userID uuid.UUID) (int64, error) {
	return s.exec(ctx, "sessions", `DELETE FROM sessions WHERE user_id = $1`, userID)
}

// DeleteExpiredSessions removes sessions past their idle window or older than
// maxLifetime, and reports how many went.
//
// Nothing depends on this for correctness — SessionByToken already refuses
// them — so it is housekeeping rather than security. It keeps the table from
// growing without bound in a deployment that runs for a year.
func (s *Store) DeleteExpiredSessions(ctx context.Context, maxLifetime time.Duration) (int64, error) {
	return s.exec(ctx, "sessions",
		`DELETE FROM sessions
		  WHERE expires_at < now()
		     OR created_at < now() - make_interval(secs => $1)`,
		maxLifetime.Seconds())
}

// Sessions lists an account's live sessions, newest first. Not exposed over
// HTTP yet; it is what "you are signed in on three devices" will read.
func (s *Store) Sessions(ctx context.Context, userID uuid.UUID) ([]Session, error) {
	out, err := queryAll[Session](ctx, s.pool, "sessions",
		`SELECT `+sessionColumns+`
		   FROM sessions
		  WHERE user_id = $1 AND expires_at > now()
		  ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("sessions: %w", err)
	}
	return out, nil
}
