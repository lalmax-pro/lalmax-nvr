package auth

import (
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const passwordCacheTTL = 5 * time.Minute

type passwordCacheEntry struct {
	hash       string
	verifiedAt time.Time
}

var passwordCache sync.Map

// HashPassword hashes a password with bcrypt's default cost.
func HashPassword(password string) (string, error) {
	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hashed), nil
}

// CheckPassword compares a plaintext password with a bcrypt hash. Successful
// checks are cached briefly to avoid repeating the expensive bcrypt operation.
func CheckPassword(password, hash string) bool {
	if strings.TrimSpace(hash) == "" {
		return false
	}
	cacheKey := password + "\x00" + hash
	if value, ok := passwordCache.Load(cacheKey); ok {
		entry := value.(passwordCacheEntry)
		if entry.hash == hash && time.Since(entry.verifiedAt) < passwordCacheTTL {
			return true
		}
		passwordCache.Delete(cacheKey)
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return false
	}
	passwordCache.Store(cacheKey, passwordCacheEntry{hash: hash, verifiedAt: time.Now()})
	return true
}
