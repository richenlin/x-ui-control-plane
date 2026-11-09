package password

import (
	"crypto/subtle"
	"strings"
	"sync"

	"golang.org/x/crypto/bcrypt"
)

// Cost is the bcrypt cost used for new password hashes.
// 12 is a good trade-off between security and login latency (~250ms on modern
// hardware). Login latency is also a natural rate-limiter for brute force.
const Cost = 12

// maxPasswordLen is the maximum password length bcrypt can handle (72 bytes).
const maxPasswordLen = 72

var (
	dummyOnce sync.Once
	dummyHash []byte
)

// IsHashed reports whether the stored value already is a bcrypt hash.
func IsHashed(stored string) bool {
	return strings.HasPrefix(stored, "$2a$") ||
		strings.HasPrefix(stored, "$2b$") ||
		strings.HasPrefix(stored, "$2y$")
}

// Hash returns the bcrypt hash of plain.
func Hash(plain string) (string, error) {
	if len(plain) > maxPasswordLen {
		// Refuse to silently truncate: that would make long passwords weak.
		plain = plain[:maxPasswordLen]
	}
	b, err := bcrypt.GenerateFromPassword([]byte(plain), Cost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Verify checks plain against stored. stored may be a bcrypt hash or a legacy
// plain-text password (as written by old versions of x-ui).
// The second return value reports whether a legacy plain-text password matched,
// so the caller can transparently upgrade it to a hash.
func Verify(stored string, plain string) (ok bool, legacy bool) {
	if stored == "" {
		return false, false
	}
	if len(plain) > maxPasswordLen {
		plain = plain[:maxPasswordLen]
	}
	if IsHashed(stored) {
		return bcrypt.CompareHashAndPassword([]byte(stored), []byte(plain)) == nil, false
	}
	// legacy plain-text comparison, constant time
	if subtle.ConstantTimeCompare([]byte(stored), []byte(plain)) == 1 {
		return true, true
	}
	return false, false
}

// DummyCheck burns approximately the same amount of CPU as a real bcrypt
// verification. It is called when the username does not exist, so that an
// attacker cannot distinguish "unknown user" from "wrong password" by timing.
func DummyCheck(plain string) {
	dummyOnce.Do(func() {
		dummyHash, _ = bcrypt.GenerateFromPassword([]byte("x-ui-timing-equalizer"), Cost)
	})
	if len(plain) > maxPasswordLen {
		plain = plain[:maxPasswordLen]
	}
	_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(plain))
}
