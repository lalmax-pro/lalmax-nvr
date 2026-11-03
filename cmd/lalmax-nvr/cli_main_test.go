package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/stretchr/testify/require"
)

func TestCLIVersionFlag(t *testing.T) {
	out, err := runCLI(t, "-version")
	require.NoError(t, err, out)
	require.Contains(t, out, "lalmax-nvr version")
	require.Contains(t, out, appVersion)
}

func TestCLIRejectsBrokenConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.yaml")
	require.NoError(t, os.WriteFile(path, []byte("server: [unclosed\n"), 0o644))
	out, err := runCLI(t, "-config", path)
	require.Error(t, err)
	require.Contains(t, out, "level=ERROR")
}

// TestCLIStartsAndStopsOnSignal runs the real main entry point in a child
// process, waits for the HTTP listener, and lets SIGTERM drive the graceful
// shutdown path.
func TestCLIStartsAndStopsOnSignal(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NVR_DATA_DIR", dir)
	cfgPath := filepath.Join(dir, "config", "lalmax-nvr.yaml")
	cfg := autoInitConfig(cfgPath)
	addr := freeTCPAddr(t)
	cfg.Server.Listen = addr
	// Keep the child's FTP listener disabled: its dependency races on
	// start/stop, which the race-enabled test binary would report.
	ftpDisabled := false
	cfg.FTP.Enabled = &ftpDisabled
	require.NoError(t, config.Save(cfgPath, cfg))

	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(),
		"LALMAX_NVR_TEST_CLI=1",
		"LALMAX_NVR_TEST_CLI_ARGS="+strings.Join([]string{"-config", cfgPath}, "\x1f"),
		"NVR_DATA_DIR="+dir,
	)
	var mu sync.Mutex
	var output strings.Builder
	capture := &lockedWriter{mu: &mu, b: &output}
	cmd.Stdout = capture
	cmd.Stderr = capture
	require.NoError(t, cmd.Start())

	waitForTCP(t, addr)
	require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		require.NoError(t, err, "child output:\n%s", capturedOutput(capture))
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("child did not exit after SIGTERM:\n%s", capturedOutput(capture))
	}
}

type lockedWriter struct {
	mu *sync.Mutex
	b  *strings.Builder
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func capturedOutput(w *lockedWriter) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

// TestCLIAutoInitRejectsShortPassword covers the first-run bootstrap path: the
// configuration file is missing, so it is generated from NVR_DATA_DIR and
// NVR_PASSWORD. A too-short password aborts before any listener is opened.
func TestCLIAutoInitRejectsShortPassword(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config", "lalmax-nvr.yaml")
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(),
		"LALMAX_NVR_TEST_CLI=1",
		"LALMAX_NVR_TEST_CLI_ARGS="+strings.Join([]string{"-config", cfgPath}, "\x1f"),
		"NVR_DATA_DIR="+dir,
		"NVR_PASSWORD=short",
	)
	out, err := cmd.CombinedOutput()
	require.Error(t, err)
	require.Contains(t, string(out), "at least 8 characters")
}

func TestCLIHashPasswordRejectsLongPassword(t *testing.T) {
	out, err := runCLI(t, "hash-password", strings.Repeat("a", 80))
	require.Error(t, err)
	require.Contains(t, out, "Error:")
}

func TestCLIInitRequiresPasswordWhenFlagIsDangling(t *testing.T) {
	out, err := runCLI(t, "init", "--data-dir", t.TempDir(), "--password")
	require.Error(t, err)
	require.Contains(t, out, "password is required")
}

func TestCLIHealthUsesConfigListenAddr(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "health.yaml")
	require.NoError(t, config.Save(cfgPath, &config.Config{
		Version: "1.0",
		Server:  config.ServerConfig{Listen: freeTCPAddr(t)},
	}))
	// The dangling --addr keeps the listener from the config file, so the
	// health probe targets a port where nothing is listening.
	out, err := runCLI(t, "health", "--config", cfgPath, "--addr")
	require.Error(t, err)
	require.Contains(t, out, "Health check failed")
}
