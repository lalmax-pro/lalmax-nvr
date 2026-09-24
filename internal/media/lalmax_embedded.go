package media

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lal/pkg/logic"
	lalmaxconfig "github.com/q191201771/lalmax/config"
	lalmaxserver "github.com/q191201771/lalmax/server"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
)

type EmbeddedLalmaxConfig struct {
	HTTPAddr    string
	PublicURL   string
	ConfigPath  string
	RTMPPort    int
	RTSPPort    int
	HTTPPort    int
	RTMPEnabled bool
	SRTPort     int
	SRTEnabled  bool
	// lal (TS HLS) settings
	HLSEnabled            bool
	LalFragmentDurationMs int
	LalFragmentNum        int
	LalCleanupMode        int
	LalUseMemory          bool
	// lalmax (fMP4/LL-HLS) settings
	LalmaxSegmentCount    int
	LalmaxSegmentDuration int
	LalmaxPartDuration    int
	HLSOnDemand           bool
	DLNAEnabled           bool
	HLSIdleTimeoutMs      int
	// RTSP server auth
	RTSPAuthEnable bool
	RTSPAuthMethod int
	RTSPUsername   string
	RTSPPassword   string
	// LalLogLevel mirrors observability.log_level (debug/info/warn/error).
	LalLogLevel string
	// HTTPTSGOPNum is lal httpts.gop_num. 1 caches the latest GOP.
	HTTPTSGOPNum int
}

type EmbeddedLalmax struct {
	*LalmaxHTTP
	cfg        EmbeddedLalmaxConfig
	mu         sync.Mutex
	server     *lalmaxserver.LalMaxServer
	httpEngine *LalmaxHTTP
	svrOpts    []lalmaxserver.LalMaxServerOption
}

func applyEmbeddedPortDefaults(cfg *EmbeddedLalmaxConfig) {
	if cfg.HTTPAddr == "" {
		cfg.HTTPAddr = config.DefaultLalmaxHTTPAddr
	}
	if cfg.RTMPPort == 0 {
		cfg.RTMPPort = config.DefaultLalRTMPPort
	}
	if cfg.RTSPPort == 0 {
		cfg.RTSPPort = config.DefaultLalRTSPPort
	}
	if cfg.HTTPPort == 0 {
		cfg.HTTPPort = config.DefaultLalHTTPPort
	}
	if cfg.SRTPort == 0 {
		cfg.SRTPort = config.DefaultSRTPort
	}
}

func lalmaxListenAddr(httpAddr string) string {
	parsed, err := url.Parse(httpAddr)
	if err != nil || parsed.Host == "" {
		return fmt.Sprintf(":%d", config.DefaultLalmaxHTTPPort)
	}
	if port := parsed.Port(); port != "" {
		return ":" + port
	}
	return fmt.Sprintf(":%d", config.DefaultLalmaxHTTPPort)
}

func NewEmbeddedLalmax(cfg EmbeddedLalmaxConfig, svrOpts ...lalmaxserver.LalMaxServerOption) (*EmbeddedLalmax, error) {
	if cfg.HTTPAddr == "" {
		cfg.HTTPAddr = config.DefaultLalmaxHTTPAddr
	}
	if cfg.RTMPPort == 0 {
		cfg.RTMPPort = config.DefaultLalRTMPPort
	}
	if cfg.SRTPort == 0 {
		cfg.SRTPort = config.DefaultSRTPort
	}
	applyEmbeddedPortDefaults(&cfg)
	httpEngine, err := NewLalmaxHTTP(LalmaxHTTPConfig{
		BaseURL:   cfg.HTTPAddr,
		PublicURL: firstNonEmpty(cfg.PublicURL, cfg.HTTPAddr),
		RTMPPort:  cfg.RTMPPort,
		RTSPPort:  cfg.RTSPPort,
		HTTPPort:  cfg.HTTPPort,
	})
	if err != nil {
		return nil, err
	}
	lalCfg, err := loadEmbeddedLalmaxConfig(cfg)
	if err != nil {
		return nil, err
	}
	svr, err := lalmaxserver.NewLalMaxServer(lalCfg, svrOpts...)
	if err != nil {
		return nil, err
	}
	return &EmbeddedLalmax{LalmaxHTTP: httpEngine, cfg: cfg, server: svr, httpEngine: httpEngine, svrOpts: svrOpts}, nil
}

// RegisterHookPlugin registers a hook plugin with the embedded lalmax server.
// Must be called after NewEmbeddedLalmax and before Start.
func (e *EmbeddedLalmax) RegisterHookPlugin(plugin lalmaxserver.HookPlugin, options lalmaxserver.HookPluginOptions) (func(), error) {
	e.mu.Lock()
	server := e.server
	e.mu.Unlock()
	if server == nil {
		return nil, fmt.Errorf("embedded lalmax server is nil")
	}
	return server.RegisterHookPlugin(plugin, options)
}

// Server returns the underlying lalmax server. Must be called after NewEmbeddedLalmax.
func (e *EmbeddedLalmax) Server() *lalmaxserver.LalMaxServer {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.server
}

// PlayHTTPHandler returns the in-process lalmax HTTP handler for play proxying.
func (e *EmbeddedLalmax) PlayHTTPHandler() http.Handler {
	e.mu.Lock()
	server := e.server
	e.mu.Unlock()
	if server == nil {
		return nil
	}
	return server.HTTPHandler()
}

// AddCustomizePubSession registers a custom publish session for feeding frames directly into lal.
func (e *EmbeddedLalmax) AddCustomizePubSession(_ context.Context, streamName string) (CustomizePubSession, error) {
	e.mu.Lock()
	server := e.server
	e.mu.Unlock()
	if server == nil {
		return nil, fmt.Errorf("embedded lalmax server is nil")
	}
	ctx, err := server.AddCustomizePubSession(streamName)
	if err != nil {
		return nil, err
	}
	return &customizePubSessionWrapper{ctx: ctx}, nil
}

// DelCustomizePubSession removes a custom publish session.
func (e *EmbeddedLalmax) DelCustomizePubSession(_ context.Context, session CustomizePubSession) error {
	wrapper, ok := session.(*customizePubSessionWrapper)
	if !ok {
		return fmt.Errorf("invalid session type")
	}
	e.mu.Lock()
	server := e.server
	e.mu.Unlock()
	if server == nil {
		return fmt.Errorf("embedded lalmax server is nil")
	}
	server.DelCustomizePubSession(wrapper.ctx)
	return nil
}

// customizePubSessionWrapper wraps logic.ICustomizePubSessionContext as CustomizePubSession.
type customizePubSessionWrapper struct {
	ctx logic.ICustomizePubSessionContext
}

func (w *customizePubSessionWrapper) FeedAvPacket(packet base.AvPacket) error {
	return w.ctx.FeedAvPacket(packet)
}

func (w *customizePubSessionWrapper) FeedRtmpMsg(msg base.RtmpMsg) error {
	return w.ctx.FeedRtmpMsg(msg)
}

func (w *customizePubSessionWrapper) WithOption(modOption func(*base.AvPacketStreamOption)) {
	w.ctx.WithOption(modOption)
}

func (w *customizePubSessionWrapper) FeedAudioSpecificConfig(asc []byte) error {
	return w.ctx.FeedAudioSpecificConfig(asc)
}

func (e *EmbeddedLalmax) Start(ctx context.Context) error {
	e.mu.Lock()
	server := e.server
	e.mu.Unlock()
	if server == nil {
		return fmt.Errorf("embedded lalmax server is nil")
	}
	if err := server.Start(ctx); err != nil {
		return err
	}
	e.applyRuntimeHLSSettings(server, e.cfg)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := e.LalmaxHTTP.Ready(ctx); err == nil && server.Ready() {
			return nil
		}
		select {
		case <-ctx.Done():
			e.shutdownAndClear(server)
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	e.shutdownAndClear(server)
	return fmt.Errorf("embedded lalmax is not ready")
}

func (e *EmbeddedLalmax) Shutdown(ctx context.Context) error {
	e.mu.Lock()
	server := e.server
	e.mu.Unlock()
	if server == nil {
		return nil
	}
	if err := server.Shutdown(ctx); err != nil {
		return err
	}
	e.clearServer(server)
	return nil
}

func (e *EmbeddedLalmax) Restart(ctx context.Context, rtmpPort, srtPort int, rtmpEnabled, srtEnabled bool) error {
	e.mu.Lock()
	old := e.server
	e.server = nil
	e.cfg.RTMPPort = rtmpPort
	e.cfg.SRTPort = srtPort
	e.cfg.RTMPEnabled = rtmpEnabled
	e.cfg.SRTEnabled = srtEnabled
	cfgPath := e.cfg.ConfigPath
	e.mu.Unlock()

	// Drop live HTTP/RTMP sessions immediately. Graceful Shutdown waits for
	// in-flight viewers and times out while holding the engine lock.
	if old != nil {
		old.Close()
		time.Sleep(200 * time.Millisecond)
	}

	// Patch existing config file (preserves user customizations) or generate new one
	rtmpAddr := fmt.Sprintf(":%d", rtmpPort)
	srtAddr := fmt.Sprintf(":%d", srtPort)

	if cfgPath != "" {
		if _, err := os.Stat(cfgPath); err == nil {
			// File exists 闁?patch only rtmp/srt fields
			if err := patchLalmaxConfig(cfgPath, rtmpEnabled, srtEnabled, rtmpAddr, srtAddr, e.cfg.HTTPTSGOPNum); err != nil {
				return fmt.Errorf("patch lalmax config: %w", err)
			}
		} else {
			// File doesn't exist 闁?generate full config
			raw, err := embeddedConfigJSON(e.cfg)
			if err != nil {
				return fmt.Errorf("generate config: %w", err)
			}
			if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
				return fmt.Errorf("create config dir: %w", err)
			}
			if err := os.WriteFile(cfgPath, raw, 0o644); err != nil {
				return fmt.Errorf("write config: %w", err)
			}
		}
	}

	// Create and start new server
	lalCfg, err := loadEmbeddedLalmaxConfig(e.cfg)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	svr, err := lalmaxserver.NewLalMaxServer(lalCfg, e.svrOpts...)
	if err != nil {
		return fmt.Errorf("create server: %w", err)
	}

	e.mu.Lock()
	e.server = svr
	e.mu.Unlock()

	if err := svr.Start(ctx); err != nil {
		return fmt.Errorf("start server: %w", err)
	}
	e.applyRuntimeHLSSettings(svr, e.cfg)

	// Update LalmaxHTTP port reference
	e.LalmaxHTTP = e.httpEngine

	// Wait for ready
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := e.LalmaxHTTP.Ready(ctx); err == nil && svr.Ready() {
			slog.Info("lalmax restarted", "rtmp_port", rtmpPort, "srt_port", srtPort,
				"rtmp_enabled", rtmpEnabled, "srt_enabled", srtEnabled)
			return nil
		}
		select {
		case <-ctx.Done():
			e.shutdownAndClear(svr)
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	e.shutdownAndClear(svr)
	return fmt.Errorf("lalmax not ready after restart")
}

func (e *EmbeddedLalmax) Ready(ctx context.Context) error {
	e.mu.Lock()
	server := e.server
	e.mu.Unlock()
	if server == nil || !server.Ready() {
		return fmt.Errorf("embedded lalmax is not ready")
	}
	return e.LalmaxHTTP.Ready(ctx)
}

func (e *EmbeddedLalmax) ConfigPath() string { return e.cfg.ConfigPath }

func lalLogLevelFromString(level string) int {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return 1
	case "warn":
		return 3
	case "error":
		return 4
	default:
		return 2
	}
}

func lalLogSection(level int) map[string]any {
	return map[string]any{
		"level":                  level,
		"filename":               "",
		"is_to_stdout":           false,
		"is_rotate_daily":        false,
		"short_file_flag":        true,
		"timestamp_flag":         true,
		"timestamp_with_ms_flag": true,
		"level_flag":             true,
		"assert_behavior":        1,
	}
}

// ensureLalLogConfig adds a log section to the embedded lal config when missing.
// Lal defaults to debug+stdout when log is absent, which floods lalmax-nvr.log.
func ensureLalLogConfig(path string, logLevel string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	lal := map[string]json.RawMessage{}
	if lalRaw, ok := raw["lal"]; ok {
		if err := json.Unmarshal(lalRaw, &lal); err != nil {
			return err
		}
	}
	if _, ok := lal["log"]; ok {
		return nil
	}
	level := lalLogLevelFromString(logLevel)
	logJSON, err := json.Marshal(lalLogSection(level))
	if err != nil {
		return err
	}
	lal["log"] = logJSON
	lalPatched, err := json.Marshal(lal)
	if err != nil {
		return err
	}
	raw["lal"] = lalPatched
	patched, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, patched, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func loadEmbeddedLalmaxConfig(cfg EmbeddedLalmaxConfig) (*lalmaxconfig.Config, error) {
	if cfg.ConfigPath != "" {
		// If config file exists, patch only the runtime-controlled protocol
		// fields before loading it. This preserves customizations while keeping
		// the lalmax listener config in sync with the NVR settings.
		if _, err := os.Stat(cfg.ConfigPath); err == nil {
			rtmpPort := cfg.RTMPPort
			if rtmpPort == 0 {
				rtmpPort = config.DefaultLalRTMPPort
			}
			srtPort := cfg.SRTPort
			if srtPort == 0 {
				srtPort = config.DefaultSRTPort
			}
			if err := patchLalmaxConfig(
				cfg.ConfigPath,
				cfg.RTMPEnabled,
				cfg.SRTEnabled,
				fmt.Sprintf(":%d", rtmpPort),
				fmt.Sprintf(":%d", srtPort),
				cfg.HTTPTSGOPNum,
			); err != nil {
				return nil, fmt.Errorf("sync lalmax protocol config: %w", err)
			}
			if err := ensureLalLogConfig(cfg.ConfigPath, cfg.LalLogLevel); err != nil {
				slog.Warn("ensure lal log config", "path", cfg.ConfigPath, "error", err)
			}
			if err := lalmaxconfig.Open(cfg.ConfigPath); err != nil {
				return nil, err
			}
			slog.Info("loaded existing lalmax config", "path", cfg.ConfigPath)
			c := lalmaxconfig.GetConfig()
			// Fix invalid low_latency config (segment_count must be >= 7)
			if c.Fmp4Config.Hls.LowLatency && c.Fmp4Config.Hls.SegmentCount < 7 {
				slog.Warn("fixing lalmax config: segment_count must be >= 7 when low_latency is enabled",
					"old", c.Fmp4Config.Hls.SegmentCount, "new", 7)
				c.Fmp4Config.Hls.SegmentCount = 7
				if cfg.ConfigPath != "" {
					if raw, err := json.MarshalIndent(c, "", "  "); err == nil {
						_ = os.WriteFile(cfg.ConfigPath, raw, 0o644)
					}
				}
			}
			return c, nil
		}
		// File doesn't exist 闁?generate default config and save it
		raw, err := embeddedConfigJSON(cfg)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(cfg.ConfigPath), 0o755); err != nil {
			return nil, fmt.Errorf("create lalmax config dir: %w", err)
		}
		if err := os.WriteFile(cfg.ConfigPath, raw, 0o644); err != nil {
			return nil, fmt.Errorf("write default lalmax config: %w", err)
		}
		slog.Info("created default lalmax config", "path", cfg.ConfigPath,
			"rtmp_enabled", cfg.RTMPEnabled, "srt_enabled", cfg.SRTEnabled)
		if err := lalmaxconfig.Open(cfg.ConfigPath); err != nil {
			return nil, err
		}
		return lalmaxconfig.GetConfig(), nil
	}
	raw, err := embeddedConfigJSON(cfg)
	if err != nil {
		return nil, err
	}
	if err := lalmaxconfig.Unmarshal(raw); err != nil {
		return nil, err
	}
	return lalmaxconfig.GetConfig(), nil
}

// httptsGopNum clamps the HTTP-TS GOP cache. 0 and negatives mean the default
// of one GOP, which lets a new player start on the latest keyframe.
func httptsGopNum(n int) int {
	if n < 1 {
		return 1
	}
	if n > 16 {
		return 16
	}
	return n
}

// patchLalmaxConfig reads the existing lalmax config JSON file, patches only
// the RTMP/SRT enable and addr fields in the lal/lalmax sections, and writes
// it back. This preserves all user customizations.
func patchLalmaxConfig(path string, rtmpEnabled, srtEnabled bool, rtmpAddr, srtAddr string, gopNum int) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read lalmax config: %w", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parse lalmax config: %w", err)
	}

	// Patch lal section (rtmp/rtsp/httpflv defaults used by embedded mode).
	lal := map[string]json.RawMessage{}
	if lalRaw, ok := raw["lal"]; ok {
		if err := json.Unmarshal(lalRaw, &lal); err != nil {
			return fmt.Errorf("parse lal config: %w", err)
		}
	}
	rtmp := map[string]any{}
	if rtmpRaw, ok := lal["rtmp"]; ok {
		if err := json.Unmarshal(rtmpRaw, &rtmp); err != nil {
			return fmt.Errorf("parse rtmp config: %w", err)
		}
	}
	rtmp["enable"] = rtmpEnabled
	rtmp["addr"] = rtmpAddr
	patchedRTMP, err := json.Marshal(rtmp)
	if err != nil {
		return fmt.Errorf("marshal rtmp config: %w", err)
	}
	lal["rtmp"] = patchedRTMP

	rtsp := map[string]any{}
	if rtspRaw, ok := lal["rtsp"]; ok {
		if err := json.Unmarshal(rtspRaw, &rtsp); err != nil {
			return fmt.Errorf("parse rtsp config: %w", err)
		}
	}
	rtsp["addr"] = fmt.Sprintf(":%d", config.DefaultLalRTSPPort)
	patchedRTSP, err := json.Marshal(rtsp)
	if err != nil {
		return fmt.Errorf("marshal rtsp config: %w", err)
	}
	lal["rtsp"] = patchedRTSP

	defaultHTTP := map[string]any{}
	if httpRaw, ok := lal["default_http"]; ok {
		if err := json.Unmarshal(httpRaw, &defaultHTTP); err != nil {
			return fmt.Errorf("parse default_http config: %w", err)
		}
	}
	defaultHTTP["http_listen_addr"] = fmt.Sprintf(":%d", config.DefaultLalHTTPPort)
	patchedHTTP, err := json.Marshal(defaultHTTP)
	if err != nil {
		return fmt.Errorf("marshal default_http config: %w", err)
	}
	lal["default_http"] = patchedHTTP

	// HTTP-TS shares the HTTP-FLV listener, so a .ts URL still returns headers
	// when httpts is disabled. MPEG-TS packets are only produced when the
	// remuxer is started, which requires httpts.enable.
	httpts := map[string]any{}
	if tsRaw, ok := lal["httpts"]; ok {
		if err := json.Unmarshal(tsRaw, &httpts); err != nil {
			return fmt.Errorf("parse httpts config: %w", err)
		}
	}
	httpts["enable"] = true
	if strings.TrimSpace(fmt.Sprint(httpts["url_pattern"])) == "" || httpts["url_pattern"] == nil {
		httpts["url_pattern"] = "/live/"
	}
	httpts["gop_num"] = httptsGopNum(gopNum)
	patchedTS, err := json.Marshal(httpts)
	if err != nil {
		return fmt.Errorf("marshal httpts config: %w", err)
	}
	lal["httpts"] = patchedTS

	patchedLal, err := json.Marshal(lal)
	if err != nil {
		return fmt.Errorf("marshal lal config: %w", err)
	}
	raw["lal"] = patchedLal

	// Patch lalmax section (http_config/srt_config).
	lalmax := map[string]json.RawMessage{}
	if maxRaw, ok := raw["lalmax"]; ok {
		if err := json.Unmarshal(maxRaw, &lalmax); err != nil {
			return fmt.Errorf("parse lalmax config: %w", err)
		}
	}

	httpCfg := map[string]any{}
	if httpRaw, ok := lalmax["http_config"]; ok {
		if err := json.Unmarshal(httpRaw, &httpCfg); err != nil {
			return fmt.Errorf("parse http_config: %w", err)
		}
	}
	httpCfg["http_listen_addr"] = fmt.Sprintf(":%d", config.DefaultLalmaxHTTPPort)
	patchedHTTPConfig, err := json.Marshal(httpCfg)
	if err != nil {
		return fmt.Errorf("marshal http_config: %w", err)
	}
	lalmax["http_config"] = patchedHTTPConfig

	srt := map[string]any{}
	if srtRaw, ok := lalmax["srt_config"]; ok {
		if err := json.Unmarshal(srtRaw, &srt); err != nil {
			return fmt.Errorf("parse srt config: %w", err)
		}
	}
	srt["enable"] = srtEnabled
	srt["addr"] = srtAddr
	patchedSRT, err := json.Marshal(srt)
	if err != nil {
		return fmt.Errorf("marshal srt config: %w", err)
	}
	lalmax["srt_config"] = patchedSRT

	patchedLalmax, err := json.Marshal(lalmax)
	if err != nil {
		return fmt.Errorf("marshal lalmax config: %w", err)
	}
	raw["lalmax"] = patchedLalmax

	patched, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal lalmax config: %w", err)
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, patched, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// patchLalmaxHLSConfig patches only HLS-related fields in the lalmax config JSON.
// This preserves user customizations and avoids restarting lalmax (which would
// interrupt active pull streams used for recording).
func patchLalmaxHLSConfig(path string, cfg EmbeddedLalmaxConfig) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read lalmax config: %w", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parse lalmax config: %w", err)
	}

	cleanupMode := cfg.LalCleanupMode
	if cleanupMode <= 0 {
		cleanupMode = 2
	}
	segmentCount := cfg.LalmaxSegmentCount
	if segmentCount <= 0 {
		segmentCount = 7
	}
	segmentDuration := cfg.LalmaxSegmentDuration
	if segmentDuration <= 0 {
		segmentDuration = 1
	}
	partDuration := cfg.LalmaxPartDuration
	if partDuration <= 0 {
		partDuration = 200
	}
	fragmentDurationMs := cfg.LalFragmentDurationMs
	if fragmentDurationMs <= 0 {
		fragmentDurationMs = 3000
	}
	fragmentNum := cfg.LalFragmentNum
	if fragmentNum <= 0 {
		fragmentNum = 6
	}

	if lalRaw, ok := raw["lal"]; ok {
		var lal map[string]json.RawMessage
		if err := json.Unmarshal(lalRaw, &lal); err == nil {
			idleTimeoutMs := cfg.HLSIdleTimeoutMs
			if idleTimeoutMs <= 0 {
				idleTimeoutMs = 60000
			}
			hls := map[string]any{
				"enable":                    cfg.HLSEnabled,
				"fragment_duration_ms":      fragmentDurationMs,
				"fragment_num":              fragmentNum,
				"cleanup_mode":              cleanupMode,
				"use_memory_as_disk_flag":   cfg.LalUseMemory,
				"on_demand":                 cfg.HLSOnDemand,
				"on_demand_idle_timeout_ms": idleTimeoutMs,
			}
			if existingRaw, ok := lal["hls"]; ok {
				var existing map[string]any
				if err := json.Unmarshal(existingRaw, &existing); err == nil {
					for k, v := range existing {
						if _, set := hls[k]; !set {
							hls[k] = v
						}
					}
				}
			}
			if patched, err := json.Marshal(hls); err == nil {
				lal["hls"] = patched
			}
			if patched, err := json.Marshal(lal); err == nil {
				raw["lal"] = patched
			}
		}
	}

	if maxRaw, ok := raw["lalmax"]; ok {
		var lalmax map[string]json.RawMessage
		if err := json.Unmarshal(maxRaw, &lalmax); err == nil {
			fmp4 := map[string]json.RawMessage{}
			if fmp4Raw, ok := lalmax["fmp4_config"]; ok {
				_ = json.Unmarshal(fmp4Raw, &fmp4)
			}
			idleTimeoutMs := cfg.HLSIdleTimeoutMs
			if idleTimeoutMs <= 0 {
				idleTimeoutMs = 60000
			}
			hls := map[string]any{
				"enable":                    cfg.HLSEnabled,
				"segment_count":             segmentCount,
				"segment_duration":          segmentDuration,
				"part_duration":             partDuration,
				"on_demand":                 cfg.HLSOnDemand,
				"on_demand_idle_timeout_ms": idleTimeoutMs,
			}
			if existingRaw, ok := fmp4["hls"]; ok {
				var existing map[string]any
				if err := json.Unmarshal(existingRaw, &existing); err == nil {
					for k, v := range existing {
						if _, set := hls[k]; !set {
							hls[k] = v
						}
					}
				}
			}
			if patched, err := json.Marshal(hls); err == nil {
				fmp4["hls"] = patched
			}
			if patched, err := json.Marshal(fmp4); err == nil {
				lalmax["fmp4_config"] = patched
			}
			if patched, err := json.Marshal(lalmax); err == nil {
				raw["lalmax"] = patched
			}
		}
	}

	patched, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal lalmax config: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, patched, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// SyncHLSFromConfig updates in-memory embedded lal/lalmax HLS settings from NVR config.
func (e *EmbeddedLalmax) SyncHLSFromConfig(hls config.HLSConfig) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cfg.HLSEnabled = hls.Enabled != nil && *hls.Enabled
	if hls.LalFragmentDurationMs > 0 {
		e.cfg.LalFragmentDurationMs = hls.LalFragmentDurationMs
	}
	if hls.LalFragmentNum > 0 {
		e.cfg.LalFragmentNum = hls.LalFragmentNum
	}
	if hls.LalCleanupMode > 0 {
		e.cfg.LalCleanupMode = hls.LalCleanupMode
	}
	e.cfg.LalUseMemory = hls.LalUseMemory
	if hls.SegmentCount > 0 {
		e.cfg.LalmaxSegmentCount = hls.SegmentCount
	}
	if hls.LalmaxSegmentDuration > 0 {
		e.cfg.LalmaxSegmentDuration = hls.LalmaxSegmentDuration
	}
	if hls.LalmaxPartDuration > 0 {
		e.cfg.LalmaxPartDuration = hls.LalmaxPartDuration
	}
	tmp := &config.Config{HLS: hls}
	e.cfg.HLSOnDemand = tmp.IsHLSOnDemand()
	e.cfg.HLSIdleTimeoutMs = int(tmp.HLSIdleTimeout() / time.Millisecond)
}

// ApplyHLSConfig syncs HLS settings, patches lalmax.conf.json, and updates the
// running lal/lalmax HLS pipeline without restarting lalmax.
func (e *EmbeddedLalmax) ApplyHLSConfig(hls config.HLSConfig) error {
	e.SyncHLSFromConfig(hls)
	e.mu.Lock()
	cfgPath := e.cfg.ConfigPath
	cfg := e.cfg
	server := e.server
	e.mu.Unlock()

	if server != nil {
		e.applyRuntimeHLSSettings(server, cfg)
	}

	if cfgPath == "" {
		return nil
	}
	if _, err := os.Stat(cfgPath); err != nil {
		raw, err := embeddedConfigJSON(cfg)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
			return err
		}
		return os.WriteFile(cfgPath, raw, 0o644)
	}
	return patchLalmaxHLSConfig(cfgPath, cfg)
}

func (e *EmbeddedLalmax) RegenerateConfig() error {
	e.mu.Lock()
	cfg := e.cfg
	e.mu.Unlock()

	raw, err := embeddedConfigJSON(cfg)
	if err != nil {
		return err
	}
	if cfg.ConfigPath == "" {
		return fmt.Errorf("no config path set")
	}
	if err := os.MkdirAll(filepath.Dir(cfg.ConfigPath), 0o755); err != nil {
		return err
	}
	slog.Info("regenerated lalmax config", "path", cfg.ConfigPath)
	return os.WriteFile(cfg.ConfigPath, raw, 0o644)
}

func (e *EmbeddedLalmax) applyRuntimeHLSSettings(server *lalmaxserver.LalMaxServer, cfg EmbeddedLalmaxConfig) {
	if server == nil {
		return
	}
	server.SetHlsEnabled(cfg.HLSEnabled)
	if !cfg.HLSEnabled {
		return
	}
	idleMs := cfg.HLSIdleTimeoutMs
	if idleMs <= 0 {
		idleMs = 60000
	}
	server.SetHlsOnDemand(cfg.HLSOnDemand, idleMs)
}

func embeddedConfigJSON(cfg EmbeddedLalmaxConfig) ([]byte, error) {
	addr := fmt.Sprintf(":%d", config.DefaultLalmaxHTTPPort)
	if parsed, err := url.Parse(cfg.HTTPAddr); err == nil && parsed.Host != "" {
		host := parsed.Host
		if strings.Contains(host, ":") {
			addr = ":" + strings.Split(host, ":")[1]
		}
	}
	rtmpAddr := fmt.Sprintf(":%d", cfg.RTMPPort)
	srtAddr := fmt.Sprintf(":%d", cfg.SRTPort)

	// Apply defaults for lalmax HLS settings
	segmentCount := cfg.LalmaxSegmentCount
	if segmentCount <= 0 {
		segmentCount = 7
	}
	segmentDuration := cfg.LalmaxSegmentDuration
	if segmentDuration <= 0 {
		segmentDuration = 1
	}
	partDuration := cfg.LalmaxPartDuration
	if partDuration <= 0 {
		partDuration = 200
	}
	idleTimeoutMs := cfg.HLSIdleTimeoutMs
	if idleTimeoutMs <= 0 {
		idleTimeoutMs = 60000
	}
	// Default HLS cleanup: delete segments ASAP after leaving playlist
	cleanupMode := cfg.LalCleanupMode
	if cleanupMode <= 0 {
		cleanupMode = 2 // CleanupModeAsap
	}

	cfgJSON := map[string]any{
		"lalmax": map[string]any{
			"server_id": "lalmax-nvr-embedded",
			"http_config": map[string]any{
				"http_listen_addr": addr,
				"enable_https":     false,
			},
			"srt_config": map[string]any{"enable": cfg.SRTEnabled, "addr": srtAddr},
			"rtc_config": map[string]any{
				"enable":           true,
				"ice_udp_mux_port": 4888,
				"ice_tcp_mux_port": 4888,
			},
			"fmp4_config": map[string]any{
				"http": map[string]any{"enable": true},
				"hls": map[string]any{
					"enable":                    cfg.HLSEnabled,
					"segment_count":             segmentCount,
					"segment_duration":          segmentDuration,
					"part_duration":             partDuration,
					"low_latency":               true,
					"on_demand":                 cfg.HLSOnDemand,
					"on_demand_idle_timeout_ms": idleTimeoutMs,
				},
			},
			"logic_config": map[string]any{"gop_cache_num": 1},
			"http_notify":  map[string]any{"enable": false},
		},
		"lal": map[string]any{
			"conf_version": "v0.4.1",
			"rtmp":         map[string]any{"enable": cfg.RTMPEnabled, "addr": rtmpAddr},
			"rtsp": map[string]any{
				"enable":      true,
				"addr":        fmt.Sprintf(":%d", config.DefaultLalRTSPPort),
				"auth_enable": cfg.RTSPAuthEnable,
				"auth_method": cfg.RTSPAuthMethod,
				"username":    cfg.RTSPUsername,
				"password":    cfg.RTSPPassword,
			},
			"httpflv":      map[string]any{"enable": true, "url_pattern": "/"},
			"default_http": map[string]any{"http_listen_addr": fmt.Sprintf(":%d", config.DefaultLalHTTPPort)},
			"http_api":     map[string]any{"enable": false},
			// Keep the native TS fan-out available to the NVR DLNA proxy. The lal
			// listener is not advertised to DLNA clients directly.
			"httpts": map[string]any{"enable": true, "url_pattern": "/live/", "gop_num": httptsGopNum(cfg.HTTPTSGOPNum)},
			"hls": map[string]any{
				"enable":                    cfg.HLSEnabled,
				"url_pattern":               "/hls/",
				"fragment_duration_ms":      cfg.LalFragmentDurationMs,
				"fragment_num":              cfg.LalFragmentNum,
				"cleanup_mode":              cleanupMode,
				"use_memory_as_disk_flag":   cfg.LalUseMemory,
				"on_demand":                 cfg.HLSOnDemand,
				"on_demand_idle_timeout_ms": idleTimeoutMs,
			},
			"record": map[string]any{"enable_flv": false},
			"log":    lalLogSection(lalLogLevelFromString(cfg.LalLogLevel)),
		},
	}
	return json.Marshal(cfgJSON)
}

func (e *EmbeddedLalmax) UpdateConfig(ctx context.Context, content []byte) error {
	if e.cfg.ConfigPath == "" {
		return fmt.Errorf("media.lalmax_config_path is required to update embedded lalmax config")
	}
	if !json.Valid(content) {
		return fmt.Errorf("lalmax config must be valid json")
	}
	if err := os.MkdirAll(filepath.Dir(e.cfg.ConfigPath), 0o755); err != nil {
		return err
	}
	tmp := e.cfg.ConfigPath + ".tmp"
	if err := os.WriteFile(tmp, content, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, e.cfg.ConfigPath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (e *EmbeddedLalmax) clearServer(server *lalmaxserver.LalMaxServer) {
	e.mu.Lock()
	if e.server == server {
		e.server = nil
	}
	e.mu.Unlock()
}

func (e *EmbeddedLalmax) shutdownAndClear(server *lalmaxserver.LalMaxServer) {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = server.Shutdown(shutdownCtx)
	cancel()
	e.clearServer(server)
}
