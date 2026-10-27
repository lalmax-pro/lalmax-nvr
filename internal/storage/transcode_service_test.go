package storage

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/transcode"
	"github.com/stretchr/testify/require"
)

type failedRecoveryProbe struct{ calls atomic.Int32 }

func (p *failedRecoveryProbe) Run(context.Context, string, ...string) ([]byte, error) {
	p.calls.Add(1)
	return nil, errors.New("missing tool")
}
func TestTranscodeAlwaysRevalidatesAfterRestart(t *testing.T) {
	db := transcodeTestDB(t)
	ctx := context.Background()
	p, err := db.SaveTranscodeProfile(ctx, transcode.DefaultProfile("ffmpeg"), 0)
	require.NoError(t, err)
	_, err = db.SaveTranscodeJob(ctx, transcode.Job{ID: "recover", Source: transcode.SourceRef{EngineID: "embedded", StreamID: "camera", AppName: "live"}, ProfileID: p.ID, ProfileRevision: p.Revision, Enabled: true, RunPolicy: transcode.Always}, 0)
	require.NoError(t, err)
	runner := &failedRecoveryProbe{}
	manager := transcode.NewManager(ctx, nil, 1)
	defer manager.Close(ctx)
	service := transcode.NewService(db, manager, transcode.NewProbeService(runner), transcode.Config{Enabled: true})
	result := service.ReconcileAlways(ctx)
	require.Greater(t, runner.calls.Load(), int32(0), "restart must revalidate rather than silently stall on empty cache")
	require.Contains(t, result, "recover")
	calls := runner.calls.Load()
	service.ReconcileAlways(ctx)
	require.Equal(t, calls, runner.calls.Load(), "failed recovery must respect backoff")
}

func TestTranscodeVariantCatalogAndCrossSourceRejection(t *testing.T) {
	db := transcodeTestDB(t)
	ctx := context.Background()
	p, err := db.SaveTranscodeProfile(ctx, transcode.DefaultProfile("ffmpeg"), 0)
	require.NoError(t, err)
	j := transcode.Job{ID: "draft", Source: transcode.SourceRef{EngineID: "embedded", AppName: "live", StreamID: "camera"}, ProfileID: p.ID, ProfileRevision: p.Revision, RunPolicy: transcode.Manual}
	service := transcode.NewService(db, nil, nil, transcode.Config{})
	_, err = service.Save(ctx, j, 0)
	require.NoError(t, err)
	variants, err := service.Variants(ctx, "camera")
	require.NoError(t, err)
	require.Len(t, variants, 1)
	require.Equal(t, transcode.Disabled, variants[0].State)
	require.Equal(t, "nvr_tc_draft", variants[0].ID)
	others, err := service.Variants(ctx, "different")
	require.NoError(t, err)
	require.Empty(t, others)
	_, _, err = service.PlaybackTarget(ctx, "different", "draft")
	require.Error(t, err)
	_, _, err = service.PlaybackTarget(ctx, "camera", "draft")
	require.Error(t, err)
}

func TestTranscodeServiceDisabledDraftAndGuards(t *testing.T) {
	db := transcodeTestDB(t)
	ctx := context.Background()
	p, err := db.SaveTranscodeProfile(ctx, transcode.DefaultProfile("ffmpeg"), 0)
	require.NoError(t, err)
	manager := transcode.NewManager(ctx, nil, 2)
	t.Cleanup(func() { require.NoError(t, manager.Close(ctx)) })
	service := transcode.NewService(db, manager, transcode.NewProbeService(nil), transcode.Config{})
	j := transcode.Job{ID: "draft", Source: transcode.SourceRef{EngineID: "embedded", AppName: "live", StreamID: "camera"}, ProfileID: p.ID, ProfileRevision: p.Revision, RunPolicy: transcode.Manual}
	saved, err := service.Save(ctx, j, 0)
	require.NoError(t, err)
	_, err = service.Start(ctx, saved.ID, saved.Generation)
	require.Error(t, err)
	saved.Enabled = true
	_, err = service.Save(ctx, saved, saved.Generation)
	require.Error(t, err)
	require.Error(t, service.Delete(ctx, saved.ID, saved.Generation+1))
	require.NoError(t, service.Delete(ctx, saved.ID, saved.Generation))
	jobs, err := db.ListTranscodeJobs(ctx)
	require.NoError(t, err)
	require.Empty(t, jobs)
}
func TestTranscodeReconcileBackoffAndManualExclusion(t *testing.T) {
	db := transcodeTestDB(t)
	ctx := context.Background()
	p, err := db.SaveTranscodeProfile(ctx, transcode.DefaultProfile("ffmpeg"), 0)
	require.NoError(t, err)
	for _, policy := range []transcode.RunPolicy{transcode.Always, transcode.Manual} {
		_, err = db.SaveTranscodeJob(ctx, transcode.Job{ID: string(policy), Source: transcode.SourceRef{EngineID: "embedded", StreamID: "camera", AppName: "live"}, ProfileID: p.ID, ProfileRevision: p.Revision, RunPolicy: policy, Enabled: true}, 0)
		require.NoError(t, err)
	}
	service := transcode.NewService(db, nil, nil, transcode.Config{})
	first := service.ReconcileAlways(ctx)
	require.Contains(t, first, "always")
	require.NotContains(t, first, "manual")
	require.Empty(t, service.ReconcileAlways(ctx), "immediate retry must be suppressed")
}

func TestTranscodeServiceRequiresProfileProbe(t *testing.T) {
	db := transcodeTestDB(t)
	ctx := context.Background()
	p, err := db.SaveTranscodeProfile(ctx, transcode.DefaultProfile("ffmpeg"), 0)
	require.NoError(t, err)
	manager := transcode.NewManager(ctx, nil, 2)
	defer manager.Close(ctx)
	service := transcode.NewService(db, manager, transcode.NewProbeService(nil), transcode.Config{Enabled: true})
	j := transcode.Job{ID: "unchecked", Source: transcode.SourceRef{EngineID: "embedded", AppName: "live", StreamID: "camera"}, ProfileID: p.ID, ProfileRevision: p.Revision, RunPolicy: transcode.Always, Enabled: true}
	_, err = service.Save(ctx, j, 0)
	var domain *transcode.Error
	require.ErrorAs(t, err, &domain)
	require.Equal(t, "probe_required", domain.Code)
}
