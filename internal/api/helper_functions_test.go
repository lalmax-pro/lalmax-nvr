package api

import (
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/gb28181"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestEncodingFromMediaCodec(t *testing.T) {
	require.Equal(t, string(model.FormatH264), encodingFromMediaCodec("AVC1"))
	require.Equal(t, string(model.FormatH265), encodingFromMediaCodec(" hevc "))
	require.Equal(t, string(model.FormatMJPEG), encodingFromMediaCodec("JPEG"))
	require.Empty(t, encodingFromMediaCodec("opus"))
}

func TestIsValidMonthAndStreamIDForCamera(t *testing.T) {
	require.True(t, isValidMonth("2026-01"))
	require.False(t, isValidMonth("2026-13"))
	require.False(t, isValidMonth("202601"))
	require.Equal(t, "cam1", streamIDForCamera(storage.CameraRow{StreamID: " cam1 "}))
	require.Empty(t, streamIDForCamera(storage.CameraRow{}))
}

func TestIsONVIFNotSupportedAndQueryInt(t *testing.T) {
	require.False(t, isONVIFNotSupported(nil))
	require.True(t, isONVIFNotSupported(errors.New("Action Not Supported")))
	require.True(t, isONVIFNotSupported(errors.New("unsupported")))
	require.False(t, isONVIFNotSupported(errors.New("timeout")))
	require.False(t, isONVIFAuthError(nil))

	r := httptest.NewRequest(http.MethodGet, "/x?limit=8", nil)
	require.Equal(t, 8, queryInt(r, "limit", 10))
	require.Equal(t, 10, queryInt(r, "offset", 10))
	r = httptest.NewRequest(http.MethodGet, "/x?limit=nope", nil)
	require.Equal(t, 3, queryInt(r, "limit", 3))
}

func TestRequestIPAndUsername(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "192.0.2.10:12345"
	require.Equal(t, "192.0.2.10", requestIP(r))
	r.RemoteAddr = "invalid"
	require.Equal(t, "invalid", requestIP(r))

	r = httptest.NewRequest(http.MethodGet, "/", nil)
	r.SetBasicAuth("admin", "secret")
	require.Equal(t, "admin", requestUsername(r))

	token := base64.StdEncoding.EncodeToString([]byte("operator:pw"))
	r = httptest.NewRequest(http.MethodGet, "/?token="+url.QueryEscape(token), nil)
	require.Equal(t, "operator", requestUsername(r))
	r = httptest.NewRequest(http.MethodGet, "/", nil)
	require.Empty(t, requestUsername(r))
}

func TestLogLevelAtLeastAndResolveUpstreamLocation(t *testing.T) {
	require.True(t, logLevelAtLeast("error", slog.LevelWarn))
	require.False(t, logLevelAtLeast("debug", slog.LevelInfo))

	base, err := url.Parse("http://127.0.0.1:8080/live/")
	require.NoError(t, err)
	require.Empty(t, resolveUpstreamLocation(base, ""))
	require.Equal(t, "http://example/abs", resolveUpstreamLocation(base, "http://example/abs"))
	require.Equal(t, "http://127.0.0.1:8080/live/seg.ts", resolveUpstreamLocation(base, "seg.ts"))
	require.Equal(t, "://bad", resolveUpstreamLocation(base, "://bad"))
	require.Equal(t, "seg.ts", resolveUpstreamLocation(nil, "seg.ts"))
}

func TestSetupBrowsePathAllowed(t *testing.T) {
	root := t.TempDir()
	t.Setenv("NVR_DATA_DIR", root)
	nested := filepath.Join(root, "nested")
	require.NoError(t, os.MkdirAll(nested, 0o755))
	file := filepath.Join(root, "file.txt")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o644))

	got, err := setupBrowsePath("")
	require.NoError(t, err)
	require.NotEmpty(t, got)

	got, err = setupBrowsePath(nested)
	require.NoError(t, err)
	require.Equal(t, nested, got)

	_, err = setupBrowsePath(file)
	require.ErrorIs(t, err, os.ErrInvalid)
	_, err = setupBrowsePath(filepath.Join(root, "missing"))
	require.Error(t, err)
	require.True(t, setupBrowseAllowed(nested))
	require.False(t, setupBrowseAllowed(filepath.Join(t.TempDir(), "outside")))
	require.Contains(t, setupBrowseRoots(), root)
}

func TestBuildGB28181PTZCommandWrappers(t *testing.T) {
	require.Equal(t, gb28181.BuildStop(), buildGB28181PTZCommand("stop", 0.5))
	require.Equal(t, gb28181.BuildContinuousMove("up", 0.5), buildGB28181PTZCommand("up", 0.5))
	require.Equal(t, buildGB28181PTZCommand("left", 0.2), buildPTZCommand("left", 0.2))
	require.Empty(t, buildGB28181PTZCommand("diagonal", 0.5))
}
