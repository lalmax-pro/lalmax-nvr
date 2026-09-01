package config

import (
	"fmt"
	"strings"
	"time"
)

const (
	ActivationActive  = "active"
	ActivationPending = "pending_activation"
)

// AutoDiscoverConfig controls background ONVIF plug-and-play enrollment.
type AutoDiscoverConfig struct {
	Enabled          *bool  `yaml:"enabled" json:"enabled"`
	ListenForHello   *bool  `yaml:"listen_for_hello" json:"listen_for_hello"`
	ScanInterval     string `yaml:"scan_interval" json:"scan_interval"`
	DefaultUsername  string `yaml:"default_username" json:"default_username"`
	DefaultPassword  string `yaml:"default_password" json:"default_password,omitempty"`
	NetworkInterface string `yaml:"network_interface" json:"network_interface"`
}

// IsEnabled reports whether auto-discover is explicitly turned on.
func (c AutoDiscoverConfig) IsEnabled() bool {
	return c.Enabled != nil && *c.Enabled
}

// HelloListenEnabled reports whether the WS-Discovery Hello listener should run.
func (c AutoDiscoverConfig) HelloListenEnabled() bool {
	if !c.IsEnabled() {
		return false
	}
	return c.ListenForHello == nil || *c.ListenForHello
}

// ScanIntervalDuration returns the periodic Probe interval (minimum 30s).
func (c AutoDiscoverConfig) ScanIntervalDuration() time.Duration {
	d, err := time.ParseDuration(strings.TrimSpace(c.ScanInterval))
	if err != nil || d < 30*time.Second {
		return 60 * time.Second
	}
	return d
}

// RediscoveryConfig controls unicast IP self-healing for ONVIF cameras.
type RediscoveryConfig struct {
	Enabled      *bool  `yaml:"enabled" json:"enabled"`
	MaxParallel  int    `yaml:"max_parallel" json:"max_parallel"`
	ProbeTimeout string `yaml:"probe_timeout" json:"probe_timeout"`
}

// IsEnabled reports whether rediscovery is on (default true when unset).
func (c RediscoveryConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

// ProbeTimeoutDuration returns the per-IP probe timeout.
func (c RediscoveryConfig) ProbeTimeoutDuration() time.Duration {
	d, err := time.ParseDuration(strings.TrimSpace(c.ProbeTimeout))
	if err != nil || d <= 0 {
		return 2 * time.Second
	}
	return d
}

// EventConfig controls event-triggered recording windows.
type EventConfig struct {
	PostRoll    string `yaml:"post_roll" json:"post_roll"`
	MaxDuration string `yaml:"max_duration" json:"max_duration"`
}

// PostRollDuration is how long to keep recording after the last trigger.
func (c EventConfig) PostRollDuration() time.Duration {
	d, err := time.ParseDuration(strings.TrimSpace(c.PostRoll))
	if err != nil || d <= 0 {
		return 30 * time.Second
	}
	return d
}

// MaxDurationValue caps a single event session.
func (c EventConfig) MaxDurationValue() time.Duration {
	d, err := time.ParseDuration(strings.TrimSpace(c.MaxDuration))
	if err != nil || d <= 0 {
		return 5 * time.Minute
	}
	return d
}

// CameraAdaptiveConfig is per-camera sparse-recording settings.
type CameraAdaptiveConfig struct {
	TimelapseInterval string `yaml:"timelapse_interval,omitempty" json:"timelapse_interval,omitempty"`
}

// TimelapseIntervalDuration is the calm-state IDR write interval.
func (c *CameraAdaptiveConfig) TimelapseIntervalDuration() time.Duration {
	if c == nil {
		return 30 * time.Second
	}
	d, err := time.ParseDuration(strings.TrimSpace(c.TimelapseInterval))
	if err != nil || d < 5*time.Second {
		return 30 * time.Second
	}
	if d > 10*time.Minute {
		return 10 * time.Minute
	}
	return d
}

func applyP0Defaults(cfg *Config) {
	if strings.TrimSpace(cfg.AutoDiscover.ScanInterval) == "" {
		cfg.AutoDiscover.ScanInterval = "60s"
	}
	if cfg.Health.Rediscovery.MaxParallel <= 0 {
		cfg.Health.Rediscovery.MaxParallel = 16
	}
	if strings.TrimSpace(cfg.Health.Rediscovery.ProbeTimeout) == "" {
		cfg.Health.Rediscovery.ProbeTimeout = "2s"
	}
	if strings.TrimSpace(cfg.Event.PostRoll) == "" {
		cfg.Event.PostRoll = "30s"
	}
	if strings.TrimSpace(cfg.Event.MaxDuration) == "" {
		cfg.Event.MaxDuration = "5m"
	}
	if cfg.Streaming.PreviewAutoStopSec == 0 {
		cfg.Streaming.PreviewAutoStopSec = 60
	}
}

func validateP0(cfg *Config) error {
	if cfg.AutoDiscover.IsEnabled() {
		if _, err := time.ParseDuration(cfg.AutoDiscover.ScanInterval); err != nil {
			return fmt.Errorf("auto_discover.scan_interval invalid: %w", err)
		}
	}
	if cfg.Health.Rediscovery.IsEnabled() {
		if cfg.Health.Rediscovery.MaxParallel < 1 {
			return fmt.Errorf("health.rediscovery.max_parallel must be >= 1")
		}
		if _, err := time.ParseDuration(cfg.Health.Rediscovery.ProbeTimeout); err != nil {
			return fmt.Errorf("health.rediscovery.probe_timeout invalid: %w", err)
		}
	}
	if _, err := time.ParseDuration(cfg.Event.PostRoll); err != nil {
		return fmt.Errorf("event.post_roll invalid: %w", err)
	}
	if _, err := time.ParseDuration(cfg.Event.MaxDuration); err != nil {
		return fmt.Errorf("event.max_duration invalid: %w", err)
	}
	return nil
}
