package api

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/ai"
	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"
	gopsutilnet "github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
)

// --- System metrics history ---

const metricsHistoryCap = 2880 // 24h at 30s intervals

// sysMetricsHistory is a fixed-capacity ring buffer of SystemMetricSample.
type sysMetricsHistory struct {
	mu           sync.RWMutex
	samples      [metricsHistoryCap]model.SystemMetricSample
	head         int // next write position
	count        int // number of valid entries (≤ cap)
	prevCPUTotal uint64
	prevCPUIdle  uint64
	prevNetSent  uint64
	prevNetRecv  uint64
	prevTS       int64
}

func newSysMetricsHistory() *sysMetricsHistory {
	return &sysMetricsHistory{}
}

// sample collects one data point and appends it to the ring buffer.
func (h *sysMetricsHistory) sample() {
	now := time.Now().Unix()

	cpuTotal, cpuIdle, _ := readCPURaw()
	memTotal, memAvailable, _ := readMemoryInfo()
	netSent, netRecv, _ := readNetworkInfo()

	h.mu.Lock()
	defer h.mu.Unlock()

	s := model.SystemMetricSample{Timestamp: now}

	if h.prevTS > 0 {
		dt := float64(now - h.prevTS)
		if dt > 0 {
			totalDelta := cpuTotal - h.prevCPUTotal
			idleDelta := cpuIdle - h.prevCPUIdle
			if totalDelta > 0 {
				s.CPUPct = math.Round((float64(totalDelta-idleDelta)/float64(totalDelta)*100)*10) / 10
			}
			s.NetUpBps = math.Round(float64(netSent-h.prevNetSent)/dt*10) / 10
			s.NetDnBps = math.Round(float64(netRecv-h.prevNetRecv)/dt*10) / 10
		}
	}
	if memTotal > 0 {
		s.MemPct = math.Round((float64(memTotal-memAvailable)/float64(memTotal)*100)*10) / 10
	}
	s.Goroutines = runtime.NumGoroutine()

	h.prevCPUTotal = cpuTotal
	h.prevCPUIdle = cpuIdle
	h.prevNetSent = netSent
	h.prevNetRecv = netRecv
	h.prevTS = now

	h.samples[h.head] = s
	h.head = (h.head + 1) % metricsHistoryCap
	if h.count < metricsHistoryCap {
		h.count++
	}
}

// since returns all samples newer than cutoff, in chronological order.
func (h *sysMetricsHistory) since(cutoff int64) []model.SystemMetricSample {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.count == 0 {
		return []model.SystemMetricSample{}
	}

	// Determine start position in ring
	start := (h.head - h.count + metricsHistoryCap) % metricsHistoryCap
	var result []model.SystemMetricSample
	for i := 0; i < h.count; i++ {
		pos := (start + i) % metricsHistoryCap
		if h.samples[pos].Timestamp >= cutoff {
			result = append(result, h.samples[pos])
		}
	}
	if result == nil {
		return []model.SystemMetricSample{}
	}
	return result
}

// startMetricsSampler launches a background goroutine that samples every 30 seconds.
func (h *Handler) startMetricsSampler(ctx context.Context) {
	if h.sysMetrics == nil {
		return
	}
	go func() {
		// Immediate first sample so we have a baseline for delta computation
		h.sysMetrics.sample()
		// Second sample after 2s for an early CPU/net rate
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
		h.sysMetrics.sample()

		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				h.sysMetrics.sample()
			}
		}
	}()
}

// handleSystemHistory returns a time-series of system resource samples.
// Query param: period=1h|6h|24h (default 1h)
func (h *Handler) handleSystemHistory(w http.ResponseWriter, r *http.Request) {
	if h.sysMetrics == nil {
		writeJSON(w, http.StatusOK, []model.SystemMetricSample{})
		return
	}
	period := r.URL.Query().Get("period")
	var cutoff int64
	switch period {
	case "5m":
		cutoff = time.Now().Add(-5 * time.Minute).Unix()
	case "15m":
		cutoff = time.Now().Add(-15 * time.Minute).Unix()
	case "30m":
		cutoff = time.Now().Add(-30 * time.Minute).Unix()
	case "6h":
		cutoff = time.Now().Add(-6 * time.Hour).Unix()
	case "24h":
		cutoff = time.Now().Add(-24 * time.Hour).Unix()
	default: // "1h"
		cutoff = time.Now().Add(-1 * time.Hour).Unix()
	}
	writeJSON(w, http.StatusOK, h.sysMetrics.since(cutoff))
}

// handleHourlyStats returns per-hour recording activity.
// Query param: hours=24|48|168 (default 24)
func (h *Handler) handleHourlyStats(w http.ResponseWriter, r *http.Request) {
	hours := 24
	if v := r.URL.Query().Get("hours"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 720 {
			hours = n
		}
	}
	data, err := h.db.GetHourlyRecordingStats(r.Context(), hours)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get hourly stats")
		return
	}
	writeJSON(w, http.StatusOK, data)
}

// handleCameraUptimeStats returns health-event activity per camera.
// Query param: days=7|14|30 (default 7)
func (h *Handler) handleCameraUptimeStats(w http.ResponseWriter, r *http.Request) {
	days := 7
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 90 {
			days = n
		}
	}
	data, err := h.db.GetCameraUptimeStats(r.Context(), days)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get camera uptime stats")
		return
	}
	writeJSON(w, http.StatusOK, data)
}

// --- Public endpoints ---

func (h *Handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	resp := HealthResponse{Checks: make(map[string]HealthCheck)}
	hasWarning, hasError := false, false

	// Database check
	if h.db != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		err := h.db.DB().PingContext(ctx)
		if err != nil {
			resp.Checks["database"] = HealthCheck{Status: "error", Message: err.Error()}
			hasError = true
		} else {
			resp.Checks["database"] = HealthCheck{Status: "ok"}
		}
	} else {
		resp.Checks["database"] = HealthCheck{Status: "error", Message: "database not configured"}
		hasError = true
	}

	// Storage check
	if h.store != nil {
		total, used, err := h.store.GetDiskUsage()
		if err != nil {
			resp.Checks["storage"] = HealthCheck{Status: "error", Message: err.Error()}
			hasError = true
		} else {
			pct := 0
			if total > 0 {
				pct = int(float64(used) / float64(total) * 100)
			}
			msg := fmt.Sprintf("%d%% used (%d / %d bytes)", pct, used, total)
			if pct > 95 {
				resp.Checks["storage"] = HealthCheck{Status: "error", Message: msg}
				hasError = true
			} else if pct > 90 {
				resp.Checks["storage"] = HealthCheck{Status: "warning", Message: msg}
				hasWarning = true
			} else {
				resp.Checks["storage"] = HealthCheck{Status: "ok", Message: msg}
			}
		}
	} else {
		resp.Checks["storage"] = HealthCheck{Status: "error", Message: "storage not configured"}
		hasError = true
	}

	// Goroutine check
	numGoroutines := runtime.NumGoroutine()
	if numGoroutines > 1000 {
		resp.Checks["goroutines"] = HealthCheck{Status: "error", Message: fmt.Sprintf("%d goroutines (threshold: 1000)", numGoroutines)}
		hasError = true
	} else {
		resp.Checks["goroutines"] = HealthCheck{Status: "ok", Message: fmt.Sprintf("%d goroutines", numGoroutines)}
	}

	// Camera health aggregation (influences overall status)
	camHealth := h.aggregateCameraHealth(r)
	resp.Cameras = camHealth
	if camHealth != nil {
		if camHealth.Error > 0 {
			hasWarning = true // any camera in error = degraded
		}
		if camHealth.Reconnecting > 0 {
			hasWarning = true // any reconnecting = degraded
		}
		if camHealth.Total > 0 && camHealth.Offline > camHealth.Total/2 {
			hasError = true // majority offline = error
		}
	}

	// Overall status
	switch {
	case hasError:
		resp.Status = "unhealthy"
	case hasWarning:
		resp.Status = "degraded"
	default:
		resp.Status = "ok"
	}

	// Uptime
	resp.Uptime = formatUptime(time.Since(appStartTime))
	resp.StartTime = appStartTime

	// SetupRequired — true when no password is configured
	resp.SetupRequired = h.config != nil && h.config.Auth.PasswordHash == "" && h.config.Auth.Password == ""
	writeJSON(w, http.StatusOK, resp)
}

// aggregateCameraHealth builds a CameraHealthSummary from the health manager and camera DB.
func (h *Handler) aggregateCameraHealth(r *http.Request) *CameraHealthSummary {
	allHealth := h.cameraHealthWithConfiguredCameras(r)
	nameLookup := map[string]string{}
	if h.db != nil {
		cameras, err := h.db.ListCameras(r.Context())
		if err == nil {
			for _, c := range cameras {
				nameLookup[c.ID] = c.Name
			}
		}
	}

	summary := &CameraHealthSummary{Total: len(allHealth)}
	for id, ch := range allHealth {
		detail := CameraHealthDetail{
			ID:     id,
			Name:   nameLookup[id],
			Status: ch.LatestStatus,
		}
		summary.Details = append(summary.Details, detail)

		switch ch.LatestStatus {
		case "healthy", "recording":
			summary.Recording++
		case "reconnecting":
			summary.Reconnecting++
		case "error", "unhealthy":
			summary.Error++
		default:
			summary.Offline++
		}
	}

	return summary
}

// handleHealthCameras returns full camera health map with scores.
// Public endpoint — no auth required.
func (h *Handler) handleHealthCameras(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.cameraHealthWithConfiguredCameras(r))
}

func (h *Handler) cameraHealthWithConfiguredCameras(r *http.Request) map[string]*model.CameraHealth {
	health := map[string]*model.CameraHealth{}
	if h.healthMgr != nil {
		for id, ch := range h.healthMgr.GetAllHealth() {
			if ch != nil {
				health[id] = ch
			}
		}
	}
	if h.db == nil {
		return health
	}

	cameras, err := h.db.ListCameras(r.Context())
	if err != nil {
		return health
	}
	for _, cam := range cameras {
		if _, ok := health[cam.ID]; ok {
			continue
		}
		status := string(model.HealthStatusUnknown)
		if !cam.Enabled {
			status = string(model.StatusStopped)
		}
		health[cam.ID] = &model.CameraHealth{
			CameraID:     cam.ID,
			LatestStatus: status,
		}
	}
	return health
}

func (h *Handler) handleReadyz(w http.ResponseWriter, r *http.Request) {
	checks := make(map[string]HealthCheck)

	// Database must be ok
	allOK := true
	if h.db == nil {
		checks["database"] = HealthCheck{Status: "error", Message: "database not configured"}
		allOK = false
	} else {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := h.db.DB().PingContext(ctx); err != nil {
			checks["database"] = HealthCheck{Status: "error", Message: err.Error()}
			allOK = false
		} else {
			checks["database"] = HealthCheck{Status: "ok"}
		}
	}

	// Storage must be < 95%
	if h.store == nil {
		checks["storage"] = HealthCheck{Status: "error", Message: "storage not configured"}
		allOK = false
	} else {
		var total, used int64
		var err error
		if h.readyzDiskUsage != nil {
			total, used, err = h.readyzDiskUsage()
		} else {
			total, used, err = h.store.GetDiskUsage()
		}
		if err != nil {
			checks["storage"] = HealthCheck{Status: "error", Message: err.Error()}
			allOK = false
		} else {
			pct := 0
			if total > 0 {
				pct = int(float64(used) / float64(total) * 100)
			}
			if pct >= 95 {
				checks["storage"] = HealthCheck{Status: "error", Message: fmt.Sprintf("%d%% used (threshold: 95%%)", pct)}
				allOK = false
			} else {
				checks["storage"] = HealthCheck{Status: "ok"}
			}
		}
	}

	// Goroutines must be < 5000
	numGoroutines := runtime.NumGoroutine()
	if numGoroutines >= 5000 {
		checks["goroutines"] = HealthCheck{Status: "error", Message: fmt.Sprintf("%d goroutines (threshold: 5000)", numGoroutines)}
		allOK = false
	} else {
		checks["goroutines"] = HealthCheck{Status: "ok"}
	}

	if allOK {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	} else {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not ready", "checks": checks})
	}
}

func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	// Validate credentials by running through the configured auth middleware.
	// This keeps login behavior consistent with the multi-user protected routes.
	// Use httptest.ResponseRecorder to capture middleware output without writing to client w.
	done := make(chan int, 1)
	var authenticatedRequest *http.Request

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authenticatedRequest = r
		done <- http.StatusOK
	})
	rec := httptest.NewRecorder()
	loginMW := h.authMW
	if h.multiUserMW != nil {
		loginMW = h.multiUserMW
	}
	loginMW(inner).ServeHTTP(rec, r)

	select {
	case status := <-done:
		if status == http.StatusOK {
			logRequest := r
			if authenticatedRequest != nil {
				logRequest = authenticatedRequest
			}
			h.logOperation(logRequest, "auth.login", "auth", "", "success", "user login succeeded", nil)
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		}
	default:
		// Forward the middleware's captured response (503 SETUP_REQUIRED, 401, etc.)
		// without double-writing to the client.
		for k, vv := range rec.Header() {
			for _, v := range vv {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(rec.Code)
		w.Write(rec.Body.Bytes())
		h.logOperation(r, "auth.login", "auth", "", "failure", fmt.Sprintf("login failed with HTTP %d", rec.Code), nil)
	}
}

func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	h.logOperation(r, "auth.logout", "auth", "", "success", "user logout succeeded", nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) handleStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	total, used, err := h.store.GetDiskUsage()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get disk usage")
		return
	}

	count, err := h.db.CountRecordings(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to count recordings")
		return
	}

	cameras, err := h.db.ListCameras(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to count cameras")
		return
	}

	stats := model.StorageStats{
		TotalBytes:     total,
		UsedBytes:      used,
		RecordingCount: count,
		CameraCount:    len(cameras),
	}
	writeJSON(w, http.StatusOK, stats)
}

func (h *Handler) handleStatsTrends(w http.ResponseWriter, r *http.Request) {
	days := 7
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 30 {
			days = n
		}
	}
	trends, err := h.db.GetRecordingTrends(r.Context(), days)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get recording trends")
		return
	}
	writeJSON(w, http.StatusOK, trends)
}

// --- Settings endpoints ---

func (h *Handler) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	if h.config == nil {
		writeError(w, http.StatusInternalServerError, "config not available")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"cleanup": map[string]any{
			"retention_days":         h.config.Cleanup.RetentionDays,
			"check_interval":         h.config.Cleanup.CheckInterval,
			"disk_threshold_percent": h.config.Cleanup.DiskThresholdPercent,
		},
		"webdav": map[string]any{
			"enabled":     h.config.WebDAV.Enabled != nil && *h.config.WebDAV.Enabled,
			"path_prefix": h.config.WebDAV.PathPrefix,
			"read_write":  h.config.WebDAV.ReadWrite,
		},
		"auth": map[string]any{
			"username":        h.config.Auth.Username,
			"auth_configured": h.config.Auth.PasswordHash != "" || h.config.Auth.Password != "",
		},
	})
}

func (h *Handler) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	if h.config == nil {
		writeError(w, http.StatusInternalServerError, "config not available")
		return
	}

	var body struct {
		Cleanup *struct {
			RetentionDays        *int    `json:"retention_days"`
			DiskThresholdPercent *int    `json:"disk_threshold_percent"`
			CheckInterval        *string `json:"check_interval"`
		} `json:"cleanup"`
		WebDAV *struct {
			Enabled    *bool   `json:"enabled"`
			PathPrefix *string `json:"path_prefix"`
			ReadWrite  *bool   `json:"read_write"`
		} `json:"webdav"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Update cleanup settings
	if body.Cleanup != nil {
		if body.Cleanup.RetentionDays != nil {
			if *body.Cleanup.RetentionDays < 1 {
				writeError(w, http.StatusBadRequest, "retention_days must be >= 1")
				return
			}
			h.config.Cleanup.RetentionDays = *body.Cleanup.RetentionDays
		}
		if body.Cleanup.DiskThresholdPercent != nil {
			if *body.Cleanup.DiskThresholdPercent < 1 || *body.Cleanup.DiskThresholdPercent > 100 {
				writeError(w, http.StatusBadRequest, "disk_threshold_percent must be between 1 and 100")
				return
			}
			h.config.Cleanup.DiskThresholdPercent = *body.Cleanup.DiskThresholdPercent
		}
		if body.Cleanup.CheckInterval != nil {
			if _, err := time.ParseDuration(*body.Cleanup.CheckInterval); err != nil {
				writeError(w, http.StatusBadRequest, "check_interval must be a valid duration (e.g., \"30m\", \"1h\")")
				return
			}
			h.config.Cleanup.CheckInterval = *body.Cleanup.CheckInterval
		}
	}

	// Update webdav settings
	if body.WebDAV != nil {
		if body.WebDAV.Enabled != nil {
			if h.config.WebDAV.Enabled == nil {
				h.config.WebDAV.Enabled = new(bool)
			}
			*h.config.WebDAV.Enabled = *body.WebDAV.Enabled
		}
		if body.WebDAV.PathPrefix != nil {
			h.config.WebDAV.PathPrefix = *body.WebDAV.PathPrefix
		}
		if body.WebDAV.ReadWrite != nil {
			h.config.WebDAV.ReadWrite = *body.WebDAV.ReadWrite
		}
	}

	// Persist config to disk (with conflict detection)
	if !h.saveConfig(w) {
		return
	}
	h.logOperation(r, "config.update", "config", "", "success", "settings updated", nil)

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (h *Handler) handleGetStreamingSettings(w http.ResponseWriter, r *http.Request) {
	if h.config == nil {
		writeError(w, http.StatusInternalServerError, "config not available")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"default_protocol":      h.config.Streaming.DefaultProtocol,
		"auto_stop_no_view_sec": h.config.Streaming.AutoStopNoViewSec,
		"webrtc": map[string]any{
			"enabled":      h.config.Streaming.WebRTC.Enabled != nil && *h.config.Streaming.WebRTC.Enabled,
			"max_viewers":  h.config.Streaming.WebRTC.MaxViewers,
			"idle_timeout": h.config.Streaming.WebRTC.IdleTimeout,
		},
		"flv": map[string]any{
			"enabled":        h.config.Streaming.FLV.Enabled != nil && *h.config.Streaming.FLV.Enabled,
			"max_viewers":    h.config.Streaming.FLV.MaxViewers,
			"idle_timeout":   h.config.Streaming.FLV.IdleTimeout,
			"gop_cache_size": h.config.Streaming.FLV.GOPCacheSize,
		},
		"hls": map[string]any{
			"enabled":     h.config.IsHLSEnabled(),
			"low_latency": h.config.HLS.LowLatency,
		},
		"rtmp": map[string]any{
			"enabled": h.config.RTMP.Enabled != nil && *h.config.RTMP.Enabled,
			"port":    h.config.RTMP.Port,
		},
		"srt": map[string]any{
			"enabled": h.config.SRT.Enabled != nil && *h.config.SRT.Enabled,
			"port":    h.config.SRT.Port,
		},
		"rtsp_server": map[string]any{
			"auth_enabled": h.config.Media.RTSPAuthEnable,
			"auth_method":  h.config.Media.RTSPAuthMethod,
			"username":     h.config.Media.RTSPUsername,
		},
	})
}

func (h *Handler) handleUpdateStreamingSettings(w http.ResponseWriter, r *http.Request) {
	if h.config == nil {
		writeError(w, http.StatusInternalServerError, "config not available")
		return
	}

	var body struct {
		DefaultProtocol   *string `json:"default_protocol"`
		AutoStopNoViewSec *int    `json:"auto_stop_no_view_sec"`
		WebRTC            *struct {
			Enabled     *bool   `json:"enabled"`
			MaxViewers  *int    `json:"max_viewers"`
			IdleTimeout *string `json:"idle_timeout"`
		} `json:"webrtc"`
		FLV *struct {
			Enabled      *bool   `json:"enabled"`
			MaxViewers   *int    `json:"max_viewers"`
			IdleTimeout  *string `json:"idle_timeout"`
			GOPCacheSize *int    `json:"gop_cache_size"`
		} `json:"flv"`
		HLS *struct {
			Enabled    *bool `json:"enabled"`
			LowLatency *bool `json:"low_latency"`
		} `json:"hls"`
		RTMP *struct {
			Enabled *bool `json:"enabled"`
			Port    *int  `json:"port"`
		} `json:"rtmp"`
		SRT *struct {
			Enabled *bool `json:"enabled"`
			Port    *int  `json:"port"`
		} `json:"srt"`
		RTSPServer *struct {
			AuthEnabled *bool   `json:"auth_enabled"`
			AuthMethod  *int    `json:"auth_method"`
			Username    *string `json:"username"`
			Password    *string `json:"password"`
		} `json:"rtsp_server"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if body.DefaultProtocol != nil {
		h.config.Streaming.DefaultProtocol = *body.DefaultProtocol
	}
	if body.AutoStopNoViewSec != nil {
		h.config.Streaming.AutoStopNoViewSec = *body.AutoStopNoViewSec
	}

	if body.WebRTC != nil {
		if body.WebRTC.Enabled != nil {
			if h.config.Streaming.WebRTC.Enabled == nil {
				h.config.Streaming.WebRTC.Enabled = new(bool)
			}
			*h.config.Streaming.WebRTC.Enabled = *body.WebRTC.Enabled
		}
		if body.WebRTC.MaxViewers != nil {
			h.config.Streaming.WebRTC.MaxViewers = *body.WebRTC.MaxViewers
		}
		if body.WebRTC.IdleTimeout != nil {
			h.config.Streaming.WebRTC.IdleTimeout = *body.WebRTC.IdleTimeout
		}
	}

	if body.FLV != nil {
		if body.FLV.Enabled != nil {
			if h.config.Streaming.FLV.Enabled == nil {
				h.config.Streaming.FLV.Enabled = new(bool)
			}
			*h.config.Streaming.FLV.Enabled = *body.FLV.Enabled
		}
		if body.FLV.MaxViewers != nil {
			h.config.Streaming.FLV.MaxViewers = *body.FLV.MaxViewers
		}
		if body.FLV.IdleTimeout != nil {
			h.config.Streaming.FLV.IdleTimeout = *body.FLV.IdleTimeout
		}
		if body.FLV.GOPCacheSize != nil {
			h.config.Streaming.FLV.GOPCacheSize = *body.FLV.GOPCacheSize
		}
	}

	if body.HLS != nil {
		if body.HLS.Enabled != nil {
			if h.config.HLS.Enabled == nil {
				h.config.HLS.Enabled = new(bool)
			}
			*h.config.HLS.Enabled = *body.HLS.Enabled
		}
		if body.HLS.LowLatency != nil {
			h.config.HLS.LowLatency = *body.HLS.LowLatency
		}
	}

	// Track whether RTMP/SRT config changed (requires lalmax restart)
	needRestart := false

	if body.RTMP != nil {
		if body.RTMP.Enabled != nil {
			if h.config.RTMP.Enabled == nil {
				h.config.RTMP.Enabled = new(bool)
			}
			if *h.config.RTMP.Enabled != *body.RTMP.Enabled {
				needRestart = true
			}
			*h.config.RTMP.Enabled = *body.RTMP.Enabled
		}
		if body.RTMP.Port != nil && *body.RTMP.Port != h.config.RTMP.Port {
			needRestart = true
			h.config.RTMP.Port = *body.RTMP.Port
		}
	}

	if body.SRT != nil {
		if body.SRT.Enabled != nil {
			if h.config.SRT.Enabled == nil {
				h.config.SRT.Enabled = new(bool)
			}
			if *h.config.SRT.Enabled != *body.SRT.Enabled {
				needRestart = true
			}
			*h.config.SRT.Enabled = *body.SRT.Enabled
		}
		if body.SRT.Port != nil && *body.SRT.Port != h.config.SRT.Port {
			needRestart = true
			h.config.SRT.Port = *body.SRT.Port
		}
	}

	if body.RTSPServer != nil {
		if body.RTSPServer.AuthEnabled != nil && h.config.Media.RTSPAuthEnable != *body.RTSPServer.AuthEnabled {
			needRestart = true
			h.config.Media.RTSPAuthEnable = *body.RTSPServer.AuthEnabled
		}
		if body.RTSPServer.AuthMethod != nil && h.config.Media.RTSPAuthMethod != *body.RTSPServer.AuthMethod {
			needRestart = true
			h.config.Media.RTSPAuthMethod = *body.RTSPServer.AuthMethod
		}
		if body.RTSPServer.Username != nil && h.config.Media.RTSPUsername != *body.RTSPServer.Username {
			needRestart = true
			h.config.Media.RTSPUsername = *body.RTSPServer.Username
		}
		if body.RTSPServer.Password != nil {
			needRestart = true
			h.config.Media.RTSPPassword = *body.RTSPServer.Password
		}
	}

	// Persist config to disk (with conflict detection)
	if !h.saveConfig(w) {
		return
	}

	h.logOperation(r, "config.update", "config", "streaming", "success", "streaming settings updated", nil)

	// Restart lalmax if RTMP/SRT config changed
	if needRestart && h.mediaEngine != nil {
		if restarter, ok := h.mediaEngine.(media.Restarter); ok {
			rtmpEnabled := h.config.RTMP.Enabled != nil && *h.config.RTMP.Enabled
			srtEnabled := h.config.SRT.Enabled != nil && *h.config.SRT.Enabled
			ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
			defer cancel()
			if err := restarter.Restart(ctx, h.config.RTMP.Port, h.config.SRT.Port, rtmpEnabled, srtEnabled); err != nil {
				logger.Warn("lalmax restart failed", "error", err)
				writeError(w, http.StatusInternalServerError, "lalmax restart failed")
				return
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (h *Handler) handleGetAISettings(w http.ResponseWriter, r *http.Request) {
	if h.config == nil {
		writeError(w, http.StatusInternalServerError, "config not available")
		return
	}

	ai := h.config.AI
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"enabled":              ai.Enabled,
		"backend":              ai.Backend,
		"frame_skip_rate":      ai.FrameSkipRate,
		"confidence_threshold": ai.ConfidenceThreshold,
		"inference_timeout_ms": ai.InferenceTimeoutMs,
		"webhook":              ai.Webhook,
		"multimodal":           ai.Multimodal,
	})
}

func (h *Handler) handleUpdateAISettings(w http.ResponseWriter, r *http.Request) {
	if h.config == nil {
		writeError(w, http.StatusInternalServerError, "config not available")
		return
	}

	var body struct {
		Enabled             *bool                      `json:"enabled"`
		Backend             *string                    `json:"backend"`
		FrameSkipRate       *int                       `json:"frame_skip_rate"`
		ConfidenceThreshold *float64                   `json:"confidence_threshold"`
		InferenceTimeoutMs  *int                       `json:"inference_timeout_ms"`
		Webhook             *config.AIWebhookConfig    `json:"webhook"`
		Multimodal          *config.AIMultimodalConfig `json:"multimodal"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Update enabled
	if body.Enabled != nil {
		h.config.AI.Enabled = *body.Enabled
	}

	// Update backend (webhook mode only)
	if body.Backend != nil {
		switch *body.Backend {
		case "webhook", "disabled":
			h.config.AI.Backend = *body.Backend
		default:
			writeError(w, http.StatusBadRequest, "backend must be 'webhook' or 'disabled'")
			return
		}
	}

	// Update frame skip rate
	if body.FrameSkipRate != nil {
		if *body.FrameSkipRate < 1 || *body.FrameSkipRate > 30 {
			writeError(w, http.StatusBadRequest, "frame_skip_rate must be between 1 and 30")
			return
		}
		h.config.AI.FrameSkipRate = *body.FrameSkipRate
	}

	// Update confidence threshold
	if body.ConfidenceThreshold != nil {
		if *body.ConfidenceThreshold < 0.1 || *body.ConfidenceThreshold > 1.0 {
			writeError(w, http.StatusBadRequest, "confidence_threshold must be between 0.1 and 1.0")
			return
		}
		h.config.AI.ConfidenceThreshold = *body.ConfidenceThreshold
	}

	// Update inference timeout
	if body.InferenceTimeoutMs != nil {
		if *body.InferenceTimeoutMs < 1000 || *body.InferenceTimeoutMs > 60000 {
			writeError(w, http.StatusBadRequest, "inference_timeout_ms must be between 1000 and 60000")
			return
		}
		h.config.AI.InferenceTimeoutMs = *body.InferenceTimeoutMs
	}

	// Update webhook config
	if body.Webhook != nil {
		if h.config.AI.Webhook == nil {
			h.config.AI.Webhook = &config.AIWebhookConfig{}
		}
		h.config.AI.Webhook.Enabled = body.Webhook.Enabled
	}

	if body.Multimodal != nil {
		h.config.AI.Multimodal = body.Multimodal
	}

	// Apply defaults
	h.config.ApplyDefaults()

	// Persist config to disk
	if !h.saveConfig(w) {
		return
	}

	h.logOperation(r, "config.update", "config", "ai", "success", "AI settings updated", nil)

	// Reinitialize AI Manager with new config
	if h.aiManager != nil {
		h.aiManager.StopAll()
	}
	newMgr := ai.NewManager(h.config.AI)
	if h.db != nil {
		newMgr.SetStore(h.db)
	}
	h.aiManager = newMgr

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (h *Handler) handleBackup(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusInternalServerError, "database not available")
		return
	}
	backupDir := filepath.Join(filepath.Dir(h.configPath), "backups")
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create backup directory")
		return
	}
	filename := fmt.Sprintf("nvr-backup-%s.db", time.Now().Format("20060102-150405"))
	destPath := filepath.Join(backupDir, filename)
	if err := h.db.Backup(r.Context(), destPath); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create backup")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "created", "file": filename})
}

func (h *Handler) handleListBackups(w http.ResponseWriter, r *http.Request) {
	backupDir := filepath.Join(filepath.Dir(h.configPath), "backups")
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		writeJSON(w, http.StatusOK, []string{})
		return
	}
	var backups []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".db") {
			backups = append(backups, e.Name())
		}
	}
	if backups == nil {
		backups = []string{}
	}
	writeJSON(w, http.StatusOK, backups)
}

// protocolInfo describes a protocol for the /api/protocols endpoint.
type protocolInfo struct {
	ID           string          `json:"id"`
	Label        string          `json:"label"`
	Encodings    []string        `json:"encodings"`
	BuiltIn      bool            `json:"built_in"`
	Addable      bool            `json:"addable"` // Whether this protocol can be manually added
	Capabilities map[string]bool `json:"capabilities"`
}

func (h *Handler) handleProtocols(w http.ResponseWriter, r *http.Request) {
	protocols := []protocolInfo{
		{
			ID:           "rtsp",
			Label:        "RTSP",
			Encodings:    []string{"h264", "h265", "mjpeg"},
			BuiltIn:      true,
			Addable:      true,
			Capabilities: map[string]bool{"hls": true, "ptz": false, "snapshot": false, "discovery": false, "auth": true},
		},
		{
			ID:           "http",
			Label:        "HTTP JPEG",
			Encodings:    []string{"jpeg"},
			BuiltIn:      true,
			Addable:      true,
			Capabilities: map[string]bool{"hls": false, "ptz": false, "snapshot": true, "discovery": false, "auth": true},
		},
		{
			ID:           "onvif",
			Label:        "ONVIF",
			Encodings:    []string{"h264", "h265", "mjpeg"},
			BuiltIn:      true,
			Addable:      true,
			Capabilities: map[string]bool{"hls": true, "ptz": true, "snapshot": false, "discovery": true, "auth": true},
		},
		{
			ID:           "gb28181",
			Label:        "GB28181",
			Encodings:    []string{"h264", "h265"},
			BuiltIn:      true,
			Addable:      false, // Auto-registered via SIP, not manually added
			Capabilities: map[string]bool{"hls": true, "ptz": false, "snapshot": false, "discovery": false, "auth": false},
		},
		{
			ID:           "xiaomi",
			Label:        "Xiaomi",
			Encodings:    []string{"h264", "h265"},
			BuiltIn:      true,
			Addable:      true,
			Capabilities: map[string]bool{"hls": true, "ptz": false, "snapshot": false, "discovery": true, "auth": true},
		},
		{
			ID:           "rtmp-pull",
			Label:        "RTMP Pull",
			Encodings:    []string{"h264", "h265"},
			BuiltIn:      true,
			Addable:      true,
			Capabilities: map[string]bool{"hls": true, "ptz": false, "snapshot": false, "discovery": false, "auth": false},
		},
		{
			ID:           "http-flv-pull",
			Label:        "HTTP-FLV Pull",
			Encodings:    []string{"h264", "h265"},
			BuiltIn:      true,
			Addable:      true,
			Capabilities: map[string]bool{"hls": true, "ptz": false, "snapshot": false, "discovery": false, "auth": false},
		},
		{
			ID:           "rtmp",
			Label:        "RTMP Push",
			Encodings:    []string{"h264", "h265"},
			BuiltIn:      true,
			Addable:      false, // Auto-registered when stream is pushed
			Capabilities: map[string]bool{"hls": false, "ptz": false, "snapshot": false, "discovery": false, "auth": false},
		},
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"protocols": protocols,
	})
}

// --- Feature toggle endpoints ---

func (h *Handler) handleGetFeatures(w http.ResponseWriter, r *http.Request) {
	flags, err := h.db.GetFeatureFlags(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get feature flags")
		return
	}
	protocols := make(map[string]bool)
	for k, v := range flags {
		if strings.HasPrefix(k, "protocol.") {
			proto := strings.TrimPrefix(k, "protocol.")
			protocols[proto] = v
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"protocols": protocols})
}

func (h *Handler) handleUpdateFeatures(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Protocols map[string]bool `json:"protocols"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	ctx := r.Context()
	for proto, enabled := range body.Protocols {
		if err := h.db.SetFeatureFlag(ctx, "protocol."+proto, enabled); err != nil {
			logger.Warn("failed to set feature flag", "protocol", proto, "error", err)
		}
		if h.camMgr != nil {
			h.camMgr.SetProtocolEnabled(proto, enabled)
		}
	}
	// Return updated state
	h.handleGetFeatures(w, r)
}

// formatUptime converts a duration to a human-readable string like "2h 15m 30s".
func formatUptime(d time.Duration) string {
	rounded := d.Round(time.Second)
	h := rounded / time.Hour
	rounded -= h * time.Hour
	m := rounded / time.Minute
	rounded -= m * time.Minute
	s := rounded / time.Second
	if h > 0 {
		return fmt.Sprintf("%dh %dm %ds", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%dm %ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

func readCPURaw() (total, idle uint64, err error) {
	times, err := cpu.Times(false)
	if err != nil || len(times) == 0 {
		return 0, 0, err
	}
	t := times[0]
	total = uint64((t.User + t.Nice + t.System + t.Idle + t.Iowait + t.Irq + t.Softirq + t.Steal) * 100)
	idle = uint64(t.Idle * 100)
	return
}

func readMemoryInfo() (total, available uint64, err error) {
	vm, err := mem.VirtualMemory()
	if err != nil {
		return 0, 0, err
	}
	return vm.Total, vm.Available, nil
}

func readNetworkInfo() (bytesSent, bytesRecv uint64, err error) {
	counters, err := gopsutilnet.IOCounters(false)
	if err != nil || len(counters) == 0 {
		return 0, 0, err
	}
	return counters[0].BytesSent, counters[0].BytesRecv, nil
}

func readProcessRSS() uint64 {
	p, err := process.NewProcess(int32(os.Getpid()))
	if err != nil {
		return 0
	}
	memInfo, err := p.MemoryInfo()
	if err != nil || memInfo == nil {
		return 0
	}
	return memInfo.RSS
}

func (h *Handler) handleSystemStats(w http.ResponseWriter, r *http.Request) {
	cpuTotal, cpuIdle, _ := readCPURaw()
	memTotal, memAvailable, _ := readMemoryInfo()
	netSent, netRecv, _ := readNetworkInfo()
	processRSS := readProcessRSS()

	writeJSON(w, http.StatusOK, SystemStats{
		CPU:       CPUStats{Total: cpuTotal, Idle: cpuIdle},
		Memory:    MemoryStats{Total: memTotal, Available: memAvailable, ProcessRSS: processRSS},
		Network:   NetworkStats{BytesSent: netSent, BytesRecv: netRecv},
		System:    SystemInfo{OS: runtime.GOOS, Arch: runtime.GOARCH, CPUCores: runtime.NumCPU()},
		Uptime:    formatUptime(time.Since(appStartTime)),
		Timestamp: time.Now().Unix(),
	})
}

func (h *Handler) handleGetGB28181Settings(w http.ResponseWriter, r *http.Request) {
	if h.config == nil {
		writeError(w, http.StatusInternalServerError, "config not available")
		return
	}

	enabled := h.config.GB28181.Enabled != nil && *h.config.GB28181.Enabled

	// Use default values if empty
	id := h.config.GB28181.ID
	if id == "" {
		id = "34020000002000000001"
	}
	password := h.config.GB28181.Password
	if password == "" {
		password = "12345678"
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":          enabled,
		"host":             h.config.GB28181.Host,
		"port":             h.config.GB28181.Port,
		"id":               id,
		"password":         password,
		"media_ip":         h.config.GB28181.MediaIP,
		"media_port":       h.config.GB28181.MediaPort,
		"standard_version": h.config.GB28181.StandardVersion,
	})
}

func (h *Handler) handleUpdateGB28181Settings(w http.ResponseWriter, r *http.Request) {
	if h.config == nil {
		writeError(w, http.StatusInternalServerError, "config not available")
		return
	}

	var body struct {
		Enabled         *bool   `json:"enabled"`
		Host            *string `json:"host"`
		Port            *int    `json:"port"`
		ID              *string `json:"id"`
		Password        *string `json:"password"`
		MediaIP         *string `json:"media_ip"`
		MediaPort       *int    `json:"media_port"`
		StandardVersion *string `json:"standard_version"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Track whether config changed (requires GB28181 restart)
	needRestart := false

	if body.Enabled != nil {
		if h.config.GB28181.Enabled == nil {
			h.config.GB28181.Enabled = new(bool)
		}
		if *h.config.GB28181.Enabled != *body.Enabled {
			needRestart = true
		}
		*h.config.GB28181.Enabled = *body.Enabled
	}
	if body.Host != nil && *body.Host != h.config.GB28181.Host {
		needRestart = true
		h.config.GB28181.Host = *body.Host
	}
	if body.Port != nil && *body.Port != h.config.GB28181.Port {
		needRestart = true
		h.config.GB28181.Port = *body.Port
	}
	if body.ID != nil && *body.ID != h.config.GB28181.ID {
		needRestart = true
		h.config.GB28181.ID = *body.ID
	}
	if body.Password != nil && *body.Password != h.config.GB28181.Password {
		needRestart = true
		h.config.GB28181.Password = *body.Password
	}
	if body.MediaIP != nil && *body.MediaIP != h.config.GB28181.MediaIP {
		needRestart = true
		h.config.GB28181.MediaIP = *body.MediaIP
	}
	if body.MediaPort != nil && *body.MediaPort != h.config.GB28181.MediaPort {
		needRestart = true
		h.config.GB28181.MediaPort = *body.MediaPort
	}
	if body.StandardVersion != nil && *body.StandardVersion != h.config.GB28181.StandardVersion {
		needRestart = true
		h.config.GB28181.StandardVersion = *body.StandardVersion
	}

	// Persist config to disk (with conflict detection)
	if !h.saveConfig(w) {
		return
	}

	h.logOperation(r, "config.update", "config", "gb28181", "success", "GB28181 settings updated", nil)

	// Restart GB28181 if config changed
	if needRestart && h.gb28181Restarter != nil {
		enabled := h.config.GB28181.Enabled != nil && *h.config.GB28181.Enabled
		gbCfg := &config.GB28181Config{
			Enabled:         &enabled,
			Host:            h.config.GB28181.Host,
			Port:            h.config.GB28181.Port,
			ID:              h.config.GB28181.ID,
			Password:        h.config.GB28181.Password,
			MediaIP:         h.config.GB28181.MediaIP,
			MediaPort:       h.config.GB28181.MediaPort,
			StandardVersion: h.config.GB28181.StandardVersion,
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		if err := h.gb28181Restarter.RestartGB28181(ctx, gbCfg); err != nil {
			logger.Warn("GB28181 restart failed", "error", err)
			writeError(w, http.StatusInternalServerError, "GB28181 restart failed")
			return
		}
		if provider, ok := h.gb28181Restarter.(GB28181ServerProvider); ok {
			current := provider.CurrentGB28181Server()
			h.SetGB28181Server(current)
			h.SetGB28181ServerInstance(current)
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// saveConfig saves the config using the Watcher (with conflict detection).
// Returns true if save succeeded, false if conflict detected (409 already written).
func (h *Handler) saveConfig(w http.ResponseWriter) bool {
	if h.configWatcher == nil {
		if err := config.Save(h.configPath, h.config); err != nil {
			logger.Warn("failed to save config", "error", err)
		}
		return true
	}
	if err := h.configWatcher.Save(false); err != nil {
		if err == config.ErrConfigModified {
			writeError(w, http.StatusConflict, "config file was modified externally; please reload before saving")
			return false
		}
		logger.Warn("failed to save config", "error", err)
	}
	return true
}

// handleReloadConfig re-reads the config from disk and notifies subscribers.
func (h *Handler) handleReloadConfig(w http.ResponseWriter, r *http.Request) {
	if h.configWatcher == nil {
		// Fallback: reload directly without watcher
		cfg, err := config.Load(h.configPath)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to reload config: "+err.Error())
			return
		}
		h.config = cfg
		h.logSuccess(r, "config.reload", "config", "", "config reloaded from disk", nil)
		writeJSON(w, http.StatusOK, map[string]string{"status": "reloaded"})
		return
	}
	if !h.configWatcher.CheckExternalChange() {
		writeJSON(w, http.StatusOK, map[string]string{"status": "no_changes"})
		return
	}
	if err := h.configWatcher.Reload(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to reload config: "+err.Error())
		return
	}
	h.config = h.configWatcher.Config()
	h.logSuccess(r, "config.reload", "config", "", "config reloaded from disk", nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "reloaded"})
}

// handleCheckConfigChange checks if the config file was modified externally.
func (h *Handler) handleCheckConfigChange(w http.ResponseWriter, r *http.Request) {
	if h.configWatcher == nil {
		writeJSON(w, http.StatusOK, map[string]any{"changed": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"changed": h.configWatcher.CheckExternalChange()})
}

// handleRegenerateLalmaxConfig forces a full regeneration of the lalmax config file.
func (h *Handler) handleRegenerateLalmaxConfig(w http.ResponseWriter, r *http.Request) {
	type restarter interface {
		RegenerateConfig() error
	}
	if h.mediaEngine == nil {
		writeError(w, http.StatusInternalServerError, "media engine not available")
		return
	}
	e, ok := h.mediaEngine.(restarter)
	if !ok {
		writeError(w, http.StatusBadRequest, "media engine does not support config regeneration")
		return
	}
	if err := e.RegenerateConfig(); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to regenerate config: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "regenerated"})
}

func (h *Handler) handleGetHLSSettings(w http.ResponseWriter, r *http.Request) {
	if h.config == nil {
		writeError(w, http.StatusInternalServerError, "config not available")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":                  h.config.IsHLSEnabled(),
		"on_demand":                h.config.IsHLSOnDemand(),
		"idle_timeout":             h.config.HLS.IdleTimeout,
		"segment_count":            h.config.HLS.SegmentCount,
		"lal_fragment_duration_ms": h.config.HLS.LalFragmentDurationMs,
		"lal_fragment_num":         h.config.HLS.LalFragmentNum,
		"lal_cleanup_mode":         h.config.HLS.LalCleanupMode,
		"lal_use_memory":           h.config.HLS.LalUseMemory,
		"lalmax_segment_duration":  h.config.HLS.LalmaxSegmentDuration,
		"lalmax_part_duration":     h.config.HLS.LalmaxPartDuration,
	})
}

func (h *Handler) handleUpdateHLSSettings(w http.ResponseWriter, r *http.Request) {
	if h.config == nil {
		writeError(w, http.StatusInternalServerError, "config not available")
		return
	}
	var body struct {
		Enabled               *bool   `json:"enabled"`
		OnDemand              *bool   `json:"on_demand"`
		IdleTimeout           *string `json:"idle_timeout"`
		SegmentCount          *int    `json:"segment_count"`
		LalFragmentDurationMs *int    `json:"lal_fragment_duration_ms"`
		LalFragmentNum        *int    `json:"lal_fragment_num"`
		LalCleanupMode        *int    `json:"lal_cleanup_mode"`
		LalUseMemory          *bool   `json:"lal_use_memory"`
		LalmaxSegmentDuration *int    `json:"lalmax_segment_duration"`
		LalmaxPartDuration    *int    `json:"lalmax_part_duration"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	hlsChanged := false
	if body.Enabled != nil {
		if h.config.HLS.Enabled == nil {
			h.config.HLS.Enabled = new(bool)
		}
		if *h.config.HLS.Enabled != *body.Enabled {
			hlsChanged = true
		}
		*h.config.HLS.Enabled = *body.Enabled
	}
	if body.OnDemand != nil {
		if h.config.HLS.OnDemand == nil {
			h.config.HLS.OnDemand = new(bool)
		}
		if *h.config.HLS.OnDemand != *body.OnDemand {
			hlsChanged = true
		}
		*h.config.HLS.OnDemand = *body.OnDemand
	}
	if body.IdleTimeout != nil {
		if h.config.HLS.IdleTimeout != *body.IdleTimeout {
			hlsChanged = true
		}
		h.config.HLS.IdleTimeout = *body.IdleTimeout
	}
	if body.SegmentCount != nil {
		h.config.HLS.SegmentCount = *body.SegmentCount
		hlsChanged = true
	}
	if body.LalFragmentDurationMs != nil {
		h.config.HLS.LalFragmentDurationMs = *body.LalFragmentDurationMs
		hlsChanged = true
	}
	if body.LalFragmentNum != nil {
		h.config.HLS.LalFragmentNum = *body.LalFragmentNum
		hlsChanged = true
	}
	if body.LalCleanupMode != nil {
		h.config.HLS.LalCleanupMode = *body.LalCleanupMode
		hlsChanged = true
	}
	if body.LalUseMemory != nil {
		h.config.HLS.LalUseMemory = *body.LalUseMemory
		hlsChanged = true
	}
	if body.LalmaxSegmentDuration != nil {
		h.config.HLS.LalmaxSegmentDuration = *body.LalmaxSegmentDuration
		hlsChanged = true
	}
	if body.LalmaxPartDuration != nil {
		h.config.HLS.LalmaxPartDuration = *body.LalmaxPartDuration
		hlsChanged = true
	}
	if !h.config.IsHLSEnabled() {
		switch h.config.Streaming.DefaultProtocol {
		case "hls", "ll-hls":
			h.config.Streaming.DefaultProtocol = "webrtc"
		}
	}
	if !h.saveConfig(w) {
		return
	}
	h.logOperation(r, "config.update", "config", "hls", "success", "HLS settings updated", nil)
	if hlsChanged && h.mediaEngine != nil {
		if applier, ok := h.mediaEngine.(interface {
			ApplyHLSConfig(config.HLSConfig) error
		}); ok {
			if err := applier.ApplyHLSConfig(h.config.HLS); err != nil {
				logger.Warn("failed to apply HLS config", "error", err)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// --- Network interfaces endpoint ---

type NetworkInterface struct {
	Name         string   `json:"name"`
	MTU          int      `json:"mtu"`
	HardwareAddr string   `json:"hardware_addr"`
	Addresses    []string `json:"addresses"`
	IsUp         bool     `json:"is_up"`
	IsLoopback   bool     `json:"is_loopback"`
	Speed        string   `json:"speed,omitempty"` // e.g. "1000Mbps", "100Mbps", "unknown"
}

func (h *Handler) handleGetNetworkInterfaces(w http.ResponseWriter, r *http.Request) {
	interfaces, err := getNetworkInterfaces()
	if err != nil {
		logger.Error("get network interfaces failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to get network interfaces")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"interfaces": interfaces,
	})
}

func getNetworkInterfaces() ([]NetworkInterface, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}

	var result []NetworkInterface
	for _, iface := range ifaces {
		// Skip loopback and down interfaces for cleaner output
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		var ipAddrs []string
		for _, addr := range addrs {
			if !isDisplayableInterfaceAddress(addr) {
				continue
			}
			ipAddrs = append(ipAddrs, addr.String())
		}
		if len(ipAddrs) == 0 {
			continue
		}

		isUp := iface.Flags&net.FlagUp != 0
		speed := getInterfaceSpeed(iface.Name)

		result = append(result, NetworkInterface{
			Name:         iface.Name,
			MTU:          iface.MTU,
			HardwareAddr: iface.HardwareAddr.String(),
			Addresses:    ipAddrs,
			IsUp:         isUp,
			IsLoopback:   iface.Flags&net.FlagLoopback != 0,
			Speed:        speed,
		})
	}

	return result, nil
}

func isDisplayableInterfaceAddress(addr net.Addr) bool {
	prefix, err := netip.ParsePrefix(addr.String())
	if err != nil {
		return true
	}
	ip := prefix.Addr()
	return !ip.IsLinkLocalUnicast()
}

// getInterfaceSpeed reads the link speed from /sys/class/net on Linux.
// Returns "unknown" if unable to determine.
func getInterfaceSpeed(name string) string {
	// Try Linux sysfs
	speedPath := fmt.Sprintf("/sys/class/net/%s/speed", name)
	data, err := os.ReadFile(speedPath)
	if err != nil {
		return "unknown"
	}
	speedStr := strings.TrimSpace(string(data))
	speed, err := strconv.Atoi(speedStr)
	if err != nil || speed <= 0 {
		return "unknown"
	}
	return fmt.Sprintf("%dMbps", speed)
}
