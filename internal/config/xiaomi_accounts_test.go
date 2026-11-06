package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestXiaomiNamedAccountsEncryptAndRestore(t *testing.T) {
	key := helperGenerateKey(t)
	cfg := &Config{Xiaomi: XiaomiConfig{UserID: "default", Token: "default-secret", Accounts: map[string]XiaomiAccount{
		"home": {UserID: "home-user", Token: "home-secret", Region: "sg"},
	}}}
	snapshot := snapshotSensitive(cfg)
	encryptConfig(cfg, key)
	require.True(t, IsEncrypted(cfg.Xiaomi.Accounts["home"].UserID))
	require.True(t, IsEncrypted(cfg.Xiaomi.Accounts["home"].Token))
	encoded, err := json.Marshal(cfg.Xiaomi.Accounts["home"])
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "token")
	decryptConfig(cfg, key)
	require.Equal(t, "home-secret", cfg.Xiaomi.Accounts["home"].Token)
	encryptConfig(cfg, key)
	snapshot.restore(cfg)
	require.Equal(t, "home-secret", cfg.Xiaomi.Accounts["home"].Token)
	require.Equal(t, "default-secret", cfg.Xiaomi.Token)
}

func TestSaveXiaomiAccountsEncryptsWithoutMutatingLiveConfig(t *testing.T) {
	key := helperGenerateKey(t)
	helperSetEnvKey(t, key)
	t.Cleanup(func() { encryptionKeyMu.Lock(); encryptionKey = nil; encryptionKeyMu.Unlock() })
	cfg := &Config{Xiaomi: XiaomiConfig{Accounts: map[string]XiaomiAccount{
		"home": {UserID: "home-user", Token: "home-secret", Region: "sg"},
	}}}
	path := filepath.Join(t.TempDir(), "nvr.yaml")
	require.NoError(t, Save(path, cfg))
	require.Equal(t, "home-secret", cfg.Xiaomi.Accounts["home"].Token)
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(body), "home-secret")
	require.NotContains(t, string(body), "home-user")
	loaded, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, cfg.Xiaomi.Accounts["home"], loaded.Xiaomi.Accounts["home"])
}
