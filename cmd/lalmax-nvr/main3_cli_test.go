package main

import (
	"crypto/rand"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// main3RunCLI executes the real CLI entry point (via the TestMain hook in
// cli_commands_test.go) in a child process with extra environment variables.
func main3RunCLI(t *testing.T, env []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(append(os.Environ(),
		"LALMAX_NVR_TEST_CLI=1",
		"LALMAX_NVR_TEST_CLI_ARGS="+strings.Join(args, "\x1f"),
	), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// main3BlockerFile creates a regular file that cannot be used as a directory.
func main3BlockerFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "blocker")
	require.NoError(t, os.WriteFile(path, []byte("not a directory"), 0o644))
	return path
}

// TestMain3AutoInitWarnsWhenDataDirCannotBeCreated covers the two best-effort
// directory creation warnings and the config persistence warning in
// autoInitConfig.
func TestMain3AutoInitWarnsWhenDataDirCannotBeCreated(t *testing.T) {
	blocker := main3BlockerFile(t)
	dir := t.TempDir()
	t.Setenv("NVR_PASSWORD", "")
	t.Setenv("NVR_DATA_DIR", filepath.Join(blocker, "data"))

	// A writable config path: only the data directory creation fails.
	cfgPath := filepath.Join(dir, "config", "lalmax-nvr.yaml")
	cfg := autoInitConfig(cfgPath)
	require.NotNil(t, cfg)
	require.FileExists(t, cfgPath)
}

// TestMain3AutoInitWarnsWhenConfigDirCannotBeCreated covers the config
// directory creation warning and the failed save warning.
func TestMain3AutoInitWarnsWhenConfigDirCannotBeCreated(t *testing.T) {
	blocker := main3BlockerFile(t)
	dataDir := t.TempDir()
	t.Setenv("NVR_DATA_DIR", dataDir)
	t.Setenv("NVR_PASSWORD", "")

	cfgPath := filepath.Join(blocker, "config", "lalmax-nvr.yaml")
	cfg := autoInitConfig(cfgPath)
	require.NotNil(t, cfg)
	require.Equal(t, dataDir, cfg.Storage.RootDir)
	require.NoFileExists(t, cfgPath)
}

// TestMain3InitFailsWhenDataDirCannotBeCreated covers the CLI error path when
// the requested data directory cannot be created.
func TestMain3InitFailsWhenDataDirCannotBeCreated(t *testing.T) {
	blocker := main3BlockerFile(t)
	dir := t.TempDir()
	out, err := main3RunCLI(t, nil,
		"init",
		"--password", "unit-test-password",
		"--data-dir", filepath.Join(blocker, "data"),
		"--config", filepath.Join(dir, "nvr.yaml"),
	)
	require.Error(t, err)
	require.Contains(t, out, "Error creating data directory")
}

// TestMain3InitFailsWhenPasswordCannotBeHashed covers the bcrypt rejection path
// (passwords longer than 72 bytes are rejected by the hasher).
func TestMain3InitFailsWhenPasswordCannotBeHashed(t *testing.T) {
	dir := t.TempDir()
	out, err := main3RunCLI(t, nil,
		"init",
		"--password", strings.Repeat("a", 80),
		"--data-dir", filepath.Join(dir, "data"),
		"--config", filepath.Join(dir, "nvr.yaml"),
	)
	require.Error(t, err)
	require.Contains(t, out, "Error hashing password")
}

// TestMain3InitFailsWhenConfigCannotBeSaved covers the CLI config save error
// path: the parent of the config file is a regular file.
func TestMain3InitFailsWhenConfigCannotBeSaved(t *testing.T) {
	blocker := main3BlockerFile(t)
	dir := t.TempDir()
	out, err := main3RunCLI(t, nil,
		"init",
		"--password", "unit-test-password",
		"--data-dir", filepath.Join(dir, "data"),
		"--config", filepath.Join(blocker, "nvr.yaml"),
	)
	require.Error(t, err)
	require.Contains(t, out, "Error saving config")
}

// TestMain3EncryptConfigWithoutSensitiveFields covers the "nothing to encrypt"
// branch of encrypt-config.
func TestMain3EncryptConfigWithoutSensitiveFields(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "empty-encrypt.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("version: \"1.0\"\n"), 0o644))

	var key [32]byte
	_, err := rand.Read(key[:])
	require.NoError(t, err)
	out, err := main3RunCLI(t, []string{
		"NVR_ENCRYPTION_KEY=" + base64.StdEncoding.EncodeToString(key[:]),
	}, "encrypt-config", "--config", cfgPath)
	require.NoError(t, err, out)
	require.Contains(t, out, "No plaintext sensitive fields found")
}
