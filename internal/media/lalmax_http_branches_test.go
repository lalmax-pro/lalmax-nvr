package media

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHTTPAdapterEventSubscriptionRejection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	defer server.Close()
	e, err := NewLalmaxHTTP(LalmaxHTTPConfig{BaseURL: server.URL})
	require.NoError(t, err)
	ctx := context.Background()
	events, err := e.SubscribeEvents(ctx, EventFilter{StreamID: "cam", AppName: "live", Types: []EventType{EventPublisherStarted}})
	require.Nil(t, events)
	require.ErrorContains(t, err, "HTTP 403")
	rtmp, err := e.SubscribeRTMPEvents(ctx)
	require.Nil(t, rtmp)
	require.ErrorContains(t, err, "HTTP 403")
	srt, err := e.SubscribeSRTEvents(ctx)
	require.Nil(t, srt)
	require.ErrorContains(t, err, "HTTP 403")
	whip, err := e.SubscribeWHIPEvents(ctx)
	require.Nil(t, whip)
	require.ErrorContains(t, err, "HTTP 403")
}

func TestHTTPAdapterGetStreamSuccessAndPlayAliases(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"data":{"stream_name":"cam","app_name":"live","pub":{"id":"publisher"},"in_frame_per_sec":[{"unix_sec":1,"fps":20},{"unix_sec":2,"value":10}]}}`)
	}))
	defer server.Close()
	e, err := NewLalmaxHTTP(LalmaxHTTPConfig{BaseURL: server.URL, PublicURL: "https://example.test:12090", HTTPPort: 18080})
	require.NoError(t, err)
	info, err := e.GetStream(context.Background(), "unit-diagnostic-74")
	require.NoError(t, err)
	require.Equal(t, "cam", info.StreamID)
	require.Equal(t, float64(15), info.InFPS)
	require.True(t, info.Active)
	_, err = e.GetStream(context.Background(), "")
	require.Error(t, err)
	for _, tc := range []struct{ protocol, wantProtocol, scheme, path string }{
		{"", "hls", "https", "/hls/cam.m3u8"}, {"http-fmp4", "fmp4", "https", "/live/m4s/cam.mp4"},
		{"websocket-flv", "ws-flv", "wss", "/app/cam.flv"}, {"http-ts", "httpts", "https", "/live/cam.ts"},
		{"hls-fmp4", "ll-hls", "https", "/live/hls/cam/index.m3u8"}, {"llhls", "ll-hls", "https", "/live/hls/cam/index.m3u8"},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			before := time.Now()
			play, err := e.BuildPlayURL(context.Background(), PlayURLRequest{StreamID: "cam", AppName: "app", Protocol: tc.protocol, Token: "secret", TTL: time.Minute})
			require.NoError(t, err)
			require.Equal(t, tc.wantProtocol, play.Protocol)
			parsed, err := url.Parse(play.URL)
			require.NoError(t, err)
			require.Equal(t, tc.scheme, parsed.Scheme)
			require.Equal(t, tc.path, parsed.Path)
			require.Equal(t, "secret", parsed.Query().Get("token"))
			require.True(t, play.ExpiresAt.After(before.Add(time.Minute-time.Second)))
			if tc.wantProtocol == "fmp4" || tc.wantProtocol == "ll-hls" {
				require.Equal(t, "app", parsed.Query().Get("app_name"))
			}
		})
	}
	_, err = e.BuildPlayURL(context.Background(), PlayURLRequest{})
	require.Error(t, err)
	_, err = e.BuildPlayURL(context.Background(), PlayURLRequest{StreamID: "cam", Protocol: "unsupported"})
	require.ErrorContains(t, err, "unsupported play protocol")
}
