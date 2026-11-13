package media

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/auth"
	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/transcode"
)

type cleanupEngine struct {
	Engine
	mu             sync.Mutex
	info           *StreamInfo
	getErr         error
	kickErrByID    map[string]error
	kickCalls      []string
	listStreamsErr error
}

func (e *cleanupEngine) GetStream(context.Context, string) (*StreamInfo, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.getErr != nil {
		return nil, e.getErr
	}
	if e.info == nil {
		return nil, nil
	}
	copyInfo := *e.info
	copyInfo.Subscribers = append([]SessionInfo(nil), e.info.Subscribers...)
	return &copyInfo, nil
}

func (e *cleanupEngine) KickSession(_ context.Context, sessionID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.kickCalls = append(e.kickCalls, sessionID)
	return e.kickErrByID[sessionID]
}

func (e *cleanupEngine) ListStreams(context.Context) ([]StreamInfo, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return nil, e.listStreamsErr
}

func (e *cleanupEngine) setGetError(err error) {
	e.mu.Lock()
	e.getErr = err
	e.mu.Unlock()
}

func (e *cleanupEngine) setKickErrors(errs map[string]error) {
	e.mu.Lock()
	e.kickErrByID = errs
	e.mu.Unlock()
}

func newCleanupAdapter(engine *cleanupEngine, authorization *auth.MediaAuthorizer) *TranscodeAdapter {
	return &TranscodeAdapter{
		Engine: engine,
		Auth:   authorization,
		Config: &config.Config{Media: config.MediaConfig{Mode: "embedded"}},
		outputs: map[string]derivedOutput{
			"output": {source: transcode.SourceRef{EngineID: "embedded", StreamID: "source", AppName: "live"}, playback: "playback"},
		},
	}
}

func reserveCleanupGrant(t *testing.T) *auth.MediaAuthorizer {
	t.Helper()
	authorization := auth.NewMediaAuthorizer()
	if err := authorization.ReserveInternalStream("output", "publish", "playback"); err != nil {
		t.Fatal(err)
	}
	return authorization
}

func requireOldCleanupCredentialsRejected(t *testing.T, authorization *auth.MediaAuthorizer) {
	t.Helper()
	if authorization.AuthorizePublish("rtmp", "output", "publish") == nil {
		t.Fatal("old publish token remains valid")
	}
	if authorization.AuthorizePlayback("rtsp", "output", "playback") == nil {
		t.Fatal("old playback token remains valid")
	}
	if !authorization.InternalPlaybackRequired("output") {
		t.Fatal("revoked output lost its protected tombstone")
	}
}

func TestTranscodeReleaseRevokesBeforeNormalCleanup(t *testing.T) {
	authorization := reserveCleanupGrant(t)
	engine := &cleanupEngine{info: &StreamInfo{}}
	adapter := newCleanupAdapter(engine, authorization)
	stopObservedRevoked := false
	adapter.StopDerivedRecording = func(context.Context, string) error {
		stopObservedRevoked = authorization.AuthorizePlayback("rtsp", "output", "playback") != nil
		return nil
	}

	if err := adapter.Release(context.Background(), "output"); err != nil {
		t.Fatal(err)
	}
	if !stopObservedRevoked {
		t.Fatal("recording stop ran before credential revocation")
	}
	if _, ok := adapter.outputs["output"]; ok {
		t.Fatal("successful cleanup retained reservation")
	}
	requireOldCleanupCredentialsRejected(t, authorization)
	if err := adapter.Release(context.Background(), "output"); err != nil {
		t.Fatal("duplicate release should be idempotent: ", err)
	}
}

func TestTranscodeReleaseStopFailureRevokesAndSupportsRetry(t *testing.T) {
	authorization := reserveCleanupGrant(t)
	engine := &cleanupEngine{info: &StreamInfo{}}
	adapter := newCleanupAdapter(engine, authorization)
	stopErr := errors.New("recording stop failed")
	adapter.StopDerivedRecording = func(context.Context, string) error { return stopErr }

	err := adapter.Release(context.Background(), "output")
	if !errors.Is(err, stopErr) {
		t.Fatalf("expected stop error, got %v", err)
	}
	requireOldCleanupCredentialsRejected(t, authorization)
	if _, ok := adapter.outputs["output"]; !ok {
		t.Fatal("failed cleanup lost reservation")
	}

	// The output stays occupied while cleanup is retryable; a new task cannot
	// claim the same ID or inherit the old publisher/subscriber state.
	enabled := true
	adapter.Config.RTMP.Enabled = &enabled
	if _, err := adapter.Reserve(context.Background(), transcode.SourceRef{EngineID: "embedded", StreamID: "source", AppName: "live"}, "output"); err == nil {
		t.Fatal("new task claimed an output retained for cleanup retry")
	}
	adapter.StopDerivedRecording = nil
	if err := adapter.Release(context.Background(), "output"); err != nil {
		t.Fatal("cleanup retry failed: ", err)
	}
	if _, ok := adapter.outputs["output"]; ok {
		t.Fatal("successful cleanup retry retained reservation")
	}
	requireOldCleanupCredentialsRejected(t, authorization)
}

func TestTranscodeReleaseGetStreamFailureKeepsReservationAndRevokes(t *testing.T) {
	authorization := reserveCleanupGrant(t)
	engine := &cleanupEngine{getErr: errors.New("stream lookup failed")}
	adapter := newCleanupAdapter(engine, authorization)
	if err := adapter.Release(context.Background(), "output"); err == nil {
		t.Fatal("GetStream failure was ignored")
	}
	requireOldCleanupCredentialsRejected(t, authorization)
	if _, ok := adapter.outputs["output"]; !ok {
		t.Fatal("GetStream failure lost reservation")
	}
	engine.setGetError(nil)
	if err := adapter.Release(context.Background(), "output"); err != nil {
		t.Fatal("retry after GetStream failure failed: ", err)
	}
}

func TestTranscodeReleaseKickFailureKeepsReservationAndRetries(t *testing.T) {
	authorization := reserveCleanupGrant(t)
	kickErr := errors.New("kick failed")
	engine := &cleanupEngine{
		info:        &StreamInfo{Subscribers: []SessionInfo{{SessionID: "sub-a"}, {SessionID: "sub-b"}}},
		kickErrByID: map[string]error{"sub-a": kickErr},
	}
	adapter := newCleanupAdapter(engine, authorization)
	if err := adapter.Release(context.Background(), "output"); !errors.Is(err, kickErr) {
		t.Fatalf("expected kick error, got %v", err)
	}
	requireOldCleanupCredentialsRejected(t, authorization)
	if _, ok := adapter.outputs["output"]; !ok {
		t.Fatal("kick failure lost reservation")
	}
	engine.setKickErrors(map[string]error{})
	if err := adapter.Release(context.Background(), "output"); err != nil {
		t.Fatal("retry after kick failure failed: ", err)
	}
	if _, ok := adapter.outputs["output"]; ok {
		t.Fatal("successful kick retry retained reservation")
	}
}

func TestTranscodeReleaseConcurrentCleanupIsSerialized(t *testing.T) {
	authorization := reserveCleanupGrant(t)
	engine := &cleanupEngine{info: &StreamInfo{}}
	adapter := newCleanupAdapter(engine, authorization)
	started := make(chan struct{})
	allowFirst := make(chan struct{})
	var stopCalls atomic.Int32
	firstErr := errors.New("first cleanup attempt failed")
	adapter.StopDerivedRecording = func(context.Context, string) error {
		if stopCalls.Add(1) == 1 {
			close(started)
			<-allowFirst
			return firstErr
		}
		return nil
	}

	results := make(chan error, 2)
	go func() { results <- adapter.Release(context.Background(), "output") }()
	<-started
	go func() { results <- adapter.Release(context.Background(), "output") }()
	close(allowFirst)
	var sawFirst bool
	for range 2 {
		if err := <-results; errors.Is(err, firstErr) {
			sawFirst = true
		} else if err != nil {
			t.Fatalf("unexpected concurrent cleanup error: %v", err)
		}
	}
	if !sawFirst || stopCalls.Load() != 2 {
		t.Fatalf("cleanup was not serialized with retry: first=%v calls=%d", sawFirst, stopCalls.Load())
	}
	if _, ok := adapter.outputs["output"]; ok {
		t.Fatal("concurrent cleanup left reservation after retry succeeded")
	}
	requireOldCleanupCredentialsRejected(t, authorization)
}
