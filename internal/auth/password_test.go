package auth

import (
	"regexp"
	"strings"
	"testing"
)

// cheap is what most of these hash with. Argon2id at policy is 19 MiB and two
// passes per call by design; multiplied by the number of assertions here, and
// again by the race detector's shadow memory in CI, that turns a fast test
// package into a slow one. The expensive parameters are exercised once, in
// TestRoundTripAtPolicy, which is where they matter.
var cheap = Params{Memory: 1024, Time: 1, Threads: 1, KeyLen: 32, SaltLen: 16}

func TestRoundTripAtPolicy(t *testing.T) {
	const password = "correct horse battery staple"

	encoded, err := Hash(password)
	if err != nil {
		t.Fatalf("Hash(): %v", err)
	}

	// The parameters have to be legible in the stored string, or they can
	// never be raised: there would be no way to tell an old hash from a new
	// one. 19456 KiB is the OWASP floor of 19 MiB.
	want := regexp.MustCompile(`^\$argon2id\$v=19\$m=19456,t=2,p=1\$[A-Za-z0-9+/]{22}\$[A-Za-z0-9+/]{43}$`)
	if !want.MatchString(encoded) {
		t.Fatalf("encoded hash = %q, want the standard argon2id form at policy parameters", encoded)
	}

	ok, rehash, err := Verify(encoded, password)
	if err != nil {
		t.Fatalf("Verify(): %v", err)
	}
	if !ok {
		t.Error("a password did not verify against its own hash")
	}
	if rehash {
		t.Error("a hash made at current policy was reported as needing a re-hash")
	}
}

func TestWrongPasswordIsRejected(t *testing.T) {
	encoded, err := cheap.Hash("correct horse battery staple")
	if err != nil {
		t.Fatalf("Hash(): %v", err)
	}

	for _, wrong := range []string{
		"",
		"correct horse battery stapl",
		"correct horse battery staple ",
		"Correct horse battery staple",
		"correct horse battery staplf",
	} {
		ok, _, err := cheap.Verify(encoded, wrong)
		if err != nil {
			t.Fatalf("Verify(%q): %v", wrong, err)
		}
		if ok {
			t.Errorf("Verify(%q) accepted the wrong password", wrong)
		}
	}
}

func TestSaltIsPerPassword(t *testing.T) {
	const password = "the same password twice"

	a, err := cheap.Hash(password)
	if err != nil {
		t.Fatalf("Hash(): %v", err)
	}
	b, err := cheap.Hash(password)
	if err != nil {
		t.Fatalf("Hash(): %v", err)
	}

	// Two people who choose the same password must not share a stored value.
	// If they do, one precomputed table breaks both of them at once.
	if a == b {
		t.Fatal("hashing the same password twice produced the same string, so the salt is not per-password")
	}
	for _, encoded := range []string{a, b} {
		ok, _, err := cheap.Verify(encoded, password)
		if err != nil || !ok {
			t.Fatalf("Verify() = %v, %v; both hashes must verify", ok, err)
		}
	}
}

func TestParamsUpgradeTriggersRehash(t *testing.T) {
	const password = "hashed under the old policy"

	// An account hashed before the parameters were raised.
	old, err := cheap.Hash(password)
	if err != nil {
		t.Fatalf("Hash(): %v", err)
	}

	// The right password still verifies — raising the floor must not lock
	// anybody out — and comes with the flag that says to re-encode it.
	ok, rehash, err := DefaultParams.Verify(old, password)
	if err != nil {
		t.Fatalf("Verify(): %v", err)
	}
	if !ok {
		t.Fatal("a password hashed at weaker parameters no longer verifies")
	}
	if !rehash {
		t.Fatal("a hash below current policy was not flagged for re-hashing")
	}

	// The wrong password must not be flagged for anything, or a failed login
	// becomes a way to make the server do extra work.
	if ok, rehash, err := DefaultParams.Verify(old, "not it"); err != nil || ok || rehash {
		t.Fatalf("Verify(wrong) = %v, %v, %v; want false, false, nil", ok, rehash, err)
	}

	// Each axis on its own, so a future edit cannot drop one of the
	// comparisons without a test noticing.
	for _, tc := range []struct {
		name   string
		stored Params
		want   bool
	}{
		{"less memory", Params{Memory: 1024, Time: 2, Threads: 1, KeyLen: 32, SaltLen: 16}, true},
		{"fewer iterations", Params{Memory: 19 * 1024, Time: 1, Threads: 1, KeyLen: 32, SaltLen: 16}, true},
		{"shorter output", Params{Memory: 19 * 1024, Time: 2, Threads: 1, KeyLen: 16, SaltLen: 16}, true},
		{"shorter salt", Params{Memory: 19 * 1024, Time: 2, Threads: 1, KeyLen: 32, SaltLen: 8}, true},
		{"at policy", DefaultParams, false},
		{"above policy", Params{Memory: 64 * 1024, Time: 4, Threads: 1, KeyLen: 32, SaltLen: 16}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.stored.weakerThan(DefaultParams); got != tc.want {
				t.Errorf("weakerThan() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestVerifyRejectsMalformedHashes(t *testing.T) {
	good, err := cheap.Hash("something")
	if err != nil {
		t.Fatalf("Hash(): %v", err)
	}

	for name, encoded := range map[string]string{
		"empty":              "",
		"not a hash":         "hunter2",
		"bcrypt":             "$2y$10$abcdefghijklmnopqrstuv",
		"wrong algorithm":    strings.Replace(good, "argon2id", "argon2i", 1),
		"unknown version":    strings.Replace(good, "v=19", "v=16", 1),
		"missing parameters": "$argon2id$v=19$$c2FsdA$aGFzaA",
		"zero memory":        strings.Replace(good, "m=1024", "m=0", 1),
		"zero iterations":    strings.Replace(good, "t=1", "t=0", 1),
		"bad base64 salt":    "$argon2id$v=19$m=1024,t=1,p=1$not!base64$aGFzaA",
		"truncated":          "$argon2id$v=19$m=1024,t=1,p=1$c2FsdA",
	} {
		t.Run(name, func(t *testing.T) {
			ok, rehash, err := Verify(encoded, "something")
			if err == nil {
				t.Fatalf("Verify(%q) returned no error", encoded)
			}
			// A hash the server cannot read is never a successful login. The
			// tempting failure mode is to treat a parse error as "no hash
			// stored" and wave the request through.
			if ok || rehash {
				t.Errorf("Verify(%q) = %v, %v; a damaged hash must never authenticate", encoded, ok, rehash)
			}
		})
	}
}

func TestDummyHashMatchesNothing(t *testing.T) {
	d := DummyHash()

	if d != DummyHash() {
		t.Error("DummyHash() is not stable, so the cost of a login against an unknown address would vary")
	}
	// It has to be a real, parseable hash at policy parameters: the entire
	// point is that verifying against it costs exactly what verifying against
	// a real account costs.
	if !strings.HasPrefix(d, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Errorf("DummyHash() = %q, want an argon2id hash at policy parameters", d)
	}
	for _, guess := range []string{"", "password", "admin"} {
		ok, _, err := Verify(d, guess)
		if err != nil {
			t.Fatalf("Verify(dummy, %q): %v", guess, err)
		}
		if ok {
			t.Errorf("Verify(dummy, %q) succeeded", guess)
		}
	}
}
