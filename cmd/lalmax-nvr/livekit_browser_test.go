//go:build livekit_browser

package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/api"
	coreauth "github.com/lalmax-pro/lalmax-nvr/internal/auth"
	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/middleware"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
)

func TestLiveKitBrowser(t *testing.T) {
	t.Chdir(t.TempDir())
	ctx := context.Background()
	db, err := storage.New(filepath.Join(t.TempDir(), "nvr.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Init(ctx); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "admin", PasswordHash: "hash", Role: model.RoleSuperAdmin, Enabled: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err = db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := tcp.Addr().(*net.TCPAddr).Port
	tcp.Close()
	udp, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	udpPort := udp.LocalAddr().(*net.UDPAddr).Port
	udp.Close()
	const key, secret = "testkey", "testsecret-testsecret-testsecret-123456"
	yaml := fmt.Sprintf("development: false\nport: %d\nbind_addresses: [127.0.0.1]\nkeys:\n  %s: %s\nrtc:\n  node_ip: 127.0.0.1\n  enable_loopback_candidate: true\n  ips:\n    includes: [127.0.0.1/32]\n  tcp_port: 0\n  port_range_start: 0\n  port_range_end: 0\n  udp_port: %d\n", port, key, secret, udpPort)
	p := filepath.Join(t.TempDir(), "livekit.yaml")
	if err = os.WriteFile(p, []byte(yaml), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, _ := testConfig(t)
	isolateEmbeddedMedia(t, cfg)
	cfg.SFU = config.SFUConfig{Enabled: true, LiveKit: config.SFULiveKitConfig{PublicURL: fmt.Sprintf("http://127.0.0.1:%d", port)}}
	engine, err := newMediaEngine(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := engine.Shutdown(c); err != nil {
			t.Error(err)
		}
	}()
	runtime, err := newNVRLiveKitRuntime(&App{db: db, cfg: cfg}, config.SFULiveKitConfig{ConfigFile: p, APIKey: key, PublicURL: fmt.Sprintf("http://127.0.0.1:%d", port)})
	if err != nil {
		t.Fatal(err)
	}
	embedded := engine.(*media.EmbeddedLalmax)
	runtime.bridges, err = newNVRLiveKitBridgeRegistry(runtime, &nvrSFUPublisher{engine: embedded, server: embedded.Server()}, coreauth.NewMediaAuthorizer(), db)
	if err != nil {
		t.Fatal(err)
	}
	if err = runtime.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := runtime.Close(c); err != nil {
			t.Error(err)
		}
	}()
	h := api.NewHandler(nil, nil, func(next http.Handler) http.Handler { return next }, cfg, nil, "", nil, nil)
	h.SetMultiUserAuthMW(middleware.InjectUser(user))
	h.SetLiveKitJoinIssuer(runtime)
	h.SetSFURoomManager(&nvrLiveKitRoomAdmin{runtime: runtime})
	h.SetSFUBridgeRegistry(runtime.bridges)
	h.SetMediaEngine(engine)
	mux := http.NewServeMux()
	mux.HandleFunc("/_test/restart-media", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		restartCtx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		if err := embedded.Restart(restartCtx, 1935, 9000, false, false); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.Handle("/", h.Routes())
	svr := httptest.NewServer(mux)
	defer svr.Close()
	browserCtx, cancel := context.WithTimeout(ctx, 150*time.Second)
	defer cancel()
	// t.Chdir isolates lal's logs; locate the source script independently of cwd.
	_, source, _, _ := goruntime.Caller(0)
	command := exec.CommandContext(browserCtx, "node", filepath.Join(filepath.Dir(source), "../../tools/sfu-meet/browser-e2e.cjs"), svr.URL)
	command.Env = append(os.Environ(), "LK_BRIDGE_TEST=1", "LK_RESTART_TEST=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("browser: %v\n%s", err, output)
	}
	runtime.bridges.mu.Lock()
	publisher, ok := runtime.bridges.publisher.(*nvrSFUPublisher)
	retiring := len(runtime.bridges.retiring)
	closed := runtime.bridges.closed
	runtime.bridges.mu.Unlock()
	if !ok || publisher.server != embedded.Server() || retiring != 0 || closed {
		t.Fatalf("bridge manager did not recover after media restart: publisher=%v retiring=%d closed=%v", ok, retiring, closed)
	}
	t.Log(string(output))
}
