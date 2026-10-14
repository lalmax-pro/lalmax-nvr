package auth

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"sync"
	"time"
)

const (
	defaultDigestNonceTTL      = 5 * time.Minute
	defaultDigestNonceCapacity = 8192
)

type digestNonceEntry struct {
	expires  time.Time
	maxCount uint32
	consumed bool
}

// DigestNonceStore issues bounded-lifetime Digest nonces and tracks their use
// to reject expired or replayed authorization requests.
type DigestNonceStore struct {
	mu       sync.Mutex
	entries  map[string]digestNonceEntry
	ttl      time.Duration
	capacity int
}

// NewDigestNonceStore creates a nonce store. Non-positive values select the
// defaults of five minutes and 8192 outstanding nonces.
func NewDigestNonceStore(ttl time.Duration, capacity int) *DigestNonceStore {
	if ttl <= 0 {
		ttl = defaultDigestNonceTTL
	}
	if capacity <= 0 {
		capacity = defaultDigestNonceCapacity
	}
	return &DigestNonceStore{entries: make(map[string]digestNonceEntry), ttl: ttl, capacity: capacity}
}

// Issue creates a cryptographically random nonce and records its expiry.
func (s *DigestNonceStore) Issue() (string, error) {
	if s == nil {
		return "", fmt.Errorf("digest nonce store is nil")
	}
	var random [24]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("create digest nonce: %w", err)
	}
	nonce := base64.RawURLEncoding.EncodeToString(random[:])
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reapExpired(now)
	if len(s.entries) >= s.capacity {
		var oldest string
		var earliest time.Time
		for existing, entry := range s.entries {
			if oldest == "" || entry.expires.Before(earliest) {
				oldest, earliest = existing, entry.expires
			}
		}
		delete(s.entries, oldest)
	}
	s.entries[nonce] = digestNonceEntry{expires: now.Add(s.ttl)}
	return nonce, nil
}

// New is a convenience wrapper for call sites that cannot return errors. An
// empty result signals nonce generation failure; callers must fail closed.
func (s *DigestNonceStore) New() string {
	nonce, _ := s.Issue()
	return nonce
}

// Consume marks a nonce as used exactly once. Use this for Digest exchanges
// without qop/nonce-count support and for policies that require one request
// per challenge.
func (s *DigestNonceStore) Consume(nonce string) bool {
	return s.Use(nonce, 0)
}

// Use accepts a nonce-count request only when its count is strictly greater
// than every prior accepted count. A zero count consumes the nonce once.
func (s *DigestNonceStore) Use(nonce string, nonceCount uint32) bool {
	if s == nil || nonce == "" {
		return false
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[nonce]
	if !ok {
		return false
	}
	if !now.Before(entry.expires) {
		delete(s.entries, nonce)
		return false
	}
	if nonceCount == 0 {
		if entry.consumed || entry.maxCount != 0 {
			return false
		}
		entry.consumed = true
	} else {
		if entry.consumed || nonceCount <= entry.maxCount {
			return false
		}
		entry.maxCount = nonceCount
	}
	s.entries[nonce] = entry
	return true
}

func (s *DigestNonceStore) reapExpired(now time.Time) {
	for nonce, entry := range s.entries {
		if !now.Before(entry.expires) {
			delete(s.entries, nonce)
		}
	}
}
