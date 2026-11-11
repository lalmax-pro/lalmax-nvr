package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestS2WatcherLifecycle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lalmax-nvr.yaml")
	require.NoError(t, os.WriteFile(path, []byte("version: \"1.0\"\n"), 0o600))

	cfg, err := Load(path)
	require.NoError(t, err)
	w, err := NewWatcher(cfg, path)
	require.NoError(t, err)
	require.Same(t, cfg, w.Config())
	require.False(t, w.CheckExternalChange())

	reloaded := make(chan *Config, 1)
	w.Subscribe(func(c *Config) { reloaded <- c })

	// An external change (different content length) is detected and reloaded.
	require.NoError(t, os.WriteFile(path, []byte("version: \"2.0\"\n# externally edited\n"), 0o600))
	require.True(t, w.CheckExternalChange())
	require.NoError(t, w.Reload())

	select {
	case got := <-reloaded:
		require.Equal(t, "2.0", got.Version)
	default:
		t.Fatal("subscriber callback was not invoked")
	}
	require.False(t, w.CheckExternalChange())
	require.Equal(t, "2.0", w.Config().Version)

	// Save refuses to clobber an externally modified file, but force wins.
	require.NoError(t, os.WriteFile(path, []byte("version: \"3.0\"\n# another external edit\n"), 0o600))
	require.ErrorIs(t, w.Save(false), ErrConfigModified)
	require.NoError(t, w.Save(true))
	require.False(t, w.CheckExternalChange())

	// UpdateConfig applies the mutation and persists it; force skips the
	// external-modification guard.
	require.NoError(t, os.WriteFile(path, []byte("version: \"4.0\"\n# conflict\n"), 0o600))
	require.ErrorIs(t, w.UpdateConfig(func(c *Config) { c.Version = "5.0" }, false), ErrConfigModified)
	require.NoError(t, w.UpdateConfig(func(c *Config) { c.Version = "5.0" }, true))
	require.Equal(t, "5.0", w.Config().Version)

	onDisk, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, "5.0", onDisk.Version)
}

func TestS2WatcherFileErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent-dir", "absent.yaml")

	_, err := NewWatcher(&Config{}, missing)
	require.Error(t, err)

	// A watcher whose file never existed: every file operation fails cleanly.
	w := &Watcher{cfg: &Config{}, path: missing}
	require.False(t, w.CheckExternalChange())
	require.Error(t, w.Reload())
	require.Error(t, w.statFile())
	require.Error(t, w.Save(false))
	require.Error(t, w.UpdateConfig(func(c *Config) { c.Version = "9.0" }, true))

	// A config file that cannot be parsed surfaces as a reload error.
	dir := t.TempDir()
	badPath := filepath.Join(dir, "bad.yaml")
	require.NoError(t, os.WriteFile(badPath, []byte("server: [broken"), 0o600))
	bad := &Watcher{cfg: &Config{}, path: badPath}
	err = bad.Reload()
	require.Error(t, err)
	require.Contains(t, err.Error(), "reload config")
}
