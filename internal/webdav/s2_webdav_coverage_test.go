package webdav

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

// s2WebDAVRepo is a scriptable storage.WebDAVRepository.
type s2WebDAVRepo struct {
	cameras     []storage.CameraRow
	listErr     error
	upsertErr   error
	insertErr   error
	upsertCalls []storage.CameraRow
	upserts     int
	recordings  []*model.Recording
}

func (r *s2WebDAVRepo) ListCameras(context.Context) ([]storage.CameraRow, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	return r.cameras, nil
}

func (r *s2WebDAVRepo) UpsertCamera(_ context.Context, id, name, protocol, encoding, url, username, password string, enabled bool, onvifEndpoint, profileToken, streamEncoding string, extras ...string) error {
	r.upserts++
	r.upsertCalls = append(r.upsertCalls, storage.CameraRow{ID: id, Name: name, Protocol: protocol, Enabled: enabled})
	return r.upsertErr
}

func (r *s2WebDAVRepo) InsertRecording(_ context.Context, recording *model.Recording) error {
	if r.insertErr != nil {
		return r.insertErr
	}
	r.recordings = append(r.recordings, recording)
	return nil
}

func (r *s2WebDAVRepo) InsertRecordingWithRetry(ctx context.Context, recording *model.Recording, _ int, _ time.Duration) error {
	return r.InsertRecording(ctx, recording)
}

func s2Server(t *testing.T, repo storage.WebDAVRepository) (*Server, string) {
	t.Helper()
	root := t.TempDir()
	store, err := storage.NewManager(root)
	require.NoError(t, err)
	return NewServer(store, "/dav", nil, repo, true), root
}

func TestS2ResolveOrCreateCamera(t *testing.T) {
	ctx := context.Background()

	// An existing camera is reused by name.
	repo := &s2WebDAVRepo{cameras: []storage.CameraRow{
		{ID: "cam-1", Name: "other"},
		{ID: "cam-2", Name: "front-door"},
	}}
	srv, _ := s2Server(t, repo)
	require.Equal(t, "cam-2", srv.resolveOrCreateCamera(ctx, "front-door"))
	require.Equal(t, 0, repo.upserts)

	// A lookup failure falls through to creation.
	repo = &s2WebDAVRepo{listErr: errors.New("camera table unavailable")}
	srv, _ = s2Server(t, repo)
	id := srv.resolveOrCreateCamera(ctx, "new-camera")
	require.NotEmpty(t, id)
	require.Equal(t, 1, repo.upserts)
	require.Equal(t, "new-camera", repo.upsertCalls[0].Name)

	// A failed creation returns an empty id.
	repo = &s2WebDAVRepo{upsertErr: errors.New("insert failed")}
	srv, _ = s2Server(t, repo)
	require.Empty(t, srv.resolveOrCreateCamera(ctx, "broken"))
}

func TestS2HandlePutRegistersRecording(t *testing.T) {
	repo := &s2WebDAVRepo{cameras: []storage.CameraRow{{ID: "cam-1", Name: "camera-01"}}}
	srv, root := s2Server(t, repo)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "camera-01"), 0o755))

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, err := http.NewRequest(http.MethodPut, ts.URL+"/dav/camera-01/uploaded.mp4", strings.NewReader("payload"))
	require.NoError(t, err)
	resp, err := ts.Client().Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Contains(t, []int{http.StatusOK, http.StatusCreated, http.StatusNoContent}, resp.StatusCode)

	require.Len(t, repo.recordings, 1)
	rec := repo.recordings[0]
	require.Equal(t, "cam-1", rec.CameraID)
	require.Equal(t, "camera-01/uploaded.mp4", rec.FilePath)
	require.Equal(t, int64(len("payload")), rec.FileSize)
	require.Equal(t, model.FormatH264, rec.Format)
	require.NotEmpty(t, rec.ID)

	// A JPEG upload is registered with the MJPEG format.
	req, err = http.NewRequest(http.MethodPut, ts.URL+"/dav/camera-01/snapshot.jpg", strings.NewReader("jpeg"))
	require.NoError(t, err)
	resp, err = ts.Client().Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Len(t, repo.recordings, 2)
	require.Equal(t, model.FormatMJPEG, repo.recordings[1].Format)
}

func TestS2HandlePutErrorBranches(t *testing.T) {
	t.Run("insert failure", func(t *testing.T) {
		repo := &s2WebDAVRepo{
			cameras:   []storage.CameraRow{{ID: "cam-1", Name: "camera-01"}},
			insertErr: errors.New("recording insert exploded"),
		}
		srv, root := s2Server(t, repo)
		require.NoError(t, os.MkdirAll(filepath.Join(root, "camera-01"), 0o755))
		ts := httptest.NewServer(srv.Handler())
		defer ts.Close()

		req, err := http.NewRequest(http.MethodPut, ts.URL+"/dav/camera-01/x.mp4", strings.NewReader("x"))
		require.NoError(t, err)
		resp, err := ts.Client().Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		require.Empty(t, repo.recordings)
	})

	t.Run("camera creation failure", func(t *testing.T) {
		repo := &s2WebDAVRepo{upsertErr: errors.New("camera create exploded")}
		srv, root := s2Server(t, repo)
		require.NoError(t, os.MkdirAll(filepath.Join(root, "unknown-cam"), 0o755))
		ts := httptest.NewServer(srv.Handler())
		defer ts.Close()

		req, err := http.NewRequest(http.MethodPut, ts.URL+"/dav/unknown-cam/x.mp4", strings.NewReader("x"))
		require.NoError(t, err)
		resp, err := ts.Client().Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		require.Empty(t, repo.recordings)
		require.Equal(t, 1, repo.upserts)
	})

	t.Run("no database configured", func(t *testing.T) {
		root := t.TempDir()
		store, err := storage.NewManager(root)
		require.NoError(t, err)
		srv := NewServer(store, "/dav", nil, nil, true)
		require.NoError(t, os.MkdirAll(filepath.Join(root, "camera-01"), 0o755))
		ts := httptest.NewServer(srv.Handler())
		defer ts.Close()

		req, err := http.NewRequest(http.MethodPut, ts.URL+"/dav/camera-01/x.mp4", strings.NewReader("x"))
		require.NoError(t, err)
		resp, err := ts.Client().Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		require.Contains(t, []int{http.StatusOK, http.StatusCreated, http.StatusNoContent}, resp.StatusCode)
	})
}
