package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/middleware"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

func setupTestHandlerForSetup(t *testing.T) (*Handler, string) {
	t.Helper()
	db, store := setupTestDB(t)
	cfgPath := filepath.Join(t.TempDir(), "test-config.yaml")
	err := os.WriteFile(cfgPath, []byte("version: \"1.0\"\n"), 0644)
	require.NoError(t, err)
	cfg := &config.Config{Version: "1.0"}
	h := NewHandler(db, store, noopAuthMW(), cfg, nil, cfgPath, nil, nil)
	return h, cfgPath
}

func TestHandleSetup_Success(t *testing.T) {
	t.Parallel()
	h, cfgPath := setupTestHandlerForSetup(t)

	body := setupRequest{Username: "admin", Password: "testpassword123"}
	b, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/setup", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.handleSetup(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]string
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.Equal(t, "ok", resp["status"])
	require.NotEmpty(t, resp["token"])

	// Verify config file was written with password_hash
	saved, err := config.Load(cfgPath)
	require.NoError(t, err)
	require.Equal(t, "admin", saved.Auth.Username)
	require.NotEmpty(t, saved.Auth.PasswordHash)

	// Verify in-memory config updated
	require.Equal(t, "admin", h.config.Auth.Username)
	require.NotEmpty(t, h.config.Auth.PasswordHash)
}

func TestHandleSetup_AlreadyConfigured(t *testing.T) {
	t.Parallel()
	h, _ := setupTestHandlerForSetup(t)

	h.config.Auth.PasswordHash = "$2a$10$somehash"

	body := setupRequest{Username: "admin", Password: "testpassword123"}
	b, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/setup", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.handleSetup(rec, req)

	require.Equal(t, http.StatusConflict, rec.Code)
}

func TestHandleSetup_ShortPassword(t *testing.T) {
	t.Parallel()
	h, _ := setupTestHandlerForSetup(t)

	body := setupRequest{Username: "admin", Password: "short"}
	b, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/setup", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.handleSetup(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleSetup_EmptyUsername(t *testing.T) {
	t.Parallel()
	h, _ := setupTestHandlerForSetup(t)

	body := setupRequest{Username: "", Password: "testpassword123"}
	b, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/setup", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.handleSetup(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleSetup_InvalidJSON(t *testing.T) {
	t.Parallel()
	h, _ := setupTestHandlerForSetup(t)

	req := httptest.NewRequest(http.MethodPost, "/api/setup", bytes.NewReader([]byte("not json")))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.handleSetup(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleSetupDirectories_DefaultPath(t *testing.T) {
	t.Parallel()
	h, _ := setupTestHandlerForSetup(t)

	req := httptest.NewRequest(http.MethodGet, "/api/setup/directories", nil)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		Path    string `json:"path"`
		Entries []struct {
			Name string `json:"name"`
			Path string `json:"path"`
		} `json:"entries"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.NotEmpty(t, resp.Path)
}

func TestHandleSetup_UsesExistingStorageRoot(t *testing.T) {
	t.Parallel()
	h, cfgPath := setupTestHandlerForSetup(t)
	h.config.Storage.RootDir = "/tmp/existing-nvr-data"

	body := setupRequest{Username: "admin", Password: "testpassword123"}
	b, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/setup", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.handleSetup(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	saved, err := config.Load(cfgPath)
	require.NoError(t, err)
	require.Equal(t, "/tmp/existing-nvr-data", saved.Storage.RootDir)
}

func TestHandleSetup_SelectedDataDirectory(t *testing.T) {
	t.Parallel()
	h, cfgPath := setupTestHandlerForSetup(t)
	dataDir := t.TempDir()

	body := setupRequest{Username: "admin", Password: "testpassword123", DataDir: dataDir}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/setup", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.handleSetup(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		RestartRequired bool   `json:"restart_required"`
		ConfigPath      string `json:"config_path"`
		DataDir         string `json:"data_dir"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.True(t, resp.RestartRequired)
	require.Equal(t, dataDir, resp.DataDir)
	require.Equal(t, filepath.Join(dataDir, "lalmax-nvr.yaml"), resp.ConfigPath)

	saved, err := config.Load(resp.ConfigPath)
	require.NoError(t, err)
	require.Equal(t, dataDir, saved.Storage.RootDir)
	require.NotEmpty(t, saved.Auth.PasswordHash)

	marker, err := os.ReadFile(cfgPath + ".target")
	require.NoError(t, err)
	require.Equal(t, resp.ConfigPath+"\n", string(marker))

	targetDB, err := storage.New(filepath.Join(dataDir, "lalmax-nvr.db"))
	require.NoError(t, err)
	defer targetDB.Close()
	user, err := targetDB.GetUserByUsername(req.Context(), "admin")
	require.NoError(t, err)
	require.NotNil(t, user)
}

func TestHandleSetup_TokenIsValid(t *testing.T) {
	t.Parallel()
	h, _ := setupTestHandlerForSetup(t)

	username := "testuser"
	password := "securepassword123"
	body := setupRequest{Username: username, Password: password}
	b, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/api/setup", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.handleSetup(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var resp map[string]string
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))

	// Verify token matches expected BasicAuth encoding
	decoded, err := base64.StdEncoding.DecodeString(resp["token"])
	require.NoError(t, err)
	require.Equal(t, username+":"+password, string(decoded))

	// Verify the hashed password actually validates via bcrypt
	require.True(t, middleware.CheckPassword(password, h.config.Auth.PasswordHash))
}
