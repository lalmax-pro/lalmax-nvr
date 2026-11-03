// SPDX-License-Identifier: MIT
//
// Xiaomi camera plugin registration for lalmax-nvr.
// Licensed under the MIT License.

package xiaomi

import (
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/metrics"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

// XiaomiPlugin provides Xiaomi camera recorder creation.
type XiaomiPlugin struct{}

func (p *XiaomiPlugin) Name() string { return "xiaomi" }

func (p *XiaomiPlugin) Protocols() []string { return []string{"xiaomi"} }

// cloudCfg stores the Xiaomi cloud credentials, set via SetCloudConfig.
var (
	cloudCfg   XiaomiCloudConfig
	cloudCfgMu sync.RWMutex
)

func currentCloudConfig() XiaomiCloudConfig {
	cloudCfgMu.RLock()
	defer cloudCfgMu.RUnlock()
	return cloudCfg
}

// SetCloudConfig stores the Xiaomi cloud credentials for use by recorders.
// Existing recorders read the latest credentials on each reconnect.
func SetCloudConfig(cfg config.XiaomiConfig) {
	cloudCfgMu.Lock()
	defer cloudCfgMu.Unlock()
	cloudCfg = XiaomiCloudConfig{
		UserID: cfg.UserID,
		Token:  cfg.Token,
		Region: cfg.Region,
	}
}

// extractDID extracts the device ID from a xiaomi:// URL.
// Input: "xiaomi://655448418" → Output: "655448418"
func extractDID(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err == nil && u.Scheme == "xiaomi" {
		return u.Host
	}
	return strings.TrimPrefix(rawURL, "xiaomi://")
}

func cameraStreamOptions(rawURL string) (string, string) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", ""
	}
	return u.Query().Get("channel"), u.Query().Get("quality")
}

func (p *XiaomiPlugin) NewRecorder(cfg config.CameraConfig, store *storage.Manager, db RecordingDB, opts ...*metrics.Metrics) model.Recorder {
	channel, quality := cameraStreamOptions(cfg.URL)
	did := cfg.DID
	if did == "" {
		did = extractDID(cfg.URL)
	}

	recCfg := XiaomiRecorderConfig{
		CameraID:            cfg.ID,
		DID:                 did,
		Channel:             channel,
		Quality:             quality,
		CloudCfg:            currentCloudConfig(),
		CloudConfigProvider: currentCloudConfig,
		SegmentDur:          30 * time.Second,
		DB:                  db,
		AudioEnabled:        cfg.AudioEnabled,
	}
	return NewXiaomiRecorder(recCfg, store, opts...)
}

func (p *XiaomiPlugin) NewRecorderWithMediaEngine(cfg config.CameraConfig, store *storage.Manager, db RecordingDB, mediaEngine media.Engine, opts ...*metrics.Metrics) model.Recorder {
	channel, quality := cameraStreamOptions(cfg.URL)
	did := cfg.DID
	if did == "" {
		did = extractDID(cfg.URL)
	}

	recCfg := XiaomiRecorderConfig{
		CameraID:            cfg.ID,
		DID:                 did,
		Channel:             channel,
		Quality:             quality,
		CloudCfg:            currentCloudConfig(),
		CloudConfigProvider: currentCloudConfig,
		SegmentDur:          30 * time.Second,
		DB:                  db,
		AudioEnabled:        cfg.AudioEnabled,
		MediaEngine:         mediaEngine,
	}
	return NewXiaomiRecorder(recCfg, store, opts...)
}

func (p *XiaomiPlugin) RegisterRoutes(r chi.Router) {
	// Xiaomi-specific routes registered separately in api/handler.go
}

func (p *XiaomiPlugin) ConfigSchema() interface{} {
	return config.XiaomiConfig{}
}
