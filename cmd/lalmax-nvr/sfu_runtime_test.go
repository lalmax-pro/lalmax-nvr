package main

import (
	"context"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/q191201771/lalmax/sfu"
	"github.com/q191201771/lalmax/sfu/access"
)

func TestNVRSFUCodecValidation(t *testing.T) {
	valid := sfu.Publication{Kind: "video", RTPReady: true, Negotiated: sfu.NegotiatedCodec{MimeType: "video/H264", ClockRate: 90000, FMTP: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f"}}
	cases := []struct {
		name   string
		change func(*sfu.Publication)
		ok     bool
	}{
		{"constrained baseline", func(*sfu.Publication) {}, true},
		{"not ready", func(p *sfu.Publication) { p.RTPReady = false }, false},
		{"metadata absent", func(p *sfu.Publication) { p.Negotiated = sfu.NegotiatedCodec{} }, false},
		{"wrong clock", func(p *sfu.Publication) { p.Negotiated.ClockRate = 48000 }, false},
		{"high profile", func(p *sfu.Publication) { p.Negotiated.FMTP = "packetization-mode=1;profile-level-id=64001f" }, false},
		{"mode zero", func(p *sfu.Publication) { p.Negotiated.FMTP = "packetization-mode=0;profile-level-id=42e01f" }, false},
		{"duplicate key", func(p *sfu.Publication) { p.Negotiated.FMTP += ";packetization-mode=0" }, false},
		{"malformed", func(p *sfu.Publication) { p.Negotiated.FMTP = "packetization-mode=1;profile-level-id=42e0zz" }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := valid
			tc.change(&p)
			err := nvrSFUValidateH264(context.Background(), p)
			if (err == nil) != tc.ok {
				t.Fatalf("validation: %v", err)
			}
		})
	}
}
func TestNVRSFUOrigin(t *testing.T) {
	req := httptest.NewRequest("GET", "http://nvr.test/api/sfu/ws", nil)
	for _, tc := range []struct {
		origin string
		ok     bool
	}{{"http://nvr.test", true}, {"https://nvr.test", true}, {"http://evil.test", false}, {"http://nvr.test.evil.test", false}, {"", false}, {"http://nvr.test/path", false}} {
		req.Header.Set("Origin", tc.origin)
		if got := nvrSFUOrigin(req); got != tc.ok {
			t.Fatalf("%q: %v", tc.origin, got)
		}
	}
}

func TestNVRSFUPolicyRevalidatesAccountAndScope(t *testing.T) {
	db, err := storage.New(filepath.Join(t.TempDir(), "nvr.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "alice", PasswordHash: "hash", Role: model.RoleSuperAdmin, Enabled: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err = db.CreateUser(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	app := &App{db: db, cfg: &config.Config{}}
	policy := &nvrSFUPolicy{app: app, generation: "gen"}
	req := access.Request{Principal: fmt.Sprintf("user:%d", user.ID), Room: nvrSFURoom, RoomGeneration: "gen", ParticipantID: "member", Operation: access.Join}
	if err = policy.Authorize(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	req.Operation = access.Publish
	req.Kind = "video"
	req.Source = "camera"
	req.Codec = "video/H264"
	if err = policy.Authorize(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	req.Codec = "video/VP8"
	if err = policy.Authorize(context.Background(), req); err == nil {
		t.Fatal("unsupported publish allowed")
	}
	req.Operation = access.ManageBridge
	req.TargetGroup = "arbitrary"
	if err = policy.Authorize(context.Background(), req); err == nil {
		t.Fatal("arbitrary group allowed")
	}
	req.TargetGroup = "sfu_" + strings.Repeat("ab", 24)
	if err = policy.Authorize(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	req.RoomGeneration = "old"
	if err = policy.Authorize(context.Background(), req); err == nil {
		t.Fatal("old generation allowed")
	}
	req.RoomGeneration = "gen"
	user.Role = model.RoleUser
	user.UpdatedAt = time.Now()
	if err = db.UpdateUser(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	if err = policy.Authorize(context.Background(), req); err == nil {
		t.Fatal("demoted user authorized")
	}
}
func TestNVRSFUExternalEngineRejected(t *testing.T) {
	cfg, path := testConfig(t)
	useHTTPMedia(cfg)
	cfg.SFU.Enabled = true
	app, err := NewApp(cfg, path)
	if err == nil || app != nil || !strings.Contains(err.Error(), "requires embedded") {
		t.Fatalf("external media: app=%v err=%v", app, err)
	}
}
