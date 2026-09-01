package autodiscover

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/event"
	"github.com/lalmax-pro/lalmax-nvr/internal/onvif"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

var logger = slog.Default().With("component", "autodiscover")

const TopicCameraAdded = "camera.added"

// AdderEnroller is implemented by camera.CameraManager for enrollment.
type AdderEnroller interface {
	AddCamera(ctx context.Context, cam config.CameraConfig) (string, error)
	UpdateONVIFEndpoint(ctx context.Context, cameraID, endpoint string) error
	RestartRecorder(ctx context.Context, cameraID string) error
}

// DeviceInfoFunc fetches ONVIF device information for enrichment.
type DeviceInfoFunc func(ctx context.Context, endpoint, username, password string) (*onvif.DeviceInfo, error)

// Service runs Hello listening and periodic Probe scans.
type Service struct {
	cfg     config.AutoDiscoverConfig
	adder   *Adder
	cancel  context.CancelFunc
	mu      sync.Mutex
	started bool
}

func New(cfg config.AutoDiscoverConfig, enroller AdderEnroller, db *storage.DB, bus *event.EventBus, infoFn DeviceInfoFunc) *Service {
	return &Service{
		cfg:   cfg,
		adder: NewAdder(cfg, enroller, db, bus, infoFn),
	}
}

func (s *Service) Start(ctx context.Context) error {
	if s == nil || !s.cfg.IsEnabled() {
		return nil
	}
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.started = true
	s.mu.Unlock()

	if s.cfg.HelloListenEnabled() {
		if err := onvif.ListenHello(ctx, s.cfg.NetworkInterface, func(d onvif.DiscoveredDevice) {
			s.adder.HandleDiscovered(ctx, d)
		}); err != nil {
			logger.Warn("hello listener failed, falling back to probe scan", "error", err)
		} else {
			logger.Info("ws-discovery hello listener started")
		}
	}

	go s.scanLoop(ctx)
	logger.Info("auto-discover started", "scan_interval", s.cfg.ScanIntervalDuration())
	return nil
}

func (s *Service) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	s.started = false
}

// Apply replaces runtime config and restarts or stops the service.
func (s *Service) Apply(ctx context.Context, cfg config.AutoDiscoverConfig) {
	if s == nil {
		return
	}
	s.Stop()
	s.mu.Lock()
	s.cfg = cfg
	if s.adder != nil {
		s.adder.cfg = cfg
	}
	s.mu.Unlock()
	_ = s.Start(ctx)
}

func (s *Service) scanLoop(ctx context.Context) {
	s.scanOnce(ctx)
	ticker := time.NewTicker(s.cfg.ScanIntervalDuration())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.scanOnce(ctx)
		}
	}
}

func (s *Service) scanOnce(ctx context.Context) {
	result := onvif.Discover(ctx, 5*time.Second)
	for _, d := range result.Devices {
		s.adder.HandleDiscovered(ctx, d)
	}
}

func normalizeEndpoint(endpoint string) string {
	return strings.TrimRight(strings.TrimSpace(endpoint), "/")
}
