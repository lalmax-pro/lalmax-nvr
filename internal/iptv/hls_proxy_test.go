package iptv

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestRewriteHLSManifestProxiesSegmentsAndURIAttributes(t *testing.T) {
	baseURL, err := url.Parse("https://cdn.example.test/live/master.m3u8?token=abc")
	require.NoError(t, err)
	body := []byte("#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"keys/key.bin?key=1\"\n#EXTINF:6,\nsegments/001.ts?token=2\n")

	rewritten, err := RewriteHLSManifest(body, baseURL, "channel-1")
	require.NoError(t, err)
	text := string(rewritten)
	require.Contains(t, text, "/api/iptv/channels/channel-1/hls?url=https%3A%2F%2Fcdn.example.test%2Flive%2Fkeys%2Fkey.bin%3Fkey%3D1")
	require.Contains(t, text, "/api/iptv/channels/channel-1/hls?url=https%3A%2F%2Fcdn.example.test%2Flive%2Fsegments%2F001.ts%3Ftoken%3D2")
	require.NotContains(t, text, `URI="https://cdn.example.test`)
	require.NotContains(t, text, "\nsegments/001.ts")
}

func TestRewriteHLSManifestRejectsUnsafeChildURL(t *testing.T) {
	baseURL, err := url.Parse("https://cdn.example.test/live/index.m3u8")
	require.NoError(t, err)
	_, err = RewriteHLSManifest([]byte("#EXTM3U\nhttp://127.0.0.1/private.ts\n"), baseURL, "channel-1")
	require.Error(t, err)
}

func TestOpenPlaybackResourceUsesSavedHeadersAndRange(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	source := storage.IPTVSource{ID: "source-1", Name: "test", PlaylistURL: "https://playlist.example.test/list.m3u", Enabled: true, RequestHeaders: sealHeaders(map[string]string{"Authorization": "Bearer source-token"})}
	require.NoError(t, db.InsertIPTVSource(ctx, source))
	channel := storage.IPTVChannel{
		ID: "channel-1", SourceID: source.ID, ExternalID: "id-1", StreamID: "iptv_test", Name: "Test",
		SourceURL: sealSecret("https://media.example.test/live/index.m3u8"), RequestHeaders: sealHeaders(map[string]string{"Referer": "https://site.example.test/"}), Enabled: true,
	}
	require.NoError(t, db.UpsertIPTVChannel(ctx, channel))

	var gotURL string
	var gotAuth, gotReferer, gotRange string
	svc := NewService(db, nil)
	svc.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		gotURL = req.URL.String()
		gotAuth = req.Header.Get("Authorization")
		gotReferer = req.Header.Get("Referer")
		gotRange = req.Header.Get("Range")
		return &http.Response{
			StatusCode: http.StatusPartialContent,
			Header:     http.Header{"Content-Type": []string{"video/mp2t"}},
			Body:       io.NopCloser(strings.NewReader("segment")),
			Request:    req,
		}, nil
	})}

	resp, finalURL, err := svc.OpenPlaybackResource(ctx, channel.ID, "https://media.example.test/chunks/1.ts?sig=abc", "bytes=0-6")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, "https://media.example.test/chunks/1.ts?sig=abc", gotURL)
	require.Equal(t, "Bearer source-token", gotAuth)
	require.Equal(t, "https://site.example.test/", gotReferer)
	require.Equal(t, "bytes=0-6", gotRange)
	require.Equal(t, "media.example.test", finalURL.Host)
	require.Equal(t, http.StatusPartialContent, resp.StatusCode)
	resp.Body.Close()

	resp, _, err = svc.OpenPlaybackResource(ctx, channel.ID, "https://cdn.example.test/chunks/2.ts", "")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Empty(t, gotAuth, "source authorization must not be sent to a different CDN origin")
	require.Empty(t, gotReferer, "source headers must not be sent to a different CDN origin")
}

func TestGetPlaybackDetailsReturnsSameOriginProxyURL(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	require.NoError(t, db.InsertIPTVSource(ctx, storage.IPTVSource{ID: "source-1", Name: "test", Enabled: true}))
	require.NoError(t, db.UpsertIPTVChannel(ctx, storage.IPTVChannel{
		ID: "channel-1", SourceID: "source-1", ExternalID: "id-1", Name: "Test", Enabled: true,
		SourceURL: sealSecret("https://media.example.test/live/index.m3u8?token=private"),
	}))

	details, err := NewService(db, nil).GetPlaybackDetails(ctx, "channel-1")
	require.NoError(t, err)
	require.Equal(t, "/api/iptv/channels/channel-1/hls", details.URL)
	require.NotContains(t, details.URL, "media.example.test")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
