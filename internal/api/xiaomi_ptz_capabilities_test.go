package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestXiaomiPTZCapabilities(t *testing.T) {
	db, store := setupTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.UpsertCamera(ctx, "mi", "Camera", "xiaomi", "h264", "xiaomi://1", "", "", true, "", "", ""))
	require.NoError(t, db.UpsertCamera(ctx, "other", "Camera", "onvif", "h264", "", "", "", true, "", "", ""))
	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	get := func(id string) *httptest.ResponseRecorder {
		return doRequest(t, h.Routes(), http.MethodGet, "/api/cameras/"+id+"/ptz/capabilities", nil, "", "")
	}
	rr := get("mi")
	require.Equal(t, http.StatusOK, rr.Code)
	require.JSONEq(t, `{"model":"","control":"unknown","source":"unknown","device_verified":false}`, rr.Body.String())
	require.NoError(t, db.UpdateCameraMetadata(ctx, "mi", "", "", "", "isa.camera.hlc8a", "", 0))
	rr = get("mi")
	require.Equal(t, http.StatusOK, rr.Code)
	require.JSONEq(t, `{"model":"isa.camera.hlc8a","control":"fixed_step","source":"model","device_verified":false}`, rr.Body.String())
	require.Equal(t, http.StatusBadRequest, get("other").Code)
	require.Equal(t, http.StatusNotFound, get("missing").Code)
}
