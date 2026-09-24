package storage

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIPTVImportAndChannelRoundTrip(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	job := IPTVImportJob{ID: "job-1", Name: "demo", Status: "ready", TotalItems: 1}
	require.NoError(t, db.InsertIPTVImportJob(ctx, job))
	require.NoError(t, db.InsertIPTVImportItems(ctx, []IPTVImportItem{{
		ID:         "item-1",
		JobID:      "job-1",
		Name:       "CCTV-1",
		ExternalID: "cctv1",
		GroupName:  "news",
		SourceURL:  "https://example.com/a.m3u8",
		Status:     "playable",
		Playable:   true,
	}}))

	items, err := db.ListIPTVImportItems(ctx, "job-1", "playable", "CCTV")
	require.NoError(t, err)
	require.Len(t, items, 1)

	src := IPTVSource{ID: "src-1", Name: "demo", PlaylistURL: "https://example.com/live.m3u", Enabled: true}
	require.NoError(t, db.InsertIPTVSource(ctx, src))
	require.NoError(t, db.UpsertIPTVChannel(ctx, IPTVChannel{
		ID:         "ch-1",
		SourceID:   "src-1",
		ExternalID: "cctv1",
		StreamID:   "iptv_cctv1",
		Name:       "CCTV-1",
		GroupName:  "news",
		SourceURL:  "https://example.com/a.m3u8",
		Enabled:    true,
		Playable:   true,
	}))

	channels, err := db.ListIPTVChannels(ctx, "src-1", "news", "", nil)
	require.NoError(t, err)
	require.Len(t, channels, 1)
	require.Equal(t, "iptv_cctv1", channels[0].StreamID)
}
