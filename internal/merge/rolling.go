package merge

import (
	"context"
	"sync"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/event"
)

// RollingCoordinator debounce-merges closed segments into the current UTC hour bucket.
type RollingCoordinator struct {
	mgr          *MergeManager
	getGlobalCfg func() config.MergeConfig
	bus          *event.EventBus

	mu      sync.Mutex
	timers  map[string]*time.Timer
	pending map[string]time.Time
}

func NewRollingCoordinator(mgr *MergeManager, getGlobalCfg func() config.MergeConfig, bus *event.EventBus) *RollingCoordinator {
	return &RollingCoordinator{
		mgr:          mgr,
		getGlobalCfg: getGlobalCfg,
		bus:          bus,
		timers:       make(map[string]*time.Timer),
		pending:      make(map[string]time.Time),
	}
}

// Start subscribes to segment.completed and runs until ctx is done.
func (c *RollingCoordinator) Start(ctx context.Context) {
	if c.bus == nil || c.mgr == nil {
		return
	}
	ch := make(chan event.Event, 64)
	if err := c.bus.Subscribe(event.TopicSegmentCompleted, ch, 64); err != nil {
		logger.Warn("rolling merge subscribe failed", "error", err)
		return
	}
	defer c.bus.Unsubscribe(event.TopicSegmentCompleted, ch)

	logger.Info("rolling merge started")
	for {
		select {
		case <-ctx.Done():
			c.stopTimers()
			return
		case ev, ok := <-ch:
			if !ok {
				c.stopTimers()
				return
			}
			sc, ok := ev.Data.(event.SegmentCompleted)
			if !ok || sc.CameraID == "" {
				continue
			}
			cfg := c.getGlobalCfg()
			if c.mgr != nil {
				cfg = config.ResolveMergeConfig(cfg, c.mgr.getCameraCfg(sc.CameraID))
			}
			if !cfg.IsRollingEnabled() {
				continue
			}
			at := parseSegmentTime(sc.StartedAt)
			if at.IsZero() {
				at = time.Now()
			}
			c.schedule(ctx, sc.CameraID, at, cfg.RollingDebounceDuration())
		}
	}
}

func (c *RollingCoordinator) stopTimers() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, t := range c.timers {
		t.Stop()
		delete(c.timers, id)
	}
	c.pending = make(map[string]time.Time)
}

func (c *RollingCoordinator) schedule(ctx context.Context, cameraID string, at time.Time, debounce time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending[cameraID] = at
	if existing, ok := c.timers[cameraID]; ok {
		existing.Reset(debounce)
		return
	}
	c.timers[cameraID] = time.AfterFunc(debounce, func() {
		c.mu.Lock()
		when := c.pending[cameraID]
		delete(c.timers, cameraID)
		delete(c.pending, cameraID)
		c.mu.Unlock()
		if err := c.mgr.MergeHour(ctx, cameraID, when); err != nil {
			logger.Warn("rolling merge failed", "camera_id", cameraID, "error", err)
		}
	})
}

func parseSegmentTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
