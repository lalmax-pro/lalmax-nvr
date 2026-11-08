package livekitsfu

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/livekitsfu/bridge"
	"github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

var (
	ErrTrackNotFound  = errors.New("livekitsfu: track not found")
	ErrTrackEnded     = errors.New("livekitsfu: track ended")
	ErrTrackCodec     = errors.New("livekitsfu: unsupported track codec")
	ErrTrackPair      = errors.New("livekitsfu: incompatible track pair")
	ErrTrackQueueFull = errors.New("livekitsfu: bridge subscriber queue full")
	ErrRoomChanged    = errors.New("livekitsfu: room generation changed")
)

// TrackRequest binds a bridge to exact LiveKit room and publication SIDs.
// A pair must be camera+microphone or screen+screen audio from one participant.
type TrackRequest struct {
	RoomSID, VideoSID, AudioSID string
}

type senderReport struct {
	value    bridge.ClockReference
	ssrc     uint32
	received time.Time
}

type trackSubscriber struct {
	owner        *TrackSession
	id           string
	publisherSID string
	packets      chan *rtp.Packet
	ready        chan struct{}
	mu           sync.Mutex
	err          error
	report       senderReport
	started      bool // guarded by owner.mu
}

func (s *trackSubscriber) Packets() <-chan *rtp.Packet { return s.packets }
func (s *trackSubscriber) Err() error                  { s.mu.Lock(); defer s.mu.Unlock(); return s.err }
func (s *trackSubscriber) Close()                      { s.owner.Close() }
func (s *trackSubscriber) setError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err == nil {
		s.err = err
	}
}
func (s *trackSubscriber) setReport(packet rtcp.Packet, track *webrtc.TrackRemote) {
	report, ok := packet.(*rtcp.SenderReport)
	if !ok || report.NTPTime == 0 || report.SSRC != uint32(track.SSRC()) {
		return
	}
	s.mu.Lock()
	s.report = senderReport{value: bridge.ClockReference{NTP: report.NTPTime, RTP: report.RTPTime, Rate: track.Codec().ClockRate}, ssrc: report.SSRC, received: time.Now()}
	s.mu.Unlock()
}

// TrackSession is a dedicated LiveKit subscriber. It never publishes media and
// cancels both tracks when either source ends, the room reconnects, or the
// bounded bridge queue fills. Close disconnects the SDK and waits for readers.
type TrackSession struct {
	ctx          context.Context
	cancel       context.CancelFunc
	room         *lksdk.Room
	mu           sync.Mutex
	closed       bool
	subs         map[string]*trackSubscriber
	wg           sync.WaitGroup
	once         sync.Once
	done         chan struct{}
	video, audio *trackSubscriber
}

// OpenTracks connects with a caller-issued, subscribe-only token. The token
// must be restricted to the intended room and trusted by the target server.
// setup bounds the connection and subscription attempt. lifetime controls the
// established bridge and may outlive the incoming HTTP request.
func OpenTracks(setup, lifetime context.Context, endpoint, token string, req TrackRequest) (*TrackSession, error) {
	if setup == nil || lifetime == nil || endpoint == "" || token == "" || req.RoomSID == "" || (req.AudioSID == "" && req.VideoSID == "") || (req.AudioSID != "" && req.AudioSID == req.VideoSID) {
		return nil, ErrInvalidJoinGrant
	}
	if err := setup.Err(); err != nil {
		return nil, err
	}
	if err := lifetime.Err(); err != nil {
		return nil, err
	}
	life, cancel := context.WithCancel(lifetime)
	s := &TrackSession{ctx: life, cancel: cancel, subs: make(map[string]*trackSubscriber), done: make(chan struct{})}
	cb := lksdk.NewRoomCallback()
	changed := make(chan struct{}, 1)
	notify := func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	}
	cb.OnTrackPublished = func(_ *lksdk.RemoteTrackPublication, _ *lksdk.RemoteParticipant) { notify() }
	cb.OnParticipantConnected = func(_ *lksdk.RemoteParticipant) { notify() }
	cb.OnTrackSubscribed = s.onSubscribed
	cb.OnTrackUnsubscribed = func(_ *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, _ *lksdk.RemoteParticipant) {
		s.trackEnded(pub.SID())
	}
	cb.OnTrackUnpublished = func(pub *lksdk.RemoteTrackPublication, _ *lksdk.RemoteParticipant) { s.trackEnded(pub.SID()); notify() }
	cb.OnDisconnected = func() { s.fail(ErrTrackEnded) }
	cb.OnReconnecting = func() { s.fail(ErrTrackEnded) }
	room := lksdk.NewRoom(cb)
	joinCtx, stopJoin := context.WithTimeout(setup, 10*time.Second)
	defer stopJoin()
	joined := make(chan error, 1)
	go func() {
		joined <- room.JoinWithContextAndToken(joinCtx, endpoint, token, lksdk.WithAutoSubscribe(false), lksdk.WithConnectTimeout(10*time.Second))
	}()
	var joinErr error
	select {
	case joinErr = <-joined:
	case <-setup.Done():
		stopJoin()
		cancel()
		go func() { <-joined; room.Disconnect() }()
		return nil, setup.Err()
	case <-life.Done():
		stopJoin()
		go func() { <-joined; room.Disconnect() }()
		return nil, life.Err()
	}
	if joinErr != nil {
		room.Disconnect()
		cancel()
		if err := setup.Err(); err != nil {
			return nil, err
		}
		if err := life.Err(); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("livekitsfu: connect bridge subscriber: %w", joinErr)
	}
	s.room = room
	go func() { <-life.Done(); s.Close() }()
	ok := false
	defer func() {
		if !ok {
			s.Close()
		}
	}()
	if err := life.Err(); err != nil {
		return nil, err
	}
	if err := setup.Err(); err != nil {
		return nil, err
	}
	roomSID := make(chan string, 1)
	go func() { roomSID <- room.SID() }()
	var sid string
	select {
	case sid = <-roomSID:
	case <-setup.Done():
		return nil, setup.Err()
	case <-life.Done():
		return nil, life.Err()
	}
	if sid != req.RoomSID {
		return nil, ErrRoomChanged
	}
	type selected struct {
		pub         *lksdk.RemoteTrackPublication
		participant *lksdk.RemoteParticipant
	}
	var selectedTracks map[string]selected
	discovery := time.NewTimer(3 * time.Second)
	defer discovery.Stop()
	refresh := time.NewTicker(100 * time.Millisecond)
	defer refresh.Stop()
	for {
		selectedTracks = make(map[string]selected)
		for _, participant := range room.GetRemoteParticipants() {
			for _, item := range participant.TrackPublications() {
				if item.SID() != req.VideoSID && item.SID() != req.AudioSID {
					continue
				}
				pub, valid := item.(*lksdk.RemoteTrackPublication)
				if !valid || selectedTracks[item.SID()].pub != nil {
					return nil, ErrTrackPair
				}
				selectedTracks[item.SID()] = selected{pub: pub, participant: participant}
			}
		}
		if (req.VideoSID == "" || selectedTracks[req.VideoSID].pub != nil) && (req.AudioSID == "" || selectedTracks[req.AudioSID].pub != nil) {
			break
		}
		select {
		case <-changed:
		case <-refresh.C:
		case <-discovery.C:
			return nil, ErrTrackNotFound
		case <-setup.Done():
			return nil, setup.Err()
		case <-life.Done():
			return nil, life.Err()
		}
	}
	if req.VideoSID != "" && req.AudioSID != "" {
		video, audio := selectedTracks[req.VideoSID], selectedTracks[req.AudioSID]
		if video.participant.SID() != audio.participant.SID() || !validSourcePair(video.pub.Source(), audio.pub.Source()) {
			return nil, ErrTrackPair
		}
	}
	for _, id := range []string{req.VideoSID, req.AudioSID} {
		if id == "" {
			continue
		}
		entry := selectedTracks[id]
		if (id == req.VideoSID && entry.pub.Kind() != lksdk.TrackKindVideo) || (id == req.AudioSID && entry.pub.Kind() != lksdk.TrackKindAudio) {
			return nil, ErrTrackCodec
		}
		sub := &trackSubscriber{owner: s, id: id, publisherSID: entry.participant.SID(), packets: make(chan *rtp.Packet, 128), ready: make(chan struct{})}
		s.mu.Lock()
		s.subs[id] = sub
		s.mu.Unlock()
		if id == req.VideoSID {
			s.video = sub
		} else {
			s.audio = sub
		}
	}
	for _, id := range []string{req.VideoSID, req.AudioSID} {
		if id == "" {
			continue
		}
		if err := selectedTracks[id].pub.SetSubscribed(true); err != nil {
			return nil, fmt.Errorf("livekitsfu: subscribe: %w", err)
		}
	}
	for _, sub := range []*trackSubscriber{s.video, s.audio} {
		if sub == nil {
			continue
		}
		select {
		case <-sub.ready:
		case <-setup.Done():
			return nil, setup.Err()
		case <-life.Done():
			if err := sub.Err(); err != nil {
				return nil, err
			}
			return nil, life.Err()
		}
	}
	if err := life.Err(); err != nil {
		return nil, err
	}
	if err := setup.Err(); err != nil {
		return nil, err
	}
	ok = true
	return s, nil
}

func validSourcePair(video, audio livekit.TrackSource) bool {
	return video == livekit.TrackSource_CAMERA && audio == livekit.TrackSource_MICROPHONE || video == livekit.TrackSource_SCREEN_SHARE && audio == livekit.TrackSource_SCREEN_SHARE_AUDIO
}

func validateBridgeCodec(track *webrtc.TrackRemote) error {
	codec := track.Codec().RTPCodecCapability
	if track.Kind() == webrtc.RTPCodecTypeAudio {
		if strings.EqualFold(codec.MimeType, "audio/opus") && codec.ClockRate == 48000 && codec.Channels == 2 {
			return nil
		}
		return ErrTrackCodec
	}
	if track.Kind() != webrtc.RTPCodecTypeVideo || !strings.EqualFold(codec.MimeType, "video/h264") || codec.ClockRate != 90000 || codec.Channels != 0 {
		return ErrTrackCodec
	}
	params := map[string]string{}
	for _, part := range strings.Split(codec.SDPFmtpLine, ";") {
		pair := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(pair) != 2 {
			return ErrTrackCodec
		}
		key := strings.ToLower(strings.TrimSpace(pair[0]))
		value := strings.TrimSpace(pair[1])
		_, duplicate := params[key]
		if key == "" || value == "" || duplicate {
			return ErrTrackCodec
		}
		params[key] = value
	}
	profile, err := hex.DecodeString(params["profile-level-id"])
	if params["packetization-mode"] != "1" || err != nil || len(profile) != 3 || profile[0] != 0x42 || profile[1]&0x40 == 0 {
		return ErrTrackCodec
	}
	return nil
}

func (s *TrackSession) onSubscribed(track *webrtc.TrackRemote, pub *lksdk.RemoteTrackPublication, participant *lksdk.RemoteParticipant) {
	s.mu.Lock()
	sub := s.subs[pub.SID()]
	if s.closed || sub == nil || sub.started {
		s.mu.Unlock()
		return
	}
	if participant.SID() != sub.publisherSID {
		s.mu.Unlock()
		s.fail(ErrTrackPair)
		return
	}
	if err := validateBridgeCodec(track); err != nil {
		s.mu.Unlock()
		s.fail(err)
		return
	}
	sub.started = true
	pub.OnRTCP(func(packet rtcp.Packet) { sub.setReport(packet, track) })
	s.wg.Add(1)
	if track.Kind() == webrtc.RTPCodecTypeVideo {
		s.wg.Add(1)
	}
	close(sub.ready)
	s.mu.Unlock()
	go s.readTrack(sub, track)
	if track.Kind() == webrtc.RTPCodecTypeVideo {
		go s.requestKeyFrames(track, participant)
	}
}
func (s *TrackSession) readTrack(sub *trackSubscriber, track *webrtc.TrackRemote) {
	defer s.wg.Done()
	defer close(sub.packets)
	for {
		packet, _, err := track.ReadRTP()
		if err != nil {
			if s.ctx.Err() != nil {
				sub.setError(s.ctx.Err())
			} else if errors.Is(err, io.EOF) {
				s.fail(ErrTrackEnded)
			} else {
				s.fail(err)
			}
			return
		}
		select {
		case sub.packets <- packet:
		case <-s.ctx.Done():
			sub.setError(s.ctx.Err())
			return
		default:
			s.fail(ErrTrackQueueFull)
			return
		}
	}
}
func (s *TrackSession) requestKeyFrames(track *webrtc.TrackRemote, participant *lksdk.RemoteParticipant) {
	defer s.wg.Done()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			participant.WritePLI(track.SSRC())
		}
	}
}
func (s *TrackSession) trackEnded(id string) {
	s.mu.Lock()
	_, selected := s.subs[id]
	s.mu.Unlock()
	if selected {
		s.fail(ErrTrackEnded)
	}
}
func (s *TrackSession) fail(err error) {
	s.mu.Lock()
	for _, sub := range s.subs {
		sub.setError(err)
	}
	s.mu.Unlock()
	s.cancel()
}

func (s *TrackSession) Video() bridge.RTPSubscriber {
	if s.video == nil {
		return nil
	}
	return s.video
}
func (s *TrackSession) Audio() bridge.RTPSubscriber {
	if s.audio == nil {
		return nil
	}
	return s.audio
}

// RunToGroup consumes the selected track or synchronized pair and owns its
// lal custom publisher until the source ends or ctx is canceled. The caller
// must authorize and reserve the target stream before calling it.
func (s *TrackSession) RunToGroup(ctx context.Context, publisher bridge.Publisher, target string) error {
	if s == nil {
		return bridge.ErrInvalidPublisher
	}
	switch {
	case s.audio != nil && s.video != nil:
		return bridge.RunAVToLal(ctx, s.Audio(), s.Video(), publisher, target, s.AVReferences)
	case s.audio != nil:
		return bridge.RunOpusToLal(ctx, s.Audio(), publisher, target)
	case s.video != nil:
		return bridge.RunH264ToLal(ctx, s.Video(), publisher, target)
	default:
		return bridge.ErrInvalidPublisher
	}
}

// AVReferences returns recent sender reports for the two selected tracks. The
// pair is restricted to one publisher and media source during subscription.
func (s *TrackSession) AVReferences(ctx context.Context) (bridge.AVReferences, error) {
	if ctx == nil || s.audio == nil || s.video == nil {
		return bridge.AVReferences{}, bridge.ErrInvalidClock
	}
	if err := ctx.Err(); err != nil {
		return bridge.AVReferences{}, err
	}
	s.audio.mu.Lock()
	a := s.audio.report
	s.audio.mu.Unlock()
	s.video.mu.Lock()
	v := s.video.report
	s.video.mu.Unlock()
	if a.received.IsZero() || v.received.IsZero() || time.Since(a.received) > 10*time.Second || time.Since(v.received) > 10*time.Second {
		return bridge.AVReferences{}, bridge.ErrAVClockNotReady
	}
	return bridge.AVReferences{Audio: a.value, Video: v.value, AudioSSRC: a.ssrc, VideoSSRC: v.ssrc}, nil
}

func (s *TrackSession) Close() {
	if s == nil {
		return
	}
	s.once.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		s.cancel()
		if s.room != nil {
			s.room.Disconnect()
		}
		s.wg.Wait()
		close(s.done)
	})
	<-s.done
}
