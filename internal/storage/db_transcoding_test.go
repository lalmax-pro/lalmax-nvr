package storage

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/transcode"
	"github.com/stretchr/testify/require"
)

func transcodeTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := New(filepath.Join(t.TempDir(), "transcode.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.Init(context.Background()))
	return db
}
func TestTranscodeIntentReferencesAndRevisions(t *testing.T) {
	db := transcodeTestDB(t)
	ctx := context.Background()
	p, err := db.SaveTranscodeProfile(ctx, transcode.DefaultProfile("ffmpeg"), 0)
	require.NoError(t, err)
	require.EqualValues(t, 1, p.Revision)
	_, err = db.SaveTranscodeProfile(ctx, p, 0)
	require.Error(t, err)
	j := transcode.Job{ID: "job1", Source: transcode.SourceRef{EngineID: "embedded", StreamID: "camera", AppName: "live"}, ProfileID: p.ID, ProfileRevision: p.Revision, RunPolicy: transcode.Always}
	j, err = db.SaveTranscodeJob(ctx, j, 0)
	require.NoError(t, err)
	require.EqualValues(t, 1, j.Generation)
	require.Error(t, db.DeleteTranscodeProfile(ctx, p.ID, p.Revision))
	_, err = db.SaveTranscodeProfile(ctx, p, p.Revision)
	require.Error(t, err)
	require.Error(t, db.DeleteTranscodeJob(ctx, j.ID, 0))
	require.NoError(t, db.Init(ctx))
	jobs, err := db.ListTranscodeJobs(ctx)
	require.NoError(t, err)
	require.Equal(t, []transcode.Job{j}, jobs)
	require.NoError(t, db.DeleteTranscodeJob(ctx, j.ID, j.Generation))
	require.NoError(t, db.DeleteTranscodeProfile(ctx, p.ID, p.Revision))
	profiles, err := db.ListTranscodeProfiles(ctx)
	require.NoError(t, err)
	require.Empty(t, profiles)
}
func TestTranscodeConcurrentProfileCAS(t *testing.T) {
	db := transcodeTestDB(t)
	ctx := context.Background()
	p, err := db.SaveTranscodeProfile(ctx, transcode.DefaultProfile("ffmpeg"), 0)
	require.NoError(t, err)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := db.SaveTranscodeProfile(ctx, p, p.Revision); results <- err }()
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	require.Equal(t, 1, success)
	profiles, err := db.ListTranscodeProfiles(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 2, profiles[0].Revision)
}

func TestTranscodeRecreatedIDsRejectStaleVersions(t *testing.T) {
	db := transcodeTestDB(t)
	ctx := context.Background()
	p, err := db.SaveTranscodeProfile(ctx, transcode.DefaultProfile("ffmpeg"), 0)
	require.NoError(t, err)
	j := transcode.Job{ID: "job1", Source: transcode.SourceRef{EngineID: "embedded", StreamID: "camera", AppName: "live"}, ProfileID: p.ID, ProfileRevision: p.Revision, RunPolicy: transcode.Manual}
	j, err = db.SaveTranscodeJob(ctx, j, 0)
	require.NoError(t, err)
	require.NoError(t, db.DeleteTranscodeJob(ctx, j.ID, j.Generation))
	newJob, err := db.SaveTranscodeJob(ctx, j, 0)
	require.NoError(t, err)
	require.Greater(t, newJob.Generation, j.Generation)
	require.Error(t, db.DeleteTranscodeJob(ctx, j.ID, j.Generation))
	_, err = db.SaveTranscodeJob(ctx, j, j.Generation)
	require.Error(t, err)
	require.Error(t, db.SetTranscodeJobStopped(ctx, j.ID, j.Generation, true))
	require.NoError(t, db.DeleteTranscodeJob(ctx, newJob.ID, newJob.Generation))
	require.NoError(t, db.DeleteTranscodeProfile(ctx, p.ID, p.Revision))
	newProfile, err := db.SaveTranscodeProfile(ctx, p, 0)
	require.NoError(t, err)
	require.Greater(t, newProfile.Revision, p.Revision)
	require.Error(t, db.DeleteTranscodeProfile(ctx, p.ID, p.Revision))
}
