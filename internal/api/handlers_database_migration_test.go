package api

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/stretchr/testify/require"
)

func TestDatabaseMigrationSettingsDoNotReturnDSN(t *testing.T) {
	cfg := &config.Config{Storage: config.StorageConfig{MigrationTarget: &config.DatabaseTargetConfig{
		Driver: "mysql",
		DSN:    "nvr:do-not-return@tcp(127.0.0.1:53306)/nvr",
	}}}
	h := &Handler{config: cfg}
	response := httptest.NewRecorder()
	h.handleGetDatabaseMigrationSettings(response, httptest.NewRequest("GET", "/api/settings/database-migration", nil))
	require.Equal(t, 200, response.Code)
	require.NotContains(t, response.Body.String(), "do-not-return")
	require.Contains(t, response.Body.String(), `"target_configured":true`)
	require.Contains(t, response.Body.String(), `"migration_available":true`)
	require.Contains(t, response.Body.String(), `"active_driver":"sqlite"`)
}

func TestRunDatabaseMigrationRequiresConfiguredTarget(t *testing.T) {
	h := &Handler{config: &config.Config{}}
	response := httptest.NewRecorder()
	h.handleRunDatabaseMigration(response, httptest.NewRequest("POST", "/api/settings/database-migration/run", nil))
	require.Equal(t, 400, response.Code)
}

func TestDatabaseMigrationTargetEndpointConnectsToDocker(t *testing.T) {
	for _, tc := range []struct {
		name   string
		driver string
		env    string
	}{
		{name: "postgres", driver: "postgres", env: "LALMAX_TEST_POSTGRES_DSN"},
		{name: "mysql", driver: "mysql", env: "LALMAX_TEST_MYSQL_DSN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dsn := os.Getenv(tc.env)
			if dsn == "" {
				t.Skipf("set %s to test the %s migration connection", tc.env, tc.name)
			}
			h := &Handler{config: &config.Config{}}
			body, err := json.Marshal(databaseMigrationTargetRequest{Driver: tc.driver, DSN: dsn})
			require.NoError(t, err)
			request := httptest.NewRequest("POST", "/api/settings/database-migration/test", bytes.NewReader(body))
			response := httptest.NewRecorder()
			h.handleTestDatabaseMigrationTarget(response, request)
			require.Equal(t, 200, response.Code, response.Body.String())
			require.Contains(t, response.Body.String(), `"status":"connected"`)
		})
	}
}

func TestDatabaseMigrationTargetCanBeTestedAndSaved(t *testing.T) {
	cfg := &config.Config{}
	h := &Handler{config: cfg, configPath: filepath.Join(t.TempDir(), "config.yaml")}
	body, err := json.Marshal(databaseMigrationTargetRequest{Driver: "sqlite", DSN: ":memory:"})
	require.NoError(t, err)
	request := httptest.NewRequest("POST", "/api/settings/database-migration/test", bytes.NewReader(body))
	response := httptest.NewRecorder()
	h.handleTestDatabaseMigrationTarget(response, request)
	require.Equal(t, 200, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), `"status":"connected"`)

	request = httptest.NewRequest("PUT", "/api/settings/database-migration", bytes.NewReader(body))
	response = httptest.NewRecorder()
	h.handleUpdateDatabaseMigrationSettings(response, request)
	require.Equal(t, 200, response.Code, response.Body.String())
	require.Equal(t, "sqlite", cfg.Storage.MigrationTarget.Driver)
	require.Equal(t, ":memory:", cfg.Storage.MigrationTarget.DSN)
}
