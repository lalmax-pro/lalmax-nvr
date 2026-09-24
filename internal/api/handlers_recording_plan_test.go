package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

func listPlans(t *testing.T, h *Handler) []storage.RecordingPlan {
	t.Helper()
	rr := doRequest(t, h.Routes(), "GET", "/api/recording-plans", nil, "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code)
	var resp struct {
		Plans []storage.RecordingPlan `json:"plans"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	return resp.Plans
}

func TestRecordingPlan_CRUD(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)

	require.Empty(t, listPlans(t, h))

	body := `{"stream_id":"obs-1","name":"OBS","mode":"scheduled","windows":[{"day_of_week":1,"start_time":"09:00","end_time":"17:00"}]}`
	rr := doRequest(t, h.Routes(), "POST", "/api/recording-plans", strings.NewReader(body), "admin", "pass")
	require.Equal(t, http.StatusCreated, rr.Code)

	var created storage.RecordingPlan
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &created))
	require.NotEmpty(t, created.ID)
	require.Equal(t, "obs-1", created.StreamID)
	require.Equal(t, storage.RecordingModeScheduled, created.Mode)
	require.True(t, created.Enabled)
	require.Len(t, created.Windows, 1)

	plans := listPlans(t, h)
	require.Len(t, plans, 1)

	// The same stream cannot have two plans.
	rr = doRequest(t, h.Routes(), "POST", "/api/recording-plans", strings.NewReader(body), "admin", "pass")
	require.Equal(t, http.StatusConflict, rr.Code)

	// A partial pause/resume update must preserve the recording mode and windows.
	upd := `{"enabled":false}`
	rr = doRequest(t, h.Routes(), "PUT", "/api/recording-plans/"+created.ID, strings.NewReader(upd), "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code)
	var updated storage.RecordingPlan
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &updated))
	require.Equal(t, storage.RecordingModeScheduled, updated.Mode)
	require.False(t, updated.Enabled)
	require.Equal(t, "obs-1", updated.StreamID, "stream_id is kept when omitted")
	require.Len(t, updated.Windows, 1, "windows are kept when omitted")

	// An explicit mode change still takes effect.
	upd = `{"mode":"continuous","enabled":true}`
	rr = doRequest(t, h.Routes(), "PUT", "/api/recording-plans/"+created.ID, strings.NewReader(upd), "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code)
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &updated))
	require.Equal(t, storage.RecordingModeContinuous, updated.Mode)
	require.True(t, updated.Enabled)
	require.Len(t, updated.Windows, 1, "windows are kept when omitted")

	rr = doRequest(t, h.Routes(), "DELETE", "/api/recording-plans/"+created.ID, nil, "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Empty(t, listPlans(t, h))

	rr = doRequest(t, h.Routes(), "DELETE", "/api/recording-plans/"+created.ID, nil, "admin", "pass")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestRecordingPlan_Validation(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)

	cases := []struct {
		name string
		body string
		want int
	}{
		{"missing stream", `{"name":"x"}`, http.StatusBadRequest},
		{"bad mode", `{"stream_id":"s1","mode":"nope"}`, http.StatusBadRequest},
		{"bad day", `{"stream_id":"s1","windows":[{"day_of_week":9,"start_time":"09:00","end_time":"10:00"}]}`, http.StatusBadRequest},
		{"bad time", `{"stream_id":"s1","windows":[{"day_of_week":1,"start_time":"9:00","end_time":"10:00"}]}`, http.StatusBadRequest},
		{"ok", `{"stream_id":"s1","mode":"off"}`, http.StatusCreated},
	}
	for _, tc := range cases {
		rr := doRequest(t, h.Routes(), "POST", "/api/recording-plans", strings.NewReader(tc.body), "admin", "pass")
		require.Equalf(t, tc.want, rr.Code, "case %s", tc.name)
	}
}

// Promoting a stream to a device must not start recording: recording is driven
// by recording plans only.
func TestPauseResume_DoesNotInventRecordingPlan(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	ctx := context.Background()
	require.NoError(t, db.UpsertCamera(ctx, "cam-1", "Cam", "rtsp", "h264", "rtsp://x", "", "", true, "", "", ""))
	require.NoError(t, db.SetCameraStream(ctx, "cam-1", "obs-1"))

	req := httptest.NewRequest(http.MethodPost, "/api/cameras/cam-1/pause-recording", nil)
	require.NoError(t, h.setStreamRecordingEnabled(req, "cam-1", false))
	plan, err := db.GetRecordingPlanByStream(ctx, "obs-1")
	require.NoError(t, err)
	require.Nil(t, plan, "pause must not create a recording plan")

	require.NoError(t, h.setStreamRecordingEnabled(req, "cam-1", true))
	plan, err = db.GetRecordingPlanByStream(ctx, "obs-1")
	require.NoError(t, err)
	require.Nil(t, plan, "resume must not create a recording plan")

	require.NoError(t, db.UpsertRecordingPlan(ctx, &storage.RecordingPlan{
		StreamID: "obs-1", Mode: storage.RecordingModeScheduled, Enabled: true,
	}))
	require.NoError(t, h.setStreamRecordingEnabled(req, "cam-1", false))
	plan, err = db.GetRecordingPlanByStream(ctx, "obs-1")
	require.NoError(t, err)
	require.NotNil(t, plan)
	require.False(t, plan.Enabled)
	require.Equal(t, storage.RecordingModeScheduled, plan.Mode)
}

func TestPromoteStream_DoesNotCreateRecordingPlan(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()

	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetMediaEngine(&stubMediaEngine{
		stream: &media.StreamInfo{
			StreamID:   "obs-1",
			AppName:    "live",
			Active:     true,
			VideoCodec: "h264",
			Publisher:  &media.SessionInfo{SessionID: "pub-1", Protocol: "rtmp", Remote: "10.0.0.5:1935"},
		},
	})

	rr := doRequest(t, h.Routes(), "POST", "/api/streams/obs-1/promote",
		strings.NewReader(`{"name":"OBS"}`), "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code)

	plans, err := db.ListRecordingPlans(context.Background())
	require.NoError(t, err)
	require.Empty(t, plans, "promote must not create a recording plan")
}

func TestListCameras_RecordingModeComesFromBoundStreamPlan(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	ctx := context.Background()

	require.NoError(t, db.UpsertCamera(ctx, "cam-1", "Cam", "rtsp", "h264", "rtsp://x", "", "", true, "", "", ""))
	require.NoError(t, db.SetCameraStream(ctx, "cam-1", "obs-1"))

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras", nil, "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code)
	var cameras []storage.CameraRow
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &cameras))
	require.Len(t, cameras, 1)
	require.Equal(t, "obs-1", cameras[0].StreamID)
	require.Equal(t, storage.RecordingModeOff, cameras[0].RecordingMode, "no plan means no recording")

	require.NoError(t, db.UpsertRecordingPlan(ctx, &storage.RecordingPlan{
		StreamID: "obs-1",
		Name:     "OBS",
		Mode:     storage.RecordingModeScheduled,
		Enabled:  true,
	}))

	rr = doRequest(t, h.Routes(), "GET", "/api/cameras", nil, "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code)
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &cameras))
	require.Len(t, cameras, 1)
	require.Equal(t, storage.RecordingModeScheduled, cameras[0].RecordingMode)
}
