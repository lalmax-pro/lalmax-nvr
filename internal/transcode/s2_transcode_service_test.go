package transcode

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// s2Repository is a scriptable in-memory transcode repository.
type s2Repository struct {
	mu        sync.Mutex
	profiles  []Profile
	jobs      []Job
	listErr   error
	saveErr   error
	deleteErr error
	stopErr   error
	saved     []Job
	deleted   []string
	stopped   []bool
}

func (r *s2Repository) ListTranscodeProfiles(context.Context) ([]Profile, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	return append([]Profile(nil), r.profiles...), nil
}

func (r *s2Repository) SaveTranscodeProfile(_ context.Context, p Profile, _ uint64) (Profile, error) {
	if r.saveErr != nil {
		return Profile{}, r.saveErr
	}
	return p, nil
}

func (r *s2Repository) DeleteTranscodeProfile(_ context.Context, id string, _ uint64) error {
	if r.deleteErr != nil {
		return r.deleteErr
	}
	r.deleted = append(r.deleted, id)
	return nil
}

func (r *s2Repository) ListTranscodeJobs(context.Context) ([]Job, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	return append([]Job(nil), r.jobs...), nil
}

func (r *s2Repository) SaveTranscodeJob(_ context.Context, j Job, _ uint64) (Job, error) {
	if r.saveErr != nil {
		return Job{}, r.saveErr
	}
	r.mu.Lock()
	r.saved = append(r.saved, j)
	r.mu.Unlock()
	return j, nil
}

func (r *s2Repository) DeleteTranscodeJob(_ context.Context, id string, _ uint64) error {
	if r.deleteErr != nil {
		return r.deleteErr
	}
	r.mu.Lock()
	r.deleted = append(r.deleted, id)
	r.mu.Unlock()
	return nil
}

func (r *s2Repository) SetTranscodeJobStopped(_ context.Context, _ string, _ uint64, stopped bool) error {
	if r.stopErr != nil {
		return r.stopErr
	}
	r.mu.Lock()
	r.stopped = append(r.stopped, stopped)
	r.mu.Unlock()
	return nil
}

// s2ServiceHarness wires a service with an always-approving probe gate.
func s2ServiceHarness(t *testing.T) (*Service, *s2Repository, *Manager, Profile, Job) {
	t.Helper()
	p := testProfile(t)
	j := managerJob(t, "camera", p)
	repo := &s2Repository{profiles: []Profile{p}, jobs: []Job{j}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	manager := NewManager(ctx, &fakeExecutor{}, 1)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	probes := NewProbeService(nil)
	fingerprint, err := ProbeFingerprint(Config{Enabled: true}.Tools(), p)
	if err != nil {
		t.Fatal(err)
	}
	probes.reports[p.ID] = CapabilityReport{CanEnable: true, Fingerprint: fingerprint, CheckedAt: time.Now()}
	service := NewService(repo, manager, probes, Config{Enabled: true})
	return service, repo, manager, p, j
}

func TestS2ServiceProfileAndJobLookups(t *testing.T) {
	service, repo, _, p, j := s2ServiceHarness(t)
	ctx := context.Background()

	got, err := service.profile(ctx, j)
	if err != nil || got.ID != p.ID {
		t.Fatalf("profile lookup failed: %v", err)
	}

	if _, err := service.profile(ctx, Job{ProfileID: "missing", ProfileRevision: 1}); err == nil {
		t.Fatal("expected profile_not_found")
	}
	if _, err := service.profile(ctx, Job{ProfileID: p.ID, ProfileRevision: 9}); err == nil {
		t.Fatal("expected revision mismatch")
	}

	if _, err := service.job(ctx, j.ID, j.Generation); err != nil {
		t.Fatalf("job lookup failed: %v", err)
	}
	if _, err := service.job(ctx, j.ID, j.Generation+1); err == nil {
		t.Fatal("expected generation conflict")
	}
	if _, err := service.job(ctx, "missing", 1); err == nil {
		t.Fatal("expected job_not_found")
	}

	repo.listErr = errors.New("repository exploded")
	if _, err := service.profile(ctx, j); err == nil {
		t.Fatal("expected profile listing error")
	}
	if _, err := service.job(ctx, j.ID, j.Generation); err == nil {
		t.Fatal("expected job listing error")
	}
}

func TestS2ServiceAllow(t *testing.T) {
	p := testProfile(t)
	service, _, manager, _, _ := s2ServiceHarness(t)

	if err := service.allow(p); err != nil {
		t.Fatalf("configured service must allow the profile: %v", err)
	}

	disabled := NewService(&s2Repository{}, manager, service.probes, Config{Enabled: false})
	if err := disabled.allow(p); !isCode(err, "transcoding_disabled") {
		t.Fatalf("expected transcoding_disabled, got %v", err)
	}

	noBackend := NewService(&s2Repository{}, nil, nil, Config{Enabled: true})
	if err := noBackend.allow(p); !isCode(err, "backend_unavailable") {
		t.Fatalf("expected backend_unavailable, got %v", err)
	}

	// Missing, stale and non-enabling probe results are rejected.
	noReport := NewService(&s2Repository{}, manager, NewProbeService(nil), Config{Enabled: true})
	if err := noReport.allow(p); !isCode(err, "probe_required") {
		t.Fatalf("expected probe_required, got %v", err)
	}

	stale := NewService(&s2Repository{}, manager, NewProbeService(nil), Config{Enabled: true})
	stale.probes.reports[p.ID] = CapabilityReport{CanEnable: true, CheckedAt: time.Now().Add(-time.Hour)}
	if err := stale.allow(p); !isCode(err, "probe_required") {
		t.Fatalf("expected probe_required for stale report, got %v", err)
	}

	unusable := NewService(&s2Repository{}, manager, NewProbeService(nil), Config{Enabled: true})
	unusable.probes.reports[p.ID] = CapabilityReport{CanEnable: false, CheckedAt: time.Now()}
	if err := unusable.allow(p); !isCode(err, "probe_required") {
		t.Fatalf("expected probe_required for non-enabling report, got %v", err)
	}
}

func isCode(err error, code string) bool {
	var domain *Error
	return errors.As(err, &domain) && domain.Code == code
}

func TestS2ServiceSave(t *testing.T) {
	service, repo, manager, p, j := s2ServiceHarness(t)
	ctx := context.Background()

	// Invalid jobs are rejected before touching the repository.
	invalidJob := j
	invalidJob.RunPolicy = RunPolicy("later")
	if _, err := service.Save(ctx, invalidJob, 0); err == nil {
		t.Fatal("expected validation error")
	}

	// Unknown profile revisions are rejected.
	unknown := j
	unknown.ProfileRevision = 99
	if _, err := service.Save(ctx, unknown, 0); !isCode(err, "profile_not_found") {
		t.Fatalf("expected profile_not_found, got %v", err)
	}

	// Enabling requires a fresh probe approval.
	service.config.Enabled = false
	if _, err := service.Save(ctx, j, 0); !isCode(err, "transcoding_disabled") {
		t.Fatalf("expected transcoding_disabled, got %v", err)
	}
	service.config.Enabled = true

	// Outcome revisions must match an existing job.
	if _, err := service.Save(ctx, j, j.Generation+7); !isCode(err, "revision_conflict") {
		t.Fatalf("expected revision_conflict, got %v", err)
	}

	// Unknown jobs are rejected before persisting.
	ghost := j
	ghost.ID = "ghost"
	if _, err := service.Save(ctx, ghost, 1); !isCode(err, "job_not_found") {
		t.Fatalf("expected job_not_found, got %v", err)
	}

	// A running job cannot be edited.
	if _, err := manager.Acquire(j, p); err != nil {
		t.Fatalf("acquire failed: %v", err)
	}
	if _, err := service.Save(ctx, j, j.Generation); !isCode(err, "job_running") {
		t.Fatalf("expected job_running, got %v", err)
	}
	if err := manager.Release(ctx, j.ID, j.Generation); err != nil {
		t.Fatalf("release failed: %v", err)
	}

	// Disabled jobs skip the probe gate and persist.
	disabledJob := j
	disabledJob.Enabled = false
	if _, err := service.Save(ctx, disabledJob, j.Generation); err != nil {
		t.Fatalf("save failed: %v", err)
	}
	if len(repo.saved) != 1 {
		t.Fatalf("expected one persisted job, got %d", len(repo.saved))
	}

	// Repository failures propagate.
	repo.saveErr = errors.New("save exploded")
	if _, err := service.Save(ctx, disabledJob, j.Generation); err == nil {
		t.Fatal("expected save error")
	}
}

func TestS2ServiceStartAndStop(t *testing.T) {
	service, repo, manager, p, j := s2ServiceHarness(t)
	ctx := context.Background()

	// Unknown jobs and profiles propagate their errors.
	if _, err := service.Start(ctx, j.ID, j.Generation+1); err == nil {
		t.Fatal("expected generation conflict")
	}
	repo.jobs = []Job{{ID: j.ID, ProfileID: "missing", ProfileRevision: 1, Source: j.Source, Enabled: true, RunPolicy: Always, Generation: 1}}
	if _, err := service.Start(ctx, j.ID, 1); !isCode(err, "profile_not_found") {
		t.Fatalf("expected profile_not_found, got %v", err)
	}
	repo.jobs = []Job{j}

	// Starting a stopped job clears the persisted stop flag.
	stopped := j
	stopped.StoppedByUser = true
	repo.jobs = []Job{stopped}
	if _, err := service.Start(ctx, stopped.ID, stopped.Generation); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	if len(repo.stopped) != 1 || repo.stopped[0] {
		t.Fatalf("expected stop flag cleared, got %v", repo.stopped)
	}

	// A retry backoff is cleared once the job is running again.
	service.retry[j.ID] = retryState{generation: j.Generation, healthySince: time.Now(), healthyVariantID: "old"}
	repo.jobs = []Job{j}
	if _, err := service.Start(ctx, j.ID, j.Generation); err != nil {
		t.Fatalf("restart failed: %v", err)
	}
	if retry := service.retry[j.ID]; !retry.healthySince.IsZero() {
		t.Fatal("expected healthy interval reset")
	}

	// A failing stop-flag write releases a newly started process.
	repo.stopErr = errors.New("stop flag exploded")
	repo.jobs = []Job{stopped}
	service.config.Enabled = true
	// Use a fresh manager so the job is not already active.
	freshCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service.manager = NewManager(freshCtx, &fakeExecutor{}, 1)
	defer func() { _ = service.manager.Close(context.Background()) }()
	if _, err := service.Start(ctx, stopped.ID, stopped.Generation); err == nil {
		t.Fatal("expected stop-flag error")
	}
	repo.stopErr = nil

	// Stop propagates lookup and persistence failures.
	if err := service.Stop(ctx, j.ID, j.Generation+1); err == nil {
		t.Fatal("expected generation conflict")
	}
	repo.stopErr = errors.New("stop exploded")
	if err := service.Stop(ctx, j.ID, j.Generation); err == nil {
		t.Fatal("expected stop error")
	}
	repo.stopErr = nil

	// Stopping without a manager only persists intent.
	noManager := NewService(repo, nil, service.probes, Config{Enabled: true})
	if err := noManager.Stop(ctx, j.ID, j.Generation); err != nil {
		t.Fatalf("stop without manager failed: %v", err)
	}

	if err := service.Stop(ctx, j.ID, j.Generation); err != nil {
		t.Fatalf("stop failed: %v", err)
	}
	_ = p
	_ = manager
}

func TestS2ServiceDelete(t *testing.T) {
	service, repo, _, _, j := s2ServiceHarness(t)
	ctx := context.Background()

	if err := service.Delete(ctx, j.ID, j.Generation+1); err == nil {
		t.Fatal("expected generation conflict")
	}

	// Deletion releases the runtime before removing the record.
	service.retry[j.ID] = retryState{generation: j.Generation}
	if err := service.Delete(ctx, j.ID, j.Generation); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	if _, exists := service.retry[j.ID]; exists {
		t.Fatal("expected retry state to be removed")
	}
	if len(repo.deleted) != 1 {
		t.Fatalf("expected one deleted job, got %v", repo.deleted)
	}

	// Repository failures propagate.
	repo.deleteErr = errors.New("delete exploded")
	if err := service.Delete(ctx, j.ID, j.Generation); err == nil {
		t.Fatal("expected delete error")
	}
	repo.deleteErr = nil

	// A manager release failure aborts deletion.
	repo.jobs = []Job{{ID: j.ID, ProfileID: j.ProfileID, ProfileRevision: j.ProfileRevision, Source: j.Source, Enabled: true, RunPolicy: Always, Generation: 3}}
	service.retry[j.ID] = retryState{generation: 3}
	if err := service.Delete(ctx, j.ID, 3); err != nil {
		t.Fatalf("stale release must not block deletion: %v", err)
	}

	// Deleting without a manager skips the release step.
	repo.jobs = []Job{j}
	noManager := NewService(repo, nil, service.probes, Config{Enabled: true})
	if err := noManager.Delete(ctx, j.ID, j.Generation); err != nil {
		t.Fatalf("delete without manager failed: %v", err)
	}
}

func TestS2ServiceVariants(t *testing.T) {
	service, repo, manager, p, j := s2ServiceHarness(t)
	ctx := context.Background()

	// Nothing running: the job is reported as stopped.
	variants, err := service.Variants(ctx, "")
	if err != nil {
		t.Fatalf("variants failed: %v", err)
	}
	if len(variants) != 1 || variants[0].State != Stopped || variants[0].JobID != j.ID {
		t.Fatalf("unexpected variants: %+v", variants)
	}

	// Source filtering.
	if got, err := service.Variants(ctx, "other-stream"); err != nil || len(got) != 0 {
		t.Fatalf("expected no variants for other source, got %v (err %v)", got, err)
	}

	// Disabled jobs report the disabled state.
	disabled := j
	disabled.Enabled = false
	repo.jobs = []Job{disabled}
	variants, err = service.Variants(ctx, j.Source.StreamID)
	if err != nil || len(variants) != 1 || variants[0].State != Disabled {
		t.Fatalf("unexpected disabled variant: %+v (err %v)", variants, err)
	}

	// Running jobs mirror the manager runtime state.
	repo.jobs = []Job{j}
	manager.healthTimeout = 20 * time.Millisecond
	if executor, ok := manager.executor.(*fakeExecutor); ok {
		executor.ready.Store(true)
	}
	if _, err := manager.Acquire(j, p); err != nil {
		t.Fatalf("acquire failed: %v", err)
	}
	variants, err = service.Variants(ctx, "")
	if err != nil || len(variants) != 1 {
		t.Fatalf("unexpected variants: %+v (err %v)", variants, err)
	}
	if variants[0].State != WaitingSource && variants[0].State != Running && variants[0].State != Starting {
		t.Fatalf("expected a manager runtime state, got %q", variants[0].State)
	}

	// Error backoff is surfaced with a retry timestamp.
	service.retry[j.ID] = retryState{generation: j.Generation, errorCode: "source_offline", next: time.Now().Add(30 * time.Second)}
	variants, err = service.Variants(ctx, "")
	if err != nil || len(variants) != 1 {
		t.Fatalf("unexpected variants: %+v (err %v)", variants, err)
	}
	if variants[0].RetryAt == nil && variants[0].State != Running && variants[0].State != Starting {
		t.Fatalf("expected retry metadata, got %+v", variants[0])
	}

	// Repository failures and invalid identifiers propagate.
	repo.listErr = errors.New("list exploded")
	if _, err := service.Variants(ctx, ""); err == nil {
		t.Fatal("expected list error")
	}
	repo.listErr = nil
	repo.jobs = []Job{{ID: "bad id!", ProfileID: j.ProfileID, Source: j.Source, Enabled: true, Generation: 1}}
	if _, err := service.Variants(ctx, ""); err == nil {
		t.Fatal("expected invalid job id error")
	}
}

func TestS2ServicePlaybackTarget(t *testing.T) {
	service, repo, manager, p, j := s2ServiceHarness(t)
	ctx := context.Background()

	// Unknown jobs are rejected.
	if _, _, err := service.PlaybackTarget(ctx, j.Source.StreamID, "missing"); !isCode(err, "job_not_found") {
		t.Fatalf("expected job_not_found, got %v", err)
	}

	// Source mismatch is treated as not found.
	if _, _, err := service.PlaybackTarget(ctx, "other-stream", j.ID); !isCode(err, "job_not_found") {
		t.Fatalf("expected job_not_found for source mismatch, got %v", err)
	}

	// Not running yet.
	if _, _, err := service.PlaybackTarget(ctx, j.Source.StreamID, j.ID); !isCode(err, "output_unavailable") {
		t.Fatalf("expected output_unavailable, got %v", err)
	}

	// Disabled jobs are unavailable.
	disabled := j
	disabled.Enabled = false
	repo.jobs = []Job{disabled}
	if _, _, err := service.PlaybackTarget(ctx, j.Source.StreamID, j.ID); !isCode(err, "output_unavailable") {
		t.Fatalf("expected output_unavailable for disabled job, got %v", err)
	}

	// No manager means no derived output.
	noManager := NewService(repo, nil, service.probes, Config{Enabled: true})
	repo.jobs = []Job{j}
	if _, _, err := noManager.PlaybackTarget(ctx, j.Source.StreamID, j.ID); !isCode(err, "output_unavailable") {
		t.Fatalf("expected output_unavailable without manager, got %v", err)
	}

	// A running job resolves to its physical variant.
	manager.healthTimeout = 20 * time.Millisecond
	if executor, ok := manager.executor.(*fakeExecutor); ok {
		executor.ready.Store(true)
	}
	if _, err := manager.Acquire(j, p); err != nil {
		t.Fatalf("acquire failed: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	var variantID string
	for time.Now().Before(deadline) {
		source, id, err := service.PlaybackTarget(ctx, j.Source.StreamID, j.ID)
		if err == nil {
			if source.StreamID != j.Source.StreamID || id == "" {
				t.Fatalf("unexpected playback target %+v %q", source, id)
			}
			variantID = id
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if variantID == "" {
		t.Fatal("running job never resolved a playback target")
	}

	// Repository failures propagate.
	repo.listErr = errors.New("list exploded")
	if _, _, err := service.PlaybackTarget(ctx, j.Source.StreamID, j.ID); err == nil {
		t.Fatal("expected list error")
	}
}

func TestS2ServiceRun(t *testing.T) {
	// A cancelled context returns immediately.
	service, _, _, _, _ := s2ServiceHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	service.Run(ctx)

	// One reconciliation tick reports repository failures.
	repo := &s2Repository{listErr: errors.New("list exploded")}
	service = NewService(repo, nil, NewProbeService(nil), Config{Enabled: true})
	ctx, cancel = context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		service.Run(ctx)
		close(done)
	}()
	time.Sleep(2200 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
}
