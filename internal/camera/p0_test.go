package camera

import (
	"context"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/stretchr/testify/require"
)

func TestHandleStreamEventIgnoresSubStream(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	mgr.cfg.Cameras = []config.CameraConfig{{
		ID: "cam-1", Name: "Cam", Protocol: "rtsp", Encoding: "h264", Enabled: true,
	}}
	stopped := false
	mgr.recorders["cam-1"] = &stubRecorder{status: model.StatusRecording, stopFn: func() error {
		stopped = true
		return nil
	}}
	mgr.handleStreamEvent(context.Background(), media.Event{
		Type:     media.EventStreamStopped,
		StreamID: media.SubStreamID("cam-1"),
	})
	require.False(t, stopped)
	_, ok := mgr.recorders["cam-1"]
	require.True(t, ok)
}

func TestUpdateCameraONVIFEndpointRestarts(t *testing.T) {
	mgr, _, _, _ := newTestManager(t)
	ctx := context.Background()
	id, err := mgr.AddCamera(ctx, config.CameraConfig{
		Name:          "ONVIF Cam",
		Protocol:      "onvif",
		ONVIFEndpoint: "http://10.0.0.1/onvif/device_service",
		URL:           "http://10.0.0.1/onvif/device_service",
		Enabled:       false,
	})
	require.NoError(t, err)
	ep := "http://10.0.0.9/onvif/device_service"
	cam, err := mgr.UpdateCamera(ctx, id, CameraUpdate{ONVIFEndpoint: &ep, URL: &ep})
	require.NoError(t, err)
	require.Equal(t, ep, cam.ONVIFEndpoint)
}

type stubRecorder struct {
	status model.RecorderStatus
	stopFn func() error
}

func (s *stubRecorder) Start(context.Context) error { return nil }
func (s *stubRecorder) Stop() error {
	if s.stopFn != nil {
		return s.stopFn()
	}
	return nil
}
func (s *stubRecorder) Status() model.RecorderStatus { return s.status }
