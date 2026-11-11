package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lalmax-pro/lalmax-nvr/internal/jt808"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/middleware"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/muxer"
	"github.com/lalmax-pro/lalmax-nvr/internal/relay"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// um74 helpers (issue #74: users / VOD / relay / service logs / JT808)
// ---------------------------------------------------------------------------

func um74NewHandler(t *testing.T) (*Handler, *storage.DB) {
	t.Helper()
	db, store := setupTestDB(t)
	return TestHandler(db, store), db
}

// um74Repo embeds a real repository and lets tests inject failures for the
// repository methods exercised by the handlers under test.
type um74Repo struct {
	storage.APIRepository

	listUsersErr  error
	getUserErr    error
	getByNameErr  error
	existingUser  *model.User
	createUserErr error
	updateUserErr error
	deleteUserErr error
	hasAdminErr   error

	listRecordingsErr error
	getRecordingErr   error
	timelineErr       error
}

func (r *um74Repo) ListUsers(ctx context.Context) ([]*model.User, error) {
	if r.listUsersErr != nil {
		return nil, r.listUsersErr
	}
	return r.APIRepository.ListUsers(ctx)
}

func (r *um74Repo) GetUserByID(ctx context.Context, id int64) (*model.User, error) {
	if r.getUserErr != nil {
		return nil, r.getUserErr
	}
	return r.APIRepository.GetUserByID(ctx, id)
}

func (r *um74Repo) GetUserByUsername(ctx context.Context, username string) (*model.User, error) {
	if r.getByNameErr != nil {
		return nil, r.getByNameErr
	}
	if r.existingUser != nil {
		return r.existingUser, nil
	}
	return r.APIRepository.GetUserByUsername(ctx, username)
}

func (r *um74Repo) CreateUser(ctx context.Context, u *model.User) error {
	if r.createUserErr != nil {
		return r.createUserErr
	}
	return r.APIRepository.CreateUser(ctx, u)
}

func (r *um74Repo) UpdateUser(ctx context.Context, u *model.User) error {
	if r.updateUserErr != nil {
		return r.updateUserErr
	}
	return r.APIRepository.UpdateUser(ctx, u)
}

func (r *um74Repo) DeleteUser(ctx context.Context, id int64) error {
	if r.deleteUserErr != nil {
		return r.deleteUserErr
	}
	return r.APIRepository.DeleteUser(ctx, id)
}

func (r *um74Repo) HasSuperAdmin(ctx context.Context) (bool, error) {
	if r.hasAdminErr != nil {
		return false, r.hasAdminErr
	}
	return r.APIRepository.HasSuperAdmin(ctx)
}

func (r *um74Repo) ListRecordings(ctx context.Context, filter model.RecordingFilter) ([]model.Recording, error) {
	if r.listRecordingsErr != nil {
		return nil, r.listRecordingsErr
	}
	return r.APIRepository.ListRecordings(ctx, filter)
}

func (r *um74Repo) GetRecording(ctx context.Context, id string) (*model.Recording, error) {
	if r.getRecordingErr != nil {
		return nil, r.getRecordingErr
	}
	return r.APIRepository.GetRecording(ctx, id)
}

func (r *um74Repo) ListRecordingTimeline(ctx context.Context, cameraID string, start, end time.Time, limit int) ([]storage.TimelineEntry, error) {
	if r.timelineErr != nil {
		return nil, r.timelineErr
	}
	return r.APIRepository.ListRecordingTimeline(ctx, cameraID, start, end, limit)
}

// um74RelayRepo lets tests inject failure into the relay task repository.
type um74RelayRepo struct {
	storage.RelayTaskRepository
	listErr   error
	deleteErr error
}

func (r *um74RelayRepo) ListRelayTasks(ctx context.Context) ([]storage.RelayTaskRecord, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	return r.RelayTaskRepository.ListRelayTasks(ctx)
}

func (r *um74RelayRepo) DeleteRelayTask(ctx context.Context, taskID string) error {
	if r.deleteErr != nil {
		return r.deleteErr
	}
	return r.RelayTaskRepository.DeleteRelayTask(ctx, taskID)
}

func um74SeedUser(t *testing.T, db storage.APIRepository, username string, role model.Role, enabled bool) *model.User {
	t.Helper()
	now := time.Now().UTC()
	u := &model.User{
		Username:     username,
		PasswordHash: "not-a-real-hash",
		Role:         role,
		DisplayName:  username,
		Enabled:      enabled,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	require.NoError(t, db.CreateUser(context.Background(), u))
	require.NotZero(t, u.ID)
	return u
}

func um74SeedRecording(t *testing.T, db storage.APIRepository, id, cameraID, path string, format model.Format, start time.Time, dur time.Duration) {
	t.Helper()
	require.NoError(t, db.InsertRecording(context.Background(), &model.Recording{
		ID:          id,
		CameraID:    cameraID,
		FilePath:    path,
		Format:      format,
		StartedAt:   start,
		EndedAt:     start.Add(dur),
		Duration:    dur.Seconds(),
		MergeStatus: "none",
	}))
}

var (
	um74SPS = []byte{0x67, 0x42, 0x00, 0x0a, 0xe2, 0x40, 0x40, 0x04, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0xc8, 0x40}
	um74PPS = []byte{0x68, 0xce, 0x38, 0x80}
)

// um74WriteSegment writes a real, parseable H.264 MP4 segment with n one-second samples.
func um74WriteSegment(t *testing.T, path string, samples int) {
	t.Helper()
	m := muxer.NewMP4Muxer(path)
	trackID, err := m.AddH264Track(um74SPS, um74PPS)
	require.NoError(t, err)
	for i := 0; i < samples; i++ {
		nalu := []byte{0x41, 0x10, 0x00, 0x0c}
		require.NoError(t, m.WriteSample(trackID, nalu, time.Duration(i)*time.Second, time.Second))
	}
	require.NoError(t, m.Close())
}

func um74VODQuery(start, end time.Time) string {
	return "?start=" + url.QueryEscape(start.Format(time.RFC3339)) +
		"&end=" + url.QueryEscape(end.Format(time.RFC3339))
}

// um74FlushWriter records output and signals every Flush call so streaming
// handlers can be driven deterministically without polling the body.
type um74FlushWriter struct {
	*httptest.ResponseRecorder
	flushed chan struct{}
}

func um74NewFlushWriter() *um74FlushWriter {
	return &um74FlushWriter{
		ResponseRecorder: httptest.NewRecorder(),
		flushed:          make(chan struct{}, 32),
	}
}

func (w *um74FlushWriter) Flush() {
	w.ResponseRecorder.Flush()
	select {
	case w.flushed <- struct{}{}:
	default:
	}
}

func (w *um74FlushWriter) waitFlush(t *testing.T, what string) {
	t.Helper()
	select {
	case <-w.flushed:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s flush", what)
	}
}

// um74PlainWriter is an http.ResponseWriter without http.Flusher.
type um74PlainWriter struct {
	header http.Header
	status int
	buf    bytes.Buffer
}

func (w *um74PlainWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *um74PlainWriter) Write(p []byte) (int, error) { return w.buf.Write(p) }

func (w *um74PlainWriter) WriteHeader(status int) { w.status = status }

// um74ErrWriter rejects every write, emulating a broken client connection.
type um74ErrWriter struct {
	header http.Header
	wrote  bool
}

func (w *um74ErrWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *um74ErrWriter) Write([]byte) (int, error) {
	w.wrote = true
	return 0, errors.New("broken pipe")
}

func (w *um74ErrWriter) WriteHeader(int) {}

// um74JT808 is a recording stub for the jt808Signaling interface.
type um74JT808 struct {
	err error

	playInput      jt808.PlayInput
	stopInput      jt808.StopPlayInput
	controlInput   jt808.LiveControlInput
	lossInput      jt808.LossInput
	playbackInput  jt808.PlaybackInput
	pbControlInput jt808.PlaybackControlInput
	resourceQuery  jt808.ResourceQuery
	uploadInput    jt808.UploadInput
	uploadCtlInput jt808.UploadControlInput
	ptzInput       jt808.PTZInput
	resources      []jt808.Resource
}

func (s *um74JT808) ListTerminals() []jt808.Terminal {
	return []jt808.Terminal{{Key: "1003", Online: true}}
}

func (s *um74JT808) Play(in jt808.PlayInput) (string, error) {
	s.playInput = in
	if s.err != nil {
		return "", s.err
	}
	return "stream-1", nil
}

func (s *um74JT808) StopPlay(in jt808.StopPlayInput) error {
	s.stopInput = in
	return s.err
}

func (s *um74JT808) LiveControl(in jt808.LiveControlInput) error {
	s.controlInput = in
	return s.err
}

func (s *um74JT808) ReportLoss(in jt808.LossInput) error {
	s.lossInput = in
	return s.err
}

func (s *um74JT808) QueryAV(string) (jt808.AVProperties, error) {
	if s.err != nil {
		return jt808.AVProperties{}, s.err
	}
	return jt808.AVProperties{VideoCodec: 98}, nil
}

func (s *um74JT808) Playback(in jt808.PlaybackInput) (string, error) {
	s.playbackInput = in
	if s.err != nil {
		return "", s.err
	}
	return "pb-1", nil
}

func (s *um74JT808) PlaybackControl(in jt808.PlaybackControlInput) error {
	s.pbControlInput = in
	return s.err
}

func (s *um74JT808) QueryResources(in jt808.ResourceQuery) ([]jt808.Resource, error) {
	s.resourceQuery = in
	if s.err != nil {
		return nil, s.err
	}
	return s.resources, nil
}

func (s *um74JT808) Upload(in jt808.UploadInput) (uint16, error) {
	s.uploadInput = in
	if s.err != nil {
		return 0, s.err
	}
	return 7, nil
}

func (s *um74JT808) UploadControl(in jt808.UploadControlInput) error {
	s.uploadCtlInput = in
	return s.err
}

func (s *um74JT808) PTZ(in jt808.PTZInput) error {
	s.ptzInput = in
	return s.err
}

// um74AudioEngine adds the JT1078 audio pusher capability to the shared stub.
type um74AudioEngine struct {
	*stubMediaEngine
	mu      sync.Mutex
	calls   int
	gotSIM  string
	gotCh   byte
	gotPT   byte
	gotData []byte
	pushErr error
}

func (e *um74AudioEngine) PushJT1078Audio(sim string, channel byte, pt byte, payload []byte) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	e.gotSIM = sim
	e.gotCh = channel
	e.gotPT = pt
	e.gotData = append([]byte(nil), payload...)
	return e.pushErr
}

func um74NewAudioEngine() *um74AudioEngine {
	return &um74AudioEngine{stubMediaEngine: &stubMediaEngine{}}
}

// ---------------------------------------------------------------------------
// Users
// ---------------------------------------------------------------------------

func TestIssue74UserHandlersCRUD(t *testing.T) {
	h, _ := um74NewHandler(t)
	routes := h.Routes()

	// Empty list.
	rr := doRequest(t, routes, "GET", "/api/users/", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var listed []userResponse
	parseJSON(t, rr, &listed)
	require.Empty(t, listed)

	// Invalid / missing IDs.
	rr = doRequest(t, routes, "GET", "/api/users/not-a-number", nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, routes, "GET", "/api/users/424242", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)

	// Create validation failures.
	rr = doRequest(t, routes, "POST", "/api/users/", strings.NewReader("{"), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)

	rr = doRequest(t, routes, "POST", "/api/users/", strings.NewReader(`{"username":"   ","password":"password123"}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)

	rr = doRequest(t, routes, "POST", "/api/users/", strings.NewReader(`{"username":"shorty","password":"short"}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)

	rr = doRequest(t, routes, "POST", "/api/users/", strings.NewReader(`{"username":"badrole","password":"password123","role":"root"}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)

	// bcrypt rejects passwords longer than 72 bytes.
	longPass := strings.Repeat("a", 73)
	rr = doRequest(t, routes, "POST", "/api/users/", strings.NewReader(`{"username":"longpass","password":"`+longPass+`"}`), "", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code)

	// Create success with an explicit role.
	rr = doRequest(t, routes, "POST", "/api/users/", strings.NewReader(`{"username":"alice","password":"password123","role":"super_admin","display_name":"Alice"}`), "", "")
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
	var created userResponse
	parseJSON(t, rr, &created)
	require.NotZero(t, created.ID)
	require.Equal(t, "alice", created.Username)
	require.Equal(t, string(model.RoleSuperAdmin), created.Role)
	require.Equal(t, "Alice", created.DisplayName)
	require.True(t, created.Enabled)
	require.NotEmpty(t, created.CreatedAt)

	// Duplicate username conflicts.
	rr = doRequest(t, routes, "POST", "/api/users/", strings.NewReader(`{"username":"alice","password":"password123"}`), "", "")
	require.Equal(t, http.StatusConflict, rr.Code)

	// Defaults: role=user and display_name falls back to username.
	rr = doRequest(t, routes, "POST", "/api/users/", strings.NewReader(`{"username":"bob","password":"password123"}`), "", "")
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
	var bob userResponse
	parseJSON(t, rr, &bob)
	require.Equal(t, string(model.RoleUser), bob.Role)
	require.Equal(t, "bob", bob.DisplayName)

	// Single user lookup.
	rr = doRequest(t, routes, "GET", "/api/users/"+itoa64(created.ID), nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var got userResponse
	parseJSON(t, rr, &got)
	require.Equal(t, created.Username, got.Username)

	// List now returns both users.
	rr = doRequest(t, routes, "GET", "/api/users/", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	parseJSON(t, rr, &listed)
	require.Len(t, listed, 2)
}

func TestIssue74UserHandlersUpdate(t *testing.T) {
	h, db := um74NewHandler(t)
	routes := h.Routes()
	u := um74SeedUser(t, db, "carol", model.RoleUser, true)

	rr := doRequest(t, routes, "PUT", "/api/users/bad", strings.NewReader(`{}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)

	rr = doRequest(t, routes, "PUT", "/api/users/424242", strings.NewReader(`{}`), "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)

	rr = doRequest(t, routes, "PUT", "/api/users/"+itoa64(u.ID), strings.NewReader(`{`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)

	rr = doRequest(t, routes, "PUT", "/api/users/"+itoa64(u.ID), strings.NewReader(`{"password":"short"}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)

	rr = doRequest(t, routes, "PUT", "/api/users/"+itoa64(u.ID), strings.NewReader(`{"password":"`+strings.Repeat("b", 73)+`"}`), "", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code)

	rr = doRequest(t, routes, "PUT", "/api/users/"+itoa64(u.ID), strings.NewReader(`{"role":"owner"}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)

	rr = doRequest(t, routes, "PUT", "/api/users/"+itoa64(u.ID),
		strings.NewReader(`{"password":"newpassword1","role":"super_admin","display_name":"  Carol C  ","enabled":false}`), "", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var updated userResponse
	parseJSON(t, rr, &updated)
	require.Equal(t, "Carol C", updated.DisplayName)
	require.Equal(t, string(model.RoleSuperAdmin), updated.Role)
	require.False(t, updated.Enabled)

	stored, err := db.GetUserByID(context.Background(), u.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, "Carol C", stored.DisplayName)
	require.False(t, stored.Enabled)
	require.NotEqual(t, "not-a-real-hash", stored.PasswordHash)
}

func TestIssue74UserHandlersDeleteGuards(t *testing.T) {
	h, db := um74NewHandler(t)
	h.SetMultiUserAuthMW(middleware.InjectUser(&model.User{ID: 99, Username: "root", Role: model.RoleSuperAdmin, Enabled: true}))
	routes := h.Routes()

	admin := um74SeedUser(t, db, "rootadmin", model.RoleSuperAdmin, true)

	rr := doRequest(t, routes, "DELETE", "/api/users/bad", nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)

	rr = doRequest(t, routes, "DELETE", "/api/users/424242", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)

	// Only super admin in the database: refusing to delete it.
	rr = doRequest(t, routes, "DELETE", "/api/users/"+itoa64(admin.ID), nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())

	// Adding any other user allows deleting the admin.
	member := um74SeedUser(t, db, "member", model.RoleUser, true)
	rr = doRequest(t, routes, "DELETE", "/api/users/"+itoa64(admin.ID), nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	rr = doRequest(t, routes, "DELETE", "/api/users/"+itoa64(member.ID), nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	// Self-deletion is rejected.
	h2, db2 := um74NewHandler(t)
	self := um74SeedUser(t, db2, "self", model.RoleSuperAdmin, true)
	h2.SetMultiUserAuthMW(middleware.InjectUser(&model.User{ID: self.ID, Username: self.Username, Role: model.RoleSuperAdmin, Enabled: true}))
	rr = doRequest(t, h2.Routes(), "DELETE", "/api/users/"+itoa64(self.ID), nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
}

func TestIssue74UserHandlersStorageErrors(t *testing.T) {
	db, store := setupTestDB(t)
	repo := &um74Repo{APIRepository: db}
	h := TestHandler(repo, store)
	// Requester ID 99 avoids the self-deletion guard on /api/users/1.
	h.SetMultiUserAuthMW(middleware.InjectUser(&model.User{ID: 99, Username: "root", Role: model.RoleSuperAdmin, Enabled: true}))
	routes := h.Routes()

	repo.listUsersErr = errors.New("boom")
	rr := doRequest(t, routes, "GET", "/api/users/", nil, "", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code)
	repo.listUsersErr = nil

	repo.getUserErr = errors.New("boom")
	rr = doRequest(t, routes, "GET", "/api/users/1", nil, "", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code)
	rr = doRequest(t, routes, "PUT", "/api/users/1", strings.NewReader(`{}`), "", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code)
	rr = doRequest(t, routes, "DELETE", "/api/users/1", nil, "", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code)
	repo.getUserErr = nil

	repo.getByNameErr = errors.New("boom")
	rr = doRequest(t, routes, "POST", "/api/users/", strings.NewReader(`{"username":"dave","password":"password123"}`), "", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code)
	repo.getByNameErr = nil

	repo.existingUser = &model.User{ID: 7, Username: "taken"}
	rr = doRequest(t, routes, "POST", "/api/users/", strings.NewReader(`{"username":"taken","password":"password123"}`), "", "")
	require.Equal(t, http.StatusConflict, rr.Code)
	repo.existingUser = nil

	repo.createUserErr = errors.New("boom")
	rr = doRequest(t, routes, "POST", "/api/users/", strings.NewReader(`{"username":"dave","password":"password123"}`), "", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code)
	repo.createUserErr = nil

	member := um74SeedUser(t, db, "member2", model.RoleUser, true)
	admin := um74SeedUser(t, db, "admin2", model.RoleSuperAdmin, true)
	h.SetMultiUserAuthMW(middleware.InjectUser(&model.User{ID: 99, Username: "root", Role: model.RoleSuperAdmin, Enabled: true}))

	repo.updateUserErr = errors.New("boom")
	rr = doRequest(t, routes, "PUT", "/api/users/"+itoa64(member.ID), strings.NewReader(`{"display_name":"x"}`), "", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code)
	repo.updateUserErr = nil

	repo.deleteUserErr = errors.New("boom")
	rr = doRequest(t, routes, "DELETE", "/api/users/"+itoa64(member.ID), nil, "", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code)
	repo.deleteUserErr = nil

	repo.hasAdminErr = errors.New("boom")
	rr = doRequest(t, routes, "DELETE", "/api/users/"+itoa64(admin.ID), nil, "", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code)
	repo.hasAdminErr = nil
}

func TestIssue74UserHandlersAuthAndMethod(t *testing.T) {
	db, store := setupTestDB(t)
	hash, err := middleware.HashPassword("secret123")
	require.NoError(t, err)
	authed := TestHandlerWithAuth(db, store, "admin", hash)

	rr := doRequest(t, authed.Routes(), "GET", "/api/users/", nil, "", "")
	require.Equal(t, http.StatusUnauthorized, rr.Code)

	rr = doRequest(t, authed.Routes(), "GET", "/api/users/", nil, "admin", "secret123")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	// Non-admin roles are forbidden on operate endpoints.
	h, _ := um74NewHandler(t)
	h.SetMultiUserAuthMW(middleware.InjectUser(&model.User{ID: 7, Username: "viewer", Role: model.RoleUser, Enabled: true}))
	for _, ep := range []struct{ method, path string }{
		{"GET", "/api/users/"},
		{"POST", "/api/relay/tasks"},
		{"POST", "/api/jt808/play"},
		{"GET", "/api/service-logs"},
	} {
		rr := doRequest(t, h.Routes(), ep.method, ep.path, strings.NewReader(`{}`), "", "")
		require.Equal(t, http.StatusForbidden, rr.Code, "%s %s", ep.method, ep.path)
	}

	// Method not allowed.
	admin, _ := um74NewHandler(t)
	rr = doRequest(t, admin.Routes(), "PUT", "/api/service-logs", nil, "", "")
	require.Equal(t, http.StatusMethodNotAllowed, rr.Code)
	rr = doRequest(t, admin.Routes(), "GET", "/api/jt808/play", nil, "", "")
	require.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

func itoa64(v int64) string {
	return strconv.FormatInt(v, 10)
}

// ---------------------------------------------------------------------------
// VOD
// ---------------------------------------------------------------------------

func TestIssue74VODPlaylistAndExport(t *testing.T) {
	h, db := um74NewHandler(t)
	dir := t.TempDir()
	seg := filepath.Join(dir, "seg-vod.mp4")
	um74WriteSegment(t, seg, 8)

	start := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	um74SeedRecording(t, db, "rec-vod-1", "cam-vod", seg, model.FormatH264, start, 8*time.Second)
	window := um74VODQuery(start, start.Add(8*time.Second))

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam-vod/playback/playlist.m3u8"+window, nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Equal(t, "application/vnd.apple.mpegurl", rr.Header().Get("Content-Type"))
	require.Equal(t, "no-cache", rr.Header().Get("Cache-Control"))
	body := rr.Body.String()
	require.Contains(t, body, "#EXTM3U")
	require.Contains(t, body, "#EXT-X-ENDLIST")
	require.Contains(t, body, "/api/cameras/cam-vod/playback/rec-vod-1/init.mp4")
	require.Contains(t, body, "/api/cameras/cam-vod/playback/rec-vod-1/f0-")
	require.Contains(t, body, "#EXTINF:")

	// Bad time window.
	rr = doRequest(t, h.Routes(), "GET", "/api/cameras/cam-vod/playback/playlist.m3u8?start=nope&end=nope", nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, h.Routes(), "GET", "/api/cameras/cam-vod/playback/playlist.m3u8", nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)

	// Recordings that are not VOD or whose segment is missing are skipped.
	um74SeedRecording(t, db, "rec-vod-mjpeg", "cam-vod", seg, model.FormatMJPEG, start, 8*time.Second)
	um74SeedRecording(t, db, "rec-vod-missing", "cam-vod", filepath.Join(dir, "missing.mp4"), model.FormatH264, start, 8*time.Second)
	rr = doRequest(t, h.Routes(), "GET", "/api/cameras/cam-vod/playback/playlist.m3u8"+window, nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	body = rr.Body.String()
	require.NotContains(t, body, "rec-vod-mjpeg")
	require.NotContains(t, body, "rec-vod-missing")
	require.Contains(t, body, "rec-vod-1")

	// Export success.
	rr = doRequest(t, h.Routes(), "GET", "/api/cameras/cam-vod/playback/export.m3u8"+window, nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Contains(t, rr.Header().Get("Content-Disposition"), `filename="cam-vod-export.m3u8"`)
	require.Contains(t, rr.Body.String(), "#EXTM3U")

	// Partial window: the clip starts inside the recording.
	partialStart := start.Add(2 * time.Second)
	partial := um74VODQuery(partialStart, start.Add(6*time.Second))
	rr = doRequest(t, h.Routes(), "GET", "/api/cameras/cam-vod/playback/export.m3u8"+partial, nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), partialStart.UTC().Format(time.RFC3339Nano))

	// A window that begins exactly at the recording end has no samples to clip.
	edge := um74VODQuery(start.Add(8*time.Second), start.Add(9*time.Second))
	rr = doRequest(t, h.Routes(), "GET", "/api/cameras/cam-vod/playback/export.m3u8"+edge, nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)

	// Export window larger than 24h.
	huge := um74VODQuery(start, start.Add(25*time.Hour))
	rr = doRequest(t, h.Routes(), "GET", "/api/cameras/cam-vod/playback/export.m3u8"+huge, nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)

	// Export window past the recording.
	late := um74VODQuery(start.Add(30*time.Second), start.Add(40*time.Second))
	rr = doRequest(t, h.Routes(), "GET", "/api/cameras/cam-vod/playback/export.m3u8"+late, nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)

	// Export with no recordings at all.
	far := start.Add(48 * time.Hour)
	rr = doRequest(t, h.Routes(), "GET", "/api/cameras/cam-vod/playback/export.m3u8"+um74VODQuery(far, far.Add(time.Hour)), nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)

	// Export time validation.
	rr = doRequest(t, h.Routes(), "GET", "/api/cameras/cam-vod/playback/export.m3u8?start=bad&end=bad", nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestIssue74VODSegmentServing(t *testing.T) {
	h, db := um74NewHandler(t)
	dir := t.TempDir()
	seg := filepath.Join(dir, "seg-serve.mp4")
	um74WriteSegment(t, seg, 8)
	start := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	um74SeedRecording(t, db, "rec-serve", "cam-serve", seg, model.FormatH264, start, 8*time.Second)
	um74SeedRecording(t, db, "rec-mjpeg", "cam-serve", seg, model.FormatMJPEG, start, 8*time.Second)
	um74SeedRecording(t, db, "rec-gone", "cam-serve", filepath.Join(dir, "gone.mp4"), model.FormatH264, start, 8*time.Second)

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam-serve/playback/rec-serve/init.mp4", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Equal(t, "video/mp4", rr.Header().Get("Content-Type"))
	require.NotZero(t, rr.Body.Len())

	rr = doRequest(t, h.Routes(), "GET", "/api/cameras/cam-serve/playback/rec-serve/f0-7.m4s", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Equal(t, "video/iso.segment", rr.Header().Get("Content-Type"))
	require.NotZero(t, rr.Body.Len())

	// Out-of-range fragment: the handler stays 200 but writes no payload.
	rr = doRequest(t, h.Routes(), "GET", "/api/cameras/cam-serve/playback/rec-serve/f0-99.m4s", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Zero(t, rr.Body.Len())

	// Non-matching fragment names.
	rr = doRequest(t, h.Routes(), "GET", "/api/cameras/cam-serve/playback/rec-serve/nope.m4s", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
	rr = doRequest(t, h.Routes(), "GET", "/api/cameras/cam-serve/playback/rec-serve/init.mp4/x.mp4", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)

	// Unknown recording and camera mismatch.
	rr = doRequest(t, h.Routes(), "GET", "/api/cameras/cam-serve/playback/unknown/init.mp4", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
	rr = doRequest(t, h.Routes(), "GET", "/api/cameras/other-cam/playback/rec-serve/init.mp4", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)

	// Non-VOD format and missing segment file.
	rr = doRequest(t, h.Routes(), "GET", "/api/cameras/cam-serve/playback/rec-mjpeg/init.mp4", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
	rr = doRequest(t, h.Routes(), "GET", "/api/cameras/cam-serve/playback/rec-gone/init.mp4", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)

	// A failed init write is logged and does not corrupt the response status.
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "cam-serve")
	rctx.URLParams.Add("recId", "rec-serve")
	req := httptest.NewRequest("GET", "/api/cameras/cam-serve/playback/rec-serve/init.mp4", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	ew := &um74ErrWriter{}
	h.handleVODInit(ew, req)
	require.True(t, ew.wrote)
	require.Equal(t, "video/mp4", ew.Header().Get("Content-Type"))
}

func TestIssue74VODStorageErrors(t *testing.T) {
	db, store := setupTestDB(t)
	repo := &um74Repo{APIRepository: db}
	h := TestHandler(repo, store)
	start := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	window := um74VODQuery(start, start.Add(time.Minute))

	repo.listRecordingsErr = errors.New("boom")
	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam-err/playback/playlist.m3u8"+window, nil, "", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code)
	rr = doRequest(t, h.Routes(), "GET", "/api/cameras/cam-err/playback/export.m3u8"+window, nil, "", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code)
	repo.listRecordingsErr = nil

	repo.getRecordingErr = errors.New("boom")
	rr = doRequest(t, h.Routes(), "GET", "/api/cameras/cam-err/playback/rec-x/init.mp4", nil, "", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code)
	rr = doRequest(t, h.Routes(), "GET", "/api/cameras/cam-err/playback/rec-x/f0-1.m4s", nil, "", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code)
	repo.getRecordingErr = nil

	repo.timelineErr = errors.New("boom")
	rr = doRequest(t, h.Routes(), "GET", "/api/recordings/timeline?camera_id=cam-err"+strings.ReplaceAll(window, "?", "&"), nil, "", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code)
	repo.timelineErr = nil
}

func TestIssue74RecordingsTimeline(t *testing.T) {
	h, db := um74NewHandler(t)
	start := time.Date(2026, 4, 5, 6, 0, 0, 0, time.UTC)
	um74SeedRecording(t, db, "rec-tl", "cam-tl", filepath.Join(t.TempDir(), "x.mp4"), model.FormatH264, start, time.Minute)

	rr := doRequest(t, h.Routes(), "GET", "/api/recordings/timeline", nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)

	rr = doRequest(t, h.Routes(), "GET", "/api/recordings/timeline?camera_id=cam-tl", nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)

	rr = doRequest(t, h.Routes(), "GET", "/api/recordings/timeline?camera_id=cam-tl&start=bad&end=bad", nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)

	window := um74VODQuery(start.Add(-time.Minute), start.Add(2*time.Minute))
	rr = doRequest(t, h.Routes(), "GET", "/api/recordings/timeline?camera_id=cam-tl"+strings.ReplaceAll(window, "?", "&"), nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var body struct {
		CameraID string                  `json:"camera_id"`
		Entries  []storage.TimelineEntry `json:"entries"`
	}
	parseJSON(t, rr, &body)
	require.Equal(t, "cam-tl", body.CameraID)
	require.Len(t, body.Entries, 1)
	require.Equal(t, "rec-tl", body.Entries[0].ID)

	// A camera without recordings still returns an empty (non-null) entry list.
	emptyWindow := um74VODQuery(start, start.Add(time.Minute))
	rr = doRequest(t, h.Routes(), "GET", "/api/recordings/timeline?camera_id=cam-empty"+strings.ReplaceAll(emptyWindow, "?", "&"), nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Contains(t, rr.Body.String(), `"entries":[]`)
	var emptyBody struct {
		Entries []storage.TimelineEntry `json:"entries"`
	}
	parseJSON(t, rr, &emptyBody)
	require.Empty(t, emptyBody.Entries)
}

// ---------------------------------------------------------------------------
// Relay
// ---------------------------------------------------------------------------

func um74RelayEngine() *stubMediaEngine {
	return &stubMediaEngine{
		streams: []media.StreamInfo{{StreamID: "stream/one", Active: true}},
		streamsByID: map[string]*media.StreamInfo{
			"stream/one": {StreamID: "stream/one", Active: false},
		},
	}
}

func TestIssue74RelayHandlers(t *testing.T) {
	h, db := um74NewHandler(t)
	mgr := relay.NewManager(db, um74RelayEngine())
	t.Cleanup(mgr.Stop)
	h.SetRelayManager(mgr)
	routes := h.Routes()

	rr := doRequest(t, routes, "GET", "/api/relay/tasks", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var tasks []relay.Task
	parseJSON(t, rr, &tasks)
	require.Empty(t, tasks)

	// Create validation.
	rr = doRequest(t, routes, "POST", "/api/relay/tasks", strings.NewReader("{"), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, routes, "POST", "/api/relay/tasks", strings.NewReader(`{"target_url":"rtmp://x/live"}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, routes, "POST", "/api/relay/tasks", strings.NewReader(`{"stream_id":"stream/one"}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, routes, "POST", "/api/relay/tasks", strings.NewReader(`{"stream_id":"missing/stream","target_url":"rtmp://x/live"}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)

	rr = doRequest(t, routes, "POST", "/api/relay/tasks", strings.NewReader(`{"stream_id":"stream/one","target_url":"rtmp://x/live"}`), "", "")
	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
	var created relay.Task
	parseJSON(t, rr, &created)
	require.NotEmpty(t, created.ID)
	require.Equal(t, "stream/one", created.StreamID)

	rr = doRequest(t, routes, "GET", "/api/relay/tasks", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	parseJSON(t, rr, &tasks)
	require.Len(t, tasks, 1)

	rr = doRequest(t, routes, "GET", "/api/relay/tasks/"+created.ID, nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var got relay.Task
	parseJSON(t, rr, &got)
	require.Equal(t, created.ID, got.ID)

	rr = doRequest(t, routes, "GET", "/api/relay/tasks/does-not-exist", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)

	rr = doRequest(t, routes, "GET", "/api/relay/tasks/"+created.ID+"/stats", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var stats relay.TaskStats
	parseJSON(t, rr, &stats)
	require.Equal(t, created.ID, stats.TaskID)

	rr = doRequest(t, routes, "GET", "/api/relay/tasks/missing/stats", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)

	rr = doRequest(t, routes, "GET", "/api/relay/tasks/"+created.ID+"/stats/history", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var history relay.StatsHistory
	parseJSON(t, rr, &history)
	require.Equal(t, created.ID, history.TaskID)

	rr = doRequest(t, routes, "GET", "/api/relay/tasks/"+created.ID+"/stats/history?duration=45", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	rr = doRequest(t, routes, "GET", "/api/relay/tasks/"+created.ID+"/stats/history?duration=notanumber", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	rr = doRequest(t, routes, "GET", "/api/relay/tasks/missing/stats/history", nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)

	// The stubbed stream is not active, so start fails.
	rr = doRequest(t, routes, "POST", "/api/relay/tasks/"+created.ID+"/start", nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())

	// The task is not running, so stop fails.
	rr = doRequest(t, routes, "POST", "/api/relay/tasks/"+created.ID+"/stop", nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())

	rr = doRequest(t, routes, "DELETE", "/api/relay/tasks/"+created.ID, nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

	rr = doRequest(t, routes, "GET", "/api/relay/tasks/"+created.ID, nil, "", "")
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestIssue74RelayUnavailableAndErrors(t *testing.T) {
	// No relay manager configured.
	h, _ := um74NewHandler(t)
	for _, ep := range []struct{ method, path string }{
		{"GET", "/api/relay/tasks"},
		{"POST", "/api/relay/tasks"},
		{"GET", "/api/relay/tasks/x"},
		{"DELETE", "/api/relay/tasks/x"},
		{"POST", "/api/relay/tasks/x/start"},
		{"POST", "/api/relay/tasks/x/stop"},
		{"GET", "/api/relay/tasks/x/stats"},
		{"GET", "/api/relay/tasks/x/stats/history"},
	} {
		body := strings.NewReader("")
		if ep.method == "POST" {
			body = strings.NewReader(`{"stream_id":"a","target_url":"rtmp://x"}`)
		}
		rr := doRequest(t, h.Routes(), ep.method, ep.path, body, "", "")
		require.Equal(t, http.StatusServiceUnavailable, rr.Code, "%s %s", ep.method, ep.path)
	}

	// Repository failures.
	db, store := setupTestDB(t)
	repo := &um74RelayRepo{RelayTaskRepository: db}
	mgr := relay.NewManager(repo, um74RelayEngine())
	t.Cleanup(mgr.Stop)
	h2 := TestHandler(db, store)
	h2.SetRelayManager(mgr)

	repo.listErr = errors.New("boom")
	rr := doRequest(t, h2.Routes(), "GET", "/api/relay/tasks", nil, "", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code)
	repo.listErr = nil

	repo.deleteErr = errors.New("boom")
	rr = doRequest(t, h2.Routes(), "DELETE", "/api/relay/tasks/whatever", nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	repo.deleteErr = nil
}

func TestIssue74RelayEmptyTaskID(t *testing.T) {
	h, db := um74NewHandler(t)
	mgr := relay.NewManager(db, um74RelayEngine())
	t.Cleanup(mgr.Stop)
	h.SetRelayManager(mgr)

	// The router always fills {id}, so exercise the defensive empty-id branches
	// by invoking the handlers with a route context that has no URL params.
	call := func(fn http.HandlerFunc) *httptest.ResponseRecorder {
		rctx := chi.NewRouteContext()
		req := httptest.NewRequest("GET", "/api/relay/tasks", nil)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		rr := httptest.NewRecorder()
		fn(rr, req)
		return rr
	}
	for name, fn := range map[string]http.HandlerFunc{
		"get":     h.handleGetRelayTask,
		"delete":  h.handleDeleteRelayTask,
		"start":   h.handleStartRelayTask,
		"stop":    h.handleStopRelayTask,
		"stats":   h.handleGetRelayTaskStats,
		"history": h.handleGetRelayTaskStatsHistory,
	} {
		rr := call(fn)
		require.Equal(t, http.StatusBadRequest, rr.Code, name)
	}
}

// ---------------------------------------------------------------------------
// Service logs
// ---------------------------------------------------------------------------

func TestIssue74ServiceLogsParameters(t *testing.T) {
	h, _ := um74NewHandler(t)
	ring := middleware.NewLogRing(8)
	h.logRing = ring
	now := time.Now().UTC()
	ring.Append(middleware.LogEntry{Time: now.Add(-time.Minute), Level: "INFO", Message: "first"})
	ring.Append(middleware.LogEntry{Time: now, Level: "ERROR", Message: "second", Attrs: map[string]any{"camera_id": "cam-1"}})

	rr := doRequest(t, h.Routes(), "GET", "/api/service-logs?after_seq=abc", nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, h.Routes(), "GET", "/api/service-logs?since=yesterday", nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, h.Routes(), "GET", "/api/service-logs?limit=-3", nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)

	// limit=0 falls back to the default, huge limits are clamped.
	rr = doRequest(t, h.Routes(), "GET", "/api/service-logs?limit=0", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var body struct {
		Logs      []middleware.LogEntry `json:"logs"`
		Count     int                   `json:"count"`
		Capacity  int                   `json:"capacity"`
		Buffered  int                   `json:"buffered"`
		Truncated bool                  `json:"truncated"`
		Source    string                `json:"source"`
		Path      string                `json:"path"`
	}
	parseJSON(t, rr, &body)
	require.Equal(t, 2, body.Count)
	require.Equal(t, 8, body.Capacity)
	require.Equal(t, "memory", body.Source)
	require.False(t, body.Truncated)

	rr = doRequest(t, h.Routes(), "GET", "/api/service-logs?limit=999999", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)

	// after_seq + since + level + q combine.
	rr = doRequest(t, h.Routes(), "GET",
		"/api/service-logs?after_seq=1&since=2020-01-01T00:00:00Z&level=error&q=second", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	parseJSON(t, rr, &body)
	require.Equal(t, 1, body.Count)
	require.Equal(t, "second", body.Logs[0].Message)

	// Missing ring falls back to the process-wide ring.
	h.logRing = nil
	rr = doRequest(t, h.Routes(), "GET", "/api/service-logs", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	parseJSON(t, rr, &body)
	require.Greater(t, body.Capacity, 0)

	// A directory or missing file is ignored and the ring is used.
	h.logRing = ring
	h.SetServiceLogPath(t.TempDir())
	rr = doRequest(t, h.Routes(), "GET", "/api/service-logs", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	parseJSON(t, rr, &body)
	require.Equal(t, "memory", body.Source)
	h.SetServiceLogPath(filepath.Join(t.TempDir(), "absent.log"))
	rr = doRequest(t, h.Routes(), "GET", "/api/service-logs", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	parseJSON(t, rr, &body)
	require.Equal(t, "memory", body.Source)

	// Existing log file is served from disk.
	path := filepath.Join(t.TempDir(), "lalmax-nvr.log")
	content := "time=2026-09-22T15:21:18.438+08:00 level=ERROR msg=file-line\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	h.SetServiceLogPath(path)
	rr = doRequest(t, h.Routes(), "GET", "/api/service-logs?level=error&q=file-line", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	parseJSON(t, rr, &body)
	require.Equal(t, "file", body.Source)
	require.Equal(t, path, body.Path)
	require.Equal(t, 1, body.Count)
	require.Zero(t, body.Capacity)
	require.Greater(t, body.Buffered, 0)
}

// um74StartStream launches the SSE handler, waits for the header flush and then
// retries appendFn until at least one event is flushed. It returns the writer
// and a stop function that cancels the handler and waits for it to exit.
func um74StartStream(t *testing.T, h *Handler, target string, appendFn func()) (*um74FlushWriter, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", target, nil).WithContext(ctx)
	w := um74NewFlushWriter()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.handleServiceLogsStream(w, req)
	}()
	w.waitFlush(t, "initial headers")
	// The handler subscribes right after the first flush, so entries appended
	// before that are missed; retry until an event comes through.
	stop := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("service-log stream did not stop after cancel")
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		appendFn()
		select {
		case <-w.flushed:
			return w, stop
		case <-time.After(10 * time.Millisecond):
			if time.Now().After(deadline) {
				stop()
				t.Fatal("service-log stream produced no matching event")
			}
		}
	}
}

func TestIssue74ServiceLogsStreamRing(t *testing.T) {
	h, _ := um74NewHandler(t)
	ring := middleware.NewLogRing(16)
	h.logRing = ring

	w, stop := um74StartStream(t, h, "/api/service-logs/stream?level=warn&q=needle", func() {
		ring.Append(middleware.LogEntry{Level: "INFO", Message: "ignored by level"})
		ring.Append(middleware.LogEntry{Level: "WARN", Message: "no query match", Attrs: map[string]any{"camera_id": "cam-9"}})
		ring.Append(middleware.LogEntry{Level: "WARN", Message: "matched entry", Attrs: map[string]any{"needle_key": "value"}})
	})
	stop()
	body := w.Body.String()
	require.Equal(t, "text/event-stream", w.Header().Get("Content-Type"))
	require.Equal(t, "no-cache", w.Header().Get("Cache-Control"))
	require.Contains(t, body, "event: log")
	require.Contains(t, body, "needle_key")
	require.NotContains(t, body, "ignored by level")
}

func TestIssue74ServiceLogsStreamWithoutQuery(t *testing.T) {
	h, _ := um74NewHandler(t)
	ring := middleware.NewLogRing(16)
	h.logRing = ring

	// No level/q params: every INFO entry is streamed.
	w, stop := um74StartStream(t, h, "/api/service-logs/stream", func() {
		ring.Append(middleware.LogEntry{Level: "INFO", Message: "plain entry"})
	})
	stop()
	require.Contains(t, w.Body.String(), "plain entry")
}

func TestIssue74ServiceLogsUnreadableFile(t *testing.T) {
	h, _ := um74NewHandler(t)
	h.logRing = middleware.NewLogRing(4)
	path := filepath.Join(t.TempDir(), "locked.log")
	require.NoError(t, os.WriteFile(path, []byte("time=2026-09-22T15:21:18.438+08:00 level=INFO msg=x\n"), 0o600))
	require.NoError(t, os.Chmod(path, 0o000))
	if f, err := os.Open(path); err == nil {
		_ = f.Close()
		t.Skip("file permissions are not enforced for this user")
	}
	h.SetServiceLogPath(path)

	rr := doRequest(t, h.Routes(), "GET", "/api/service-logs", nil, "", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code)

	w := um74NewFlushWriter()
	h.handleServiceLogsStream(w, httptest.NewRequest("GET", "/api/service-logs/stream", nil))
	require.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestIssue74ServiceLogsStreamUnsupported(t *testing.T) {
	h, _ := um74NewHandler(t)
	h.logRing = middleware.NewLogRing(4)

	// A nil handler reports no log file.
	var nilHandler *Handler
	require.Empty(t, nilHandler.serviceLogFile())

	// Ring-backed stream requires http.Flusher.
	w := &um74PlainWriter{}
	h.handleServiceLogsStream(w, httptest.NewRequest("GET", "/api/service-logs/stream", nil))
	require.Equal(t, http.StatusInternalServerError, w.status)
	require.Contains(t, w.buf.String(), "streaming not supported")

	// File-backed stream requires http.Flusher as well.
	path := filepath.Join(t.TempDir(), "lalmax-nvr.log")
	require.NoError(t, os.WriteFile(path, []byte("time=2026-09-22T15:21:18.438+08:00 level=INFO msg=x\n"), 0o644))
	h.SetServiceLogPath(path)
	w2 := &um74PlainWriter{}
	h.handleServiceLogsStream(w2, httptest.NewRequest("GET", "/api/service-logs/stream", nil))
	require.Equal(t, http.StatusInternalServerError, w2.status)
	require.Contains(t, w2.buf.String(), "streaming not supported")
}

func TestIssue74ServiceLogsStreamFile(t *testing.T) {
	h, _ := um74NewHandler(t)
	h.logRing = middleware.NewLogRing(4)
	path := filepath.Join(t.TempDir(), "lalmax-nvr.log")
	content := "\n" +
		"time=2026-09-22T15:21:17.438+08:00 level=INFO msg=other-entry\n" +
		"time=2026-09-22T15:21:18.438+08:00 level=ERROR msg=file-match\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	h.SetServiceLogPath(path)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest("GET", "/api/service-logs/stream?level=error&q=file-match&after_seq=0", nil).WithContext(ctx)
	w := um74NewFlushWriter()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.handleServiceLogsStream(w, req)
	}()
	w.waitFlush(t, "initial headers")
	// The handler polls the log file once per second.
	w.waitFlush(t, "log file event")
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("file-backed service-log stream did not stop after cancel")
	}
	body := w.Body.String()
	require.Contains(t, body, "event: log")
	require.Contains(t, body, "file-match")
}

func TestIssue74ServiceLogsStreamFileTruncation(t *testing.T) {
	h, _ := um74NewHandler(t)
	h.logRing = middleware.NewLogRing(4)
	path := filepath.Join(t.TempDir(), "lalmax-nvr.log")
	first := "time=2026-09-22T15:21:18.438+08:00 level=INFO msg=first-generation-padding-padding-padding-padding-padding-padding\n"
	require.NoError(t, os.WriteFile(path, []byte(first), 0o644))
	h.SetServiceLogPath(path)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest("GET", "/api/service-logs/stream?after_seq=0", nil).WithContext(ctx)
	w := um74NewFlushWriter()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.handleServiceLogsStream(w, req)
	}()
	w.waitFlush(t, "initial headers")
	w.waitFlush(t, "first generation event")

	// Rotating the file to a shorter payload exercises the truncation reset.
	second := "time=2026-09-22T15:21:20.438+08:00 level=INFO msg=second-generation\n"
	require.NoError(t, os.WriteFile(path, []byte(second), 0o644))
	w.waitFlush(t, "second generation event")

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("file-backed service-log stream did not stop after cancel")
	}
	body := w.Body.String()
	require.Contains(t, body, "first-generation-padding")
	require.Contains(t, body, "second-generation")
}

// ---------------------------------------------------------------------------
// JT808
// ---------------------------------------------------------------------------

func TestIssue74JT808Handlers(t *testing.T) {
	h, _ := um74NewHandler(t)
	routes := h.Routes()

	// No server configured.
	rr := doRequest(t, routes, "GET", "/api/jt808/terminals", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	var terminals struct {
		Terminals []jt808.Terminal `json:"terminals"`
	}
	parseJSON(t, rr, &terminals)
	require.Empty(t, terminals.Terminals)

	for _, ep := range []struct{ method, path, body string }{
		{"POST", "/api/jt808/play", `{"key":"1003"}`},
		{"POST", "/api/jt808/stop", `{"key":"1003"}`},
		{"POST", "/api/jt808/control", `{"key":"1003"}`},
		{"POST", "/api/jt808/loss", `{"key":"1003"}`},
		{"GET", "/api/jt808/av?key=1003", ""},
		{"POST", "/api/jt808/playback", `{"key":"1003"}`},
		{"POST", "/api/jt808/playback/control", `{"key":"1003"}`},
		{"POST", "/api/jt808/resources", `{"key":"1003"}`},
		{"POST", "/api/jt808/upload", `{"key":"1003"}`},
		{"POST", "/api/jt808/upload/control", `{"key":"1003"}`},
		{"POST", "/api/jt808/ptz", `{"key":"1003"}`},
		{"POST", "/api/jt808/downlink", `{"key":"1003"}`},
	} {
		rr := doRequest(t, routes, ep.method, ep.path, strings.NewReader(ep.body), "", "")
		require.Equal(t, http.StatusServiceUnavailable, rr.Code, "%s %s", ep.method, ep.path)
	}

	srv := &um74JT808{}
	h.SetJT808Server(srv)

	rr = doRequest(t, routes, "GET", "/api/jt808/terminals", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	parseJSON(t, rr, &terminals)
	require.Len(t, terminals.Terminals, 1)
	require.Equal(t, "1003", terminals.Terminals[0].Key)

	// Play: body/key validation, bad-input mapping, runtime error and success.
	rr = doRequest(t, routes, "POST", "/api/jt808/play", strings.NewReader("{"), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, routes, "POST", "/api/jt808/play", strings.NewReader(`{}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)

	srv.err = jt808.ErrBadInput
	rr = doRequest(t, routes, "POST", "/api/jt808/play", strings.NewReader(`{"key":"1003"}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	srv.err = errors.New("terminal offline")
	rr = doRequest(t, routes, "POST", "/api/jt808/play", strings.NewReader(`{"key":"1003"}`), "", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code)

	srv.err = nil
	rr = doRequest(t, routes, "POST", "/api/jt808/play",
		strings.NewReader(`{"key":"1003","channel":2,"transport":"tcp","data_type":3,"stream":1}`), "", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var playResp map[string]any
	parseJSON(t, rr, &playResp)
	require.Equal(t, "stream-1", playResp["stream_id"])
	require.Equal(t, byte(2), srv.playInput.Channel)
	require.Equal(t, "tcp", srv.playInput.Transport)
	require.Equal(t, byte(3), srv.playInput.DataType)
	require.Equal(t, byte(1), srv.playInput.Stream)

	// Stop.
	rr = doRequest(t, routes, "POST", "/api/jt808/stop", strings.NewReader(`{"key":"1003","channel":4}`), "", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Equal(t, byte(4), srv.stopInput.Channel)
	rr = doRequest(t, routes, "POST", "/api/jt808/stop", strings.NewReader(`{"channel":4}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	srv.err = jt808.ErrBadInput
	rr = doRequest(t, routes, "POST", "/api/jt808/stop", strings.NewReader(`{"key":"1003"}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	srv.err = nil

	// Live control.
	rr = doRequest(t, routes, "POST", "/api/jt808/control",
		strings.NewReader(`{"key":"1003","channel":1,"control":2,"close_av":1,"stream":1}`), "", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Equal(t, byte(2), srv.controlInput.Control)
	require.Equal(t, byte(1), srv.controlInput.CloseAV)
	rr = doRequest(t, routes, "POST", "/api/jt808/control", strings.NewReader(`{"channel":1}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	srv.err = errors.New("boom")
	rr = doRequest(t, routes, "POST", "/api/jt808/control", strings.NewReader(`{"key":"1003"}`), "", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code)
	srv.err = nil

	// Packet loss report.
	rr = doRequest(t, routes, "POST", "/api/jt808/loss", strings.NewReader(`{"key":"1003","channel":1,"rate":2}`), "", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Equal(t, byte(2), srv.lossInput.Rate)
	rr = doRequest(t, routes, "POST", "/api/jt808/loss", strings.NewReader(`{"channel":1}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	srv.err = errors.New("boom")
	rr = doRequest(t, routes, "POST", "/api/jt808/loss", strings.NewReader(`{"key":"1003"}`), "", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code)
	srv.err = nil

	// AV query.
	rr = doRequest(t, routes, "GET", "/api/jt808/av", nil, "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, routes, "GET", "/api/jt808/av?key=1003", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var av jt808.AVProperties
	parseJSON(t, rr, &av)
	require.Equal(t, byte(98), av.VideoCodec)
	srv.err = errors.New("boom")
	rr = doRequest(t, routes, "GET", "/api/jt808/av?key=1003", nil, "", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code)
	srv.err = nil
}

func TestIssue74JT808PlaybackAndCommands(t *testing.T) {
	h, _ := um74NewHandler(t)
	srv := &um74JT808{resources: []jt808.Resource{{Channel: 1, AVType: 2}}}
	h.SetJT808Server(srv)
	routes := h.Routes()

	// Playback time parsing and validation.
	rr := doRequest(t, routes, "POST", "/api/jt808/playback", strings.NewReader("{"), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, routes, "POST", "/api/jt808/playback", strings.NewReader(`{}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, routes, "POST", "/api/jt808/playback", strings.NewReader(`{"key":"1003"}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, routes, "POST", "/api/jt808/playback", strings.NewReader(`{"key":"1003","start":"bad","end":"2026-01-02T03:04:05Z"}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, routes, "POST", "/api/jt808/playback", strings.NewReader(`{"key":"1003","start":"2026-01-02T03:04:05Z","end":"bad"}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)

	// RFC3339 layout.
	rr = doRequest(t, routes, "POST", "/api/jt808/playback",
		strings.NewReader(`{"key":"1003","channel":1,"transport":"tcp","av_type":2,"stream_type":1,"storage":1,"mode":1,"speed":2,"start":"2026-01-02T03:04:05Z","end":"2026-01-02T03:14:05Z"}`), "", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var playResp map[string]any
	parseJSON(t, rr, &playResp)
	require.Equal(t, "pb-1", playResp["stream_id"])
	wantStart, err := time.ParseInLocation(time.RFC3339, "2026-01-02T03:04:05Z", time.Local)
	require.NoError(t, err)
	require.True(t, wantStart.Equal(srv.playbackInput.Start))
	require.Equal(t, byte(2), srv.playbackInput.AVType)

	// Local layouts without a zone.
	rr = doRequest(t, routes, "POST", "/api/jt808/playback",
		strings.NewReader(`{"key":"1003","start":"2026-01-02T03:04:05","end":"2026-01-02 03:14:05"}`), "", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Equal(t, 2026, srv.playbackInput.Start.Year())
	require.Equal(t, time.January, srv.playbackInput.End.Month())

	srv.err = errors.New("boom")
	rr = doRequest(t, routes, "POST", "/api/jt808/playback",
		strings.NewReader(`{"key":"1003","start":"2026-01-02T03:04:05Z","end":"2026-01-02T03:14:05Z"}`), "", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code)
	srv.err = nil

	// Playback control.
	rr = doRequest(t, routes, "POST", "/api/jt808/playback/control", strings.NewReader("{"), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, routes, "POST", "/api/jt808/playback/control", strings.NewReader(`{}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, routes, "POST", "/api/jt808/playback/control", strings.NewReader(`{"key":"1003","drag_to":"bad"}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, routes, "POST", "/api/jt808/playback/control", strings.NewReader(`{"key":"1003","channel":1,"control":2,"speed":3}`), "", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Equal(t, byte(3), srv.pbControlInput.Speed)
	require.True(t, srv.pbControlInput.DragTo.IsZero())
	rr = doRequest(t, routes, "POST", "/api/jt808/playback/control",
		strings.NewReader(`{"key":"1003","control":2,"drag_to":"2026-01-02T03:04:05Z"}`), "", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.False(t, srv.pbControlInput.DragTo.IsZero())
	srv.err = errors.New("boom")
	rr = doRequest(t, routes, "POST", "/api/jt808/playback/control", strings.NewReader(`{"key":"1003"}`), "", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code)
	srv.err = nil

	// Resource query.
	rr = doRequest(t, routes, "POST", "/api/jt808/resources", strings.NewReader(`{"key":"1003"}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, routes, "POST", "/api/jt808/resources", strings.NewReader(`{"channel":1}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, routes, "POST", "/api/jt808/resources",
		strings.NewReader(`{"key":"1003","start":"2026-01-02T03:04:05Z","end":"bad"}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, routes, "POST", "/api/jt808/resources",
		strings.NewReader(`{"key":"1003","channel":1,"start":"2026-01-02T03:04:05Z","end":"2026-01-02T04:04:05Z","av_type":2}`), "", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var resourceResp struct {
		Resources []jt808.Resource `json:"resources"`
	}
	parseJSON(t, rr, &resourceResp)
	require.Len(t, resourceResp.Resources, 1)
	require.Equal(t, byte(1), srv.resourceQuery.Channel)
	srv.err = errors.New("boom")
	rr = doRequest(t, routes, "POST", "/api/jt808/resources",
		strings.NewReader(`{"key":"1003","start":"2026-01-02T03:04:05Z","end":"2026-01-02T04:04:05Z"}`), "", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code)
	srv.err = nil

	// Upload.
	rr = doRequest(t, routes, "POST", "/api/jt808/upload", strings.NewReader(`{"key":"1003","start":"2026-01-02T03:04:05Z"}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, routes, "POST", "/api/jt808/upload", strings.NewReader(`{"start":"2026-01-02T03:04:05Z","end":"2026-01-02T04:04:05Z"}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, routes, "POST", "/api/jt808/upload",
		strings.NewReader(`{"key":"1003","start":"bad","end":"2026-01-02T04:04:05Z"}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, routes, "POST", "/api/jt808/upload",
		strings.NewReader(`{"key":"1003","start":"2026-01-02T03:04:05Z","end":"bad"}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, routes, "POST", "/api/jt808/upload",
		strings.NewReader(`{"key":"1003","channel":1,"host":"1.2.3.4","port":21,"user":"u","password":"p","path":"/x","start":"2026-01-02T03:04:05Z","end":"2026-01-02T04:04:05Z","av_type":2,"stream_type":1,"storage":1,"condition":1}`), "", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var uploadResp map[string]any
	parseJSON(t, rr, &uploadResp)
	require.Equal(t, float64(7), uploadResp["serial"])
	require.Equal(t, 21, srv.uploadInput.Port)
	require.Equal(t, "1.2.3.4", srv.uploadInput.Host)
	srv.err = errors.New("boom")
	rr = doRequest(t, routes, "POST", "/api/jt808/upload",
		strings.NewReader(`{"key":"1003","start":"2026-01-02T03:04:05Z","end":"2026-01-02T04:04:05Z"}`), "", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code)
	srv.err = nil

	// Upload control.
	rr = doRequest(t, routes, "POST", "/api/jt808/upload/control", strings.NewReader(`{"key":"1003","serial":7,"control":1}`), "", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Equal(t, uint16(7), srv.uploadCtlInput.Serial)
	rr = doRequest(t, routes, "POST", "/api/jt808/upload/control", strings.NewReader(`{"serial":7}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	srv.err = errors.New("boom")
	rr = doRequest(t, routes, "POST", "/api/jt808/upload/control", strings.NewReader(`{"key":"1003"}`), "", "")
	require.Equal(t, http.StatusInternalServerError, rr.Code)
	srv.err = nil

	// PTZ.
	rr = doRequest(t, routes, "POST", "/api/jt808/ptz",
		strings.NewReader(`{"key":"1003","channel":1,"action":"turn_up","direct":1,"speed":2}`), "", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Equal(t, "turn_up", srv.ptzInput.Action)
	require.Equal(t, byte(2), srv.ptzInput.Speed)
	rr = doRequest(t, routes, "POST", "/api/jt808/ptz", strings.NewReader(`{"action":"turn_up"}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	srv.err = jt808.ErrBadInput
	rr = doRequest(t, routes, "POST", "/api/jt808/ptz", strings.NewReader(`{"key":"1003","action":"nope"}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestIssue74JT808Downlink(t *testing.T) {
	h, _ := um74NewHandler(t)
	srv := &um74JT808{}
	h.SetJT808Server(srv)
	routes := h.Routes()

	// The plain stub media engine does not implement JT1078 audio pushing.
	h.SetMediaEngine(&stubMediaEngine{})
	rr := doRequest(t, routes, "POST", "/api/jt808/downlink", strings.NewReader(`{"key":"1003","payload":"AQID"}`), "", "")
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)

	engine := um74NewAudioEngine()
	h.SetMediaEngine(engine)

	rr = doRequest(t, routes, "POST", "/api/jt808/downlink", strings.NewReader("{"), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, routes, "POST", "/api/jt808/downlink", strings.NewReader(`{}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, routes, "POST", "/api/jt808/downlink", strings.NewReader(`{"key":"1003","payload":"!!not-base64!!"}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)
	rr = doRequest(t, routes, "POST", "/api/jt808/downlink", strings.NewReader(`{"key":"1003","payload":""}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)

	payload := []byte{0x01, 0x02, 0x03}
	rr = doRequest(t, routes, "POST", "/api/jt808/downlink",
		strings.NewReader(`{"key":"1003","payload":"`+base64.StdEncoding.EncodeToString(payload)+`"}`), "", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	engine.mu.Lock()
	require.Equal(t, 1, engine.calls)
	require.Equal(t, "1003", engine.gotSIM)
	require.Equal(t, byte(1), engine.gotCh)
	require.Equal(t, byte(6), engine.gotPT)
	require.Equal(t, payload, engine.gotData)
	engine.mu.Unlock()

	// Explicit channel/PT are honoured.
	rr = doRequest(t, routes, "POST", "/api/jt808/downlink",
		strings.NewReader(`{"key":"1003","channel":2,"pt":8,"payload":"`+base64.StdEncoding.EncodeToString(payload)+`"}`), "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	engine.mu.Lock()
	require.Equal(t, byte(2), engine.gotCh)
	require.Equal(t, byte(8), engine.gotPT)
	engine.mu.Unlock()

	// Unsupported payloads map to 400, other pusher failures to 502.
	engine.pushErr = errors.New("unsupported audio codec")
	rr = doRequest(t, routes, "POST", "/api/jt808/downlink",
		strings.NewReader(`{"key":"1003","payload":"`+base64.StdEncoding.EncodeToString(payload)+`"}`), "", "")
	require.Equal(t, http.StatusBadRequest, rr.Code)

	engine.pushErr = errors.New("downlink socket closed")
	rr = doRequest(t, routes, "POST", "/api/jt808/downlink",
		strings.NewReader(`{"key":"1003","payload":"`+base64.StdEncoding.EncodeToString(payload)+`"}`), "", "")
	require.Equal(t, http.StatusBadGateway, rr.Code)
}
