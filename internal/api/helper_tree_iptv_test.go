package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/lalmax-pro/lalmax-nvr/internal/xiaomi"
	"github.com/stretchr/testify/require"
)

func TestBuildGroupTree(t *testing.T) {
	groups := []storage.DeviceGroup{
		{ID: 1, Name: "root", ParentID: 0, Level: 1, SortOrder: 1},
		{ID: 2, Name: "child", ParentID: 1, Level: 2, SortOrder: 1},
		{ID: 3, Name: "other", ParentID: 9, Level: 1, SortOrder: 2},
	}
	tree := buildGroupTree(groups, 0, false)
	require.Len(t, tree, 1)
	require.Equal(t, "root", tree[0].Name)
	require.Len(t, tree[0].Children, 1)
	require.Equal(t, "child", tree[0].Children[0].Name)
	require.Empty(t, tree[0].Children[0].Children)
	require.Empty(t, buildGroupTree(nil, 0, true))
}

func TestParseEventID(t *testing.T) {
	req := func(id string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/events/"+id, nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", id)
		return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
	}
	w := httptest.NewRecorder()
	id, ok := parseEventID(w, req("12"))
	require.True(t, ok)
	require.Equal(t, int64(12), id)

	w = httptest.NewRecorder()
	_, ok = parseEventID(w, req("0"))
	require.False(t, ok)
	require.Equal(t, http.StatusBadRequest, w.Code)

	w = httptest.NewRecorder()
	_, ok = parseEventID(w, req("abc"))
	require.False(t, ok)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestCopyIPTVProxyHeadersAndWriteIPTVError(t *testing.T) {
	src := http.Header{}
	src.Set("Content-Type", "application/vnd.apple.mpegurl")
	src.Set("ETag", `"abc"`)
	src.Set("X-Ignored", "nope")
	dst := http.Header{}
	copyIPTVProxyHeaders(dst, src)
	require.Equal(t, "application/vnd.apple.mpegurl", dst.Get("Content-Type"))
	require.Equal(t, `"abc"`, dst.Get("ETag"))
	require.Empty(t, dst.Get("X-Ignored"))

	cases := []struct {
		err  string
		code int
	}{
		{"channel not found", http.StatusNotFound},
		{"invalid url", http.StatusBadRequest},
		{"protocol not allowed", http.StatusBadRequest},
		{"format not supported", http.StatusBadRequest},
		{"channel disabled", http.StatusBadRequest},
		{"no channels in playlist", http.StatusBadRequest},
		{"none of the selected channels", http.StatusBadRequest},
		{"already committed", http.StatusBadRequest},
		{"upstream timeout", http.StatusBadGateway},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		writeIPTVError(w, errors.New(tc.err))
		require.Equal(t, tc.code, w.Code, tc.err)
	}
}

func TestXiaomiSessionAndModelHelpers(t *testing.T) {
	require.Nil(t, sessionToResult(nil))
	got := sessionToResult(&xiaomi.CloudSession{UserID: "u1", PassToken: "tok", Region: "cn"})
	require.Equal(t, "u1", got.UserID)
	require.Equal(t, "tok", got.PassToken)
	require.Equal(t, "cn", got.Region)

	require.True(t, isXiaomiCameraModel("xiaomi.camera.ipc"))
	require.True(t, isXiaomiCameraModel("foo.cateye.bar"))
	require.True(t, isXiaomiCameraModel("foo.feeder.bar"))
	require.False(t, isXiaomiCameraModel("generic.nvr"))

	login := &xiaomi.LoginError{Captcha: []byte("image"), VerifyPhone: "phone"}
	v := loginErrToVerification(login, "sess")
	require.Equal(t, []byte("image"), v.Captcha)
	require.Equal(t, "phone", v.VerifyPhone)
	require.Equal(t, "sess", v.CaptchaSessionID)

	cap := &xiaomi.CaptchaSessionError{LoginError: &xiaomi.LoginError{Captcha: []byte("image"), VerifyEmail: "email"}, CaptchaSessionID: "c1"}
	v = captchaErrToVerification(cap)
	require.Equal(t, "email", v.VerifyEmail)
	require.Equal(t, "c1", v.CaptchaSessionID)

	var loginTarget *xiaomi.LoginError
	require.True(t, isLoginErr(login, &loginTarget))
	var captchaTarget *xiaomi.CaptchaSessionError
	require.True(t, isCaptchaErr(cap, &captchaTarget))
	require.False(t, isLoginErr(errors.New("x"), &loginTarget))
}

func TestTranscodeGenerationOK(t *testing.T) {
	w := httptest.NewRecorder()
	gen, ok := transcodeGeneration(w, httptest.NewRequest(http.MethodPost, "/jobs/x/start?generation=9", nil))
	require.True(t, ok)
	require.Equal(t, uint64(9), gen)
	require.Equal(t, 200, w.Code)
}

func TestWriteJSONAndError(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSON(w, http.StatusCreated, map[string]string{"ok": "yes"})
	require.Equal(t, http.StatusCreated, w.Code)
	require.Equal(t, "application/json", w.Header().Get("Content-Type"))
	var body map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, "yes", body["ok"])

	w = httptest.NewRecorder()
	writeError(w, http.StatusTeapot, "nope")
	require.Equal(t, http.StatusTeapot, w.Code)
}
