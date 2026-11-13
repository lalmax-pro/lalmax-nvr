package main

import (
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	lalmaxserver "github.com/q191201771/lalmax/server"
)

const (
	testGBDeviceID = "32010000001327000001"
	testGBChannelID = "34020000001320000001"
)

func missingGBWHEPHook(app, schema, stream string) lalmaxserver.HookEvent {
	body, _ := json.Marshal(map[string]string{
		"app": app, "schema": schema, "stream": stream,
	})
	return lalmaxserver.HookEvent{Event: lalmaxserver.HookEventStreamNotFound, Payload: body}
}

func awaitGBWHEPStart(t *testing.T, started <-chan string) string {
	t.Helper()
	select {
	case stream := <-started: return stream
	case <-time.After(2*time.Second):
		t.Fatal("GB28181 on-demand start not requested")
		return ""
	}
}

func TestGBWHEPOnDemandEnabledValidationAndDedupe(t *testing.T) {
	stream := testGBDeviceID + ":" + testGBChannelID
	started := make(chan string, 2)
	release := make(chan struct{})
	var calls atomic.Int32
	var accepted atomic.Int32
	h := newGBWHEPOnDemandHook(
		func(dev, ch string) bool {
			accepted.Add(1)
			return dev == testGBDeviceID && ch == testGBChannelID
		},
		func(dev, ch, streamID string) error {
			calls.Add(1)
			started <- streamID
			<-release
			return nil
		},
	)
	ev := missingGBWHEPHook("live", "whep", stream)
	if err := h.OnHookEvent(ev); err != nil { t.Fatal(err) }
	if calls.Load() != 0 { t.Fatal("disabled hook launched SIP INVITE") }

	h.SetEnabled(true)
	for _, ignored := range []lalmaxserver.HookEvent{
		missingGBWHEPHook("live", "srt", stream),
		missingGBWHEPHook("other", "whep", stream),
		missingGBWHEPHook("live", "whep", "stream102"),
		missingGBWHEPHook("live", "whep", testGBDeviceID + ":invalid"),
	} {
		if err := h.OnHookEvent(ignored); err != nil { t.Fatal(err) }
	}
	if calls.Load() != 0 || accepted.Load() != 0 {
		t.Fatal("unexpected SIP INVITE for unapproved protocol or unregistered stream")
	}

	if err := h.OnHookEvent(ev); err != nil { t.Fatal(err) }
	if got := awaitGBWHEPStart(t, started); got != stream { t.Fatalf("stream=%s", got) }
	if err := h.OnHookEvent(ev); err != nil { t.Fatal(err) }
	if got := calls.Load(); got != 1 { t.Fatalf("duplicate INVITE: %d", got) }
	close(release)
	// A repeat after start is throttled even if the first INVITE returned.
	if err := h.OnHookEvent(ev); err != nil { t.Fatal(err) }
	if got := calls.Load(); got != 1 { t.Fatalf("cooldown bypass: %d", got) }
}

func TestGBWHEPOnDemandBadEventAndUnknownChannel(t *testing.T) {
	h := newGBWHEPOnDemandHook(
		func(string, string) bool { return false },
		func(string, string, string) error { t.Error("called start on unknown channel"); return nil },
	)
	h.SetEnabled(true)
	bad := lalmaxserver.HookEvent{Event: lalmaxserver.HookEventStreamNotFound, Payload: []byte("{")}
	if err := h.OnHookEvent(bad); err == nil { t.Fatal("malformed event accepted") }
	ev := missingGBWHEPHook("live", "whep", testGBDeviceID+":"+testGBChannelID)
	if err := h.OnHookEvent(ev); err != nil { t.Fatal(err) }
	if _, _, ok := splitGBWHEPStreamID(testGBDeviceID+":"+testGBChannelID); !ok {
		t.Fatal("GB28181 20-digit IDs not recognized")
	}
	if _, _, ok := splitGBWHEPStreamID(testGBDeviceID+":"+testGBChannelID+":extra"); ok {
		t.Fatal("extra colon suffix accepted")
	}
}

func TestGBWHEPOnDemandConcurrencyLimit(t *testing.T) {
	started := make(chan string, 8)
	release := make(chan struct{})
	h := newGBWHEPOnDemandHook(
		func(string, string) bool { return true },
		func(_, _, streamID string) error { started <- streamID; <-release; return nil },
	)
	h.SetEnabled(true)
	for i := 1; i <= gbWHEPOnDemandMaxConcurrent; i++ {
		id := fmt.Sprintf("%020d", i)
		ev := missingGBWHEPHook("live", "whep", id+":"+testGBChannelID)
		if err := h.OnHookEvent(ev); err != nil { t.Fatal(err) }
	}
	for i := 0; i < gbWHEPOnDemandMaxConcurrent; i++ { awaitGBWHEPStart(t, started) }
	extra := missingGBWHEPHook("live", "whep", fmt.Sprintf("%020d", 100)+":"+testGBChannelID)
	if err := h.OnHookEvent(extra); err != nil { t.Fatal(err) }
	select {
	case unexpected := <-started: t.Fatalf("exceeded concurrency ceiling: %s", unexpected)
	default:
	}
	close(release)
}


// RTSP DESCRIBE and RTMP play must wake only known channels, and requests
// arriving through different protocols must share one INVITE/cooldown.
func TestGBOnDemandRTSPRTMPPlaybackAndCrossProtocolDedupe(t *testing.T) {
	stream := testGBDeviceID + ":" + testGBChannelID
	started := make(chan string, 4)
	release := make(chan struct{})
	var calls atomic.Int32
	h := newGBWHEPOnDemandHook(
		func(dev, ch string) bool { return dev == testGBDeviceID && ch == testGBChannelID },
		func(_, _, streamID string) error {
			calls.Add(1)
			started <- streamID
			<-release
			return nil
		},
	)
	h.SetEnabled(true)
	for _, schema := range []string{"rtsp", "rtmp", "whep"} {
		if err := h.OnHookEvent(missingGBWHEPHook("live", schema, stream)); err != nil { t.Fatal(err) }
	}
	if got := awaitGBWHEPStart(t, started); got != stream { t.Fatalf("start stream=%s", got) }
	if got := calls.Load(); got != 1 { t.Fatalf("cross-protocol duplicate INVITE: %d", got) }
	close(release)
	// A new channel can still be started by native RTMP even with the same hook.
	another := testGBDeviceID + ":" + "34020000001320000002"
	otherStarted := make(chan string, 1)
	h2 := newGBWHEPOnDemandHook(func(string,string) bool { return true }, func(_,_,streamID string) error { otherStarted <- streamID; return nil })
	h2.SetEnabled(true)
	if err := h2.OnHookEvent(missingGBWHEPHook("live", "rtmp", another)); err != nil { t.Fatal(err) }
	if got := awaitGBWHEPStart(t, otherStarted); got != another { t.Fatalf("rtmp start=%s", got) }
}

func TestGBOnDemandNativePlaybackRejectsInvalidRequests(t *testing.T) {
	var calls atomic.Int32
	h := newGBWHEPOnDemandHook(func(string,string) bool { return true },
		func(string,string,string) error { calls.Add(1); return nil })
	h.SetEnabled(true)
	stream := testGBDeviceID + ":" + testGBChannelID
	for _, event := range []lalmaxserver.HookEvent{
		missingGBWHEPHook("other", "rtsp", stream),
		missingGBWHEPHook("live", "rtsp", "unknown"),
		missingGBWHEPHook("live", "rtmp", testGBDeviceID + ":not-a-gb-channel"),
		missingGBWHEPHook("live", "publish", stream),
	} {
		if err := h.OnHookEvent(event); err != nil { t.Fatal(err) }
	}
	if calls.Load() != 0 { t.Fatal("unknown or publish requests woke GB28181 channel") }
}
