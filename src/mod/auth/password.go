package auth

/*
	password.go: password hashing.

	New and changed passwords are stored as bcrypt hashes. Older versions
	stored an unsalted SHA-512 hex digest; those are still accepted, and
	callers that can write the user record replace them with bcrypt after a
	successful check (see PasswordMatches' upgrade result).
*/

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

// PasswordMatches checks raw against a stored hash. upgrade is true when the
// password matched an old-style hash that should be replaced with
// HashPassword(raw).
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

// legacyPasswordDigest is the unsalted SHA-512 digest used before 2.2.2. It
// is only computed to check passwords stored that way.
func legacyPasswordDigest(raw string) string {
	h := sha512.Sum512([]byte(raw))
	return hex.EncodeToString(h[:])
}

/*
	Per-request checks (HTTP basic auth on proxy rules, WebDAV) run bcrypt
	once per credential and then remember the result for a few minutes, so
	browsers re-sending credentials on every request stay fast.
*/

const verifiedPasswordTTL = 5 * time.Minute
const maxVerifiedPasswords = 1024

type verifiedPassword struct {
	raw     string
	expires time.Time
}

var verifiedPasswords sync.Map // stored hash -> verifiedPassword
var verifiedPasswordCount int64
var verifiedPasswordMu sync.Mutex

// PasswordMatchesCached is PasswordMatches for checks repeated on every
// request. It does not report upgrades.
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
