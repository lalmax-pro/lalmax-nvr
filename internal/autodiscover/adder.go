package autodiscover

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/event"
	"github.com/lalmax-pro/lalmax-nvr/internal/onvif"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

const dedupWindow = 5 * time.Minute

// Adder classifies discovered ONVIF devices and enrolls or updates cameras.
type Adder struct {
	cfg      config.AutoDiscoverConfig
	enroller AdderEnroller
	db       *storage.DB
	bus      *event.EventBus
	infoFn   DeviceInfoFunc

	mu    sync.Mutex
	seen  map[string]time.Time
}

func NewAdder(cfg config.AutoDiscoverConfig, enroller AdderEnroller, db *storage.DB, bus *event.EventBus, infoFn DeviceInfoFunc) *Adder {
	if infoFn == nil {
		infoFn = defaultDeviceInfo
	}
	return &Adder{
		cfg:      cfg,
		enroller: enroller,
		db:       db,
		bus:      bus,
		infoFn:   infoFn,
		seen:     make(map[string]time.Time),
	}
}

func defaultDeviceInfo(ctx context.Context, endpoint, username, password string) (*onvif.DeviceInfo, error) {
	client := onvif.NewClient(endpoint, username, password)
	if err := client.Connect(ctx); err != nil {
		return nil, err
	}
	return client.GetDeviceInformation(ctx)
}

func (a *Adder) HandleDiscovered(ctx context.Context, d onvif.DiscoveredDevice) {
	endpoint := normalizeEndpoint(d.Endpoint)
	if endpoint == "" {
		return
	}
	key := endpoint + "|" + d.UUID
	now := time.Now()
	a.mu.Lock()
	if t, ok := a.seen[key]; ok && now.Sub(t) < dedupWindow {
		a.mu.Unlock()
		return
	}
	a.seen[key] = now
	a.mu.Unlock()

	info, profilesOK := a.enrich(ctx, endpoint)
	serial := ""
	brand, model := "", ""
	if info != nil {
		serial = strings.TrimSpace(info.SerialNumber)
		brand = info.Manufacturer
		model = info.Model
	}

	if a.db != nil {
		if serial != "" {
			if existing, err := a.db.FindCameraBySerial(ctx, serial); err == nil && existing != nil {
				a.handleExisting(ctx, existing, endpoint)
				return
			}
		}
		if existing, err := a.db.FindCameraByEndpoint(ctx, endpoint); err == nil && existing != nil {
			return
		}
	}

	name := d.Name
	if name == "" {
		name = d.Hardware
	}
	if name == "" {
		name = "ONVIF " + shortID(d.UUID, endpoint)
	}

	cam := config.CameraConfig{
		Name:             name,
		Protocol:         "onvif",
		URL:              endpoint,
		ONVIFEndpoint:    endpoint,
		Enabled:          true,
		Username:         a.cfg.DefaultUsername,
		Password:         a.cfg.DefaultPassword,
		ActivationState:  config.ActivationActive,
		StableID:         serial,
		RecordingMode:    storage.RecordingModeContinuous,
	}

	if !profilesOK {
		cam.ActivationState = config.ActivationPending
		cam.Username = ""
		cam.Password = ""
		cam.Enabled = true
	}

	id, err := a.enroller.AddCamera(ctx, cam)
	if err != nil {
		logger.Warn("auto-enroll failed", "endpoint", endpoint, "error", err)
		return
	}
	if a.db != nil {
		_ = a.db.UpdateCameraMetadata(ctx, id, "", "", brand, model, serial, 0)
		if serial != "" {
			_ = a.db.UpdateCameraStableID(ctx, id, serial)
		}
		_ = a.db.UpdateCameraActivation(ctx, id, cam.ActivationState)
	}
	logger.Info("auto-enrolled onvif camera", "camera_id", id, "endpoint", endpoint, "activation", cam.ActivationState)
	if a.bus != nil {
		a.bus.Publish(ctx, TopicCameraAdded, map[string]any{
			"camera_id":        id,
			"source":           "auto",
			"activation_state": cam.ActivationState,
			"name":             name,
		})
	}
}

func (a *Adder) handleExisting(ctx context.Context, existing *storage.CameraRow, endpoint string) {
	if existing.Archived {
		return
	}
	cur := normalizeEndpoint(existing.ONVIFEndpoint)
	if cur == "" {
		cur = normalizeEndpoint(existing.URL)
	}
	if cur == endpoint {
		return
	}
	if err := a.enroller.UpdateONVIFEndpoint(ctx, existing.ID, endpoint); err != nil {
		logger.Warn("failed to update roaming endpoint", "camera_id", existing.ID, "error", err)
		return
	}
	if err := a.enroller.RestartRecorder(ctx, existing.ID); err != nil {
		logger.Warn("failed to restart after roaming update", "camera_id", existing.ID, "error", err)
	}
	logger.Info("updated onvif camera endpoint after roam", "camera_id", existing.ID, "endpoint", endpoint)
}

func (a *Adder) enrich(ctx context.Context, endpoint string) (*onvif.DeviceInfo, bool) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	info, err := a.infoFn(ctx, endpoint, a.cfg.DefaultUsername, a.cfg.DefaultPassword)
	if err == nil && info != nil {
		return info, a.cfg.DefaultUsername == "" || a.canGetProfiles(ctx, endpoint)
	}
	if a.cfg.DefaultUsername == "" && a.cfg.DefaultPassword == "" {
		info, err = a.infoFn(ctx, endpoint, "", "")
		if err == nil && info != nil {
			return info, true
		}
	}
	return info, false
}

func (a *Adder) canGetProfiles(ctx context.Context, endpoint string) bool {
	client := onvif.NewClient(endpoint, a.cfg.DefaultUsername, a.cfg.DefaultPassword)
	if err := client.Connect(ctx); err != nil {
		return false
	}
	profiles, err := client.GetProfiles(ctx)
	return err == nil && len(profiles) > 0
}

func shortID(uuid, endpoint string) string {
	if uuid != "" {
		parts := strings.Split(uuid, ":")
		id := parts[len(parts)-1]
		if len(id) > 8 {
			return id[:8]
		}
		return id
	}
	return endpoint
}
