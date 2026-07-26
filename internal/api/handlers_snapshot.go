package api

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
)

const snapshotCacheTTL = 10 * time.Second

// handleGetSnapshot returns the latest snapshot for a camera.
func (h *Handler) handleGetSnapshot(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "camera_id")
	if cameraID == "" {
		writeError(w, http.StatusBadRequest, "camera_id is required")
		return
	}
	h.serveCameraSnapshot(w, r, cameraID)
}

func (h *Handler) serveCameraSnapshot(w http.ResponseWriter, r *http.Request, cameraID string) {
	// Try snapshot manager first
	if h.snapshotMgr != nil {
		path := h.snapshotMgr.GetSnapshotPath(cameraID)
		if path != "" {
			http.ServeFile(w, r, path)
			return
		}
	}

	if data, ok := h.getCachedSnapshot(cameraID, snapshotCacheTTL); ok {
		serveSnapshotBytes(w, data, "max-age=5")
		return
	}

	cam := h.getSnapshotCameraConfig(cameraID)
	if cam == nil {
		writeError(w, http.StatusNotFound, "camera not found")
		return
	}

	var lastErr error
	if cam.SnapshotURL != "" {
		data, err := fetchRemoteSnapshot(r.Context(), cam.SnapshotURL, cam.Username, cam.Password)
		if err == nil {
			h.setCachedSnapshot(cameraID, data)
			serveSnapshotBytes(w, data, "max-age=5")
			return
		}
		lastErr = err
	}

	if cam.ONVIFEndpoint != "" && h.camMgr != nil {
		data, err := h.fetchONVIFSnapshot(r.Context(), cameraID, cam)
		if err == nil {
			h.setCachedSnapshot(cameraID, data)
			serveSnapshotBytes(w, data, "max-age=5")
			return
		}
		lastErr = err
	}

	data, err := h.captureStreamSnapshot(r.Context(), cameraID)
	if err == nil {
		h.setCachedSnapshot(cameraID, data)
		serveSnapshotBytes(w, data, "max-age=5")
		return
	}
	lastErr = err

	if data, ok := h.getCachedSnapshot(cameraID, 0); ok {
		w.Header().Set("X-Cache", "stale")
		serveSnapshotBytes(w, data, "no-cache")
		return
	}
	if lastErr != nil {
		logger.Debug("snapshot fallback failed", "camera_id", cameraID, "error", lastErr)
	}
	writeError(w, http.StatusNotFound, "snapshot not found")
}

func (h *Handler) getSnapshotCameraConfig(cameraID string) *config.CameraConfig {
	if h.camMgr != nil {
		if cam := h.camMgr.GetCameraConfig(cameraID); cam != nil {
			return cam
		}
	}
	if h.config == nil {
		return nil
	}
	for i := range h.config.Cameras {
		if h.config.Cameras[i].ID == cameraID {
			return &h.config.Cameras[i]
		}
	}
	return nil
}

func (h *Handler) getCachedSnapshot(cameraID string, ttl time.Duration) ([]byte, bool) {
	h.snapshotMu.RLock()
	cached, ok := h.snapshots[cameraID]
	h.snapshotMu.RUnlock()
	if !ok || cached == nil || len(cached.data) == 0 {
		return nil, false
	}
	if ttl > 0 && time.Since(cached.timestamp) > ttl {
		return nil, false
	}
	return cached.data, true
}

func (h *Handler) setCachedSnapshot(cameraID string, data []byte) {
	if len(data) == 0 {
		return
	}
	h.snapshotMu.Lock()
	h.snapshots[cameraID] = &snapshotCache{data: data, timestamp: time.Now()}
	h.snapshotMu.Unlock()
}

func serveSnapshotBytes(w http.ResponseWriter, data []byte, cacheControl string) {
	w.Header().Set("Content-Type", "image/jpeg")
	if cacheControl != "" {
		w.Header().Set("Cache-Control", cacheControl)
	}
	_, _ = w.Write(data)
}

func fetchRemoteSnapshot(ctx context.Context, rawURL, username, password string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if username != "" {
		req.SetBasicAuth(username, password)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
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
	if len(data) == 0 {
		return nil, fmt.Errorf("empty snapshot response")
	}
	return data, nil
}

func (h *Handler) fetchONVIFSnapshot(ctx context.Context, cameraID string, cam *config.CameraConfig) ([]byte, error) {
	provider, err := h.camMgr.GetSnapshotProvider(ctx, cameraID)
	if err != nil {
		return nil, err
	}
	uri, err := provider.GetSnapshotUri(ctx)
	if err != nil {
		return nil, err
	}
	return fetchRemoteSnapshot(ctx, uri, cam.Username, cam.Password)
}

func (h *Handler) captureStreamSnapshot(ctx context.Context, cameraID string) ([]byte, error) {
	if h.mediaEngine == nil {
		return nil, fmt.Errorf("media engine not available")
	}
	ffmpegPath, err := h.resolveSnapshotFFmpegPath()
	if err != nil {
		return nil, err
	}
	for _, protocol := range []string{"rtsp", "flv"} {
		playURL, err := h.mediaEngine.BuildPlayURL(ctx, media.PlayURLRequest{
			StreamID: cameraID,
			AppName:  "live",
			Protocol: protocol,
		})
		if err != nil || playURL == nil || playURL.URL == "" {
			continue
		}
		data, err := captureSnapshotWithFFmpeg(ctx, ffmpegPath, playURL.URL)
		if err == nil {
			return data, nil
		}
	}
	return nil, fmt.Errorf("stream snapshot failed")
}

func (h *Handler) resolveSnapshotFFmpegPath() (string, error) {
	if path, err := exec.LookPath("ffmpeg"); err == nil {
		return path, nil
	}
	return "", fmt.Errorf("ffmpeg not available for stream snapshot fallback")
}

func captureSnapshotWithFFmpeg(ctx context.Context, ffmpegPath, inputURL string) ([]byte, error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()

	args := []string{"-hide_banner", "-loglevel", "warning"}
	if strings.HasPrefix(inputURL, "rtsp://") {
		args = append(args, "-rtsp_transport", "tcp")
	}
	args = append(args,
		"-i", inputURL,
		"-an",
		"-frames:v", "1",
		"-f", "image2",
		"-vcodec", "mjpeg",
		"pipe:1",
	)

	var stderr bytes.Buffer
	cmd := exec.CommandContext(timeoutCtx, ffmpegPath, args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return nil, fmt.Errorf("ffmpeg snapshot failed: %w: %s", err, msg)
		}
		return nil, fmt.Errorf("ffmpeg snapshot failed: %w", err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("ffmpeg snapshot returned empty output")
	}
	return out, nil
}

// handleGetLatestSnapshot returns the latest snapshot file path.
func (h *Handler) handleGetLatestSnapshot(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "camera_id")
	if cameraID == "" {
		writeError(w, http.StatusBadRequest, "camera_id is required")
		return
	}

	if h.snapshotMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "snapshot manager not available")
		return
	}

	path := h.snapshotMgr.GetSnapshotPath(cameraID)
	if path == "" {
		writeError(w, http.StatusNotFound, "snapshot not found")
		return
	}

	// Check if file exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		writeError(w, http.StatusNotFound, "snapshot file not found")
		return
	}

	// Return file info
	info, _ := os.Stat(path)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"camera_id": cameraID,
		"path":      path,
		"size":      info.Size(),
		"mod_time":  info.ModTime(),
		"url":       h.snapshotMgr.GetSnapshotURL(cameraID),
	})
}

// handleTakeSnapshot takes an immediate snapshot from a camera.
func (h *Handler) handleTakeSnapshot(w http.ResponseWriter, r *http.Request) {
	cameraID := chi.URLParam(r, "camera_id")
	if cameraID == "" {
		writeError(w, http.StatusBadRequest, "camera_id is required")
		return
	}

	if h.snapshotMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "snapshot manager not available")
		return
	}

	// Get camera config
	if h.camMgr == nil {
		writeError(w, http.StatusServiceUnavailable, "camera manager not available")
		return
	}

	cam := h.camMgr.GetCameraConfig(cameraID)
	if cam == nil {
		writeError(w, http.StatusNotFound, "camera not found")
		return
	}

	// Take snapshot based on camera type
	var data []byte
	var err error

	// Try ONVIF first
	if cam.ONVIFEndpoint != "" {
		// Use existing ONVIF snapshot handler
		snapshotURI := "/api/cameras/" + cameraID + "/snapshot/uri"
		// Redirect to internal handler
		r2 := r.Clone(r.Context())
		r2.URL.Path = snapshotURI
		h.handleSnapshotGetUri(w, r2)
		return
	}

	// Try snapshot URL
	if cam.SnapshotURL != "" {
		client := &http.Client{Timeout: 10 * 1000000000}
		resp, err := client.Get(cam.SnapshotURL)
		if err == nil && resp.StatusCode == http.StatusOK {
			defer resp.Body.Close()
			h.logSuccess(r, "snapshot.take", "camera", cameraID, "snapshot taken", nil)
			w.Header().Set("Content-Type", "image/jpeg")
			w.Header().Set("Cache-Control", "no-cache")
			buf := make([]byte, 32*1024)
			for {
				n, readErr := resp.Body.Read(buf)
				if n > 0 {
					w.Write(buf[:n])
				}
				if readErr != nil {
					break
				}
			}
			return
		}
	}

	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to take snapshot: "+err.Error())
		return
	}

	if data == nil {
		writeError(w, http.StatusNotFound, "unable to take snapshot")
		return
	}
}

// GetSnapshotDir returns the snapshot directory path.
func GetSnapshotDir(rootDir string) string {
	return filepath.Join(rootDir, "snapshots")
}
