package recorder

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/event"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/muxer"
	"github.com/q191201771/lal/pkg/base"
)

const groupGOPLimit = 300

// recSession is one camera's subscriber queue. OnMsg only enqueues.
// Disk IO and the write switch run on loop.
type recSession struct {
	cameraID  string
	subID     string
	audio     bool
	audioOnly bool
	segDur    time.Duration
	store     segmentStore
	db        recordingDB
	bus       *event.EventBus
	group     subscribeGroup
	owner     *GroupWriter

	queue   chan base.RtmpMsg
	stop    chan struct{}
	done    chan struct{}
	once    sync.Once
	writing atomic.Bool
	rotate  atomic.Bool

	flushed  bool
	videoSeq base.RtmpMsg
	audioSeq base.RtmpMsg
	gop      []base.RtmpMsg

	codec       string
	sps         []byte
	pps         []byte
	vps         []byte
	audioCodec  string
	audioConfig []byte

	mux        *muxer.MP4Muxer
	videoTrack int
	audioTrack int
	tempPath   string
	finalPath  string
	segStart   time.Time
	lastDts    time.Duration
	hasDts     bool
	frames     int
	pendingCut bool
}

func (s *recSession) halt() {
	s.once.Do(func() { close(s.stop) })
}

func (s *recSession) loop() {
	defer close(s.done)
	for {
		select {
		case <-s.stop:
			s.drain()
			s.closeSegment()
			return
		case msg := <-s.queue:
			s.handle(msg)
		}
	}
}

func (s *recSession) drain() {
	for {
		select {
		case msg := <-s.queue:
			s.handle(msg)
		default:
			return
		}
	}
}

func (s *recSession) handle(msg base.RtmpMsg) {
	if s.rotate.Swap(false) {
		s.closeSegment()
		s.flushed = false
	}
	if !s.writing.Load() {
		s.flushed = false
		s.remember(msg)
		if s.mux != nil {
			s.closeSegment()
		}
		return
	}
	if !s.flushed {
		s.flushHeld()
		s.flushed = true
	}
	s.apply(msg)
	s.remember(msg)
}

func (s *recSession) flushHeld() {
	if s.videoSeq.Header.MsgTypeId != 0 {
		s.apply(s.videoSeq)
	}
	if s.audio && s.audioSeq.Header.MsgTypeId != 0 {
		s.apply(s.audioSeq)
	}
	for _, msg := range s.gop {
		s.apply(msg)
	}
}

func (s *recSession) remember(msg base.RtmpMsg) {
	if msg.Header.MsgTypeId == base.RtmpTypeIdVideo && len(msg.Payload) >= 5 && msg.IsVideoKeySeqHeader() {
		s.videoSeq = msg
		return
	}
	if af, ok := parseAudio(msg); ok && af.isConfig {
		s.audioSeq = msg
		return
	}
	if isVideoKey(msg) {
		s.gop = []base.RtmpMsg{msg}
		return
	}
	if msg.Header.MsgTypeId != base.RtmpTypeIdVideo && msg.Header.MsgTypeId != base.RtmpTypeIdAudio {
		return
	}
	s.gop = append(s.gop, msg)
	if len(s.gop) > groupGOPLimit {
		s.gop = s.gop[len(s.gop)-groupGOPLimit:]
	}
}

func (s *recSession) apply(msg base.RtmpMsg) {
	if codec, sps, pps, vps, ok := parseVideoSeq(msg); ok {
		if s.codec != "" && (codec != s.codec || !bytesEqual(s.sps, sps) || !bytesEqual(s.pps, pps) || !bytesEqual(s.vps, vps)) {
			s.closeSegment()
		}
		s.codec, s.sps, s.pps, s.vps = codec, sps, pps, vps
		return
	}
	if s.audio {
		if af, ok := parseAudio(msg); ok {
			if af.isConfig {
				s.audioCodec = af.codec
				s.audioConfig = af.config
				return
			}
			s.writeAudio(msg, af)
			return
		}
	}
	nals, ok := videoNALUs(msg)
	if !ok {
		return
	}
	dts, pts, ok := videoTimes(msg)
	if !ok {
		return
	}
	key := isVideoKey(msg)
	if s.mux != nil && s.pendingCut && key {
		s.closeSegment()
	}
	first := true
	for _, nalu := range nals {
		vclKey, vcl := isVCL(s.codec, nalu)
		if !vcl {
			continue
		}
		if s.mux == nil && !(key || vclKey) {
			continue
		}
		if s.sps == nil || s.codec == "" {
			continue
		}
		if s.mux == nil {
			if err := s.openSegment(); err != nil {
				slog.Error("group recorder failed to open segment", "camera_id", s.cameraID, "error", err)
				return
			}
		}
		dur := time.Millisecond
		if first && s.hasDts {
			delta := dts - s.lastDts
			if delta > 0 {
				dur = delta
			}
		}
		if err := s.mux.WriteTimedSample(s.videoTrack, nalu, dts, pts, dur); err != nil {
			slog.Error("group recorder failed to write video", "camera_id", s.cameraID, "error", err)
			continue
		}
		first = false
		s.hasDts = true
		s.lastDts = dts
		s.frames++
	}
	if s.mux != nil && !s.pendingCut && time.Since(s.segStart) >= s.segDur {
		s.pendingCut = true
	}
}

func (s *recSession) writeAudio(msg base.RtmpMsg, af audioFrame) {
	if len(af.frame) == 0 {
		return
	}
	if s.audioOnly && s.mux == nil {
		s.codec = af.codec
		if err := s.openSegment(); err != nil {
			slog.Error("audio recording open failed", "error", err)
			return
		}
	}
	if s.mux == nil {
		return
	}
	if s.audioTrack == 0 {
		if af.codec == "" {
			af.codec = s.audioCodec
		}
		cfg := af.config
		if len(cfg) == 0 {
			cfg = s.audioConfig
		}
		if af.codec == "" || (af.codec == "aac" && len(cfg) == 0) {
			return
		}
		id, err := s.mux.AddAudioTrack(af.codec, cfg)
		if err != nil {
			slog.Error("group recorder failed to add audio track", "camera_id", s.cameraID, "error", err)
			return
		}
		s.audioTrack = id
		s.audioCodec = af.codec
	}
	dts := time.Duration(msg.Dts()) * time.Millisecond
	if err := s.mux.WriteTimedSample(s.audioTrack, af.frame, dts, dts, audioDuration(af)); err != nil {
		slog.Error("group recorder failed to write audio", "camera_id", s.cameraID, "error", err)
	}
	if s.audioOnly {
		s.frames++
		if time.Since(s.segStart) >= s.segDur {
			s.closeSegment()
		}
	}
}

func (s *recSession) openSegment() error {
	tempPath, finalPath, err := s.store.CreateSegment(s.cameraID, s.codec)
	if err != nil {
		return err
	}
	m := muxer.NewMP4Muxer(tempPath)
	var trackID int
	if s.audioOnly {
		// The first media packet supplies the audio track.
	} else if s.codec == string(model.FormatH265) {
		trackID, err = m.AddH265Track(s.vps, s.sps, s.pps)
	} else {
		trackID, err = m.AddH264Track(s.sps, s.pps)
	}
	if err != nil {
		os.Remove(tempPath)
		return err
	}
	s.mux = m
	s.videoTrack = trackID
	s.audioTrack = 0
	s.tempPath = tempPath
	s.finalPath = finalPath
	s.segStart = time.Now()
	s.frames = 0
	s.pendingCut = false
	s.hasDts = false
	return nil
}

func (s *recSession) closeSegment() {
	if s.mux == nil {
		return
	}
	frames := s.frames
	tempPath := s.tempPath
	finalPath := s.finalPath
	start := s.segStart
	codec := s.codec
	if err := s.mux.Close(); err != nil {
		slog.Error("group recorder failed to close segment", "camera_id", s.cameraID, "error", err)
		os.Remove(tempPath)
		s.resetSegment()
		return
	}
	s.resetSegment()
	if frames == 0 {
		os.Remove(tempPath)
		return
	}
	if err := s.store.CloseSegment(tempPath, finalPath); err != nil {
		slog.Error("group recorder failed to publish segment", "camera_id", s.cameraID, "error", err)
		return
	}
	now := time.Now()
	rec := &model.Recording{
		ID:         fmt.Sprintf("%d", now.UnixNano()),
		CameraID:   s.cameraID,
		StreamID:   s.cameraID,
		FilePath:   finalPath,
		Format:     model.Format(codec),
		StartedAt:  start,
		EndedAt:    now,
		Duration:   now.Sub(start).Seconds(),
		FrameCount: frames,
	}
	if info, err := os.Stat(finalPath); err == nil {
		rec.FileSize = info.Size()
	}
	if s.db != nil {
		if err := s.db.InsertRecordingWithRetry(context.Background(), rec, 3, 500*time.Millisecond); err != nil {
			slog.Error("group recorder failed to insert recording", "camera_id", s.cameraID, "error", err)
		}
	}
	if s.bus != nil {
		s.bus.Publish(context.Background(), event.TopicSegmentCompleted, event.SegmentCompleted{
			CameraID:    s.cameraID,
			FilePath:    finalPath,
			Format:      codec,
			StartedAt:   start.Format(time.RFC3339Nano),
			EndedAt:     now.Format(time.RFC3339Nano),
			FileSize:    rec.FileSize,
			RecordingID: rec.ID,
		})
	}
}

func (s *recSession) resetSegment() {
	s.mux = nil
	s.videoTrack = 0
	s.audioTrack = 0
	s.tempPath = ""
	s.finalPath = ""
	s.frames = 0
	s.pendingCut = false
	s.hasDts = false
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func audioDuration(af audioFrame) time.Duration {
	if af.codec == "g711" {
		return time.Duration(len(af.frame)) * time.Second / 8000
	}
	if af.codec == "opus" && len(af.frame) > 0 {
		c := af.frame[0] >> 3
		var us int
		switch {
		case c >= 16:
			us = 2500 << (c & 3)
		case c >= 12:
			us = 10000 << (c & 1)
		default:
			us = []int{10000, 20000, 40000, 60000}[c&3]
		}
		frames := 1
		switch af.frame[0] & 3 {
		case 1, 2:
			frames = 2
		case 3:
			if len(af.frame) < 2 {
				return time.Millisecond
			}
			frames = int(af.frame[1] & 63)
		}
		if frames > 0 && us*frames <= 120000 {
			return time.Duration(us*frames) * time.Microsecond
		}
	}
	return time.Millisecond
}
