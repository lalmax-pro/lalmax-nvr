package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/middleware"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

// setupRequest is the JSON body for POST /api/setup.
type setupRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Language string `json:"language,omitempty"`
	DataDir  string `json:"data_dir,omitempty"`
}

// validateSetupDataDir validates and prepares a server-side data directory.
// A browser cannot provide the server's absolute filesystem path via a folder
// picker, so the setup UI intentionally sends this as a text path.
func validateSetupDataDir(raw string) (string, error) {
	dataDir := filepath.Clean(strings.TrimSpace(raw))
	if dataDir == "." || dataDir == "" {
		return "", fmt.Errorf("data directory is required")
	}
	if !filepath.IsAbs(dataDir) {
		return "", fmt.Errorf("data directory must be an absolute server path")
	}
	if dataDir == filepath.Dir(dataDir) {
		return "", fmt.Errorf("data directory cannot be the filesystem root")
	}
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return "", fmt.Errorf("create data directory: %w", err)
	}

	probe, err := os.CreateTemp(dataDir, ".lalmax-nvr-write-test-*")
	if err != nil {
		return "", fmt.Errorf("data directory is not writable: %w", err)
	}
	probePath := probe.Name()
	if err := probe.Close(); err != nil {
		_ = os.Remove(probePath)
		return "", fmt.Errorf("check data directory: %w", err)
	}
	if err := os.Remove(probePath); err != nil {
		return "", fmt.Errorf("clean data directory test file: %w", err)
	}
	return dataDir, nil
}

func resolveSetupStorageRoot(cfg *config.Config) string {
	if cfg != nil && strings.TrimSpace(cfg.Storage.RootDir) != "" {
		return cfg.Storage.RootDir
	}
	if envDir := os.Getenv("NVR_DATA_DIR"); envDir != "" {
		return envDir
	}
	if info, err := os.Stat("/data"); err == nil && info.IsDir() {
		return "/data"
	}
	return "/var/lib/lalmax-nvr"
}

// handleSetup handles POST /api/setup — first-time initialization.
// Only succeeds when no password_hash is configured (SETUP_REQUIRED state).
func (h *Handler) handleSetup(w http.ResponseWriter, r *http.Request) {
	// Security: reject if auth is already configured
	if strings.TrimSpace(h.config.Auth.PasswordHash) != "" {
		writeError(w, http.StatusConflict, "setup already completed")
		return
	}

	var req setupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validate username
	if strings.TrimSpace(req.Username) == "" {
		writeError(w, http.StatusBadRequest, "username is required")
		return
	}

	// Validate password (same rule as CLI: min 8 chars)
	if len(req.Password) < 8 {
		writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}

	// An explicitly selected directory takes precedence over the current
	// bootstrap config. The legacy fallback keeps existing deployments working.
	dataDir := resolveSetupStorageRoot(h.config)
	explicitDataDir := strings.TrimSpace(req.DataDir) != ""
	if explicitDataDir {
		var err error
		dataDir, err = validateSetupDataDir(req.DataDir)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	// Hash password with bcrypt
	hash, err := middleware.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to hash password: %v", err))
		return
	}

	cfg := config.Config{
		Server:  config.ServerConfig{Listen: ":9090"},
		Storage: config.StorageConfig{RootDir: dataDir, SegmentDuration: "30s"},
		Auth:    config.AuthConfig{Username: req.Username, PasswordHash: hash},
		Cameras: []config.CameraConfig{},
		Cleanup: config.CleanupConfig{RetentionDays: 30, CheckInterval: "1h", DiskThresholdPercent: 95},
		FTP:     config.FTPConfig{Port: 2121, PassivePortRange: "2122-2140"},
		WebDAV:  config.WebDAVConfig{PathPrefix: "/dav"},
		Observability: config.ObservabilityConfig{
			LogLevel:  "info",
			LogFormat: "text",
		},
		Version: "1.0",
	}
	cfg.ApplyDefaults()

	// Keep the canonical config next to the database and recordings when a
	// directory was selected. The original config path is only a bootstrap
	// location and is followed by main() after the next restart.
	targetConfigPath := h.configPath
	if explicitDataDir {
		targetConfigPath = filepath.Join(dataDir, "lalmax-nvr.yaml")
		if err := initializeSetupDatabase(r.Context(), h, dataDir, req.Username, hash); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to initialize data directory: %v", err))
			return
		}
	}

	// Atomic save
	if err := config.Save(targetConfigPath, &cfg); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to save config: %v", err))
		return
	}
	if explicitDataDir && filepath.Clean(targetConfigPath) != filepath.Clean(h.configPath) {
		// This marker is not a second config/database/recordings location. It
		// only lets a restart launched with the old -config argument discover
		// the canonical config inside the selected data directory.
		if err := os.WriteFile(h.configPath+".target", []byte(targetConfigPath+"\n"), 0600); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to save config location: %v", err))
			return
		}
	}
	h.logOperation(r, "auth.setup", "auth", "", "success", "initial authentication configured", nil)

	// Update in-memory config so middleware picks up the new password hash
	h.config.Auth.Username = req.Username
	h.config.Auth.PasswordHash = hash
	h.config.Storage.RootDir = dataDir

	// Create super_admin user in the users table
	now := time.Now().UTC()
	dbUser := &model.User{
		Username:     req.Username,
		PasswordHash: hash,
		Role:         model.RoleSuperAdmin,
		DisplayName:  req.Username,
		Enabled:      true,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if !explicitDataDir {
		if err := h.db.CreateUser(r.Context(), dbUser); err != nil {
			logger.Error("failed to create super_admin user in DB", "error", err)
		}
	}

	// A selected data directory is opened by the next process after restart;
	// its super_admin was created by initializeSetupDatabase above.
	if explicitDataDir {
		h.configPath = targetConfigPath
	}

	// Generate basic auth token for auto-login
	token := base64.StdEncoding.EncodeToString([]byte(req.Username + ":" + req.Password))
	if explicitDataDir {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":           "ok",
			"token":            token,
			"data_dir":         dataDir,
			"config_path":      targetConfigPath,
			"restart_required": true,
		})
		h.requestRestart()
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status": "ok",
		"token":  token,
	})
}

func initializeSetupDatabase(ctx context.Context, h *Handler, dataDir, username, passwordHash string) error {
	if h != nil && filepath.Clean(dataDir) == filepath.Clean(resolveSetupStorageRoot(h.config)) {
		now := time.Now().UTC()
		return h.db.CreateUser(ctx, &model.User{
			Username:     username,
			PasswordHash: passwordHash,
			Role:         model.RoleSuperAdmin,
			DisplayName:  username,
			Enabled:      true,
			CreatedAt:    now,
			UpdatedAt:    now,
		})
	}
	targetDB, err := storage.New(filepath.Join(dataDir, "lalmax-nvr.db"))
	if err != nil {
		return err
	}
	defer targetDB.Close()
	if err := targetDB.Init(ctx); err != nil {
		return err
	}
	now := time.Now().UTC()
	if err := targetDB.CreateUser(ctx, &model.User{
		Username:     username,
		PasswordHash: passwordHash,
		Role:         model.RoleSuperAdmin,
		DisplayName:  username,
		Enabled:      true,
		CreatedAt:    now,
		UpdatedAt:    now,
	}); err != nil {
		return err
	}
	return nil
}
