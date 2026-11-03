package camera

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/recorder"
	"github.com/stretchr/testify/require"
)

type issue74PlayEngine struct {
	stubMediaEngine
	play      *media.PlayURL
	playErr   error
	streamErr error
}

func (s *issue74PlayEngine) BuildPlayURL(context.Context, media.PlayURLRequest) (*media.PlayURL, error) {
	return s.play, s.playErr
}
func (s *issue74PlayEngine) GetStream(context.Context, string) (*media.StreamInfo, error) {
	return nil, s.streamErr
}

func TestIssue74RecordingSourceURLFailures(t *testing.T) {
	mgr := NewCameraManager(&config.Config{}, nil, nil, "")
	cam := config.CameraConfig{ID: "cam", Protocol: "rtsp", Encoding: "h264"}
	require.Empty(t, mgr.recordingSourceURL(cam))
	engine := &issue74PlayEngine{playErr: errors.New("play URL failed")}
	mgr.SetMediaEngine(engine)
	require.Empty(t, mgr.recordingSourceURL(cam))
	engine.playErr = nil
	require.Empty(t, mgr.recordingSourceURL(cam), "nil play URL is not a usable relay")
	engine.play = &media.PlayURL{URL: "rtsp://unused/relay"}
	for _, protocol := range []string{"rtsp", "gb28181", "onvif", "rtmp-pull", "http-flv-pull", "udp-ts-pull", "xiaomi", "http"} {
		t.Run(protocol, func(t *testing.T) {
			cam.Protocol = protocol
			cam.Encoding = "mjpeg"
			require.Empty(t, mgr.recordingSourceURL(cam))
			cam.Encoding = "h265"
			if protocol == "http" {
				require.Empty(t, mgr.recordingSourceURL(cam))
			} else {
				require.Equal(t, engine.play.URL, mgr.recordingSourceURL(cam))
			}
		})
	}
	require.Equal(t, "h265", mgr.normalizedRecordingEncoding(config.CameraConfig{StreamEncoding: "H265"}))
	require.Empty(t, mgr.normalizedRecordingEncoding(config.CameraConfig{}))
}

func TestIssue74CreateRecorderConfigurationBranches(t *testing.T) {
	mgr := NewCameraManager(&config.Config{}, nil, nil, "")
	mgr.SetMediaEngine(&stubMediaEngine{})
	for _, tc := range []struct {
		protocol, encoding string
		supported          bool
	}{
		{"stream", "h264", true}, {"stream", "h265", true}, {"stream", "mjpeg", false},
		{"gb28181", "", true}, {"gb28181", "h265", true}, {"gb28181", "mpeg4", false},
		{"http", "h264", false}, {"http", "jpeg", true},
		{"rtmp-pull", "", true}, {"http-flv-pull", "h265", true}, {"udp-ts-pull", "mpeg4", false},
		{"unknown", "", false},
	} {
		t.Run(tc.protocol+"/"+tc.encoding, func(t *testing.T) {
			cam := config.CameraConfig{ID: "cam", Protocol: tc.protocol, Encoding: tc.encoding, FrameWatchdogTimeout: "7s"}
			rec := mgr.createRecorder(cam, time.Minute)
			if !tc.supported {
				require.Nil(t, rec)
				return
			}
			require.NotNil(t, rec)
			switch r := rec.(type) {
			case *recorder.H264Recorder:
				require.NotNil(t, r.Hub)
			case *recorder.H265Recorder:
				require.NotNil(t, r.Hub)
			case *recorder.HTTPJPEGRecorder:
				require.NotNil(t, r.Hub)
			default:
				t.Fatalf("unexpected recorder %T", rec)
			}
		})
	}
	mgr.SetMediaEngine(nil)
	require.Nil(t, mgr.createRecorder(config.CameraConfig{Protocol: "rtmp-pull", Encoding: "h264"}, time.Minute))
	for _, interval := range []string{"invalid", "0s", "2s"} {
		rec := mgr.createRecorder(config.CameraConfig{ID: "timelapse", Protocol: "timelapse", Timelapse: &config.CameraTimelapseConfig{Interval: interval, OutputFPS: 15, VideoCodec: "h265"}}, time.Minute)
		require.IsType(t, &recorder.TimelapseRecorder{}, rec)
	}
}

func TestIssue74StartStreamRecordingGuards(t *testing.T) {
	mgr := NewCameraManager(&config.Config{}, nil, nil, "")
	ctx := context.Background()
	require.EqualError(t, mgr.StartStreamRecording(ctx, "cam"), "media engine not available")
	sentinel := errors.New("stream lookup failed")
	mgr.SetMediaEngine(&issue74PlayEngine{streamErr: sentinel})
	require.EqualError(t, mgr.StartStreamRecording(ctx, "  "), "stream ID is required")
	require.ErrorIs(t, mgr.StartStreamRecording(ctx, "cam"), sentinel)
	require.NoError(t, mgr.StopStreamRecording(ctx, "  "))
	require.NoError(t, mgr.StopStreamRecording(ctx, "missing"))
	mgr.StopAllStreamRecordings()
}
