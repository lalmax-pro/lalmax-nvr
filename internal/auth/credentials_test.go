package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

func TestPasswordHashAndCache(t *testing.T) {
	password := "test-password-unique-to-cache-test"
	hash, err := HashPassword(password)
	require.NoError(t, err)
	require.NotEqual(t, password, hash)
	cost, err := bcrypt.Cost([]byte(hash))
	require.NoError(t, err)
	require.Equal(t, bcrypt.DefaultCost, cost)
	key := password + "\x00" + hash
	t.Cleanup(func() { passwordCache.Delete(key) })
	require.False(t, CheckPassword(password, " \t"))
	require.False(t, CheckPassword(password, "invalid hash"))
	require.False(t, CheckPassword("wrong", hash))
	require.True(t, CheckPassword(password, hash))
	require.True(t, CheckPassword(password, hash))
	for _, entry := range []passwordCacheEntry{
		{hash: hash, verifiedAt: time.Now().Add(-2 * passwordCacheTTL)},
		{hash: "different", verifiedAt: time.Now()},
	} {
		passwordCache.Store(key, entry)
		require.True(t, CheckPassword(password, hash))
		refreshed, ok := passwordCache.Load(key)
		require.True(t, ok)
		require.Equal(t, hash, refreshed.(passwordCacheEntry).hash)
		require.WithinDuration(t, time.Now(), refreshed.(passwordCacheEntry).verifiedAt, time.Second)
	}
	_, err = HashPassword(strings.Repeat("x", 73))
	require.ErrorIs(t, err, bcrypt.ErrPasswordTooLong)
	// An expired cache entry must not turn a previously cached failure into success.
	badKey := "wrong\x00" + hash
	t.Cleanup(func() { passwordCache.Delete(badKey) })
	passwordCache.Store(badKey, passwordCacheEntry{hash: hash, verifiedAt: time.Now().Add(-2 * passwordCacheTTL)})
	require.False(t, CheckPassword("wrong", hash))
	_, ok := passwordCache.Load(badKey)
	require.False(t, ok)
}

func TestConstantTimeEqual(t *testing.T) {
	for _, tc := range []struct {
		got, want string
		equal     bool
	}{
		{"", "", false}, {"", "secret", false}, {"secret", "", false}, {"secret", "secret", true}, {"secret", "Secret", false}, {"short", "longer", false},
	} {
		require.Equal(t, tc.equal, ConstantTimeEqual(tc.got, tc.want))
	}
}

func TestMediaTokenPolicy(t *testing.T) {
	var absent *MediaAuthorizer
	absent.SetPublishTokens("rtmp", nil)
	absent.SetPlaybackTokens("rtsp", nil)
	require.False(t, absent.PlaybackRequired())
	require.False(t, absent.SharedPublishRequired())
	require.ErrorContains(t, absent.AuthorizePublish(" RTMP ", " cam ", "secret"), "rtmp publish is not configured")
	a := NewMediaAuthorizer()
	require.False(t, a.PlaybackRequired())
	require.False(t, a.SharedPublishRequired())
	require.Error(t, a.AuthorizePublish("rtmp", "cam", ""))
	tokens := map[string]string{" cam ": "shared", "": "empty-id", "ignored": ""}
	a.SetPublishTokens(" MEDIA ", tokens)
	tokens[" cam "] = "mutated"
	require.True(t, a.SharedPublishRequired())
	require.NoError(t, a.AuthorizePublish(" RTMP ", " cam ", "shared"))
	require.Error(t, a.AuthorizePublish("rtmp", "ignored", ""))
	require.Error(t, a.AuthorizePublish("rtmp", "cam", "mutated"))
	a.SetPublishTokens("rtmp", map[string]string{"cam": "specific"})
	require.NoError(t, a.AuthorizePublish("rtmp", "cam", "specific"))
	require.Error(t, a.AuthorizePublish("rtmp", "cam", "shared"))
	require.NoError(t, a.AuthorizePublish("rtsp", "cam", "shared"))
	require.Error(t, a.AuthorizePlayback("rtmp", "cam", "specific"))
	a.SetPlaybackTokens("rtsp", map[string]string{"cam": "viewer"})
	require.True(t, a.PlaybackRequired())
	require.NoError(t, a.AuthorizePlayback("rtsp", "cam", "viewer"))
	a.SetPlaybackTokens("rtsp", nil)
	require.False(t, a.PlaybackRequired())
	a.SetPublishTokens("media", nil)
	require.False(t, a.SharedPublishRequired())
	a.SetPublishTokens("rtmp", nil)
	require.Error(t, a.AuthorizePublish("rtmp", "cam", "specific"))
	var zero MediaAuthorizer
	zero.SetPlaybackTokens("rtsp", map[string]string{"cam": "viewer"})
	require.NoError(t, zero.AuthorizePlayback("rtsp", "cam", "viewer"))
}

func TestQueryToken(t *testing.T) {
	for _, tc := range []struct{ query, key, want string }{
		{"token=a%2Bb%20c", "token", "a+b c"}, {"token=first&token=second", "token", "first"}, {"other=value", "token", ""}, {"", "token", ""},
	} {
		got, err := QueryToken(tc.query, tc.key)
		require.NoError(t, err)
		require.Equal(t, tc.want, got)
	}
	for _, query := range []string{"token=ok&bad=%zz", "token=ok;bad=value"} {
		got, err := QueryToken(query, "token")
		require.Error(t, err)
		require.Empty(t, got)
	}
}
