package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/sfubridge"
	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
	"github.com/q191201771/lal/pkg/logic"
	lalmaxserver "github.com/q191201771/lalmax/server"
	"github.com/q191201771/lalmax/sfu"
	"github.com/q191201771/lalmax/sfu/access"
	"github.com/q191201771/lalmax/sfu/signaling"
	"github.com/q191201771/lalmax/sfu/webrtcbridge"
)

var nvrSFURoom = sfu.Key{Namespace: "nvr", ID: "default"}

func nvrSFUVideoFeedback() []webrtc.RTCPFeedback {
	return []webrtc.RTCPFeedback{{Type: "nack", Parameter: "pli"}, {Type: "ccm", Parameter: "fir"}}
}

type nvrSFURuntime struct {
	rooms       *sfu.Manager
	roomAdmin   *nvrSFURoomAdmin
	room        *sfu.Room
	registry    *nvrSFUBridgeRouter
	signal      http.Handler
	tickets     *nvrSFUTickets
	policy      *nvrSFUPolicy
	engine      *media.EmbeddedLalmax
	server      *lalmaxserver.LalMaxServer
	idleTimeout time.Duration
}

type nvrSFUPolicy struct {
	app        *App
	generation string
	rooms      *sfu.Manager
}

// Only registered SFU outputs use a shared RTP sender-report clock. Other
// internal outputs, including transcodes, must keep their existing timing.
func (a *App) sfuRecordingSourceTime(ctx context.Context, streamID string) (bool, error) {
	if len(streamID) != 52 || !strings.HasPrefix(streamID, "sfu_") {
		return false, nil
	}
	if a.db == nil {
		return false, errors.New("SFU output registry unavailable")
	}
	return a.db.IsSFUBridgeOutput(ctx, streamID)
}

// The bridge is tied to the media-server instance on which it was created.
// Never register on an unready or replacement server: changing the owner while
// a custom session is alive could remove or feed the wrong Group.
type nvrSFUPublisher struct {
	engine *media.EmbeddedLalmax
	server *lalmaxserver.LalMaxServer
}

func (p *nvrSFUPublisher) AddCustomizePubSession(stream string) (logic.ICustomizePubSessionContext, error) {
	if p.engine.Server() != p.server || !p.server.Ready() {
		return nil, errors.New("SFU media server unavailable")
	}
	return p.server.AddCustomizePubSession(stream)
}
func (p *nvrSFUPublisher) DelCustomizePubSession(session logic.ICustomizePubSessionContext) {
	p.server.DelCustomizePubSession(session)
}

func (p *nvrSFUPolicy) check(ctx context.Context, principal string) error {
	switch {
	case strings.HasPrefix(principal, "user:"):
		id, err := strconv.ParseInt(strings.TrimPrefix(principal, "user:"), 10, 64)
		if err != nil || id <= 0 {
			return errors.New("unauthorized")
		}
		user, err := p.app.db.GetUserByID(ctx, id)
		if err != nil || user == nil || !user.Enabled || user.Role != model.RoleSuperAdmin {
			return errors.New("unauthorized")
		}
		return nil
	case strings.HasPrefix(principal, "legacy:"):
		count, err := p.app.db.CountUsers(ctx)
		if err != nil || count != 0 || strings.TrimPrefix(principal, "legacy:") != p.app.cfg.Auth.Username {
			return errors.New("unauthorized")
		}
		return nil
	default:
		return errors.New("unauthorized")
	}
}
func (p *nvrSFUPolicy) Authorize(ctx context.Context, r access.Request) error {
	validRoom := r.Room == nvrSFURoom && r.RoomGeneration == p.generation
	if p.rooms != nil {
		room, ok := p.rooms.Get(r.Room)
		validRoom = ok && r.Room.Namespace == nvrSFURoom.Namespace && room.Snapshot().Generation == r.RoomGeneration
	}
	if !validRoom || r.ParticipantID == "" {
		return errors.New("scope denied")
	}
	if err := p.check(ctx, r.Principal); err != nil {
		return err
	}
	switch r.Operation {
	case access.Join, access.Unpublish, access.Subscribe, access.Unsubscribe:
		return nil
	case access.Publish:
		if r.Kind == "audio" && r.Source == "microphone" && strings.EqualFold(r.Codec, "audio/opus") {
			return nil
		}
		if r.Kind == "video" && (r.Source == "camera" || r.Source == "screen") && strings.EqualFold(r.Codec, "video/h264") {
			return nil
		}
	case access.ManageBridge:
		if len(r.TargetGroup) == 52 && strings.HasPrefix(r.TargetGroup, "sfu_") {
			if _, err := hex.DecodeString(r.TargetGroup[4:]); err == nil {
				return nil
			}
		}
	}
	return errors.New("operation denied")
}

// Only constrained baseline, packetization mode 1 and a 90 kHz clock are
// accepted. This checks negotiation, not bitstream conformance; the H264
// adapter additionally rejects B and unsupported slices while forwarding.
func nvrSFUValidateH264(ctx context.Context, p sfu.Publication) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c := p.Negotiated
	if !p.RTPReady || p.Kind != "video" || !strings.EqualFold(c.MimeType, "video/H264") || c.ClockRate != 90000 || c.Channels != 0 {
		return errors.New("source codec is not ready or unsupported")
	}
	params := map[string]string{}
	for _, part := range strings.Split(c.FMTP, ";") {
		pair := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(pair) != 2 {
			return errors.New("invalid fmtp")
		}
		key := strings.ToLower(strings.TrimSpace(pair[0]))
		value := strings.TrimSpace(pair[1])
		if key == "" || value == "" {
			return errors.New("invalid fmtp")
		}
		if _, exists := params[key]; exists {
			return errors.New("duplicate fmtp")
		}
		params[key] = value
	}
	profile, err := hex.DecodeString(params["profile-level-id"])
	if params["packetization-mode"] != "1" || err != nil || len(profile) != 3 || profile[0] != 0x42 || profile[1]&0x40 == 0 {
		return errors.New("unsupported H264 profile")
	}
	return nil
}

func nvrSFUValidateBridge(ctx context.Context, p sfu.Publication) error {
	if p.Kind != "audio" {
		return nvrSFUValidateH264(ctx, p)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	c := p.Negotiated
	if !p.RTPReady || !strings.EqualFold(p.Codec, "audio/opus") || !strings.EqualFold(c.MimeType, "audio/opus") || c.ClockRate != 48000 || c.Channels != 2 {
		return errors.New("source codec is not ready or unsupported")
	}
	return nil
}
func nvrSFUOrigin(r *http.Request) bool {
	origin, err := url.Parse(r.Header.Get("Origin"))
	if err != nil || origin.User != nil || origin.Host == "" || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return false
	}
	if r.TLS != nil && origin.Scheme != "https" {
		return false
	}
	return (origin.Scheme == "http" || origin.Scheme == "https") && strings.EqualFold(origin.Host, r.Host)
}
func nvrSFUIdentityForUser(user *model.User) (signaling.Identity, error) {
	if user == nil || !user.Enabled || user.Role != model.RoleSuperAdmin || user.ID < 0 {
		return signaling.Identity{}, errors.New("unauthorized")
	}
	principal := ""
	if user.ID > 0 {
		principal = "user:" + strconv.FormatInt(user.ID, 10)
	} else if user.Username != "" {
		principal = "legacy:" + user.Username
	} else {
		return signaling.Identity{}, errors.New("unauthorized")
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return signaling.Identity{}, err
	}
	return signaling.Identity{Principal: principal, Room: nvrSFURoom, Participant: sfu.Participant{ID: hex.EncodeToString(id[:]), Name: user.DisplayName}}, nil
}
func nvrSFUNewPeer(servers []webrtc.ICEServer) func(context.Context, signaling.Identity) (*webrtc.PeerConnection, error) {
	return nvrSFUNewPeerObservingBWE(servers, nil)
}
func nvrSFUNewPeerObservingBWE(servers []webrtc.ICEServer, onEstimate func(signaling.Identity, int)) func(context.Context, signaling.Identity) (*webrtc.PeerConnection, error) {
	return nvrSFUNewPeerWithPacing(servers, onEstimate, false)
}
func nvrSFUNewPeerWithPacing(servers []webrtc.ICEServer, onEstimate func(signaling.Identity, int), enablePacing bool) func(context.Context, signaling.Identity) (*webrtc.PeerConnection, error) {
	return func(_ context.Context, identity signaling.Identity) (*webrtc.PeerConnection, error) {
		engine := &webrtc.MediaEngine{}
		if err := engine.RegisterCodec(webrtc.RTPCodecParameters{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, PayloadType: 111}, webrtc.RTPCodecTypeAudio); err != nil {
			return nil, err
		}
		if err := engine.RegisterCodec(webrtc.RTPCodecParameters{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f", RTCPFeedback: nvrSFUVideoFeedback()}, PayloadType: 96}, webrtc.RTPCodecTypeVideo); err != nil {
			return nil, err
		}
		registry := &interceptor.Registry{}
		if onEstimate != nil {
			registry.Add(&sfuBWEFactory{onEstimate: func(bitrate int) { onEstimate(identity, bitrate) }, pacing: enablePacing})
		}
		if err := webrtc.ConfigureTWCCHeaderExtensionSender(engine, registry); err != nil {
			return nil, err
		}
		// NACK may resend one cached header concurrently; isolate it before TWCC mutates it.
		registry.Add(webrtcbridge.CopyRTPHeaderFactory{})
		if err := webrtc.RegisterDefaultInterceptors(engine, registry); err != nil {
			return nil, err
		}
		settings := webrtc.SettingEngine{}
		settings.SetIncludeLoopbackCandidate(true)
		return webrtc.NewAPI(webrtc.WithSettingEngine(settings), webrtc.WithMediaEngine(engine), webrtc.WithInterceptorRegistry(registry)).NewPeerConnection(webrtc.Configuration{ICEServers: servers})
	}
}
func newNVRSFURuntime(a *App) (*nvrSFURuntime, error) {
	idleTimeout, err := a.cfg.SFU.RoomIdleTimeout()
	if err != nil {
		return nil, err
	}
	embedded, ok := a.mediaEngine.(*media.EmbeddedLalmax)
	if !ok || embedded.Server() == nil {
		return nil, errors.New("SFU requires embedded media engine")
	}
	rooms := sfu.NewManager()
	room, err := rooms.CreateWithLimits(nvrSFURoom, nvrSFURoomLimits)
	if err != nil {
		return nil, err
	}
	policy := &nvrSFUPolicy{app: a, generation: room.Snapshot().Generation, rooms: rooms}
	roomAdmin := newNVRSFURoomAdmin(rooms, room, policy)
	server := embedded.Server()
	newRegistry := func(ctx context.Context, target *sfu.Room) (nvrRoomBridge, error) {
		service, err := sfubridge.NewPersistent(ctx, target, policy, &nvrSFUPublisher{engine: embedded, server: server}, a.mediaAuth, nvrSFUValidateBridge, 8, a.db)
		if err != nil {
			return nil, err
		}
		registry, err := sfubridge.NewRegistry(service, func(ctx context.Context, r sfubridge.ManagementRequest) error {
			if r.Principal != r.Owner {
				return errors.New("not owner")
			}
			return policy.check(ctx, r.Principal)
		}, 64)
		if err != nil {
			_ = service.Close(context.Background())
			return nil, err
		}
		return registry, nil
	}
	registry := newNVRSFUBridgeRouter(policy.check)
	defaultRegistry, err := newRegistry(context.Background(), room)
	if err != nil {
		return nil, err
	}
	if err = registry.add(room, defaultRegistry); err != nil {
		_ = defaultRegistry.Close(context.Background())
		return nil, err
	}
	roomAdmin.bridges, roomAdmin.newBridge = registry, newRegistry
	iceServers := make([]webrtc.ICEServer, 0, len(a.cfg.SFU.ICEServers))
	for _, server := range a.cfg.SFU.ICEServers {
		iceServers = append(iceServers, webrtc.ICEServer{URLs: append([]string(nil), server.URLs...), Username: server.Username, Credential: server.Credential, CredentialType: webrtc.ICECredentialTypePassword})
	}
	newPeer := nvrSFUNewPeer(iceServers)
	if a.cfg.SFU.ObserveBandwidth {
		newPeer = nvrSFUNewPeerWithPacing(iceServers, func(id signaling.Identity, bitrate int) {
			slog.Debug("sfu bandwidth estimate", "namespace", id.Room.Namespace, "room", id.Room.ID, "participant", id.Participant.ID, "bitrate_bps", bitrate)
		}, a.cfg.SFU.Pacing)
	}
	factory := &signaling.RoomFactory{NewPeer: newPeer, MaxSubscribers: 32, QueueSize: 64, Codec: func(p sfu.Publication) (webrtc.RTPCodecCapability, error) {
		if p.Kind == "audio" && strings.EqualFold(p.Codec, "audio/opus") {
			return webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, nil
		}
		if p.Kind == "video" && strings.EqualFold(p.Codec, "video/h264") {
			if err := nvrSFUValidateH264(context.Background(), p); err != nil {
				return webrtc.RTPCodecCapability{}, err
			}
			return webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: p.Negotiated.FMTP, RTCPFeedback: nvrSFUVideoFeedback()}, nil
		}
		return webrtc.RTPCodecCapability{}, errors.New("unsupported codec")
	}, OnInboundPeer: func(ctx context.Context, _ signaling.Identity, pc *webrtc.PeerConnection, track *webrtc.TrackRemote, done <-chan error, err error) {
		if err != nil || track.Kind() != webrtc.RTPCodecTypeVideo {
			return
		}
		go func() {
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-done:
					return
				case <-ticker.C:
					if pc.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: uint32(track.SSRC())}}) != nil {
						return
					}
				}
			}
		}()
	}}
	tickets := newNVRSFUTickets(policy)
	tickets.resolveRoom = roomAdmin.resolve
	type identityKey struct{}
	handler := &signaling.Handler{Rooms: rooms, Policy: policy, CheckOrigin: nvrSFUOrigin, Authenticate: func(r *http.Request) (signaling.Identity, error) {
		identity, ok := r.Context().Value(identityKey{}).(signaling.Identity)
		if !ok {
			return signaling.Identity{}, errors.New("unauthorized")
		}
		return identity, nil
	}, NewSessionNegotiator: factory.NewNegotiator, Filter: func(ctx context.Context, id signaling.Identity, snapshot sfu.Snapshot) (sfu.Snapshot, error) {
		if err := policy.check(ctx, id.Principal); err != nil {
			return sfu.Snapshot{}, err
		}
		return snapshot, nil
	}}
	guarded := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !nvrSFUOrigin(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		query := r.URL.Query()
		if len(query) != 1 || len(query["ticket"]) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		identity, err := tickets.consume(r.Context(), query.Get("ticket"))
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		go func() {
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					checkCtx, done := context.WithTimeout(ctx, 2*time.Second)
					err := policy.check(checkCtx, identity.Principal)
					done()
					if err != nil {
						cancel()
						return
					}
				}
			}
		}()
		handler.ServeHTTP(w, r.WithContext(context.WithValue(ctx, identityKey{}, identity)))
	})
	return &nvrSFURuntime{rooms: rooms, room: room, roomAdmin: roomAdmin, registry: registry, signal: guarded, tickets: tickets, policy: policy, engine: embedded, server: server, idleTimeout: idleTimeout}, nil
}

// Start monitors account revocation and embedded media server replacement.
// A replacement invalidates the bound publisher; close the runtime rather
// than silently sending frames into an old server. A full NVR restart is
// required to construct a new runtime after an embedded media restart.
func (s *nvrSFURuntime) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.registry.PruneFinished(time.Now().Add(-5 * time.Minute))
				if s.engine.Server() != s.server {
					closeCtx, done := context.WithTimeout(context.Background(), 15*time.Second)
					_ = s.Close(closeCtx)
					done()
					return
				}
				checkCtx, done := context.WithTimeout(ctx, 3*time.Second)
				_ = s.registry.RevalidateOwners(checkCtx, s.policy.check)
				done()
				if s.idleTimeout > 0 {
					cleanupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
					if err := s.roomAdmin.collectIdle(cleanupCtx, s.idleTimeout); err != nil && ctx.Err() == nil {
						slog.Warn("SFU idle room cleanup incomplete", "error", err)
					}
					cancel()
				}
			}
		}
	}()
}
func (s *nvrSFURuntime) Close(ctx context.Context) error {
	s.tickets.Close()
	err := s.roomAdmin.closeAll()
	bridgeErr := s.registry.Close(ctx)
	if err != nil {
		return errors.Join(fmt.Errorf("close SFU rooms: %w", err), bridgeErr)
	}
	return bridgeErr
}
