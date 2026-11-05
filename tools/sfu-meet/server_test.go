package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

const testSecret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func setup(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewUnstartedServer(nil)
	origin := "http://" + srv.Listener.Addr().String()
	handler, err := newDemoHandler(origin, testSecret)
	if err != nil {
		t.Fatal(err)
	}
	srv.Config.Handler = handler
	srv.Start()
	t.Cleanup(srv.Close)
	return srv, origin
}
func login(t *testing.T, origin, token, requestOrigin string) *http.Response {
	t.Helper()
	payload, _ := json.Marshal(map[string]string{"token": token})
	req, _ := http.NewRequest(http.MethodPost, origin+"/session", bytes.NewReader(payload))
	req.Header.Set("Origin", requestOrigin)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}
func dial(t *testing.T, origin, requestOrigin string, cookie *http.Cookie) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	headers := http.Header{"Origin": []string{requestOrigin}}
	if cookie != nil {
		headers.Set("Cookie", cookie.String())
	}
	return websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(origin, "http")+"/ws", headers)
}
func TestDemoLoginAndWebSocket(t *testing.T) {
	_, origin := setup(t)
	if c, resp, err := dial(t, origin, origin, nil); err == nil {
		c.Close()
		t.Fatal("unauthenticated accepted")
	} else if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: %v", resp)
	}
	if resp := login(t, origin, "wrong", origin); resp.StatusCode != http.StatusUnauthorized {
		t.Fatal(resp.StatusCode)
	}
	if resp := login(t, origin, testSecret, "http://attacker.example"); resp.StatusCode != http.StatusForbidden {
		t.Fatal(resp.StatusCode)
	}
	resp := login(t, origin, testSecret, origin)
	if resp.StatusCode != http.StatusNoContent || len(resp.Cookies()) != 1 {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	cookie := resp.Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie: %+v", cookie)
	}
	if c, resp, err := dial(t, origin, "http://attacker.example", cookie); err == nil {
		c.Close()
		t.Fatal("bad origin accepted")
	} else if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("origin: %v", resp)
	}
	conn, _, err := dial(t, origin, origin, cookie)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var snapshot map[string]any
	if err = conn.ReadJSON(&snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot["type"] != "snapshot" {
		t.Fatalf("snapshot: %+v", snapshot)
	}
	if err = conn.WriteJSON(map[string]any{"id": "v", "type": "publish", "kind": "video", "source": "camera", "codec": "video/H264", "track_id": "video"}); err != nil {
		t.Fatal(err)
	}
	for {
		var reply map[string]any
		if err = conn.ReadJSON(&reply); err != nil {
			t.Fatal(err)
		}
		if reply["type"] == "result" {
			if reply["error"] != "forbidden" {
				t.Fatalf("video should be denied: %+v", reply)
			}
			break
		}
	}
}
func TestRejectsNonLoopbackAndSpoofedHost(t *testing.T) {
	if _, err := newDemoHandler("http://0.0.0.0:8080", testSecret); err == nil {
		t.Fatal("non-loopback accepted")
	}
	_, origin := setup(t)
	req, _ := http.NewRequest(http.MethodGet, origin+"/healthz", nil)
	req.Host = "attacker.example"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatal(resp.StatusCode)
	}
}

func TestEmbeddedWebAssets(t *testing.T) {
	_, origin := setup(t)
	for _, tc := range []struct{ path, contentType, contains string }{{"/", "text/html", "SFU Meet"}, {"/app.js", "text/javascript", "RTCPeerConnection"}, {"/style.css", "text/css", ".layout"}} {
		resp, err := http.Get(origin + tc.path)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), tc.contentType) || !strings.Contains(string(body), tc.contains) {
			t.Fatalf("asset %s status=%d type=%q", tc.path, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		if strings.Contains(string(body), testSecret) {
			t.Fatal("secret exposed in asset")
		}
	}
	resp, err := http.Get(origin + "/web/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("directory status %d", resp.StatusCode)
	}
}
