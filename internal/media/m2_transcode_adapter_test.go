package media

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lalmax-pro/lalmax-nvr/internal/auth"
	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/transcode"
)

type m2TranscodeEngine struct {
	Engine
	info        *StreamInfo
	getErr      error
	streams     []StreamInfo
	listErr     error
	play        *PlayURL
	playErr     error
	kicked      []string
	kickErr     error
	lastRequest PlayURLRequest
}

func (e *m2TranscodeEngine) GetStream(context.Context, string) (*StreamInfo, error) {
	return e.info, e.getErr
}

func (e *m2TranscodeEngine) ListStreams(context.Context) ([]StreamInfo, error) {
	return e.streams, e.listErr
}

func (e *m2TranscodeEngine) BuildPlayURL(_ context.Context, req PlayURLRequest) (*PlayURL, error) {
	e.lastRequest = req
	return e.play, e.playErr
}

func (e *m2TranscodeEngine) KickSession(_ context.Context, sessionID string) error {
	e.kicked = append(e.kicked, sessionID)
	return e.kickErr
}

func m2EmbeddedConfig() *config.Config {
	enabled := true
	return &config.Config{
		Media: config.MediaConfig{Mode: "embedded"},
		RTMP:  config.RTMPConfig{Enabled: &enabled},
	}
}

func m2Source() transcode.SourceRef {
	return transcode.SourceRef{EngineID: "embedded", StreamID: "camera", AppName: "live"}
}

func TestM2TranscodeResolveErrorBranches(t *testing.T) {
	base := m2EmbeddedConfig()
	engine := &m2TranscodeEngine{info: &StreamInfo{StreamID: "camera", AppName: "live", Active: true, VideoCodec: "H264", AudioCodec: "AAC"}}
	adapter := &TranscodeAdapter{Engine: engine, Config: base}

	_, err := adapter.Resolve(context.Background(), transcode.SourceRef{EngineID: "embedded", StreamID: "a/b", AppName: "live"})
	require.Error(t, err)

	for _, cfg := range []*config.Config{nil, func() *config.Config {
		c := m2EmbeddedConfig()
		c.Media.Mode = "http"
		return c
	}()} {
		_, err = (&TranscodeAdapter{Engine: engine, Config: cfg}).Resolve(context.Background(), m2Source())
		var domain *transcode.Error
		require.True(t, errors.As(err, &domain))
		require.Equal(t, "engine_unavailable", domain.Code)
	}

	authorizer := auth.NewMediaAuthorizer()
	require.NoError(t, authorizer.ReserveInternalStream("camera", "pub", "play"))
	_, err = (&TranscodeAdapter{Engine: engine, Config: base, Auth: authorizer}).Resolve(context.Background(), m2Source())
	require.Error(t, err)

	offline := &m2TranscodeEngine{}
	_, err = (&TranscodeAdapter{Engine: offline, Config: base}).Resolve(context.Background(), m2Source())
	var domain *transcode.Error
	require.True(t, errors.As(err, &domain))
	require.Equal(t, "source_offline", domain.Code)

	_, err = (&TranscodeAdapter{Engine: &m2TranscodeEngine{getErr: errors.New("boom")}, Config: base}).Resolve(context.Background(), m2Source())
	require.Error(t, err)

	// App name defaults to live when the engine omits it.
	defaultEngine := &m2TranscodeEngine{info: &StreamInfo{StreamID: "camera", Active: true, VideoCodec: "H264", AudioCodec: "AAC"}, play: &PlayURL{URL: "rtsp://127.0.0.1:15544/live/camera"}}
	_, err = (&TranscodeAdapter{Engine: defaultEngine, Config: base}).Resolve(context.Background(), m2Source())
	require.NoError(t, err)
	require.Equal(t, "live", defaultEngine.lastRequest.AppName)

	_, err = (&TranscodeAdapter{Engine: &m2TranscodeEngine{info: &StreamInfo{StreamID: "camera", AppName: "other", Active: true}}, Config: base}).Resolve(context.Background(), m2Source())
	require.Error(t, err)

	// Playback tokens configured but missing for this stream.
	tokenConfig := m2EmbeddedConfig()
	tokenConfig.Auth = config.AuthConfig{PlaybackTokens: map[string]string{"other": "secret"}}
	_, err = (&TranscodeAdapter{Engine: engine, Config: tokenConfig}).Resolve(context.Background(), m2Source())
	require.Error(t, err)

	// Play URL errors and wrong schemes.
	_, err = (&TranscodeAdapter{Engine: &m2TranscodeEngine{info: engine.info, playErr: errors.New("down")}, Config: base}).Resolve(context.Background(), m2Source())
	require.Error(t, err)
	_, err = (&TranscodeAdapter{Engine: &m2TranscodeEngine{info: engine.info, play: &PlayURL{URL: "http://127.0.0.1/live"}}, Config: base}).Resolve(context.Background(), m2Source())
	require.Error(t, err)
	_, err = (&TranscodeAdapter{Engine: &m2TranscodeEngine{info: engine.info, play: &PlayURL{URL: "rtsp://%zz"}}, Config: base}).Resolve(context.Background(), m2Source())
	require.Error(t, err)

	// RTSP auth is embedded in the resolved URL and codecs are normalized.
	rtspAuth := m2EmbeddedConfig()
	rtspAuth.Media.RTSPAuthEnable = true
	rtspAuth.Media.RTSPUsername = "user"
	rtspAuth.Media.RTSPPassword = "pass"
	rtspAuth.Auth = config.AuthConfig{PlaybackTokens: map[string]string{"camera": "tok"}}
	authEngine := &m2TranscodeEngine{info: &StreamInfo{StreamID: "camera", AppName: "live", Active: true, VideoCodec: "H264", AudioCodec: "AAC"}, play: &PlayURL{URL: "rtsp://127.0.0.1:15544/live/camera"}}
	resolved, err := (&TranscodeAdapter{Engine: authEngine, Config: rtspAuth}).Resolve(context.Background(), m2Source())
	require.NoError(t, err)
	require.Equal(t, "rtsp://user:pass@127.0.0.1:15544/live/camera", resolved.URL)
	require.Equal(t, "h264", resolved.VideoCodec)
	require.Equal(t, "aac", resolved.AudioCodec)
}

func TestM2TranscodeReserveBranches(t *testing.T) {
	ctx := context.Background()
	source := m2Source()
	_, err := (&TranscodeAdapter{}).Reserve(ctx, source, "out")
	require.Error(t, err)

	authorizer := auth.NewMediaAuthorizer()
	disabled := false
	disabledConfig := m2EmbeddedConfig()
	disabledConfig.RTMP.Enabled = &disabled
	_, err = (&TranscodeAdapter{Engine: &m2TranscodeEngine{}, Auth: authorizer, Config: disabledConfig}).Reserve(ctx, source, "out")
	var domain *transcode.Error
	require.True(t, errors.As(err, &domain))
	require.Equal(t, "rtmp_disabled", domain.Code)

	_, err = (&TranscodeAdapter{Engine: &m2TranscodeEngine{listErr: errors.New("down")}, Auth: authorizer, Config: m2EmbeddedConfig()}).Reserve(ctx, source, "out")
	require.Error(t, err)

	_, err = (&TranscodeAdapter{Engine: &m2TranscodeEngine{streams: []StreamInfo{{StreamID: "out"}}}, Auth: authorizer, Config: m2EmbeddedConfig()}).Reserve(ctx, source, "out")
	require.Error(t, err)

	// A successful reservation registers credentials and returns a publish URL.
	engine := &m2TranscodeEngine{}
	registered := 0
	adapter := &TranscodeAdapter{Engine: engine, Auth: authorizer, Config: m2EmbeddedConfig(),
		RegisterOutput: func(_ context.Context, id, sourceID string) error {
			registered++
			require.Equal(t, "out", id)
			require.Equal(t, "camera", sourceID)
			return nil
		}}
	publishURL, err := adapter.Reserve(ctx, source, "out")
	require.NoError(t, err)
	require.Equal(t, 1, registered)
	require.Contains(t, publishURL, "rtmp://127.0.0.1:11935/live/out?stream_key=")
	require.True(t, authorizer.InternalPlaybackRequired("out"))
	require.Contains(t, adapter.outputs, "out")

	_, err = adapter.Reserve(ctx, source, "out")
	require.Error(t, err)

	// A registry failure revokes the grant and does not record the output.
	second := &TranscodeAdapter{Engine: &m2TranscodeEngine{}, Auth: auth.NewMediaAuthorizer(), Config: m2EmbeddedConfig(),
		RegisterOutput: func(context.Context, string, string) error { return errors.New("registry down") }}
	_, err = second.Reserve(ctx, source, "out2")
	require.Error(t, err)
	require.NotContains(t, second.outputs, "out2")
	// Revocation leaves a deny-only tombstone so direct playback stays closed.
	require.True(t, second.Auth.InternalPlaybackRequired("out2"))
}

func TestM2TranscodeObserveAndRelease(t *testing.T) {
	ctx := context.Background()
	_, err := (&TranscodeAdapter{Engine: &m2TranscodeEngine{getErr: errors.New("down")}}).Observe(ctx, "out")
	require.Error(t, err)

	obs, err := (&TranscodeAdapter{Engine: &m2TranscodeEngine{}}).Observe(ctx, "out")
	require.NoError(t, err)
	require.False(t, obs.Active)

	frameTime := time.Now()
	obs, err = (&TranscodeAdapter{Engine: &m2TranscodeEngine{info: &StreamInfo{Active: true, VideoCodec: "H265", LastFrameTime: frameTime}}}).Observe(ctx, "out")
	require.NoError(t, err)
	require.True(t, obs.Active)
	require.Equal(t, "h265", obs.VideoCodec)
	require.Equal(t, frameTime, obs.LastFrame)

	authorizer := auth.NewMediaAuthorizer()
	require.NoError(t, (&TranscodeAdapter{Auth: authorizer, outputs: map[string]derivedOutput{}}).Release(ctx, "missing"))

	// Successful cleanup deletes the reservation after kicking subscribers.
	engine := &m2TranscodeEngine{info: &StreamInfo{Subscribers: []SessionInfo{{SessionID: "sub-1"}, {SessionID: "sub-2"}}}}
	adapter := &TranscodeAdapter{Engine: engine, Auth: authorizer, outputs: map[string]derivedOutput{"out": {}}}
	require.NoError(t, adapter.Release(ctx, "out"))
	require.Equal(t, []string{"sub-1", "sub-2"}, engine.kicked)
	require.NotContains(t, adapter.outputs, "out")

	// A kick failure keeps the reservation for a retry.
	kickFail := &m2TranscodeEngine{info: &StreamInfo{Subscribers: []SessionInfo{{SessionID: "sub-3"}}}, kickErr: errors.New("kick failed")}
	adapter = &TranscodeAdapter{Engine: kickFail, Auth: authorizer, outputs: map[string]derivedOutput{"out": {}}}
	require.Error(t, adapter.Release(ctx, "out"))
	require.Contains(t, adapter.outputs, "out")
}
