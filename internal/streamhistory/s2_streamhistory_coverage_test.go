package streamhistory

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/media"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	lalmaxserver "github.com/q191201771/lalmax/server"
	"github.com/stretchr/testify/require"
)

// s2HistoryRepo is a fully scriptable storage.StreamHistoryRepository.
type s2HistoryRepo struct {
	mu          sync.Mutex
	inserts     []storage.StreamHistory
	finishes    []s2FinishCall
	mediaStates []bool
	insertErr   error
	finishErr   error
	finalizeErr error
	mediaErr    error
}

type s2FinishCall struct {
	StreamID  string
	SessionID string
	Reason    string
	Detail    string
}

func (r *s2HistoryRepo) InsertStreamHistory(_ context.Context, history *storage.StreamHistory) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.insertErr != nil {
		return r.insertErr
	}
	r.inserts = append(r.inserts, *history)
	return nil
}

// counts returns how many inserts, finishes and media-state updates were recorded.
func (r *s2HistoryRepo) counts() (inserts, finishes, mediaStates int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.inserts), len(r.finishes), len(r.mediaStates)
}

// finishCalls returns a copy of the recorded finish calls.
func (r *s2HistoryRepo) finishCalls() []s2FinishCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]s2FinishCall(nil), r.finishes...)
}

func (r *s2HistoryRepo) FinishStreamHistory(_ context.Context, streamID, sessionID string, _ time.Time, reason, detail string, _, _ uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finishes = append(r.finishes, s2FinishCall{StreamID: streamID, SessionID: sessionID, Reason: reason, Detail: detail})
	return r.finishErr
}

func (r *s2HistoryRepo) FinalizeOpenStreamHistories(context.Context, time.Time, string) error {
	return r.finalizeErr
}

func (r *s2HistoryRepo) RecordStreamHistoryMediaState(_ context.Context, _ string, _ time.Time, active bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mediaStates = append(r.mediaStates, active)
	return r.mediaErr
}

// s2Engine stubs only SubscribeEvents; every other media.Engine method panics
// if called, which keeps the manager contract honest in these tests.
type s2Engine struct {
	media.Engine
	events chan media.Event
	err    error
}

func (e *s2Engine) SubscribeEvents(context.Context, media.EventFilter) (<-chan media.Event, error) {
	if e.err != nil {
		return nil, e.err
	}
	return e.events, nil
}

// s2HookRegistrar records hook plugin registration.
type s2HookRegistrar struct {
	plugin lalmaxserver.HookPlugin
	err    error
}

func (s *s2HookRegistrar) RegisterHookPlugin(plugin lalmaxserver.HookPlugin, _ lalmaxserver.HookPluginOptions) (func(), error) {
	s.plugin = plugin
	if s.err != nil {
		return nil, s.err
	}
	return func() {}, nil
}

func TestS2IPTVMediaHistoryLifecycle(t *testing.T) {
	repo := &s2HistoryRepo{}
	m := NewManager(repo, nil)

	// Start with a zero timestamp.
	m.RecordMediaState("iptv_ch1", true, time.Time{})
	require.Len(t, repo.inserts, 1)
	require.Equal(t, "HLS_PULL", repo.inserts[0].Protocol)
	require.Equal(t, []bool{true}, repo.mediaStates)

	// A second start for the same stream only refreshes the media state.
	m.RecordMediaState("iptv_ch1", true, time.Now())
	require.Len(t, repo.inserts, 1)
	require.Equal(t, []bool{true, true}, repo.mediaStates)

	// A recorded pull error becomes the stop reason.
	m.RecordHLSPullAttemptError("iptv_ch1", "  upstream timeout  ")
	m.RecordMediaState("iptv_ch1", false, time.Time{})
	require.Len(t, repo.finishes, 1)
	require.Equal(t, "upstream_error", repo.finishes[0].Reason)
	require.Equal(t, "upstream timeout", repo.finishes[0].Detail)

	// HLS pull task start clears the pending error and keeps the session.
	m.RecordMediaState("iptv_ch1", true, time.Now())
	m.RecordHLSPullAttemptError("iptv_ch1", "flaky")
	m.RecordHLSPullSessionState("iptv_ch1", true, time.Now(), "", "")
	require.Empty(t, m.iptvPullErrors)

	// HLS pull task ending without a reason finishes with "unknown".
	m.RecordHLSPullSessionState("iptv_ch1", false, time.Time{}, "", "detail")
	require.Len(t, repo.finishes, 2)
	require.Equal(t, "unknown", repo.finishes[1].Reason)
	require.Equal(t, "detail", repo.finishes[1].Detail)

	// Ending an unknown session is a no-op.
	m.RecordHLSPullSessionState("iptv_unknown", false, time.Now(), "x", "y")
	require.Len(t, repo.finishes, 2)
}

func TestS2MediaStateNilAndEmptyGuards(t *testing.T) {
	var nilManager *Manager
	nilManager.RecordHLSPullSessionState("iptv_a", false, time.Time{}, "", "")
	nilManager.RecordHLSPullAttemptError("iptv_a", "boom")
	nilManager.RecordMediaState("iptv_a", true, time.Time{})
	require.NoError(t, nilManager.Reconcile(context.Background()))

	// Managers without a database are inert.
	dbless := NewManager(nil, nil)
	dbless.RecordHLSPullSessionState("iptv_a", false, time.Time{}, "", "")
	dbless.RecordMediaState("iptv_a", true, time.Time{})

	// Empty identifiers and blank details are ignored.
	repo := &s2HistoryRepo{}
	m := NewManager(repo, nil)
	m.RecordMediaState("", true, time.Now())
	m.RecordMediaState("cam1", true, time.Now()) // non-IPTV
	m.RecordHLSPullSessionState("", false, time.Now(), "", "")
	m.RecordHLSPullAttemptError("", "boom")
	m.RecordHLSPullAttemptError("iptv_a", "   ")
	require.Len(t, repo.mediaStates, 1)
}

func TestS2MediaStateErrorBranches(t *testing.T) {
	// Non-IPTV media state failures are logged and swallowed.
	repo := &s2HistoryRepo{mediaErr: errors.New("media state exploded")}
	m := NewManager(repo, nil)
	m.RecordMediaState("cam1", true, time.Now())

	// IPTV start: insert failure rolls back the tracked session.
	repo = &s2HistoryRepo{insertErr: errors.New("insert exploded")}
	m = NewManager(repo, nil)
	m.RecordMediaState("iptv_x", true, time.Now())
	require.Empty(t, m.iptvSessions)

	// IPTV start: media state failure after a successful insert.
	repo = &s2HistoryRepo{mediaErr: errors.New("media state exploded")}
	m = NewManager(repo, nil)
	m.RecordMediaState("iptv_y", true, time.Now())
	require.Len(t, repo.inserts, 1)
	// The session still tracks so the stop event can close it.
	m.RecordMediaState("iptv_y", true, time.Now())
	require.Len(t, repo.inserts, 1)

	// IPTV stop: finish failure is logged.
	repo = &s2HistoryRepo{finishErr: errors.New("finish exploded")}
	m = NewManager(repo, nil)
	m.RecordMediaState("iptv_z", true, time.Now())
	m.RecordMediaState("iptv_z", false, time.Now())

	// HLS pull end: finish failure is logged.
	m = NewManager(repo, nil)
	m.RecordMediaState("iptv_w", true, time.Now())
	m.RecordHLSPullSessionState("iptv_w", false, time.Now(), "reason", "detail")
}

func TestS2Reconcile(t *testing.T) {
	ctx := context.Background()

	// Nil manager and missing database are no-ops.
	require.NoError(t, (*Manager)(nil).Reconcile(ctx))
	require.NoError(t, NewManager(nil, nil).Reconcile(ctx))

	// A finalize failure is returned and not cached.
	repo := &s2HistoryRepo{finalizeErr: errors.New("finalize exploded")}
	m := NewManager(repo, nil)
	require.ErrorContains(t, m.Reconcile(ctx), "finalize exploded")
	require.False(t, m.historyReconciled)

	// Success is cached so subsequent calls are no-ops.
	repo.finalizeErr = nil
	require.NoError(t, m.Reconcile(ctx))
	require.True(t, m.historyReconciled)
	repo.finalizeErr = errors.New("finalize exploded")
	require.NoError(t, m.Reconcile(ctx))
}

func TestS2StartVariants(t *testing.T) {
	ctx := context.Background()

	// Reconcile failures abort Start.
	repo := &s2HistoryRepo{finalizeErr: errors.New("finalize exploded")}
	require.ErrorContains(t, NewManager(repo, nil).Start(ctx), "finalize previous stream history sessions")

	// An already registered hook plugin skips SSE subscription.
	repo = &s2HistoryRepo{}
	engine := &s2Engine{events: make(chan media.Event)}
	m := NewManager(repo, engine)
	m.hookRegistered = true
	require.NoError(t, m.Start(ctx))
	m.Stop()

	// A nil engine leaves nothing to subscribe to.
	require.NoError(t, NewManager(repo, nil).Start(ctx))

	// Subscription failures are retried until the context is cancelled.
	failing := &s2Engine{err: errors.New("subscribe exploded")}
	m = NewManager(repo, failing)
	m.cancelFunc = func() {}
	require.NoError(t, m.Start(ctx))
	m.Stop()
	require.NoError(t, m.Start(ctx))
	m.Stop()
}

func TestS2RunHandlesEveryEvent(t *testing.T) {
	repo := &s2HistoryRepo{}
	events := make(chan media.Event, 8)
	engine := &s2Engine{events: events}
	m := NewManager(repo, engine)
	require.NoError(t, m.Start(context.Background()))
	t.Cleanup(m.Stop)

	startRaw, err := json.Marshal(pubStartPayload{
		SessionId: "sess-1", Protocol: "RTMP", AppName: "live", StreamName: "cam1", RemoteAddr: "1.2.3.4",
	})
	require.NoError(t, err)
	stopRaw, err := json.Marshal(pubStopPayload{
		SessionId: "sess-1", Protocol: "RTMP", AppName: "live", StreamName: "cam1", Reason: "kicked",
	})
	require.NoError(t, err)

	events <- media.Event{Type: media.EventPublisherStarted, At: time.Now(), Raw: startRaw}
	events <- media.Event{Type: media.EventPublisherStopped, At: time.Now(), Raw: stopRaw}
	events <- media.Event{Type: media.EventRelayPullStarted, At: time.Now(), Raw: startRaw}
	events <- media.Event{Type: media.EventRelayPullStopped, At: time.Now(), Raw: stopRaw}
	events <- media.Event{Type: media.EventStreamActive, StreamID: "cam1", At: time.Now()}
	events <- media.Event{Type: media.EventStreamStopped, StreamID: "cam1", At: time.Now()}
	events <- media.Event{Type: media.EventType("unknown.event")}

	deadline := time.Now().Add(2 * time.Second)
	for {
		_, finishes, _ := repo.counts()
		if finishes >= 2 || !time.Now().Before(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	inserts, finishes, mediaStates := repo.counts()
	require.GreaterOrEqual(t, finishes, 2)
	require.NotEmpty(t, inserts)
	require.NotEmpty(t, mediaStates)

	// Closing the event channel ends run; the manager retries in the background
	// until Stop cancels the context.
	close(events)
	m.Stop()
}

func TestS2HandlePubStartFallbacks(t *testing.T) {
	repo := &s2HistoryRepo{}
	m := NewManager(repo, nil)

	// Unparsable payloads are ignored.
	m.handlePubStart(media.Event{Raw: []byte("not-json")})
	require.Empty(t, repo.inserts)

	// Missing envelope fields fall back to the payload.
	raw, err := json.Marshal(pubStartPayload{
		SessionId: "sess-9", Protocol: "RTSP", StreamName: "cam9",
	})
	require.NoError(t, err)
	m.handlePubStart(media.Event{At: time.Now(), Raw: raw})
	require.Len(t, repo.inserts, 1)
	require.Equal(t, "cam9", repo.inserts[0].StreamID)
	require.Equal(t, "RTSP", repo.inserts[0].Protocol)
	require.Equal(t, "sess-9", repo.inserts[0].SessionID)

	// Insert failures are logged and not retried.
	repo = &s2HistoryRepo{insertErr: errors.New("insert exploded")}
	m = NewManager(repo, nil)
	m.handlePubStart(media.Event{StreamID: "cam1", SessionID: "s1", At: time.Now(), Raw: []byte("{}")})
	require.Empty(t, repo.inserts)
}

func TestS2HandlePubStopFallbacks(t *testing.T) {
	// Unparsable payload still finishes the session.
	repo := &s2HistoryRepo{}
	m := NewManager(repo, nil)
	m.handlePubStop(media.Event{StreamID: "cam1", SessionID: "s1", Raw: []byte("not-json")}, "publisher_stopped")
	require.Len(t, repo.finishes, 1)
	require.Equal(t, "publisher_stopped", repo.finishes[0].Reason)

	// Zero timestamps are defaulted and payload identifiers win.
	raw, err := json.Marshal(pubStopPayload{
		SessionId: "sess-2", StreamName: "cam2", Protocol: "RTSP", Message: "bye",
	})
	require.NoError(t, err)
	m.handlePubStop(media.Event{Raw: raw}, "")
	require.Len(t, repo.finishes, 1+1)
	call := repo.finishes[len(repo.finishes)-1]
	require.Equal(t, "cam2", call.StreamID)
	require.Equal(t, "sess-2", call.SessionID)
	require.Equal(t, "upstream_error", call.Reason)

	// Internal HLS history rows are skipped.
	raw, err = json.Marshal(pubStopPayload{StreamName: "iptv_ch1", Protocol: "customize"})
	require.NoError(t, err)
	before := len(repo.finishes)
	m.handlePubStop(media.Event{Raw: raw}, "publisher_stopped")
	require.Len(t, repo.finishes, before)

	// Finish failures are logged.
	repo = &s2HistoryRepo{finishErr: errors.New("finish exploded")}
	m = NewManager(repo, nil)
	m.handlePubStop(media.Event{StreamID: "cam1", SessionID: "s1", Raw: []byte("{}")}, "publisher_stopped")
}

func TestS2RegisterAsHookPlugin(t *testing.T) {
	repo := &s2HistoryRepo{}
	m := NewManager(repo, nil)

	// Registration failures propagate.
	failing := &s2HookRegistrar{err: errors.New("register exploded")}
	_, err := m.RegisterAsHookPlugin(failing)
	require.ErrorContains(t, err, "register exploded")
	require.False(t, m.hookRegistered)

	// Successful registration is remembered.
	ok := &s2HookRegistrar{}
	cancel, err := m.RegisterAsHookPlugin(ok)
	require.NoError(t, err)
	require.NotNil(t, cancel)
	require.True(t, m.hookRegistered)
	require.NotNil(t, ok.plugin)
	require.Equal(t, "stream-history", ok.plugin.Name())
}

func TestS2HookPluginRelayEvents(t *testing.T) {
	repo := &s2HistoryRepo{}
	m := NewManager(repo, nil)
	plugin := &hookPlugin{mgr: m}

	start, err := json.Marshal(pubStartPayload{SessionId: "sess-r", Protocol: "RTMP", StreamName: "cam-r"})
	require.NoError(t, err)
	stop, err := json.Marshal(pubStopPayload{SessionId: "sess-r", StreamName: "cam-r", Reason: "done"})
	require.NoError(t, err)
	active, err := json.Marshal(map[string]string{"stream_name": "cam-g"})
	require.NoError(t, err)

	require.NoError(t, plugin.OnHookEvent(lalmaxserver.HookEvent{
		Event: lalmaxserver.HookEventRelayPullStart, Payload: start, Timestamp: time.Now().Format(time.RFC3339Nano),
	}))
	require.Len(t, repo.inserts, 1)
	require.NoError(t, plugin.OnHookEvent(lalmaxserver.HookEvent{
		Event: lalmaxserver.HookEventRelayPullStop, Payload: stop, Timestamp: time.Now().Format(time.RFC3339Nano),
	}))
	require.Len(t, repo.finishes, 1)
	require.NoError(t, plugin.OnHookEvent(lalmaxserver.HookEvent{
		Event: lalmaxserver.HookEventStreamActive, Payload: active,
	}))
	require.Equal(t, []bool{true}, repo.mediaStates)
	require.NoError(t, plugin.OnHookEvent(lalmaxserver.HookEvent{
		Event: lalmaxserver.HookEventGroupStop, Payload: active, Timestamp: "not-a-timestamp",
	}))
	require.Equal(t, []bool{true, false}, repo.mediaStates)

	// Unknown hook events are ignored.
	require.NoError(t, plugin.OnHookEvent(lalmaxserver.HookEvent{Event: "other"}))
}
