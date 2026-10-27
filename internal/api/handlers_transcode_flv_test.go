package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

func TestDerivedFLVUsesTrustedQueryAndFilteredHeaders(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("token") != "internal" || len(r.URL.Query()["token"]) != 1 {
			t.Error("client query overwrote internal token")
		}
		if r.Header.Get("Authorization") != "Basic administrator" {
			t.Error("missing engine account auth")
		}
		if r.Header.Get("Cookie") != "" {
			t.Error("browser cookie leaked to engine")
		}
		w.Header().Set("Set-Cookie", "internal=secret")
		w.Write([]byte("FLV-test"))
	}))
	defer backend.Close()
	upstream, _ := url.Parse(backend.URL + "/live/output.flv?token=internal")
	request := httptest.NewRequest("GET", "/preview?token=attacker", nil)
	request.Header.Set("Authorization", "Basic administrator")
	request.Header.Set("Cookie", "browser=session")
	response := httptest.NewRecorder()
	h := &Handler{}
	h.proxyDerivedFLV(response, request, upstream)
	if response.Code != 200 || response.Body.String() != "FLV-test" {
		t.Fatalf("unexpected preview: %d %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Set-Cookie") != "" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("unsafe response headers")
	}
}
func TestDerivedFLVCancellationClosesUpstream(t *testing.T) {
	connected := make(chan struct{})
	disconnected := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("FLV"))
		w.(http.Flusher).Flush()
		close(connected)
		<-r.Context().Done()
		close(disconnected)
	}))
	defer backend.Close()
	upstream, _ := url.Parse(backend.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest("GET", "/preview", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() { defer close(done); (&Handler{}).proxyDerivedFLV(httptest.NewRecorder(), request, upstream) }()
	select {
	case <-connected:
	case <-time.After(time.Second):
		t.Fatal("preview never connected")
	}
	cancel()
	select {
	case <-disconnected:
	case <-time.After(time.Second):
		t.Fatal("upstream survived client cancellation")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("proxy did not exit")
	}
}

func TestDerivedFLVDoesNotFollowRedirect(t *testing.T) {
	var leaked atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true) }))
	defer target.Close()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer backend.Close()
	upstream, _ := url.Parse(backend.URL)
	request := httptest.NewRequest("GET", "/preview", nil)
	request.Header.Set("Authorization", "Basic secret")
	response := httptest.NewRecorder()
	(&Handler{}).proxyDerivedFLV(response, request, upstream)
	if response.Code != 502 || leaked.Load() {
		t.Fatal("redirect followed or unexpected response")
	}
}
