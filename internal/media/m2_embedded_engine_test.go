package media

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/q191201771/lal/pkg/base"
	"github.com/stretchr/testify/require"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
)

// m2EmbeddedConfigPath writes a minimal embedded lalmax config that binds only
// loopback ephemeral ports and disables side services (RTC, JT1078).
func m2EmbeddedConfigPath(t *testing.T) string {
	t.Helper()
	raw, err := embeddedConfigJSON(EmbeddedLalmaxConfig{})
	require.NoError(t, err)
	var conf map[string]any
	require.NoError(t, json.Unmarshal(raw, &conf))
	max := conf["lalmax"].(map[string]any)
	max["rtc_config"] = map[string]any{"enable": false}
	max["jt1078_config"] = map[string]any{"enable": false}
	max["http_config"] = map[string]any{"http_listen_addr": "127.0.0.1:0"}
	max["fmp4_config"] = map[string]any{}
	lal := conf["lal"].(map[string]any)
	lal["rtsp"] = map[string]any{"enable": false}
	lal["httpflv"] = map[string]any{"enable": false}
	lal["httpts"] = map[string]any{"enable": true, "http_listen_addr": "127.0.0.1:0", "url_pattern": "/live/"}
	lal["log"] = map[string]any{"filename": "", "is_to_file": false, "is_to_stdout": false, "level": 5}
	raw, err = json.Marshal(conf)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "lalmax.json")
	require.NoError(t, os.WriteFile(path, raw, 0o600))
	return path
}

func m2StartAPI(t *testing.T, engine *EmbeddedLalmax) {
	t.Helper()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		engine.Server().HTTPHandler().ServeHTTP(w, r)
	}))
	t.Cleanup(api.Close)
	httpEngine, err := NewLalmaxHTTP(LalmaxHTTPConfig{BaseURL: api.URL})
	require.NoError(t, err)
	engine.httpEngine = httpEngine
	engine.LalmaxHTTP = httpEngine
}

func TestM2EmbeddedEngineLifecycle(t *testing.T) {
	t.Chdir(t.TempDir())
	path := m2EmbeddedConfigPath(t)
	engine, err := NewEmbeddedLalmax(EmbeddedLalmaxConfig{ConfigPath: path})
	require.NoError(t, err)
	require.Equal(t, path, engine.ConfigPath())
	require.NotNil(t, engine.Server())
	m2StartAPI(t, engine)

	// Runtime HLS settings are applied to the live server on both paths.
	engine.applyRuntimeHLSSettings(engine.Server(), EmbeddedLalmaxConfig{HLSEnabled: true, HLSOnDemand: true, HLSIdleTimeoutMs: 10})
	engine.applyRuntimeHLSSettings(engine.Server(), EmbeddedLalmaxConfig{HLSEnabled: false})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, engine.Start(ctx))
	t.Cleanup(func() { _ = engine.Shutdown(context.Background()) })
	require.NoError(t, engine.Ready(ctx))
	require.NotNil(t, engine.PlayHTTPHandler())

	// In-process publish session accepts frames and audio config.
	session, err := engine.AddCustomizePubSession(ctx, "m2-pub")
	require.NoError(t, err)
	require.NotNil(t, session)
	require.NoError(t, session.FeedAudioSpecificConfig([]byte{0x12, 0x10}))
	require.NoError(t, session.FeedAvPacket(base.AvPacket{
		PayloadType: base.AvPacketPtG711A,
		Timestamp:   1000,
		Payload:     []byte{0x01, 0x02, 0x03},
	}))
	require.NoError(t, session.FeedRtmpMsg(base.RtmpMsg{
		Header:  base.RtmpHeader{MsgTypeId: base.RtmpTypeIdAudio, TimestampAbs: 1000},
		Payload: []byte{0xaf, 0x01, 0x02},
	}))
	session.WithOption(func(opt *base.AvPacketStreamOption) {
		opt.VideoFormat = base.AvPacketStreamVideoFormatAvcc
	})
	resetter, ok := session.(interface{ ResetMedia() error })
	require.True(t, ok)
	require.NoError(t, resetter.ResetMedia())
	require.NoError(t, engine.DelCustomizePubSession(ctx, session))

	// JT1078 audio for an unknown terminal is rejected by the media server.
	require.Error(t, engine.PushJT1078Audio("1003", 1, 7, []byte{0x01, 0x02}))

	// HLS config changes patch the on-disk file while the server runs.
	require.NoError(t, engine.ApplyHLSConfig(config.HLSConfig{
		Enabled:               m2Enabled(true),
		LalFragmentDurationMs: 4000,
		LalFragmentNum:        8,
		SegmentCount:          8,
	}))

	require.NoError(t, engine.Shutdown(context.Background()))
	require.Error(t, engine.Ready(ctx))
	require.Nil(t, engine.PlayHTTPHandler())
	_, err = engine.AddCustomizePubSession(ctx, "m2-after-shutdown")
	require.Error(t, err)
}

func TestM2EmbeddedStartCanceledContext(t *testing.T) {
	t.Chdir(t.TempDir())
	engine, err := NewEmbeddedLalmax(EmbeddedLalmaxConfig{ConfigPath: m2EmbeddedConfigPath(t)})
	require.NoError(t, err)
	m2StartAPI(t, engine)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	startErr := engine.Start(ctx)
	require.Error(t, startErr)
	require.NoError(t, engine.Shutdown(context.Background()))
}

func TestM2EmbeddedRestartBranches(t *testing.T) {
	t.Run("patch error", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "bad.json")
		require.NoError(t, os.WriteFile(path, []byte("{"), 0o644))
		engine := &EmbeddedLalmax{cfg: EmbeddedLalmaxConfig{ConfigPath: path}}
		require.Error(t, engine.Restart(context.Background(), 1935, 9000, false, false))
	})

	t.Run("generate error", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "regular")
		require.NoError(t, os.WriteFile(file, []byte("x"), 0o644))
		engine := &EmbeddedLalmax{cfg: EmbeddedLalmaxConfig{ConfigPath: filepath.Join(file, "lalmax.json")}}
		require.Error(t, engine.Restart(context.Background(), 1935, 9000, false, false))
	})

	t.Run("success with no previous server", func(t *testing.T) {
		t.Chdir(t.TempDir())
		engine, err := NewEmbeddedLalmax(EmbeddedLalmaxConfig{ConfigPath: m2EmbeddedConfigPath(t)})
		require.NoError(t, err)
		m2StartAPI(t, engine)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		require.NoError(t, engine.Restart(ctx, 1935, 9000, false, false))
		require.NotNil(t, engine.Server())
		require.NoError(t, engine.Shutdown(context.Background()))
		require.Nil(t, engine.Server())
	})
}

func TestM2EmbeddedRestartCanceledContext(t *testing.T) {
	t.Chdir(t.TempDir())
	engine, err := NewEmbeddedLalmax(EmbeddedLalmaxConfig{ConfigPath: m2EmbeddedConfigPath(t)})
	require.NoError(t, err)
	m2StartAPI(t, engine)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Error(t, engine.Restart(ctx, 1935, 9000, false, false))
	require.Nil(t, engine.Server())
}
