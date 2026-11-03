package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/middleware"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestHomeAssistantPTZScopePermissionAndValidation(t *testing.T) {
	db, store := setupTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.UpsertCamera(ctx, "ptz-camera", "PTZ", "onvif", "h264", "", "", "", false, "http://127.0.0.1/device", "profile", ""))
	require.NoError(t, db.BindStreamToCamera(ctx, "published", "ptz-camera"))
	cfg, err := db.GetHomeAssistantSettings(ctx)
	require.NoError(t, err)
	cfg.Enabled = true
	cfg.Streams = []storage.HomeAssistantPublication{{StreamID: "published"}}
	require.NoError(t, db.SaveHomeAssistantSettings(ctx, cfg))
	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	path := "/api/integrations/home-assistant/streams/published/ptz"
	for _, body := range []string{`{"mode":"unknown"}`, `{"mode":"continuous","pan":2}`, `{"mode":"continuous","duration":20}`, `{"mode":"goto_preset"}`, `{"mode":"absolute","zoom":-1}`, `{"mode":"goto_preset","preset_token":"<xml>"}`} {
		require.Equal(t, 400, doRequest(t, h.Routes(), "POST", path, strings.NewReader(body), "", "").Code, body)
	}
	require.Equal(t, 404, doRequest(t, h.Routes(), "POST", strings.Replace(path, "published", "hidden", 1), strings.NewReader(`{"mode":"stop"}`), "", "").Code)
	h.SetMultiUserAuthMW(middleware.InjectUser(&model.User{Role: model.RoleUser}))
	require.Equal(t, http.StatusForbidden, doRequest(t, h.Routes(), "POST", path, strings.NewReader(`{"mode":"stop"}`), "", "").Code)
}

func TestHomeAssistantXiaomiPTZ(t *testing.T) {
	db, store := setupTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.UpsertCamera(ctx, "mi", "Camera", "xiaomi", "h264", "xiaomi://1", "", "", true, "", "", ""))
	require.NoError(t, db.BindStreamToCamera(ctx, "published", "mi"))
	api3HAPublish(t, db, "", storage.HomeAssistantPublication{StreamID: "published", AppName: "live"})
	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	var directions []string
	h.xiaomiMotorDispatch = func(_ context.Context, id, direction string, speed int) error {
		require.Equal(t, "mi", id)
		directions = append(directions, direction)
		return nil
	}
	path := "/api/integrations/home-assistant/streams/published/ptz"
	rr := doRequest(t, h.Routes(), http.MethodPost, path, strings.NewReader(`{"mode":"continuous","pan":0.5,"duration":0.6}`), "", "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Equal(t, []string{"right", "right", "stop"}, directions)
	for _, body := range []string{`{"mode":"absolute","pan":1}`, `{"mode":"continuous","zoom":1}`} {
		rr = doRequest(t, h.Routes(), http.MethodPost, path, strings.NewReader(body), "", "")
		require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	}
	rr = doRequest(t, h.Routes(), http.MethodGet, path+"/presets", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), `"presets":[]`)
}
