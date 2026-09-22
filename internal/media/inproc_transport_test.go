package media

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestIsInProcessPlayPath(t *testing.T) {
	t.Parallel()
	require.True(t, IsInProcessPlayPath("/live/hls/cam1/index.m3u8"))
	require.True(t, IsInProcessPlayPath("/live/m4s/cam1.mp4"))
	require.True(t, IsInProcessPlayPath("/webrtc/whep"))
	require.True(t, IsInProcessPlayPath("/WEBRTC/whep"))
	require.False(t, IsInProcessPlayPath("/hls/cam1.m3u8"))
	require.False(t, IsInProcessPlayPath("/live/cam1.flv"))
	require.False(t, IsInProcessPlayPath("/api/stat/all_group"))
}

type staticPlayProvider struct {
	h http.Handler
}

func (p staticPlayProvider) PlayHTTPHandler() http.Handler { return p.h }

type fallbackTripper struct {
	called bool
}

func (f *fallbackTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	f.called = true
	return &http.Response{
		StatusCode: http.StatusTeapot,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("tcp")),
		Request:    req,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
	}, nil
}

func TestInProcessPlayTransport_StreamsBeforeHandlerReturns(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	release := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		close(started)
		if _, err := w.Write([]byte("chunk-1")); err != nil {
			return
		}
		<-release
		_, _ = w.Write([]byte("-chunk-2"))
	})
	rt := NewInProcessPlayTransport(staticPlayProvider{h: h}, &fallbackTripper{})
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://127.0.0.1:12090/live/m4s/cam.mp4", nil)
	require.NoError(t, err)

	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "video/mp4", resp.Header.Get("Content-Type"))

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("handler did not send headers")
	}

	buf := make([]byte, 7)
	n, err := io.ReadFull(resp.Body, buf)
	require.NoError(t, err)
	require.Equal(t, 7, n)
	require.Equal(t, "chunk-1", string(buf))
	close(release)

	rest, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "-chunk-2", string(rest))
}

func TestInProcessPlayTransport_FallsBackForOtherPaths(t *testing.T) {
	t.Parallel()
	fb := &fallbackTripper{}
	rt := NewInProcessPlayTransport(staticPlayProvider{h: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})}, fb)
	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:18080/live/cam.flv", nil)
	require.NoError(t, err)
	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.True(t, fb.called)
	require.Equal(t, http.StatusTeapot, resp.StatusCode)
}

func TestInProcessPlayTransport_EmptyHandlerFallsBack(t *testing.T) {
	t.Parallel()
	fb := &fallbackTripper{}
	rt := NewInProcessPlayTransport(staticPlayProvider{}, fb)
	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:12090/live/hls/cam/index.m3u8", nil)
	require.NoError(t, err)
	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.True(t, fb.called)
}

func TestServeInProcess_UsesTestServerHandler(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var sawPath string
	inner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		sawPath = r.URL.Path
		mu.Unlock()
		w.Header().Set("X-Inproc", "1")
		_, _ = io.WriteString(w, "ok")
	}))
	defer inner.Close()

	rt := NewInProcessPlayTransport(staticPlayProvider{h: inner.Config.Handler}, http.DefaultTransport)
	req, err := http.NewRequest(http.MethodGet, "http://inproc.local/live/hls/cam/index.m3u8", nil)
	require.NoError(t, err)
	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "ok", string(body))
	require.Equal(t, "1", resp.Header.Get("X-Inproc"))
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, "/live/hls/cam/index.m3u8", sawPath)
}
