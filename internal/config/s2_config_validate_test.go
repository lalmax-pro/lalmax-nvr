package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestS2ConfigValidateBranches drives Validate into each error/edge branch by
// mutating a single field of an otherwise valid config.
func TestS2ConfigValidateBranches(t *testing.T) {
	s2RTSPCamera := func(extra func(*CameraConfig)) []CameraConfig {
		cam := CameraConfig{ID: "c1", Protocol: "rtsp", Encoding: "h264", URL: "rtsp://192.168.1.10/stream"}
		if extra != nil {
			extra(&cam)
		}
		return []CameraConfig{cam}
	}

	cases := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{
			name:   "camera url required",
			mutate: func(c *Config) { c.Cameras = []CameraConfig{{ID: "c1", Protocol: "rtsp"}} },
			want:   "url is required",
		},
		{
			name:   "onvif endpoint required",
			mutate: func(c *Config) { c.Cameras = []CameraConfig{{ID: "c1", Protocol: "onvif"}} },
			want:   "url or onvif_endpoint is required",
		},
		{
			name: "legacy protocol invalid",
			mutate: func(c *Config) {
				c.Cameras = s2RTSPCamera(func(cam *CameraConfig) { cam.Protocol = "rtsp_vp9" })
			},
			want: "protocol invalid",
		},
		{
			name: "health max idr interval invalid",
			mutate: func(c *Config) {
				c.Cameras = s2RTSPCamera(func(cam *CameraConfig) { cam.HealthOverrides.MaxIDRInterval = "nope" })
			},
			want: "max_idr_interval invalid duration",
		},
		{
			name: "health offline threshold invalid",
			mutate: func(c *Config) {
				c.Cameras = s2RTSPCamera(func(cam *CameraConfig) { cam.HealthOverrides.OfflineThreshold = "nope" })
			},
			want: "offline_threshold invalid duration",
		},
		{
			name: "health freeze timeout invalid",
			mutate: func(c *Config) {
				c.Cameras = s2RTSPCamera(func(cam *CameraConfig) { cam.HealthOverrides.FreezeTimeout = "nope" })
			},
			want: "freeze_timeout invalid duration",
		},
		{
			name: "health bitrate threshold out of range",
			mutate: func(c *Config) {
				c.Cameras = s2RTSPCamera(func(cam *CameraConfig) { cam.HealthOverrides.BitrateChangeThreshold = 2 })
			},
			want: "bitrate_change_threshold must be between 0 and 1",
		},
		{
			name: "health min fps negative",
			mutate: func(c *Config) {
				c.Cameras = s2RTSPCamera(func(cam *CameraConfig) { cam.HealthOverrides.MinFPS = -1 })
			},
			want: "min_fps must be >= 0",
		},
		{
			name:   "media mode invalid",
			mutate: func(c *Config) { c.Media.Mode = "rtmp" },
			want:   "media.mode invalid",
		},
		{
			name:   "media lalmax http addr required",
			mutate: func(c *Config) { c.Media.LalmaxHTTPAddr = "  " },
			want:   "media.lalmax_http_addr is required",
		},
		{
			name:   "media lalmax http addr invalid",
			mutate: func(c *Config) { c.Media.LalmaxHTTPAddr = "http://[::1" },
			want:   "media.lalmax_http_addr invalid",
		},
		{
			name:   "trace retention invalid",
			mutate: func(c *Config) { c.Observability.TraceRetention = "nope" },
			want:   "trace_retention must be a positive duration",
		},
		{
			name:   "max local traces out of range",
			mutate: func(c *Config) { c.Observability.MaxLocalTraces = 50 },
			want:   "max_local_traces must be between 100 and 1000000",
		},
		{
			name:   "trace ui url template missing placeholder",
			mutate: func(c *Config) { c.Observability.TraceUIURLTemplate = "https://traces.example.com/search" },
			want:   "trace_ui_url_template must be an http(s) URL containing {trace_id}",
		},
		{
			name: "otlp endpoint with query",
			mutate: func(c *Config) {
				c.Observability.OTLP.Enabled = true
				c.Observability.OTLP.Endpoint = "http://127.0.0.1:4318/?debug=1"
			},
			want: "otlp.endpoint must not contain a query or fragment",
		},
		{
			name: "otlp service name required",
			mutate: func(c *Config) {
				c.Observability.OTLP.Enabled = true
				c.Observability.OTLP.ServiceName = "  "
			},
			want: "otlp.service_name is required",
		},
		{
			name:   "remote log endpoint required",
			mutate: func(c *Config) { c.RemoteLog = RemoteLogConfig{Enabled: true, Format: "jsonline"} },
			want:   "remote_log.endpoint is required",
		},
		{
			name: "remote log format invalid",
			mutate: func(c *Config) {
				c.RemoteLog = RemoteLogConfig{Enabled: true, Endpoint: "http://127.0.0.1:9428/insert/jsonline", Format: "csv"}
			},
			want: "remote_log.format must be",
		},
		{
			name: "merge rolling debounce invalid",
			mutate: func(c *Config) {
				c.Merge.Enabled = true
				c.Merge.CheckInterval = "1h"
				c.Merge.WindowSize = "5m"
				c.Merge.BatchLimit = 200
				c.Merge.MinSegmentAge = "1m"
				c.Merge.MinSegmentsToMerge = 2
				c.Merge.RollingDebounce = "nope"
			},
			want: "invalid merge rolling_debounce",
		},
		{
			name: "camera timelapse interval invalid",
			mutate: func(c *Config) {
				c.Cameras = s2RTSPCamera(func(cam *CameraConfig) {
					cam.Timelapse = &CameraTimelapseConfig{Enabled: true, Interval: "nope", OutputFPS: 30}
				})
			},
			want: "timelapse.interval invalid duration",
		},
		{
			name:   "hls idle timeout non positive",
			mutate: func(c *Config) { c.HLS.IdleTimeout = "0s" },
			want:   "hls.idle_timeout must be > 0",
		},
		{
			name:   "dlna browse count out of range",
			mutate: func(c *Config) { c.DLNA.MaxBrowseCount = 0 },
			want:   "dlna.max_browse_count must be between 1 and 1000",
		},
		{
			name:   "dlna media viewers out of range",
			mutate: func(c *Config) { c.DLNA.MaxMediaViewers = 0 },
			want:   "dlna.max_media_viewers must be between 1 and 100",
		},
		{
			name:   "dlna port out of range",
			mutate: func(c *Config) { c.DLNA.Port = 70000 },
			want:   "dlna.port must be between 1 and 65535",
		},
		{
			name: "dlna port conflicts with http listen",
			mutate: func(c *Config) {
				c.Server.Listen = ":8200"
				c.DLNA.Enabled = s2Bool(true)
				c.DLNA.Port = 8200
			},
			want: "must be different from the HTTP listen port",
		},
		{
			name:   "dlna advertise url not http",
			mutate: func(c *Config) { c.DLNA.AdvertiseURL = "https://10.0.0.5:8200/" },
			want:   "dlna.advertise_url must be an http URL",
		},
		{
			name:   "dlna advertise url loopback",
			mutate: func(c *Config) { c.DLNA.AdvertiseURL = "http://127.0.0.1:8200/" },
			want:   "must be reachable from the LAN, not loopback",
		},
		{
			name:   "dlna allowed cidrs invalid",
			mutate: func(c *Config) { c.DLNA.AllowedCIDRs = []string{"10.0.0.0/8", "not-a-cidr"} },
			want:   "dlna.allowed_cidrs contains invalid CIDR",
		},
		{
			name:   "dlna gop cache out of range",
			mutate: func(c *Config) { c.DLNA.GopCache = 20 },
			want:   "dlna.gop_cache must be between 1 and 16",
		},
		{
			name:   "webrtc idle timeout invalid",
			mutate: func(c *Config) { c.Streaming.WebRTC.IdleTimeout = "nope" },
			want:   "streaming.webrtc.idle_timeout invalid",
		},
		{
			name:   "flv idle timeout invalid",
			mutate: func(c *Config) { c.Streaming.FLV.IdleTimeout = "nope" },
			want:   "streaming.flv.idle_timeout invalid",
		},
		{
			name: "health auto remediation offline restart seconds",
			mutate: func(c *Config) {
				c.Health.Enabled = true
				c.Health.AutoRemediation = HealthAutoRemediationConfig{
					Enabled:               true,
					MaxRestartsPerHour:    3,
					CooldownMinutes:       5,
					OfflineRestartSeconds: 0,
				}
			},
			want: "offline_restart_seconds must be >= 1",
		},
		{
			name: "auto discover scan interval invalid",
			mutate: func(c *Config) {
				c.AutoDiscover.Enabled = s2Bool(true)
				c.AutoDiscover.ScanInterval = "nope"
			},
			want: "auto_discover.scan_interval invalid",
		},
		{
			name:   "rediscovery max parallel invalid",
			mutate: func(c *Config) { c.Health.Rediscovery.MaxParallel = 0 },
			want:   "health.rediscovery.max_parallel must be >= 1",
		},
		{
			name:   "rediscovery probe timeout invalid",
			mutate: func(c *Config) { c.Health.Rediscovery.ProbeTimeout = "nope" },
			want:   "health.rediscovery.probe_timeout invalid",
		},
		{
			name:   "event post roll invalid",
			mutate: func(c *Config) { c.Event.PostRoll = "nope" },
			want:   "event.post_roll invalid",
		},
		{
			name:   "event max duration invalid",
			mutate: func(c *Config) { c.Event.MaxDuration = "nope" },
			want:   "event.max_duration invalid",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := s2BaseConfig(t)
			tc.mutate(cfg)
			err := Validate(cfg)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want)
		})
	}
}

// TestS2ConfigValidateAcceptance covers the DLNA port fallback and the legacy
// combined protocol conversion, which are only reachable on the success path.
func TestS2ConfigValidateAcceptance(t *testing.T) {
	cfg := s2BaseConfig(t)
	// Port 0 falls back to DefaultDLNAPort without tripping validation.
	cfg.DLNA.Port = 0
	require.NoError(t, Validate(cfg))

	// Legacy combined protocol is parsed and validated.
	cfg = s2BaseConfig(t)
	cfg.Cameras = []CameraConfig{{ID: "c1", Protocol: "rtsp_h264", Encoding: "h264", URL: "rtsp://192.168.1.10/stream"}}
	require.NoError(t, Validate(cfg))
}
