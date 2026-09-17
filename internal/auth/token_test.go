package auth

import (
	"encoding/base64"
	"testing"
)

func TestNewTokenIsUnpredictable(t *testing.T) {
	const n = 1000

	seen := make(map[string]bool, n)
	for range n {
		tok, err := NewToken()
		if err != nil {
			t.Fatalf("NewToken(): %v", err)
		}

		raw, err := base64.RawURLEncoding.DecodeString(tok)
		if err != nil {
			t.Fatalf("NewToken() = %q, which is not URL-safe base64: %v", tok, err)
		}
		// The design asks for at least 128 bits. Anything shorter is
		// guessable at the rate the network allows, which is the whole
		// question for a link that sets a password.
		if len(raw) != TokenBytes || len(raw)*8 < 128 {
			t.Fatalf("token is %d bytes, want %d (at least 16)", len(raw), TokenBytes)
		}
		if seen[tok] {
			t.Fatalf("NewToken() repeated a value within %d draws", n)
		}
		seen[tok] = true
	}
}

func TestHashTokenIsDeterministicAndOpaque(t *testing.T) {
	tok, err := NewToken()
	if err != nil {
		t.Fatalf("NewToken(): %v", err)
	}

	h := HashToken(tok)
	// Determinism is a requirement, not an accident: the database lookup is by
	// hash, so a salted or randomised digest could never be found again.
	if string(h) != string(HashToken(tok)) {
		t.Fatal("HashToken() is not deterministic, so a stored token could never be looked up")
	}
	if len(h) != 32 {
		t.Fatalf("HashToken() returned %d bytes, want 32", len(h))
	}
	if string(h) == tok {
		t.Fatal("HashToken() returned the token itself")
	}
	if string(HashToken(tok)) == string(HashToken(tok+"x")) {
		t.Fatal("HashToken() collided on two different tokens")
	}

	// Mutating the returned slice must not reach back into the hash state.
	h[0] ^= 0xff
	if string(h) == string(HashToken(tok)) {
		t.Fatal("HashToken() returns a slice that aliases something shared")
	}
}
