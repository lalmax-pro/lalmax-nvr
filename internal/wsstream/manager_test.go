package wsstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/stretchr/testify/require"
)

func TestManagerOptionsAndLifecycle(t *testing.T) {
	defaults := NewManager(WithMaxViewers(0), WithWriteBufSize(-1), WithIdleTimeout(0))
	require.Equal(t, defaultMaxViewers, defaults.maxViewers)
	require.Equal(t, defaultWriteBufSize, defaults.writeBufSize)
	require.Equal(t, defaultIdleTimeout, defaults.idleTimeout)
	m := NewManager(WithMaxViewers(2), WithWriteBufSize(4), WithIdleTimeout(time.Minute))
	require.Equal(t, 2, m.maxViewers)
	require.Equal(t, 4, m.writeBufSize)
	require.NoError(t, m.SetAudioConfig("missing", nil))
	require.ErrorIs(t, m.SetAudioConfig("missing", &AudioCodecInfo{}), ErrStreamNotActive)
	require.False(t, m.IsActive("cam"))
	require.Zero(t, m.ViewerCount("cam"))
	hub := model.NewStreamHub()
	require.NoError(t, m.RegisterStream("cam", model.FormatH264, nil, nil, nil, hub))
	t.Cleanup(m.StopAll)
	require.ErrorIs(t, m.RegisterStream("cam", model.FormatH265, nil, nil, nil, nil), ErrStreamExists)
	require.True(t, m.IsActive("cam"))
	require.Zero(t, m.ViewerCount("cam"))
	audio := &AudioCodecInfo{Codec: AudioCodecG711A, SampleRate: 8000, Channels: 1}
	require.NoError(t, m.SetAudioConfig("cam", audio))
	require.NoError(t, m.SetAudioConfig("cam", audio))
	require.Equal(t, "ws-audio-cam", m.streams["cam"].hubAudioSubID)
	require.Equal(t, audio, m.streams["cam"].audioInfo.Load())
	m.streams["cam"].dropCount.Store(1)
	m.UnregisterStream("cam")
	m.UnregisterStream("missing")
	require.False(t, m.IsActive("cam"))
	// Both hub subscriptions must be released when a stream is removed.
	require.NoError(t, hub.Subscribe("ws-cam", func(int64, [][]byte) {}))
	hub.Unsubscribe("ws-cam")
	require.NoError(t, hub.SubscribeAudio("ws-audio-cam", func(int64, model.AudioCodec, []byte) {}))
	hub.UnsubscribeAudio("ws-audio-cam")
	require.NoError(t, m.RegisterStream("other", model.FormatH265, nil, nil, nil, nil))
	m.StopAll()
	require.False(t, m.IsActive("other"))
}

func TestManagerQueuesAndKeyframes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		codec model.Format
		au    [][]byte
		key   bool
	}{
		{"h264 IDR after parameters", model.FormatH264, [][]byte{nil, {0x67}, {0x68}, {0x65, 1}}, true},
		{"h264 delta", model.FormatH264, [][]byte{{0x41}}, false},
		{"h265 CRA after parameters", model.FormatH265, [][]byte{nil, {0x40}, {21 << 1, 1}}, true},
		{"h265 delta", model.FormatH265, [][]byte{{1 << 1, 1}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager()
			entry := &streamEntry{codec: tc.codec, frameCh: make(chan frameMsg, 1)}
			m.streams["cam"] = entry // No drain goroutine: queue overflow is deterministic.
			m.WriteH264("missing", 1, tc.au)
			m.WriteH264("cam", 1, nil)
			require.Empty(t, entry.frameCh)
			if tc.codec == model.FormatH265 {
				m.WriteH265("cam", 42, tc.au)
			} else {
				m.WriteH264("cam", 42, tc.au)
			}
			msg := <-entry.frameCh
			require.Equal(t, int64(42), msg.pts)
			require.Equal(t, tc.au, msg.au)
			require.Equal(t, tc.key, msg.isKeyframe)
			m.writeFrame("cam", 43, tc.au)
			for i := 0; i < 100; i++ {
				m.writeFrame("cam", 44, tc.au)
			}
			require.Equal(t, int64(100), entry.dropCount.Load())
		})
	}
}

func TestManagerFanoutAndSlowViewers(t *testing.T) {
	m := NewManager()
	fast := &viewerConn{ch: make(chan []byte, 2)}
	slow := &viewerConn{ch: make(chan []byte)}
	entry := &streamEntry{viewers: map[int64]*viewerConn{1: fast, 2: slow}, frameCh: make(chan frameMsg, 2)}
	m.writeAudio(entry, 1, nil)
	require.Zero(t, entry.dropCount.Load())
	m.writeAudio(entry, 90, []byte{1, 2})
	audio, err := DecodeAudioFrame(<-fast.ch)
	require.NoError(t, err)
	require.Equal(t, int64(90), audio.PTS)
	require.Equal(t, []byte{1, 2}, audio.Data)
	require.Equal(t, int64(1), entry.dropCount.Load())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.writeLoop(ctx, "cam", entry) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("writeLoop did not stop")
		}
	})
	entry.frameCh <- frameMsg{pts: 180, au: [][]byte{{0x65, 1}}, isKeyframe: true}
	select {
	case data := <-fast.ch:
		video, err := DecodeVideoFrame(data)
		require.NoError(t, err)
		require.Equal(t, int64(180), video.PTS)
		require.True(t, video.IsKeyframe)
	case <-time.After(time.Second):
		t.Fatal("video was not forwarded")
	}
	require.Eventually(t, func() bool { return entry.dropCount.Load() == 2 }, time.Second, time.Millisecond)
}

func TestManagerServeWSValidation(t *testing.T) {
	m := NewManager(WithMaxViewers(1))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	require.ErrorIs(t, m.ServeWS("missing", httptest.NewRecorder(), req), ErrStreamNotActive)
	require.NoError(t, m.RegisterStream("cam", model.FormatH264, nil, nil, nil, nil))
	defer m.StopAll()
	require.Error(t, m.ServeWS("cam", httptest.NewRecorder(), req))
	entry := m.streams["cam"]
	entry.viewerMu.Lock()
	entry.viewers[1] = &viewerConn{}
	entry.viewerMu.Unlock()
	require.ErrorIs(t, m.ServeWS("cam", httptest.NewRecorder(), req), ErrMaxViewers)
	entry.viewerMu.Lock()
	delete(entry.viewers, 1)
	entry.viewerMu.Unlock()
}

func TestManagerWebSocketVideoAndAudio(t *testing.T) {
	for _, codec := range []model.Format{model.FormatH264, model.FormatH265} {
		t.Run(string(codec), func(t *testing.T) {
			// Keep the idle watchdog out of this short transport test.
			m := NewManager(WithIdleTimeout(time.Hour))
			hub := model.NewStreamHub()
			sps, pps, vps := []byte{0x67, 100, 0, 31}, []byte{0x68}, []byte{0x40}
			require.NoError(t, m.RegisterStream("cam", codec, sps, pps, vps, hub))
			defer m.StopAll()
			aci := &AudioCodecInfo{Codec: AudioCodecAAC, SampleRate: 44100, Channels: 2, Config: []byte{0x12, 0x10}}
			require.NoError(t, m.SetAudioConfig("cam", aci))
			exited := make(chan error, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { exited <- m.ServeWS("cam", w, r) }))
			defer server.Close()
			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
			require.NoError(t, err)
			defer conn.Close()
			require.NoError(t, conn.SetReadDeadline(time.Now().Add(3*time.Second)))
			read := func() []byte {
				t.Helper()
				typ, data, err := conn.ReadMessage()
				require.NoError(t, err)
				require.Equal(t, websocket.BinaryMessage, typ)
				return data
			}
			ci, err := DecodeCodecInfo(read())
			require.NoError(t, err)
			require.Equal(t, string(codec), ci.Codec)
			require.Equal(t, byte(100), ci.Profile)
			require.Equal(t, byte(31), ci.Level)
			require.Equal(t, sps, ci.SPS)
			if codec == model.FormatH265 {
				require.Equal(t, vps, ci.VPS)
			}
			gotAudio, err := DecodeAudioCodecInfo(read())
			require.NoError(t, err)
			require.Equal(t, aci, gotAudio)
			require.Eventually(t, func() bool { return m.ViewerCount("cam") == 1 }, time.Second, time.Millisecond)
			au := [][]byte{{0x65, 1}}
			if codec == model.FormatH265 {
				au = [][]byte{{19 << 1, 1}}
			}
			hub.Broadcast(9000, au, true)
			video, err := DecodeVideoFrame(read())
			require.NoError(t, err)
			require.True(t, video.IsKeyframe)
			require.Equal(t, au, video.NALUs)
			require.Equal(t, int64(9000), video.PTS)
			hub.BroadcastAudio(18000, model.AudioAAC, []byte{1, 2, 3})
			audio, err := DecodeAudioFrame(read())
			require.NoError(t, err)
			require.Equal(t, int64(18000), audio.PTS)
			require.Equal(t, []byte{1, 2, 3}, audio.Data)
			require.NoError(t, conn.Close())
			select {
			case err := <-exited:
				require.NoError(t, err)
			case <-time.After(3 * time.Second):
				t.Fatal("ServeWS did not exit after disconnect")
			}
			require.Zero(t, m.ViewerCount("cam"))
		})
	}
}
