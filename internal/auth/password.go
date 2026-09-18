// Package auth hashes passwords and mints the one-time secrets that stand in
// for them.
//
// Nothing here is invented: the hashing is golang.org/x/crypto/argon2, the
// randomness is crypto/rand, the comparison is crypto/subtle. The package's
// actual job is the bookkeeping around those three — encoding the cost
// parameters alongside every hash so they can be raised later, and noticing on
// the way past that a stored hash was made with weaker ones.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

// Params are the Argon2id cost parameters.
//
// Memory is in KiB, which is the unit x/crypto/argon2 takes and the unit the
// encoded form carries, so converting on the way in and out would only create
// an opportunity to get it wrong.
type Params struct {
	Memory  uint32 // KiB
	Time    uint32 // iterations
	Threads uint8  // degree of parallelism
	KeyLen  uint32 // bytes of output
	SaltLen uint32 // bytes of salt
}

// DefaultParams is the current policy: the OWASP floor for Argon2id.
//
// Memory is the parameter that matters. Iterations cost an attacker the same
// multiple they cost us, but 19 MiB per guess is what makes a GPU — which has
// thousands of cores and nothing like thousands of times the memory bandwidth
// — stop being an advantage. Raising these later is safe: the numbers live in
// every encoded hash, so old passwords keep verifying and are re-hashed the
// next time their owner logs in.
var DefaultParams = Params{
	Memory:  19 * 1024, // 19 MiB
	Time:    2,
	Threads: 1,
	KeyLen:  32,
	SaltLen: 16,
}

// ErrInvalidHash reports a stored hash this package cannot read: not Argon2id,
// not this version, or structurally damaged. It is deliberately distinct from
// "the password did not match", because the two want different responses — one
// is a wrong guess, the other is a corrupted row.
var ErrInvalidHash = errors.New("auth: malformed password hash")

// MaxConcurrentHashes bounds how many Argon2 evaluations run at once.
//
// Memory is the point of Argon2, and it is this process's memory as well as
// the attacker's. Every login costs one evaluation — deliberately including a
// login for an address with no account, so that timing says nothing about who
// has one — which makes the cost something an anonymous caller can ask for,
// twenty at a time under the per-address limit. Unbounded, that is twenty
// 19 MiB blocks at once: measured at 415 MiB in a bare process, against a
// container that is rightly given a fraction of that. Seven would do it.
//
// So they queue. Four at a time is 76 MiB, each takes a few tens of
// milliseconds, and a burst of twenty drains in well under a second. The wait
// falls on every caller alike, whether the account exists or not, so it
// discloses nothing the hashing itself was arranged not to.
const MaxConcurrentHashes = 4

var hashSlots = make(chan struct{}, MaxConcurrentHashes)

// idKey is argon2.IDKey behind the bound above, and the only place this
// package calls it. A second call site added beside it would be a second way
// to spend the memory, outside the limit.
func idKey(password string, salt []byte, time, memory uint32, threads uint8, keyLen uint32) []byte {
	hashSlots <- struct{}{}
	defer func() { <-hashSlots }()
	return argon2.IDKey([]byte(password), salt, time, memory, threads, keyLen)
}

// Hash hashes password with the default policy.
func Hash(password string) (string, error) { return DefaultParams.Hash(password) }

// Hash hashes password with p, drawing a fresh salt for every call.
//
// The salt is never reused and never derived from the password or the account,
// so two people who choose the same password get different hashes and one
// precomputed table buys an attacker nothing.
func (p Params) Hash(password string) (string, error) {
	salt := make([]byte, p.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: read salt: %w", err)
	}
	key := idKey(password, salt, p.Time, p.Memory, p.Threads, p.KeyLen)
	return p.encode(salt, key), nil
}

// Verify checks password against encoded using the default policy.
func Verify(encoded, password string) (ok, rehash bool, err error) {
	return DefaultParams.Verify(encoded, password)
}

// Verify reports whether password produces encoded, and whether encoded was
// made with parameters weaker than p.
//
// rehash is only meaningful when ok is true: it says "this password is
// correct, and the hash you have stored for it is below current policy", which
// is the one moment the plaintext is in hand and a stronger hash can be
// written without asking anybody to do anything.
func (p Params) Verify(encoded, password string) (ok, rehash bool, err error) {
	stored, salt, want, err := decode(encoded)
	if err != nil {
		return false, false, err
	}

	// The output length comes from the stored hash rather than from policy, so
	// raising KeyLen does not make every existing password fail to verify.
	got := idKey(password, salt, stored.Time, stored.Memory, stored.Threads, uint32(len(want)))

	// Constant-time: a byte-by-byte comparison leaks how much of a guess was
	// right through how long the answer took.
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return false, false, nil
	}
	return true, stored.weakerThan(p), nil
}

// weakerThan reports whether p is below o on any axis that matters.
func (p Params) weakerThan(o Params) bool {
	return p.Memory < o.Memory ||
		p.Time < o.Time ||
		p.KeyLen < o.KeyLen ||
		p.SaltLen < o.SaltLen
}

// b64 is the encoding the Argon2 reference implementation uses in its string
// form: standard alphabet, no padding.
var b64 = base64.RawStdEncoding

// encode writes the standard PHC string form:
//
//	$argon2id$v=19$m=19456,t=2,p=1$<salt>$<hash>
//
// The parameters travel with the hash on purpose. A deployment that stores
// only the digest can never raise its cost factors, because it has no way to
// tell an old hash from a new one.
func (p Params) encode(salt, key []byte) string {
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Time, p.Threads,
		b64.EncodeToString(salt), b64.EncodeToString(key))
}

// decode parses the PHC string form back into parameters, salt and digest.
func decode(encoded string) (Params, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return Params{}, nil, nil, ErrInvalidHash
	}

	var version int
	if n, err := fmt.Sscanf(parts[2], "v=%d", &version); n != 1 || err != nil {
		return Params{}, nil, nil, ErrInvalidHash
	}
	// Refuse a version this build's argon2 does not implement rather than
	// hashing with the wrong one and reporting a perfectly good password wrong.
	if version != argon2.Version {
		return Params{}, nil, nil, fmt.Errorf("%w: argon2 version %d", ErrInvalidHash, version)
	}

	var p Params
	if n, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Time, &p.Threads); n != 3 || err != nil {
		return Params{}, nil, nil, ErrInvalidHash
	}

	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return Params{}, nil, nil, ErrInvalidHash
	}
	key, err := b64.DecodeString(parts[5])
	if err != nil {
		return Params{}, nil, nil, ErrInvalidHash
	}

	// argon2.IDKey panics on a zero memory, time or threads, so these are
	// rejected here rather than reached.
	if p.Memory == 0 || p.Time == 0 || p.Threads == 0 || len(salt) == 0 || len(key) == 0 {
		return Params{}, nil, nil, ErrInvalidHash
	}
	p.SaltLen = uint32(len(salt))
	p.KeyLen = uint32(len(key))

	return p, salt, key, nil
}

// dummy is an encoded hash of a random string nobody will ever type.
var dummy = sync.OnceValue(func() string {
	h, err := Hash(rand.Text())
	if err != nil {
		// Hash only fails if the system CSPRNG does, which Go already treats
		// as fatal. Falling back to a fixed encoding keeps the cost of a login
		// identical, which is the only thing this value is for.
		return DefaultParams.encode(make([]byte, DefaultParams.SaltLen), make([]byte, DefaultParams.KeyLen))
	}
	return h
})

// DummyHash returns an encoded hash that no password matches.
//
// Verifying against it is how a login against an address that does not exist —
// or an account that has not set a password yet — costs the same as a login
// against one that does. Without it the response time answers "is this person
// a user here?", which is the question the account-creation design goes out of
// its way not to answer.
//
// The value is computed once, lazily, and kept for the life of the process.
func DummyHash() string { return dummy() }
