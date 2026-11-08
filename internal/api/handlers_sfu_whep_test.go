package api

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/middleware"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
)

func TestSFUBridgeWHEPProxyScopesSessionAndHidesCredential(t *testing.T) {
	const id = "d14f36e0-08ca-4b9f-9568-8493f6ad1122"
	const offer = "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\nm=video 9 UDP/TLS/RTP/SAVPF 102\r\na=recvonly\r\na=rtpmap:102 H264/90000\r\n"
	location := "/webrtc/whep/" + id + "?token=test-token"
	deletes := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("token") != "test-token" {
			t.Errorf("upstream missing token")
			w.WriteHeader(401)
			return
		}
		if r.Header.Get("Cookie") != "" {
			t.Error("cookie forwarded")
		}
		if r.Method == http.MethodDelete {
			if r.URL.Path != "/webrtc/whep/"+id {
				t.Errorf("wrong delete path %s", r.URL.Path)
			}
			deletes++
			w.WriteHeader(204)
			return
		}
		if r.URL.Path != "/webrtc/whep" || r.URL.Query().Get("streamid") != "stream" || r.URL.Query().Get("app_name") != "live" {
			t.Errorf("wrong upstream target %s", r.URL.Redacted())
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != offer {
			t.Error("offer changed")
		}
		w.Header().Set("Location", location)
		w.WriteHeader(201)
		_, _ = io.WriteString(w, "v=0\r\n")
	}))
	defer upstream.Close()
	registry := &testSFUBridgeRegistry{}
	engine := &haWHEPEngine{stubMediaEngine: &stubMediaEngine{}, target: upstream.URL + "/webrtc/whep"}
	h := NewHandler(nil, nil, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetSFUBridgeRegistry(registry)
	h.SetMediaEngine(engine)
	h.SetMultiUserAuthMW(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user := &model.User{ID: 42, Role: model.RoleSuperAdmin, Enabled: true}
			if r.Header.Get("X-Test-Other") == "1" {
				user.ID = 43
			}
			middleware.InjectUser(user)(next).ServeHTTP(w, r)
		})
	})
	router := h.Routes()
	request := func(method, path, body string, other bool) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if method == "POST" {
			req.Header.Set("Content-Type", "application/sdp")
		}
		req.Header.Set("Authorization", "Basic test")
		req.Header.Set("Cookie", "private=1")
		if other {
			req.Header.Set("X-Test-Other", "1")
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	path := "/api/sfu/bridges/bridge/whep"
	rec := request("POST", path, offer, false)
	if rec.Code != 201 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("POST: %d %s", rec.Code, rec.Body.String())
	}
	session := rec.Header().Get("Location")
	if !strings.HasPrefix(session, path+"/") || strings.Contains(session, "test-token") || strings.Contains(session, upstream.URL) {
		t.Fatalf("unsafe session location: %s", session)
	}
	if engine.last.StreamID != "stream" || engine.last.AppName != "live" || engine.last.Token != "test-token" {
		t.Fatalf("incorrect play URL request: %+v", engine.last)
	}
	if got := request("DELETE", session, "", true).Code; got != 404 || deletes != 0 {
		t.Fatalf("cross-user delete: %d", got)
	}
	if got := request("DELETE", session+"x", "", false).Code; got != 404 || deletes != 0 {
		t.Fatalf("forged delete: %d", got)
	}
	if got := request("DELETE", session, "", false).Code; got != 204 || deletes != 1 {
		t.Fatalf("delete: %d (%d)", got, deletes)
	}
	location = "http://evil.invalid/webrtc/whep/" + id
	if got := request("POST", path, offer, false).Code; got != 502 {
		t.Fatalf("foreign location accepted: %d", got)
	}
	if got := request("POST", path, "invalid", false).Code; got != 400 {
		t.Fatalf("invalid SDP accepted: %d", got)
	}
}
func TestSFUWHEPSessionRejectsExpiredOrChangedTokens(t *testing.T) {
	h := NewHandler(nil, nil, noopAuthMW(), nil, nil, "", nil, nil)
	token := h.sealSFUWHEPSession(sfuWHEPSession{Principal: "user:1", BridgeID: "bridge", StreamID: "stream", ID: "d14f36e0-08ca-4b9f-9568-8493f6ad1122", Expires: 1})
	if _, err := h.openSFUWHEPSession(token); err == nil {
		t.Fatal("expired session accepted")
	}
	if _, err := h.openSFUWHEPSession(fmt.Sprint(token, "x")); err == nil {
		t.Fatal("forged session accepted")
	}
}
