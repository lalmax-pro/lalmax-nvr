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
