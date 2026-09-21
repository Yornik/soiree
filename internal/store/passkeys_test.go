package store_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Yornik/soiree/internal/store"
)

// Ada, Grace and Linus at example.test, as everywhere else here. The bytes
// standing in for credential ids and public keys are equally synthetic: this
// layer never looks inside either, which is exactly what these tests assert by
// getting away with it.

func seedAccount(t *testing.T, s *store.Store, email string) store.User {
	t.Helper()
	u, err := s.CreateUser(t.Context(), store.User{
		Email: email, Role: store.RoleEditor, Status: store.StatusActive,
	})
	if err != nil {
		t.Fatalf("create %s: %v", email, err)
	}
	return u
}

func seedPasskey(t *testing.T, s *store.Store, userID uuid.UUID, credID string, label string) store.PasskeyCredential {
	t.Helper()
	row, err := s.CreatePasskeyCredential(t.Context(), store.PasskeyCredential{
		UserID:         userID,
		CredentialID:   []byte(credID),
		PublicKey:      []byte("cose-public-key-" + credID),
		AAGUID:         []byte("0123456789abcdef"),
		Transports:     []string{"internal", "hybrid"},
		BackupEligible: true,
		BackupState:    true,
		UserPresent:    true,
		UserVerified:   true,
		Label:          label,
	})
	if err != nil {
		t.Fatalf("create passkey %s: %v", credID, err)
	}
	return row
}

func TestPasskeyCredentialRoundTrip(t *testing.T) {
	s := newStore(t)
	ada := seedAccount(t, s, "ada@example.test")

	row := seedPasskey(t, s, ada.ID, "cred-phone", "Ada's phone")
	if row.ID == uuid.Nil || row.CreatedAt.IsZero() {
		t.Fatalf("created row = %+v, want an id and a creation time", row)
	}
	if row.LastUsedAt != nil {
		t.Errorf("LastUsedAt = %v, want nil before first use", row.LastUsedAt)
	}
	if got := row.Transports; len(got) != 2 || got[0] != "internal" || got[1] != "hybrid" {
		t.Errorf("Transports = %v, want the two that went in", got)
	}

	back, err := s.PasskeyCredentialByCredentialID(t.Context(), []byte("cred-phone"))
	if err != nil {
		t.Fatalf("by credential id: %v", err)
	}
	if back.ID != row.ID || back.UserID != ada.ID {
		t.Errorf("lookup returned %+v, want the row just written", back)
	}
	if string(back.PublicKey) != "cose-public-key-cred-phone" {
		t.Errorf("PublicKey = %q, want it back unchanged", back.PublicKey)
	}
}

// A person has several: a phone and a laptop are separate key pairs, and the
// list is what makes removing one of them possible.
func TestPasskeyCredentialsAreListedPerAccount(t *testing.T) {
	s := newStore(t)
	ada := seedAccount(t, s, "ada@example.test")
	grace := seedAccount(t, s, "grace@example.test")

	seedPasskey(t, s, ada.ID, "ada-phone", "phone")
	seedPasskey(t, s, ada.ID, "ada-laptop", "laptop")
	seedPasskey(t, s, grace.ID, "grace-key", "yubikey")

	adas, err := s.PasskeyCredentials(t.Context(), ada.ID)
	if err != nil {
		t.Fatalf("list ada: %v", err)
	}
	if len(adas) != 2 {
		t.Fatalf("ada has %d passkeys, want 2", len(adas))
	}
	for _, row := range adas {
		if row.UserID != ada.ID {
			t.Errorf("ada's list carries a credential owned by %s", row.UserID)
		}
	}

	graces, err := s.PasskeyCredentials(t.Context(), grace.ID)
	if err != nil {
		t.Fatalf("list grace: %v", err)
	}
	if len(graces) != 1 || string(graces[0].CredentialID) != "grace-key" {
		t.Fatalf("grace's list = %+v, want only her own", graces)
	}
}

// A credential belongs to exactly one account, and the constraint is what makes
// that true rather than hoped for: with two owners a discoverable login has two
// answers for whose it is.
func TestPasskeyCredentialIDIsUniqueAcrossAccounts(t *testing.T) {
	s := newStore(t)
	ada := seedAccount(t, s, "ada@example.test")
	grace := seedAccount(t, s, "grace@example.test")

	seedPasskey(t, s, ada.ID, "shared-id", "phone")

	_, err := s.CreatePasskeyCredential(t.Context(), store.PasskeyCredential{
		UserID:       grace.ID,
		CredentialID: []byte("shared-id"),
		PublicKey:    []byte("another-key"),
	})
	if err == nil {
		t.Fatal("a credential id registered to Ada was accepted for Grace as well")
	}
	if !store.IsUniqueViolation(err) {
		t.Fatalf("second registration failed with %v, want a unique violation", err)
	}
}

// Deleting is scoped by account in the statement, so somebody else's credential
// is refused — and refused with the same answer as one that never existed.
func TestPasskeyCredentialCannotBeDeletedByAnotherAccount(t *testing.T) {
	s := newStore(t)
	ada := seedAccount(t, s, "ada@example.test")
	grace := seedAccount(t, s, "grace@example.test")

	row := seedPasskey(t, s, ada.ID, "ada-phone", "phone")

	err := s.DeletePasskeyCredential(t.Context(), grace.ID, row.ID)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Grace deleting Ada's passkey: %v, want ErrNotFound", err)
	}
	// And it is still there.
	if _, err := s.PasskeyCredentialByCredentialID(t.Context(), []byte("ada-phone")); err != nil {
		t.Fatalf("Ada's passkey went anyway: %v", err)
	}

	// An id that never existed is the same answer, so the refusal says nothing
	// about whose it was.
	if err := s.DeletePasskeyCredential(t.Context(), grace.ID, uuid.New()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleting an unknown id: %v, want ErrNotFound", err)
	}

	if err := s.DeletePasskeyCredential(t.Context(), ada.ID, row.ID); err != nil {
		t.Fatalf("Ada deleting her own passkey: %v", err)
	}
	if _, err := s.PasskeyCredentialByCredentialID(t.Context(), []byte("ada-phone")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("passkey survived its owner deleting it: %v", err)
	}
}

// A credential is a way into an account, so adding one changes how that
// account can be signed in to. The screen somebody reads to find out what has
// happened to an account is the activity, and what it has to say here is that
// a credential appeared and on whose account, in the shape a revocation
// already uses.
func TestRegisteringAPasskeyIsRecorded(t *testing.T) {
	s := newStore(t)
	ada := seedAccount(t, s, "ada@example.test")

	seedPasskey(t, s, ada.ID, "ada-phone", "Ada's phone")

	entries := history(t, s, store.EntityUsers, ada.ID)
	if len(entries) != 2 {
		t.Fatalf("history has %d entries, want the create and the registration", len(entries))
	}
	entry := entries[0]
	if entry.Action != store.ChangeUpdate {
		t.Errorf("action = %q, want update", entry.Action)
	}
	if entry.ActorID == nil || *entry.ActorID != ada.ID {
		t.Errorf("actor = %v, want the account the credential was added to", entry.ActorID)
	}
	if entry.Revision != nil {
		t.Errorf("revision = %d, want none: no column of the account moved", *entry.Revision)
	}
	added, recorded := entry.Changes["credentials"]
	if !recorded {
		t.Fatalf("the entry does not say what was done: %v", entry.Changes)
	}
	if asString(t, added.New) != "added" {
		t.Errorf("credentials = %s, want added", added.New)
	}
	// One field and no more, as for a revocation: there is no admin view of
	// anybody's passkeys, and an entry naming the device would be one.
	if len(entry.Changes) != 1 {
		t.Errorf("the entry records %d fields, want the one: %v", len(entry.Changes), entry.Changes)
	}
	// Neither the label its owner chose nor the identifier the authenticator
	// reported, both of which carry "phone" here.
	blob, err := json.Marshal(entry.Changes)
	if err != nil {
		t.Fatalf("marshal the recorded change: %v", err)
	}
	if strings.Contains(string(blob), "phone") {
		t.Errorf("the entry describes the credential: %s", blob)
	}
}

// The other side of the same question, and the one somebody asks after a
// credential goes missing from their account.
func TestRemovingAPasskeyIsRecorded(t *testing.T) {
	s := newStore(t)
	ada := seedAccount(t, s, "ada@example.test")
	grace := seedAccount(t, s, "grace@example.test")
	row := seedPasskey(t, s, ada.ID, "ada-phone", "Ada's phone")

	// A refused deletion is not a change to anybody: an entry for it would say
	// a credential left an account that still has it.
	if err := s.DeletePasskeyCredential(t.Context(), grace.ID, row.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Grace deleting Ada's passkey: %v, want ErrNotFound", err)
	}
	if entries := history(t, s, store.EntityUsers, grace.ID); len(entries) != 1 {
		t.Fatalf("Grace's history has %d entries, want her create alone", len(entries))
	}
	if entries := history(t, s, store.EntityUsers, ada.ID); len(entries) != 2 {
		t.Fatalf("Ada's history has %d entries, want the create and the registration", len(entries))
	}

	if err := s.DeletePasskeyCredential(t.Context(), ada.ID, row.ID); err != nil {
		t.Fatalf("Ada deleting her own passkey: %v", err)
	}

	entries := history(t, s, store.EntityUsers, ada.ID)
	if len(entries) != 3 {
		t.Fatalf("history has %d entries, want the create, the registration and the removal", len(entries))
	}
	entry := entries[0]
	if entry.Action != store.ChangeUpdate {
		t.Errorf("action = %q, want update", entry.Action)
	}
	if entry.ActorID == nil || *entry.ActorID != ada.ID {
		t.Errorf("actor = %v, want the account the credential was removed from", entry.ActorID)
	}
	removed, recorded := entry.Changes["credentials"]
	if !recorded {
		t.Fatalf("the entry does not say what was done: %v", entry.Changes)
	}
	if asString(t, removed.New) != "removed" {
		t.Errorf("credentials = %s, want removed", removed.New)
	}
	if len(entry.Changes) != 1 {
		t.Errorf("the entry records %d fields, want the one: %v", len(entry.Changes), entry.Changes)
	}
}

func TestPasskeyCredentialsGoWithTheirAccount(t *testing.T) {
	s := newStore(t)
	ada := seedAccount(t, s, "ada@example.test")
	seedPasskey(t, s, ada.ID, "ada-phone", "phone")

	current, err := s.User(t.Context(), ada.ID)
	if err != nil {
		t.Fatalf("read ada: %v", err)
	}
	if err := s.DeleteUser(t.Context(), ada.ID, current.Revision); err != nil {
		t.Fatalf("delete ada: %v", err)
	}

	if _, err := s.PasskeyCredentialByCredentialID(t.Context(), []byte("ada-phone")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a deleted account's passkey survived: %v", err)
	}
}

func TestTouchPasskeyCredentialRecordsUse(t *testing.T) {
	s := newStore(t)
	ada := seedAccount(t, s, "ada@example.test")
	row := seedPasskey(t, s, ada.ID, "ada-phone", "phone")

	if err := s.TouchPasskeyCredential(t.Context(), row.ID, 7, store.PasskeyFlags{
		BackupState: false, UserPresent: true, UserVerified: true,
	}); err != nil {
		t.Fatalf("touch: %v", err)
	}

	back, err := s.PasskeyCredentialByCredentialID(t.Context(), []byte("ada-phone"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if back.SignCount != 7 {
		t.Errorf("SignCount = %d, want 7", back.SignCount)
	}
	if back.LastUsedAt == nil {
		t.Error("LastUsedAt is still nil after a recorded use")
	}
	if back.BackupState {
		t.Error("BackupState was not updated; it changes over a credential's life")
	}
	// BackupEligible is fixed at registration and is never written back: an
	// assertion that disagrees with it is refused a layer up, and a column that
	// drifted would make that check unfalsifiable.
	if !back.BackupEligible {
		t.Error("BackupEligible changed, and it must not")
	}

	// Nothing to touch is an error rather than a silent no-op: it would mean
	// the handler verified an assertion against a row that is gone.
	if err := s.TouchPasskeyCredential(t.Context(), uuid.New(), 1, store.PasskeyFlags{}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("touching an unknown credential: %v, want ErrNotFound", err)
	}
}

// The counter is a uint32 on the wire and a bigint in the column. The CHECK is
// what keeps the two from disagreeing about what fits.
func TestPasskeySignCountIsBoundedToAUint32(t *testing.T) {
	s := newStore(t)
	ada := seedAccount(t, s, "ada@example.test")
	row := seedPasskey(t, s, ada.ID, "ada-phone", "phone")

	if err := s.TouchPasskeyCredential(t.Context(), row.ID, store.MaxSignCount, store.PasskeyFlags{}); err != nil {
		t.Fatalf("the largest counter a uint32 holds was refused: %v", err)
	}
	if err := s.TouchPasskeyCredential(t.Context(), row.ID, store.MaxSignCount+1, store.PasskeyFlags{}); err == nil {
		t.Fatal("a counter larger than a uint32 was accepted")
	}
}

// --- challenges -------------------------------------------------------------

func newChallenge(t *testing.T, s *store.Store, value string, ceremony store.PasskeyCeremony, userID *uuid.UUID, ttl time.Duration) store.PasskeyChallenge {
	t.Helper()
	ch, err := s.CreatePasskeyChallenge(t.Context(), store.PasskeyChallenge{
		Challenge:   value,
		Ceremony:    ceremony,
		UserID:      userID,
		SessionData: []byte(`{"challenge":"` + value + `"}`),
		ExpiresAt:   time.Now().Add(ttl),
	})
	if err != nil {
		t.Fatalf("create challenge %s: %v", value, err)
	}
	return ch
}

// The single most important property in the whole feature. A challenge that can
// be presented twice is an assertion that can be replayed, and a replayable
// assertion proves nothing at all.
func TestPasskeyChallengeIsSingleUse(t *testing.T) {
	s := newStore(t)
	newChallenge(t, s, "challenge-one", store.PasskeyCeremonyLogin, nil, time.Minute)

	first, err := s.ConsumePasskeyChallenge(t.Context(), "challenge-one")
	if err != nil {
		t.Fatalf("first redemption: %v", err)
	}
	if first.Ceremony != store.PasskeyCeremonyLogin || first.UserID != nil {
		t.Errorf("redeemed %+v, want a login challenge bound to nobody", first)
	}
	if string(first.SessionData) == "" {
		t.Error("the ceremony's session data did not come back")
	}

	if _, err := s.ConsumePasskeyChallenge(t.Context(), "challenge-one"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second redemption: %v, want ErrNotFound", err)
	}
}

func TestPasskeyChallengeExpires(t *testing.T) {
	s := newStore(t)
	// Already past. The clock that decides is the database's, which is the one
	// every replica agrees on.
	newChallenge(t, s, "challenge-stale", store.PasskeyCeremonyLogin, nil, -time.Second)

	if _, err := s.ConsumePasskeyChallenge(t.Context(), "challenge-stale"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("redeeming an expired challenge: %v, want ErrNotFound", err)
	}
}

func TestUnknownPasskeyChallengeIsRefused(t *testing.T) {
	s := newStore(t)
	for _, value := range []string{"", "never-issued"} {
		if _, err := s.ConsumePasskeyChallenge(t.Context(), value); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("redeeming %q: %v, want ErrNotFound", value, err)
		}
	}
}

// Beginning a ceremony clears out the ones nobody finished, so a table a public
// endpoint writes to does not grow without bound.
func TestCreatingAPasskeyChallengeSweepsTheExpiredOnes(t *testing.T) {
	s := newStore(t)
	newChallenge(t, s, "challenge-stale", store.PasskeyCeremonyLogin, nil, -time.Minute)
	newChallenge(t, s, "challenge-live", store.PasskeyCeremonyLogin, nil, time.Minute)

	// The second insert above already swept. Prove it by showing the sweeper
	// has nothing left to do, and that the live one was not caught in it.
	n, err := s.DeleteExpiredPasskeyChallenges(t.Context())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 0 {
		t.Errorf("the insert left %d expired challenges behind", n)
	}
	if _, err := s.ConsumePasskeyChallenge(t.Context(), "challenge-live"); err != nil {
		t.Fatalf("the live challenge was swept too: %v", err)
	}
}

func TestPasskeyChallengeRecordsWhoBegunARegistration(t *testing.T) {
	s := newStore(t)
	ada := seedAccount(t, s, "ada@example.test")
	newChallenge(t, s, "challenge-register", store.PasskeyCeremonyRegister, &ada.ID, time.Minute)

	ch, err := s.ConsumePasskeyChallenge(t.Context(), "challenge-register")
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if ch.Ceremony != store.PasskeyCeremonyRegister {
		t.Errorf("Ceremony = %q, want register", ch.Ceremony)
	}
	if ch.UserID == nil || *ch.UserID != ada.ID {
		t.Fatalf("UserID = %v, want Ada's id — a registration belongs to whoever began it", ch.UserID)
	}
}

func TestPasskeyChallengeCeremonyIsConstrained(t *testing.T) {
	s := newStore(t)
	_, err := s.CreatePasskeyChallenge(t.Context(), store.PasskeyChallenge{
		Challenge:   "challenge-nonsense",
		Ceremony:    store.PasskeyCeremony("something-else"),
		SessionData: []byte(`{}`),
		ExpiresAt:   time.Now().Add(time.Minute),
	})
	if err == nil {
		t.Fatal("a challenge for no known ceremony was stored")
	}
}
