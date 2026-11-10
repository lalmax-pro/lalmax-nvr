package recorder

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/q191201771/lal/pkg/avc"
	lalbase "github.com/q191201771/lal/pkg/base"
	maxlogic "github.com/q191201771/lalmax/logic"
	"github.com/stretchr/testify/require"
)

// The group and embedded frame subscriber are real; the RTMP messages are
// synthetic. This verifies the path used by planned H.264 bridge recordings.
func TestSFUGroupFrameSourceRecordsOpus(t *testing.T) {
	groups := maxlogic.GetGroupManagerInstance()
	key := maxlogic.NewStreamKey("live", "sfu_test_opus_recording")
	group, created := groups.GetOrCreateGroup(key, "sfu-opus-test", nil, 1, 128)
	require.True(t, created)
	t.Cleanup(func() { group.OnStop(); groups.RemoveGroupIfMatch(key, group) })
	engine := &media.EmbeddedLalmax{}
	store := newTestManager(t)
	rec := NewH264Recorder(H264Config{
		CameraID: key.StreamName, SegmentDur: time.Minute, AudioEnabled: true,
		FrameSource: func(ctx context.Context) (media.FrameSubscription, error) {
			return engine.SubscribeFrames(ctx, media.SubscribeFramesRequest{StreamID: key.StreamName, AppName: "live"})
		},
	}, store)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, rec.Start(ctx))
	t.Cleanup(func() { _ = rec.Stop() })
	require.Eventually(t, func() bool { return len(group.StatSubscribers()) > 0 }, time.Second, time.Millisecond)
	seq, err := avc.BuildSeqHeaderFromSpsPps(testSPS, testPPS)
	require.NoError(t, err)
	group.OnMsg(lalbase.RtmpMsg{Header: lalbase.RtmpHeader{MsgTypeId: lalbase.RtmpTypeIdVideo, MsgLen: uint32(len(seq))}, Payload: seq})
	group.OnMsg(rtmpVideo(0, 0, lalbase.RtmpAvcPacketTypeNalu, true, naluAVCC(testIDR)))
	require.Eventually(t, func() bool { rec.mu.Lock(); defer rec.mu.Unlock(); return rec.muxer != nil }, time.Second, time.Millisecond)
	for i := range 20 {
		payload := []byte{lalbase.RtmpSoundFormatOpus << 4, 0xf8, 0xff, 0xfe}
		group.OnMsg(lalbase.RtmpMsg{Header: lalbase.RtmpHeader{MsgTypeId: lalbase.RtmpTypeIdAudio, TimestampAbs: uint32(i * 20), MsgLen: uint32(len(payload))}, Payload: payload})
		group.OnMsg(rtmpVideo(uint32(i*20), 0, lalbase.RtmpAvcPacketTypeNalu, false, naluAVCC(testP)))
		time.Sleep(5 * time.Millisecond)
	}
	require.NoError(t, rec.Stop())
	files, err := store.ListFiles(key.StreamName)
	require.NoError(t, err)
	require.NotEmpty(t, files)
	data, err := os.ReadFile(files[0])
	require.NoError(t, err)
	require.Contains(t, string(data), "Opus")
	require.Contains(t, string(data), string([]byte{0xf8, 0xff, 0xfe}))
	if artifact := os.Getenv("SFU_GROUP_TEST_MP4"); artifact != "" {
		require.NoError(t, os.WriteFile(artifact, data, 0o600))
	}
}
