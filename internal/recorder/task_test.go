package recorder

import (
	"context"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/stretchr/testify/require"
)

type stubTaskEngine struct {
	info *media.StreamInfo
	byID map[string]*media.StreamInfo
	subs int
}

func (s *stubTaskEngine) Start(context.Context) error { return nil }
func (s *stubTaskEngine) Shutdown(context.Context) error {
	return nil
}
func (s *stubTaskEngine) Ready(context.Context) error { return nil }
func (s *stubTaskEngine) StartPull(context.Context, media.StartPullRequest) (*media.StreamSession, error) {
	return nil, nil
}
func (s *stubTaskEngine) StopPull(context.Context, string) error { return nil }
func (s *stubTaskEngine) StartRTPReceive(context.Context, media.StartRTPReceiveRequest) (*media.StreamSession, error) {
	return nil, nil
}
func (s *stubTaskEngine) StopRTPReceive(context.Context, string) error { return nil }
func (s *stubTaskEngine) KickSession(context.Context, string) error    { return nil }
func (s *stubTaskEngine) GetStream(_ context.Context, streamID string) (*media.StreamInfo, error) {
	if s.byID != nil {
		if info, ok := s.byID[streamID]; ok {
			return info, nil
		}
		return nil, nil
	}
	if s.info != nil && s.info.StreamID == streamID {
		return s.info, nil
	}
	return nil, nil
}
func (s *stubTaskEngine) ListStreams(context.Context) ([]media.StreamInfo, error) {
	return nil, nil
}
func (s *stubTaskEngine) BuildPlayURL(context.Context, media.PlayURLRequest) (*media.PlayURL, error) {
	return nil, nil
}
func (s *stubTaskEngine) AddCustomizePubSession(context.Context, string) (media.CustomizePubSession, error) {
	return nil, nil
}
func (s *stubTaskEngine) DelCustomizePubSession(context.Context, media.CustomizePubSession) error {
	return nil
}
func (s *stubTaskEngine) SubscribeEvents(context.Context, media.EventFilter) (<-chan media.Event, error) {
	return nil, nil
}
func (s *stubTaskEngine) SubscribeRTMPEvents(context.Context) (<-chan media.RTMPEvent, error) {
	return nil, nil
}
func (s *stubTaskEngine) SubscribeSRTEvents(context.Context) (<-chan media.SRTEvent, error) {
	return nil, nil
}
func (s *stubTaskEngine) SubscribeWHIPEvents(context.Context) (<-chan media.WHIPEvent, error) {
	return nil, nil
}
func (s *stubTaskEngine) SubscribeFrames(context.Context, media.SubscribeFramesRequest) (media.FrameSubscription, error) {
	s.subs++
	return &stubFrameSub{}, nil
}

type stubFrameSub struct{}

func (s *stubFrameSub) ID() string { return "nvr-record-test" }
func (s *stubFrameSub) Frames() <-chan media.MediaFrame {
	ch := make(chan media.MediaFrame)
	close(ch)
	return ch
}
func (s *stubFrameSub) Err() <-chan error { return nil }
func (s *stubFrameSub) Close() error      { return nil }

func TestTaskManager_EnsureStopAndObserver(t *testing.T) {
	t.Parallel()
	engine := &stubTaskEngine{info: &media.StreamInfo{StreamID: "obs", VideoCodec: "h264", Active: true}}
	mgr := NewTaskManager(engine, nil, nil, nil, nil, time.Second)
	mgr.SetOwnerFunc(func(streamID string) StreamOwner {
		return StreamOwner{CameraID: "cam-1", Name: "Cam", Encoding: "h264"}
	})
	want := false
	mgr.SetShouldRecord(func(string) bool { return want })

	ctx := context.Background()
	require.NoError(t, mgr.Ensure(ctx, "obs"))
	require.True(t, mgr.Running("obs"))
	require.Equal(t, 1, mgr.Count())
	require.NoError(t, mgr.Ensure(ctx, "obs"))
	require.Equal(t, 1, mgr.Count())

	require.NoError(t, mgr.Stop(ctx, "obs", ReasonPlanInactive))
	require.False(t, mgr.Running("obs"))
	require.Equal(t, 0, mgr.Count())

	mgr.OnStreamDown(ctx, "obs")
	require.False(t, mgr.Running("obs"))

	want = true
	mgr.OnStreamUp(ctx, "obs")
	require.True(t, mgr.Running("obs"))
	require.NotNil(t, mgr.Recorder("obs"))
	require.NotEqual(t, model.StatusError, mgr.Status()["obs"])

	mgr.OnDeviceRemoved(ctx, "cam-1", "obs")
	require.False(t, mgr.Running("obs"))
}

func TestTaskManager_RejectsUnknownCodec(t *testing.T) {
	t.Parallel()
	engine := &stubTaskEngine{info: &media.StreamInfo{StreamID: "jpeg", VideoCodec: "jpeg", Active: true}}
	mgr := NewTaskManager(engine, nil, nil, nil, nil, time.Second)
	err := mgr.Ensure(context.Background(), "jpeg")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not recordable")
}
