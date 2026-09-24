package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

func ingestTestConfig(rtmp, srt, whip bool) *config.Config {
	cfg := &config.Config{}
	cfg.RTMP.Enabled = &rtmp
	cfg.SRT.Enabled = &srt
	cfg.SRT.Port = 19000
	cfg.WHIP.Enabled = &whip
	return cfg
}

func newCreatedStreamHarness(t *testing.T, cfg *config.Config) (*Handler, *stubMediaEngine) {
	t.Helper()
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	engine := &stubMediaEngine{
		stream: nil,
		playURLs: map[string]string{
			"rtmp": "rtmp://192.168.1.8:1935/live/ignored",
			"whip": "http://192.168.1.8:12090/webrtc/whip?streamid=ignored",
		},
	}
	h := NewHandler(db, store, noopAuthMW(), cfg, nil, "", nil, nil)
	h.SetMediaEngine(engine)
	return h, engine
}

func TestCreateStream_ReturnsCopyableIngestURLs(t *testing.T) {
	h, _ := newCreatedStreamHarness(t, ingestTestConfig(true, true, true))
	seedBoundCamera(t, h.db, "cam-a", "Lobby", "rtsp", "h264")
	h.mediaEngine.(*stubMediaEngine).streams = []media.StreamInfo{{
		StreamID: "cam-a",
		AppName:  "live",
		Active:   true,
	}}

	rr := doRequest(t, h.Routes(), http.MethodPost, "/api/streams",
		strings.NewReader(`{"stream_id":"room-1","name":"会议室"}`), "admin", "pass")
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())

	var created streamSummary
	parseJSON(t, rr, &created)
	require.Equal(t, "room-1", created.StreamID)
	require.Equal(t, "会议室", created.Name)
	require.Equal(t, "push", created.SourceType)
	require.False(t, created.Managed)
	require.False(t, created.Active)
	require.Equal(t, []string{"rtmp", "srt", "whip"}, protocolsOf(created.IngestURLs))
	require.Equal(t, "srt://192.168.1.8:19000?streamid=#!::h=room-1,m=publish", urlFor(created.IngestURLs, "srt"))

	rr = doRequest(t, h.Routes(), http.MethodGet, "/api/streams?managed=false", nil, "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var external streamListResponse
	parseJSON(t, rr, &external)
	require.Equal(t, 1, external.Total)
	require.Equal(t, "room-1", external.Streams[0].StreamID)
	require.Equal(t, []string{"rtmp", "srt", "whip"}, protocolsOf(external.Streams[0].IngestURLs))

	rr = doRequest(t, h.Routes(), http.MethodGet, "/api/streams?managed=true", nil, "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var managed streamListResponse
	parseJSON(t, rr, &managed)
	require.GreaterOrEqual(t, managed.Total, 1)
	var camera *streamSummary
	for i := range managed.Streams {
		if managed.Streams[i].StreamID == "cam-a" {
			camera = &managed.Streams[i]
			break
		}
	}
	require.NotNil(t, camera)
	require.Equal(t, []string{"whip"}, protocolsOf(camera.IngestURLs))

	rr = doRequest(t, h.Routes(), http.MethodGet, "/api/streams/room-1", nil, "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var got streamSummary
	parseJSON(t, rr, &got)
	require.Equal(t, "会议室", got.Name)
	require.Equal(t, []string{"rtmp", "srt", "whip"}, protocolsOf(got.IngestURLs))
}

func TestCreateStream_RejectsDuplicatesInvalidIDsAndCameras(t *testing.T) {
	h, _ := newCreatedStreamHarness(t, ingestTestConfig(true, true, true))
	seedBoundCamera(t, h.db, "cam-x", "Door", "rtsp", "h264")

	rr := doRequest(t, h.Routes(), http.MethodPost, "/api/streams",
		strings.NewReader(`{"stream_id":"room-1"}`), "admin", "pass")
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())

	rr = doRequest(t, h.Routes(), http.MethodPost, "/api/streams",
		strings.NewReader(`{"stream_id":"room-1"}`), "admin", "pass")
	require.Equal(t, http.StatusConflict, rr.Code, rr.Body.String())

	rr = doRequest(t, h.Routes(), http.MethodPost, "/api/streams",
		strings.NewReader(`{"stream_id":"bad id"}`), "admin", "pass")
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())

	rr = doRequest(t, h.Routes(), http.MethodPost, "/api/streams",
		strings.NewReader(`{"stream_id":"cam-x"}`), "admin", "pass")
	require.Equal(t, http.StatusConflict, rr.Code, rr.Body.String())
}

func TestCreateStream_RequiresAnEnabledIngest(t *testing.T) {
	h, _ := newCreatedStreamHarness(t, ingestTestConfig(false, false, false))
	rr := doRequest(t, h.Routes(), http.MethodPost, "/api/streams",
		strings.NewReader(`{"stream_id":"room-1"}`), "admin", "pass")
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
}

func TestDeleteStream_RemovesIdleCreatedSlot(t *testing.T) {
	h, engine := newCreatedStreamHarness(t, ingestTestConfig(true, false, true))
	rr := doRequest(t, h.Routes(), http.MethodPost, "/api/streams",
		strings.NewReader(`{"stream_id":"room-idle","name":"空闲"}`), "admin", "pass")
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
	engine.stream = nil

	rr = doRequest(t, h.Routes(), http.MethodDelete, "/api/streams/room-idle", nil, "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	row, err := h.db.GetCreatedStream(context.Background(), "room-idle")
	require.NoError(t, err)
	require.Nil(t, row)
}

func TestDeleteStream_RemovesCreatedSlotWhenKickFails(t *testing.T) {
	h, engine := newCreatedStreamHarness(t, ingestTestConfig(true, false, true))
	rr := doRequest(t, h.Routes(), http.MethodPost, "/api/streams",
		strings.NewReader(`{"stream_id":"room-kick","name":"直推"}`), "admin", "pass")
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())

	engine.stream = &media.StreamInfo{
		StreamID:  "room-kick",
		Active:    true,
		Publisher: &media.SessionInfo{SessionID: "pub-1", Protocol: "rtmp"},
	}
	engine.kickErr = errors.New("session not found")

	rr = doRequest(t, h.Routes(), http.MethodDelete, "/api/streams/room-kick", nil, "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	row, err := h.db.GetCreatedStream(context.Background(), "room-kick")
	require.NoError(t, err)
	require.Nil(t, row)
}

func TestDeleteStream_RemovesCreatedSlotWhenGetStreamErrors(t *testing.T) {
	h, engine := newCreatedStreamHarness(t, ingestTestConfig(true, false, true))
	rr := doRequest(t, h.Routes(), http.MethodPost, "/api/streams",
		strings.NewReader(`{"stream_id":"room-err","name":"异常"}`), "admin", "pass")
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
	engine.getErr = errors.New("lalmax error 500: group lookup failed")

	rr = doRequest(t, h.Routes(), http.MethodDelete, "/api/streams/room-err", nil, "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	row, err := h.db.GetCreatedStream(context.Background(), "room-err")
	require.NoError(t, err)
	require.Nil(t, row)
}

func TestDeleteStream_RemovesRecordingPlan(t *testing.T) {
	h, engine := newCreatedStreamHarness(t, ingestTestConfig(true, false, true))
	ctx := context.Background()
	rr := doRequest(t, h.Routes(), http.MethodPost, "/api/streams",
		strings.NewReader(`{"stream_id":"room-plan","name":"计划流"}`), "admin", "pass")
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
	require.NoError(t, h.db.UpsertRecordingPlan(ctx, &storage.RecordingPlan{
		StreamID: "room-plan", Mode: storage.RecordingModeContinuous, Enabled: true,
	}))
	engine.stream = nil

	rr = doRequest(t, h.Routes(), http.MethodDelete, "/api/streams/room-plan", nil, "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	plan, err := h.db.GetRecordingPlanByStream(ctx, "room-plan")
	require.NoError(t, err)
	require.Nil(t, plan)
}

func TestDeleteStream_RemovesCreatedSlotWhileLive(t *testing.T) {
	h, engine := newCreatedStreamHarness(t, ingestTestConfig(true, false, true))
	rr := doRequest(t, h.Routes(), http.MethodPost, "/api/streams",
		strings.NewReader(`{"stream_id":"room-2","name":"大厅"}`), "admin", "pass")
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())

	engine.stream = &media.StreamInfo{StreamID: "room-2", AppName: "live", Active: true, Publisher: &media.SessionInfo{SessionID: "pub-1"}}
	rr = doRequest(t, h.Routes(), http.MethodDelete, "/api/streams/room-2", nil, "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	row, err := h.db.GetCreatedStream(context.Background(), "room-2")
	require.NoError(t, err)
	require.Nil(t, row)

	engine.stream = nil
	rr = doRequest(t, h.Routes(), http.MethodGet, "/api/streams/room-2", nil, "admin", "pass")
	require.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())
}

func TestUpdateStream_RenamesCreatedDisplayName(t *testing.T) {
	h, _ := newCreatedStreamHarness(t, ingestTestConfig(true, true, true))
	rr := doRequest(t, h.Routes(), http.MethodPost, "/api/streams",
		strings.NewReader(`{"stream_id":"room-1","name":"会议室"}`), "admin", "pass")
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())

	rr = doRequest(t, h.Routes(), http.MethodPut, "/api/streams/room-1",
		strings.NewReader(`{"name":"大会议室"}`), "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var updated streamSummary
	parseJSON(t, rr, &updated)
	require.Equal(t, "大会议室", updated.Name)

	rr = doRequest(t, h.Routes(), http.MethodGet, "/api/streams/room-1", nil, "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var got streamSummary
	parseJSON(t, rr, &got)
	require.Equal(t, "大会议室", got.Name)

	row, err := h.db.GetCreatedStream(context.Background(), "room-1")
	require.NoError(t, err)
	require.Equal(t, "大会议室", row.Name)
}

func TestUpdateStream_EmptyNameFallsBackToStreamID(t *testing.T) {
	h, _ := newCreatedStreamHarness(t, ingestTestConfig(true, true, true))
	rr := doRequest(t, h.Routes(), http.MethodPost, "/api/streams",
		strings.NewReader(`{"stream_id":"room-1","name":"会议室"}`), "admin", "pass")
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())

	rr = doRequest(t, h.Routes(), http.MethodPut, "/api/streams/room-1",
		strings.NewReader(`{"name":"  "}`), "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var updated streamSummary
	parseJSON(t, rr, &updated)
	require.Equal(t, "room-1", updated.Name)
}

func TestUpdateStream_LiveUnmanagedUpsertsDisplayName(t *testing.T) {
	h, engine := newCreatedStreamHarness(t, ingestTestConfig(true, false, true))
	engine.stream = &media.StreamInfo{StreamID: "obs-1", AppName: "live", Active: true}

	rr := doRequest(t, h.Routes(), http.MethodPut, "/api/streams/obs-1",
		strings.NewReader(`{"name":"导播"}`), "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var updated streamSummary
	parseJSON(t, rr, &updated)
	require.Equal(t, "导播", updated.Name)

	row, err := h.db.GetCreatedStream(context.Background(), "obs-1")
	require.NoError(t, err)
	require.NotNil(t, row)
	require.Equal(t, "导播", row.Name)
}

func TestUpdateStream_UnknownStreamNotFound(t *testing.T) {
	h, _ := newCreatedStreamHarness(t, ingestTestConfig(true, true, true))
	rr := doRequest(t, h.Routes(), http.MethodPut, "/api/streams/missing",
		strings.NewReader(`{"name":"x"}`), "admin", "pass")
	require.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())
}

func TestCreateStream_PullsFromSourceURL(t *testing.T) {
	h, engine := newCreatedStreamHarness(t, ingestTestConfig(false, false, false))

	rr := doRequest(t, h.Routes(), http.MethodPost, "/api/streams",
		strings.NewReader(`{"stream_id":"src-1","name":"门口","input_mode":"pull","source_url":"rtsp://192.168.1.20/live"}`),
		"admin", "pass")
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())

	var created streamSummary
	parseJSON(t, rr, &created)
	require.Equal(t, "relay_pull", created.SourceType)
	require.Equal(t, "rtsp://192.168.1.20/live", created.SourceURL)
	require.Empty(t, created.IngestURLs)
	require.Len(t, engine.pulls, 1)
	require.Equal(t, "src-1", engine.pulls[0].StreamID)
	require.Equal(t, "rtsp://192.168.1.20/live", engine.pulls[0].SourceURL)
	require.Equal(t, -1, engine.pulls[0].PullRetryNum)

	rr = doRequest(t, h.Routes(), http.MethodGet, "/api/streams?managed=false", nil, "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var listed streamListResponse
	parseJSON(t, rr, &listed)
	require.Equal(t, 1, listed.Total)
	require.Equal(t, "relay_pull", listed.Streams[0].SourceType)
	require.Equal(t, "门口", listed.Streams[0].Name)

	rr = doRequest(t, h.Routes(), http.MethodPost, "/api/streams",
		strings.NewReader(`{"stream_id":"src-2","input_mode":"pull","source_url":"not a url"}`),
		"admin", "pass")
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())

	h.RestoreCreatedPulls(context.Background())
	require.Len(t, engine.pulls, 2)
	require.Equal(t, "src-1", engine.pulls[1].StreamID)
}

func protocolsOf(urls []streamPlayURL) []string {
	out := make([]string, 0, len(urls))
	for _, item := range urls {
		out = append(out, item.Protocol)
	}
	return out
}

func urlFor(urls []streamPlayURL, protocol string) string {
	for _, item := range urls {
		if item.Protocol == protocol {
			return item.URL
		}
	}
	return ""
}
