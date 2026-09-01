package health

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
)

// RestartRecorderFunc is the function signature for restarting a camera recorder.
// Injected to avoid circular dependency on internal/camera.
type RestartRecorderFunc func(ctx context.Context, cameraID string) error

// RediscoverFunc attempts to find a camera by serial and reconnect it.
type RediscoverFunc func(ctx context.Context, cameraID string) (found bool, err error)

// IsCameraEnabledFunc checks whether a camera is enabled for auto-remediation.
type IsCameraEnabledFunc func(cameraID string) bool

// cameraRestartState tracks per-camera restart history and blacklist status.
type cameraRestartState struct {
	attempts            []time.Time
	blacklistedSince    time.Time
	rediscoverAttempted bool
}

// AutoRemediator decides whether to automatically restart a failed camera recorder.
// It triggers immediately on StatusError, and also on a recorder that has stayed
// StatusReconnecting/StatusOffline for longer than OfflineRestartSeconds — which
// catches "stuck" streams the reconnect loop can never recover on its own (e.g. the
// source encoding changed mid-stream). It enforces safety rules: per-camera rate
// limiting, cooldown, global rate limiting, and blacklisting.
type AutoRemediator struct {
	cfg          config.HealthAutoRemediationConfig
	restartFn    RestartRecorderFunc
	isEnabledFn  IsCameraEnabledFunc
	rediscoverFn RediscoverFunc

	mu             sync.Mutex
	cameraStates   map[string]*cameraRestartState
	globalRestarts []time.Time
	// unhealthySince tracks when a camera first entered a sustained-unhealthy
	// state (reconnecting/offline). Cleared when it recovers or is restarted.
	unhealthySince map[string]time.Time
}

// NewAutoRemediator creates a new AutoRemediator with the given config and injected functions.
func NewAutoRemediator(cfg config.HealthAutoRemediationConfig, restartFn RestartRecorderFunc, isEnabledFn IsCameraEnabledFunc) *AutoRemediator {
	return &AutoRemediator{
		cfg:            cfg,
		restartFn:      restartFn,
		isEnabledFn:    isEnabledFn,
		cameraStates:   make(map[string]*cameraRestartState),
		globalRestarts: make([]time.Time, 0),
		unhealthySince: make(map[string]time.Time),
	}
}

// Check evaluates whether a camera should be auto-restarted based on its status.
// Returns nil if restart was triggered, or an error explaining why it was not.
func (r *AutoRemediator) Check(cameraID string, status string) error {
	// Safety check 0: feature must be enabled.
	if !r.cfg.Enabled {
		return nil
	}

	// Safety check 1: camera must be enabled.
	if r.isEnabledFn != nil && !r.isEnabledFn(cameraID) {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()

	// Safety check 2: status must be actionable.
	switch status {
	case string(model.StatusError):
		// Recorder gave up — act immediately.
		delete(r.unhealthySince, cameraID)
	case string(model.StatusReconnecting), string(model.StatusOffline):
		// A stuck stream the reconnect loop can never recover on its own (e.g.
		// the source video encoding changed mid-stream and the codec-locked
		// recorder can't reattach). Only act once it has stayed unhealthy for
		// OfflineRestartSeconds, so transient reconnects and brief camera
		// reboots heal without intervention.
		since, ok := r.unhealthySince[cameraID]
		if !ok {
			r.unhealthySince[cameraID] = now
			return nil
		}
		if now.Sub(since) < time.Duration(r.cfg.OfflineRestartSeconds)*time.Second {
			return nil
		}
	default:
		// Healthy or intentional state (recording/paused/stopped) — never restart.
		delete(r.unhealthySince, cameraID)
		return nil
	}

	state := r.getOrCreateState(cameraID)

	// Safety check 3: not blacklisted.
	if !state.blacklistedSince.IsZero() {
		blacklistExpiry := state.blacklistedSince.Add(time.Duration(r.cfg.BlacklistHours) * time.Hour)
		if now.Before(blacklistExpiry) {
			if r.rediscoverFn != nil && !state.rediscoverAttempted {
				state.rediscoverAttempted = true
				r.mu.Unlock()
				found, err := r.rediscoverFn(context.Background(), cameraID)
				r.mu.Lock()
				if found {
					state.blacklistedSince = time.Time{}
					state.attempts = nil
					state.rediscoverAttempted = false
					delete(r.unhealthySince, cameraID)
					return nil
				}
				if err != nil {
					slog.Warn("rediscovery after blacklist failed", "camera_id", cameraID, "error", err)
				}
			}
			return fmt.Errorf("camera %s is blacklisted until %s", cameraID, blacklistExpiry.Format(time.RFC3339))
		}
		// Blacklist expired — reset state.
		state.blacklistedSince = time.Time{}
		state.attempts = nil
		state.rediscoverAttempted = false
	}

	// Safety check 4: per-camera rate limit (count attempts in last hour).
	recentAttempts := filterRecent(state.attempts, now, time.Hour)
	if len(recentAttempts) >= r.cfg.MaxRestartsPerHour {
		return fmt.Errorf("camera %s exceeded max restarts per hour (%d)", cameraID, r.cfg.MaxRestartsPerHour)
	}

	// Safety check 5: cooldown after last attempt.
	if len(recentAttempts) > 0 {
		lastAttempt := recentAttempts[len(recentAttempts)-1]
		cooldownEnd := lastAttempt.Add(time.Duration(r.cfg.CooldownMinutes) * time.Minute)
		if now.Before(cooldownEnd) {
			return fmt.Errorf("camera %s is in cooldown until %s", cameraID, cooldownEnd.Format(time.RFC3339))
		}
	}

	// Safety check 6: global rate limit.
	recentGlobal := filterRecent(r.globalRestarts, now, time.Minute)
	if len(recentGlobal) >= r.cfg.GlobalMaxPerMin {
		return fmt.Errorf("global restart rate limit exceeded (%d/min)", r.cfg.GlobalMaxPerMin)
	}

	// All checks passed — record attempt and trigger restart.
	state.attempts = append(state.attempts, now)
	r.globalRestarts = append(r.globalRestarts, now)
	// Re-arm sustained-unhealthy tracking; if the restart doesn't recover the
	// stream, the next cycle starts counting toward another attempt afresh.
	delete(r.unhealthySince, cameraID)

	// Check if this attempt triggers blacklisting.
	updatedRecent := filterRecent(state.attempts, now, time.Hour)
	if len(updatedRecent) >= r.cfg.MaxRestartsPerHour {
		state.blacklistedSince = now
	}

	// Release lock before calling restartFn (which may be slow).
	r.mu.Unlock()
	err := r.restartFn(context.Background(), cameraID)
	r.mu.Lock() // re-acquire for deferred unlock

	return err
}

// IsBlacklisted returns whether a camera is currently blacklisted from auto-remediation.
func (r *AutoRemediator) IsBlacklisted(cameraID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	state, ok := r.cameraStates[cameraID]
	if !ok || state.blacklistedSince.IsZero() {
		return false
	}

	blacklistExpiry := state.blacklistedSince.Add(time.Duration(r.cfg.BlacklistHours) * time.Hour)
	return time.Now().Before(blacklistExpiry)
}

// SetRediscoverer injects IP self-healing used once when a camera is blacklisted.
func (r *AutoRemediator) SetRediscoverer(fn RediscoverFunc) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rediscoverFn = fn
}

// ClearBlacklist removes a camera from the auto-remediation blacklist.
func (r *AutoRemediator) ClearBlacklist(cameraID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if state, ok := r.cameraStates[cameraID]; ok {
		state.blacklistedSince = time.Time{}
		state.attempts = nil
		state.rediscoverAttempted = false
	}
	delete(r.unhealthySince, cameraID)
}

// CheckAll evaluates all cameras in the given status map and attempts remediation
// for those that need it. Errors for individual cameras are logged but do not
// prevent processing of other cameras.
func (r *AutoRemediator) CheckAll(statuses map[string]string) {
	for cameraID, status := range statuses {
		if err := r.Check(cameraID, status); err != nil {
			slog.Warn("auto-remediate skipped", "camera_id", cameraID, "error", err)
		}
	}
}

// getOrCreateState returns the restart state for a camera, creating it if needed.
// Caller must hold r.mu.
func (r *AutoRemediator) getOrCreateState(cameraID string) *cameraRestartState {
	state, ok := r.cameraStates[cameraID]
	if !ok {
		state = &cameraRestartState{}
		r.cameraStates[cameraID] = state
	}
	return state
}

// filterRecent returns only timestamps within the given duration from now.
func filterRecent(times []time.Time, now time.Time, window time.Duration) []time.Time {
	cutoff := now.Add(-window)
	recent := make([]time.Time, 0, len(times))
	for _, t := range times {
		if !t.Before(cutoff) {
			recent = append(recent, t)
		}
	}
	return recent
}
