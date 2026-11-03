package main

import (
	"net"
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

// main3MainConfig is a minimal configuration that boots the real main() without
// binding any media port.
func main3MainConfig(t *testing.T, listen, rootDir string) *config.Config {
	t.Helper()
	disabled := false
	return &config.Config{
		Version: "1.0",
		Server:  config.ServerConfig{Listen: listen},
		Storage: config.StorageConfig{RootDir: rootDir, SegmentDuration: "30s"},
		Media: config.MediaConfig{
			Mode:            "http",
			LalmaxHTTPAddr:  "http://127.0.0.1:8080",
			LalmaxPublicURL: "http://127.0.0.1:8080",
		},
		FTP:     config.FTPConfig{Enabled: &disabled, Port: 2121, PassivePortRange: "2122-2140"},
		Cleanup: config.CleanupConfig{RetentionDays: 30, CheckInterval: "1h", DiskThresholdPercent: 95},
		WebDAV:  config.WebDAVConfig{PathPrefix: "/dav"},
	}
}

// main3WriteConfig persists a configuration file inside dir.
func main3WriteConfig(t *testing.T, dir string, cfg *config.Config) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, "lalmax-nvr.yaml")
	require.NoError(t, config.Save(path, cfg))
	return path
}

// main3RunChild runs main() in a child process with a hard timeout and returns
// its combined output and exit code.
func main3RunChild(t *testing.T, env []string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Dir = t.TempDir()
	cmd.Env = append(append(os.Environ(),
		"LALMAX_NVR_TEST_CLI=1",
		"LALMAX_NVR_TEST_CLI_ARGS="+strings.Join(args, "\x1f"),
	), env...)
	var mu sync.Mutex
	var output strings.Builder
	capture := &lockedWriter{mu: &mu, b: &output}
	cmd.Stdout = capture
	cmd.Stderr = capture
	require.NoError(t, cmd.Start())

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		mu.Lock()
		out := output.String()
		mu.Unlock()
		if err == nil {
			return out, 0
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			return out, exitErr.ExitCode()
		}
		return out, -1
	case <-time.After(60 * time.Second):
		_ = cmd.Process.Kill()
		mu.Lock()
		out := output.String()
		mu.Unlock()
		t.Fatalf("child did not exit in time, output:\n%s", out)
		return out, -1
	}
}

// TestMain3MainFixesDockerStoragePath covers the Docker storage-path auto-fix
// of main() and the warning emitted when the corrected config cannot be saved.
func TestMain3MainFixesDockerStoragePath(t *testing.T) {
	addr := freeTCPAddr(t)
	dataDir := t.TempDir()
	configDir := filepath.Join(t.TempDir(), "readonly")
	cfgPath := main3WriteConfig(t, configDir, main3MainConfig(t, addr, "/var/lib/lalmax-nvr"))
	require.NoError(t, os.Chmod(configDir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(configDir, 0o755) })

	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(),
		"LALMAX_NVR_TEST_CLI=1",
		"LALMAX_NVR_TEST_CLI_ARGS="+strings.Join([]string{"-config", cfgPath}, "\x1f"),
		"NVR_DATA_DIR="+dataDir,
		"NVR_PASSWORD=",
	)
	var mu sync.Mutex
	var output strings.Builder
	cmd.Stdout = &lockedWriter{mu: &mu, b: &output}
	cmd.Stderr = &lockedWriter{mu: &mu, b: &output}
	require.NoError(t, cmd.Start())

	waitForTCP(t, addr)
	require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		require.NoError(t, err, "child output:\n%s", func() string {
			mu.Lock()
			defer mu.Unlock()
			return output.String()
		}())
	case <-time.After(60 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("child did not exit after SIGTERM")
	}
}

// TestMain3MainRejectsInvalidConfig covers the config validation failure of
// main().
func TestMain3MainRejectsInvalidConfig(t *testing.T) {
	cfg := main3MainConfig(t, freeTCPAddr(t), t.TempDir())
	cfg.VoIP.Enable = true // requires media.mode=embedded, which is not set
	cfgPath := main3WriteConfig(t, t.TempDir(), cfg)

	out, code := main3RunChild(t, []string{"NVR_DATA_DIR=" + t.TempDir()}, "-config", cfgPath)
	require.Equal(t, 1, code, out)
	require.Contains(t, out, "config validation")
}

// TestMain3MainFailsOnInit covers the NewApp failure path of main().
func TestMain3MainFailsOnInit(t *testing.T) {
	blocker := main3BlockerFile(t)
	rootDir := filepath.Join(blocker, "storage")
	cfg := main3MainConfig(t, freeTCPAddr(t), rootDir)
	cfgPath := main3WriteConfig(t, t.TempDir(), cfg)

	out, code := main3RunChild(t, []string{"NVR_DATA_DIR=" + t.TempDir()}, "-config", cfgPath)
	require.Equal(t, 1, code, out)
	require.Contains(t, out, "level=ERROR")
	require.Contains(t, out, "init")
}

// TestMain3MainFailsOnRun covers the Start failure path of main().
func TestMain3MainFailsOnRun(t *testing.T) {
	occupied, err := net.Listen("tcp", "0.0.0.0:0")
	require.NoError(t, err)
	t.Cleanup(func() { occupied.Close() })

	cfg := main3MainConfig(t, freeTCPAddr(t), t.TempDir())
	cfg.Media.Mode = "embedded"
	cfg.JT808.Enabled = true
	cfg.JT808.Port = occupied.Addr().(*net.TCPAddr).Port
	cfg.JT808.MediaIP = "127.0.0.1"
	cfg.JT808.AuthCode = "secret"
	cfgPath := main3WriteConfig(t, t.TempDir(), cfg)

	out, code := main3RunChild(t, []string{"NVR_DATA_DIR=" + t.TempDir()}, "-config", cfgPath)
	require.Equal(t, 1, code, out)
	require.Contains(t, out, "start JT808 signaling")
}

// TestMain3AutoInitWithoutDataDirEnv covers the fallback data directory
// selection when NVR_DATA_DIR is not set.
func TestMain3AutoInitWithoutDataDirEnv(t *testing.T) {
	if _, err := os.Stat("/data"); err == nil {
		t.Skip("/data exists, the Docker branch would be used")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root: the fallback data directory would be created")
	}
	t.Setenv("NVR_DATA_DIR", "")
	t.Setenv("NVR_PASSWORD", "")

	cfgPath := filepath.Join(t.TempDir(), "config", "lalmax-nvr.yaml")
	cfg := autoInitConfig(cfgPath)
	require.Equal(t, "/var/lib/lalmax-nvr", cfg.Storage.RootDir)
	require.FileExists(t, cfgPath)
}

// TestMain3InitWithoutDataDirFlag covers the CLI default data directory branch.
func TestMain3InitWithoutDataDirFlag(t *testing.T) {
	if _, err := os.Stat("/data"); err == nil {
		t.Skip("/data exists, the Docker branch would be used")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root: the fallback data directory would be created")
	}
	out, err := main3RunCLI(t, nil,
		"init",
		"--password", "unit-test-password",
		"--config", filepath.Join(t.TempDir(), "nvr.yaml"),
	)
	require.Error(t, err)
	require.Contains(t, out, "Error creating data directory")
}
