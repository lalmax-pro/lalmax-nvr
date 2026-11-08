package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/api"
	coreauth "github.com/lalmax-pro/lalmax-nvr/internal/auth"
	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/middleware"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/sfu"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/lal/pkg/logic"
)

type liveKitBridgeHook struct{ messages chan base.RtmpMsg }

type failingSFUOutputStore struct{ attempted chan string }

func (s *failingSFUOutputStore) RegisterSFUBridgeOutput(_ context.Context, id string) error {
	s.attempted <- id
	return errors.New("storage unavailable")
}
func (*failingSFUOutputStore) ListSFUBridgeOutputIDs(context.Context) ([]string, error) {
	return nil, nil
}

func (h *liveKitBridgeHook) OnMsg(msg base.RtmpMsg) {
	select {
	case h.messages <- msg.Clone():
	default:
	}
}
func (*liveKitBridgeHook) OnStop() {}

func TestNVRLiveKitJoinAndRevocation(t *testing.T) {
	t.Chdir(t.TempDir())
	ctx := context.Background()
	db, err := storage.New(filepath.Join(t.TempDir(), "nvr.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Init(ctx); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "alice", PasswordHash: "hash", Role: model.RoleSuperAdmin, Enabled: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := tcp.Addr().(*net.TCPAddr).Port
	if err := tcp.Close(); err != nil {
		t.Fatal(err)
	}
	udp, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	udpPort := udp.LocalAddr().(*net.UDPAddr).Port
	if err := udp.Close(); err != nil {
		t.Fatal(err)
	}
	const key, secret = "testkey", "testsecret-testsecret-testsecret-123456"
	data := fmt.Sprintf("development: false\nport: %d\nbind_addresses: [127.0.0.1]\nkeys:\n  %s: %s\nrtc:\n  node_ip: 127.0.0.1\n  enable_loopback_candidate: true\n  ips:\n    includes: [127.0.0.1/32]\n  tcp_port: 0\n  port_range_start: 0\n  port_range_end: 0\n  udp_port: %d\n", port, key, secret, udpPort)
	configPath := filepath.Join(t.TempDir(), "livekit.yaml")
	if err := os.WriteFile(configPath, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	runtime, err := newNVRLiveKitRuntime(&App{db: db, cfg: &config.Config{}}, config.SFULiveKitConfig{ConfigFile: configPath, APIKey: key, PublicURL: url})
	if err != nil {
		t.Fatal(err)
	}
	closed, err := newNVRLiveKitRuntime(&App{db: db, cfg: &config.Config{}}, config.SFULiveKitConfig{ConfigFile: configPath, APIKey: key, PublicURL: url})
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := closed.Start(ctx); err == nil {
		t.Fatal("closed runtime started")
	}
	if err := runtime.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := runtime.Close(closeCtx); err != nil {
			t.Error(err)
		}
	})
	principal := fmt.Sprintf("user:%d", user.ID)
	roomAdmin := &nvrLiveKitRoomAdmin{runtime: runtime}
	createdRoom, err := roomAdmin.CreateRoom(ctx, principal, "managed")
	if err != nil || createdRoom.Generation == "" {
		t.Fatalf("create room: %+v %v", createdRoom, err)
	}
	if _, err := roomAdmin.CreateRoom(ctx, principal, "managed"); !errors.Is(err, sfu.ErrRoomExists) {
		t.Fatalf("duplicate room: %v", err)
	}
	roomSnapshots, err := roomAdmin.ListRooms(ctx, principal)
	if err != nil || len(roomSnapshots) != 1 || roomSnapshots[0].Generation != createdRoom.Generation {
		t.Fatalf("list rooms: %+v %v", roomSnapshots, err)
	}
	if err := roomAdmin.CloseRoom(ctx, principal, "managed", "RM_stale"); !errors.Is(err, sfu.ErrRoomNotFound) {
		t.Fatalf("stale room close: %v", err)
	}
	if err := roomAdmin.CloseRoom(ctx, principal, "managed", createdRoom.Generation); err != nil {
		t.Fatal(err)
	}
	secondRoom, err := roomAdmin.CreateRoom(ctx, principal, "managed")
	if err != nil || secondRoom.Generation == createdRoom.Generation {
		t.Fatalf("recreate room: %+v %v", secondRoom, err)
	}
	if err := roomAdmin.CloseRoom(ctx, principal, "managed", createdRoom.Generation); !errors.Is(err, sfu.ErrRoomNotFound) {
		t.Fatalf("old generation closed replacement: %v", err)
	}
	if err := roomAdmin.CloseRoom(ctx, principal, "managed", secondRoom.Generation); err != nil {
		t.Fatal(err)
	}
	lalManager := logic.NewServerManager(func(o *logic.Option) {
		o.ConfRawContent = []byte(`{"hls":{"enable":true,"on_demand":true,"url_pattern":"/hls/"}}`)
	})
	hook := &liveKitBridgeHook{messages: make(chan base.RtmpMsg, 128)}
	lalManager.WithOnHookSession(func(_, _ string) logic.ICustomizeHookSessionContext { return hook })
	grants := coreauth.NewMediaAuthorizer()
	bridges, err := newNVRLiveKitBridgeRegistry(runtime, lalManager, grants, db)
	if err != nil {
		t.Fatal(err)
	}
	runtime.bridges = bridges
	noop := func(next http.Handler) http.Handler { return next }
	handler := api.NewHandler(nil, nil, noop, nil, nil, "", nil, nil)
	handler.SetMultiUserAuthMW(middleware.InjectUser(user))
	handler.SetLiveKitJoinIssuer(runtime)
	handler.SetSFURoomManager(roomAdmin)
	handler.SetSFUBridgeRegistry(bridges)
	req := httptest.NewRequest(http.MethodPost, "/api/sfu/livekit/join", strings.NewReader(`{"room":"front-door"}`))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.Routes().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("join endpoint status: %d", recorder.Code)
	}
	var join api.LiveKitJoin
	if err := json.Unmarshal(recorder.Body.Bytes(), &join); err != nil {
		t.Fatal(err)
	}
	received := make(chan []byte, 1)
	var watched atomic.Int64
	callback := lksdk.NewRoomCallback()
	callback.OnTrackSubscribed = func(track *webrtc.TrackRemote, _ *lksdk.RemoteTrackPublication, _ *lksdk.RemoteParticipant) {
		for {
			packet, _, err := track.ReadRTP()
			if err != nil {
				return
			}
			watched.Add(1)
			select {
			case received <- packet.Payload:
			default:
			}
		}
	}
	viewer, err := lksdk.ConnectToRoomWithToken(join.URL, join.Token, callback)
	if err != nil {
		t.Fatal(err)
	}
	defer viewer.Disconnect()
	pubJoin, err := runtime.IssueLiveKitJoin(ctx, principal, "front-door")
	if err != nil {
		t.Fatal(err)
	}
	if join.Identity == pubJoin.Identity {
		t.Fatal("participant identities collided")
	}
	publisher, err := lksdk.ConnectToRoomWithToken(pubJoin.URL, pubJoin.Token, lksdk.NewRoomCallback(), lksdk.WithAutoSubscribe(false))
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Disconnect()
	track, err := lksdk.NewLocalTrack(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2})
	if err != nil {
		t.Fatal(err)
	}
	publication, err := publisher.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{Name: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	payload := []byte{0xf8, 0xff, 0xfe}
	var sequence uint16
receive:
	for {
		select {
		case <-ticker.C:
			sequence++
			if err := track.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: sequence, Timestamp: uint32(sequence) * 960}, Payload: payload}, nil); err != nil {
				t.Fatal(err)
			}
		case got := <-received:
			if !bytes.Equal(got, payload) {
				t.Fatal("unexpected payload")
			}
			break receive
		case <-deadline.C:
			t.Fatal("track did not arrive")
		}
	}
	producerCtx, stopProducer := context.WithCancel(ctx)
	defer stopProducer()
	go func() {
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-producerCtx.Done():
				return
			case <-tick.C:
				sequence++
				_ = track.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: sequence, Timestamp: uint32(sequence) * 960}, Payload: payload}, nil)
			}
		}
	}()
	// A failed output identity write must prevent publication and revoke the
	// reserved playback grant even after the LiveKit subscription succeeded.
	failingStore := &failingSFUOutputStore{attempted: make(chan string, 1)}
	failingRegistry, err := newNVRLiveKitBridgeRegistry(runtime, lalManager, grants, failingStore)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := failingRegistry.CreateForRoom(ctx, principal, "front-door", viewer.SID(), publication.SID(), "", ""); err == nil {
		t.Fatal("bridge succeeded despite failed output persistence")
	}
	var failedStream string
	select {
	case failedStream = <-failingStore.attempted:
	case <-time.After(5 * time.Second):
		t.Fatal("bridge failed before output registration")
	}
	if group := lalManager.GetGroup("live", failedStream); group != nil && group.HasInSession() {
		t.Fatal("failed output created a publisher")
	}
	if !grants.InternalPlaybackRequired(failedStream) || grants.AuthorizePlayback("webrtc", failedStream, "anything") == nil {
		t.Fatal("failed output playback was not revoked")
	}
	if err := failingRegistry.Close(ctx); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"room_id": "front-door", "room_generation": viewer.SID(), "publication_id": publication.SID()})
	staleBody, _ := json.Marshal(map[string]string{"room_id": "front-door", "room_generation": "RM_stale", "publication_id": publication.SID()})
	staleRequest := httptest.NewRequest(http.MethodPost, "/api/sfu/bridges/", bytes.NewReader(staleBody))
	staleRequest.Header.Set("Content-Type", "application/json")
	staleResponse := httptest.NewRecorder()
	handler.Routes().ServeHTTP(staleResponse, staleRequest)
	if staleResponse.Code != http.StatusNotFound {
		t.Fatalf("stale room bridge: %d %s", staleResponse.Code, staleResponse.Body.String())
	}
	create := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/sfu/bridges/", bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", "bridge-test-1")
		w := httptest.NewRecorder()
		handler.Routes().ServeHTTP(w, r)
		return w
	}
	created := create()
	if created.Code != http.StatusAccepted {
		t.Fatalf("bridge create: %d %s", created.Code, created.Body.String())
	}
	var status struct {
		ID       string `json:"id"`
		StreamID string `json:"stream_id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.ID == "" || !strings.HasPrefix(status.StreamID, "sfu_") {
		t.Fatal("invalid bridge status")
	}
	retry := create()
	if retry.Code != http.StatusAccepted || retry.Body.String() != created.Body.String() {
		t.Fatalf("idempotent retry: %d", retry.Code)
	}
	conflictRequest := httptest.NewRequest(http.MethodPost, "/api/sfu/bridges/", bytes.NewReader(staleBody))
	conflictRequest.Header.Set("Content-Type", "application/json")
	conflictRequest.Header.Set("Idempotency-Key", "bridge-test-1")
	conflictResponse := httptest.NewRecorder()
	handler.Routes().ServeHTTP(conflictResponse, conflictRequest)
	if conflictResponse.Code != http.StatusConflict {
		t.Fatalf("idempotency conflict: %d %s", conflictResponse.Code, conflictResponse.Body.String())
	}
	select {
	case <-hook.messages:
	case <-time.After(10 * time.Second):
		t.Fatal("bridge group did not receive audio")
	}
	roomSnapshots, err = roomAdmin.ListRooms(ctx, principal)
	if err != nil || len(roomSnapshots) != 1 || roomSnapshots[0].ID != "front-door" || roomSnapshots[0].Participants != 2 {
		t.Fatalf("hidden bridge counted as participant: %+v %v", roomSnapshots, err)
	}
	stream, playToken, err := bridges.Playback(ctx, principal, status.ID)
	if err != nil || stream != status.StreamID || grants.AuthorizePlayback("webrtc", stream, playToken) != nil {
		t.Fatal("bridge playback grant missing")
	}
	if ok, err := db.IsSFUBridgeOutput(ctx, stream); err != nil || !ok {
		t.Fatal("bridge output was not persisted")
	}
	stopReq := httptest.NewRequest(http.MethodPost, "/api/sfu/bridges/"+status.ID+"/stop", strings.NewReader(`{}`))
	stopReq.Header.Set("Content-Type", "application/json")
	stopped := httptest.NewRecorder()
	handler.Routes().ServeHTTP(stopped, stopReq)
	if stopped.Code != http.StatusNoContent {
		t.Fatalf("bridge stop: %d", stopped.Code)
	}
	stopDeadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(stopDeadline) {
		group := lalManager.GetGroup("live", stream)
		if (group == nil || !group.HasInSession()) && grants.AuthorizePlayback("webrtc", stream, playToken) != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if grants.AuthorizePlayback("webrtc", stream, playToken) == nil {
		t.Fatal("bridge playback grant not revoked")
	}
	if group := lalManager.GetGroup("live", stream); group != nil && group.HasInSession() {
		t.Fatal("group publisher not released")
	}
	// Demoting the owner must cancel an active bridge and its playback grant,
	// while an independent participant keeps receiving the same source.
	revoked, err := bridges.CreateForRoom(ctx, principal, "front-door", viewer.SID(), publication.SID(), "", "")
	if err != nil {
		t.Fatalf("create bridge for revocation: %v", err)
	}
	revokedStream, revokedToken, err := bridges.Playback(ctx, principal, revoked.ID)
	if err != nil {
		t.Fatalf("get playback before revocation: %v", err)
	}
	revocationDeadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(revocationDeadline) {
		if group := lalManager.GetGroup("live", revokedStream); group != nil && group.HasInSession() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if group := lalManager.GetGroup("live", revokedStream); group == nil || !group.HasInSession() {
		t.Fatal("revocation test bridge never became active")
	}
	user.Role = model.RoleUser
	user.UpdatedAt = time.Now()
	if err := db.UpdateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	beforeRevocation := watched.Load()
	bridges.maintain(ctx, time.Now().Add(-5*time.Minute))
	revocationDeadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(revocationDeadline) {
		group := lalManager.GetGroup("live", revokedStream)
		if (group == nil || !group.HasInSession()) && grants.AuthorizePlayback("webrtc", revokedStream, revokedToken) != nil && watched.Load() > beforeRevocation {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if group := lalManager.GetGroup("live", revokedStream); group != nil && group.HasInSession() {
		t.Fatal("demoted owner retained the bridge publisher")
	}
	if grants.AuthorizePlayback("webrtc", revokedStream, revokedToken) == nil {
		t.Fatal("demoted owner retained the playback grant")
	}
	if watched.Load() <= beforeRevocation {
		t.Fatal("independent viewer stopped receiving after bridge revocation")
	}
	user.Role = model.RoleSuperAdmin
	if err := db.UpdateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	// Completed room generations and their indexes must not exhaust the limits
	// during a long-running deployment.
	pruneDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(pruneDeadline) {
		bridges.maintain(ctx, time.Now().Add(time.Second))
		bridges.mu.Lock()
		rooms, ids := len(bridges.rooms), len(bridges.byID)
		bridges.mu.Unlock()
		if rooms == 0 && ids == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	bridges.mu.Lock()
	rooms, ids := len(bridges.rooms), len(bridges.byID)
	bridges.mu.Unlock()
	if rooms != 0 || ids != 0 {
		t.Fatalf("terminal bridge not pruned: %d rooms, %d IDs", rooms, ids)
	}
	stopProducer()
	if join.Room != "front-door" || join.Identity == "" {
		t.Fatal("invalid join response")
	}
	if _, err := runtime.IssueLiveKitJoin(ctx, principal, "invalid/room"); err == nil {
		t.Fatal("invalid room accepted")
	}
	user.Role = model.RoleUser
	user.UpdatedAt = time.Now()
	if err := db.UpdateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.IssueLiveKitJoin(ctx, principal, "front-door"); err == nil {
		t.Fatal("demoted user accepted")
	}
	if _, err := roomAdmin.ListRooms(ctx, principal); err == nil {
		t.Fatal("demoted user listed rooms")
	}
	if _, err := roomAdmin.CreateRoom(ctx, principal, "forbidden"); err == nil {
		t.Fatal("demoted user created a room")
	}
	user.Role = model.RoleSuperAdmin
	if err := db.UpdateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = runtime.IssueLiveKitJoin(ctx, principal, "front-door")
		}()
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runtime.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if _, err := runtime.IssueLiveKitJoin(ctx, principal, "front-door"); err == nil {
		t.Fatal("closed runtime issued a token")
	}
}

func TestNVRLiveKitRejectsDevelopmentMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "livekit.yaml")
	if err := os.WriteFile(path, []byte("development: true\nkeys:\n  key: testsecret-testsecret-testsecret-123456\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := newNVRLiveKitRuntime(&App{}, config.SFULiveKitConfig{ConfigFile: path, APIKey: "key", PublicURL: "http://127.0.0.1:7880"}); err == nil {
		t.Fatal("development mode was accepted")
	}
}

func TestNVRSFURequiresLiveKitConfiguration(t *testing.T) {
	cfg, path := testConfig(t)
	useHTTPMedia(cfg)
	cfg.SFU.Enabled = true
	app, err := NewApp(cfg, path)
	if err == nil || app != nil || !strings.Contains(err.Error(), "livekit requires") {
		t.Fatalf("missing LiveKit configuration: app=%v err=%v", app, err)
	}
}
