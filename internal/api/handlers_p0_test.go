package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/stretchr/testify/require"
)

func TestCameraProtocols_SubFallsBackToMain(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	seedCameraWithEncoding(t, db, "cam1", "h264")

	reg := NewStreamRegistry()
	reg.Register(&HLSStreamHandler{})
	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetStreamRegistry(reg)

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam1/protocols?quality=sub", nil, "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "main", rr.Header().Get("X-Stream-Quality"))
}

func TestAutoDiscoverSettings_Get(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{}
	cfg.AutoDiscover.ScanInterval = "60s"
	h := NewHandler(db, store, noopAuthMW(), cfg, nil, "", nil, nil)
	rr := doRequest(t, h.Routes(), "GET", "/api/settings/auto-discover", nil, "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code)
}

func TestIsValidRecordingModeEventAdaptive(t *testing.T) {
	require.True(t, isValidRecordingMode("event"))
	require.True(t, isValidRecordingMode("adaptive"))
	require.False(t, isValidRecordingMode("nope"))
}

func TestCameraProtocols_IncludesRTSP(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	seedCameraWithEncoding(t, db, "cam1", "h264")

	reg := NewStreamRegistry()
	reg.Register(&HLSStreamHandler{})
	reg.Register(&StaticStreamHandler{Protocol: "rtsp", Codecs: []model.Format{model.FormatH264, model.FormatH265}})
	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetStreamRegistry(reg)
	h.SetMediaEngine(&stubMediaEngine{
		playURLs: map[string]string{"rtsp": "rtsp://nvr.example:15544/live/cam1"},
	})

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam1/protocols", nil, "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code)

	var body struct {
		Protocols []ProtocolDetail `json:"protocols"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	var rtsp *ProtocolDetail
	for i := range body.Protocols {
		if body.Protocols[i].Protocol == "rtsp" {
			rtsp = &body.Protocols[i]
			break
		}
	}
	require.NotNil(t, rtsp)
	require.True(t, rtsp.Available)
	require.Equal(t, "rtsp://nvr.example:15544/live/cam1", rtsp.PlayURL)
}

func TestCameraFlow_NoEngineDegrades(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	seedCameraWithEncoding(t, db, "cam1", "h264")

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam1/flow", nil, "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code)

	var body flowCameraResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	require.Equal(t, "cam1", body.CameraID)
	require.False(t, body.Source.Active)
	require.Nil(t, body.Engine)
	require.Empty(t, body.Viewers)
}

func TestCameraFlow_CountsSubscribersByProtocol(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	seedCameraWithEncoding(t, db, "cam1", "h264")

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(&stubMediaEngine{
		streamsByID: map[string]*media.StreamInfo{
			"cam1": {
				StreamID:   "cam1",
				Active:     true,
				VideoCodec: "H264",
				InFPS:      25,
				Publisher:  &media.SessionInfo{Protocol: "rtsp"},
				Subscribers: []media.SessionInfo{
					{Protocol: "rtsp"},
					{Protocol: "RTSP"},
					{Protocol: "hls"},
				},
			},
			"cam1_sub": {StreamID: "cam1_sub", Active: true},
		},
	})

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam1/flow", nil, "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code)

	var body flowCameraResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	require.True(t, body.Source.Active)
	require.NotNil(t, body.Engine)
	require.Equal(t, "H264", body.Engine.VideoCodec)
	require.Equal(t, 2, body.Viewers["rtsp"])
	require.Equal(t, 1, body.Viewers["hls"])
	require.True(t, body.Substream.Active)
}

func TestRecordingsTimeline_RequiresCamera(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/timeline", nil, "admin", "pass")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}
