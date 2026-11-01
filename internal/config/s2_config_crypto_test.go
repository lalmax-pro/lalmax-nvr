package config

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lalmax-pro/lalmax-nvr/internal/voip"
)

// s2ResetEncryptionState clears the package-level encryption globals (and the
// related environment variables) before and after a test.
func s2ResetEncryptionState(t *testing.T) {
	t.Helper()
	t.Setenv("NVR_ENCRYPTION_KEY", "")
	t.Setenv("NVR_ENCRYPTION_KEY_FILE", "")
	reset := func() {
		encryptionKeyMu.Lock()
		encryptionKey = nil
		webEncryptionKeyFile = ""
		encryptionKeyMu.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

func TestS2EncryptionKeySource(t *testing.T) {
	s2ResetEncryptionState(t)
	require.Equal(t, "none", EncryptionKeySource())

	t.Setenv("NVR_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(helperGenerateKey(t)))
	require.Equal(t, "environment", EncryptionKeySource())

	t.Setenv("NVR_ENCRYPTION_KEY", "")
	keyFile := filepath.Join(t.TempDir(), "env.key")
	require.NoError(t, os.WriteFile(keyFile, []byte("x"), 0o600))
	t.Setenv("NVR_ENCRYPTION_KEY_FILE", keyFile)
	require.Equal(t, "environment_file", EncryptionKeySource())

	t.Setenv("NVR_ENCRYPTION_KEY_FILE", "")
	SetWebEncryptionKeyFile(keyFile)
	require.Equal(t, "web", EncryptionKeySource())

	SetWebEncryptionKeyFile(filepath.Join(t.TempDir(), "missing.key"))
	require.Equal(t, "none", EncryptionKeySource())
}

func TestS2GetEncryptionKeyFromWebFile(t *testing.T) {
	s2ResetEncryptionState(t)
	key := helperGenerateKey(t)
	keyFile := filepath.Join(t.TempDir(), "web.key")
	require.NoError(t, os.WriteFile(keyFile, []byte(base64.StdEncoding.EncodeToString(key)+"\n"), 0o600))

	SetWebEncryptionKeyFile(keyFile)
	require.True(t, HasKey())
	require.Equal(t, key, GetEncryptionKey())

	// Cached key survives a subsequent call.
	require.Equal(t, key, GetEncryptionKey())

	// A configured but unreadable key file yields no key.
	s2ResetEncryptionState(t)
	SetWebEncryptionKeyFile(filepath.Join(t.TempDir(), "absent.key"))
	require.Nil(t, GetEncryptionKey())
	require.False(t, HasKey())
}

func TestS2GetEncryptionKeyEnvFileErrors(t *testing.T) {
	s2ResetEncryptionState(t)
	// Environment key file that cannot be read.
	t.Setenv("NVR_ENCRYPTION_KEY_FILE", filepath.Join(t.TempDir(), "absent.key"))
	require.Nil(t, GetEncryptionKey())

	// Environment key file with an unparseable payload.
	badFile := filepath.Join(t.TempDir(), "bad.key")
	require.NoError(t, os.WriteFile(badFile, []byte("not-a-key"), 0o600))
	t.Setenv("NVR_ENCRYPTION_KEY_FILE", badFile)
	require.Nil(t, GetEncryptionKey())
}

func TestS2ParseEncryptionKeyRaw32Bytes(t *testing.T) {
	raw := strings.Repeat("a", 32)
	key, err := parseEncryptionKey(raw)
	require.NoError(t, err)
	require.Equal(t, []byte(raw), key)

	// A 32-byte raw key is accepted by Encrypt/Decrypt too.
	enc, err := Encrypt("secret", key)
	require.NoError(t, err)
	dec, err := Decrypt(enc, key)
	require.NoError(t, err)
	require.Equal(t, "secret", dec)

	_, err = parseEncryptionKey("too-short")
	require.Error(t, err)
}

func TestS2CreateWebEncryptionKey(t *testing.T) {
	t.Run("invalid key", func(t *testing.T) {
		s2ResetEncryptionState(t)
		err := CreateWebEncryptionKey(filepath.Join(t.TempDir(), "key"), "short")
		require.ErrorIs(t, err, ErrInvalidEncryptionKey)
	})

	t.Run("environment managed", func(t *testing.T) {
		s2ResetEncryptionState(t)
		t.Setenv("NVR_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(helperGenerateKey(t)))
		err := CreateWebEncryptionKey(filepath.Join(t.TempDir(), "key"), base64.StdEncoding.EncodeToString(helperGenerateKey(t)))
		require.ErrorIs(t, err, ErrEncryptionKeyConfigured)
	})

	t.Run("empty path", func(t *testing.T) {
		s2ResetEncryptionState(t)
		err := CreateWebEncryptionKey("", base64.StdEncoding.EncodeToString(helperGenerateKey(t)))
		require.Error(t, err)
		require.Contains(t, err.Error(), "config path is not available")
	})

	t.Run("already configured in memory", func(t *testing.T) {
		s2ResetEncryptionState(t)
		dir := t.TempDir()
		keyValue := base64.StdEncoding.EncodeToString(helperGenerateKey(t))
		require.NoError(t, CreateWebEncryptionKey(filepath.Join(dir, "key"), keyValue))

		err := CreateWebEncryptionKey(filepath.Join(dir, "other-key"), keyValue)
		require.ErrorIs(t, err, ErrEncryptionKeyConfigured)
	})

	t.Run("success writes sidecar file", func(t *testing.T) {
		s2ResetEncryptionState(t)
		dir := t.TempDir()
		path := filepath.Join(dir, "nested", "encryption.key")
		key := helperGenerateKey(t)
		require.NoError(t, CreateWebEncryptionKey(path, base64.StdEncoding.EncodeToString(key)))

		info, err := os.Stat(path)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

		data, err := os.ReadFile(path)
		require.NoError(t, err)
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
		require.NoError(t, err)
		require.Equal(t, key, decoded)

		require.Equal(t, "web", EncryptionKeySource())
		require.Equal(t, key, GetEncryptionKey())
	})

	t.Run("preexisting file is rejected", func(t *testing.T) {
		s2ResetEncryptionState(t)
		path := filepath.Join(t.TempDir(), "existing.key")
		require.NoError(t, os.WriteFile(path, []byte("occupied"), 0o600))
		err := CreateWebEncryptionKey(path, base64.StdEncoding.EncodeToString(helperGenerateKey(t)))
		require.ErrorIs(t, err, ErrEncryptionKeyConfigured)
	})

	t.Run("unusable directory", func(t *testing.T) {
		s2ResetEncryptionState(t)
		blocker := filepath.Join(t.TempDir(), "blocker")
		require.NoError(t, os.WriteFile(blocker, []byte("not a directory"), 0o600))
		err := CreateWebEncryptionKey(filepath.Join(blocker, "sub", "key"), base64.StdEncoding.EncodeToString(helperGenerateKey(t)))
		require.Error(t, err)
		require.Contains(t, err.Error(), "create key directory")
	})
}

// s2SensitiveConfig returns a config with every sensitive field set to a
// distinct plaintext value.
func s2SensitiveConfig() *Config {
	return &Config{
		Storage: StorageConfig{
			DatabaseDSN:     "postgres://user:pass@db/nvr",
			MigrationTarget: &DatabaseTargetConfig{Driver: "postgres", DSN: "postgres://migrate@db/nvr"},
		},
		Auth: AuthConfig{
			Password:       "auth-password",
			PlaybackTokens: map[string]string{"cam1": "play-token"},
			PublishTokens:  map[string]string{"cam1": "publish-token"},
		},
		Media:   MediaConfig{RTSPPassword: "rtsp-password"},
		MQTT:    MQTTConfig{Password: "mqtt-password"},
		Xiaomi:  XiaomiConfig{UserID: "xiaomi-user", Token: "xiaomi-token"},
		GB28181: GB28181Config{Password: "gb-password"},
		Cameras: []CameraConfig{{ID: "c1", Password: "camera-password"}},
		RTMP:    RTMPConfig{StreamKeys: map[string]string{"cam1": "rtmp-key"}},
		SRT:     SRTConfig{StreamKeys: map[string]string{"cam1": "srt-key"}},
		WHIP:    WHIPConfig{PublishTokens: map[string]string{"cam1": "whip-token"}},
		VoIP:    voip.Config{Users: []voip.User{{Username: "alice", Password: "voip-password"}}},
		MetricsAuth: MetricsAuthConfig{
			Username: "metrics",
			Password: "metrics-password",
		},
		AutoDiscover: AutoDiscoverConfig{DefaultPassword: "discover-password"},
	}
}

func TestS2EncryptDecryptConfigAllFields(t *testing.T) {
	key := helperGenerateKey(t)
	cfg := s2SensitiveConfig()

	encrypted := encryptConfig(cfg, key)
	require.ElementsMatch(t, []string{
		"storage.database_dsn",
		"storage.database_migration_target.dsn",
		"auth.password",
		"media.rtsp_password",
		"auth.playback_tokens[cam1]",
		"auth.publish_tokens[cam1]",
		"mqtt.password",
		"xiaomi.user_id",
		"xiaomi.token",
		"gb28181.password",
		"cameras[0].password",
		"rtmp.stream_keys[cam1]",
		"srt.stream_keys[cam1]",
		"whip.publish_tokens[cam1]",
		"voip.users[0].password",
		"metrics_auth.password",
		"auto_discover.default_password",
	}, encrypted)

	// Everything is encrypted now.
	require.True(t, IsEncrypted(cfg.Storage.DatabaseDSN))
	require.True(t, IsEncrypted(cfg.Storage.MigrationTarget.DSN))
	require.True(t, IsEncrypted(cfg.MQTT.Password))
	require.True(t, IsEncrypted(cfg.GB28181.Password))
	require.True(t, IsEncrypted(cfg.VoIP.Users[0].Password))
	require.True(t, IsEncrypted(cfg.AutoDiscover.DefaultPassword))
	require.Empty(t, SensitiveFieldPaths(cfg))

	// Re-encrypting is a no-op.
	require.Empty(t, encryptConfig(cfg, key))

	decryptConfig(cfg, key)
	require.Equal(t, s2SensitiveConfig(), cfg)
}

func TestS2SensitiveFieldPathsAllFields(t *testing.T) {
	cfg := s2SensitiveConfig()
	fields := SensitiveFieldPaths(cfg)
	require.ElementsMatch(t, []string{
		"storage.database_dsn",
		"auth.password",
		"media.rtsp_password",
		"mqtt.password",
		"xiaomi.user_id",
		"xiaomi.token",
		"gb28181.password",
		"cameras[0].password",
		"rtmp.stream_keys[cam1]",
		"srt.stream_keys[cam1]",
		"whip.publish_tokens[cam1]",
		"metrics_auth.password",
		"auto_discover.default_password",
		"storage.database_migration_target.dsn",
		"voip.users[0].password",
		"auth.playback_tokens[cam1]",
		"auth.publish_tokens[cam1]",
	}, fields)
}
