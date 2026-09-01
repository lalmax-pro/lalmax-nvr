package media

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGroupToStreamInfoRuntimeStats(t *testing.T) {
	t.Parallel()

	info := groupToStreamInfo(groupPayload{
		StreamName: "cam1",
		AppName:    "live",
		Pub:        sessionPayload{SessionID: "pub-1", Protocol: "RTMP", BitrateKbits: 3200, ReadBitrateKbits: 3180, WriteBitrateKbits: 12},
		Subs: []sessionPayload{
			{SessionID: "sub-1", Protocol: "FLV", Remote: "127.0.0.1:10001", BitrateKbits: 3100, WriteBitrateKbits: 3090},
			{SessionID: "sub-2", Protocol: "HLS", Remote: "127.0.0.1:10002", BitrateKbits: 2800, WriteBitrateKbits: 2790},
		},
		FPS: []struct {
			UnixSec int64   `json:"unix_sec"`
			V       float64 `json:"v"`
			Value   float64 `json:"value"`
			Num     float64 `json:"num"`
			FPS     float64 `json:"fps"`
		}{
			{UnixSec: 1000, V: 30},
			{UnixSec: 1001, V: 29},
			{UnixSec: 1002, V: 31},
		},
	})

	require.True(t, info.Active)
	require.NotNil(t, info.Publisher)
	require.Equal(t, 3200, info.Publisher.BitrateKbits)
	require.Equal(t, 3180, info.Publisher.ReadBitrateKbits)
	require.Len(t, info.Subscribers, 2)
	require.Equal(t, 3100, info.Subscribers[0].BitrateKbits)
	require.Equal(t, 3090, info.Subscribers[0].WriteBitrateKbits)
	require.InDelta(t, 30.0, info.InFPS, 0.01)
	require.Equal(t, time.Unix(1002, 0), info.LastFrameTime)
}

func TestGroupToStreamInfo_PullSessionActive(t *testing.T) {
	t.Parallel()

	// Simulate a relay pull stream where pub is empty but pull is active
	info := groupToStreamInfo(groupPayload{
		StreamName: "cam-onvif",
		AppName:    "live",
		Pub:        sessionPayload{}, // Empty pub for pull streams
		Pull:       sessionPayload{SessionID: "RTSPPULL1", Protocol: "RTSP", Remote: "192.168.1.100:554", BitrateKbits: 690, ReadBitrateKbits: 690},
		FPS: []struct {
			UnixSec int64   `json:"unix_sec"`
			V       float64 `json:"v"`
			Value   float64 `json:"value"`
			Num     float64 `json:"num"`
			FPS     float64 `json:"fps"`
		}{
			{UnixSec: 1000, V: 15},
		},
	})

	require.True(t, info.Active, "stream with active pull session should be Active=true")
	require.NotNil(t, info.Publisher)
	require.Equal(t, "RTSPPULL1", info.Publisher.SessionID)
	require.Equal(t, "RTSP", info.Publisher.Protocol)
	require.Equal(t, 690, info.Publisher.BitrateKbits)
	require.InDelta(t, 15.0, info.InFPS, 0.01)
}

func TestGroupToStreamInfo_InactiveStream(t *testing.T) {
	t.Parallel()

	// Simulate an inactive stream where both pub and pull are empty
	info := groupToStreamInfo(groupPayload{
		StreamName: "cam-idle",
		AppName:    "live",
		Pub:        sessionPayload{},
		Pull:       sessionPayload{},
	})

	require.False(t, info.Active, "stream with no pub or pull session should be Active=false")
	require.Nil(t, info.Publisher)
}

func TestLalmaxHTTPBuildPlayURL_WSFLV(t *testing.T) {
	t.Parallel()

	engine, err := NewLalmaxHTTP(LalmaxHTTPConfig{
		BaseURL:   "http://127.0.0.1:8080",
		PublicURL: "https://stream.example.com",
		HTTPPort:  8080,
	})
	require.NoError(t, err)

	playURL, err := engine.BuildPlayURL(context.Background(), PlayURLRequest{
		StreamID: "cam1",
		AppName:  "live",
		Protocol: "ws-flv",
	})
	require.NoError(t, err)
	require.NotNil(t, playURL)
	require.Equal(t, "ws-flv", playURL.Protocol)
	require.Equal(t, "wss://stream.example.com:8080/live/cam1.flv", playURL.URL)
}

func TestLalmaxHTTPBuildPlayURL_HLS(t *testing.T) {
	t.Parallel()

	engine, err := NewLalmaxHTTP(LalmaxHTTPConfig{
		BaseURL:   "http://127.0.0.1:1290",
		PublicURL: "http://127.0.0.1:1290",
		HTTPPort:  8080,
	})
	require.NoError(t, err)

	playURL, err := engine.BuildPlayURL(context.Background(), PlayURLRequest{
		StreamID: "cam1",
		AppName:  "live",
		Protocol: "hls",
	})
	require.NoError(t, err)
	require.NotNil(t, playURL)
	require.Equal(t, "hls", playURL.Protocol)
	require.Equal(t, "http://127.0.0.1:8080/hls/cam1.m3u8", playURL.URL)
}

func TestLalmaxHTTPBuildPlayURL_LLHLS(t *testing.T) {
	t.Parallel()

	engine, err := NewLalmaxHTTP(LalmaxHTTPConfig{
		BaseURL:   "http://127.0.0.1:1290",
		PublicURL: "http://127.0.0.1:1290",
	})
	require.NoError(t, err)

	playURL, err := engine.BuildPlayURL(context.Background(), PlayURLRequest{
		StreamID: "cam1",
		AppName:  "live",
		Protocol: "ll-hls",
	})
	require.NoError(t, err)
	require.NotNil(t, playURL)
	require.Equal(t, "ll-hls", playURL.Protocol)
	require.Equal(t, "http://127.0.0.1:1290/live/hls/cam1/index.m3u8?app_name=live&ll-hls=1", playURL.URL)
}

func TestLalmaxHTTPBuildPlayURL_RTMP(t *testing.T) {
	t.Parallel()

	engine, err := NewLalmaxHTTP(LalmaxHTTPConfig{
		BaseURL:   "http://127.0.0.1:1290",
		PublicURL: "http://127.0.0.1:1290",
		RTMPPort:  1935,
	})
	require.NoError(t, err)

	playURL, err := engine.BuildPlayURL(context.Background(), PlayURLRequest{
		StreamID: "cam1",
		AppName:  "live",
		Protocol: "rtmp",
	})
	require.NoError(t, err)
	require.NotNil(t, playURL)
	require.Equal(t, "rtmp", playURL.Protocol)
	require.Equal(t, "rtmp://127.0.0.1:1935/live/cam1", playURL.URL)
}

func TestLalmaxHTTPBuildPlayURL_RTSP(t *testing.T) {
	t.Parallel()

	engine, err := NewLalmaxHTTP(LalmaxHTTPConfig{
		BaseURL:   "http://127.0.0.1:1290",
		PublicURL: "http://127.0.0.1:1290",
		RTSPPort:  5544,
	})
	require.NoError(t, err)

	playURL, err := engine.BuildPlayURL(context.Background(), PlayURLRequest{
		StreamID: "cam1",
		AppName:  "live",
		Protocol: "rtsp",
	})
	require.NoError(t, err)
	require.NotNil(t, playURL)
	require.Equal(t, "rtsp", playURL.Protocol)
	require.Equal(t, "rtsp://127.0.0.1:5544/live/cam1", playURL.URL)
}

func TestLalmaxHTTPBuildPlayURL_RTSPDefaultPort(t *testing.T) {
	t.Parallel()

	engine, err := NewLalmaxHTTP(LalmaxHTTPConfig{
		BaseURL:   "http://127.0.0.1:12090",
		PublicURL: "http://nvr.example.com:12090",
	})
	require.NoError(t, err)

	playURL, err := engine.BuildPlayURL(context.Background(), PlayURLRequest{
		StreamID: "cam1",
		AppName:  "live",
		Protocol: "rtsp",
	})
	require.NoError(t, err)
	require.NotNil(t, playURL)
	require.Equal(t, "rtsp://nvr.example.com:15544/live/cam1", playURL.URL)
}

func TestLalmaxHTTPBuildPlayURL_WHIP(t *testing.T) {
	t.Parallel()

	engine, err := NewLalmaxHTTP(LalmaxHTTPConfig{
		BaseURL:   "http://127.0.0.1:12090",
		PublicURL: "http://nvr.example.com:12090",
	})
	require.NoError(t, err)

	playURL, err := engine.BuildPlayURL(context.Background(), PlayURLRequest{
		StreamID: "front-door",
		Protocol: "whip",
	})
	require.NoError(t, err)
	require.NotNil(t, playURL)
	require.Equal(t, "whip", playURL.Protocol)
	require.Equal(t, "http://nvr.example.com:12090/webrtc/whip?streamid=front-door", playURL.URL)
}

func TestLalmaxHTTPBuildPlayURL_FLV(t *testing.T) {
	t.Parallel()

	engine, err := NewLalmaxHTTP(LalmaxHTTPConfig{
		BaseURL:   "http://127.0.0.1:1290",
		PublicURL: "http://127.0.0.1:1290",
		HTTPPort:  8080,
	})
	require.NoError(t, err)

	playURL, err := engine.BuildPlayURL(context.Background(), PlayURLRequest{
		StreamID: "cam1",
		AppName:  "live",
		Protocol: "flv",
	})
	require.NoError(t, err)
	require.NotNil(t, playURL)
	require.Equal(t, "flv", playURL.Protocol)
	require.Equal(t, "http://127.0.0.1:8080/live/cam1.flv", playURL.URL)
}

func TestLalmaxHTTPBuildPlayURL_WebRTC(t *testing.T) {
	t.Parallel()

	engine, err := NewLalmaxHTTP(LalmaxHTTPConfig{
		BaseURL:   "http://127.0.0.1:1290",
		PublicURL: "http://127.0.0.1:1290",
	})
	require.NoError(t, err)

	playURL, err := engine.BuildPlayURL(context.Background(), PlayURLRequest{
		StreamID: "cam1",
		AppName:  "live",
		Protocol: "webrtc",
	})
	require.NoError(t, err)
	require.NotNil(t, playURL)
	require.Equal(t, "whep", playURL.Protocol)
	require.Equal(t, "http://127.0.0.1:1290/webrtc/whep?app_name=live&streamid=cam1", playURL.URL)
}

func TestLalmaxHTTP_GetStream_GroupNotFound(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/stat/group", r.URL.Path)
		require.Equal(t, "34020000001320000001:34020000001320000001", r.URL.Query().Get("stream_name"))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error_code": 1001,
			"desp":       "group not found",
			"data":       nil,
		})
	}))
	t.Cleanup(srv.Close)

	engine, err := NewLalmaxHTTP(LalmaxHTTPConfig{BaseURL: srv.URL})
	require.NoError(t, err)

	info, err := engine.GetStream(context.Background(), "34020000001320000001:34020000001320000001")
	require.NoError(t, err)
	require.Nil(t, info)
}

func TestIsLalGroupNotFound(t *testing.T) {
	t.Parallel()
	require.True(t, isLalGroupNotFound(fmt.Errorf("lalmax error 1001: group not found")))
	require.False(t, isLalGroupNotFound(fmt.Errorf("lalmax error 2002: listen fail")))
	require.False(t, isLalGroupNotFound(nil))
}
