package access

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/q191201771/lalmax/sfu"
	"github.com/q191201771/lalmax/sfu/bridge"
)

// StartAVBridgeWithContexts combines two already subscribed publications
// owned by the same participant. The caller validates negotiated codecs and
// that both tracks share a sender clock; authorization is checked per track.
func (s *Session) StartAVBridgeWithContexts(authCtx, lifetimeCtx context.Context, audioID, videoID, target string, publisher bridge.Publisher) (<-chan error, error) {
	if authCtx == nil || lifetimeCtx == nil || audioID == "" || videoID == "" || audioID == videoID || target == "" || publisher == nil {
		return nil, ErrInvalid
	}
	if err := lifetimeCtx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	snap, err := s.room.SnapshotSession(s.scope.ParticipantID, s.generation)
	if err != nil {
		return nil, err
	}
	var audio, video sfu.Publication
	for _, p := range snap.Publications {
		if p.ID == audioID {
			audio = p
		}
		if p.ID == videoID {
			video = p
		}
	}
	if audio.ID == "" || video.ID == "" {
		return nil, sfu.ErrPublicationNotFound
	}
	if audio.ParticipantID != video.ParticipantID || audio.Kind != "audio" || video.Kind != "video" || !strings.EqualFold(audio.Codec, "audio/opus") || !strings.EqualFold(video.Codec, "video/h264") {
		return nil, ErrBridgeCodec
	}
	for _, p := range []sfu.Publication{audio, video} {
		req := s.scope
		req.PublicationID, req.Kind, req.Source, req.Codec, req.TargetGroup = p.ID, p.Kind, p.Source, p.Codec, target
		for _, op := range []Operation{Subscribe, ManageBridge} {
			req.Operation = op
			if err = authorize(authCtx, s.policy, req); err != nil {
				return nil, err
			}
		}
		if s.bridges[p.ID] != nil {
			return nil, errors.New("sfu access: bridge already running")
		}
	}
	if err = lifetimeCtx.Err(); err != nil {
		return nil, err
	}
	aSub, err := s.room.OpenSubscriptionRTP(s.scope.ParticipantID, s.generation, audioID)
	if err != nil {
		return nil, err
	}
	vSub, err := s.room.OpenSubscriptionRTP(s.scope.ParticipantID, s.generation, videoID)
	if err != nil {
		aSub.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(lifetimeCtx)
	binding := &bridgeBinding{cancel: cancel}
	if s.bridges == nil {
		s.bridges = make(map[string]*bridgeBinding)
	}
	s.bridges[audioID] = binding
	s.bridges[videoID] = binding
	refs := func(ctx context.Context, videoSSRC uint32) (bridge.AVReferences, error) {
		if err := ctx.Err(); err != nil {
			return bridge.AVReferences{}, err
		}
		a, ok, err := s.room.SenderReportSession(s.scope.ParticipantID, s.generation, audioID)
		if err != nil {
			return bridge.AVReferences{}, err
		}
		if !ok {
			return bridge.AVReferences{}, bridge.ErrAVClockNotReady
		}
		if videoSSRC == 0 {
			return bridge.AVReferences{}, bridge.ErrAVClockNotReady
		}
		v, ok, err := s.room.SenderReportSSRCSession(s.scope.ParticipantID, s.generation, videoID, videoSSRC)
		if err != nil {
			return bridge.AVReferences{}, err
		}
		if !ok {
			return bridge.AVReferences{}, bridge.ErrAVClockNotReady
		}
		if time.Since(a.ReceivedAt) > 10*time.Second || time.Since(v.ReceivedAt) > 10*time.Second {
			return bridge.AVReferences{}, bridge.ErrAVClockNotReady
		}
		return bridge.AVReferences{Audio: bridge.ClockReference{NTP: a.NTP, RTP: a.RTP, Rate: audio.Negotiated.ClockRate}, Video: bridge.ClockReference{NTP: v.NTP, RTP: v.RTP, Rate: video.Negotiated.ClockRate}, AudioSSRC: a.SSRC, VideoSSRC: v.SSRC}, nil
	}
	result := make(chan error, 1)
	go func() {
		err := bridge.RunAVToLal(ctx, aSub, vSub, publisher, target, refs)
		cancel()
		s.mu.Lock()
		for _, id := range []string{audioID, videoID} {
			if s.bridges[id] == binding {
				delete(s.bridges, id)
			}
		}
		s.mu.Unlock()
		result <- err
		close(result)
	}()
	return result, nil
}
