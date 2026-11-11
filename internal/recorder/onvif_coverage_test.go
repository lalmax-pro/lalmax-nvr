package recorder

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/onvif"
)

// covProfilesErrClient makes GetProfiles fail while keeping every other
// MockDeviceClient behaviour.
type covProfilesErrClient struct {
	*onvif.MockDeviceClient
}

func (c *covProfilesErrClient) GetProfiles(context.Context) ([]onvif.DeviceProfile, error) {
	return nil, errors.New("profiles failed")
}

// covPausableMock extends mockRecorder with the pausable contract.
type covPausableMock struct {
	mockRecorder
	paused  int
	resumed int
}

func (m *covPausableMock) Pause()         { m.paused++ }
func (m *covPausableMock) Resume()        { m.resumed++ }
func (m *covPausableMock) IsPaused() bool { return m.paused > m.resumed }

func TestCovOnvif_HubPauseResumeDelegation(t *testing.T) {
	client := &onvif.MockDeviceClient{StreamURI: &onvif.StreamInfo{URI: "rtsp://127.0.0.1:1/live"}}
	rec := newTestONVIFRecorder(t, client)

	require.Nil(t, rec.GetHub())
	rec.Hub = model.NewStreamHub()
	require.NotNil(t, rec.GetHub())

	// Without a delegate every delegation is a no-op.
	rec.Pause()
	rec.Resume()
	require.False(t, rec.IsPaused())
	require.Nil(t, rec.Delegate())
	require.NoError(t, rec.Stop())
	require.Equal(t, model.StatusStopped, rec.Status())

	// A pausable delegate receives the calls.
	mock := &covPausableMock{}
	rec.delegate = mock
	rec.Pause()
	rec.Resume()
	rec.Pause()
	require.Equal(t, 2, mock.paused)
	require.Equal(t, 1, mock.resumed)
	require.True(t, rec.IsPaused())
	require.Same(t, mock, rec.Delegate())
	require.NoError(t, rec.Stop())
	require.True(t, mock.stopped)

	// A delegate without the pausable contract reports not paused.
	rec2 := newTestONVIFRecorder(t, client)
	rec2.delegate = &mockRecorder{}
	rec2.Pause()
	rec2.Resume()
	require.False(t, rec2.IsPaused())
}

func TestCovOnvif_StartPathVariants(t *testing.T) {
	t.Run("no media profiles", func(t *testing.T) {
		client := &onvif.MockDeviceClient{Profiles: nil}
		rec := newTestONVIFRecorder(t, client, func(r *ONVIFRecorder) { r.cfg.ProfileToken = "" })
		require.ErrorContains(t, rec.Start(context.Background()), "no media profiles")
	})

	t.Run("profile autoselect then empty uri", func(t *testing.T) {
		client := &onvif.MockDeviceClient{
			Profiles:  []onvif.DeviceProfile{{Token: "auto", Encoding: "H265"}},
			StreamURI: &onvif.StreamInfo{URI: ""},
		}
		rec := newTestONVIFRecorder(t, client, func(r *ONVIFRecorder) { r.cfg.ProfileToken = "" })
		require.ErrorContains(t, rec.Start(context.Background()), "empty stream URI")
	})

	t.Run("profile autoselect success", func(t *testing.T) {
		client := &onvif.MockDeviceClient{
			Profiles:  []onvif.DeviceProfile{{Token: "auto", Encoding: "H264"}},
			StreamURI: &onvif.StreamInfo{URI: "rtsp://127.0.0.1:1/live"},
		}
		rec := newTestONVIFRecorder(t, client, func(r *ONVIFRecorder) {
			r.cfg.ProfileToken = ""
			r.newRecorder = func(string) model.Recorder { return &mockRecorder{} }
		})
		require.NoError(t, rec.Start(context.Background()))
		require.NoError(t, rec.Stop())
	})

	t.Run("profiles lookup failure", func(t *testing.T) {
		client := &covProfilesErrClient{MockDeviceClient: &onvif.MockDeviceClient{}}
		rec := newTestONVIFRecorder(t, client, func(r *ONVIFRecorder) { r.cfg.ProfileToken = "" })
		require.ErrorContains(t, rec.Start(context.Background()), "get profiles")
	})
}

func TestCovOnvif_DetectEncoding(t *testing.T) {
	t.Run("manual override", func(t *testing.T) {
		rec := newTestONVIFRecorder(t, &onvif.MockDeviceClient{})
		rec.cfg.StreamEncoding = "H265"
		require.Equal(t, "H265", rec.detectEncoding(context.Background()))
		rec.cfg.StreamEncoding = "H264"
		require.Equal(t, "H264", rec.detectEncoding(context.Background()))
	})

	t.Run("profile metadata h264", func(t *testing.T) {
		client := &onvif.MockDeviceClient{Profiles: []onvif.DeviceProfile{{Token: "p", Encoding: "H264"}}}
		rec := newTestONVIFRecorder(t, client)
		require.Equal(t, "H264", rec.detectEncoding(context.Background()))
	})

	t.Run("profile metadata h265", func(t *testing.T) {
		client := &onvif.MockDeviceClient{Profiles: []onvif.DeviceProfile{
			{Token: "p1", Encoding: "JPEG"},
			{Token: "p2", Encoding: "H265"},
		}}
		rec := newTestONVIFRecorder(t, client)
		require.Equal(t, "H265", rec.detectEncoding(context.Background()))
	})

	t.Run("profiles error falls through to default", func(t *testing.T) {
		client := &covProfilesErrClient{MockDeviceClient: &onvif.MockDeviceClient{}}
		rec := newTestONVIFRecorder(t, client)
		require.Equal(t, "H264", rec.detectEncoding(context.Background()))
	})

	t.Run("probe detects h265", func(t *testing.T) {
		srv := newTestRTSPServerH265(t)
		defer srv.close()
		rec := newTestONVIFRecorder(t, &onvif.MockDeviceClient{})
		rec.rtspURL = srv.rtspURL
		rec.cfg.Username = "admin"
		rec.cfg.Password = "pass"
		require.Equal(t, "H265", rec.detectEncoding(context.Background()))
	})

	t.Run("probe detects h264", func(t *testing.T) {
		srv := newTestRTSPServer(t)
		defer srv.close()
		rec := newTestONVIFRecorder(t, &onvif.MockDeviceClient{})
		rec.rtspURL = srv.rtspURL
		require.Equal(t, "H264", rec.probeRTSPEncoding())
	})

	t.Run("probe without supported video format", func(t *testing.T) {
		srv := newMjpegTestServer(t)
		defer srv.close()
		rec := newTestONVIFRecorder(t, &onvif.MockDeviceClient{})
		rec.rtspURL = srv.rtspURL
		require.Empty(t, rec.probeRTSPEncoding())
	})

	t.Run("probe invalid url", func(t *testing.T) {
		rec := newTestONVIFRecorder(t, &onvif.MockDeviceClient{})
		rec.rtspURL = "://bad"
		require.Empty(t, rec.probeRTSPEncoding())
	})

	t.Run("probe unreachable", func(t *testing.T) {
		rec := newTestONVIFRecorder(t, &onvif.MockDeviceClient{})
		rec.rtspURL = "rtsp://127.0.0.1:1/live"
		require.Empty(t, rec.probeRTSPEncoding())
	})
}

func TestCovOnvif_CreateDelegate(t *testing.T) {
	m := newTestONVIFRecorder(t, &onvif.MockDeviceClient{})
	m.Hub = model.NewStreamHub()

	m.cfg.StreamEncoding = "H265"
	h265 := m.createDelegate("rtsp://127.0.0.1:1/live")
	require.IsType(t, (*H265Recorder)(nil), h265)
	require.NotNil(t, h265.(*H265Recorder).Hub)

	m.cfg.StreamEncoding = "H264"
	h264 := m.createDelegate("rtsp://127.0.0.1:1/live")
	require.IsType(t, (*H264Recorder)(nil), h264)
	require.NotNil(t, h264.(*H264Recorder).Hub)
}
