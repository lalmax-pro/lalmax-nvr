package media

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// IsInProcessPlayPath reports whether a URL path is served by the embedded
// lalmax gin router (LL-HLS, HTTP-fMP4, WHEP) and can skip TCP loopback.
func IsInProcessPlayPath(p string) bool {
	p = strings.ToLower(p)
	if strings.HasPrefix(p, "/live/hls/") || strings.HasPrefix(p, "/live/m4s/") {
		return true
	}
	return strings.HasPrefix(p, "/webrtc/")
}

// NewInProcessPlayTransport returns a RoundTripper that ServeHTTPs matching
// play paths on handler and falls back to TCP for everything else.
// provider is consulted on each request so an embedded lalmax Restart() is picked up.
func NewInProcessPlayTransport(provider PlayHandlerProvider, fallback http.RoundTripper) http.RoundTripper {
	if fallback == nil {
		fallback = http.DefaultTransport
	}
	return &inProcessPlayTransport{provider: provider, fallback: fallback}
}

type inProcessPlayTransport struct {
	provider PlayHandlerProvider
	fallback http.RoundTripper
}

func (t *inProcessPlayTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil || req.URL == nil || t.provider == nil || !IsInProcessPlayPath(req.URL.Path) {
		return t.fallback.RoundTrip(req)
	}
	h := t.provider.PlayHTTPHandler()
	if h == nil {
		return t.fallback.RoundTrip(req)
	}
	return serveInProcess(h, req)
}

func serveInProcess(h http.Handler, req *http.Request) (*http.Response, error) {
	pr, pw := io.Pipe()
	rw := &pipeResponseWriter{
		header:     make(http.Header),
		pw:         pw,
		headerSent: make(chan struct{}),
	}
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				rw.abort(fmt.Errorf("in-process play panic: %v", rec))
				return
			}
			rw.finish()
		}()
		h.ServeHTTP(rw, req)
	}()

	select {
	case <-rw.headerSent:
	case <-req.Context().Done():
		_ = pr.Close()
		return nil, req.Context().Err()
	}

	return &http.Response{
		Status:        http.StatusText(rw.statusCode()),
		StatusCode:    rw.statusCode(),
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        rw.headerSnapshot(),
		Body:          pr,
		ContentLength: -1,
		Request:       req,
		Close:         true,
	}, nil
}

type pipeResponseWriter struct {
	mu         sync.Mutex
	header     http.Header
	cloned     http.Header
	status     int
	pw         *io.PipeWriter
	headerSent chan struct{}
	sent       bool
}

func (w *pipeResponseWriter) Header() http.Header { return w.header }

func (w *pipeResponseWriter) writeHeaderLocked(code int) {
	if w.sent {
		return
	}
	if code == 0 {
		code = http.StatusOK
	}
	w.status = code
	w.cloned = w.header.Clone()
	w.sent = true
	close(w.headerSent)
}

func (w *pipeResponseWriter) WriteHeader(code int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writeHeaderLocked(code)
}

func (w *pipeResponseWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	if !w.sent {
		w.writeHeaderLocked(http.StatusOK)
	}
	w.mu.Unlock()
	return w.pw.Write(p)
}

func (w *pipeResponseWriter) Flush() {
	w.mu.Lock()
	if !w.sent {
		w.writeHeaderLocked(http.StatusOK)
	}
	w.mu.Unlock()
}

func (w *pipeResponseWriter) finish() {
	w.mu.Lock()
	if !w.sent {
		w.writeHeaderLocked(http.StatusOK)
	}
	w.mu.Unlock()
	_ = w.pw.Close()
}

func (w *pipeResponseWriter) abort(err error) {
	w.mu.Lock()
	if !w.sent {
		w.writeHeaderLocked(http.StatusInternalServerError)
	}
	w.mu.Unlock()
	_ = w.pw.CloseWithError(err)
}

func (w *pipeResponseWriter) statusCode() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func (w *pipeResponseWriter) headerSnapshot() http.Header {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cloned != nil {
		return w.cloned.Clone()
	}
	return w.header.Clone()
}

var _ http.Flusher = (*pipeResponseWriter)(nil)
