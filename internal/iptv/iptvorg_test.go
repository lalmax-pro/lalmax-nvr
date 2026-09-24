package iptv

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/storage"
	"github.com/stretchr/testify/require"
)

const iptvOrgScienceURL = "https://iptv-org.github.io/iptv/categories/science.m3u"

func testDB(t *testing.T) *storage.DB {
	t.Helper()
	db, err := storage.New(filepath.Join(t.TempDir(), "iptv.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.Init(context.Background()))
	return db
}

func TestIPTVOrgSciencePlaylist(t *testing.T) {
	if testing.Short() {
		t.Skip("skip live iptv-org test in short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	svc := NewService(testDB(t), nil)
	job, err := svc.ImportFromURL(ctx, ImportRequest{
		Name:        "iptv-org science",
		PlaylistURL: iptvOrgScienceURL,
	})
	require.NoError(t, err)
	require.NotEmpty(t, job.ID)
	require.GreaterOrEqual(t, job.TotalItems, 10, "science.m3u should contain multiple channels")
	t.Logf("imported %d channels from %s", job.TotalItems, iptvOrgScienceURL)

	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		got, err := svc.GetImport(ctx, job.ID)
		require.NoError(t, err)
		require.NotNil(t, got)
		if got.Status == JobReady || got.Status == JobFailed {
			job = got
			break
		}
		time.Sleep(400 * time.Millisecond)
	}
	require.Equal(t, JobReady, job.Status, "probe job error=%s", job.Error)

	items, err := svc.ListImportItems(ctx, job.ID, "", "")
	require.NoError(t, err)
	require.Len(t, items, job.TotalItems)

	counts := map[string]int{}
	var enroll []string
	for _, item := range items {
		counts[item.Status]++
		t.Logf("%-28s  status=%-12s codec=%s/%s playable=%v err=%s", item.Name, item.Status, item.VideoCodec, item.AudioCodec, item.Playable, item.Error)
		if item.Status == ItemPlayable || item.Status == ItemWarning {
			enroll = append(enroll, item.ID)
		}
	}
	t.Logf("probe summary: %+v", counts)

	if len(enroll) == 0 {
		t.Log("no playable/warning channels from live probe; import+parse+probe path still succeeded")
		return
	}
	if len(enroll) > 5 {
		enroll = enroll[:5]
	}
	src, n, err := svc.Commit(ctx, job.ID, enroll)
	require.NoError(t, err)
	require.Equal(t, len(enroll), n)
	require.NotNil(t, src)
	channels, err := svc.ListChannels(ctx, src.ID, "", "", nil)
	require.NoError(t, err)
	require.Len(t, channels, n)
	t.Logf("enrolled %d IPTV channels into source %s", n, src.Name)
}

func TestParseDownloadedScienceM3U(t *testing.T) {
	if testing.Short() {
		t.Skip("skip live iptv-org test in short mode")
	}
	path := "/tmp/iptv-org-test/science.m3u"
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skip("science.m3u not downloaded")
	}
	got, err := ParseM3U(bytes.NewReader(data))
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(got), 10)
	var withUA int
	for _, ch := range got {
		require.NotEmpty(t, ch.Name)
		require.NotEmpty(t, ch.SourceURL)
		if ch.Headers["User-Agent"] != "" {
			withUA++
		}
	}
	t.Logf("parsed %d channels, %d with custom User-Agent", len(got), withUA)
	require.Greater(t, withUA, 0)
}
