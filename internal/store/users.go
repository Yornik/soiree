package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Role is what an account may do. Enforced server-side and mirrored by a
// CHECK constraint, because a role the database will not store is better than
// a role only the handler remembers to look at.
type Role string

const (
	RoleAdmin  Role = "admin"
	RoleEditor Role = "editor"
	RoleViewer Role = "viewer"
)

// UserStatus tracks an account through invitation. Accounts are created by an
// admin, so "exists but has never had a password" is a normal state rather
// than an anomaly.
type UserStatus string

const (
	StatusInvited  UserStatus = "invited"
	StatusActive   UserStatus = "active"
	StatusDisabled UserStatus = "disabled"
)

// User is an account. PasswordHash is nil until the person follows their
// one-time link; it is an Argon2id encoded hash and this layer never looks
// inside it.
type User struct {
	ID           uuid.UUID  `db:"id"`
	Email        string     `db:"email"`
	Role         Role       `db:"role"`
	PasswordHash *string    `db:"password_hash"`
	Status       UserStatus `db:"status"`
	CreatedBy    *uuid.UUID `db:"created_by"`
	CreatedAt    time.Time  `db:"created_at"`
	Revision     int64      `db:"revision"`
	UpdatedAt    time.Time  `db:"updated_at"`
}

const userColumns = `id, email, role, password_hash, status, created_by, created_at, revision, updated_at`

// CreateUser inserts an account. A zero ID lets the database generate one.
//
// The account's history records the role it was given and the address it was
// given to — "who made Linus an admin" and "who changed Ada's login address"
// are both worth answering — but never the password hash. See redactedFields
// in audit.go.
func (s *Store) CreateUser(ctx context.Context, in User) (User, error) {
	actor := resolveActor(ctx, in.CreatedBy)
	return createAudited(ctx, s, EntityUsers, actor, func(tx pgx.Tx) (User, error) {
		return queryOne[User](ctx, tx, "users",
			`INSERT INTO users (id, email, role, password_hash, status, created_by)
			 VALUES (COALESCE($1, gen_random_uuid()), $2, COALESCE($3::text, 'viewer'), $4, COALESCE($5::text, 'invited'), $6)
			 RETURNING `+userColumns,
			newID(in.ID), in.Email, nullString(string(in.Role)), in.PasswordHash,
			nullString(string(in.Status)), in.CreatedBy)
	})
}

// User reads one account by id.
func (s *Store) User(ctx context.Context, id uuid.UUID) (User, error) {
	return queryOne[User](ctx, s.pool, "users",
		`SELECT `+userColumns+` FROM users WHERE id = $1`, id)
}

// UserByEmail reads one account by address, case-insensitively — matching the
// unique index, so this cannot find an account that a second signup would be
// allowed to duplicate.
func (s *Store) UserByEmail(ctx context.Context, email string) (User, error) {
	return queryOne[User](ctx, s.pool, "users",
		`SELECT `+userColumns+` FROM users WHERE lower(email) = lower($1)`, email)
}

// Users lists every account, ordered for display.
func (s *Store) Users(ctx context.Context) ([]User, error) {
	return queryAll[User](ctx, s.pool, "users",
		`SELECT `+userColumns+` FROM users ORDER BY lower(email), id`)
}

// UpdateUser writes every mutable field of an account, refusing the write if
// in.Revision is no longer current.
func (s *Store) UpdateUser(ctx context.Context, in User) (User, error) {
	actor := resolveActor(ctx, nil)
	out, err := updateAudited(ctx, s, EntityUsers, userColumns, in.ID, actor,
		func(tx pgx.Tx, _ User) (User, error) {
			return queryOne[User](ctx, tx, "users",
				`UPDATE users
				    SET email = $1, role = $2, password_hash = $3, status = $4,
				        revision = revision + 1, updated_at = now()
				  WHERE id = $5 AND revision = $6
				RETURNING `+userColumns,
				in.Email, in.Role, in.PasswordHash, in.Status, in.ID, in.Revision)
		})
	if err == nil {
		return out, nil
	}
	if !isNotFound(err) {
		return User{}, err
	}

	current, err := s.User(ctx, in.ID)
	return User{}, conflict("users", in.ID, in.Revision, current, err)
}

// DeleteUser removes an account, refusing if revision is no longer current.
// Their UI preferences go with them; their attributions on shared rows do not
// — updated_by becomes null, so the plan's history degrades to "someone"
// rather than losing the row.
//
// The change log does the same: every entry they wrote survives with a null
// actor. The alternative — deleting their entries — would let anyone erase what
// they changed by deleting their own account, and the money would still have
// moved.
func (s *Store) DeleteUser(ctx context.Context, id uuid.UUID, revision int64) error {
	err := deleteAudited[User](ctx, s, EntityUsers, userColumns, id, revision)
	if err == nil {
		return nil
	}
	if !isNotFound(err) {
		return err
	}

	current, err := s.User(ctx, id)
	return conflict("users", id, revision, current, err)
}

// nullString lets a create fall back to the column's default instead of
// writing an empty string that would fail the CHECK constraint.
func nullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// NotifiableAdmins returns the email addresses of every admin who can actually
// receive mail, ordered for a stable recipient list.
//
// Only `active` accounts: an `invited` admin has never set a password and may
// not be a real address yet, and a `disabled` one has had their access removed
// on purpose — mailing either would be sending the event's finances to someone
// who is not currently trusted with them.
//
// Resolved at send time rather than configured, so an admin added next month
// is on the next digest without anybody editing a deployment.
func (s *Store) NotifiableAdmins(ctx context.Context) ([]string, error) {
	rows, err := queryAll[struct {
		Email string `db:"email"`
	}](ctx, s.pool, "users",
		`SELECT email FROM users
		  WHERE role = 'admin' AND status = 'active'
		  ORDER BY lower(email)`)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Email)
	}
	return out, nil
}
