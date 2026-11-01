package upload

import (
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

// s2UploadRepo is a scriptable storage.UploadRepository.
type s2UploadRepo struct {
	camera     *storage.CameraRow
	getErr     error
	insertErr  error
	recordings []*model.Recording
}

func (r *s2UploadRepo) GetCamera(context.Context, string) (*storage.CameraRow, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	return r.camera, nil
}

func (r *s2UploadRepo) UpsertCamera(context.Context, string, string, string, string, string, string, string, bool, string, string, string, ...string) error {
	return nil
}

func (r *s2UploadRepo) UpdateCameraMetadata(context.Context, string, string, string, string, string, string, int) error {
	return nil
}

func (r *s2UploadRepo) UpdateCameraProfileName(context.Context, string, string) error { return nil }
func (r *s2UploadRepo) UpdateCameraActivation(context.Context, string, string) error  { return nil }
func (r *s2UploadRepo) UpdateCameraStableID(context.Context, string, string) error    { return nil }
func (r *s2UploadRepo) SaveCameraExtras(context.Context, config.CameraConfig) error   { return nil }
func (r *s2UploadRepo) ListCameraConfigs(context.Context) ([]config.CameraConfig, error) {
	return nil, nil
}

func (r *s2UploadRepo) InsertRecording(_ context.Context, recording *model.Recording) error {
	if r.insertErr != nil {
		return r.insertErr
	}
	r.recordings = append(r.recordings, recording)
	return nil
}

func (r *s2UploadRepo) InsertRecordingWithRetry(ctx context.Context, recording *model.Recording, _ int, _ time.Duration) error {
	return r.InsertRecording(ctx, recording)
}

// s2FailReader always fails, simulating a broken request body.
type s2FailReader struct{}

func (s2FailReader) Read([]byte) (int, error) { return 0, errors.New("body exploded") }

func s2UploadEnv(t *testing.T, repo storage.UploadRepository, maxSize int64) (*Handler, *storage.Manager) {
	t.Helper()
	mgr, err := storage.NewManager(t.TempDir())
	require.NoError(t, err)
	return NewHandler(mgr, repo, maxSize), mgr
}

func TestS2UploadJPEGErrors(t *testing.T) {
	t.Run("camera lookup failure", func(t *testing.T) {
		repo := &s2UploadRepo{getErr: errors.New("db down")}
		h, _ := s2UploadEnv(t, repo, 1024)
		req := httptest.NewRequest(http.MethodPost, "/api/upload/cam1", bytes.NewReader(testJPEG))
		req.Header.Set("Content-Type", "image/jpeg")
		rec := httptest.NewRecorder()
		newRouter(h).ServeHTTP(rec, req)
		require.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("unreadable body", func(t *testing.T) {
		repo := &s2UploadRepo{camera: &storage.CameraRow{ID: "cam1"}}
		h, _ := s2UploadEnv(t, repo, 1024)
		req := httptest.NewRequest(http.MethodPost, "/api/upload/cam1", s2FailReader{})
		req.Header.Set("Content-Type", "image/jpeg")
		rec := httptest.NewRecorder()
		newRouter(h).ServeHTTP(rec, req)
		require.Equal(t, http.StatusInternalServerError, rec.Code)
		require.Contains(t, rec.Body.String(), "failed to read request body")
	})

	t.Run("segment creation failure", func(t *testing.T) {
		root := t.TempDir()
		mgr, err := storage.NewManager(root)
		require.NoError(t, err)
		require.NoError(t, os.Chmod(root, 0o500))
		t.Cleanup(func() { _ = os.Chmod(root, 0o755) })

		repo := &s2UploadRepo{camera: &storage.CameraRow{ID: "cam1"}}
		h := NewHandler(mgr, repo, 1024)
		req := httptest.NewRequest(http.MethodPost, "/api/upload/cam1", bytes.NewReader(testJPEG))
		req.Header.Set("Content-Type", "image/jpeg")
		rec := httptest.NewRecorder()
		newRouter(h).ServeHTTP(rec, req)
		require.Equal(t, http.StatusInternalServerError, rec.Code)
		require.Contains(t, rec.Body.String(), "failed to create segment")
	})

	t.Run("recording insert failure", func(t *testing.T) {
		repo := &s2UploadRepo{camera: &storage.CameraRow{ID: "cam1"}, insertErr: errors.New("insert exploded")}
		h, _ := s2UploadEnv(t, repo, 1024)
		req := httptest.NewRequest(http.MethodPost, "/api/upload/cam1", bytes.NewReader(testJPEG))
		req.Header.Set("Content-Type", "image/jpeg")
		rec := httptest.NewRecorder()
		newRouter(h).ServeHTTP(rec, req)
		require.Equal(t, http.StatusInternalServerError, rec.Code)
		require.Contains(t, rec.Body.String(), "failed to save recording metadata")
		require.Empty(t, repo.recordings)
	})
}

func TestS2UploadVideoErrors(t *testing.T) {
	t.Run("unreadable body", func(t *testing.T) {
		repo := &s2UploadRepo{camera: &storage.CameraRow{ID: "cam1"}}
		h, _ := s2UploadEnv(t, repo, 1024)
		req := httptest.NewRequest(http.MethodPost, "/api/upload/cam1/video", s2FailReader{})
		req.Header.Set("Content-Type", "video/mp4")
		rec := httptest.NewRecorder()
		newRouter(h).ServeHTTP(rec, req)
		require.Equal(t, http.StatusInternalServerError, rec.Code)
		require.Contains(t, rec.Body.String(), "failed to read request body")
	})

	t.Run("oversized upload", func(t *testing.T) {
		repo := &s2UploadRepo{camera: &storage.CameraRow{ID: "cam1"}}
		h, _ := s2UploadEnv(t, repo, 4)
		req := httptest.NewRequest(http.MethodPost, "/api/upload/cam1/video", bytes.NewReader([]byte("0123456789")))
		req.Header.Set("Content-Type", "video/mp4")
		rec := httptest.NewRecorder()
		newRouter(h).ServeHTTP(rec, req)
		require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	})

	t.Run("segment creation failure", func(t *testing.T) {
		root := t.TempDir()
		mgr, err := storage.NewManager(root)
		require.NoError(t, err)
		require.NoError(t, os.Chmod(root, 0o500))
		t.Cleanup(func() { _ = os.Chmod(root, 0o755) })

		repo := &s2UploadRepo{camera: &storage.CameraRow{ID: "cam1"}}
		h := NewHandler(mgr, repo, 1024)
		req := httptest.NewRequest(http.MethodPost, "/api/upload/cam1/video", bytes.NewReader([]byte("mp4")))
		req.Header.Set("Content-Type", "video/mp4")
		rec := httptest.NewRecorder()
		newRouter(h).ServeHTTP(rec, req)
		require.Equal(t, http.StatusInternalServerError, rec.Code)
		require.Contains(t, rec.Body.String(), "failed to create segment")
	})

	t.Run("recording insert failure", func(t *testing.T) {
		repo := &s2UploadRepo{camera: &storage.CameraRow{ID: "cam1"}, insertErr: errors.New("insert exploded")}
		h, _ := s2UploadEnv(t, repo, 1024)
		req := httptest.NewRequest(http.MethodPost, "/api/upload/cam1/video", bytes.NewReader([]byte("mp4")))
		req.Header.Set("Content-Type", "video/mp4")
		rec := httptest.NewRecorder()
		newRouter(h).ServeHTTP(rec, req)
		require.Equal(t, http.StatusInternalServerError, rec.Code)
		require.Contains(t, rec.Body.String(), "failed to save recording metadata")
	})
}

func TestS2UploadBatchErrors(t *testing.T) {
	t.Run("unsupported content type", func(t *testing.T) {
		repo := &s2UploadRepo{camera: &storage.CameraRow{ID: "cam1"}}
		h, _ := s2UploadEnv(t, repo, 1024)
		req := httptest.NewRequest(http.MethodPost, "/api/upload/cam1/batch", bytes.NewReader(nil))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		newRouter(h).ServeHTTP(rec, req)
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Contains(t, rec.Body.String(), "expected multipart/form-data")
	})

	t.Run("unparsable multipart body", func(t *testing.T) {
		repo := &s2UploadRepo{camera: &storage.CameraRow{ID: "cam1"}}
		h, _ := s2UploadEnv(t, repo, 1024)
		req := httptest.NewRequest(http.MethodPost, "/api/upload/cam1/batch", bytes.NewReader([]byte("not-multipart")))
		req.Header.Set("Content-Type", "multipart/form-data; boundary=xyz")
		rec := httptest.NewRecorder()
		newRouter(h).ServeHTTP(rec, req)
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Contains(t, rec.Body.String(), "failed to parse multipart form")
	})

	t.Run("no file section", func(t *testing.T) {
		repo := &s2UploadRepo{camera: &storage.CameraRow{ID: "cam1"}}
		h, _ := s2UploadEnv(t, repo, 1024)
		req := httptest.NewRequest(http.MethodPost, "/api/upload/cam1/batch", bytes.NewReader([]byte("x")))
		req.Header.Set("Content-Type", "multipart/form-data; boundary=xyz")
		req.MultipartForm = &multipart.Form{}
		rec := httptest.NewRecorder()
		newRouter(h).ServeHTTP(rec, req)
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Contains(t, rec.Body.String(), "no files in upload")
	})

	t.Run("no frames field", func(t *testing.T) {
		repo := &s2UploadRepo{camera: &storage.CameraRow{ID: "cam1"}}
		h, _ := s2UploadEnv(t, repo, 1024)
		req := httptest.NewRequest(http.MethodPost, "/api/upload/cam1/batch", bytes.NewReader([]byte("x")))
		req.Header.Set("Content-Type", "multipart/form-data; boundary=xyz")
		req.MultipartForm = &multipart.Form{
			Value: map[string][]string{"other": {"v"}},
			File:  map[string][]*multipart.FileHeader{"other": {{Filename: "a.jpg"}}},
		}
		rec := httptest.NewRecorder()
		newRouter(h).ServeHTTP(rec, req)
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Contains(t, rec.Body.String(), "no frames found in upload")
	})

	t.Run("segment creation failure", func(t *testing.T) {
		root := t.TempDir()
		mgr, err := storage.NewManager(root)
		require.NoError(t, err)
		require.NoError(t, os.Chmod(root, 0o500))
		t.Cleanup(func() { _ = os.Chmod(root, 0o755) })

		repo := &s2UploadRepo{camera: &storage.CameraRow{ID: "cam1"}}
		h := NewHandler(mgr, repo, 1024)
		req := httptest.NewRequest(http.MethodPost, "/api/upload/cam1/batch", bytes.NewReader([]byte("x")))
		req.Header.Set("Content-Type", "multipart/form-data; boundary=xyz")
		req.MultipartForm = &multipart.Form{
			File: map[string][]*multipart.FileHeader{"frames": {{Filename: "a.jpg"}}},
		}
		rec := httptest.NewRecorder()
		newRouter(h).ServeHTTP(rec, req)
		require.Equal(t, http.StatusInternalServerError, rec.Code)
		require.Contains(t, rec.Body.String(), "failed to create segment")
	})

	t.Run("frame file cannot be opened", func(t *testing.T) {
		repo := &s2UploadRepo{camera: &storage.CameraRow{ID: "cam1"}}
		h, _ := s2UploadEnv(t, repo, 1024)
		req := httptest.NewRequest(http.MethodPost, "/api/upload/cam1/batch", bytes.NewReader([]byte("x")))
		req.Header.Set("Content-Type", "multipart/form-data; boundary=xyz")
		req.MultipartForm = &multipart.Form{
			File: map[string][]*multipart.FileHeader{"frames": {{Filename: "gone.jpg"}}},
		}
		rec := httptest.NewRecorder()
		newRouter(h).ServeHTTP(rec, req)
		require.Equal(t, http.StatusInternalServerError, rec.Code)
		require.Contains(t, rec.Body.String(), "failed to open uploaded file")
	})

	t.Run("recording insert failure", func(t *testing.T) {
		repo := &s2UploadRepo{camera: &storage.CameraRow{ID: "cam1"}, insertErr: errors.New("insert exploded")}
		h, _ := s2UploadEnv(t, repo, 1<<20)

		var buf bytes.Buffer
		writer := multipart.NewWriter(&buf)
		part, err := writer.CreateFormFile("frames", "frame.jpg")
		require.NoError(t, err)
		_, err = part.Write(testJPEG)
		require.NoError(t, err)
		require.NoError(t, writer.Close())

		req := httptest.NewRequest(http.MethodPost, "/api/upload/cam1/batch", &buf)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		rec := httptest.NewRecorder()
		newRouter(h).ServeHTTP(rec, req)
		require.Equal(t, http.StatusInternalServerError, rec.Code)
		require.Contains(t, rec.Body.String(), "failed to save recording metadata")
	})
}
