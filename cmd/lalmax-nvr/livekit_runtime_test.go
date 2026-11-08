package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/api"
	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/middleware"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

func TestNVRLiveKitJoinAndRevocation(t *testing.T) {
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
	noop := func(next http.Handler) http.Handler { return next }
	handler := api.NewHandler(nil, nil, noop, nil, nil, "", nil, nil)
	handler.SetMultiUserAuthMW(middleware.InjectUser(user))
	handler.SetLiveKitJoinIssuer(runtime)
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
	callback := lksdk.NewRoomCallback()
	callback.OnTrackSubscribed = func(track *webrtc.TrackRemote, _ *lksdk.RemoteTrackPublication, _ *lksdk.RemoteParticipant) {
		packet, _, err := track.ReadRTP()
		if err == nil {
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
	if _, err := publisher.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{Name: "synthetic"}); err != nil {
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
