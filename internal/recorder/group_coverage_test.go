package recorder

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/q191201771/lal/pkg/avc"
	"github.com/q191201771/lal/pkg/base"
	"github.com/stretchr/testify/require"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/metrics"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

// covFinder is a deterministic groupFinder backed by maps.
type covFinder struct {
	names  []string
	groups map[string]subscribeGroup
}

func (f covFinder) Find(name string) (subscribeGroup, bool) {
	g, ok := f.groups[name]
	return g, ok
}

func (f covFinder) Each(fn func(string)) {
	for _, name := range f.names {
		fn(name)
	}
}

// covGroupEngine adds controllable stream events to the task engine stub.
type covGroupEngine struct {
	*stubTaskEngine
	events chan media.Event
	err    error
}

func (e *covGroupEngine) SubscribeEvents(context.Context, media.EventFilter) (<-chan media.Event, error) {
	if e.err != nil {
		return nil, e.err
	}
	return e.events, nil
}

func newCovGroupWriter(t *testing.T, finder groupFinder) *GroupWriter {
	t.Helper()
	store, err := storage.NewManager(t.TempDir())
	require.NoError(t, err)
	w := NewGroupWriter(store, nil, nil, nil, time.Hour)
	w.groups = finder
	t.Cleanup(w.Close)
	return w
}

func covCameraLookup(ids ...string) func(string) (config.CameraConfig, bool) {
	allowed := make(map[string]bool, len(ids))
	for _, id := range ids {
		allowed[id] = true
	}
	return func(id string) (config.CameraConfig, bool) {
		if !allowed[id] {
			return config.CameraConfig{}, false
		}
		return config.CameraConfig{ID: id, Protocol: "rtsp", Encoding: "h264", Enabled: true}, true
	}
}

func TestCovGroupWriter_NilReceiversAndDefaults(t *testing.T) {
	var nilW *GroupWriter
	nilW.SetLookup(nil)
	nilW.SetShouldWrite(nil)
	nilW.SetAccept(nil)
	require.False(t, nilW.Covers(config.CameraConfig{Protocol: "rtsp", Encoding: "h264"}))
	require.False(t, nilW.Accepts(config.CameraConfig{Protocol: "rtsp", Encoding: "h264", Enabled: true}))
	nilW.SetWriting("x", true)
	nilW.Rotate("x")
	attached, writing := nilW.State("x")
	require.False(t, attached)
	require.False(t, writing)
	_, _, _, _, _, ready := nilW.StreamInfo("x")
	require.False(t, ready)
	nilW.Sync()
	nilW.SyncContext(context.Background())
	nilW.Run(context.Background())
	nilW.Attach("x")
	nilW.AttachContext(context.Background(), "x")
	nilW.AttachCamera(config.CameraConfig{ID: "x"})
	nilW.AttachCameraContext(context.Background(), config.CameraConfig{ID: "x"})
	nilW.Detach("x")
	nilW.AttachVoIP("x", true)
	nilW.DetachVoIP("x")
	nilW.Close()

	w := NewGroupWriter(nil, nil, nil, nil, 0)
	require.Equal(t, DefaultSegmentDur, w.segDur)
	require.Nil(t, w.db)
	require.True(t, w.Covers(config.CameraConfig{Protocol: "rtsp", Encoding: "h265"}))
	require.False(t, w.Accepts(config.CameraConfig{Protocol: "rtsp", Encoding: "h265"}))
	w.SetAccept(func(config.CameraConfig) bool { return true })
	require.True(t, w.Covers(config.CameraConfig{}))
	require.True(t, w.Accepts(config.CameraConfig{Enabled: true}))

	// No session: accessors are inert.
	require.Zero(t, func() int { _, c := w.State("none"); return b2i(c) }())
	_, _, _, _, hub, ready := w.StreamInfo("none")
	require.Nil(t, hub)
	require.False(t, ready)
	require.True(t, w.wantWrite("no-plan"), "unset write switch records by default")
	w.SetShouldWrite(func(string) bool { return false })
	require.False(t, w.wantWrite("no-plan"))
	w.SetWriting("no-plan", true)
	require.True(t, w.wantWrite("no-plan"), "explicit write switch wins")
	w.Rotate("none")
	w.Detach("none")
	w.AttachVoIP("none", true)
	w.DetachVoIP("none")
	w.Close()
	w.Close() // closeOnce
}

func b2i(v bool) int {
	if v {
		return 1
	}
	return 0
}

func TestCovGroupWriter_AttachDetachAndStreamInfo(t *testing.T) {
	g := &fakeGroup{}
	w := newCovGroupWriter(t, covFinder{names: []string{"cam1"}, groups: map[string]subscribeGroup{"cam1": g}})
	w.SetLookup(covCameraLookup("cam1"))

	// Camera not managed by this writer.
	w.AttachContext(context.Background(), "unknown")
	require.False(t, func() bool { ok, _ := w.State("unknown"); return ok }())

	// Empty and sub-stream IDs are ignored, and voip style cameras bypass Accepts.
	w.AttachCameraContext(context.Background(), config.CameraConfig{})
	w.AttachContext(context.Background(), "")
	w.AttachContext(context.Background(), "cam1_sub")
	require.False(t, func() bool { ok, _ := w.State("cam1_sub"); return ok }())

	// The camera is accepted but no group exists yet.
	w.groups = covFinder{}
	w.Attach("cam1")
	require.False(t, func() bool { ok, _ := w.State("cam1"); return ok }())

	w.groups = covFinder{names: []string{"cam1"}, groups: map[string]subscribeGroup{"cam1": g}}
	w.Attach("cam1")
	ok, writing := w.State("cam1")
	require.True(t, ok)
	require.True(t, writing)
	// Attaching twice is a no-op.
	w.Attach("cam1")

	// Live stream info becomes ready once the codec parameters arrive.
	_, _, _, _, hub, ready := w.StreamInfo("cam1")
	require.NotNil(t, hub)
	require.False(t, ready)
	w.mu.Lock()
	s := w.sessions["cam1"]
	w.mu.Unlock()
	s.streamMu.Lock()
	s.liveCodec = "h264"
	s.liveSPS = []byte{0x67, 0x01}
	s.livePPS = []byte{0x68, 0x02}
	s.liveVPS = []byte{0x40, 0x03}
	s.streamMu.Unlock()
	codec, sps, pps, vps, hub2, ready := w.StreamInfo("cam1")
	require.True(t, ready)
	require.Equal(t, model.FormatH264, codec)
	require.Equal(t, []byte{0x67, 0x01}, sps)
	require.Equal(t, []byte{0x68, 0x02}, pps)
	require.Equal(t, []byte{0x40, 0x03}, vps)
	require.NotNil(t, hub2)

	// Writing switch and rotation reach the live session.
	w.SetWriting("cam1", false)
	_, writing = w.State("cam1")
	require.False(t, writing)
	require.False(t, w.wantWrite("cam1"))
	w.Rotate("cam1")
	require.True(t, s.rotate.Load())

	// Detach removes the subscriber from the group and closes the session.
	w.Detach("cam1")
	require.False(t, func() bool { ok, _ := w.State("cam1"); return ok }())

	// Sync attaches existing groups and detaches stale sessions.
	w.Sync()
	require.True(t, func() bool { ok, _ := w.State("cam1"); return ok }())
	w.groups = covFinder{names: []string{"cam1_sub", "cam2"}, groups: map[string]subscribeGroup{"cam1": g}}
	w.SetLookup(covCameraLookup("cam1", "cam2"))
	w.SyncContext(context.Background())
	require.False(t, func() bool { ok, _ := w.State("cam1"); return ok }(), "group disappeared")
	// A nil finder makes Sync a no-op.
	w.groups = nil
	w.Sync()
}

func TestCovGroupWriter_AttachWriteSwitchRecheck(t *testing.T) {
	g := &fakeGroup{}
	w := newCovGroupWriter(t, covFinder{names: []string{"cam1"}, groups: map[string]subscribeGroup{"cam1": g}})
	w.SetLookup(covCameraLookup("cam1"))
	w.SetShouldWrite(func(id string) bool {
		// The plan flips while the callback runs; the writer must re-read it.
		w.SetWriting(id, false)
		return true
	})
	w.Attach("cam1")
	ok, writing := w.State("cam1")
	require.True(t, ok)
	require.False(t, writing, "explicit write switch wins over the callback")
}

func TestCovGroupWriter_VoIPAndClose(t *testing.T) {
	g := &fakeGroup{}
	w := newCovGroupWriter(t, covFinder{names: []string{"call-1"}, groups: map[string]subscribeGroup{"call-1": g}})

	w.AttachVoIP("call-1", true)
	ok, writing := w.State("call-1")
	require.True(t, ok)
	require.True(t, writing)
	w.DetachVoIP("call-1")
	require.False(t, func() bool { ok, _ := w.State("call-1"); return ok }())
	require.NotContains(t, w.desired, "call-1")

	// A session whose group is already gone is still halted by Close.
	w.mu.Lock()
	w.sessions["orphan"] = &recSession{
		cameraID: "orphan",
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	w.mu.Unlock()
	go w.sessions["orphan"].loop()
	w.Close()
	_, ok = w.State("orphan")
	require.False(t, ok)
}

func TestCovGroupWriter_RunAndEvents(t *testing.T) {
	t.Run("subscribe failure and nil engine", func(t *testing.T) {
		store, err := storage.NewManager(t.TempDir())
		require.NoError(t, err)
		engine := &covGroupEngine{stubTaskEngine: &stubTaskEngine{}, err: errors.New("no events")}
		w := NewGroupWriter(store, nil, nil, engine, time.Hour)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			w.Run(ctx)
		}()
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Run did not stop on context cancellation")
		}

		// Run without an engine only syncs groups.
		w2 := NewGroupWriter(store, nil, nil, nil, time.Hour)
		ctx2, cancel2 := context.WithCancel(context.Background())
		done2 := make(chan struct{})
		go func() {
			defer close(done2)
			w2.Run(ctx2)
		}()
		cancel2()
		select {
		case <-done2:
		case <-time.After(5 * time.Second):
			t.Fatal("Run without engine did not stop")
		}
	})

	t.Run("stream events attach and detach", func(t *testing.T) {
		g := &fakeGroup{}
		engine := &covGroupEngine{stubTaskEngine: &stubTaskEngine{}, events: make(chan media.Event, 8)}
		store, err := storage.NewManager(t.TempDir())
		require.NoError(t, err)
		w := NewGroupWriter(store, nil, nil, engine, time.Hour)
		w.groups = covFinder{names: nil, groups: map[string]subscribeGroup{"cam1": g}}
		w.SetLookup(covCameraLookup("cam1"))

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			w.Run(ctx)
		}()

		engine.events <- media.Event{Type: media.EventStreamActive, StreamID: "cam1"}
		require.Eventually(t, func() bool {
			ok, _ := w.State("cam1")
			return ok
		}, 5*time.Second, 2*time.Millisecond)

		// Sub-stream events are ignored.
		engine.events <- media.Event{Type: media.EventStreamActive, StreamID: "cam1_sub"}

		engine.events <- media.Event{Type: media.EventPublisherStopped, StreamID: "cam1"}
		require.Eventually(t, func() bool {
			ok, _ := w.State("cam1")
			return !ok
		}, 5*time.Second, 2*time.Millisecond)

		// A closed event channel detaches the stream source without stopping Run.
		close(engine.events)
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Run did not stop after the event channel closed")
		}
	})
}

func TestCovRecSubscriber_QueueAndStop(t *testing.T) {
	var nilSub *recSubscriber
	nilSub.OnMsg(base.RtmpMsg{})
	nilSub.OnStop()
	(&recSubscriber{}).OnMsg(base.RtmpMsg{})
	(&recSubscriber{}).OnStop()

	s := &recSession{
		cameraID: "cam1",
		queue:    make(chan base.RtmpMsg, 1),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
		metrics:  metrics.NewMetrics(),
	}
	sub := &recSubscriber{s: s}
	sub.OnMsg(rtmpVideo(0, 0, base.RtmpAvcPacketTypeNalu, true, naluAVCC([]byte{0x65, 0x88})))
	require.Len(t, s.queue, 1)
	// Overflow marks a media gap and drops the message.
	for i := 0; i < 1100; i++ {
		sub.OnMsg(rtmpVideo(40, 0, base.RtmpAvcPacketTypeNalu, false, naluAVCC([]byte{0x41, 0x9a})))
	}
	require.True(t, s.mediaGap.Load())
	require.Greater(t, s.queueDrops.Load(), uint64(1024))

	// A stopped session drops messages without queuing.
	dropsBefore := s.queueDrops.Load()
	s.halt()
	sub.OnMsg(rtmpVideo(80, 0, base.RtmpAvcPacketTypeNalu, false, naluAVCC([]byte{0x41, 0x9a})))
	require.Equal(t, dropsBefore, s.queueDrops.Load(), "stopped sessions do not count drops")
	require.Len(t, s.queue, 1)

	// OnStop with an owner forgets the session without removing the subscriber.
	w := newCovGroupWriter(t, covFinder{})
	w.mu.Lock()
	w.sessions["cam1"] = s
	w.mu.Unlock()
	s.owner = w
	sub.OnStop()
	_, ok := w.State("cam1")
	require.False(t, ok)

	// OnStop without an owner halts the session directly.
	s2 := &recSession{cameraID: "cam2", stop: make(chan struct{}), done: make(chan struct{})}
	(&recSubscriber{s: s2}).OnStop()
	select {
	case <-s2.stop:
	default:
		t.Fatal("expected the session to halt")
	}
}

func TestCovLalmaxGroups(t *testing.T) {
	finder := lalmaxGroups{}
	group, ok := finder.Find("definitely-not-a-stream")
	require.False(t, ok)
	require.Nil(t, group)
	finder.Each(func(string) {})
}

// --- group_rtmp.go ---

func TestCovGroupRTMP_VideoParsing(t *testing.T) {
	t.Run("videoTimes", func(t *testing.T) {
		_, _, ok := videoTimes(base.RtmpMsg{})
		require.False(t, ok)
		_, _, ok = videoTimes(base.RtmpMsg{Header: base.RtmpHeader{MsgTypeId: base.RtmpTypeIdVideo}, Payload: []byte{1, 2}})
		require.False(t, ok)
		dts, pts, ok := videoTimes(rtmpVideo(100, 40, base.RtmpAvcPacketTypeNalu, true, naluAVCC([]byte{0x65})))
		require.True(t, ok)
		require.Equal(t, 100*time.Millisecond, dts)
		require.Equal(t, 140*time.Millisecond, pts)
	})

	t.Run("parseVideoSeq", func(t *testing.T) {
		_, _, _, _, ok := parseVideoSeq(base.RtmpMsg{})
		require.False(t, ok)

		seq, err := avc.BuildSeqHeaderFromSpsPps(groupTestSPS, groupTestPPS)
		require.NoError(t, err)
		codec, sps, pps, vps, ok := parseVideoSeq(base.RtmpMsg{
			Header:  base.RtmpHeader{MsgTypeId: base.RtmpTypeIdVideo, MsgLen: uint32(len(seq))},
			Payload: seq,
		})
		require.True(t, ok)
		require.Equal(t, "h264", codec)
		require.Equal(t, groupTestSPS, sps)
		require.Equal(t, groupTestPPS, pps)
		require.Nil(t, vps)

		// A truncated sequence header is rejected.
		_, _, _, _, ok = parseVideoSeq(base.RtmpMsg{
			Header:  base.RtmpHeader{MsgTypeId: base.RtmpTypeIdVideo},
			Payload: []byte{0x17, 0x00, 0x00, 0x00, 0x00},
		})
		require.False(t, ok)

		// A non-sequence video frame is rejected by the seq parser.
		_, _, _, _, ok = parseVideoSeq(rtmpVideo(0, 0, base.RtmpAvcPacketTypeNalu, true, naluAVCC([]byte{0x65, 0x88})))
		require.False(t, ok)
	})

	t.Run("videoNALUs", func(t *testing.T) {
		_, ok := videoNALUs(base.RtmpMsg{})
		require.False(t, ok)

		nals, ok := videoNALUs(rtmpVideo(0, 0, base.RtmpAvcPacketTypeNalu, true, naluAVCC([]byte{0x65, 0x88, 0x80})))
		require.True(t, ok)
		require.Len(t, nals, 1)
		require.Equal(t, []byte{0x65, 0x88, 0x80}, nals[0])

		// Non-NALU packet type is rejected.
		_, ok = videoNALUs(rtmpVideo(0, 0, 0, true, []byte{1, 2, 3}))
		require.False(t, ok)

		// A sequence header is not a NALU batch.
		seq, err := avc.BuildSeqHeaderFromSpsPps(groupTestSPS, groupTestPPS)
		require.NoError(t, err)
		_, ok = videoNALUs(base.RtmpMsg{
			Header:  base.RtmpHeader{MsgTypeId: base.RtmpTypeIdVideo, MsgLen: uint32(len(seq))},
			Payload: seq,
		})
		require.False(t, ok)

		// Malformed AVCC body is rejected.
		bad := rtmpVideo(0, 0, base.RtmpAvcPacketTypeNalu, true, []byte{0x00, 0x00, 0x00, 0x09, 0x65})
		_, ok = videoNALUs(bad)
		require.False(t, ok)
	})

	t.Run("isVideoKey isVCL", func(t *testing.T) {
		require.False(t, isVideoKey(base.RtmpMsg{}))
		require.True(t, isVideoKey(rtmpVideo(0, 0, base.RtmpAvcPacketTypeNalu, true, naluAVCC([]byte{0x65}))))
		require.False(t, isVideoKey(rtmpVideo(0, 0, base.RtmpAvcPacketTypeNalu, false, naluAVCC([]byte{0x41}))))

		_, ok := isVCL("h264", nil)
		require.False(t, ok)
		key, ok := isVCL("h265", []byte{40 << 1}) // VPS: not a VCL NALU
		require.False(t, ok)
		require.False(t, key)
		key, ok = isVCL("h265", []byte{19 << 1})
		require.True(t, ok)
		require.True(t, key)
		key, ok = isVCL("h265", []byte{1 << 1})
		require.True(t, ok)
		require.False(t, key)
		key, ok = isVCL("h264", []byte{0x41})
		require.True(t, ok)
		require.False(t, key)
		key, ok = isVCL("h264", []byte{0x65})
		require.True(t, ok)
		require.True(t, key)
		_, ok = isVCL("h264", []byte{0x67}) // SPS: not a VCL NALU
		require.False(t, ok)
	})
}

func TestCovGroupRTMP_AudioParsing(t *testing.T) {
	_, ok := parseAudio(base.RtmpMsg{})
	require.False(t, ok)

	audioMsg := func(formatByte byte, rest ...byte) base.RtmpMsg {
		payload := append([]byte{formatByte}, rest...)
		return base.RtmpMsg{
			Header:  base.RtmpHeader{MsgTypeId: base.RtmpTypeIdAudio, MsgLen: uint32(len(payload))},
			Payload: payload,
		}
	}

	// AAC sequence header (config) and raw frame.
	frame, ok := parseAudio(audioMsg(base.RtmpSoundFormatAac<<4, base.RtmpAacPacketTypeSeqHeader, 0x12, 0x10))
	require.True(t, ok)
	require.Equal(t, "aac", frame.codec)
	require.True(t, frame.isConfig)
	require.Equal(t, []byte{0x12, 0x10}, frame.config)

	frame, ok = parseAudio(audioMsg(base.RtmpSoundFormatAac<<4, base.RtmpAacPacketTypeRaw, 0x01, 0x02))
	require.True(t, ok)
	require.Equal(t, "aac", frame.codec)
	require.False(t, frame.isConfig)
	require.Equal(t, []byte{0x01, 0x02}, frame.frame)

	// AAC with an unknown packet type and an empty raw payload.
	_, ok = parseAudio(audioMsg(base.RtmpSoundFormatAac<<4, 0x05, 0x01))
	require.False(t, ok)
	_, ok = parseAudio(audioMsg(base.RtmpSoundFormatAac<<4, base.RtmpAacPacketTypeRaw))
	require.False(t, ok)

	// Opus.
	frame, ok = parseAudio(audioMsg(base.RtmpSoundFormatOpus<<4, 0xaa, 0xbb))
	require.True(t, ok)
	require.Equal(t, "opus", frame.codec)
	require.Equal(t, []byte{0xaa, 0xbb}, frame.frame)

	// G.711 A-law and μ-law carry a 5-byte config.
	frame, ok = parseAudio(audioMsg(base.RtmpSoundFormatG711A<<4, 0x01, 0x02))
	require.True(t, ok)
	require.Equal(t, "g711", frame.codec)
	require.Equal(t, []byte{0, 0, 0, 0x1f, 0x40}, frame.config)

	frame, ok = parseAudio(audioMsg(base.RtmpSoundFormatG711U<<4, 0x01, 0x02))
	require.True(t, ok)
	require.Equal(t, "g711", frame.codec)
	require.Equal(t, []byte{1, 0, 0, 0x1f, 0x40}, frame.config)

	// Unsupported sound format.
	_, ok = parseAudio(audioMsg(0x90, 0x01))
	require.False(t, ok)
}
