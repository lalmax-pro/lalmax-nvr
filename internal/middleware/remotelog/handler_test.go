package remotelog

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/metrics"
)

// testCollector tracks requests received by the test server.
type testCollector struct {
	Bodies []string
	Count  atomic.Int64
	mu     sync.Mutex // guards Bodies
}

func (tc *testCollector) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body.Close()
		tc.mu.Lock()
		tc.Bodies = append(tc.Bodies, string(body))
		tc.mu.Unlock()
		// Increment Count AFTER body is captured so waitForRequests can rely on
		// Count >= N implying len(Bodies) >= N. Otherwise getBodies() may observe an
		// empty slice under CI load, causing "no valid JSON entry found" flakes.
		tc.Count.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (tc *testCollector) getBodies() []string {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	out := make([]string, len(tc.Bodies))
	copy(out, tc.Bodies)
	return out
}

// startTestServer creates an httptest server that captures POST bodies.
func startTestServer(statusCode int) (*httptest.Server, *testCollector) {
	tc := &testCollector{}
	if statusCode == 0 {
		statusCode = http.StatusNoContent
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/insert/jsonline", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body.Close()
		tc.mu.Lock()
		tc.Bodies = append(tc.Bodies, string(body))
		tc.mu.Unlock()
		// See handler() above — Count must be incremented after body capture.
		tc.Count.Add(1)
		w.WriteHeader(statusCode)
	})
	srv := httptest.NewServer(mux)
	return srv, tc
}

func TestHandler_Enabled(t *testing.T) {
	t.Parallel()
	h := New("http://localhost/insert/jsonline", "jsonline", slog.LevelInfo, nil)
	defer h.Close()

	if !h.Enabled(nil, slog.LevelInfo) {
		t.Fatal("expected INFO level to be enabled")
	}
	if !h.Enabled(nil, slog.LevelError) {
		t.Fatal("expected ERROR level to be enabled")
	}
	if h.Enabled(nil, slog.LevelDebug) {
		t.Fatal("expected DEBUG level to be disabled")
	}
}

func TestHandler_Handle_BatchFlush(t *testing.T) {
	t.Parallel()
	srv, tc := startTestServer(0)
	defer srv.Close()

	h := New(srv.URL+"/insert/jsonline", "jsonline", slog.LevelDebug, nil)
	defer h.Close()

	// Send enough logs to trigger buffer-full flush
	for i := 0; i < defaultBufferSize; i++ {
		r := slog.NewRecord(time.Now(), slog.LevelInfo, "test message", 0)
		r.AddAttrs(slog.Int("index", i))
		_ = h.Handle(nil, r)
	}

	// Wait for async flush
	waitForRequests(t, &tc.Count, 1, 2*time.Second)

	bodies := tc.getBodies()
	if len(bodies) == 0 {
		t.Fatal("expected at least one batch to be sent")
	}

	// Verify NDJSON: each line should be valid JSON
	for _, body := range bodies {
		for _, line := range splitNDJSON(body) {
			if line == "" {
				continue
			}
			var obj map[string]any
			if err := json.Unmarshal([]byte(line), &obj); err != nil {
				t.Errorf("invalid JSON line: %s\nerror: %v", line, err)
			}
		}
	}
}

func TestHandler_Handle_FailureTolerance(t *testing.T) {
	t.Parallel()

	var reqCount atomic.Int64
	// Server that always returns 500
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCount.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	m := metrics.NewMetrics()
	h := New(srv.URL+"/insert/jsonline", "jsonline", slog.LevelDebug, m)
	defer h.Close()

	for i := 0; i < defaultBufferSize; i++ {
		r := slog.NewRecord(time.Now(), slog.LevelInfo, "fail test", 0)
		_ = h.Handle(nil, r)
	}

	waitForRequests(t, &reqCount, 1, 2*time.Second)
}

func TestHandler_Close_FlushesRemaining(t *testing.T) {
	t.Parallel()
	srv, tc := startTestServer(0)
	defer srv.Close()

	h := New(srv.URL+"/insert/jsonline", "jsonline", slog.LevelDebug, nil)

	// Send fewer than buffer size — won't trigger automatic flush
	for i := 0; i < 50; i++ {
		r := slog.NewRecord(time.Now(), slog.LevelInfo, "remaining msg", 0)
		r.AddAttrs(slog.Int("idx", i))
		_ = h.Handle(nil, r)
	}

	// Close is a synchronization barrier: no sleep or polling is needed.
	h.Close()

	bodies := tc.getBodies()
	if len(bodies) == 0 {
		t.Fatal("expected Close() to flush remaining logs")
	}

	// Count lines in the body
	totalLines := 0
	for _, body := range bodies {
		totalLines += len(splitNDJSON(body))
	}
	if totalLines < 50 {
		t.Errorf("expected at least 50 log lines, got %d", totalLines)
	}
}

// The final flush must finish before Close returns, even if the remote
// endpoint accepts the request but delays its response.
func TestHandler_Close_WaitsForFinalFlush(t *testing.T) {
	t.Parallel()

	requestReceived := make(chan struct{})
	allowResponse := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(allowResponse) }) }

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(requestReceived)
		<-allowResponse
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	defer unblock()

	h := New(srv.URL, "jsonline", slog.LevelInfo, nil)
	r := slog.NewRecord(time.Now(), slog.LevelInfo, "final batch", 0)
	if err := h.Handle(nil, r); err != nil {
		t.Fatal(err)
	}

	closeReturned := make(chan struct{})
	go func() {
		h.Close()
		close(closeReturned)
	}()

	select {
	case <-requestReceived:
	case <-time.After(5 * time.Second):
		t.Fatal("final flush was not initiated")
	}
	select {
	case <-closeReturned:
		t.Fatal("Close returned before the HTTP flush completed")
	default:
	}

	unblock()
	select {
	case <-closeReturned:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not finish after the HTTP response")
	}
}

// Cloned handlers share the same buffer and close barrier. Repeated Close
// calls must all wait for completion, and writes after Close must be rejected.
func TestHandler_Close_ClonesAndConcurrentCalls(t *testing.T) {
	t.Parallel()

	srv, tc := startTestServer(0)
	defer srv.Close()
	h := New(srv.URL+"/insert/jsonline", "jsonline", slog.LevelInfo, nil)
	child := h.WithAttrs([]slog.Attr{slog.String("component", "child")}).WithGroup("scope")

	for i := 0; i < 50; i++ {
		r := slog.NewRecord(time.Now(), slog.LevelInfo, "from child", 0)
		if err := child.Handle(nil, r); err != nil {
			t.Fatal(err)
		}
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				h.Close()
			} else {
				child.(*Handler).Close()
			}
		}(i)
	}
	wg.Wait()

	totalLines := 0
	for _, body := range tc.getBodies() {
		totalLines += len(splitNDJSON(body))
	}
	if totalLines != 50 {
		t.Fatalf("Close lost buffered entries: got %d, want 50", totalLines)
	}

	r := slog.NewRecord(time.Now(), slog.LevelInfo, "too late", 0)
	if err := h.Handle(nil, r); err == nil {
		t.Fatal("root Handle accepted a log after Close")
	}
	if err := child.Handle(nil, r); err == nil {
		t.Fatal("cloned Handle accepted a log after Close")
	}
}

// Concurrent Handle and Close must agree on exactly which records were accepted.
// All successful Handle calls must be represented in the completed final flush.
func TestHandler_Close_ConcurrentWriters(t *testing.T) {
	t.Parallel()

	srv, tc := startTestServer(0)
	defer srv.Close()
	h := New(srv.URL+"/insert/jsonline", "jsonline", slog.LevelInfo, nil)

	var accepted atomic.Int64
	start := make(chan struct{})
	var wg sync.WaitGroup
	for writer := 0; writer < 8; writer++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < 60; i++ {
				r := slog.NewRecord(time.Now(), slog.LevelInfo, "concurrent writer", 0)
				if err := h.Handle(nil, r); err == nil {
					accepted.Add(1)
				}
			}
		}()
	}
	close(start)
	h.Close()
	wg.Wait()

	var received int
	for _, body := range tc.getBodies() {
		received += len(splitNDJSON(body))
	}
	if int64(received) != accepted.Load() {
		t.Fatalf("accepted %d log entries but flushed %d", accepted.Load(), received)
	}
}

func TestHandler_WithAttrs(t *testing.T) {
	t.Parallel()
	srv, tc := startTestServer(0)
	defer srv.Close()

	h := New(srv.URL+"/insert/jsonline", "jsonline", slog.LevelDebug, nil)
	defer h.Close()

	// Create a child handler with attrs
	child := h.WithAttrs([]slog.Attr{slog.String("component", "test-comp")})
	for i := 0; i < defaultBufferSize; i++ {
		r := slog.NewRecord(time.Now(), slog.LevelInfo, "attrs test", 0)
		_ = child.Handle(nil, r)
	}

	waitForRequests(t, &tc.Count, 1, 2*time.Second)

	bodies := tc.getBodies()
	if len(bodies) == 0 {
		t.Fatal("expected batch to be sent")
	}

	// Check that at least one entry has the "component" field
	found := false
	for _, body := range bodies {
		for _, line := range splitNDJSON(body) {
			if line == "" {
				continue
			}
			var obj map[string]any
			if err := json.Unmarshal([]byte(line), &obj); err != nil {
				continue
			}
			if obj["component"] == "test-comp" {
				found = true
				break
			}
		}
		if found {
			break
		}
	}
	if !found {
		t.Error("expected 'component=test-comp' attr in at least one log entry")
	}
}

func TestHandler_WithGroup(t *testing.T) {
	t.Parallel()
	srv, tc := startTestServer(0)
	defer srv.Close()

	h := New(srv.URL+"/insert/jsonline", "jsonline", slog.LevelDebug, nil)
	defer h.Close()

	grouped := h.WithGroup("server")
	for i := 0; i < defaultBufferSize; i++ {
		r := slog.NewRecord(time.Now(), slog.LevelInfo, "grouped test", 0)
		r.AddAttrs(slog.String("host", "localhost"))
		_ = grouped.Handle(nil, r)
	}

	waitForRequests(t, &tc.Count, 1, 2*time.Second)

	bodies := tc.getBodies()
	found := false
	for _, body := range bodies {
		for _, line := range splitNDJSON(body) {
			if line == "" {
				continue
			}
			var obj map[string]any
			if err := json.Unmarshal([]byte(line), &obj); err != nil {
				continue
			}
			if obj["server.host"] == "localhost" {
				found = true
				break
			}
		}
		if found {
			break
		}
	}
	if !found {
		t.Error("expected 'server.host=localhost' group prefix in log entry")
	}
}

func TestHandler_Close_MultipleTimes(t *testing.T) {
	t.Parallel()
	h := New("http://localhost/insert/jsonline", "jsonline", slog.LevelInfo, nil)
	h.Close()
	h.Close() // should not panic
}

func TestHandler_NilMetrics(t *testing.T) {
	t.Parallel()
	srv, tc := startTestServer(0)
	defer srv.Close()

	h := New(srv.URL+"/insert/jsonline", "jsonline", slog.LevelDebug, nil)
	defer h.Close()

	for i := 0; i < defaultBufferSize; i++ {
		r := slog.NewRecord(time.Now(), slog.LevelInfo, "nil metrics", 0)
		_ = h.Handle(nil, r)
	}

	waitForRequests(t, &tc.Count, 1, 2*time.Second)
	// No panic = success
}

func TestHandler_WithGroup_Empty(t *testing.T) {
	t.Parallel()
	h := New("http://localhost/insert/jsonline", "jsonline", slog.LevelInfo, nil)
	defer h.Close()

	got := h.WithGroup("")
	if got != h {
		t.Error("WithGroup(\"\") should return the same handler")
	}
}

func TestExtractFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		msg     string
		want    map[string]any
		wantNot []string
	}{
		{
			name: "camera_id and component",
			msg:  "ERROR connection error component=xiaomi-recorder camera_id=cam-123 backoff=2s",
			want: map[string]any{
				"component": "xiaomi-recorder",
				"camera_id": "cam-123",
				"backoff":   "2s",
			},
		},
		{
			name: "quoted value with spaces",
			msg:  `ERROR stream error error="dial tcp: connection refused" attempt=3`,
			want: map[string]any{
				"error":   "dial tcp: connection refused",
				"attempt": "3",
			},
		},
		{
			name:    "no key=value pairs",
			msg:     "simple log message",
			wantNot: []string{"component", "camera_id"},
		},
		{
			name: "prefixed message",
			msg:  "INFO resolved xiaomi MISS URL component=xiaomi-cloud did=1058760647 ip=192.168.1.1 vendor=cs2",
			want: map[string]any{
				"component": "xiaomi-cloud",
				"did":       "1058760647",
				"ip":        "192.168.1.1",
				"vendor":    "cs2",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Helper()
			obj := make(map[string]any)
			extractFields(tt.msg, obj)
			for k, v := range tt.want {
				got, ok := obj[k]
				if !ok {
					t.Errorf("expected key %q not found", k)
				} else if got != v {
					t.Errorf("key %q: got %q, want %q", k, got, v)
				}
			}
			for _, k := range tt.wantNot {
				if _, ok := obj[k]; ok {
					t.Errorf("did not expect key %q", k)
				}
			}
		})
	}
}

func TestSend_UsesMsgField(t *testing.T) {
	t.Parallel()
	srv, tc := startTestServer(0)
	defer srv.Close()

	h := New(srv.URL+"/insert/jsonline", "jsonline", slog.LevelDebug, nil)
	defer h.Close()

	for i := 0; i < defaultBufferSize; i++ {
		r := slog.NewRecord(time.Now(), slog.LevelInfo, "test message content", 0)
		_ = h.Handle(nil, r)
	}

	waitForRequests(t, &tc.Count, 1, 2*time.Second)

	bodies := tc.getBodies()
	if len(bodies) == 0 {
		t.Fatal("expected batch to be sent")
	}

	var obj map[string]any
	for _, line := range splitNDJSON(bodies[0]) {
		if line == "" {
			continue
		}
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			continue
		}
		break
	}

	// Must have _msg (not message) as the primary message field
	if obj["_msg"] != "test message content" {
		t.Errorf("expected _msg='test message content', got %v", obj["_msg"])
	}
	// Should NOT have 'message' field
	if _, ok := obj["message"]; ok {
		t.Error("expected no 'message' field, only '_msg'")
	}
}

func TestSend_ExtractsFieldsFromMessage(t *testing.T) {
	t.Parallel()
	srv, tc := startTestServer(0)
	defer srv.Close()

	h := New(srv.URL+"/insert/jsonline", "jsonline", slog.LevelDebug, nil)
	defer h.Close()

	for i := 0; i < defaultBufferSize; i++ {
		r := slog.NewRecord(time.Now(), slog.LevelInfo, "connection error component=recorder camera_id=cam-abc", 0)
		_ = h.Handle(nil, r)
	}

	waitForRequests(t, &tc.Count, 1, 2*time.Second)

	bodies := tc.getBodies()
	var obj map[string]any
outer:
	for _, body := range bodies {
		for _, line := range splitNDJSON(body) {
			if line == "" {
				continue
			}
			if err := json.Unmarshal([]byte(line), &obj); err != nil {
				continue
			}
			break outer
		}
	}

	if obj == nil {
		t.Fatal("no valid JSON entry found")
	}
	// Extracted fields should be present
	if obj["component"] != "recorder" {
		t.Errorf("expected component='recorder', got %v", obj["component"])
	}
	if obj["camera_id"] != "cam-abc" {
		t.Errorf("expected camera_id='cam-abc', got %v", obj["camera_id"])
	}
}

func TestExtractFields_DoesNotOverwrite(t *testing.T) {
	t.Parallel()
	srv, tc := startTestServer(0)
	defer srv.Close()

	h := New(srv.URL+"/insert/jsonline", "jsonline", slog.LevelDebug, nil)
	defer h.Close()

	// Send with explicit slog attr camera_id=explicit-value
	for i := 0; i < defaultBufferSize; i++ {
		r := slog.NewRecord(time.Now(), slog.LevelInfo, "msg camera_id=extracted-value", 0)
		r.AddAttrs(slog.String("camera_id", "explicit-value"))
		_ = h.Handle(nil, r)
	}

	waitForRequests(t, &tc.Count, 1, 2*time.Second)

	bodies := tc.getBodies()
	var obj map[string]any
outer:
	for _, body := range bodies {
		for _, line := range splitNDJSON(body) {
			if line == "" {
				continue
			}
			if err := json.Unmarshal([]byte(line), &obj); err != nil {
				continue
			}
			break outer
		}
	}

	if obj == nil {
		t.Fatal("no valid JSON entry found")
	}
	// Explicit slog attr should win over extracted value
	if obj["camera_id"] != "explicit-value" {
		t.Errorf("explicit attr should win: got %v, want 'explicit-value'", obj["camera_id"])
	}
}

// --- helpers ---

func waitForRequests(t *testing.T, count *atomic.Int64, want int64, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if count.Load() >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("timed out waiting for %d requests, got %d", want, count.Load())
}

func splitNDJSON(body string) []string {
	lines := make([]string, 0)
	start := 0
	for i := 0; i <= len(body); i++ {
		if i == len(body) || body[i] == '\n' {
			if i > start {
				lines = append(lines, body[start:i])
			}
			start = i + 1
		}
	}
	return lines
}
