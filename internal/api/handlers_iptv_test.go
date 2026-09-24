package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/iptv"
	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestIPTVImportFromPlaylistText(t *testing.T) {
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetIPTVService(iptv.NewService(db, nil))

	body, err := json.Marshal(map[string]string{
		"name": "demo",
		"playlist_text": `#EXTM3U
#EXTINF:-1 tvg-id="demo" group-title="News",Demo
https://example.com/live/index.m3u8
`,
	})
	require.NoError(t, err)

	rr := doRequest(t, h.Routes(), http.MethodPost, "/api/iptv/imports", strings.NewReader(string(body)), "admin", "pass")
	require.Equal(t, http.StatusAccepted, rr.Code, rr.Body.String())

	var job struct {
		ID         string `json:"id"`
		TotalItems int    `json:"total_items"`
	}
	parseJSON(t, rr, &job)
	require.NotEmpty(t, job.ID)
	require.Equal(t, 1, job.TotalItems)

	rr = doRequest(t, h.Routes(), http.MethodGet, "/api/iptv/imports/"+job.ID+"/items", nil, "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var items struct {
		Total int `json:"total"`
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	parseJSON(t, rr, &items)
	require.Equal(t, 1, items.Total)
	require.Equal(t, "Demo", items.Items[0].Name)
}

func TestIPTVPlaybackReturnsSameOriginHLSProxy(t *testing.T) {
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	require.NoError(t, db.InsertIPTVSource(ctx, storage.IPTVSource{ID: "source-1", Name: "test", Enabled: true}))
	require.NoError(t, db.UpsertIPTVChannel(ctx, storage.IPTVChannel{
		ID: "channel-1", SourceID: "source-1", ExternalID: "id-1", Name: "Test", Enabled: true,
		SourceURL: "https://media.example.test/live/index.m3u8?token=private",
	}))
	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	h.SetIPTVService(iptv.NewService(db, nil))

	rr := doRequest(t, h.Routes(), http.MethodGet, "/api/iptv/channels/channel-1/playback", nil, "admin", "pass")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var response iptv.PlaybackDetails
	parseJSON(t, rr, &response)
	require.Equal(t, "/api/iptv/channels/channel-1/hls", response.URL)
	require.NotContains(t, response.URL, "media.example.test")
}

func TestIPTVUnavailableWithoutModule(t *testing.T) {
	db, store := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	h := NewHandler(db, store, noopAuthMW(), nil, nil, "", nil, nil)
	rr := doRequest(t, h.Routes(), http.MethodGet, "/api/iptv/channels", nil, "admin", "pass")
	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
}
