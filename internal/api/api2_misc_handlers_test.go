package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/ai"
	"github.com/lalmax-pro/lalmax-nvr/internal/ai/multimodal"
	"github.com/lalmax-pro/lalmax-nvr/internal/camera"
	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/lalmax-pro/lalmax-nvr/internal/transcode"
	"github.com/stretchr/testify/require"
)

// ==================== shared helpers (api2m prefix) ====================

// api2mRepo wraps a real repository and injects failures for selected methods so
// storage-error branches of the misc handlers can be exercised deterministically.
type api2mRepo struct {
	storage.APIRepository
	fail    map[string]error
	nilList map[string]bool
}

func api2mFailing(base storage.APIRepository, methods ...string) *api2mRepo {
	fail := make(map[string]error, len(methods))
	for _, m := range methods {
		fail[m] = errors.New("api2m injected failure")
	}
	return &api2mRepo{APIRepository: base, fail: fail}
}

func (r *api2mRepo) boom(name string) error { return r.fail[name] }

// api2mNilLists makes the selected list methods return (nil, nil) so the
// handlers' empty-slice guards can be exercised.
func api2mNilLists(base storage.APIRepository, methods ...string) *api2mRepo {
	nilList := make(map[string]bool, len(methods))
	for _, m := range methods {
		nilList[m] = true
	}
	return &api2mRepo{APIRepository: base, nilList: nilList}
}

func (r *api2mRepo) ListDeviceGroups(ctx context.Context) ([]storage.DeviceGroup, error) {
	if err := r.boom("ListDeviceGroups"); err != nil {
		return nil, err
	}
	if r.nilList["ListDeviceGroups"] {
		return nil, nil
	}
	return r.APIRepository.ListDeviceGroups(ctx)
}

func (r *api2mRepo) GetDeviceGroup(ctx context.Context, id int64) (*storage.DeviceGroup, error) {
	if err := r.boom("GetDeviceGroup"); err != nil {
		return nil, err
	}
	return r.APIRepository.GetDeviceGroup(ctx, id)
}

func (r *api2mRepo) GetGroupChannelStats(ctx context.Context, id int64) (int, int, error) {
	if err := r.boom("GetGroupChannelStats"); err != nil {
		return 0, 0, err
	}
	return r.APIRepository.GetGroupChannelStats(ctx, id)
}

func (r *api2mRepo) CreateDeviceGroup(ctx context.Context, g *storage.DeviceGroup) (int64, error) {
	if err := r.boom("CreateDeviceGroup"); err != nil {
		return 0, err
	}
	return r.APIRepository.CreateDeviceGroup(ctx, g)
}

func (r *api2mRepo) UpdateDeviceGroup(ctx context.Context, g *storage.DeviceGroup) error {
	if err := r.boom("UpdateDeviceGroup"); err != nil {
		return err
	}
	return r.APIRepository.UpdateDeviceGroup(ctx, g)
}

func (r *api2mRepo) DeleteDeviceGroup(ctx context.Context, id int64) error {
	if err := r.boom("DeleteDeviceGroup"); err != nil {
		return err
	}
	return r.APIRepository.DeleteDeviceGroup(ctx, id)
}

func (r *api2mRepo) ListGroupChannels(ctx context.Context, id int64) ([]storage.DeviceGroupChannelDetail, error) {
	if err := r.boom("ListGroupChannels"); err != nil {
		return nil, err
	}
	if r.nilList["ListGroupChannels"] {
		return nil, nil
	}
	return r.APIRepository.ListGroupChannels(ctx, id)
}

func (r *api2mRepo) AddGroupChannel(ctx context.Context, groupID int64, deviceID, channelID string) error {
	if err := r.boom("AddGroupChannel"); err != nil {
		return err
	}
	return r.APIRepository.AddGroupChannel(ctx, groupID, deviceID, channelID)
}

func (r *api2mRepo) RemoveGroupChannel(ctx context.Context, groupID int64, deviceID, channelID string) error {
	if err := r.boom("RemoveGroupChannel"); err != nil {
		return err
	}
	return r.APIRepository.RemoveGroupChannel(ctx, groupID, deviceID, channelID)
}

func (r *api2mRepo) ListEvents(ctx context.Context, f storage.EventsFilter) ([]model.Event, int, error) {
	if err := r.boom("ListEvents"); err != nil {
		return nil, 0, err
	}
	return r.APIRepository.ListEvents(ctx, f)
}

func (r *api2mRepo) GetEvent(ctx context.Context, id int64) (*model.Event, error) {
	if err := r.boom("GetEvent"); err != nil {
		return nil, err
	}
	return r.APIRepository.GetEvent(ctx, id)
}

func (r *api2mRepo) AcknowledgeEvent(ctx context.Context, id int64, at time.Time) error {
	if err := r.boom("AcknowledgeEvent"); err != nil {
		return err
	}
	return r.APIRepository.AcknowledgeEvent(ctx, id, at)
}

func (r *api2mRepo) DeleteEvent(ctx context.Context, id int64) error {
	if err := r.boom("DeleteEvent"); err != nil {
		return err
	}
	return r.APIRepository.DeleteEvent(ctx, id)
}

func (r *api2mRepo) ListAlarmRules(ctx context.Context) ([]model.AlarmRule, error) {
	if err := r.boom("ListAlarmRules"); err != nil {
		return nil, err
	}
	return r.APIRepository.ListAlarmRules(ctx)
}

func (r *api2mRepo) InsertAlarmRule(ctx context.Context, rule *model.AlarmRule) error {
	if err := r.boom("InsertAlarmRule"); err != nil {
		return err
	}
	return r.APIRepository.InsertAlarmRule(ctx, rule)
}

func (r *api2mRepo) DeleteAlarmRule(ctx context.Context, id int64) error {
	if err := r.boom("DeleteAlarmRule"); err != nil {
		return err
	}
	return r.APIRepository.DeleteAlarmRule(ctx, id)
}

func (r *api2mRepo) ListAIDetections(ctx context.Context, f storage.AIHistoryFilter) ([]ai.DetectionResult, int, error) {
	if err := r.boom("ListAIDetections"); err != nil {
		return nil, 0, err
	}
	return r.APIRepository.ListAIDetections(ctx, f)
}

func (r *api2mRepo) ListAIAnalyses(ctx context.Context, f storage.AIHistoryFilter) ([]multimodal.AnalysisResult, int, error) {
	if err := r.boom("ListAIAnalyses"); err != nil {
		return nil, 0, err
	}
	return r.APIRepository.ListAIAnalyses(ctx, f)
}

func (r *api2mRepo) ListArchivedCameras(ctx context.Context) ([]storage.CameraRow, error) {
	if err := r.boom("ListArchivedCameras"); err != nil {
		return nil, err
	}
	return r.APIRepository.ListArchivedCameras(ctx)
}

func (r *api2mRepo) GetArchiveGroupStats(ctx context.Context, cameraID string) (int, int64, error) {
	if err := r.boom("GetArchiveGroupStats"); err != nil {
		return 0, 0, err
	}
	return r.APIRepository.GetArchiveGroupStats(ctx, cameraID)
}

func (r *api2mRepo) GetCamera(ctx context.Context, cameraID string) (*storage.CameraRow, error) {
	if err := r.boom("GetCamera"); err != nil {
		return nil, err
	}
	return r.APIRepository.GetCamera(ctx, cameraID)
}

func (r *api2mRepo) ListRecordings(ctx context.Context, f model.RecordingFilter) ([]model.Recording, error) {
	if err := r.boom("ListRecordings"); err != nil {
		return nil, err
	}
	return r.APIRepository.ListRecordings(ctx, f)
}

func (r *api2mRepo) CountRecordingsWithFilter(ctx context.Context, f model.RecordingFilter) (int, error) {
	if err := r.boom("CountRecordingsWithFilter"); err != nil {
		return 0, err
	}
	return r.APIRepository.CountRecordingsWithFilter(ctx, f)
}

func (r *api2mRepo) GetRecording(ctx context.Context, id string) (*model.Recording, error) {
	if err := r.boom("GetRecording"); err != nil {
		return nil, err
	}
	return r.APIRepository.GetRecording(ctx, id)
}

func (r *api2mRepo) DeleteRecording(ctx context.Context, id string) error {
	if err := r.boom("DeleteRecording"); err != nil {
		return err
	}
	return r.APIRepository.DeleteRecording(ctx, id)
}

func (r *api2mRepo) DeleteCamera(ctx context.Context, cameraID string) error {
	if err := r.boom("DeleteCamera"); err != nil {
		return err
	}
	return r.APIRepository.DeleteCamera(ctx, cameraID)
}

func (r *api2mRepo) DeleteRecordingsByCamera(ctx context.Context, cameraID string) (int64, error) {
	if err := r.boom("DeleteRecordingsByCamera"); err != nil {
		return 0, err
	}
	return r.APIRepository.DeleteRecordingsByCamera(ctx, cameraID)
}

// api2mNoFlushWriter hides http.Flusher so the SSE handlers take their
// "streaming not supported" branch.
type api2mNoFlushWriter struct {
	http.ResponseWriter
}

// api2mPump drives a streaming handler until want shows up in its body.
// publish is called repeatedly (bounded by a deadline) so the test never sleeps
// on an unbounded duration and converges as soon as the handler flushed data.
func api2mPump(t *testing.T, body func() string, done <-chan struct{}, want string, publish func()) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(body(), want) {
		select {
		case <-done:
			t.Fatalf("handler stopped before %q arrived; body=%q", want, body())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %q; body=%q", want, body())
		}
		publish()
		time.Sleep(2 * time.Millisecond)
	}
}

func api2mWaitDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not stop after context cancellation")
	}
}

func api2mNewHandler(t *testing.T) (*storage.DB, *storage.Manager, *Handler) {
	t.Helper()
	db, store := setupTestDB(t)
	return db, store, TestHandler(db, store)
}

// api2mNewCamHandler builds a handler wired with a real CameraManager and config.
func api2mNewCamHandler(t *testing.T, cams []config.CameraConfig) (*storage.DB, *storage.Manager, *Handler, *camera.CameraManager) {
	t.Helper()
	db, store := setupTestDB(t)
	cfg := &config.Config{
		Storage: config.StorageConfig{RootDir: store.RootDir(), SegmentDuration: "30s"},
		Cleanup: config.CleanupConfig{RetentionDays: 30, CheckInterval: "1h", DiskThresholdPercent: 95},
		Cameras: cams,
	}
	camMgr := camera.NewCameraManager(cfg, store, db, "")
	h := NewHandler(db, store, noopAuthMW(), cfg, camMgr, "", nil, nil)
	return db, store, h, camMgr
}

func api2mSeedArchivedCamera(t *testing.T, db *storage.DB, id, name string) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, db.UpsertCamera(ctx, id, name, "rtsp_h264", "h264", "rtsp://example.invalid/"+id, "", "", false, "", "", ""))
	require.NoError(t, db.ArchiveCameraDB(ctx, id))
}

func api2mFFmpegStub(t *testing.T, dir, name, script string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755))
	return path
}

// ==================== device group handlers ====================

func TestApi2MiscGroupHandlers(t *testing.T) {
	db, _, h := api2mNewHandler(t)
	router := h.Routes()

	rr := doRequest(t, router, "GET", "/api/groups", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"groups"`)

	rr = doRequest(t, router, "POST", "/api/groups", strings.NewReader("{bad"), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "invalid request body")

	rr = doRequest(t, router, "POST", "/api/groups", strings.NewReader(`{"sort_order":3}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "name is required")

	rr = doRequest(t, router, "POST", "/api/groups", strings.NewReader(`{"name":"orphan","parent_id":999}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "parent group not found")

	rr = doRequest(t, router, "POST", "/api/groups", strings.NewReader(`{"name":"root","sort_order":2}`), "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var created struct {
		Group  storage.DeviceGroup `json:"group"`
		Status string              `json:"status"`
	}
	parseJSON(t, rr, &created)
	require.Equal(t, "ok", created.Status)
	require.NotZero(t, created.Group.ID)
	rootID := strconv.FormatInt(created.Group.ID, 10)

	rr = doRequest(t, router, "POST", "/api/groups", strings.NewReader(`{"name":"child","parent_id":`+rootID+`}`), "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var child struct {
		Group storage.DeviceGroup `json:"group"`
	}
	parseJSON(t, rr, &child)
	require.Equal(t, 1, child.Group.Level)
	childID := strconv.FormatInt(child.Group.ID, 10)

	rr = doRequest(t, router, "GET", "/api/groups/"+childID, nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "channel_total")

	rr = doRequest(t, router, "GET", "/api/groups/not-a-number", nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, router, "GET", "/api/groups/424242", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)

	rr = doRequest(t, router, "PUT", "/api/groups/x", strings.NewReader(`{}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, router, "PUT", "/api/groups/424242", strings.NewReader(`{}`), "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
	rr = doRequest(t, router, "PUT", "/api/groups/"+childID, strings.NewReader("{bad"), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)

	rr = doRequest(t, router, "PUT", "/api/groups/"+childID, strings.NewReader(`{"name":"child2","parent_id":0,"sort_order":5}`), "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var updated struct {
		Group storage.DeviceGroup `json:"group"`
	}
	parseJSON(t, rr, &updated)
	require.Equal(t, "child2", updated.Group.Name)
	require.Equal(t, 0, updated.Group.Level)
	require.Equal(t, 5, updated.Group.SortOrder)

	rr = doRequest(t, router, "PUT", "/api/groups/"+childID, strings.NewReader(`{"parent_id":`+rootID+`}`), "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	parseJSON(t, rr, &updated)
	require.Equal(t, 1, updated.Group.Level)

	// A missing parent only skips level recalculation.
	rr = doRequest(t, router, "PUT", "/api/groups/"+childID, strings.NewReader(`{"parent_id":424242}`), "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	parseJSON(t, rr, &updated)
	require.Equal(t, 1, updated.Group.Level)

	// Put the child back under root so the tree has a nested node.
	rr = doRequest(t, router, "PUT", "/api/groups/"+childID, strings.NewReader(`{"parent_id":`+rootID+`}`), "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	rr = doRequest(t, router, "GET", "/api/groups/"+rootID+"/channels", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	rr = doRequest(t, router, "GET", "/api/groups/nope/channels", nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)

	rr = doRequest(t, router, "POST", "/api/groups/nope/channels", strings.NewReader(`{}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, router, "POST", "/api/groups/"+rootID+"/channels", strings.NewReader("{bad"), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, router, "POST", "/api/groups/"+rootID+"/channels", strings.NewReader(`{"device_id":"dev-1"}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, router, "POST", "/api/groups/"+rootID+"/channels", strings.NewReader(`{"device_id":"dev-1","channel_id":"ch-1"}`), "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	rr = doRequest(t, router, "POST", "/api/groups/"+rootID+"/channels", strings.NewReader(`{"device_id":"dev-2","channel_id":"ch-2"}`), "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	rr = doRequest(t, router, "GET", "/api/groups/"+rootID+"/channels", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var channels struct {
		Channels []storage.DeviceGroupChannelDetail `json:"channels"`
	}
	parseJSON(t, rr, &channels)
	require.Len(t, channels.Channels, 2)

	rr = doRequest(t, router, "GET", "/api/groups/tree?stats=true", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var tree struct {
		Tree []GroupTreeNode `json:"tree"`
	}
	parseJSON(t, rr, &tree)
	require.NotEmpty(t, tree.Tree)
	hasNested := false
	for _, node := range tree.Tree {
		if len(node.Children) > 0 {
			hasNested = true
		}
	}
	require.True(t, hasNested, "expected the child group to be nested under its parent")

	rr = doRequest(t, router, "DELETE", "/api/groups/x/channels", strings.NewReader(`{}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, router, "DELETE", "/api/groups/"+rootID+"/channels", strings.NewReader("{bad"), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, router, "DELETE", "/api/groups/"+rootID+"/channels", strings.NewReader(`{"device_id":"","channel_id":""}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, router, "DELETE", "/api/groups/"+rootID+"/channels", strings.NewReader(`{"device_id":"dev-1","channel_id":"ch-1"}`), "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	rr = doRequest(t, router, "PATCH", "/api/groups/"+rootID, nil, "", "")
	require.Equal(t, http.StatusMethodNotAllowed, rr.Code)

	rr = doRequest(t, router, "DELETE", "/api/groups/x", nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, router, "DELETE", "/api/groups/"+rootID, nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	if _, err := db.GetDeviceGroup(context.Background(), created.Group.ID); err == nil {
		t.Fatal("expected deleted group to be gone")
	}
}

func TestApi2MiscGroupRepoErrors(t *testing.T) {
	t.Run("nil list renders empty array", func(t *testing.T) {
		db, store := setupTestDB(t)
		h := TestHandler(api2mNilLists(db, "ListDeviceGroups", "ListGroupChannels"), store)
		rr := doRequest(t, h.Routes(), "GET", "/api/groups", nil, "", "")
		require.Equal(t, http.StatusOK, rr.Code)
		require.Contains(t, rr.Body.String(), `"groups":[]`)

		rr = doRequest(t, h.Routes(), "GET", "/api/groups/1/channels", nil, "", "")
		require.Equal(t, http.StatusOK, rr.Code)
		require.Contains(t, rr.Body.String(), `"channels":[]`)
	})

	for _, tc := range []struct {
		name   string
		fail   string
		method string
		path   string
		body   string
		want   int
	}{
		{"list", "ListDeviceGroups", "GET", "/api/groups", "", http.StatusInternalServerError},
		{"tree", "ListDeviceGroups", "GET", "/api/groups/tree?stats=true", "", http.StatusInternalServerError},
		{"get", "GetDeviceGroup", "GET", "/api/groups/1", "", http.StatusNotFound},
		{"create", "CreateDeviceGroup", "POST", "/api/groups", `{"name":"x"}`, http.StatusInternalServerError},
		{"create-parent", "GetDeviceGroup", "POST", "/api/groups", `{"name":"x","parent_id":1}`, http.StatusBadRequest},
		{"update", "UpdateDeviceGroup", "PUT", "/api/groups/1", `{"name":"y"}`, http.StatusInternalServerError},
		{"delete", "DeleteDeviceGroup", "DELETE", "/api/groups/1", "", http.StatusInternalServerError},
		{"channels", "ListGroupChannels", "GET", "/api/groups/1/channels", "", http.StatusInternalServerError},
		{"add", "AddGroupChannel", "POST", "/api/groups/1/channels", `{"device_id":"d","channel_id":"c"}`, http.StatusInternalServerError},
		{"remove", "RemoveGroupChannel", "DELETE", "/api/groups/1/channels", `{"device_id":"d","channel_id":"c"}`, http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, store := setupTestDB(t)
			_, err := db.CreateDeviceGroup(context.Background(), &storage.DeviceGroup{Name: "seed"})
			require.NoError(t, err)
			h := TestHandler(api2mFailing(db, tc.fail), store)
			rr := doRequest(t, h.Routes(), tc.method, tc.path, strings.NewReader(tc.body), "", "")
			require.Equal(t, tc.want, rr.Code, rr.Body.String())
		})
	}
}

// ==================== events / alarm rules ====================

func TestApi2MiscEventHandlers(t *testing.T) {
	// A non-nil CameraManager is required: alarm-rule linkage dispatches event
	// notifications to the camera manager, which must not be a nil pointer.
	db, _, h, _ := api2mNewCamHandler(t, nil)
	router := h.Routes()
	now := time.Now().UTC()

	rr := doRequest(t, router, "GET", "/api/events?limit=abc", nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "invalid limit parameter")
	rr = doRequest(t, router, "GET", "/api/events?limit=-1", nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, router, "GET", "/api/events?offset=abc", nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	require.Contains(t, rr.Body.String(), "invalid offset parameter")
	rr = doRequest(t, router, "GET", "/api/events?offset=-1", nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)

	eventID, err := db.InsertEvent(context.Background(), model.Event{
		CameraID:  "cam-evt",
		Source:    model.EventSourceHealth,
		Type:      string(model.HealthEventConnectionLost),
		Severity:  model.EventSeverityCritical,
		Status:    model.EventStatusOpen,
		Message:   "misc event",
		Metadata:  "{}",
		StartedAt: now,
		CreatedAt: now,
	})
	require.NoError(t, err)
	idStr := strconv.FormatInt(eventID, 10)

	rr = doRequest(t, router, "GET", "/api/events?limit=10&offset=0&camera_id=cam-evt&source=health&type="+string(model.HealthEventConnectionLost)+"&status=open", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "misc event")

	rr = doRequest(t, router, "GET", "/api/events/nope", nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, router, "GET", "/api/events/0", nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, router, "GET", "/api/events/424242", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)

	rr = doRequest(t, router, "GET", "/api/events/"+idStr, nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "misc event")

	rr = doRequest(t, router, "POST", "/api/events/not-a-number/ack", nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, router, "POST", "/api/events/"+idStr+"/ack", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	rr = doRequest(t, router, "POST", "/api/events/424242/ack", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)

	rr = doRequest(t, router, "GET", "/api/events/rules", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	rr = doRequest(t, router, "POST", "/api/events/rules", strings.NewReader("{bad"), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, router, "POST", "/api/events/rules", strings.NewReader(`{"action":"bogus"}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, router, "POST", "/api/events/rules", strings.NewReader(`{"action":"webhook"}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, router, "POST", "/api/events/rules", strings.NewReader(`{"action":"goto_preset"}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, router, "POST", "/api/events/rules", strings.NewReader(`{"action":"record"}`), "", "")
	require.Equal(t, http.StatusCreated, rr.Code)
	var rule model.AlarmRule
	parseJSON(t, rr, &rule)
	require.NotZero(t, rule.ID)
	require.Equal(t, "record", rule.Name)
	ruleID := strconv.FormatInt(rule.ID, 10)

	rr = doRequest(t, router, "DELETE", "/api/events/rules/nope", nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, router, "DELETE", "/api/events/rules/0", nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, router, "DELETE", "/api/events/rules/424242", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
	rr = doRequest(t, router, "DELETE", "/api/events/rules/"+ruleID, nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	rr = doRequest(t, router, "DELETE", "/api/events/not-a-number", nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, router, "DELETE", "/api/events/424242", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
	rr = doRequest(t, router, "DELETE", "/api/events/"+idStr, nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	rr = doRequest(t, router, "PATCH", "/api/events/rules", nil, "", "")
	require.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

func TestApi2MiscEventRepoErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fail   string
		method string
		path   string
		body   string
		want   int
	}{
		{"list", "ListEvents", "GET", "/api/events", "", http.StatusInternalServerError},
		{"get", "GetEvent", "GET", "/api/events/7", "", http.StatusInternalServerError},
		{"ack", "AcknowledgeEvent", "POST", "/api/events/7/ack", "", http.StatusInternalServerError},
		{"delete", "DeleteEvent", "DELETE", "/api/events/7", "", http.StatusInternalServerError},
		{"rules", "ListAlarmRules", "GET", "/api/events/rules", "", http.StatusInternalServerError},
		{"rule-create", "InsertAlarmRule", "POST", "/api/events/rules", `{"action":"record"}`, http.StatusInternalServerError},
		{"rule-delete", "DeleteAlarmRule", "DELETE", "/api/events/rules/7", "", http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, store := setupTestDB(t)
			h := TestHandler(api2mFailing(db, tc.fail), store)
			rr := doRequest(t, h.Routes(), tc.method, tc.path, strings.NewReader(tc.body), "", "")
			require.Equal(t, tc.want, rr.Code, rr.Body.String())
		})
	}
}

func TestApi2MiscEventsStream(t *testing.T) {
	t.Run("no hub", func(t *testing.T) {
		_, _, h := api2mNewHandler(t)
		h.eventHub = nil
		rr := doRequest(t, h.Routes(), "GET", "/api/events/stream", nil, "", "")
		require.Equal(t, http.StatusServiceUnavailable, rr.Code)
	})

	t.Run("no flusher", func(t *testing.T) {
		_, _, h := api2mNewHandler(t)
		rec := httptest.NewRecorder()
		// Call the handler directly: routing middleware wraps the writer with a
		// Flush-capable recorder, which would hide the unsupported-stream branch.
		h.handleEventsStream(&api2mNoFlushWriter{ResponseWriter: rec}, httptest.NewRequest(http.MethodGet, "/api/events/stream", nil))
		require.Equal(t, http.StatusInternalServerError, rec.Code)
		require.Contains(t, rec.Body.String(), "streaming not supported")
	})

	t.Run("filters and delivers", func(t *testing.T) {
		_, _, h := api2mNewHandler(t)
		router := h.Routes()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		req := httptest.NewRequest(http.MethodGet, "/api/events/stream?camera_id=cam-a&source=health", nil).WithContext(ctx)
		rec := newSSERecorder()
		done := make(chan struct{})
		go func() {
			defer close(done)
			router.ServeHTTP(rec, req)
		}()

		api2mPump(t, rec.String, done, "matched-event", func() {
			h.eventHub.Publish(model.Event{CameraID: "cam-b", Source: "health", Message: "wrong-camera"})
			h.eventHub.Publish(model.Event{CameraID: "cam-a", Source: "recorder", Message: "wrong-source"})
			h.eventHub.Publish(model.Event{CameraID: "cam-a", Source: "health", Message: "matched-event"})
		})
		cancel()
		api2mWaitDone(t, done)

		body := rec.String()
		require.Contains(t, body, "event: nvr")
		require.NotContains(t, body, "wrong-camera")
		require.NotContains(t, body, "wrong-source")
		require.Equal(t, http.StatusOK, rec.Code())
		require.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	})
}

// ==================== AI handlers ====================

func TestApi2MiscAIModelServe(t *testing.T) {
	db, store := setupTestDB(t)

	// No config at all.
	h := TestHandler(db, store)
	rr := doRequest(t, h.Routes(), "GET", "/models/test.onnx", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)

	// Empty storage root.
	h = TestHandler(db, store)
	h.config = &config.Config{}
	rr = doRequest(t, h.Routes(), "GET", "/models/test.onnx", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)

	// Valid root, invalid / missing filenames, then success.
	dataDir := t.TempDir()
	h.config = &config.Config{Storage: config.StorageConfig{RootDir: dataDir}}
	require.NoError(t, os.MkdirAll(filepath.Join(dataDir, "models"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "models", "yolo.onnx"), []byte("onnx-bytes"), 0o644))

	rr = doRequest(t, h.Routes(), "GET", "/models/.onnx", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
	rr = doRequest(t, h.Routes(), "GET", "/models/missing.onnx", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
	rr = doRequest(t, h.Routes(), "GET", "/models/yolo.onnx", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "onnx-bytes", rr.Body.String())
	require.Equal(t, "application/octet-stream", rr.Header().Get("Content-Type"))

	// A directory named like a model must not be served.
	require.NoError(t, os.MkdirAll(filepath.Join(dataDir, "models", "dir.onnx"), 0o755))
	rr = doRequest(t, h.Routes(), "GET", "/models/dir.onnx", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestApi2MiscAIStatusBranches(t *testing.T) {
	db, store := setupTestDB(t)
	dataDir := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{RootDir: dataDir},
		AI:      config.AIConfig{Enabled: true, Backend: "browser", ModelURL: "/models/yolo.onnx"},
	}
	h := NewHandler(db, store, noopAuthMW(), cfg, nil, "", nil, nil)
	mgr := ai.NewManager(cfg.AI)
	h.SetAIManager(mgr)
	t.Cleanup(mgr.StopAll)
	router := h.Routes()

	// Model path invalid (not under /models/).
	cfg.AI.ModelURL = "https://example.com/yolo.onnx"
	rr := doRequest(t, router, "GET", "/api/ai/status", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var status aiStatusResponse
	parseJSON(t, rr, &status)
	require.False(t, status.Available)
	require.Contains(t, status.Reason, "模型路径无效")

	// Model path valid but not installed.
	cfg.AI.ModelURL = "/models/yolo.onnx"
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "yolo.onnx"), []byte("x"), 0o644))
	rr = doRequest(t, router, "GET", "/api/ai/status", nil, "", "")
	parseJSON(t, rr, &status)
	require.False(t, status.Available)
	require.Contains(t, status.Reason, "模型未安装")

	// Model installed.
	require.NoError(t, os.MkdirAll(filepath.Join(dataDir, "models"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "models", "yolo.onnx"), []byte("x"), 0o644))
	rr = doRequest(t, router, "GET", "/api/ai/status", nil, "", "")
	parseJSON(t, rr, &status)
	require.True(t, status.Available)
	require.Equal(t, "browser", status.Backend)

	// Browser backend without a config is reported as-is.
	plain := TestHandler(db, store)
	plainMgr := ai.NewManager(config.AIConfig{Enabled: true, Backend: "browser"})
	plain.SetAIManager(plainMgr)
	t.Cleanup(plainMgr.StopAll)
	rr = doRequest(t, plain.Routes(), "GET", "/api/ai/status", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	parseJSON(t, rr, &status)
	require.True(t, status.Available)
	require.Equal(t, "browser", status.Backend)
}

func TestApi2MiscAIHistoryHandlers(t *testing.T) {
	db, store, h := api2mNewHandler(t)
	router := h.Routes()

	rr := doRequest(t, router, "GET", "/api/ai/detections?limit=5&offset=1&camera_id=cam-ai&label=person", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"detections"`)
	require.Contains(t, rr.Body.String(), `"total"`)

	rr = doRequest(t, router, "GET", "/api/ai/analyses?limit=5&offset=1&camera_id=cam-ai&label=person", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"analyses"`)

	// Handler without a database.
	noDB := NewHandler(nil, nil, noopAuthMW(), nil, nil, "", nil, nil)
	rr = doRequest(t, noDB.Routes(), "GET", "/api/ai/detections", nil, "", "")
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
	rr = doRequest(t, noDB.Routes(), "GET", "/api/ai/analyses", nil, "", "")
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)

	// Storage failures.
	for _, tc := range []struct{ fail, path string }{
		{"ListAIDetections", "/api/ai/detections"},
		{"ListAIAnalyses", "/api/ai/analyses"},
	} {
		failing := TestHandler(api2mFailing(db, tc.fail), store)
		rr = doRequest(t, failing.Routes(), "GET", tc.path, nil, "", "")
		require.Equal(t, http.StatusInternalServerError, rr.Code)
	}
}

func TestApi2MiscAIUnconfiguredDependencies(t *testing.T) {
	db, store := setupTestDB(t)

	// No AI manager at all.
	h := TestHandler(db, store)
	router := h.Routes()
	rr := doRequest(t, router, "POST", "/api/ai/webhook", strings.NewReader(`{"camera_id":"cam"}`), "", "")
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
	rr = doRequest(t, router, "GET", "/api/ai/multimodal/status", nil, "", "")
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
	rr = doRequest(t, router, "GET", "/api/ai/multimodal/events", nil, "", "")
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)

	// AI manager disabled: receiver and multimodal manager are nil.
	disabledMgr := ai.NewManager(config.AIConfig{Enabled: false})
	h.SetAIManager(disabledMgr)
	t.Cleanup(disabledMgr.StopAll)
	rr = doRequest(t, router, "POST", "/api/ai/webhook", strings.NewReader(`{"camera_id":"cam"}`), "", "")
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
	require.Contains(t, rr.Body.String(), "webhook not configured")

	rr = doRequest(t, router, "GET", "/api/ai/multimodal/status", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"enabled":false`)

	rr = doRequest(t, router, "GET", "/api/ai/multimodal/events", nil, "", "")
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
	require.Contains(t, rr.Body.String(), "Multimodal analysis not configured")
}

func TestApi2MiscAIMultimodalStreams(t *testing.T) {
	db, store := setupTestDB(t)
	aiCfg := config.AIConfig{
		Enabled: true,
		Backend: "webhook",
		Multimodal: &config.AIMultimodalConfig{
			Enabled:  true,
			Provider: "missing-provider",
		},
	}
	h := TestHandler(db, store)
	mgr := ai.NewManager(aiCfg)
	h.SetAIManager(mgr)
	t.Cleanup(mgr.StopAll)
	router := h.Routes()

	mm := mgr.GetMultimodalManager()
	require.NotNil(t, mm)

	rr := doRequest(t, router, "GET", "/api/ai/multimodal/status", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "active_provider")

	// Streaming not supported (direct call bypasses the flush-capable router wrapper).
	recNoFlush := httptest.NewRecorder()
	h.handleAIMultimodalEvents(&api2mNoFlushWriter{ResponseWriter: recNoFlush}, httptest.NewRequest(http.MethodGet, "/api/ai/multimodal/events", nil))
	require.Equal(t, http.StatusInternalServerError, recNoFlush.Code)

	// Successful SSE delivery.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sseReq := httptest.NewRequest(http.MethodGet, "/api/ai/multimodal/events", nil).WithContext(ctx)
	rec := newSSERecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		router.ServeHTTP(rec, sseReq)
	}()

	api2mPump(t, rec.String, done, "cam-mm", func() {
		mm.PublishResult(multimodal.AnalysisResult{CameraID: "cam-mm", Analysis: "person detected"})
	})
	cancel()
	api2mWaitDone(t, done)
	require.Contains(t, rec.String(), "data: ")
}

func TestApi2MiscAIEventsStream(t *testing.T) {
	db, store := setupTestDB(t)
	h := TestHandler(db, store)
	mgr := ai.NewManager(config.AIConfig{Enabled: true, Backend: "webhook"})
	h.SetAIManager(mgr)
	t.Cleanup(mgr.StopAll)
	router := h.Routes()

	// Webhook validation branches.
	rr := doRequest(t, router, "POST", "/api/ai/webhook", strings.NewReader("{bad"), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, router, "POST", "/api/ai/webhook", strings.NewReader(`{"pts":1}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, router, "POST", "/api/ai/webhook", strings.NewReader(`{"camera_id":"cam-ai","detections":[{"label":"person","confidence":0.9}]}`), "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"ok"`)

	// Streaming not supported (direct call bypasses the flush-capable router wrapper).
	recNoFlush := httptest.NewRecorder()
	h.handleAIEvents(&api2mNoFlushWriter{ResponseWriter: recNoFlush}, httptest.NewRequest(http.MethodGet, "/api/ai/events", nil))
	require.Equal(t, http.StatusInternalServerError, recNoFlush.Code)

	// SSE delivery: push detections until the stream flushes one.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sseReq := httptest.NewRequest(http.MethodGet, "/api/ai/events", nil).WithContext(ctx)
	rec := newSSERecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		router.ServeHTTP(rec, sseReq)
	}()

	payload := `{"camera_id":"cam-sse","detections":[{"label":"person","confidence":0.9}]}`
	api2mPump(t, rec.String, done, "cam-sse", func() {
		doRequest(t, router, "POST", "/api/ai/webhook", strings.NewReader(payload), "", "")
	})
	// Give in-flight dispatch goroutines a moment to drain before the handler
	// closes its event channel during shutdown.
	time.Sleep(30 * time.Millisecond)
	cancel()
	api2mWaitDone(t, done)
	require.Contains(t, rec.String(), "data: ")
	require.Contains(t, rec.String(), "person")
}

// ==================== snapshot handlers ====================

func TestApi2MiscSnapshotServe(t *testing.T) {
	jpegData := []byte{0xFF, 0xD8, 0xFF, 0xE0, 'j', 'p', 'g'}
	var sawBasicAuth atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, pass, ok := r.BasicAuth(); ok && user == "user" && pass == "pass" {
			sawBasicAuth.Store(true)
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(jpegData)
	}))
	defer srv.Close()

	db, store, h, _ := api2mNewCamHandler(t, []config.CameraConfig{{
		ID:          "cam-snap",
		Name:        "Snap",
		Protocol:    "http_jpeg",
		URL:         srv.URL + "/stream",
		SnapshotURL: srv.URL + "/snapshot.jpg",
		Username:    "user",
		Password:    "pass",
		Enabled:     true,
	}})
	router := h.Routes()

	rr := doRequest(t, router, "GET", "/api/snapshots/cam-snap", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, jpegData, rr.Body.Bytes())
	require.Equal(t, "image/jpeg", rr.Header().Get("Content-Type"))
	require.Equal(t, "max-age=5", rr.Header().Get("Cache-Control"))
	require.True(t, sawBasicAuth.Load(), "camera credentials must be forwarded")

	// Second request is served from the in-memory cache.
	rr = doRequest(t, router, "GET", "/api/snapshots/cam-snap", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, jpegData, rr.Body.Bytes())

	rr = doRequest(t, router, "GET", "/api/snapshots/unknown-cam", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
	require.Contains(t, rr.Body.String(), "camera not found")

	// Missing camera_id (no chi route params bound).
	rec := httptest.NewRecorder()
	h.handleGetSnapshot(rec, httptest.NewRequest(http.MethodGet, "/api/snapshots/", nil))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "camera_id is required")

	// Config-only lookup (no CameraManager) still resolves cameras.
	cfg := &config.Config{Cameras: []config.CameraConfig{{
		ID:          "cam-cfg",
		Name:        "Cfg",
		Protocol:    "http_jpeg",
		URL:         srv.URL + "/stream",
		SnapshotURL: srv.URL + "/snapshot.jpg",
		Enabled:     true,
	}}}
	cfgHandler := NewHandler(db, store, noopAuthMW(), cfg, nil, "", nil, nil)
	rr = doRequest(t, cfgHandler.Routes(), "GET", "/api/snapshots/cam-cfg", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, jpegData, rr.Body.Bytes())

	// No config and no camera manager: unknown camera.
	rr = doRequest(t, TestHandler(db, store).Routes(), "GET", "/api/snapshots/nowhere", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)

	// Empty payloads are never cached.
	h.setCachedSnapshot("cam-snap-empty", nil)
	_, ok := h.getCachedSnapshot("cam-snap-empty", 0)
	require.False(t, ok)

	rr = doRequest(t, router, "DELETE", "/api/snapshots/cam-snap", nil, "", "")
	require.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

func TestApi2MiscSnapshotFallbacks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/empty") {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, _, h, _ := api2mNewCamHandler(t, []config.CameraConfig{
		{ID: "cam-bad", Protocol: "rtsp_h264", URL: "rtsp://x", SnapshotURL: "http://127.0.0.1:1/none.jpg", ONVIFEndpoint: "http://127.0.0.1:1/onvif"},
		{ID: "cam-stale", Protocol: "rtsp_h264", URL: "rtsp://x", SnapshotURL: "http://127.0.0.1:1/none.jpg"},
		{ID: "cam-empty", Protocol: "http_jpeg", URL: "http://x", SnapshotURL: srv.URL + "/empty"},
		{ID: "cam-500", Protocol: "http_jpeg", URL: "http://x", SnapshotURL: srv.URL + "/500"},
		{ID: "cam-no-source", Protocol: "rtsp_h264", URL: "rtsp://x"},
	})
	router := h.Routes()

	// Remote fetch, ONVIF fallback and stream capture all fail.
	rr := doRequest(t, router, "GET", "/api/snapshots/cam-bad", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)

	// Empty body and non-200 responses are rejected.
	rr = doRequest(t, router, "GET", "/api/snapshots/cam-empty", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
	rr = doRequest(t, router, "GET", "/api/snapshots/cam-500", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)

	// No source configured at all.
	rr = doRequest(t, router, "GET", "/api/snapshots/cam-no-source", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)

	// Stale cache is served when every upstream fails.
	h.setCachedSnapshot("cam-stale", []byte("cached-jpeg"))
	h.snapshotMu.Lock()
	h.snapshots["cam-stale"].timestamp = time.Now().Add(-time.Minute)
	h.snapshotMu.Unlock()
	rr = doRequest(t, router, "GET", "/api/snapshots/cam-stale", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "stale", rr.Header().Get("X-Cache"))
	require.Equal(t, "cached-jpeg", rr.Body.String())
}

func TestApi2MiscFetchRemoteSnapshot(t *testing.T) {
	jpegData := []byte{0xFF, 0xD8, 0xFF, 0xE0, 'x'}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			_, _ = w.Write(jpegData)
		case "/empty":
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	defer srv.Close()

	if _, err := fetchRemoteSnapshot(context.Background(), "://bad-url", "", ""); err == nil {
		t.Fatal("expected invalid URL to fail")
	}
	data, err := fetchRemoteSnapshot(context.Background(), srv.URL+"/ok", "", "")
	require.NoError(t, err)
	require.Equal(t, jpegData, data)
	if _, err := fetchRemoteSnapshot(context.Background(), srv.URL+"/empty", "", ""); err == nil {
		t.Fatal("expected empty response to fail")
	}
	if _, err := fetchRemoteSnapshot(context.Background(), srv.URL+"/forbidden", "", ""); err == nil {
		t.Fatal("expected non-200 response to fail")
	}
}

func TestApi2MiscSnapshotCapture(t *testing.T) {
	dir := t.TempDir()
	okScript := api2mFFmpegStub(t, dir, "ffmpeg-ok.sh", `printf '\377\330\377\340'; printf 'fake-jpeg'`)
	failScript := api2mFFmpegStub(t, dir, "ffmpeg-fail.sh", "echo boom-stderr >&2; exit 1")
	emptyScript := api2mFFmpegStub(t, dir, "ffmpeg-empty.sh", "exit 0")
	missing := filepath.Join(dir, "ffmpeg-missing")

	// Direct capture helper branches.
	data, err := captureSnapshotWithFFmpeg(context.Background(), okScript, "rtsp://127.0.0.1:1/live")
	require.NoError(t, err)
	require.Contains(t, string(data), "fake-jpeg")

	_, err = captureSnapshotWithFFmpeg(context.Background(), failScript, "rtsp://127.0.0.1:1/live")
	require.ErrorContains(t, err, "boom-stderr")

	_, err = captureSnapshotWithFFmpeg(context.Background(), emptyScript, "http://127.0.0.1:1/live")
	require.ErrorContains(t, err, "empty output")

	quietFailScript := api2mFFmpegStub(t, dir, "ffmpeg-quiet-fail.sh", "exit 2")
	_, err = captureSnapshotWithFFmpeg(context.Background(), quietFailScript, "rtsp://127.0.0.1:1/live")
	require.ErrorContains(t, err, "ffmpeg snapshot failed")

	// Full handler path: each case uses a distinct camera so the per-camera
	// snapshot cache cannot mask the ffmpeg failure under test.
	mkSnapHandler := func(t *testing.T, cameraID, ffmpegPath string, engine *stubMediaEngine) *Handler {
		t.Helper()
		db, store := setupTestDB(t)
		cfg := &config.Config{
			Storage:     config.StorageConfig{RootDir: store.RootDir(), SegmentDuration: "30s"},
			Transcoding: transcode.Config{FFmpegPath: ffmpegPath},
			Cameras:     []config.CameraConfig{{ID: cameraID, Protocol: "rtsp_h264", URL: "rtsp://127.0.0.1:1/live"}},
		}
		camMgr := camera.NewCameraManager(cfg, store, db, "")
		h := NewHandler(db, store, noopAuthMW(), cfg, camMgr, "", nil, nil)
		h.SetMediaEngine(engine)
		return h
	}

	liveEngine := func() *stubMediaEngine {
		return &stubMediaEngine{playURLs: map[string]string{"rtsp": "rtsp://127.0.0.1:1/live"}}
	}

	rr := doRequest(t, mkSnapHandler(t, "cam-cap", okScript, liveEngine()).Routes(), "GET", "/api/snapshots/cam-cap", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "fake-jpeg")

	// ffmpeg binary configured but absent.
	rr = doRequest(t, mkSnapHandler(t, "cam-cap-missing", missing, liveEngine()).Routes(), "GET", "/api/snapshots/cam-cap-missing", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)

	// Media engine cannot build a URL for either protocol.
	rr = doRequest(t, mkSnapHandler(t, "cam-cap-nourl", okScript, &stubMediaEngine{}).Routes(), "GET", "/api/snapshots/cam-cap-nourl", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)

	// ffmpeg errors are reported as "snapshot not found".
	rr = doRequest(t, mkSnapHandler(t, "cam-cap-broken", failScript, liveEngine()).Routes(), "GET", "/api/snapshots/cam-cap-broken", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)

	require.Equal(t, filepath.Join("/data", "snapshots"), GetSnapshotDir("/data"))
}

func TestApi2MiscSnapshotLatestAndTake(t *testing.T) {
	jpegData := []byte{0xFF, 0xD8, 0xFF, 0xE0, 't', 'a', 'k', 'e'}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/500") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(jpegData)
	}))
	defer srv.Close()

	db, store, h, _ := api2mNewCamHandler(t, []config.CameraConfig{
		{ID: "cam-take", Protocol: "http_jpeg", URL: srv.URL + "/stream", SnapshotURL: srv.URL + "/ok"},
		{ID: "cam-take-broken", Protocol: "http_jpeg", URL: srv.URL + "/stream", SnapshotURL: srv.URL + "/500"},
		{ID: "cam-take-onvif", Protocol: "onvif", URL: "rtsp://127.0.0.1:1/live", ONVIFEndpoint: "http://127.0.0.1:1/onvif"},
		{ID: "cam-take-unreachable", Protocol: "http_jpeg", URL: "http://x", SnapshotURL: "http://127.0.0.1:1/none"},
	})
	_ = db
	router := h.Routes()

	// Without an snapshot manager both endpoints report unavailable.
	rr := doRequest(t, router, "GET", "/api/snapshots/cam-take/latest", nil, "", "")
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
	rr = doRequest(t, router, "POST", "/api/snapshots/cam-take/take", nil, "", "")
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)

	rec := httptest.NewRecorder()
	h.handleGetLatestSnapshot(rec, httptest.NewRequest(http.MethodGet, "/api/snapshots//latest", nil))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	rec = httptest.NewRecorder()
	h.handleTakeSnapshot(rec, httptest.NewRequest(http.MethodPost, "/api/snapshots//take", nil))
	require.Equal(t, http.StatusBadRequest, rec.Code)

	mgr, err := camera.NewSnapshotManager(camera.SnapshotConfig{Interval: "5m", Quality: 80, MaxAge: "24h"}, store, nil)
	require.NoError(t, err)
	h.SetSnapshotManager(mgr)

	// No snapshot file yet.
	rr = doRequest(t, router, "GET", "/api/snapshots/cam-take/latest", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)

	snapshotDir := filepath.Join(store.RootDir(), "snapshots")
	require.NoError(t, os.MkdirAll(snapshotDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(snapshotDir, "cam-take_latest.jpg"), jpegData, 0o644))

	rr = doRequest(t, router, "GET", "/api/snapshots/cam-take/latest", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var latest struct {
		CameraID string `json:"camera_id"`
		Path     string `json:"path"`
		Size     int64  `json:"size"`
		URL      string `json:"url"`
	}
	parseJSON(t, rr, &latest)
	require.Equal(t, "cam-take", latest.CameraID)
	require.Equal(t, int64(len(jpegData)), latest.Size)
	require.Contains(t, latest.URL, "cam-take")

	// Snapshot manager file wins over the camera SnapshotURL.
	rr = doRequest(t, router, "GET", "/api/snapshots/cam-take", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, jpegData, rr.Body.Bytes())

	// Take snapshot: success via SnapshotURL.
	rr = doRequest(t, router, "POST", "/api/snapshots/cam-take/take", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, jpegData, rr.Body.Bytes())
	require.Equal(t, "image/jpeg", rr.Header().Get("Content-Type"))

	// Upstream 500 → no data captured.
	rr = doRequest(t, router, "POST", "/api/snapshots/cam-take-broken/take", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
	require.Contains(t, rr.Body.String(), "unable to take snapshot")

	// Unknown camera.
	rr = doRequest(t, router, "POST", "/api/snapshots/nope/take", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
	require.Contains(t, rr.Body.String(), "camera not found")

	// Unreachable upstream yields no snapshot data (the handler's client error
	// stays scoped to its own block, so the fallback is "unable to take").
	rr = doRequest(t, router, "POST", "/api/snapshots/cam-take-unreachable/take", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
	require.Contains(t, rr.Body.String(), "unable to take snapshot")

	// ONVIF cameras delegate to the snapshot-URI handler.
	rr = doRequest(t, router, "POST", "/api/snapshots/cam-take-onvif/take", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)

	rr = doRequest(t, router, "GET", "/api/snapshots/cam-take/take", nil, "", "")
	require.Equal(t, http.StatusMethodNotAllowed, rr.Code)

	// Snapshot manager present but no camera manager.
	noCam := TestHandler(db, store)
	noCam.SetSnapshotManager(mgr)
	rr = doRequest(t, noCam.Routes(), "POST", "/api/snapshots/cam-take/take", nil, "", "")
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
	require.Contains(t, rr.Body.String(), "camera manager not available")
}

// ==================== archive handlers ====================

func TestApi2MiscArchiveHandlers(t *testing.T) {
	db, _, h := api2mNewHandler(t)
	ctx := context.Background()
	router := h.Routes()
	now := time.Now().UTC()

	rr := doRequest(t, router, "GET", "/api/archives", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	api2mSeedArchivedCamera(t, db, "arc-1", "Archived One")
	seedRecording(t, db, makeRecording("arc-rec-1", "arc-1", "h264", now, false))
	_, err := db.ArchiveAllRecordings(ctx, "arc-1")
	require.NoError(t, err)

	rr = doRequest(t, router, "GET", "/api/archives", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var archives struct {
		Archives []archiveGroupItem `json:"archives"`
	}
	parseJSON(t, rr, &archives)
	require.Len(t, archives.Archives, 1)
	require.Equal(t, "arc-1", archives.Archives[0].ID)
	require.Equal(t, 1, archives.Archives[0].RecordingCount)
	require.Equal(t, int64(1024), archives.Archives[0].TotalSize)

	rr = doRequest(t, router, "GET", "/api/archives/arc-1/recordings?limit=10&offset=0&sort_by=started_at&order=asc", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var recs struct {
		Recordings []model.Recording `json:"recordings"`
		Total      int               `json:"total"`
	}
	parseJSON(t, rr, &recs)
	require.Equal(t, 1, recs.Total)
	require.Len(t, recs.Recordings, 1)

	// Invalid pagination values are ignored.
	rr = doRequest(t, router, "GET", "/api/archives/arc-1/recordings?limit=bad&offset=-2", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	rr = doRequest(t, router, "PUT", "/api/archives/arc-1/retention", strings.NewReader("{bad"), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, router, "PUT", "/api/archives/arc-1/retention", strings.NewReader(`{"retention_days":-1}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, router, "PUT", "/api/archives/missing/retention", strings.NewReader(`{"retention_days":5}`), "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
	rr = doRequest(t, router, "PUT", "/api/archives/arc-1/retention", strings.NewReader(`{"retention_days":7}`), "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	row, err := db.GetCamera(ctx, "arc-1")
	require.NoError(t, err)
	require.NotNil(t, row)
	require.Equal(t, 7, row.ArchiveRetentionDays)

	rr = doRequest(t, router, "PATCH", "/api/archives", nil, "", "")
	require.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

func TestApi2MiscArchiveRestore(t *testing.T) {
	db, store := setupTestDB(t)
	ctx := context.Background()
	api2mSeedArchivedCamera(t, db, "arc-restore", "To Restore")
	api2mSeedArchivedCamera(t, db, "arc-conflict", "Conflict")
	require.NoError(t, db.UpsertCamera(ctx, "cam-live", "Live", "rtsp_h264", "h264", "rtsp://example.invalid/live", "", "", false, "", "", ""))

	cfg := &config.Config{
		Storage: config.StorageConfig{RootDir: store.RootDir(), SegmentDuration: "30s"},
		Cameras: []config.CameraConfig{{ID: "arc-conflict", Name: "Conflict", Protocol: "rtsp_h264", URL: "rtsp://example.invalid/arc-conflict"}},
	}
	camMgr := camera.NewCameraManager(cfg, store, db, "")
	h := NewHandler(db, store, noopAuthMW(), cfg, camMgr, "", nil, nil)
	router := h.Routes()

	rr := doRequest(t, router, "POST", "/api/archives/missing/restore", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)

	// Existing but not archived.
	rr = doRequest(t, router, "POST", "/api/archives/cam-live/restore", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
	require.Contains(t, rr.Body.String(), "archived camera not found")

	// Camera already present in the live config.
	rr = doRequest(t, router, "POST", "/api/archives/arc-conflict/restore", nil, "", "")
	require.Equal(t, http.StatusConflict, rr.Code)

	rr = doRequest(t, router, "POST", "/api/archives/arc-restore/restore", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "restored")

	row, err := db.GetCamera(ctx, "arc-restore")
	require.NoError(t, err)
	require.NotNil(t, row)
	require.False(t, row.Archived)
	require.NotNil(t, camMgr.GetCameraConfig("arc-restore"))

	// Camera manager missing.
	noCam := TestHandler(db, store)
	rr = doRequest(t, noCam.Routes(), "POST", "/api/archives/arc-conflict/restore", nil, "", "")
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
	require.Contains(t, rr.Body.String(), "camera manager not available")
}

func TestApi2MiscArchiveDeleteGroup(t *testing.T) {
	db, store, h := api2mNewHandler(t)
	ctx := context.Background()
	api2mSeedArchivedCamera(t, db, "arc-delete", "To Delete")
	require.NoError(t, db.UpsertCamera(ctx, "cam-live", "Live", "rtsp_h264", "h264", "rtsp://x", "", "", false, "", "", ""))
	seedRecording(t, db, makeRecording("arc-del-rec", "arc-delete", "h264", time.Now().UTC(), false))
	_, err := db.ArchiveAllRecordings(ctx, "arc-delete")
	require.NoError(t, err)
	router := h.Routes()

	rr := doRequest(t, router, "DELETE", "/api/archives/cam-live", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)

	rr = doRequest(t, router, "DELETE", "/api/archives/arc-delete", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "deleted")

	row, err := db.GetCamera(ctx, "arc-delete")
	require.NoError(t, err)
	require.Nil(t, row)
	require.DirExists(t, store.RootDir())
}

func TestApi2MiscArchiveDeleteRecording(t *testing.T) {
	db, _, h := api2mNewHandler(t)
	ctx := context.Background()
	api2mSeedArchivedCamera(t, db, "arc-rec-cam", "Archived Rec Cam")
	now := time.Now().UTC()
	recA := makeRecording("arc-rec-a", "arc-rec-cam", "h264", now, false)
	recA.FilePath = filepath.Join(t.TempDir(), "missing-a.mp4")
	recB := makeRecording("arc-rec-b", "arc-rec-cam", "h264", now.Add(time.Minute), false)
	recB.FilePath = ""
	seedRecording(t, db, recA)
	seedRecording(t, db, recB)
	_, err := db.ArchiveAllRecordings(ctx, "arc-rec-cam")
	require.NoError(t, err)
	router := h.Routes()

	// Camera mismatch.
	rr := doRequest(t, router, "DELETE", "/api/archives/other-cam/recordings/arc-rec-a", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)

	// Unknown recording.
	rr = doRequest(t, router, "DELETE", "/api/archives/arc-rec-cam/recordings/nope", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)

	// Non-archived recording.
	seedRecording(t, db, &model.Recording{ID: "live-rec", CameraID: "arc-rec-cam", Format: "h264", StartedAt: now, EndedAt: now.Add(time.Minute)})
	rr = doRequest(t, router, "DELETE", "/api/archives/arc-rec-cam/recordings/live-rec", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)

	rr = doRequest(t, router, "DELETE", "/api/archives/arc-rec-cam/recordings/arc-rec-a", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	rr = doRequest(t, router, "DELETE", "/api/archives/arc-rec-cam/recordings/arc-rec-b", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	// The last archived recording removes the camera row.
	row, err := db.GetCamera(ctx, "arc-rec-cam")
	require.NoError(t, err)
	require.Nil(t, row)
}

func TestApi2MiscArchiveRepoErrors(t *testing.T) {
	ctx := context.Background()

	t.Run("list archives", func(t *testing.T) {
		db, store := setupTestDB(t)
		h := TestHandler(api2mFailing(db, "ListArchivedCameras"), store)
		rr := doRequest(t, h.Routes(), "GET", "/api/archives", nil, "", "")
		require.Equal(t, http.StatusInternalServerError, rr.Code)
	})

	t.Run("archive stats fallback", func(t *testing.T) {
		db, store := setupTestDB(t)
		api2mSeedArchivedCamera(t, db, "arc-err", "Err")
		h := TestHandler(api2mFailing(db, "GetArchiveGroupStats"), store)
		rr := doRequest(t, h.Routes(), "GET", "/api/archives", nil, "", "")
		require.Equal(t, http.StatusOK, rr.Code)
		var archives struct {
			Archives []archiveGroupItem `json:"archives"`
		}
		parseJSON(t, rr, &archives)
		require.Len(t, archives.Archives, 1)
		require.Zero(t, archives.Archives[0].RecordingCount)
	})

	t.Run("list recordings", func(t *testing.T) {
		db, store := setupTestDB(t)
		h := TestHandler(api2mFailing(db, "ListRecordings"), store)
		rr := doRequest(t, h.Routes(), "GET", "/api/archives/arc-1/recordings", nil, "", "")
		require.Equal(t, http.StatusInternalServerError, rr.Code)
	})

	t.Run("count recordings fallback", func(t *testing.T) {
		db, store := setupTestDB(t)
		h := TestHandler(api2mFailing(db, "CountRecordingsWithFilter"), store)
		rr := doRequest(t, h.Routes(), "GET", "/api/archives/arc-1/recordings", nil, "", "")
		require.Equal(t, http.StatusOK, rr.Code)
		var recs struct {
			Total int `json:"total"`
		}
		parseJSON(t, rr, &recs)
		require.Zero(t, recs.Total)
	})

	t.Run("restore lookup", func(t *testing.T) {
		db, store := setupTestDB(t)
		cfg := &config.Config{Storage: config.StorageConfig{RootDir: store.RootDir(), SegmentDuration: "30s"}}
		camMgr := camera.NewCameraManager(cfg, store, db, "")
		h := NewHandler(api2mFailing(db, "GetCamera"), store, noopAuthMW(), cfg, camMgr, "", nil, nil)
		rr := doRequest(t, h.Routes(), "POST", "/api/archives/arc-1/restore", nil, "", "")
		require.Equal(t, http.StatusInternalServerError, rr.Code)
	})

	t.Run("delete group lookup", func(t *testing.T) {
		db, store := setupTestDB(t)
		h := TestHandler(api2mFailing(db, "GetCamera"), store)
		rr := doRequest(t, h.Routes(), "DELETE", "/api/archives/arc-1", nil, "", "")
		require.Equal(t, http.StatusInternalServerError, rr.Code)
	})

	t.Run("delete group recordings", func(t *testing.T) {
		db, store := setupTestDB(t)
		api2mSeedArchivedCamera(t, db, "arc-del-err", "Del Err")
		h := TestHandler(api2mFailing(db, "DeleteRecordingsByCamera"), store)
		rr := doRequest(t, h.Routes(), "DELETE", "/api/archives/arc-del-err", nil, "", "")
		require.Equal(t, http.StatusInternalServerError, rr.Code)
	})

	t.Run("delete recording lookup", func(t *testing.T) {
		db, store := setupTestDB(t)
		h := TestHandler(api2mFailing(db, "GetRecording"), store)
		rr := doRequest(t, h.Routes(), "DELETE", "/api/archives/arc-1/recordings/rec-1", nil, "", "")
		require.Equal(t, http.StatusInternalServerError, rr.Code)
	})

	t.Run("delete recording camera cleanup", func(t *testing.T) {
		db, store := setupTestDB(t)
		api2mSeedArchivedCamera(t, db, "arc-cleanup", "Cleanup")
		rec := makeRecording("arc-cleanup-1", "arc-cleanup", "h264", time.Now().UTC(), false)
		rec.FilePath = ""
		seedRecording(t, db, rec)
		_, err := db.ArchiveAllRecordings(ctx, "arc-cleanup")
		require.NoError(t, err)
		// The camera-row cleanup is best-effort: a failure is logged, not fatal.
		h := TestHandler(api2mFailing(db, "DeleteCamera"), store)
		rr := doRequest(t, h.Routes(), "DELETE", "/api/archives/arc-cleanup/recordings/arc-cleanup-1", nil, "", "")
		require.Equal(t, http.StatusOK, rr.Code)
		row, err := db.GetCamera(ctx, "arc-cleanup")
		require.NoError(t, err)
		require.NotNil(t, row)
	})

	t.Run("delete recording row", func(t *testing.T) {
		db, store := setupTestDB(t)
		api2mSeedArchivedCamera(t, db, "arc-rec-err", "Rec Err")
		rec := makeRecording("arc-rec-err-1", "arc-rec-err", "h264", time.Now().UTC(), false)
		rec.FilePath = ""
		seedRecording(t, db, rec)
		_, err := db.ArchiveAllRecordings(ctx, "arc-rec-err")
		require.NoError(t, err)
		h := TestHandler(api2mFailing(db, "DeleteRecording"), store)
		rr := doRequest(t, h.Routes(), "DELETE", "/api/archives/arc-rec-err/recordings/arc-rec-err-1", nil, "", "")
		require.Equal(t, http.StatusInternalServerError, rr.Code)
	})
}
