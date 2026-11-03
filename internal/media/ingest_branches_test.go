package media

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestIngestEventDeduplicationAndLifecycle(t *testing.T) {
	var starts, stops, resolves int
	h := NewWHIPIngestHandler(nil, func(stream string) (string, bool) { resolves++; return "camera", stream == "cam" }, func(camera, stream string) {
		require.Equal(t, "camera", camera)
		require.Equal(t, "cam", stream)
		starts++
	}, func(camera, stream string) {
		require.Equal(t, "camera", camera)
		require.Equal(t, "cam", stream)
		stops++
	})
	ctx := context.Background()
	h.Stop() // Safe before Start.
	h.handleEvent(ctx, ingestEvent{Type: "pub_start", Protocol: "customize"})
	h.handleEvent(ctx, ingestEvent{Type: "unknown", StreamID: "cam", Protocol: "whip"})
	h.handleEvent(ctx, ingestEvent{Type: "stream_stopped", StreamID: "cam", Protocol: "webrtc"})
	require.Zero(t, h.ActiveIngests())
	require.Zero(t, resolves)
	h.handleEvent(ctx, ingestEvent{Type: "stream_active", StreamID: "cam", SessionID: "pub", Protocol: " WHIP_PUSH ", Traceparent: "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01", Tracestate: "vendor=value"})
	h.handleEvent(ctx, ingestEvent{Type: "pub_start", StreamID: "cam", Protocol: ""})
	require.Equal(t, 1, starts)
	require.Equal(t, 1, resolves)
	require.Equal(t, 1, h.ActiveIngests())
	h.handleEvent(ctx, ingestEvent{Type: "stream_stopped", StreamID: "cam", Protocol: "customize"})
	h.handleEvent(ctx, ingestEvent{Type: "pub_stop", StreamID: "cam", Protocol: "whip"})
	require.Equal(t, 1, stops)
	require.Zero(t, h.ActiveIngests())
	h.handleEvent(ctx, ingestEvent{Type: "pub_start", StreamID: "cam", Protocol: "whip"})
	require.Equal(t, 2, starts)
	require.Equal(t, 1, h.ActiveIngests())
}

func TestSRTStreamNameFormats(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"plain", "plain"}, {"", ""}, {"#!::h=cam,m=publish", "cam"}, {"#!::r=live/cam,m=publish", "live/cam"},
		{"#!::h=,r=fallback", "fallback"}, {"#!::broken,m=publish", ""}, {"#!::h=cam=extra", "cam=extra"},
	} {
		t.Run(tc.input, func(t *testing.T) { require.Equal(t, tc.want, SRTStreamName(tc.input)) })
	}
}

type failingIngestUpstream struct{ err error }

func (s failingIngestUpstream) SubscribeRTMPEvents(context.Context) (<-chan RTMPEvent, error) {
	return nil, s.err
}
func (s failingIngestUpstream) SubscribeSRTEvents(context.Context) (<-chan SRTEvent, error) {
	return nil, s.err
}
func (s failingIngestUpstream) SubscribeWHIPEvents(context.Context) (<-chan WHIPEvent, error) {
	return nil, s.err
}

type notifiedIngestSubscriber struct {
	subscribed chan struct{}
	err        error
}

func (s notifiedIngestSubscriber) protocol() string { return "rtmp" }
func (s notifiedIngestSubscriber) subscribe(context.Context) (<-chan ingestEvent, error) {
	close(s.subscribed)
	return nil, s.err
}

func TestIngestSubscriptionErrorsAndCancellation(t *testing.T) {
	sentinel := errors.New("subscription unavailable")
	upstream := failingIngestUpstream{err: sentinel}
	for _, sub := range []ingestSubscriber{rtmpSubscriber{upstream}, srtSubscriber{upstream}, whipSubscriber{upstream}} {
		events, err := sub.subscribe(context.Background())
		require.Nil(t, events)
		require.ErrorIs(t, err, sentinel)
	}
	sub := notifiedIngestSubscriber{subscribed: make(chan struct{}), err: sentinel}
	h := newIngestHandler(sub, nil, nil, nil)
	require.NoError(t, h.Start(context.Background()))
	select {
	case <-sub.subscribed:
	case <-time.After(time.Second):
		t.Fatal("subscription was not attempted")
	}
	stopped := make(chan struct{})
	go func() { h.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop waited for retry timer")
	}
}

func TestStreamPollerRetainsKnownStreamsAndNamespaces(t *testing.T) {
	engine := &listEngine{streams: []StreamInfo{{StreamID: "cam", AppName: "one", Active: true}, {StreamID: "cam", AppName: "two", Active: true}}}
	p := NewStreamPoller(engine, time.Hour)
	known := map[string]bool{}
	out := make(chan Event, 8)
	p.poll(context.Background(), known, out)
	require.Len(t, out, 2)
	<-out
	<-out
	p.poll(context.Background(), known, out) // A repeated snapshot must not re-emit active events.
	require.Empty(t, out)
	engine.err = errors.New("temporary failure")
	p.poll(context.Background(), known, out)
	require.Len(t, known, 2)
	require.Empty(t, out)
	engine.err = nil
	engine.streams = engine.streams[:1]
	p.poll(context.Background(), known, out)
	require.Len(t, out, 1)
	ev := <-out
	require.Equal(t, EventStreamStopped, ev.Type)
	require.Equal(t, "two", ev.AppName)
	require.Equal(t, "cam", ev.StreamID)
	require.Equal(t, map[string]bool{"one/cam": true}, known)
}
