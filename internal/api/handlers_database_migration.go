package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

type databaseMigrationTargetRequest struct {
	Driver string `json:"driver"`
	DSN    string `json:"dsn"`
}

func (h *Handler) handleGetDatabaseMigrationSettings(w http.ResponseWriter, _ *http.Request) {
	target := h.config.Storage.MigrationTarget
	activeDriver := strings.TrimSpace(h.config.Storage.DatabaseDriver)
	if activeDriver == "" {
		activeDriver = string(storage.DriverSQLite)
	}
	migrationAvailable := target != nil && strings.TrimSpace(target.DSN) != "" && (target.Driver == string(storage.DriverPostgres) || target.Driver == string(storage.DriverMySQL)) && activeDriver == string(storage.DriverSQLite)
	migrationReason := "configure and test an empty PostgreSQL or MySQL destination to begin migration"
	if migrationAvailable {
		migrationReason = "ready; database operations pause briefly while data is copied and verified"
	} else if activeDriver != string(storage.DriverSQLite) {
		migrationReason = "online migration currently supports SQLite as the source database"
	}
	response := map[string]any{
		"active_driver":       activeDriver,
		"migration_available": migrationAvailable,
		"migration_reason":    migrationReason,
		"target_configured":   target != nil && strings.TrimSpace(target.DSN) != "",
	}
	if target != nil {
		response["target_driver"] = target.Driver
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) handleUpdateDatabaseMigrationSettings(w http.ResponseWriter, r *http.Request) {
	var req databaseMigrationTargetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	driver := strings.ToLower(strings.TrimSpace(req.Driver))
	if driver != string(storage.DriverPostgres) && driver != string(storage.DriverMySQL) && driver != string(storage.DriverSQLite) {
		writeError(w, http.StatusBadRequest, "driver must be sqlite, postgres, or mysql")
		return
	}
	dsn := strings.TrimSpace(req.DSN)
	if dsn == "" && h.config.Storage.MigrationTarget != nil {
		dsn = h.config.Storage.MigrationTarget.DSN
	}
	if dsn == "" {
		writeError(w, http.StatusBadRequest, "database connection string is required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	err := testDatabaseTarget(ctx, driver, dsn)
	cancel()
	if err != nil {
		writeError(w, http.StatusBadRequest, "database connection failed; verify the driver, address, credentials, and network access")
		return
	}
	previous := h.config.Storage.MigrationTarget
	h.config.Storage.MigrationTarget = &config.DatabaseTargetConfig{Driver: driver, DSN: dsn}
	if !h.saveDatabaseMigrationConfig(w) {
		h.config.Storage.MigrationTarget = previous
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "saved", "target_driver": driver, "target_configured": true})
}

func (h *Handler) saveDatabaseMigrationConfig(w http.ResponseWriter) bool {
	var err error
	if h.configWatcher != nil {
		err = h.configWatcher.Save(false)
	} else {
		err = config.Save(h.configPath, h.config)
	}
	if err == nil {
		return true
	}
	if err == config.ErrConfigModified {
		writeError(w, http.StatusConflict, "config file was modified externally; please reload before saving")
		return false
	}
	logger.Warn("failed to save database migration config", "error", err)
	writeError(w, http.StatusInternalServerError, "failed to save database migration config")
	return false
}

func (h *Handler) handleTestDatabaseMigrationTarget(w http.ResponseWriter, r *http.Request) {
	var req databaseMigrationTargetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	driver := strings.ToLower(strings.TrimSpace(req.Driver))
	dsn := strings.TrimSpace(req.DSN)
	if driver == "" && h.config.Storage.MigrationTarget != nil {
		driver = h.config.Storage.MigrationTarget.Driver
	}
	if dsn == "" && h.config.Storage.MigrationTarget != nil {
		dsn = h.config.Storage.MigrationTarget.DSN
	}
	if dsn == "" {
		writeError(w, http.StatusBadRequest, "database connection string is required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	if err := testDatabaseTarget(ctx, driver, dsn); err != nil {
		// Driver errors may include connection details. Keep credentials out of API responses.
		writeError(w, http.StatusBadRequest, "database connection failed; verify the driver, address, credentials, and network access")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":              "connected",
		"driver":              driver,
		"migration_available": driver == string(storage.DriverPostgres) || driver == string(storage.DriverMySQL),
		"migration_reason":    "connection verified; migration requires an empty destination database",
	})
}

func (h *Handler) handleRunDatabaseMigration(w http.ResponseWriter, r *http.Request) {
	target := h.config.Storage.MigrationTarget
	if target == nil || strings.TrimSpace(target.DSN) == "" {
		writeError(w, http.StatusBadRequest, "configure and test a migration destination first")
		return
	}
	if target.Driver != string(storage.DriverPostgres) && target.Driver != string(storage.DriverMySQL) {
		writeError(w, http.StatusBadRequest, "migration destination must be PostgreSQL or MySQL")
		return
	}
	activeDriver := strings.TrimSpace(h.config.Storage.DatabaseDriver)
	if activeDriver != "" && activeDriver != string(storage.DriverSQLite) {
		writeError(w, http.StatusConflict, "online migration currently supports SQLite as the source database")
		return
	}
	migrator, ok := h.db.(interface {
		MigrateTo(context.Context, storage.DatabaseDriver, string, func() error) (storage.MigrationSummary, error)
	})
	if !ok {
		writeError(w, http.StatusNotImplemented, "the active storage adapter does not support live database migration")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	previousDriver, previousDSN := h.config.Storage.DatabaseDriver, h.config.Storage.DatabaseDSN
	summary, err := migrator.MigrateTo(ctx, storage.DatabaseDriver(target.Driver), target.DSN, func() error {
		h.config.Storage.DatabaseDriver = target.Driver
		h.config.Storage.DatabaseDSN = target.DSN
		if h.configWatcher != nil {
			err := h.configWatcher.Save(false)
			if err != nil {
				h.config.Storage.DatabaseDriver = previousDriver
				h.config.Storage.DatabaseDSN = previousDSN
			}
			return err
		}
		if err := config.Save(h.configPath, h.config); err != nil {
			h.config.Storage.DatabaseDriver = previousDriver
			h.config.Storage.DatabaseDSN = previousDSN
			return err
		}
		return nil
	})
	if err != nil {
		logger.Warn("database migration failed", "error", err)
		writeError(w, http.StatusConflict, "database migration failed; the current database remains active")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "migrated", "active_driver": string(summary.DestinationDriver),
		"total_rows": summary.TotalRows, "rows_copied": summary.RowsCopied,
	})
}

func testDatabaseTarget(ctx context.Context, driver, dsn string) error {
	db, err := storage.Open(ctx, storage.DatabaseOptions{Driver: storage.DatabaseDriver(driver), DSN: dsn})
	if err != nil {
		return err
	}
	return db.Close()
}
