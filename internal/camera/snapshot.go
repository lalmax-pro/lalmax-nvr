package camera

import (
	"bytes"
	"context"
	"fmt"
	"image/jpeg"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/onvif"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

var snapshotLogger = slog.Default().With("component", "snapshot")

// SnapshotConfig holds snapshot configuration.
type SnapshotConfig struct {
	Enabled  bool   `yaml:"enabled" json:"enabled"`
	Interval string `yaml:"interval" json:"interval"` // e.g., "5m", "1m", "10m"
	Quality  int    `yaml:"quality" json:"quality"`   // JPEG quality 1-100
	MaxAge   string `yaml:"max_age" json:"max_age"`   // max age before cleanup
}

// DefaultSnapshotConfig returns default snapshot configuration.
func DefaultSnapshotConfig() SnapshotConfig {
	return SnapshotConfig{
		Enabled:  false,
		Interval: "5m",
		Quality:  80,
		MaxAge:   "24h",
	}
}

// SnapshotManager manages periodic snapshots from cameras.
type SnapshotManager struct {
	cfg         SnapshotConfig
	store       *storage.Manager
	mediaEngine media.Engine
	interval    time.Duration
	quality     int
	maxAge      time.Duration
	mu          sync.RWMutex
	stopCh      chan struct{}
	running     bool
}

// NewSnapshotManager creates a new snapshot manager.
func NewSnapshotManager(cfg SnapshotConfig, store *storage.Manager, mediaEngine media.Engine) (*SnapshotManager, error) {
	interval, err := time.ParseDuration(cfg.Interval)
	if err != nil {
		interval = 5 * time.Minute
	}
	if interval < time.Minute {
		interval = time.Minute
	}

	maxAge, err := time.ParseDuration(cfg.MaxAge)
	if err != nil {
		maxAge = 24 * time.Hour
	}

	quality := cfg.Quality
	if quality < 1 || quality > 100 {
		quality = 80
	}

	return &SnapshotManager{
		cfg:         cfg,
		store:       store,
		mediaEngine: mediaEngine,
		interval:    interval,
		quality:     quality,
		maxAge:      maxAge,
		stopCh:      make(chan struct{}),
	}, nil
}

// Start starts the snapshot manager.
func (sm *SnapshotManager) Start(ctx context.Context) error {
	if !sm.cfg.Enabled {
		snapshotLogger.Info("snapshot manager disabled")
		return nil
	}

	sm.mu.Lock()
	if sm.running {
		sm.mu.Unlock()
		return nil
	}
	sm.running = true
	sm.mu.Unlock()

	snapshotLogger.Info("snapshot manager started",
		"interval", sm.interval,
		"quality", sm.quality,
		"maxAge", sm.maxAge)

	// Start snapshot goroutine
	go sm.run(ctx)

	return nil
}

// Stop stops the snapshot manager.
func (sm *SnapshotManager) Stop() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if !sm.running {
		return
	}

	close(sm.stopCh)
	sm.running = false
	snapshotLogger.Info("snapshot manager stopped")
}

// run runs the snapshot loop.
func (sm *SnapshotManager) run(ctx context.Context) {
	// Initial snapshot after 10 seconds
	initialTimer := time.NewTimer(10 * time.Second)
	defer initialTimer.Stop()

	select {
	case <-initialTimer.C:
		sm.takeAllSnapshots(ctx)
	case <-sm.stopCh:
		return
	case <-ctx.Done():
		return
	}

	// Periodic snapshots
	ticker := time.NewTicker(sm.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			sm.takeAllSnapshots(ctx)
			sm.cleanupOldSnapshots(ctx)
		case <-sm.stopCh:
			return
		case <-ctx.Done():
			return
		}
	}
}

// takeAllSnapshots takes snapshots from all configured cameras.
func (sm *SnapshotManager) takeAllSnapshots(ctx context.Context) {
	// Get all cameras from config
	cameras := sm.getConfiguredCameras()

	var wg sync.WaitGroup
	for _, cam := range cameras {
		if !cam.Enabled {
			continue
		}

		wg.Add(1)
		go func(cam config.CameraConfig) {
			defer wg.Done()
			sm.takeCameraSnapshot(ctx, cam)
		}(cam)
	}

	wg.Wait()
}

// getConfiguredCameras returns all configured cameras.
func (sm *SnapshotManager) getConfiguredCameras() []config.CameraConfig {
	// This should get cameras from config
	// For now, return empty - will be populated from CameraManager
	return nil
}

// takeCameraSnapshot takes a snapshot from a single camera.
func (sm *SnapshotManager) takeCameraSnapshot(ctx context.Context, cam config.CameraConfig) {
	var data []byte
	var err error

	// Method 1: Try ONVIF snapshot (if camera supports it)
	if cam.ONVIFEndpoint != "" {
		data, err = sm.takeONVIFSnapshot(ctx, cam)
		if err == nil && len(data) > 0 {
			sm.saveSnapshot(cam.ID, data, "onvif")
			return
		}
		snapshotLogger.Debug("ONVIF snapshot failed, trying stream", "camera_id", cam.ID, "error", err)
	}

	// Method 2: Try camera's snapshot URL
	if cam.SnapshotURL != "" {
		data, err = sm.takeURLSnapshot(cam.SnapshotURL)
		if err == nil && len(data) > 0 {
			sm.saveSnapshot(cam.ID, data, "url")
			return
		}
		snapshotLogger.Debug("URL snapshot failed, trying stream", "camera_id", cam.ID, "error", err)
	}

	// Method 3: Take snapshot from live stream
	data, err = sm.takeStreamSnapshot(ctx, cam.ID)
	if err == nil && len(data) > 0 {
		sm.saveSnapshot(cam.ID, data, "stream")
		return
	}

	snapshotLogger.Debug("all snapshot methods failed", "camera_id", cam.ID)
}

// takeONVIFSnapshot takes a snapshot using ONVIF protocol.
func (sm *SnapshotManager) takeONVIFSnapshot(ctx context.Context, cam config.CameraConfig) ([]byte, error) {
	// Create ONVIF client
	client := onvif.NewClient(cam.ONVIFEndpoint, cam.Username, cam.Password)

	// Connect to camera
	if err := client.Connect(ctx); err != nil {
		return nil, fmt.Errorf("connect to ONVIF camera: %w", err)
	}

	// Get snapshot provider
	profileToken := cam.ProfileToken
	if profileToken == "" {
		profileToken = "token_0" // Default profile
	}

	provider := client.NewSnapshotProvider(profileToken)
	uri, err := provider.GetSnapshotUri(ctx)
	if err != nil {
		return nil, fmt.Errorf("get snapshot URI: %w", err)
	}

	// Fetch snapshot from URI
	return sm.takeURLSnapshot(uri)
}

// takeURLSnapshot takes a snapshot from a URL.
func (sm *SnapshotManager) takeURLSnapshot(url string) ([]byte, error) {
	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024)) // 10MB max
	if err != nil {
		return nil, err
	}

	return data, nil
}

// takeStreamSnapshot takes a snapshot from a live stream.
func (sm *SnapshotManager) takeStreamSnapshot(ctx context.Context, cameraID string) ([]byte, error) {
	if sm.mediaEngine == nil {
		return nil, fmt.Errorf("media engine not available")
	}

	// Build snapshot URL from lalmax
	snapshotURL := fmt.Sprintf("http://127.0.0.1:1290/api/snap/%s", cameraID)

	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	resp, err := client.Get(snapshotURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, err
	}

	return data, nil
}

// saveSnapshot saves snapshot data to file.
func (sm *SnapshotManager) saveSnapshot(cameraID string, data []byte, method string) error {
	// Create snapshot directory
	snapshotDir := filepath.Join(sm.store.RootDir(), "snapshots")
	if err := os.MkdirAll(snapshotDir, 0755); err != nil {
		return fmt.Errorf("create snapshot dir: %w", err)
	}

	// Optimize image
	optimizedData, err := sm.optimizeImage(data)
	if err != nil {
		optimizedData = data
	}

	// Save as latest snapshot
	latestPath := filepath.Join(snapshotDir, fmt.Sprintf("%s_latest.jpg", sanitizeFilename(cameraID)))
	if err := os.WriteFile(latestPath, optimizedData, 0644); err != nil {
		return fmt.Errorf("write latest snapshot: %w", err)
	}

	// Save with timestamp
	filename := fmt.Sprintf("%s_%s_%s.jpg", cameraID, method, time.Now().Format("20060102_150405"))
	filename = sanitizeFilename(filename)
	filePath := filepath.Join(snapshotDir, filename)
	if err := os.WriteFile(filePath, optimizedData, 0644); err != nil {
		snapshotLogger.Warn("failed to save timestamped snapshot", "error", err)
	}

	snapshotLogger.Debug("snapshot saved", "camera_id", cameraID, "method", method, "size", len(optimizedData))
	return nil
}

// optimizeImage optimizes JPEG image quality.
func (sm *SnapshotManager) optimizeImage(data []byte) ([]byte, error) {
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: sm.quality}); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// cleanupOldSnapshots removes snapshots older than maxAge.
func (sm *SnapshotManager) cleanupOldSnapshots(ctx context.Context) {
	snapshotDir := filepath.Join(sm.store.RootDir(), "snapshots")
	if _, err := os.Stat(snapshotDir); os.IsNotExist(err) {
		return
	}

	entries, err := os.ReadDir(snapshotDir)
	if err != nil {
		snapshotLogger.Error("failed to read snapshot dir", "error", err)
		return
	}

	cutoff := time.Now().Add(-sm.maxAge)
	var removed int

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		// Skip latest snapshots
		name := entry.Name()
		if len(name) > 7 && name[len(name)-11:] == "_latest.jpg" {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		if info.ModTime().Before(cutoff) {
			if err := os.Remove(filepath.Join(snapshotDir, name)); err != nil {
				snapshotLogger.Warn("failed to remove old snapshot", "file", name, "error", err)
			} else {
				removed++
			}
		}
	}

	if removed > 0 {
		snapshotLogger.Info("cleaned up old snapshots", "count", removed)
	}
}

// GetSnapshotPath returns the path to the latest snapshot for a camera.
func (sm *SnapshotManager) GetSnapshotPath(cameraID string) string {
	snapshotDir := filepath.Join(sm.store.RootDir(), "snapshots")
	latestPath := filepath.Join(snapshotDir, fmt.Sprintf("%s_latest.jpg", sanitizeFilename(cameraID)))

	if _, err := os.Stat(latestPath); err == nil {
		return latestPath
	}

	return ""
}

// GetSnapshotURL returns the URL to access the latest snapshot.
func (sm *SnapshotManager) GetSnapshotURL(cameraID string) string {
	return fmt.Sprintf("/api/snapshots/%s", cameraID)
}

// sanitizeFilename removes invalid characters from filename.
func sanitizeFilename(name string) string {
	replacer := []struct{ old, new string }{
		{"/", "_"},
		{"\\", "_"},
		{":", "_"},
		{"*", "_"},
		{"?", "_"},
		{"\"", "_"},
		{"<", "_"},
		{">", "_"},
		{"|", "_"},
	}

	result := name
	for _, r := range replacer {
		result = replaceAll(result, r.old, r.new)
	}

	return result
}

func replaceAll(s, old, new string) string {
	if old == "" {
		return s
	}
	result := ""
	for i := 0; i < len(s); i++ {
		if i+len(old) <= len(s) && s[i:i+len(old)] == old {
			result += new
			i += len(old) - 1
		} else {
			result += string(s[i])
		}
	}
	return result
}
