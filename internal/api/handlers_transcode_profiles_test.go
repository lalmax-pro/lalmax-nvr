package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/lalmax-pro/lalmax-nvr/internal/transcode"
	"github.com/stretchr/testify/require"
)

func TestTranscodeProfileCRUD(t *testing.T) {
	db, err := storage.New(filepath.Join(t.TempDir(), "profiles.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.Init(context.Background()))
	h := &Handler{db: db}
	router := chi.NewRouter()
	router.Post("/profiles", h.handleSaveTranscodeProfile)
	router.Patch("/profiles/{id}", h.handleSaveTranscodeProfile)
	router.Delete("/profiles/{id}", h.handleDeleteTranscodeProfile)
	p := transcode.DefaultProfile("ffmpeg")
	p.Revision = 0
	body, err := json.Marshal(p)
	require.NoError(t, err)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/profiles", strings.NewReader(string(body))))
	require.Equal(t, 201, w.Code, w.Body.String())
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/profiles", strings.NewReader(string(body))))
	require.Equal(t, 409, w.Code)
	p.Revision = 1
	p.Name = "Updated"
	body, err = json.Marshal(p)
	require.NoError(t, err)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("PATCH", "/profiles/"+p.ID, strings.NewReader(string(body))))
	require.Equal(t, 200, w.Code)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("DELETE", "/profiles/"+p.ID+"?revision=1", nil))
	require.Equal(t, 409, w.Code)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("DELETE", "/profiles/"+p.ID+"?revision=2", nil))
	require.Equal(t, 204, w.Code)
}
