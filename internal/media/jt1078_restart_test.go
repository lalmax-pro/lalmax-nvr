package media

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/q191201771/lalmax/jt1078"
)

type rejectingJT1078Gate struct{ calls atomic.Int32 }

func (g *rejectingJT1078Gate) AllowStream(string) bool { g.calls.Add(1); return false }

func TestRestartPreservesJT1078Authorizer(t *testing.T) {
	t.Chdir(t.TempDir())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	path := filepath.Join(t.TempDir(), "lalmax.json")
	raw, err := embeddedConfigJSON(EmbeddedLalmaxConfig{})
	if err != nil {
		t.Fatal(err)
	}
	var conf map[string]any
	if err = json.Unmarshal(raw, &conf); err != nil {
		t.Fatal(err)
	}
	max := conf["lalmax"].(map[string]any)
	max["rtc_config"] = map[string]any{"enable": false}
	max["http_config"] = map[string]any{"http_listen_addr": "127.0.0.1:0"}
	max["jt1078_config"] = map[string]any{"enable": true, "addr": addr, "udp_enable": false, "phone_len": 6}
	max["fmp4_config"] = map[string]any{}
	lal := conf["lal"].(map[string]any)
	lal["rtsp"] = map[string]any{"enable": false}
	lal["httpflv"] = map[string]any{"enable": false}
	lal["httpts"] = map[string]any{"enable": true, "http_listen_addr": "127.0.0.1:0", "url_pattern": "/live/"}
	lal["log"] = map[string]any{"is_to_file": false, "is_to_stdout": false, "level": 5}
	raw, err = json.Marshal(conf)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	engine, err := NewEmbeddedLalmax(EmbeddedLalmaxConfig{ConfigPath: path})
	if err != nil {
		t.Fatal(err)
	}
	gate := &rejectingJT1078Gate{}
	engine.SetJT1078Authorizer(gate)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	t.Cleanup(func() { engine.Shutdown(context.Background()) })
	// Readiness uses the in-process API; no fixed public ports are needed.
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { engine.Server().HTTPHandler().ServeHTTP(w, r) }))
	defer api.Close()
	engine.httpEngine, err = NewLalmaxHTTP(LalmaxHTTPConfig{BaseURL: api.URL})
	if err != nil {
		t.Fatal(err)
	}
	engine.LalmaxHTTP = engine.httpEngine
	for i := 0; i < 2; i++ {
		if i == 0 {
			err = engine.Start(ctx)
		} else {
			err = engine.Restart(ctx, 1935, 9000, false, false)
		}
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(2 * time.Second)
		var conn net.Conn
		for time.Now().Before(deadline) {
			conn, err = net.Dial("tcp", addr)
			if err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err != nil {
			t.Fatal(err)
		}
		packet, err := (jt1078.Packet{Sim: "1003", LogicChannel: 1, PhoneLen: 6, Flag: jt1078.Flag{PT: jt1078.PTH264}, DataType: jt1078.DataTypeI, Body: []byte{0, 0, 0, 1, 0x65}}).Encode()
		if err != nil {
			conn.Close()
			t.Fatal(err)
		}
		before := gate.calls.Load()
		if _, err = conn.Write(packet); err != nil {
			conn.Close()
			t.Fatal(err)
		}
		for gate.calls.Load() == before && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		conn.Close()
		if gate.calls.Load() == before {
			t.Fatal("authorizer not consulted after restart")
		}
	}
}
