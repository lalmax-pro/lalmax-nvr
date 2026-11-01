// SPDX-License-Identifier: MIT
//
// Coverage tests: XiaomiPlugin recorder factory wiring.

package xiaomi

import (
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestCovXiaomiPluginMetadata(t *testing.T) {
	p := &XiaomiPlugin{}
	require.Equal(t, "xiaomi", p.Name())
	require.Equal(t, []string{"xiaomi"}, p.Protocols())
	require.Equal(t, config.XiaomiConfig{}, p.ConfigSchema())

	// Route registration is intentionally a no-op; it must not panic.
	p.RegisterRoutes(chi.NewRouter())
}

func TestCovXiaomiPluginNewRecorder(t *testing.T) {
	SetCloudConfig(config.XiaomiConfig{UserID: "user-1", Token: "token-1", Region: "sg"})
	t.Cleanup(func() { SetCloudConfig(config.XiaomiConfig{}) })

	p := &XiaomiPlugin{}
	var store *storage.Manager

	// DID comes from the URL when cfg.DID is empty.
	rec := p.NewRecorder(config.CameraConfig{
		ID:           "cam-url",
		URL:          "xiaomi://655448418",
		AudioEnabled: true,
	}, store, nil)
	require.NotNil(t, rec)
	xr, ok := rec.(*XiaomiRecorder)
	require.True(t, ok)
	require.Equal(t, "655448418", xr.cfg.DID)
	require.Equal(t, "cam-url", xr.cfg.CameraID)
	require.Equal(t, 30*time.Second, xr.cfg.SegmentDur)
	require.True(t, xr.cfg.AudioEnabled)
	require.Equal(t, XiaomiCloudConfig{UserID: "user-1", Token: "token-1", Region: "sg"}, xr.cfg.CloudCfg)

	// Explicit DID wins over the URL.
	rec = p.NewRecorder(config.CameraConfig{ID: "cam-did", DID: "did-1", URL: "xiaomi://ignored"}, store, nil)
	xr = rec.(*XiaomiRecorder)
	require.Equal(t, "did-1", xr.cfg.DID)

	// The media-engine variant wires the same fields.
	rec = p.NewRecorderWithMediaEngine(config.CameraConfig{ID: "cam-engine", URL: "xiaomi://42"}, store, &noopDB{}, nil)
	xr = rec.(*XiaomiRecorder)
	require.Equal(t, "42", xr.cfg.DID)
	require.Nil(t, xr.cfg.MediaEngine)

	rec = p.NewRecorderWithMediaEngine(config.CameraConfig{ID: "cam-engine2", DID: "did-2"}, store, &noopDB{}, nil)
	xr = rec.(*XiaomiRecorder)
	require.Equal(t, "did-2", xr.cfg.DID)

	require.Implements(t, (*model.Recorder)(nil), rec)
}
