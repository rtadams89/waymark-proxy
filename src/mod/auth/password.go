package auth

// Password hashing with bcrypt; unsalted SHA-512 digests are also accepted.

import (
	"crypto/sha512"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// HashPassword returns a bcrypt hash of raw.
func HashPassword(raw string) (string, error) {
	if len(raw) > 72 {
		return "", errors.New("passwords can be at most 72 bytes long")
	}
	b, err := bcrypt.GenerateFromPassword([]byte(raw), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// IsBcryptHash reports whether stored is a bcrypt hash.
func IsBcryptHash(stored string) bool {
	return strings.HasPrefix(stored, "$2a$") || strings.HasPrefix(stored, "$2b$") || strings.HasPrefix(stored, "$2y$")
}

// PasswordMatches checks raw against a stored hash; upgrade is true when it
// matched an unsalted SHA-512 digest.
func PasswordMatches(stored, raw string) (ok bool, upgrade bool) {
	if stored == "" {
		return false, false
	}
	if IsBcryptHash(stored) {
		return bcrypt.CompareHashAndPassword([]byte(stored), []byte(raw)) == nil, false
	}
	if legacyPasswordDigest(raw) == strings.ToLower(stored) {
		return true, true
	}
	return false, false
}

// legacyPasswordDigest returns the unsalted SHA-512 hex digest of raw.
func legacyPasswordDigest(raw string) string {
	h := sha512.Sum512([]byte(raw))
	return hex.EncodeToString(h[:])
}

/* Verified passwords are remembered for verifiedPasswordTTL per stored hash */

const verifiedPasswordTTL = 5 * time.Minute
const maxVerifiedPasswords = 1024

type verifiedPassword struct {
	raw     string
	expires time.Time
}

var verifiedPasswords sync.Map // stored hash -> verifiedPassword
var verifiedPasswordCount int64
var verifiedPasswordMu sync.Mutex

// PasswordMatchesCached is PasswordMatches with verified results cached; it
// does not report upgrades.
func PasswordMatchesCached(stored, raw string) bool {
	if v, ok := verifiedPasswords.Load(stored); ok {
		vp := v.(verifiedPassword)
		if time.Now().Before(vp.expires) && subtle.ConstantTimeCompare([]byte(vp.raw), []byte(raw)) == 1 {
			return true
		}
	}
	ok, _ := PasswordMatches(stored, raw)
	if ok {
		verifiedPasswordMu.Lock()
		if verifiedPasswordCount >= maxVerifiedPasswords {
			verifiedPasswords.Range(func(k, _ any) bool { verifiedPasswords.Delete(k); return true })
			verifiedPasswordCount = 0
		}
		if _, loaded := verifiedPasswords.Swap(stored, verifiedPassword{raw: raw, expires: time.Now().Add(verifiedPasswordTTL)}); !loaded {
			verifiedPasswordCount++
		}
		verifiedPasswordMu.Unlock()
	}
	return ok
}
