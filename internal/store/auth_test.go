package store_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Yornik/soiree/internal/auth"
	"github.com/Yornik/soiree/internal/store"
)

// Ada, Grace and Linus at example.test throughout. The schema came from real
// planning data; none of the people did.

// invite creates an account and a live set-password link for it, returning the
// account and the token's hash.
func invite(t *testing.T, s *store.Store, email string, role store.Role) (store.User, []byte) {
	t.Helper()
	ctx := t.Context()

	user, err := s.CreateUser(ctx, store.User{Email: email, Role: role})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	token, err := auth.NewToken()
	if err != nil {
		t.Fatalf("new token: %v", err)
	}
	hash := auth.HashToken(token)
	if _, err := s.IssuePasswordToken(ctx, store.PasswordToken{
		UserID:    user.ID,
		TokenHash: hash,
		Purpose:   store.PurposeInvite,
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}); err != nil {
		t.Fatalf("issue token: %v", err)
	}
	return user, hash
}

func TestPasswordTokenIsSingleUse(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	user, hash := invite(t, s, "ada@example.test", store.RoleEditor)

	activated, err := s.ConsumePasswordToken(ctx, hash, "$argon2id$first")
	if err != nil {
		t.Fatalf("first redemption: %v", err)
	}
	if activated.Status != store.StatusActive {
		t.Errorf("status = %q, want active: following the link is what activates an account", activated.Status)
	}
	if activated.PasswordHash == nil || *activated.PasswordHash != "$argon2id$first" {
		t.Errorf("passwordHash = %v, want the hash that was passed in", activated.PasswordHash)
	}
	if activated.Revision != user.Revision+1 {
		t.Errorf("revision = %d, want %d", activated.Revision, user.Revision+1)
	}

	// The second attempt must fail, and must fail the same way an invented
	// token does. A link that still works after it has been used is a link
	// that works for whoever else read the mailbox.
	if _, err := s.ConsumePasswordToken(ctx, hash, "$argon2id$second"); !errors.Is(err, store.ErrInvalidToken) {
		t.Fatalf("second redemption: err = %v, want ErrInvalidToken", err)
	}

	reread, err := s.User(ctx, user.ID)
	if err != nil {
		t.Fatalf("read user: %v", err)
	}
	if *reread.PasswordHash != "$argon2id$first" {
		t.Error("the refused second redemption still changed the password")
	}
}

func TestPasswordTokenExpires(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	user, err := s.CreateUser(ctx, store.User{Email: "grace@example.test"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	hash := auth.HashToken("expired-token-stand-in")
	if _, err := s.IssuePasswordToken(ctx, store.PasswordToken{
		UserID:    user.ID,
		TokenHash: hash,
		Purpose:   store.PurposeInvite,
		// A minute in the past: the 24h window is policy, the comparison is
		// what is under test.
		ExpiresAt: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("issue token: %v", err)
	}

	if live, err := s.PasswordTokenLive(ctx, hash); err != nil || live {
		t.Fatalf("PasswordTokenLive() = %v, %v; an expired token is not live", live, err)
	}
	// Same error as unknown and as already-used. Telling them apart tells an
	// attacker which of their guesses was once a real link.
	if _, err := s.ConsumePasswordToken(ctx, hash, "$argon2id$nope"); !errors.Is(err, store.ErrInvalidToken) {
		t.Fatalf("redeeming an expired token: err = %v, want ErrInvalidToken", err)
	}

	reread, err := s.User(ctx, user.ID)
	if err != nil {
		t.Fatalf("read user: %v", err)
	}
	if reread.PasswordHash != nil || reread.Status != store.StatusInvited {
		t.Errorf("account = %q/%v, want it untouched by the expired link", reread.Status, reread.PasswordHash)
	}
}

func TestPasswordTokenConcurrentRedemptionHasOneWinner(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	user, hash := invite(t, s, "linus@example.test", store.RoleViewer)

	// Both arrive with the same link at the same moment, on separate pooled
	// connections so they are genuinely two transactions racing. One takes the
	// row lock; the other blocks, then re-evaluates its predicate against the
	// committed row and matches nothing. A read-then-write would let both
	// through the gap between the two statements, and the loser would silently
	// overwrite the winner's password.
	const attempts = 2
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		winners []string
		losers  int
	)
	start := make(chan struct{})
	for i := range attempts {
		hashArg := "$argon2id$racer-" + string(rune('a'+i))
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := s.ConsumePasswordToken(ctx, hash, hashArg)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				winners = append(winners, hashArg)
			case errors.Is(err, store.ErrInvalidToken):
				losers++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if len(winners) != 1 || losers != attempts-1 {
		t.Fatalf("%d redemptions succeeded and %d were refused, want exactly 1 and %d", len(winners), losers, attempts-1)
	}

	reread, err := s.User(ctx, user.ID)
	if err != nil {
		t.Fatalf("read user: %v", err)
	}
	if reread.PasswordHash == nil || *reread.PasswordHash != winners[0] {
		t.Errorf("stored hash = %v, want the winner's %q", reread.PasswordHash, winners[0])
	}
}

func TestIssuingSupersedesOutstandingTokens(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	user, first := invite(t, s, "ada@example.test", store.RoleAdmin)

	second := auth.HashToken("the-second-link")
	if _, err := s.IssuePasswordToken(ctx, store.PasswordToken{
		UserID:    user.ID,
		TokenHash: second,
		Purpose:   store.PurposeReset,
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}); err != nil {
		t.Fatalf("issue second token: %v", err)
	}

	// Re-inviting is also how a link that leaked is revoked, so the old one
	// has to stop working rather than sit alongside the new one.
	if live, err := s.PasswordTokenLive(ctx, first); err != nil || live {
		t.Fatalf("the superseded link is still live (%v, %v)", live, err)
	}
	if live, err := s.PasswordTokenLive(ctx, second); err != nil || !live {
		t.Fatalf("the newest link is not live (%v, %v)", live, err)
	}
}

func TestDisabledAccountCannotRedeemALink(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	user, hash := invite(t, s, "grace@example.test", store.RoleEditor)

	user.Status = store.StatusDisabled
	disabled, err := s.UpdateUser(ctx, user)
	if err != nil {
		t.Fatalf("disable user: %v", err)
	}

	// A link issued before the account was shut off must not be a way back in
	// — that is the difference between disabling somebody and asking them
	// nicely.
	if _, err := s.ConsumePasswordToken(ctx, hash, "$argon2id$back-in"); !errors.Is(err, store.ErrInvalidToken) {
		t.Fatalf("redeeming against a disabled account: err = %v, want ErrInvalidToken", err)
	}

	reread, err := s.User(ctx, disabled.ID)
	if err != nil {
		t.Fatalf("read user: %v", err)
	}
	if reread.Status != store.StatusDisabled || reread.PasswordHash != nil {
		t.Errorf("account = %q/%v, want it still disabled with no password", reread.Status, reread.PasswordHash)
	}
}

func TestRedeemingRevokesExistingSessions(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	user, hash := invite(t, s, "linus@example.test", store.RoleEditor)
	if _, err := s.ConsumePasswordToken(ctx, hash, "$argon2id$one"); err != nil {
		t.Fatalf("redeem: %v", err)
	}

	cookie := auth.HashToken("a-live-session")
	if _, err := s.CreateSession(ctx, store.Session{
		UserID:    user.ID,
		TokenHash: cookie,
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// A second link, then a second redemption: setting a password is what
	// somebody does when they think the old one leaked, so the sessions it
	// protected have to go with it.
	second := auth.HashToken("the-reset-link")
	if _, err := s.IssuePasswordToken(ctx, store.PasswordToken{
		UserID: user.ID, TokenHash: second, Purpose: store.PurposeReset,
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("issue reset: %v", err)
	}
	if _, err := s.ConsumePasswordToken(ctx, second, "$argon2id$two"); err != nil {
		t.Fatalf("redeem reset: %v", err)
	}

	if _, _, err := s.SessionByToken(ctx, cookie, time.Hour); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("session survived a password reset: err = %v, want ErrNotFound", err)
	}
}

func TestSessionLifecycle(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	user, hash := invite(t, s, "ada@example.test", store.RoleAdmin)
	if _, err := s.ConsumePasswordToken(ctx, hash, "$argon2id$stored"); err != nil {
		t.Fatalf("redeem: %v", err)
	}

	cookie := auth.HashToken("session-one")
	sess, err := s.CreateSession(ctx, store.Session{
		UserID:    user.ID,
		TokenHash: cookie,
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	got, gotUser, err := s.SessionByToken(ctx, cookie, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("session by token: %v", err)
	}
	if got.ID != sess.ID || gotUser.ID != user.ID {
		t.Fatalf("resolved to %v/%v, want %v/%v", got.ID, gotUser.ID, sess.ID, user.ID)
	}
	// The role has to come from the row, not from anything the client holds:
	// that is what makes a role change take effect on the next request.
	if gotUser.Role != store.RoleAdmin {
		t.Errorf("role = %q, want admin", gotUser.Role)
	}

	// An absolute cap shorter than the session's age retires it however
	// recently it was used.
	if _, _, err := s.SessionByToken(ctx, cookie, time.Nanosecond); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a session past the absolute cap resolved anyway: %v", err)
	}

	// Disabling somebody logs them out, without anything having to remember to
	// go and delete their sessions.
	gotUser.Status = store.StatusDisabled
	if _, err := s.UpdateUser(ctx, gotUser); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, _, err := s.SessionByToken(ctx, cookie, 30*24*time.Hour); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a disabled account's session still resolves: %v", err)
	}
}

func TestDeleteSessionsAndSweep(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	user, hash := invite(t, s, "grace@example.test", store.RoleEditor)
	if _, err := s.ConsumePasswordToken(ctx, hash, "$argon2id$stored"); err != nil {
		t.Fatalf("redeem: %v", err)
	}

	live := auth.HashToken("live")
	dead := auth.HashToken("dead")
	for h, exp := range map[string]time.Duration{string(live): time.Hour, string(dead): -time.Hour} {
		if _, err := s.CreateSession(ctx, store.Session{
			UserID: user.ID, TokenHash: []byte(h), ExpiresAt: time.Now().Add(exp),
		}); err != nil {
			t.Fatalf("create session: %v", err)
		}
	}

	sessions, err := s.Sessions(ctx, user.ID)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("listed %d live sessions, want 1", len(sessions))
	}

	n, err := s.DeleteExpiredSessions(ctx, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 1 {
		t.Errorf("swept %d sessions, want 1", n)
	}

	// Logging out twice is not a failure.
	if err := s.DeleteSessionByToken(ctx, live); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	if err := s.DeleteSessionByToken(ctx, live); err != nil {
		t.Fatalf("deleting an already-deleted session: %v", err)
	}
}

func TestBootstrapAdminRunsOnce(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	created, err := s.EnsureBootstrapAdmin(ctx, "ada@example.test", "")
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if !created {
		t.Fatal("no admin was created on an empty database")
	}

	admin, err := s.UserByEmail(ctx, "ada@example.test")
	if err != nil {
		t.Fatalf("read bootstrap admin: %v", err)
	}
	// Invited with no password, exactly like every other account: the admin
	// who creates an account never picks its password, and that has to hold
	// for the first one too.
	if admin.Role != store.RoleAdmin || admin.Status != store.StatusInvited || admin.PasswordHash != nil {
		t.Fatalf("bootstrap admin = %q/%q/%v, want admin/invited/nil", admin.Role, admin.Status, admin.PasswordHash)
	}
	if admin.CreatedBy != nil {
		t.Error("the first admin was created by nobody; createdBy should be null")
	}

	// Restarting the process must not create a second one, and must not
	// resurrect the flow after somebody has changed the address.
	for _, email := range []string{"ada@example.test", "grace@example.test"} {
		created, err := s.EnsureBootstrapAdmin(ctx, email, "")
		if err != nil {
			t.Fatalf("second bootstrap: %v", err)
		}
		if created {
			t.Errorf("EnsureBootstrapAdmin(%q) created another admin", email)
		}
	}

	users, err := s.Users(ctx)
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("%d accounts exist, want 1", len(users))
	}
}

// A bootstrap password is the way in when mail is not available. Without it
// the first account is reachable only through a mailed link, and for the first
// account specifically that is a dead end: password-reset hands the link to
// the mailer and discards it, and every route that returns one instead needs
// the admin session being sought.
func TestBootstrapAdminWithAPasswordCanLogIn(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	hash, err := auth.Hash("correct horse battery staple")
	if err != nil {
		t.Fatalf("Hash(): %v", err)
	}

	created, err := s.EnsureBootstrapAdmin(ctx, "ada@example.test", hash)
	if err != nil {
		t.Fatalf("EnsureBootstrapAdmin(): %v", err)
	}
	if !created {
		t.Fatal("no admin was created on an empty database")
	}

	user, err := s.UserByEmail(ctx, "ada@example.test")
	if err != nil {
		t.Fatalf("UserByEmail(): %v", err)
	}
	if user.Status != store.StatusActive {
		t.Errorf("status = %q, want %q: an account that cannot log in is the bug this closes", user.Status, store.StatusActive)
	}
	if user.Role != store.RoleAdmin {
		t.Errorf("role = %q, want admin", user.Role)
	}
	if user.PasswordHash == nil || *user.PasswordHash != hash {
		t.Error("the password hash was not stored")
	}
}

// Safe to leave set forever: it must never resurrect an account somebody
// disabled on purpose, nor reset a password that has since been changed.
func TestBootstrapPasswordDoesNothingOnceAnAdminExists(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	first, err := auth.Hash("correct horse battery staple")
	if err != nil {
		t.Fatalf("Hash(): %v", err)
	}
	if _, err := s.EnsureBootstrapAdmin(ctx, "ada@example.test", first); err != nil {
		t.Fatalf("EnsureBootstrapAdmin(): %v", err)
	}

	second, err := auth.Hash("a completely different password")
	if err != nil {
		t.Fatalf("Hash(): %v", err)
	}
	created, err := s.EnsureBootstrapAdmin(ctx, "grace@example.test", second)
	if err != nil {
		t.Fatalf("EnsureBootstrapAdmin(): %v", err)
	}
	if created {
		t.Fatal("a second admin was created")
	}
	if _, err := s.UserByEmail(ctx, "grace@example.test"); !errors.Is(err, store.ErrNotFound) {
		t.Error("the second address got an account")
	}

	user, err := s.UserByEmail(ctx, "ada@example.test")
	if err != nil {
		t.Fatalf("UserByEmail(): %v", err)
	}
	if user.PasswordHash == nil || *user.PasswordHash != first {
		t.Error("the existing admin's password was overwritten by a later start-up")
	}
}

// With no password the behaviour is unchanged: invited, no way in but a link.
func TestBootstrapAdminWithoutAPasswordStaysInvited(t *testing.T) {
	s := newStore(t)
	ctx := t.Context()

	if _, err := s.EnsureBootstrapAdmin(ctx, "ada@example.test", ""); err != nil {
		t.Fatalf("EnsureBootstrapAdmin(): %v", err)
	}
	user, err := s.UserByEmail(ctx, "ada@example.test")
	if err != nil {
		t.Fatalf("UserByEmail(): %v", err)
	}
	if user.Status != store.StatusInvited {
		t.Errorf("status = %q, want %q", user.Status, store.StatusInvited)
	}
	if user.PasswordHash != nil {
		t.Error("a password was set when none was supplied")
	}
}
