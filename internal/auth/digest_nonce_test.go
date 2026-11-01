package auth

import (
	"encoding/base64"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDigestNonceStoreLifecycle(t *testing.T) {
	defaults := NewDigestNonceStore(0, 0)
	require.Equal(t, defaultDigestNonceTTL, defaults.ttl)
	require.Equal(t, defaultDigestNonceCapacity, defaults.capacity)
	var absent *DigestNonceStore
	_, err := absent.Issue()
	require.Error(t, err)
	require.Empty(t, absent.New())
	require.False(t, absent.Use("nonce", 1))
	store := NewDigestNonceStore(time.Minute, 4)
	require.False(t, store.Consume(""))
	require.False(t, store.Consume("unknown"))
	nonce := store.New()
	raw, err := base64.RawURLEncoding.DecodeString(nonce)
	require.NoError(t, err)
	require.Len(t, raw, 24)
	require.True(t, store.Consume(nonce))
	require.False(t, store.Consume(nonce))
	require.False(t, store.Use(nonce, 1))
	counted := store.New()
	require.NotEqual(t, nonce, counted)
	require.True(t, store.Use(counted, 1))
	require.False(t, store.Use(counted, 1))
	require.False(t, store.Consume(counted))
	require.True(t, store.Use(counted, 3))
	require.False(t, store.Use(counted, 2))
	// Expire entries directly rather than relying on sleeps or timing margins.
	store.mu.Lock()
	store.entries["expired"] = digestNonceEntry{expires: time.Now().Add(-time.Hour)}
	store.mu.Unlock()
	require.False(t, store.Consume("expired"))
	_, exists := store.entries["expired"]
	require.False(t, exists)
}

func TestDigestNonceStoreCapacityAndReaping(t *testing.T) {
	store := NewDigestNonceStore(time.Hour, 2)
	store.entries["oldest"] = digestNonceEntry{expires: time.Now().Add(time.Minute)}
	store.entries["newer"] = digestNonceEntry{expires: time.Now().Add(2 * time.Minute)}
	nonce, err := store.Issue()
	require.NoError(t, err)
	require.Len(t, store.entries, 2)
	require.NotContains(t, store.entries, "oldest")
	require.Contains(t, store.entries, "newer")
	require.Contains(t, store.entries, nonce)
	store.entries["newer"] = digestNonceEntry{expires: time.Now().Add(-time.Minute)}
	next, err := store.Issue()
	require.NoError(t, err)
	require.Len(t, store.entries, 2)
	require.NotContains(t, store.entries, "newer")
	require.Contains(t, store.entries, next)
}

func TestDigestNonceConsumptionConcurrent(t *testing.T) {
	store := NewDigestNonceStore(time.Minute, 1)
	nonce := store.New()
	var wg sync.WaitGroup
	var accepted atomic.Int32
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if store.Consume(nonce) {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	require.Equal(t, int32(1), accepted.Load())
}

func TestDigestAuthorizationConsumesOnlyValidNonces(t *testing.T) {
	store := NewDigestNonceStore(time.Minute, 4)
	nonce := store.New()
	header, err := BuildDigestAuthorization("DESCRIBE", "rtsp://camera/live", "user", "pass", "realm", nonce, "")
	require.NoError(t, err)
	require.False(t, VerifyDigestAuthorizationAndConsumeNonce("DESCRIBE", "user", "realm", "pass", header, nil))
	require.False(t, VerifyDigestAuthorizationAndConsumeNonce("DESCRIBE", "user", "realm", "pass", "bad", store))
	require.False(t, VerifyDigestAuthorizationAndConsumeNonce("DESCRIBE", "user", "realm", "wrong", header, store))
	require.True(t, VerifyDigestAuthorizationAndConsumeNonce("DESCRIBE", "user", "realm", "pass", header, store))
	require.False(t, VerifyDigestAuthorizationAndConsumeNonce("DESCRIBE", "user", "realm", "pass", header, store))
	unknown, err := BuildDigestAuthorization("DESCRIBE", "rtsp://camera/live", "user", "pass", "realm", "unknown", "")
	require.NoError(t, err)
	require.False(t, VerifyDigestAuthorizationAndConsumeNonce("DESCRIBE", "user", "realm", "pass", unknown, store))
}
