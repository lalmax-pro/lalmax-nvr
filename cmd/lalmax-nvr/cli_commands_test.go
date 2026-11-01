package main

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

func TestMain(m *testing.M) {
	if os.Getenv("LALMAX_NVR_TEST_CLI") == "1" {
		os.Args = append([]string{os.Args[0]}, strings.Split(os.Getenv("LALMAX_NVR_TEST_CLI_ARGS"), "\x1f")...)
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(),
		"LALMAX_NVR_TEST_CLI=1",
		"LALMAX_NVR_TEST_CLI_ARGS="+strings.Join(args, "\x1f"),
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestCLIHashPassword(t *testing.T) {
	out, err := runCLI(t, "hash-password")
	require.Error(t, err)
	require.Contains(t, out, "Usage: lalmax-nvr hash-password")

	out, err = runCLI(t, "hash-password", "unit-test-password")
	require.NoError(t, err)
	hash := strings.TrimSpace(out)
	require.NoError(t, bcrypt.CompareHashAndPassword([]byte(hash), []byte("unit-test-password")))
}

func TestCLIInitValidationAndForce(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "nvr.yaml")
	dataDir := filepath.Join(dir, "data")

	out, err := runCLI(t, "init", "--config", cfgPath, "--data-dir", dataDir)
	require.Error(t, err)
	require.Contains(t, out, "password is required")

	out, err = runCLI(t, "init", "--password", "short", "--config", cfgPath, "--data-dir", dataDir)
	require.Error(t, err)
	require.Contains(t, out, "at least 8 characters")

	out, err = runCLI(t, "init", "--password", "unit-test-password", "--username", "operator", "--listen", ":19090", "--config", cfgPath, "--data-dir", dataDir)
	require.NoError(t, err, out)
	require.Contains(t, out, "Configuration saved")
	require.FileExists(t, cfgPath)

	out, err = runCLI(t, "init", "--password", "unit-test-password", "--config", cfgPath, "--data-dir", dataDir)
	require.Error(t, err)
	require.Contains(t, out, "already exists")

	out, err = runCLI(t, "init", "--force", "--password", "unit-test-password", "--username", "root", "--config", cfgPath, "--data-dir", dataDir)
	require.NoError(t, err, out)
	cfg, err := config.Load(cfgPath)
	require.NoError(t, err)
	require.Equal(t, "root", cfg.Auth.Username)
	require.Equal(t, dataDir, cfg.Storage.RootDir)
	require.NotEmpty(t, cfg.Auth.PasswordHash)
}

func TestCLIHealthAndEncryptConfig(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/health" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ok.Close)
	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	t.Cleanup(fail.Close)

	_, portOK, err := splitHostPortLast(ok.URL)
	require.NoError(t, err)
	_, portFail, err := splitHostPortLast(fail.URL)
	require.NoError(t, err)

	out, err := runCLI(t, "health", "--addr", ":"+portOK)
	require.NoError(t, err, out)
	out, err = runCLI(t, "health", "--addr", ":"+portFail)
	require.Error(t, err)
	require.Contains(t, out, "HTTP 503")
	out, err = runCLI(t, "health", "--config", filepath.Join(t.TempDir(), "missing.yaml"))
	require.Error(t, err)
	require.Contains(t, out, "Error loading config")

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "encrypt.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte("version: \"1.0\"\nauth:\n  username: admin\n  password: unit-secret\n"), 0o644))
	var key [32]byte
	_, err = rand.Read(key[:])
	require.NoError(t, err)
	t.Setenv("NVR_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(key[:]))
	out, err = runCLI(t, "encrypt-config", "--config", cfgPath)
	require.NoError(t, err, out)
	require.Contains(t, out, "Encrypted")
	require.Contains(t, out, "auth.password")
	out, err = runCLI(t, "encrypt-config", "--config", filepath.Join(dir, "missing.yaml"))
	require.Error(t, err)
	require.Contains(t, out, "Error:")
}

func splitHostPortLast(rawURL string) (string, string, error) {
	u := strings.TrimPrefix(rawURL, "http://")
	u = strings.TrimPrefix(u, "https://")
	host, port, ok := strings.Cut(u, ":")
	if !ok {
		return "", "", os.ErrInvalid
	}
	return host, port, nil
}
