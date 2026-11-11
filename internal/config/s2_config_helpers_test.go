package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// s2BaseConfig returns a config with all defaults applied. Such a config passes
// Validate, so individual tests can mutate exactly one field to reach a branch.
func s2BaseConfig(t *testing.T) *Config {
	t.Helper()
	cfg := &Config{}
	cfg.ApplyDefaults()
	require.NoError(t, Validate(cfg))
	return cfg
}

func s2Bool(v bool) *bool { return &v }

func TestS2ConfigValidateRTSPAuth(t *testing.T) {
	// Disabled: no credentials required.
	require.NoError(t, MediaConfig{RTSPAuthEnable: false}.ValidateRTSPAuth())

	// Enabled without credentials.
	err := MediaConfig{RTSPAuthEnable: true}.ValidateRTSPAuth()
	require.Error(t, err)
	require.Contains(t, err.Error(), "rtsp_username")

	// Enabled with a password but blank username.
	err = MediaConfig{RTSPAuthEnable: true, RTSPUsername: "   ", RTSPPassword: "pw"}.ValidateRTSPAuth()
	require.Error(t, err)
	require.Contains(t, err.Error(), "rtsp_username")

	// Unsupported auth method.
	err = MediaConfig{RTSPAuthEnable: true, RTSPUsername: "u", RTSPPassword: "pw", RTSPAuthMethod: 2}.ValidateRTSPAuth()
	require.Error(t, err)
	require.Contains(t, err.Error(), "rtsp_auth_method")

	// Valid basic and digest configs.
	require.NoError(t, MediaConfig{RTSPAuthEnable: true, RTSPUsername: "u", RTSPPassword: "pw"}.ValidateRTSPAuth())
	require.NoError(t, MediaConfig{RTSPAuthEnable: true, RTSPUsername: "u", RTSPPassword: "pw", RTSPAuthMethod: 1}.ValidateRTSPAuth())
}

func TestS2ConfigNormalizeRTSPTransport(t *testing.T) {
	require.Equal(t, "udp", NormalizeRTSPTransport("udp"))
	require.Equal(t, "udp", NormalizeRTSPTransport("  UDP "))
	require.Equal(t, "tcp", NormalizeRTSPTransport("tcp"))
	require.Equal(t, "tcp", NormalizeRTSPTransport(""))
	require.Equal(t, "tcp", NormalizeRTSPTransport("sctp"))
}

func TestS2CameraSupportsAudioRecording(t *testing.T) {
	require.True(t, CameraSupportsAudioRecording(CameraConfig{Protocol: "rtsp", Encoding: "h264"}))
	require.True(t, CameraSupportsAudioRecording(CameraConfig{Protocol: "onvif", Encoding: "H265"}))
	require.False(t, CameraSupportsAudioRecording(CameraConfig{Protocol: "rtsp", Encoding: "mjpeg"}))
	require.False(t, CameraSupportsAudioRecording(CameraConfig{Protocol: "rtsp", Encoding: ""}))
	require.False(t, CameraSupportsAudioRecording(CameraConfig{Protocol: "rtsp", Encoding: "vp9"}))
	// Non-recordable protocols short-circuit before the encoding check.
	require.False(t, CameraSupportsAudioRecording(CameraConfig{Protocol: "http", Encoding: "h264"}))
}

func TestS2ApplyCameraAudioDefault(t *testing.T) {
	// nil camera is ignored.
	ApplyCameraAudioDefault(nil)

	// MJPEG cameras cannot carry the audio track; an explicit opt-in is reverted.
	cam := &CameraConfig{ID: "mjpeg", Protocol: "rtsp", Encoding: "mjpeg", AudioEnabled: true}
	ApplyCameraAudioDefault(cam)
	require.False(t, cam.AudioEnabled)

	// Supported codec gets audio enabled implicitly.
	cam = &CameraConfig{ID: "h264", Protocol: "rtsp", Encoding: "h264"}
	ApplyCameraAudioDefault(cam)
	require.True(t, cam.AudioEnabled)
}

func TestS2ConfigSmallHelpers(t *testing.T) {
	// LocalTracesEnabled defaults to true when unset.
	require.True(t, ObservabilityConfig{}.LocalTracesEnabled())
	require.False(t, ObservabilityConfig{LocalTraceEnabled: s2Bool(false)}.LocalTracesEnabled())

	// IsWHIPEnabled defaults to true, including a nil receiver.
	var nilCfg *Config
	require.True(t, nilCfg.IsWHIPEnabled())
	require.True(t, (&Config{}).IsWHIPEnabled())
	require.False(t, (&Config{WHIP: WHIPConfig{Enabled: s2Bool(false)}}).IsWHIPEnabled())

	// IsHLSOnDemand defaults to true when unset.
	require.True(t, (&Config{}).IsHLSOnDemand())

	// HLSIdleTimeout falls back to 60s for empty/invalid/non-positive values.
	require.Equal(t, 60*time.Second, (&Config{}).HLSIdleTimeout())
	require.Equal(t, 60*time.Second, (&Config{HLS: HLSConfig{IdleTimeout: "nope"}}).HLSIdleTimeout())
	require.Equal(t, 60*time.Second, (&Config{HLS: HLSConfig{IdleTimeout: "0s"}}).HLSIdleTimeout())
	require.Equal(t, 5*time.Minute, (&Config{HLS: HLSConfig{IdleTimeout: "5m"}}).HLSIdleTimeout())
}

func TestS2ParseListenPort(t *testing.T) {
	cases := map[string]int{
		"":             9090,
		"   ":          9090,
		":9090":        9090,
		"9091":         9091,
		"0":            9090,
		"abcd":         9090,
		"0.0.0.0:8080": 8080,
		"[::1]:7070":   7070,
		"not:a:port":   9090,
		"127.0.0.1:":   9090,
		"127.0.0.1:x":  9090,
	}
	for input, want := range cases {
		require.Equal(t, want, ParseListenPort(input), "input=%q", input)
	}
}

func TestS2ApplyDefaultsExtraBranches(t *testing.T) {
	// IPTV concurrent pulls are capped at 128.
	cfg := &Config{IPTV: IPTVConfig{MaxConcurrentPulls: 500}}
	cfg.ApplyDefaults()
	require.Equal(t, 128, cfg.IPTV.MaxConcurrentPulls)

	// HLS-based default protocol falls back to webrtc when HLS is disabled.
	cfg = &Config{Streaming: StreamingConfig{DefaultProtocol: "ll-hls"}}
	cfg.ApplyDefaults()
	require.Equal(t, "webrtc", cfg.Streaming.DefaultProtocol)

	// Multimodal AI provider defaults.
	cfg = &Config{AI: AIConfig{Multimodal: &AIMultimodalConfig{
		Enabled:   true,
		Providers: map[string]AIProviderConfig{"deepseek": {}},
	}}}
	cfg.ApplyDefaults()
	mm := cfg.AI.Multimodal
	require.Equal(t, "deepseek", mm.Provider)
	require.Equal(t, "5m", mm.AnalysisInterval)
	require.Equal(t, 1000, mm.MaxResults)
	prov := mm.Providers["deepseek"]
	require.Equal(t, 60, prov.Timeout)
	require.Equal(t, 2000, prov.MaxTokens)
	require.InDelta(t, 0.7, prov.Temperature, 1e-6)

	// GB28181 cameras get an informational h264 encoding.
	cfg = &Config{Cameras: []CameraConfig{{ID: "gb", Protocol: "gb28181"}}}
	cfg.ApplyDefaults()
	require.Equal(t, "h264", cfg.Cameras[0].Encoding)
}

func TestS2ResolveMergeConfigExtraBranches(t *testing.T) {
	global := MergeConfig{CheckInterval: "1h", WindowSize: "5m", BatchLimit: 200}
	perCamera := &MergeConfig{
		Enabled:         true,
		RollingEnabled:  s2Bool(true),
		RollingDebounce: "10s",
	}
	got := ResolveMergeConfig(global, perCamera)
	require.True(t, got.Enabled)
	require.NotNil(t, got.RollingEnabled)
	require.True(t, *got.RollingEnabled)
	require.Equal(t, "10s", got.RollingDebounce)
	require.Equal(t, "1h", got.CheckInterval)
}

func TestS2ResolveHealthOverrides(t *testing.T) {
	global := HealthConfig{
		Layer1:   HealthLayer1Config{OfflineThreshold: "30s"},
		Layer2:   HealthLayer2Config{BitrateChangeThreshold: 0.5, MinFPS: 5, MaxIDRInterval: "10s"},
		Layer2_5: HealthLayer2_5Config{FreezeTimeout: "20s"},
	}
	// No overrides: global values win.
	got := ResolveHealthOverrides(global, HealthOverrides{})
	require.Equal(t, "10s", got.MaxIDRInterval)
	require.Equal(t, 0.5, got.BitrateChangeThreshold)
	require.Equal(t, 5, got.MinFPS)
	require.Equal(t, "30s", got.OfflineThreshold)
	require.Equal(t, "20s", got.FreezeTimeout)

	// Every override is applied.
	got = ResolveHealthOverrides(global, HealthOverrides{
		MaxIDRInterval:         "15s",
		BitrateChangeThreshold: 0.8,
		MinFPS:                 12,
		OfflineThreshold:       "45s",
		FreezeTimeout:          "25s",
	})
	require.Equal(t, "15s", got.MaxIDRInterval)
	require.Equal(t, 0.8, got.BitrateChangeThreshold)
	require.Equal(t, 12, got.MinFPS)
	require.Equal(t, "45s", got.OfflineThreshold)
	require.Equal(t, "25s", got.FreezeTimeout)
}

func TestS2ConfigLoadUnmarshalError(t *testing.T) {
	path := writeTempYAML(t, "server: [this is not valid yaml")
	_, err := Load(path)
	require.Error(t, err)
}

func TestS2EncryptConfigFileBranches(t *testing.T) {
	t.Run("load error", func(t *testing.T) {
		s2ResetEncryptionState(t)
		helperSetEnvKey(t, helperGenerateKey(t))
		_, err := EncryptConfigFile(filepath.Join(t.TempDir(), "missing.yaml"))
		require.Error(t, err)
		require.Contains(t, err.Error(), "load config")
	})

	t.Run("nothing to encrypt", func(t *testing.T) {
		s2ResetEncryptionState(t)
		helperSetEnvKey(t, helperGenerateKey(t))
		path := writeTempYAML(t, "server:\n  listen: \":9090\"\n")
		fields, err := EncryptConfigFile(path)
		require.NoError(t, err)
		require.Empty(t, fields)
	})

	t.Run("save error", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("directory permissions are not enforced for root")
		}
		s2ResetEncryptionState(t)
		helperSetEnvKey(t, helperGenerateKey(t))
		dir := t.TempDir()
		path := filepath.Join(dir, "cfg.yaml")
		require.NoError(t, os.WriteFile(path, []byte("auth:\n  password: plain-secret\n"), 0o600))
		require.NoError(t, os.Chmod(dir, 0o500))
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		_, err := EncryptConfigFile(path)
		require.Error(t, err)
		require.Contains(t, err.Error(), "save encrypted config")
	})
}

func TestS2ConfigP0Accessors(t *testing.T) {
	// Auto-discover hello listener requires the module to be enabled.
	require.False(t, AutoDiscoverConfig{}.HelloListenEnabled())
	require.True(t, AutoDiscoverConfig{Enabled: s2Bool(true)}.HelloListenEnabled())
	require.False(t, AutoDiscoverConfig{Enabled: s2Bool(true), ListenForHello: s2Bool(false)}.HelloListenEnabled())

	// Scan interval falls back to 60s below the 30s floor.
	require.Equal(t, 60*time.Second, AutoDiscoverConfig{}.ScanIntervalDuration())
	require.Equal(t, 60*time.Second, AutoDiscoverConfig{ScanInterval: "10s"}.ScanIntervalDuration())
	require.Equal(t, 60*time.Second, AutoDiscoverConfig{ScanInterval: "nope"}.ScanIntervalDuration())
	require.Equal(t, 2*time.Minute, AutoDiscoverConfig{ScanInterval: "2m"}.ScanIntervalDuration())

	// Rediscovery probe timeout defaults to 2s.
	require.Equal(t, 2*time.Second, RediscoveryConfig{}.ProbeTimeoutDuration())
	require.Equal(t, 2*time.Second, RediscoveryConfig{ProbeTimeout: "0s"}.ProbeTimeoutDuration())
	require.Equal(t, 5*time.Second, RediscoveryConfig{ProbeTimeout: "5s"}.ProbeTimeoutDuration())
	require.True(t, RediscoveryConfig{}.IsEnabled())
	require.False(t, RediscoveryConfig{Enabled: s2Bool(false)}.IsEnabled())

	// Event window durations.
	require.Equal(t, 30*time.Second, EventConfig{}.PostRollDuration())
	require.Equal(t, 30*time.Second, EventConfig{PostRoll: "-1s"}.PostRollDuration())
	require.Equal(t, time.Minute, EventConfig{PostRoll: "1m"}.PostRollDuration())
	require.Equal(t, 5*time.Minute, EventConfig{}.MaxDurationValue())
	require.Equal(t, 5*time.Minute, EventConfig{MaxDuration: "nope"}.MaxDurationValue())
	require.Equal(t, 10*time.Minute, EventConfig{MaxDuration: "10m"}.MaxDurationValue())

	// Timelapse interval clamps into [5s, 10m].
	var nilAdaptive *CameraAdaptiveConfig
	require.Equal(t, 30*time.Second, nilAdaptive.TimelapseIntervalDuration())
	require.Equal(t, 30*time.Second, (&CameraAdaptiveConfig{}).TimelapseIntervalDuration())
	require.Equal(t, 30*time.Second, (&CameraAdaptiveConfig{TimelapseInterval: "1s"}).TimelapseIntervalDuration())
	require.Equal(t, 10*time.Minute, (&CameraAdaptiveConfig{TimelapseInterval: "1h"}).TimelapseIntervalDuration())
	require.Equal(t, 10*time.Second, (&CameraAdaptiveConfig{TimelapseInterval: "10s"}).TimelapseIntervalDuration())

	// cloneStringMap preserves nil (used by Save for optional maps).
	require.Nil(t, cloneStringMap(nil))
}
