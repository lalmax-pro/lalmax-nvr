package media

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type listEngine struct {
	Engine
	streams []StreamInfo
	err     error
}

func (e listEngine) ListStreams(context.Context) ([]StreamInfo, error) {
	return e.streams, e.err
}

func TestIsInternalRecorderSession(t *testing.T) {
	require.True(t, IsInternalRecorderSession("NVR-RECORD", "abc"))
	require.True(t, IsInternalRecorderSession("nvr-record-internal", ""))
	require.True(t, IsInternalRecorderSession("rtsp", "nvr-record-cam1"))
	require.False(t, IsInternalRecorderSession("rtsp", "viewer-1"))
	require.False(t, IsInternalRecorderSession("", ""))
}

func TestSplitStreamKey(t *testing.T) {
	app, id := splitStreamKey("live/cam1")
	require.Equal(t, "live", app)
	require.Equal(t, "cam1", id)
	app, id = splitStreamKey("cam1")
	require.Equal(t, "", app)
	require.Equal(t, "cam1", id)
	app, id = splitStreamKey("live/cam/extra")
	require.Equal(t, "live", app)
	require.Equal(t, "cam/extra", id)
}

func TestNewStreamPollerDefaultInterval(t *testing.T) {
	p := NewStreamPoller(listEngine{}, 0)
	require.Equal(t, 5*time.Second, p.interval)
	p = NewStreamPoller(listEngine{}, time.Millisecond)
	require.Equal(t, time.Millisecond, p.interval)
}

func TestStreamPollerEmitsActiveAndStopped(t *testing.T) {
	engine := &listEngine{streams: []StreamInfo{
		{StreamID: "", AppName: "live", Active: true},
		{StreamID: "idle", AppName: "live", Active: false},
		{StreamID: "cam1", AppName: "live", Active: true},
	}}
	poller := NewStreamPoller(engine, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	out := make(chan Event, 8)
	known := map[string]bool{}
	poller.poll(ctx, known, out)
	require.Len(t, out, 1)
	got := <-out
	require.Equal(t, EventStreamActive, got.Type)
	require.Equal(t, "cam1", got.StreamID)
	require.True(t, known["live/cam1"])
	require.True(t, known["live/idle"])

	engine.streams = nil
	poller.poll(ctx, known, out)
	require.Len(t, out, 2)
	stopped := map[EventType]int{}
	for i := 0; i < 2; i++ {
		ev := <-out
		require.Equal(t, EventStreamStopped, ev.Type)
		stopped[ev.Type]++
	}
	require.Equal(t, 2, stopped[EventStreamStopped])
	require.Empty(t, known)
}

func TestStreamPollerIgnoresListErrorAndCancelledEmit(t *testing.T) {
	poller := NewStreamPoller(listEngine{err: errors.New("down")}, time.Hour)
	out := make(chan Event, 1)
	known := map[string]bool{}
	poller.poll(context.Background(), known, out)
	require.Empty(t, out)
	require.Empty(t, known)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	full := make(chan Event)
	poller.emit(ctx, full, Event{Type: EventStreamActive, StreamID: "cam"})
}

func TestStreamPollerRunStopsOnCancel(t *testing.T) {
	poller := NewStreamPoller(&listEngine{streams: []StreamInfo{
		{StreamID: "cam", AppName: "live", Active: true},
	}}, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	ch := poller.Run(ctx)
	select {
	case ev := <-ch:
		require.Equal(t, EventStreamActive, ev.Type)
		require.Equal(t, "cam", ev.StreamID)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first poll")
	}
	cancel()
	select {
	case _, ok := <-ch:
		require.False(t, ok)
	case <-time.After(time.Second):
		t.Fatal("poller did not close")
	}
}

func TestLalmaxAPIErrorAndRTPNotFound(t *testing.T) {
	err := &LalmaxAPIError{Code: lalErrorCodeRTPPubSessionNotFound, Description: "gone"}
	require.Equal(t, "lalmax error 1003: gone", err.Error())
	require.True(t, IsRTPReceiveSessionNotFound(err))
	require.False(t, IsRTPReceiveSessionNotFound(&LalmaxAPIError{Code: lalErrorCodeGroupNotFound}))
	require.False(t, IsRTPReceiveSessionNotFound(errors.New("gone")))
}

func TestNewLalmaxHTTPDefaultsAndErrors(t *testing.T) {
	_, err := NewLalmaxHTTP(LalmaxHTTPConfig{})
	require.ErrorContains(t, err, "lalmax base URL is required")
	_, err = NewLalmaxHTTP(LalmaxHTTPConfig{BaseURL: "://bad"})
	require.ErrorContains(t, err, "parse lalmax base URL")
	_, err = NewLalmaxHTTP(LalmaxHTTPConfig{BaseURL: "http://127.0.0.1:8080", PublicURL: "://bad"})
	require.ErrorContains(t, err, "parse lalmax public URL")

	engine, err := NewLalmaxHTTP(LalmaxHTTPConfig{BaseURL: "http://127.0.0.1:8080/"})
	require.NoError(t, err)
	require.Equal(t, "http://127.0.0.1:8080", engine.baseURL.String())
	require.Equal(t, engine.baseURL, engine.publicURL)
	require.NotZero(t, engine.rtmpPort)
	require.NotZero(t, engine.rtspPort)
	require.NotZero(t, engine.httpPort)
}
