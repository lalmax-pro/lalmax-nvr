package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/auth"
	"github.com/lalmax-pro/lalmax-nvr/internal/livekitsfu"
	"github.com/lalmax-pro/lalmax-nvr/internal/sfu"
	"github.com/lalmax-pro/lalmax-nvr/internal/sfubridge"
	"github.com/livekit/livekit-server/pkg/service"
	"github.com/livekit/protocol/livekit"
	"github.com/q191201771/lalmax/sfu/bridge"
)

type liveKitBridgeService struct {
	runtime       *nvrLiveKitRuntime
	room, roomSID string
	publisher     bridge.Publisher
	grants        *auth.MediaAuthorizer
	outputs       sfubridge.OutputStore
	ctx           context.Context
	cancel        context.CancelFunc
	mu            sync.Mutex
	closed        bool
	active        int
	wg            sync.WaitGroup
	closeOnce     sync.Once
	closeDone     chan struct{}
}

func newLiveKitBridgeService(runtime *nvrLiveKitRuntime, room, sid string, publisher bridge.Publisher, grants *auth.MediaAuthorizer, outputs sfubridge.OutputStore) (*liveKitBridgeService, error) {
	if runtime == nil || !validNVRSFURoomID(room) || sid == "" || publisher == nil || grants == nil || outputs == nil {
		return nil, sfubridge.ErrInvalid
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &liveKitBridgeService{runtime: runtime, room: room, roomSID: sid, publisher: publisher, grants: grants, outputs: outputs, ctx: ctx, cancel: cancel, closeDone: make(chan struct{})}, nil
}
func secureBridgeID() (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func (s *liveKitBridgeService) Start(ctx context.Context, principal, id string) (*sfubridge.Handle, error) {
	return s.start(ctx, principal, id, "")
}
func (s *liveKitBridgeService) StartAV(ctx context.Context, principal, video, audio string) (*sfubridge.Handle, error) {
	if audio == "" || audio == video {
		return nil, sfubridge.ErrInvalid
	}
	return s.start(ctx, principal, video, audio)
}

func (s *liveKitBridgeService) currentServer(ctx context.Context) (*service.LivekitServer, error) {
	s.runtime.mu.Lock()
	server, closed := s.runtime.server, s.runtime.closed
	s.runtime.mu.Unlock()
	if closed || server == nil || !server.IsRunning() {
		return nil, sfubridge.ErrClosed
	}
	room := server.RoomManager().GetRoom(ctx, livekit.RoomName(s.room))
	if room == nil || string(room.ID()) != s.roomSID {
		return nil, sfu.ErrRoomNotFound
	}
	return server, nil
}

func liveKitLoopbackURL(runtime *nvrLiveKitRuntime, port int) string {
	host := "127.0.0.1"
	if len(runtime.config.BindAddresses) > 0 {
		host = runtime.config.BindAddresses[0]
		for _, addr := range runtime.config.BindAddresses {
			if addr == "" || addr == "0.0.0.0" || addr == "127.0.0.1" {
				host = "127.0.0.1"
				break
			}
			if addr == "::" || addr == "::1" {
				host = "::1"
			}
		}
		if host == "" || host == "0.0.0.0" {
			host = "127.0.0.1"
		}
		if host == "::" {
			host = "::1"
		}
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(port))
}

func (s *liveKitBridgeService) start(setup context.Context, principal, first, audio string) (*sfubridge.Handle, error) {
	if setup == nil || principal == "" || first == "" {
		return nil, sfubridge.ErrInvalid
	}
	if err := s.runtime.policy.check(setup, principal); err != nil {
		return nil, sfubridge.ErrDenied
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, sfubridge.ErrClosed
	}
	if s.active >= 8 {
		s.mu.Unlock()
		return nil, sfubridge.ErrLimit
	}
	s.active++
	s.wg.Add(1)
	s.mu.Unlock()
	handled := false
	defer func() {
		if !handled {
			s.finish()
		}
	}()
	current, err := s.currentServer(setup)
	if err != nil {
		return nil, err
	}
	participants, err := current.RoomManager().ListParticipants(setup, &livekit.ListParticipantsRequest{Room: s.room})
	if err != nil {
		return nil, sfu.ErrRoomNotFound
	}
	var firstTrack *livekit.TrackInfo
	for _, p := range participants.Participants {
		for _, tr := range p.Tracks {
			if tr.Sid == first {
				if firstTrack != nil {
					return nil, sfubridge.ErrInvalid
				}
				firstTrack = tr
			}
		}
	}
	if firstTrack == nil {
		return nil, sfu.ErrTrackNotFound
	}
	req := livekitsfu.TrackRequest{RoomSID: s.roomSID}
	if firstTrack.Type == livekit.TrackType_AUDIO && audio == "" {
		req.AudioSID = first
	} else if firstTrack.Type == livekit.TrackType_VIDEO {
		req.VideoSID = first
		req.AudioSID = audio
	} else {
		return nil, sfubridge.ErrInvalid
	}
	id, err := secureBridgeID()
	if err != nil {
		return nil, err
	}
	publish, err := secureBridgeID()
	if err != nil {
		return nil, err
	}
	playback, err := secureBridgeID()
	if err != nil {
		return nil, err
	}
	target := "sfu_" + id
	bridgeToken, err := s.runtime.issuer.IssueJoin(livekitsfu.JoinGrant{Room: s.room, Identity: "bridge_" + id, ValidFor: 5 * time.Minute, CanSubscribe: true, Hidden: true})
	if err != nil {
		return nil, err
	}
	life, cancel := context.WithCancel(s.ctx)
	tracks, err := livekitsfu.OpenTracks(setup, life, liveKitLoopbackURL(s.runtime, current.HTTPPort()), bridgeToken, req)
	if err != nil {
		cancel()
		return nil, mapLiveKitBridgeError(err)
	}
	defer func() {
		if !handled {
			cancel()
			tracks.Close()
		}
	}()
	if _, err = s.currentServer(setup); err != nil {
		return nil, err
	}
	if err = s.runtime.policy.check(setup, principal); err != nil {
		return nil, sfubridge.ErrDenied
	}
	if err = s.grants.ReserveInternalStream(target, publish, playback); err != nil {
		return nil, fmt.Errorf("reserve bridge output: %w", err)
	}
	defer func() {
		if !handled {
			s.grants.RevokeInternalStream(target)
		}
	}()
	if err = s.outputs.RegisterSFUBridgeOutput(setup, target); err != nil {
		return nil, fmt.Errorf("record bridge output: %w", err)
	}
	if err = setup.Err(); err != nil {
		return nil, err
	}
	if err = life.Err(); err != nil {
		return nil, sfubridge.ErrClosed
	}
	result := make(chan error, 1)
	handle, err := sfubridge.NewHandle(target, playback, result, cancel)
	if err != nil {
		return nil, err
	}
	handled = true
	go func() {
		err := tracks.RunToGroup(life, s.publisher, target)
		cancel()
		tracks.Close()
		s.grants.RevokeInternalStream(target)
		s.finish()
		result <- err
		close(result)
	}()
	return handle, nil
}
func mapLiveKitBridgeError(err error) error {
	switch {
	case errors.Is(err, livekitsfu.ErrRoomChanged):
		return sfu.ErrRoomNotFound
	case errors.Is(err, livekitsfu.ErrTrackNotFound):
		return sfu.ErrTrackNotFound
	case errors.Is(err, livekitsfu.ErrTrackCodec), errors.Is(err, livekitsfu.ErrTrackPair):
		return sfubridge.ErrInvalid
	default:
		return err
	}
}
func (s *liveKitBridgeService) finish() { s.mu.Lock(); s.active--; s.mu.Unlock(); s.wg.Done() }
func (s *liveKitBridgeService) Close(ctx context.Context) error {
	if ctx == nil {
		return sfubridge.ErrInvalid
	}
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		s.cancel()
	}
	s.mu.Unlock()
	s.closeOnce.Do(func() { go func() { s.wg.Wait(); close(s.closeDone) }() })
	select {
	case <-s.closeDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
