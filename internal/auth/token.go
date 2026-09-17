package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// TokenBytes is the size of a set-password link's secret and of a session
// cookie's value: 32 bytes, 256 bits. The design asks for at least 128; going
// to 256 costs nothing at all and removes the need to ever think about it
// again.
const TokenBytes = 32

// NewToken mints a URL-safe secret from the system CSPRNG.
//
// math/rand would produce something that looks identical and is guessable from
// a handful of samples, which is exactly the kind of mistake that survives
// review because the output looks the same.
func NewToken() (string, error) {
	b := make([]byte, TokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: read token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashToken returns the SHA-256 of a token, which is what the database stores.
//
// Deliberately a fast hash, unlike a password. A token is 256 bits of CSPRNG
// output, so there is no dictionary to run against it and no cost factor that
// would make guessing meaningfully harder than the 2^256 it already is. What
// storing the hash does buy is that a leaked database — a backup, a replica, a
// log of a query — contains nothing that can be replayed as a link. It also
// has to be deterministic, because the lookup is by hash.
func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
