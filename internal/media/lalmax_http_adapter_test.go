package media

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHTTPAdapterPullAndRTPRequests(t *testing.T) {
	type request struct {
		path, query string
		body        map[string]any
	}
	requests := make(chan request, 16)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		requests <- request{r.URL.Path, r.URL.RawQuery, body}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"error_code":0,"data":{"id":"fallback","session_id":"session","port":30000}}`)
	}))
	defer server.Close()
	e, err := NewLalmaxHTTP(LalmaxHTTPConfig{BaseURL: server.URL})
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, e.Start(ctx))
	require.Equal(t, "/api/stat/lal_info", (<-requests).path)
	require.NoError(t, e.Shutdown(ctx))
	for _, tc := range []struct {
		transport string
		retry     int
		forever   bool
		wantRetry float64
		tcp       bool
	}{
		{"TCP", 3, true, 3, true}, {"udp", 0, true, -1, false}, {"", 0, false, 0, true},
	} {
		session, err := e.StartPull(ctx, StartPullRequest{StreamID: "cam", AppName: "app", SourceURL: "rtsp://device/live", Transport: tc.transport, PullRetryNum: tc.retry, RetryForever: tc.forever, PullTimeout: 1500 * time.Millisecond, AutoStopNoView: 2 * time.Second})
		require.NoError(t, err)
		require.Equal(t, "session", session.SessionID)
		require.Equal(t, "app", session.AppName)
		got := <-requests
		require.Equal(t, "/api/ctrl/start_relay_pull", got.path)
		require.Equal(t, "cam", got.body["stream_name"])
		require.Equal(t, float64(1500), got.body["pull_timeout_ms"])
		require.Equal(t, float64(2000), got.body["auto_stop_pull_after_no_out_ms"])
		if tc.wantRetry == 0 {
			require.NotContains(t, got.body, "pull_retry_num")
		} else {
			require.Equal(t, tc.wantRetry, got.body["pull_retry_num"])
		}
		if tc.tcp {
			require.Equal(t, float64(0), got.body["rtsp_mode"])
		} else {
			require.NotContains(t, got.body, "rtsp_mode")
		}
	}
	for _, protocol := range []string{"TCP-passive", "udp"} {
		session, err := e.StartRTPReceive(ctx, StartRTPReceiveRequest{StreamID: "cam", AppName: "app", Protocol: protocol, Port: 30000, Timeout: time.Second})
		require.NoError(t, err)
		require.Equal(t, 30000, session.Port)
		require.Equal(t, "rtp", session.Protocol)
		got := <-requests
		require.Equal(t, "/api/ctrl/start_rtp_pub", got.path)
		require.Equal(t, float64(30000), got.body["port"])
		require.Equal(t, float64(1000), got.body["timeout_ms"])
		if protocol == "udp" {
			require.NotContains(t, got.body, "is_tcp_flag")
		} else {
			require.Equal(t, float64(1), got.body["is_tcp_flag"])
		}
	}
	require.NoError(t, e.StopPull(ctx, "cam space"))
	got := <-requests
	require.Equal(t, "/api/ctrl/stop_relay_pull", got.path)
	require.Equal(t, "stream_name=cam+space", got.query)
	require.Error(t, e.StopPull(ctx, ""))
	_, err = e.StartPull(ctx, StartPullRequest{})
	require.Error(t, err)
	_, err = e.StartPull(ctx, StartPullRequest{StreamID: "cam"})
	require.Error(t, err)
	_, err = e.StartRTPReceive(ctx, StartRTPReceiveRequest{})
	require.Error(t, err)
	require.Error(t, e.StopRTPReceive(ctx, ""))
}

func TestHTTPAdapterKickSessionAndErrors(t *testing.T) {
	for _, session := range []string{"publisher", "viewer", "missing"} {
		t.Run(session, func(t *testing.T) {
			kicks := make(chan map[string]string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/stat/all_group" {
					_, _ = fmt.Fprint(w, `{"data":{"groups":[{"stream_name":"cam","pub":{"session_id":"publisher"},"subs":[{"session_id":"viewer"}]}]}}`)
					return
				}
				var kicked map[string]string
				_ = json.NewDecoder(r.Body).Decode(&kicked)
				kicks <- kicked
				_, _ = fmt.Fprint(w, `{"error_code":0}`)
			}))
			defer server.Close()
			e, err := NewLalmaxHTTP(LalmaxHTTPConfig{BaseURL: server.URL})
			require.NoError(t, err)
			err = e.KickSession(context.Background(), session)
			if session == "missing" {
				require.ErrorContains(t, err, "not found")
				require.Empty(t, kicks)
			} else {
				require.NoError(t, err)
				require.Equal(t, map[string]string{"session_id": session, "stream_name": "cam"}, <-kicks)
			}
			require.Error(t, e.KickSession(context.Background(), ""))
		})
	}
	for _, tc := range []struct {
		name, body string
		status     int
		want       string
	}{
		{"http", "", 503, "HTTP 503"}, {"decode", "invalid json", 200, "decode lalmax response"},
		{"api", `{"error_code":1234,"desp":"refused"}`, 200, "lalmax error 1234: refused"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); _, _ = fmt.Fprint(w, tc.body) }))
			defer server.Close()
			e, err := NewLalmaxHTTP(LalmaxHTTPConfig{BaseURL: server.URL})
			require.NoError(t, err)
			ctx := context.Background()
			_, err = e.StartPull(ctx, StartPullRequest{StreamID: "cam", SourceURL: "rtsp://device/live"})
			require.ErrorContains(t, err, tc.want)
			_, err = e.StartRTPReceive(ctx, StartRTPReceiveRequest{StreamID: "cam"})
			require.ErrorContains(t, err, tc.want)
			err = e.KickSession(ctx, "viewer")
			require.ErrorContains(t, err, tc.want)
			_, err = e.GetStream(ctx, "cam")
			require.ErrorContains(t, err, tc.want)
		})
	}
	e, err := NewLalmaxHTTP(LalmaxHTTPConfig{BaseURL: "http://unused.invalid"})
	require.NoError(t, err)
	require.Error(t, e.postJSON(context.Background(), "/", nil, make(chan int), nil))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, e.Ready(ctx), context.Canceled)
	_, ok := responseBasic(&struct{}{})
	require.False(t, ok)
}

func TestHTTPAdapterSSEParsingAndMappings(t *testing.T) {
	pairs := []struct {
		hook string
		typ  EventType
	}{
		{"on_group_start", EventStreamStarted}, {"on_stream_active", EventStreamActive}, {"on_group_exit", EventStreamStopped},
		{"on_pub_start", EventPublisherStarted}, {"on_pub_stop", EventPublisherStopped}, {"on_sub_start", EventSubscriberStarted},
		{"on_sub_stop", EventSubscriberStopped}, {"on_relay_pull_start", EventRelayPullStarted}, {"on_relay_pull_stop", EventRelayPullStopped},
	}
	for _, pair := range pairs {
		require.Equal(t, pair.typ, hookToMediaEventType(pair.hook))
		require.Equal(t, pair.hook, mediaEventToHook(pair.typ))
	}
	require.Equal(t, EventStreamStopped, hookToMediaEventType("on_group_stop"))
	require.Equal(t, EventType("media.hook.custom"), hookToMediaEventType("custom"))
	require.Equal(t, "custom", mediaEventToHook(EventType("custom")))
	input := ": comment\nignored\n\nid: 42\nevent: on_pub_start\ntraceparent: trace\ntracestate: vendor=value\ndata: {\ndata: \"stream_name\":\"cam\",\"session_id\":\"pub\",\"app_name\":\"live\",\"protocol\":\"rtmp\"}\n\nevent: on_group_stop\ndata: invalid"
	out := make(chan Event, 3)
	scanSSE(context.Background(), strings.NewReader(input), out)
	require.Len(t, out, 2)
	ev := <-out
	require.Equal(t, int64(42), ev.ID)
	require.Equal(t, EventPublisherStarted, ev.Type)
	require.Equal(t, "cam", ev.StreamID)
	require.Equal(t, "pub", ev.SessionID)
	require.Equal(t, "trace", ev.Traceparent)
	require.Equal(t, "vendor=value", ev.Tracestate)
	ev = <-out
	require.Zero(t, ev.ID)
	require.Empty(t, ev.Traceparent)
	require.Empty(t, ev.Tracestate)
	require.Equal(t, EventStreamStopped, ev.Type)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	scanSSE(ctx, strings.NewReader("event: on_pub_start\n\n"), make(chan Event))
}

func TestHTTPAdapterTypedEventSubscriptions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Query().Get("events"), "on_group_stop") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		for _, name := range []string{"on_pub_start", "on_pub_stop", "on_stream_active", "on_group_stop", "on_sub_start"} {
			_, _ = fmt.Fprintf(w, "event: %s\ntraceparent: trace\ntracestate: state\ndata: {\"stream_name\":\"cam\",\"app_name\":\"live\",\"session_id\":\"pub\",\"protocol\":\"rtmp\"}\n\n", name)
		}
	}))
	defer server.Close()
	e, err := NewLalmaxHTTP(LalmaxHTTPConfig{BaseURL: server.URL})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	want := []string{"pub_start", "pub_stop", "stream_active", "stream_stopped"}
	rtmp, err := e.SubscribeRTMPEvents(ctx)
	require.NoError(t, err)
	var types []string
	for ev := range rtmp {
		types = append(types, ev.Type)
		require.Equal(t, "cam", ev.StreamID)
		require.Equal(t, "trace", ev.Traceparent)
		require.Equal(t, "state", ev.Tracestate)
	}
	require.Equal(t, want, types)
	srt, err := e.SubscribeSRTEvents(ctx)
	require.NoError(t, err)
	types = nil
	for ev := range srt {
		types = append(types, ev.Type)
		require.Equal(t, "pub", ev.SessionID)
	}
	require.Equal(t, want, types)
	whip, err := e.SubscribeWHIPEvents(ctx)
	require.NoError(t, err)
	types = nil
	for ev := range whip {
		types = append(types, ev.Type)
		require.Equal(t, "live", ev.AppName)
	}
	require.Equal(t, want, types)
}
