package api

import (
	"net/http"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/stretchr/testify/require"
)

func TestCameraProtocols_SubFallsBackToMain(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	seedCameraWithEncoding(t, db, "cam1", "h264")

	reg := NewStreamRegistry()
	reg.Register(&HLSStreamHandler{})
	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetStreamRegistry(reg)

	rr := doRequest(t, h.Routes(), "GET", "/api/cameras/cam1/protocols?quality=sub", nil, "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "main", rr.Header().Get("X-Stream-Quality"))
}

func TestAutoDiscoverSettings_Get(t *testing.T) {
	t.Parallel()
	db, store := setupTestDB(t)
	defer db.Close()
	cfg := &config.Config{}
	cfg.AutoDiscover.ScanInterval = "60s"
	h := NewHandler(db, store, noopAuthMW(), cfg, nil, "", nil, nil)
	rr := doRequest(t, h.Routes(), "GET", "/api/settings/auto-discover", nil, "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code)
}

func TestIsValidRecordingModeEventAdaptive(t *testing.T) {
	require.True(t, isValidRecordingMode("event"))
	require.True(t, isValidRecordingMode("adaptive"))
	require.False(t, isValidRecordingMode("nope"))
}
