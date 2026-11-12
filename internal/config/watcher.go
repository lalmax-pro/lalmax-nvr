package config

import (
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"
)

var watchLogger = slog.Default().With("component", "config.watcher")

// ErrConfigModified is returned by Save when the on-disk config was modified
// externally since the last load. The caller should reload before saving.
var ErrConfigModified = fmt.Errorf("config file was modified externally; reload before saving")

// Watcher tracks the on-disk config file, detects external modifications,
// and provides hot-reload capability via subscriber callbacks.
type Watcher struct {
	mu        sync.RWMutex
	cfg       *Config
	path      string
	lastMtime time.Time
	lastSize  int64
	subs      []func(*Config)
}

// NewWatcher creates a Watcher from an already-loaded Config and its file path.
// It records the current file mtime as the baseline.
func NewWatcher(cfg *Config, path string) (*Watcher, error) {
	w := &Watcher{
		cfg:  cfg,
		path: path,
	}
	if err := w.statFile(); err != nil {
		return nil, err
	}
	return w, nil
}

// Config returns the current in-memory config (read-only pointer).
func (w *Watcher) Config() *Config {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.cfg
}

// Subscribe registers a callback that is invoked after a successful Reload.
// The callback receives the new config pointer.
func (w *Watcher) Subscribe(fn func(*Config)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.subs = append(w.subs, fn)
}

// CheckExternalChange returns true if the on-disk file has been modified
// since the last load or save.
func (w *Watcher) CheckExternalChange() bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	info, err := os.Stat(w.path)
	if err != nil {
		return false
	}
	return !info.ModTime().Equal(w.lastMtime) || info.Size() != w.lastSize
}

// Reload re-reads the config from disk, updates the in-memory config,
// and notifies all subscribers. Use this when an external change is detected.
func (w *Watcher) Reload() error {
	// Load without holding the watcher lock: file IO must not block Config()
	// or subscriber registration.
	cfg, err := Load(w.path)
	if err != nil {
		return fmt.Errorf("reload config: %w", err)
	}

	w.mu.Lock()
	if err := w.statFileLocked(); err != nil {
		w.mu.Unlock()
		return err
	}
	w.cfg = cfg
	subscribers := append([]func(*Config){}, w.subs...)
	w.mu.Unlock()

	watchLogger.Info("config reloaded from disk", "path", w.path)
	// Callbacks may call Config(), Subscribe() or Save(). Never run them
	// under w.mu; otherwise a reentrant callback deadlocks the watcher.
	for _, fn := range subscribers {
		fn(cfg)
	}
	return nil
}

// ReloadWithGuard serializes loading and installation with the owner's config
// mutations while invoking watcher subscribers only after that guard is released.
func (w *Watcher) ReloadWithGuard(lock, unlock func(), apply func(*Config)) error {
	lock()
	cfg, err := Load(w.path)
	if err != nil {
		unlock()
		return fmt.Errorf("reload config: %w", err)
	}
	w.mu.Lock()
	if err := w.statFileLocked(); err != nil {
		w.mu.Unlock()
		unlock()
		return err
	}
	w.cfg = cfg
	subscribers := append([]func(*Config){}, w.subs...)
	w.mu.Unlock()
	apply(cfg)
	unlock()
	watchLogger.Info("config reloaded from disk", "path", w.path)
	for _, fn := range subscribers {
		fn(cfg)
	}
	return nil
}

// Save persists the in-memory config to disk. It checks for external
// modifications first and returns ErrConfigModified if the file was changed
// since the last load/save. Pass force=true to skip the check.
func (w *Watcher) Save(force bool) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if !force {
		info, err := os.Stat(w.path)
		if err == nil {
			if !info.ModTime().Equal(w.lastMtime) || info.Size() != w.lastSize {
				return ErrConfigModified
			}
		}
	}

	if err := Save(w.path, w.cfg); err != nil {
		return err
	}
	return w.statFileLocked()
}

// UpdateConfig applies a mutation function to the in-memory config and
// saves it to disk. The mutation function is called with the config pointer
// while the lock is held.
func (w *Watcher) UpdateConfig(mutate func(*Config), force bool) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if !force {
		info, err := os.Stat(w.path)
		if err == nil {
			if !info.ModTime().Equal(w.lastMtime) || info.Size() != w.lastSize {
				return ErrConfigModified
			}
		}
	}

	mutate(w.cfg)

	if err := Save(w.path, w.cfg); err != nil {
		return err
	}
	return w.statFileLocked()
}

func (w *Watcher) statFile() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.statFileLocked()
}

func (w *Watcher) statFileLocked() error {
	info, err := os.Stat(w.path)
	if err != nil {
		return err
	}
	w.lastMtime = info.ModTime()
	w.lastSize = info.Size()
	return nil
}
