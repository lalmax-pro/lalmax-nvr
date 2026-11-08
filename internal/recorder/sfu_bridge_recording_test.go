package recorder

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/pion/rtp"
	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lal/pkg/logic"
	"github.com/q191201771/lal/pkg/remux"
	maxlogic "github.com/q191201771/lalmax/logic"
	"github.com/q191201771/lalmax/sfu/bridge"
	"github.com/q191201771/lalmax/sfu/rtpforward"
	"github.com/stretchr/testify/require"
)

type bridgeRecordingPublisher struct {
	group *maxlogic.Group
	ready chan struct{}
}

type bridgeRecordingSession struct {
	logic.ICustomizePubSessionContext
	remuxer *remux.AvPacket2RtmpRemuxer
}

func (s *bridgeRecordingSession) WithOption(fn func(*base.AvPacketStreamOption)) {
	s.remuxer.WithOption(fn)
}
func (s *bridgeRecordingSession) FeedAvPacket(packet base.AvPacket) error {
	s.remuxer.FeedAvPacket(packet)
	return nil
}
func (p *bridgeRecordingPublisher) AddCustomizePubSession(string) (logic.ICustomizePubSessionContext, error) {
	s := &bridgeRecordingSession{remuxer: remux.NewAvPacket2RtmpRemuxer().WithOnRtmpMsg(p.group.OnMsg)}
	close(p.ready)
	return s, nil
}
func (*bridgeRecordingPublisher) DelCustomizePubSession(logic.ICustomizePubSessionContext) {}

func TestSFUAVBridgeThroughGroupToRecording(t *testing.T) {
	groups := maxlogic.GetGroupManagerInstance()
	key := maxlogic.NewStreamKey("live", "sfu_test_bridge_recording")
	group, created := groups.GetOrCreateGroup(key, "sfu-bridge-recording-test", nil, 1, 128)
	require.True(t, created)
	t.Cleanup(func() { group.OnStop(); groups.RemoveGroupIfMatch(key, group) })
	store := newTestManager(t)
	engine := &media.EmbeddedLalmax{}
	rec := NewH264Recorder(H264Config{
		CameraID: key.StreamName, SegmentDur: time.Minute, AudioEnabled: true,
		FrameSource: func(ctx context.Context) (media.FrameSubscription, error) {
			return engine.SubscribeFrames(ctx, media.SubscribeFramesRequest{StreamID: key.StreamName, AppName: "live"})
		},
	}, store)
	rec.Hub = model.NewStreamHub()
	var received atomic.Int32
	require.NoError(t, rec.Hub.SubscribeAudio("test", func(_ int64, codec model.AudioCodec, _ []byte) {
		if codec == model.AudioOpus {
			received.Add(1)
		}
	}))
	t.Cleanup(func() { rec.Hub.UnsubscribeAudio("test") })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, rec.Start(ctx))
	t.Cleanup(func() { _ = rec.Stop() })
	require.Eventually(t, func() bool { return len(group.StatSubscribers()) > 0 }, time.Second, time.Millisecond)

	audioFan, err := rtpforward.New(1, 64)
	require.NoError(t, err)
	defer audioFan.Close()
	videoFan, err := rtpforward.New(1, 64)
	require.NoError(t, err)
	defer videoFan.Close()
	audio, err := audioFan.Subscribe("audio")
	require.NoError(t, err)
	video, err := videoFan.Subscribe("video")
	require.NoError(t, err)
	publisher := &bridgeRecordingPublisher{group: group, ready: make(chan struct{})}
	refs := bridge.AVReferences{Audio: bridge.ClockReference{NTP: 100 << 32, RTP: 48000, Rate: 48000}, Video: bridge.ClockReference{NTP: 100 << 32, RTP: 90000, Rate: 90000}, AudioSSRC: 42, VideoSSRC: 42}
	bridgeCtx, stopBridge := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- bridge.RunAVToLal(bridgeCtx, audio, video, publisher, key.StreamName, func(context.Context) (bridge.AVReferences, error) { return refs, nil })
	}()
	t.Cleanup(stopBridge)
	select {
	case <-publisher.ready:
	case <-time.After(time.Second):
		t.Fatal("bridge did not start")
	}
	payload := []byte{24}
	for _, nal := range [][]byte{testSPS, testPPS, {0x65, 0xb0}} {
		payload = binary.BigEndian.AppendUint16(payload, uint16(len(nal)))
		payload = append(payload, nal...)
	}
	require.NoError(t, videoFan.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SSRC: 42, SequenceNumber: 1, Timestamp: 90000, Marker: true}, Payload: payload}))
	require.Eventually(t, func() bool { rec.mu.Lock(); defer rec.mu.Unlock(); return rec.muxer != nil }, 2*time.Second, time.Millisecond, "no recorded video")
	for i := 1; i <= 20; i++ {
		require.NoError(t, audioFan.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SSRC: 42, SequenceNumber: uint16(i), Timestamp: 48000 + uint32(i*960), Marker: true}, Payload: []byte{0xf8, 0xff, 0xfe}}))
		require.NoError(t, videoFan.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SSRC: 42, SequenceNumber: uint16(i + 1), Timestamp: 90000 + uint32(i*1800), Marker: true}, Payload: []byte{0x41, 0xc0}}))
		time.Sleep(5 * time.Millisecond)
	}
	require.Eventually(t, func() bool { return received.Load() == 20 }, 3*time.Second, time.Millisecond, "Opus did not traverse the bridge and recorder")
	require.NoError(t, rec.Stop())
	stopBridge()
	select {
	case err := <-done:
		require.True(t, errors.Is(err, context.Canceled), "%v", err)
	case <-time.After(time.Second):
		t.Fatal("bridge did not stop")
	}
	files, err := store.ListFiles(key.StreamName)
	require.NoError(t, err)
	require.NotEmpty(t, files)
	data, err := os.ReadFile(files[0])
	require.NoError(t, err)
	require.Contains(t, string(data), "Opus")
	require.Contains(t, string(data), string([]byte{0xf8, 0xff, 0xfe}))
	if artifact := os.Getenv("SFU_BRIDGE_TEST_MP4"); artifact != "" {
		require.NoError(t, os.WriteFile(artifact, data, 0o600))
	}
}
