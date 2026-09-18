package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// PushSubscription is one browser on one device that has asked to be told
// about approaching deadlines.
//
// P256dh and Auth are base64url exactly as the Push API handed them to the
// page. This layer never decodes them; internal/push does, and a value that
// does not decode is that subscription's problem rather than the table's.
type PushSubscription struct {
	ID         uuid.UUID `db:"id"`
	UserID     uuid.UUID `db:"user_id"`
	Endpoint   string    `db:"endpoint"`
	P256dh     string    `db:"p256dh"`
	Auth       string    `db:"auth"`
	CreatedAt  time.Time `db:"created_at"`
	LastSeenAt time.Time `db:"last_seen_at"`
}

const pushSubscriptionColumns = `id, user_id, endpoint, p256dh, auth, created_at, last_seen_at`

// SavePushSubscription records a device, or refreshes one already recorded.
//
// Idempotent on the endpoint, because the browser re-sends the same
// subscription on every visit: the Push API hands the page whatever
// subscription already exists rather than minting a new one, and a plain
// INSERT would either fail on the unique index or accumulate a row per page
// load. Upserting also fixes the two things that legitimately change under a
// stable endpoint — the keys, which a browser may rotate, and the owner, when
// somebody else logs in on the same device.
func (s *Store) SavePushSubscription(ctx context.Context, in PushSubscription) (PushSubscription, error) {
	return queryOne[PushSubscription](ctx, s.pool, "push_subscriptions",
		`INSERT INTO push_subscriptions (user_id, endpoint, p256dh, auth)
		      VALUES ($1, $2, $3, $4)
		 ON CONFLICT (endpoint) DO UPDATE
		         SET user_id = EXCLUDED.user_id,
		             p256dh = EXCLUDED.p256dh,
		             auth = EXCLUDED.auth,
		             last_seen_at = now()
		   RETURNING `+pushSubscriptionColumns,
		in.UserID, in.Endpoint, in.P256dh, in.Auth)
}

// DeletePushSubscription removes one device belonging to one account, and
// reports how many rows went.
//
// Scoped to the owner so that holding somebody else's endpoint — which is not
// a secret, only an unguessable URL — is not enough to switch their
// notifications off. Deleting something already gone is not an error: turning
// a notification off twice is not a failure.
func (s *Store) DeletePushSubscription(ctx context.Context, userID uuid.UUID, endpoint string) (int64, error) {
	return s.exec(ctx, "push_subscriptions",
		`DELETE FROM push_subscriptions WHERE user_id = $1 AND endpoint = $2`,
		userID, endpoint)
}

// DeletePushSubscriptionsByEndpoint removes subscriptions a push service has
// said are gone for good, whoever they belong to, and reports how many went.
//
// Deliberately not scoped to an owner: this is the sender's pruning path, and
// what it has in hand is the endpoint a 404 or a 410 came back from. Nothing
// is being authorised here — the push service has already stated that this
// subscription no longer exists anywhere, so the row is a fact about the past.
//
// Without it the table fills with endpoints that will never accept another
// notification, and every digest pays a network round trip for each of them,
// forever.
func (s *Store) DeletePushSubscriptionsByEndpoint(ctx context.Context, endpoints []string) (int64, error) {
	if len(endpoints) == 0 {
		return 0, nil
	}
	return s.exec(ctx, "push_subscriptions",
		`DELETE FROM push_subscriptions WHERE endpoint = ANY($1)`, endpoints)
}

// PushSubscriptionsForUser lists one account's devices, newest first.
func (s *Store) PushSubscriptionsForUser(ctx context.Context, userID uuid.UUID) ([]PushSubscription, error) {
	return queryAll[PushSubscription](ctx, s.pool, "push_subscriptions",
		`SELECT `+pushSubscriptionColumns+`
		   FROM push_subscriptions
		  WHERE user_id = $1
		  ORDER BY created_at DESC, id`, userID)
}

// NotifiablePushSubscriptions returns every device that should receive the
// deadline digest.
//
// The audience is the one NotifiableAdmins already defines — active admins —
// because push is a second channel for the same digest, not a second digest.
// An invited admin has never proved the account is theirs and a disabled one
// has had their access removed on purpose; a subscription made before either
// happened must not keep delivering the event's finances to a device.
//
// Resolved per send, like the mail recipients, so demoting somebody takes
// effect on the next digest without anybody deleting a row.
func (s *Store) NotifiablePushSubscriptions(ctx context.Context) ([]PushSubscription, error) {
	return queryAll[PushSubscription](ctx, s.pool, "push_subscriptions",
		`SELECT p.id, p.user_id, p.endpoint, p.p256dh, p.auth, p.created_at, p.last_seen_at
		   FROM push_subscriptions p
		   JOIN users u ON u.id = p.user_id
		  WHERE u.role = 'admin' AND u.status = 'active'
		  ORDER BY p.created_at, p.id`)
}
