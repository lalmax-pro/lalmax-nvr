package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/xiaomi"
	"github.com/stretchr/testify/require"
)

func TestXiaomiUnifiedPTZRouting(t *testing.T) {
	t.Helper()
	db, store := setupPTZTestDB(t)
	require.NoError(t, db.UpsertCamera(context.Background(), "mi-cam", "Camera", "xiaomi", "h265", "xiaomi://1", "", "", true, "", "", ""))
	h := TestHandler(db, store)
	var direction string
	var speed int
	calls := 0
	h.xiaomiMotorDispatch = func(ctx context.Context, id, dir string, s int) error {
		require.Equal(t, "mi-cam", id)
		require.NoError(t, ctx.Err())
		direction, speed = dir, s
		calls++
		return nil
	}
	for _, tc := range []struct {
		body, dir string
		speed     int
	}{
		{`{"mode":"continuous","pan":-0.5}`, "left", 5},
		{`{"mode":"continuous","pan":1}`, "right", 10},
		{`{"mode":"continuous","tilt":0.25}`, "up", 3},
		{`{"mode":"continuous","tilt":-0.5}`, "down", 5},
		{`{"mode":"continuous","pan":0,"tilt":0}`, "stop", 0},
		{`{"mode":"continuous","pan":0.2,"tilt":-0.8}`, "down", 8},
	} {
		rr := doRequest(t, h.Routes(), http.MethodPost, "/api/cameras/mi-cam/ptz/move", strings.NewReader(tc.body), "", "")
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		require.Equal(t, tc.dir, direction)
		require.Equal(t, tc.speed, speed)
	}
	before := calls
	for _, body := range []string{`{"mode":"absolute","pan":0.5}`, `{"mode":"relative","tilt":0.5}`, `{"mode":"continuous","zoom":0.1}`, `{"mode":"continuous","pan":2}`, `{"mode":"continuous","tilt":-2}`} {
		rr := doRequest(t, h.Routes(), http.MethodPost, "/api/cameras/mi-cam/ptz/move", strings.NewReader(body), "", "")
		require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	}
	require.Equal(t, before, calls)
	rr := doRequest(t, h.Routes(), http.MethodPost, "/api/cameras/mi-cam/ptz/stop", nil, "", "")
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "stop", direction)
	require.Zero(t, speed)
	h.xiaomiMotorDispatch = nil
	rr = doRequest(t, h.Routes(), http.MethodPost, "/api/cameras/mi-cam/ptz/stop", nil, "", "")
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
	h.xiaomiMotorDispatch = func(context.Context, string, string, int) error { return xiaomi.ErrNotConnected }
	rr = doRequest(t, h.Routes(), http.MethodPost, "/api/cameras/mi-cam/ptz/move", strings.NewReader(`{"mode":"continuous","pan":1}`), "", "")
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
}
