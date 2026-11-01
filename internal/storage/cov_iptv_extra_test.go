package storage

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func covIPTVTime(d time.Duration) time.Time {
	return time.Date(2024, 6, 1, 8, 0, 0, 0, time.UTC).Add(d)
}

func covIPSeedSource(t *testing.T, db *DB, id, name string) {
	t.Helper()
	require.NoError(t, db.InsertIPTVSource(context.Background(), IPTVSource{
		ID:                 id,
		Name:               name,
		PlaylistURL:        "https://example.com/" + id + ".m3u",
		Enabled:            true,
		RefreshIntervalSec: 3600,
		RefreshStatus:      "idle",
	}))
}

func covIPSeedChannel(t *testing.T, db *DB, ch IPTVChannel) {
	t.Helper()
	require.NoError(t, db.UpsertIPTVChannel(context.Background(), ch))
}

// TestCovIPTVImportJobs covers the import job lifecycle and defaults.
func TestCovIPTVImportJobs(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// Zero timestamps are defaulted on insert.
	require.NoError(t, db.InsertIPTVImportJob(ctx, IPTVImportJob{ID: "job-1", Name: "one", Status: "running", TotalItems: 3}))

	job, err := db.GetIPTVImportJob(ctx, "job-1")
	require.NoError(t, err)
	require.NotNil(t, job)
	require.Equal(t, "one", job.Name)
	require.Equal(t, "running", job.Status)
	require.Equal(t, 3, job.TotalItems)
	require.False(t, job.CreatedAt.IsZero())
	require.False(t, job.UpdatedAt.IsZero())

	require.NoError(t, db.InsertIPTVImportJob(ctx, IPTVImportJob{
		ID: "job-2", Name: "two", Status: "done", CreatedAt: covIPTVTime(0), UpdatedAt: covIPTVTime(time.Minute),
	}))
	job2, err := db.GetIPTVImportJob(ctx, "job-2")
	require.NoError(t, err)
	require.True(t, job2.CreatedAt.Equal(covIPTVTime(0)))
	require.True(t, job2.UpdatedAt.Equal(covIPTVTime(time.Minute)))

	require.NoError(t, db.UpdateIPTVImportJob(ctx, IPTVImportJob{ID: "job-1", Name: "renamed", Status: "committed", Error: "warn", TotalItems: 7}))
	updated, err := db.GetIPTVImportJob(ctx, "job-1")
	require.NoError(t, err)
	require.Equal(t, "renamed", updated.Name)
	require.Equal(t, "committed", updated.Status)
	require.Equal(t, "warn", updated.Error)
	require.Equal(t, 7, updated.TotalItems)
	require.False(t, updated.UpdatedAt.Before(updated.CreatedAt))

	missing, err := db.GetIPTVImportJob(ctx, "nope")
	require.NoError(t, err)
	require.Nil(t, missing)
}

// TestCovIPTVImportItems covers item insertion, updates, filters and lookups.
func TestCovIPTVImportItems(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	checked := covIPTVTime(time.Hour)

	// Empty batch is a no-op.
	require.NoError(t, db.InsertIPTVImportItems(ctx, nil))

	items := []IPTVImportItem{
		{ID: "item-1", JobID: "job-1", RowNo: 1, ExternalID: "cctv1", Name: "CCTV 1", GroupName: "news",
			SourceURL: "https://example.com/1.m3u8", Status: "playable", Playable: true, HTTPStatus: 200,
			ContentType: "application/vnd.apple.mpegurl", VideoCodec: "h264", AudioCodec: "aac",
			Encrypted: true, DRM: true, Recordable: true, CheckedAt: &checked, RequestHeaders: "X-A: 1"},
		{ID: "item-2", JobID: "job-1", RowNo: 2, ExternalID: "cctv_2", Name: "CCTV 2", GroupName: "sports",
			SourceURL: "https://example.com/2.m3u8", Status: "broken", Error: "404"},
		{ID: "item-3", JobID: "job-2", RowNo: 1, ExternalID: "hunan", Name: "Hunan TV", GroupName: "local",
			SourceURL: "https://example.com/3.m3u8", Status: "playable"},
	}
	require.NoError(t, db.InsertIPTVImportItems(ctx, items))

	got, err := db.GetIPTVImportItem(ctx, "item-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "CCTV 1", got.Name)
	require.Equal(t, 200, got.HTTPStatus)
	require.Equal(t, "application/vnd.apple.mpegurl", got.ContentType)
	require.Equal(t, "h264", got.VideoCodec)
	require.Equal(t, "aac", got.AudioCodec)
	require.True(t, got.Encrypted)
	require.True(t, got.DRM)
	require.True(t, got.Playable)
	require.True(t, got.Recordable)
	require.Equal(t, "X-A: 1", got.RequestHeaders)
	require.NotNil(t, got.CheckedAt)
	require.True(t, got.CheckedAt.Equal(checked))

	plain, err := db.GetIPTVImportItem(ctx, "item-2")
	require.NoError(t, err)
	require.Nil(t, plain.CheckedAt)
	require.False(t, plain.Playable)

	missing, err := db.GetIPTVImportItem(ctx, "nope")
	require.NoError(t, err)
	require.Nil(t, missing)

	// Job-scoped listing, ordered by row_no.
	jobItems, err := db.ListIPTVImportItems(ctx, "job-1", "", "")
	require.NoError(t, err)
	require.Len(t, jobItems, 2)
	require.Equal(t, "item-1", jobItems[0].ID)
	require.Equal(t, "item-2", jobItems[1].ID)

	playableOnly, err := db.ListIPTVImportItems(ctx, "job-1", "playable", "")
	require.NoError(t, err)
	require.Len(t, playableOnly, 1)
	require.Equal(t, "item-1", playableOnly[0].ID)

	// LIKE escaping: "_" is literal here, so only cctv_2 matches.
	byQ, err := db.ListIPTVImportItems(ctx, "job-1", "", "cctv_")
	require.NoError(t, err)
	require.Len(t, byQ, 1)
	require.Equal(t, "item-2", byQ[0].ID)

	// Search by group and external ID.
	byGroup, err := db.ListIPTVImportItems(ctx, "job-1", "", " sports ")
	require.NoError(t, err)
	require.Len(t, byGroup, 1)
	byExternal, err := db.ListIPTVImportItems(ctx, "job-1", "", "cctv1")
	require.NoError(t, err)
	require.Len(t, byExternal, 1)

	none, err := db.ListIPTVImportItems(ctx, "job-missing", "", "")
	require.NoError(t, err)
	require.Empty(t, none)

	byIDs, err := db.GetIPTVImportItemsByIDs(ctx, "job-1", []string{"item-1", "item-3"})
	require.NoError(t, err)
	require.Len(t, byIDs, 1)
	require.Equal(t, "item-1", byIDs[0].ID)

	byIDs, err = db.GetIPTVImportItemsByIDs(ctx, "job-1", nil)
	require.NoError(t, err)
	require.Nil(t, byIDs)

	// Updating an item persists probe results.
	require.NoError(t, db.UpdateIPTVImportItem(ctx, IPTVImportItem{
		ID: "item-2", Status: "playable", Error: "", HTTPStatus: 200, ContentType: "video/mp4",
		VideoCodec: "hevc", AudioCodec: "aac", Playable: true, Recordable: true, CheckedAt: &checked,
	}))
	updated, err := db.GetIPTVImportItem(ctx, "item-2")
	require.NoError(t, err)
	require.Equal(t, "playable", updated.Status)
	require.Equal(t, 200, updated.HTTPStatus)
	require.Equal(t, "hevc", updated.VideoCodec)
	require.True(t, updated.Recordable)
	require.NotNil(t, updated.CheckedAt)

	// A duplicate ID aborts the whole batch and rolls back.
	err = db.InsertIPTVImportItems(ctx, []IPTVImportItem{
		{ID: "item-dup", JobID: "job-1", Name: "dup", SourceURL: "u", Status: "new"},
		{ID: "item-1", JobID: "job-1", Name: "duplicate", SourceURL: "u", Status: "new"},
	})
	require.Error(t, err)
	dup, err := db.GetIPTVImportItem(ctx, "item-dup")
	require.NoError(t, err)
	require.Nil(t, dup, "failed batch must not leave partial rows")

	if _, err := db.execContext(context.Background(), `DROP TABLE iptv_import_items`); err != nil {
		require.NoError(t, err)
	}
	err = db.InsertIPTVImportItems(ctx, items)
	require.Error(t, err, "prepare failure is surfaced")
}

// TestCovIPTVSources covers source CRUD, refresh bookkeeping and delete cascade.
func TestCovIPTVSources(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	require.NoError(t, db.InsertIPTVSource(ctx, IPTVSource{
		ID: "src-1", ImportJobID: "job-1", Name: "alpha", PlaylistURL: "https://example.com/a.m3u",
		RequestHeaders: "X: y", Enabled: true, RefreshIntervalSec: 600,
	}))
	require.NoError(t, db.InsertIPTVSource(ctx, IPTVSource{
		ID: "src-2", Name: "beta", PlaylistURL: "https://example.com/b.m3u",
		Enabled: false, RefreshIntervalSec: 900, CreatedAt: covIPTVTime(0), UpdatedAt: covIPTVTime(time.Minute),
	}))

	src, err := db.GetIPTVSource(ctx, "src-1")
	require.NoError(t, err)
	require.NotNil(t, src)
	require.Equal(t, "alpha", src.Name)
	require.True(t, src.Enabled)
	require.Equal(t, 600, src.RefreshIntervalSec)
	require.Equal(t, "X: y", src.RequestHeaders)
	require.Nil(t, src.LastRefreshedAt)
	require.False(t, src.CreatedAt.IsZero())

	src2, err := db.GetIPTVSource(ctx, "src-2")
	require.NoError(t, err)
	require.False(t, src2.Enabled)
	require.True(t, src2.CreatedAt.Equal(covIPTVTime(0)))

	missing, err := db.GetIPTVSource(ctx, "nope")
	require.NoError(t, err)
	require.Nil(t, missing)

	byJob, err := db.GetIPTVSourceByImportJob(ctx, "job-1")
	require.NoError(t, err)
	require.NotNil(t, byJob)
	require.Equal(t, "src-1", byJob.ID)
	byJob, err = db.GetIPTVSourceByImportJob(ctx, "job-none")
	require.NoError(t, err)
	require.Nil(t, byJob)

	listed, err := db.ListIPTVSources(ctx)
	require.NoError(t, err)
	require.Len(t, listed, 2)
	require.Equal(t, "src-1", listed[0].ID, "ordered by name")

	require.NoError(t, db.UpdateIPTVSource(ctx, IPTVSource{ID: "src-1", Name: "alpha-renamed", Enabled: false, RefreshIntervalSec: 30}))
	updated, err := db.GetIPTVSource(ctx, "src-1")
	require.NoError(t, err)
	require.Equal(t, "alpha-renamed", updated.Name)
	require.False(t, updated.Enabled)
	require.Equal(t, 30, updated.RefreshIntervalSec)

	// Refresh without a timestamp keeps the previous value.
	refreshedAt := covIPTVTime(2 * time.Hour)
	require.NoError(t, db.UpdateIPTVSourceRefresh(ctx, "src-1", "ok", &refreshedAt, "", 4, 1))
	refreshed, err := db.GetIPTVSource(ctx, "src-1")
	require.NoError(t, err)
	require.Equal(t, "ok", refreshed.RefreshStatus)
	require.Equal(t, 4, refreshed.LastRefreshNew)
	require.Equal(t, 1, refreshed.LastRefreshMissing)
	require.NotNil(t, refreshed.LastRefreshedAt)
	require.True(t, refreshed.LastRefreshedAt.Equal(refreshedAt))

	require.NoError(t, db.UpdateIPTVSourceRefresh(ctx, "src-1", "error", nil, "boom", 0, 0))
	failed, err := db.GetIPTVSource(ctx, "src-1")
	require.NoError(t, err)
	require.Equal(t, "error", failed.RefreshStatus)
	require.Equal(t, "boom", failed.LastError)
	require.NotNil(t, failed.LastRefreshedAt)
	require.True(t, failed.LastRefreshedAt.Equal(refreshedAt), "COALESCE keeps the previous refresh time")

	// Deleting a source also removes its channels.
	covIPSeedChannel(t, db, IPTVChannel{ID: "ch-1", SourceID: "src-1", ExternalID: "e1", StreamID: "iptv_e1", Name: "e1", SourceURL: "u", Enabled: true})
	require.NoError(t, db.DeleteIPTVSource(ctx, "src-1"))
	gone, err := db.GetIPTVSource(ctx, "src-1")
	require.NoError(t, err)
	require.Nil(t, gone)
	goneChannel, err := db.GetIPTVChannel(ctx, "ch-1")
	require.NoError(t, err)
	require.Nil(t, goneChannel)
}

// TestCovIPTVChannels covers channel upsert, filters, metadata updates and groups.
func TestCovIPTVChannels(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	covIPSeedSource(t, db, "src-1", "alpha")
	covIPSeedSource(t, db, "src-2", "beta")

	checked := covIPTVTime(time.Hour)
	covIPSeedChannel(t, db, IPTVChannel{
		ID: "ch-1", SourceID: "src-1", ExternalID: "e1", StreamID: "iptv_e1", ChannelNo: "1",
		Name: "CCTV 1", GroupName: "news", SourceURL: "https://example.com/1.m3u8",
		Enabled: true, Favorite: true, Playable: true, Recordable: true, PublishEnabled: true,
		VideoCodec: "h264", AudioCodec: "aac", ProbeStatus: "ok", LastCheckedAt: &checked,
		RequestHeaders: "X: 1",
	})
	covIPSeedChannel(t, db, IPTVChannel{
		ID: "ch-2", SourceID: "src-1", ExternalID: "e_2", StreamID: "iptv_e2",
		Name: "CCTV 2", GroupName: "sports", SourceURL: "https://example.com/2.m3u8", Enabled: true,
	})
	covIPSeedChannel(t, db, IPTVChannel{
		ID: "ch-3", SourceID: "src-2", ExternalID: "e3", StreamID: "iptv_e3",
		Name: "Hunan TV", GroupName: "local", SourceURL: "https://example.com/3.m3u8",
	})

	got, err := db.GetIPTVChannel(ctx, "ch-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.True(t, got.Favorite)
	require.True(t, got.Playable)
	require.True(t, got.Recordable)
	require.True(t, got.PublishEnabled)
	require.Equal(t, "h264", got.VideoCodec)
	require.Equal(t, "aac", got.AudioCodec)
	require.Equal(t, "X: 1", got.RequestHeaders)
	require.NotNil(t, got.LastCheckedAt)

	plain, err := db.GetIPTVChannel(ctx, "ch-2")
	require.NoError(t, err)
	require.Nil(t, plain.LastCheckedAt)
	require.False(t, plain.Favorite)

	missing, err := db.GetIPTVChannel(ctx, "nope")
	require.NoError(t, err)
	require.Nil(t, missing)

	// Upsert with the same (source_id, external_id) updates in place.
	covIPSeedChannel(t, db, IPTVChannel{
		ID: "ignored", SourceID: "src-1", ExternalID: "e1", StreamID: "iptv_e1",
		Name: "CCTV 1 HD", GroupName: "news-hd", SourceURL: "https://example.com/1b.m3u8",
		Enabled: false, Playable: true, VideoCodec: "hevc", RequestHeaders: "X: 2",
	})
	upserted, err := db.GetIPTVChannel(ctx, "ch-1")
	require.NoError(t, err)
	require.Equal(t, "CCTV 1 HD", upserted.Name)
	require.Equal(t, "news-hd", upserted.GroupName)
	require.Equal(t, "hevc", upserted.VideoCodec)
	require.Equal(t, "X: 2", upserted.RequestHeaders)
	require.True(t, upserted.Favorite, "upsert preserves operator-managed flags")
	all, err := db.ListIPTVChannels(ctx, "", "", "", nil)
	require.NoError(t, err)
	require.Len(t, all, 3, "upsert must not create a duplicate channel")

	bySource, err := db.ListIPTVChannels(ctx, "src-1", "", "", nil)
	require.NoError(t, err)
	require.Len(t, bySource, 2)

	byGroup, err := db.ListIPTVChannels(ctx, "", "local", "", nil)
	require.NoError(t, err)
	require.Len(t, byGroup, 1)
	require.Equal(t, "ch-3", byGroup[0].ID)

	fav := true
	favorites, err := db.ListIPTVChannels(ctx, "", "", "", &fav)
	require.NoError(t, err)
	require.Len(t, favorites, 1)
	require.Equal(t, "ch-1", favorites[0].ID)

	notFav := false
	nonFavorites, err := db.ListIPTVChannels(ctx, "", "", "", &notFav)
	require.NoError(t, err)
	require.Len(t, nonFavorites, 2)

	// "_" is escaped, so searching "e_" only matches the literal external ID "e_2".
	byQ, err := db.ListIPTVChannels(ctx, "", "", " e_ ", nil)
	require.NoError(t, err)
	require.Len(t, byQ, 1)
	require.Equal(t, "ch-2", byQ[0].ID)

	byName, err := db.ListIPTVChannels(ctx, "", "", " cctv  ", nil)
	require.NoError(t, err)
	require.Len(t, byName, 2)

	byGroupQ, err := db.ListIPTVChannels(ctx, "", "", "sports", nil)
	require.NoError(t, err)
	require.Len(t, byGroupQ, 1)

	require.NoError(t, db.UpdateIPTVChannelMeta(ctx, "ch-1", "Renamed", false, false, false))
	renamed, err := db.GetIPTVChannel(ctx, "ch-1")
	require.NoError(t, err)
	require.Equal(t, "Renamed", renamed.Name)
	require.False(t, renamed.Enabled)
	require.False(t, renamed.Favorite)
	require.False(t, renamed.PublishEnabled)

	groups, err := db.ListIPTVGroups(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"local", "news-hd", "sports"}, groups)

	require.NoError(t, db.DeleteIPTVChannel(ctx, "ch-2"))
	gone, err := db.GetIPTVChannel(ctx, "ch-2")
	require.NoError(t, err)
	require.Nil(t, gone)

	// Deleting a channel that does not exist is not an error.
	require.NoError(t, db.DeleteIPTVChannel(ctx, "ch-2"))
}

// TestCovIPTVClosedDBErrorPaths verifies errors are propagated rather than swallowed.
func TestCovIPTVClosedDBErrorPaths(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	covIPSeedSource(t, db, "src-1", "alpha")
	require.NoError(t, db.Close())

	require.Error(t, db.InsertIPTVImportJob(ctx, IPTVImportJob{ID: "j", Status: "new"}))
	require.Error(t, db.UpdateIPTVImportJob(ctx, IPTVImportJob{ID: "j"}))
	_, err := db.GetIPTVImportJob(ctx, "j")
	require.Error(t, err)
	require.NotErrorIs(t, err, sql.ErrNoRows)

	require.Error(t, db.InsertIPTVImportItems(ctx, []IPTVImportItem{{ID: "i", JobID: "j", Name: "n", SourceURL: "u", Status: "new"}}))
	require.Error(t, db.UpdateIPTVImportItem(ctx, IPTVImportItem{ID: "i"}))
	_, err = db.ListIPTVImportItems(ctx, "j", "", "")
	require.Error(t, err)
	_, err = db.GetIPTVImportItem(ctx, "i")
	require.Error(t, err)
	_, err = db.GetIPTVImportItemsByIDs(ctx, "j", []string{"i"})
	require.Error(t, err)

	require.Error(t, db.InsertIPTVSource(ctx, IPTVSource{ID: "s", Name: "n", PlaylistURL: "u"}))
	_, err = db.ListIPTVSources(ctx)
	require.Error(t, err)
	_, err = db.GetIPTVSource(ctx, "src-1")
	require.Error(t, err)
	_, err = db.GetIPTVSourceByImportJob(ctx, "j")
	require.Error(t, err)
	require.Error(t, db.UpdateIPTVSource(ctx, IPTVSource{ID: "src-1"}))
	require.Error(t, db.UpdateIPTVSourceRefresh(ctx, "src-1", "ok", nil, "", 0, 0))
	require.Error(t, db.DeleteIPTVSource(ctx, "src-1"))

	require.Error(t, db.UpsertIPTVChannel(ctx, IPTVChannel{ID: "c", SourceID: "s", ExternalID: "e", StreamID: "st", Name: "n", SourceURL: "u"}))
	_, err = db.ListIPTVChannels(ctx, "", "", "", nil)
	require.Error(t, err)
	_, err = db.GetIPTVChannel(ctx, "c")
	require.Error(t, err)
	require.Error(t, db.UpdateIPTVChannelMeta(ctx, "c", "n", true, true, true))
	require.Error(t, db.DeleteIPTVChannel(ctx, "c"))
	_, err = db.ListIPTVGroups(ctx)
	require.Error(t, err)
}
