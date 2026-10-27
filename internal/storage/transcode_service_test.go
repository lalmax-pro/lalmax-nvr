package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lalmax-pro/lalmax-nvr/internal/transcode"
	"github.com/stretchr/testify/require"
)

type failedRecoveryProbe struct{ calls atomic.Int32 }

type resumeTestProcess struct {
	done chan struct{}
	once sync.Once
}

func (p *resumeTestProcess) Wait() error { <-p.done; return nil }
func (p *resumeTestProcess) Stop(context.Context) error {
	p.once.Do(func() { close(p.done) })
	return nil
}

type resumeTestExecutor struct{}

func (resumeTestExecutor) Start(context.Context, transcode.SourceRef, transcode.Profile, string) (transcode.Process, error) {
	return &resumeTestProcess{done: make(chan struct{})}, nil
}
func (resumeTestExecutor) Ready(context.Context, string) bool { return true }

func TestTranscodeExplicitStartClearsPersistedStop(t *testing.T) {
	if os.Getenv("NVR_TRANSCODE_MEDIA_TEST") != "1" {
		t.Skip("requires installed FFmpeg")
	}
	db := transcodeTestDB(t)
	ctx := context.Background()
	p, err := db.SaveTranscodeProfile(ctx, transcode.DefaultProfile("ffmpeg"), 0)
	require.NoError(t, err)
	j, err := db.SaveTranscodeJob(ctx, transcode.Job{ID: "resume", Source: transcode.SourceRef{EngineID: "embedded", StreamID: "camera", AppName: "live"}, ProfileID: p.ID, ProfileRevision: p.Revision, Enabled: true, RunPolicy: transcode.Always}, 0)
	require.NoError(t, err)
	manager := transcode.NewManager(ctx, resumeTestExecutor{}, 1)
	defer manager.Close(ctx)
	probes := transcode.NewProbeService(nil)
	cfg := transcode.Config{Enabled: true}
	report, err := probes.Probe(ctx, cfg.Tools(), p)
	require.NoError(t, err)
	require.True(t, report.CanEnable)
	service := transcode.NewService(db, manager, probes, cfg)
	require.NoError(t, service.Stop(ctx, j.ID, j.Generation))
	_, err = service.Start(ctx, j.ID, j.Generation)
	require.NoError(t, err)
	jobs, err := db.ListTranscodeJobs(ctx)
	require.NoError(t, err)
	require.False(t, jobs[0].StoppedByUser)
}

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

func TestTranscodeExplicitStopSurvivesServiceRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restart.db")
	db, err := New(path)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, db.Init(ctx))
	p, err := db.SaveTranscodeProfile(ctx, transcode.DefaultProfile("ffmpeg"), 0)
	require.NoError(t, err)
	j, err := db.SaveTranscodeJob(ctx, transcode.Job{ID: "stopped", Source: transcode.SourceRef{EngineID: "embedded", StreamID: "camera", AppName: "live"}, ProfileID: p.ID, ProfileRevision: p.Revision, Enabled: true, RunPolicy: transcode.Always}, 0)
	require.NoError(t, err)
	service := transcode.NewService(db, nil, nil, transcode.Config{})
	require.NoError(t, service.Stop(ctx, j.ID, j.Generation))
	require.NoError(t, db.Close())
	db, err = New(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.Init(ctx))
	runner := &failedRecoveryProbe{}
	manager := transcode.NewManager(ctx, nil, 1)
	defer manager.Close(ctx)
	restarted := transcode.NewService(db, manager, transcode.NewProbeService(runner), transcode.Config{Enabled: true})
	require.Empty(t, restarted.ReconcileAlways(ctx))
	require.Zero(t, runner.calls.Load())
	jobs, err := db.ListTranscodeJobs(ctx)
	require.NoError(t, err)
	require.True(t, jobs[0].StoppedByUser)
	jobs[0].StoppedByUser = false // edits cannot silently clear explicit stop
	updated, err := db.SaveTranscodeJob(ctx, jobs[0], jobs[0].Generation)
	require.NoError(t, err)
	require.True(t, updated.StoppedByUser)
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
	variants, err := service.Variants(ctx, "camera")
	require.NoError(t, err)
	for _, variant := range variants {
		if variant.JobID == "always" {
			require.Equal(t, transcode.Backoff, variant.State)
			require.Equal(t, "transcoding_disabled", variant.ErrorCode)
			require.NotNil(t, variant.RetryAt)
		}
	}
	require.NoError(t, service.Stop(ctx, "always", 1))
	variants, err = service.Variants(ctx, "camera")
	require.NoError(t, err)
	for _, variant := range variants {
		if variant.JobID == "always" {
			require.Equal(t, transcode.Stopped, variant.State)
			require.Nil(t, variant.RetryAt)
		}
	}
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
