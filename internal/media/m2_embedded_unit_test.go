package media

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lal/pkg/logic"
	"github.com/stretchr/testify/require"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
)

func m2Enabled(v bool) *bool { return &v }

func TestM2LalmaxListenAddr(t *testing.T) {
	require.Equal(t, ":8080", lalmaxListenAddr("http://0.0.0.0:8080"))
	require.Equal(t, ":12090", lalmaxListenAddr("http://example.test"))
	require.Equal(t, ":12090", lalmaxListenAddr("%zz"))
	require.Equal(t, ":12090", lalmaxListenAddr("http://[::1]"))
	require.Equal(t, ":99", lalmaxListenAddr("http://127.0.0.1:99"))
}

func TestM2LalLogLevelFromString(t *testing.T) {
	require.Equal(t, 1, lalLogLevelFromString(" DEBUG "))
	require.Equal(t, 2, lalLogLevelFromString("info"))
	require.Equal(t, 2, lalLogLevelFromString(""))
	require.Equal(t, 3, lalLogLevelFromString("Warn"))
	require.Equal(t, 4, lalLogLevelFromString("error"))
}

func TestM2EmbeddedConfigPath(t *testing.T) {
	e := &EmbeddedLalmax{cfg: EmbeddedLalmaxConfig{ConfigPath: "/tmp/m2.conf.json"}}
	require.Equal(t, "/tmp/m2.conf.json", e.ConfigPath())
}

func TestM2EmbeddedNilServerBranches(t *testing.T) {
	e := &EmbeddedLalmax{}
	require.Error(t, e.Ready(context.Background()))
	require.Nil(t, e.PlayHTTPHandler())
	require.Error(t, e.Start(context.Background()))
	require.NoError(t, e.Shutdown(context.Background()))
	require.Error(t, e.RegenerateConfig())

	_, err := e.AddCustomizePubSession(context.Background(), "stream")
	require.Error(t, err)
	require.Error(t, e.DelCustomizePubSession(context.Background(), nil))
	require.Error(t, e.DelCustomizePubSession(context.Background(), &customizePubSessionWrapper{}))

	// JT1078 audio validation runs before the nil server check.
	require.Error(t, e.PushJT1078Audio("sim", 1, 25, []byte{1}))
	require.Error(t, e.PushJT1078Audio("sim", 1, 7, nil))
	require.Error(t, e.PushJT1078Audio("sim", 1, 7, []byte{0x01}))
}

type m2FakePubCtx struct {
	logic.ICustomizePubSessionContext
	packets []base.AvPacket
	msgs    []base.RtmpMsg
	options []func(*base.AvPacketStreamOption)
	asc     [][]byte
	resets  int
}

func (f *m2FakePubCtx) FeedAvPacket(packet base.AvPacket) error {
	f.packets = append(f.packets, packet)
	return nil
}

func (f *m2FakePubCtx) FeedRtmpMsg(msg base.RtmpMsg) error {
	f.msgs = append(f.msgs, msg)
	return nil
}

func (f *m2FakePubCtx) WithOption(modOption func(*base.AvPacketStreamOption)) {
	f.options = append(f.options, modOption)
}

func (f *m2FakePubCtx) FeedAudioSpecificConfig(asc []byte) error {
	f.asc = append(f.asc, asc)
	return nil
}

func (f *m2FakePubCtx) ResetMedia() error {
	f.resets++
	return nil
}

func TestM2CustomizePubSessionWrapperForwards(t *testing.T) {
	fake := &m2FakePubCtx{}
	w := &customizePubSessionWrapper{ctx: fake}
	require.NoError(t, w.FeedAvPacket(base.AvPacket{PayloadType: base.AvPacketPtG711A}))
	require.NoError(t, w.FeedRtmpMsg(base.RtmpMsg{}))
	w.WithOption(func(*base.AvPacketStreamOption) {})
	require.NoError(t, w.FeedAudioSpecificConfig([]byte{0x11, 0x90}))
	require.NoError(t, w.ResetMedia())
	require.Len(t, fake.packets, 1)
	require.Len(t, fake.msgs, 1)
	require.Len(t, fake.options, 1)
	require.Len(t, fake.asc, 1)
	require.Equal(t, 1, fake.resets)

	// A context without ResetMedia support is a no-op.
	plain := &customizePubSessionWrapper{}
	require.NoError(t, plain.ResetMedia())
}

func TestM2EmbeddedRegenerateConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "lalmax.json")
	e := &EmbeddedLalmax{cfg: EmbeddedLalmaxConfig{ConfigPath: path, HLSEnabled: true, RTMPEnabled: true}}
	require.NoError(t, e.RegenerateConfig())
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(raw, &parsed))
	require.Contains(t, parsed, "lalmax")
	require.Contains(t, parsed, "lal")
}

func TestM2EmbeddedUpdateConfig(t *testing.T) {
	e := &EmbeddedLalmax{}
	require.Error(t, e.UpdateConfig(context.Background(), []byte(`{}`)))

	path := filepath.Join(t.TempDir(), "sub", "lalmax.json")
	e.cfg.ConfigPath = path
	require.Error(t, e.UpdateConfig(context.Background(), []byte("not-json")))
	require.NoError(t, e.UpdateConfig(context.Background(), []byte(`{"lal":{}}`)))
	stored, err := os.ReadFile(path)
	require.NoError(t, err)
	require.JSONEq(t, `{"lal":{}}`, string(stored))

	// Renaming the temp file over a directory fails and cleans up the temp file.
	dir := t.TempDir()
	e.cfg.ConfigPath = dir
	require.Error(t, e.UpdateConfig(context.Background(), []byte(`{}`)))
	_, err = os.Stat(dir + ".tmp")
	require.True(t, os.IsNotExist(err))
}

func TestM2EmbeddedApplyHLSConfigBranches(t *testing.T) {
	// No config path configured: only in-memory settings change.
	e := &EmbeddedLalmax{}
	require.NoError(t, e.ApplyHLSConfig(config.HLSConfig{Enabled: m2Enabled(true), SegmentCount: 9}))
	require.True(t, e.cfg.HLSEnabled)
	require.Equal(t, 9, e.cfg.LalmaxSegmentCount)

	// Missing config file: the defaults are written.
	path := filepath.Join(t.TempDir(), "deep", "lalmax.json")
	e = &EmbeddedLalmax{cfg: EmbeddedLalmaxConfig{ConfigPath: path}}
	require.NoError(t, e.ApplyHLSConfig(config.HLSConfig{Enabled: m2Enabled(false)}))
	_, err := os.Stat(path)
	require.NoError(t, err)

	// Invalid JSON in an existing file surfaces the patch error.
	bad := filepath.Join(t.TempDir(), "bad.json")
	require.NoError(t, os.WriteFile(bad, []byte("{"), 0o644))
	e = &EmbeddedLalmax{cfg: EmbeddedLalmaxConfig{ConfigPath: bad}}
	require.Error(t, e.ApplyHLSConfig(config.HLSConfig{}))
}

func TestM2EmbeddedSyncHLSFromConfig(t *testing.T) {
	e := &EmbeddedLalmax{cfg: EmbeddedLalmaxConfig{
		LalFragmentDurationMs: 3000, LalFragmentNum: 6, LalCleanupMode: 1,
		LalmaxSegmentCount: 7, LalmaxSegmentDuration: 1, LalmaxPartDuration: 200,
	}}
	e.SyncHLSFromConfig(config.HLSConfig{
		Enabled:               m2Enabled(true),
		LalFragmentDurationMs: 4000,
		LalFragmentNum:        8,
		LalCleanupMode:        2,
		LalUseMemory:          true,
		SegmentCount:          10,
		LalmaxSegmentDuration: 2,
		LalmaxPartDuration:    300,
	})
	require.True(t, e.cfg.HLSEnabled)
	require.Equal(t, 4000, e.cfg.LalFragmentDurationMs)
	require.Equal(t, 8, e.cfg.LalFragmentNum)
	require.Equal(t, 2, e.cfg.LalCleanupMode)
	require.True(t, e.cfg.LalUseMemory)
	require.Equal(t, 10, e.cfg.LalmaxSegmentCount)
	require.Equal(t, 2, e.cfg.LalmaxSegmentDuration)
	require.Equal(t, 300, e.cfg.LalmaxPartDuration)

	// Zero values keep the previous runtime settings.
	e.SyncHLSFromConfig(config.HLSConfig{})
	require.False(t, e.cfg.HLSEnabled)
	require.Equal(t, 4000, e.cfg.LalFragmentDurationMs)
	require.Equal(t, 10, e.cfg.LalmaxSegmentCount)
}

func TestM2LoadEmbeddedLalmaxConfigBranches(t *testing.T) {
	// No config path: build from in-memory defaults.
	cfg, err := loadEmbeddedLalmaxConfig(EmbeddedLalmaxConfig{})
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Corrupt existing file.
	bad := filepath.Join(t.TempDir(), "bad.json")
	require.NoError(t, os.WriteFile(bad, []byte("{"), 0o644))
	_, err = loadEmbeddedLalmaxConfig(EmbeddedLalmaxConfig{ConfigPath: bad})
	require.Error(t, err)

	// Missing file is generated.
	missing := filepath.Join(t.TempDir(), "new", "lalmax.json")
	_, err = loadEmbeddedLalmaxConfig(EmbeddedLalmaxConfig{ConfigPath: missing})
	require.NoError(t, err)
	_, err = os.Stat(missing)
	require.NoError(t, err)

	// Unwritable parent: a path whose parent is a regular file.
	file := filepath.Join(t.TempDir(), "regular")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o644))
	_, err = loadEmbeddedLalmaxConfig(EmbeddedLalmaxConfig{ConfigPath: filepath.Join(file, "lalmax.json")})
	require.Error(t, err)

	// Low latency HLS with too few segments is corrected in place.
	fix := filepath.Join(t.TempDir(), "fix.json")
	require.NoError(t, os.WriteFile(fix, []byte(`{
		"lal": {"rtmp": {"enable": true, "addr": ":11935"}},
		"lalmax": {"fmp4_config": {"hls": {"enable": true, "low_latency": true, "segment_count": 3}}}
	}`), 0o644))
	fixed, err := loadEmbeddedLalmaxConfig(EmbeddedLalmaxConfig{ConfigPath: fix})
	require.NoError(t, err)
	require.GreaterOrEqual(t, fixed.Fmp4Config.Hls.SegmentCount, 7)
}

func TestM2ConfigSectionErrors(t *testing.T) {
	write := func(t *testing.T, content string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "lalmax.json")
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
		return path
	}

	t.Run("ensureLalLogConfig", func(t *testing.T) {
		require.Error(t, ensureLalLogConfig(filepath.Join(t.TempDir(), "absent.json"), "info"))
		require.Error(t, ensureLalLogConfig(write(t, "{"), "info"))
		require.Error(t, ensureLalLogConfig(write(t, `{"lal": 5}`), "info"))
	})

	t.Run("ensureJT1078Config", func(t *testing.T) {
		require.Error(t, ensureJT1078Config(filepath.Join(t.TempDir(), "absent.json")))
		require.Error(t, ensureJT1078Config(write(t, "{")))
		require.Error(t, ensureJT1078Config(write(t, `{"lalmax": 5}`)))
		path := write(t, `{"lalmax": {}}`)
		require.NoError(t, ensureJT1078Config(path))
	})

	t.Run("patchLalmaxHLSConfig", func(t *testing.T) {
		require.Error(t, patchLalmaxHLSConfig(filepath.Join(t.TempDir(), "absent.json"), EmbeddedLalmaxConfig{}))
		require.Error(t, patchLalmaxHLSConfig(write(t, "{"), EmbeddedLalmaxConfig{}))
		// Non-object sections are skipped instead of failing the patch.
		require.NoError(t, patchLalmaxHLSConfig(write(t, `{"lal": 5}`), EmbeddedLalmaxConfig{}))
		require.NoError(t, patchLalmaxHLSConfig(write(t, `{"lalmax": 5}`), EmbeddedLalmaxConfig{}))
		require.NoError(t, patchLalmaxHLSConfig(write(t, `{"lal":{"hls":5}}`), EmbeddedLalmaxConfig{}))
		require.NoError(t, patchLalmaxHLSConfig(write(t, `{"lalmax":{"fmp4_config":{"hls":5}}}`), EmbeddedLalmaxConfig{}))
		require.NoError(t, patchLalmaxHLSConfig(write(t, `{"lal":{"hls":{"keep":true}},"lalmax":{"fmp4_config":{"hls":{"keep":true}}}}`), EmbeddedLalmaxConfig{HLSEnabled: true}))
	})

	t.Run("patchLalmaxConfig", func(t *testing.T) {
		require.Error(t, patchLalmaxConfig(filepath.Join(t.TempDir(), "absent.json"), true, true, ":1", ":2", 0, false))
		require.Error(t, patchLalmaxConfig(write(t, "{"), true, true, ":1", ":2", 0, false))
		require.Error(t, patchLalmaxConfig(write(t, `{"lal": 5}`), true, true, ":1", ":2", 0, false))
		require.Error(t, patchLalmaxConfig(write(t, `{"lal": {"rtmp": 5}}`), true, true, ":1", ":2", 0, false))
		require.Error(t, patchLalmaxConfig(write(t, `{"lal": {"rtsp": 5}}`), true, true, ":1", ":2", 0, false))
		require.Error(t, patchLalmaxConfig(write(t, `{"lal": {"default_http": 5}}`), true, true, ":1", ":2", 0, false))
		require.Error(t, patchLalmaxConfig(write(t, `{"lal": {"httpts": 5}}`), true, true, ":1", ":2", 0, false))
		require.Error(t, patchLalmaxConfig(write(t, `{"lalmax": 5}`), true, true, ":1", ":2", 0, false))
		require.Error(t, patchLalmaxConfig(write(t, `{"lalmax": {"http_config": 5}}`), true, true, ":1", ":2", 0, false))
		require.Error(t, patchLalmaxConfig(write(t, `{"lalmax": {"srt_config": 5}}`), true, true, ":1", ":2", 0, false))
	})
}

func TestM2NewEmbeddedLalmaxErrorBranches(t *testing.T) {
	_, err := NewEmbeddedLalmax(EmbeddedLalmaxConfig{HTTPAddr: "%zz"})
	require.Error(t, err)

	bad := filepath.Join(t.TempDir(), "bad.json")
	require.NoError(t, os.WriteFile(bad, []byte("{"), 0o644))
	_, err = NewEmbeddedLalmax(EmbeddedLalmaxConfig{ConfigPath: bad})
	require.Error(t, err)

	// Force NewLalMaxServer to fail: its RTC mux port is already bound.
	holder, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer holder.Close()
	port := holder.LocalAddr().(*net.UDPAddr).Port
	path := filepath.Join(t.TempDir(), "rtc.json")
	raw, err := embeddedConfigJSON(EmbeddedLalmaxConfig{})
	require.NoError(t, err)
	var conf map[string]any
	require.NoError(t, json.Unmarshal(raw, &conf))
	conf["lalmax"].(map[string]any)["rtc_config"] = map[string]any{
		"enable": true, "ice_udp_mux_port": port, "ice_tcp_mux_port": 0,
	}
	conf["lalmax"].(map[string]any)["jt1078_config"] = map[string]any{"enable": false}
	conf["lalmax"].(map[string]any)["fmp4_config"] = map[string]any{}
	lal := conf["lal"].(map[string]any)
	lal["rtsp"] = map[string]any{"enable": false}
	lal["httpflv"] = map[string]any{"enable": false}
	lal["httpts"] = map[string]any{"enable": false}
	raw, err = json.Marshal(conf)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, raw, 0o644))
	_, err = NewEmbeddedLalmax(EmbeddedLalmaxConfig{ConfigPath: path})
	require.Error(t, err)
}
