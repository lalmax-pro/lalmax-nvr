package transcode

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// Service serializes user mutations with reconciliation. Manager remains the
// process owner; repository is the sole source of persisted intent.
type Service struct {
	mu      sync.Mutex
	repo    Repository
	manager *Manager
	probes  *ProbeService
	config  Config
	retry   map[string]retryState
}

func NewService(repo Repository, manager *Manager, probes *ProbeService, cfg Config) *Service {
	cfg.ApplyDefaults()
	return &Service{repo: repo, manager: manager, probes: probes, config: cfg, retry: map[string]retryState{}}
}
func (s *Service) profile(ctx context.Context, j Job) (Profile, error) {
	profiles, err := s.repo.ListTranscodeProfiles(ctx)
	if err != nil {
		return Profile{}, err
	}
	for _, p := range profiles {
		if p.ID == j.ProfileID && p.Revision == j.ProfileRevision {
			return p, nil
		}
	}
	return Profile{}, &Error{Code: "profile_not_found", Message: "selected profile revision does not exist"}
}
func (s *Service) job(ctx context.Context, id string, generation uint64) (Job, error) {
	jobs, err := s.repo.ListTranscodeJobs(ctx)
	if err != nil {
		return Job{}, err
	}
	for _, j := range jobs {
		if j.ID == id {
			if j.Generation != generation {
				return Job{}, conflictError()
			}
			return j, nil
		}
	}
	return Job{}, &Error{Code: "job_not_found", Message: "job does not exist"}
}
func (s *Service) allow(p Profile) error {
	if !s.config.Enabled {
		return &Error{Code: "transcoding_disabled", Message: "transcoding is disabled in server configuration"}
	}
	if s.manager == nil || s.probes == nil {
		return &Error{Code: "backend_unavailable", Message: "transcoding runtime is unavailable"}
	}
	report, ok := s.probes.CurrentResult(s.config.Tools(), p)
	if !ok || report.Stale || !report.CanEnable {
		return &Error{Code: "probe_required", Message: "run a successful capability check for this profile before enabling"}
	}
	return nil
}
func (s *Service) Save(ctx context.Context, j Job, expected uint64) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := j.Validate(); err != nil {
		return j, err
	}
	p, err := s.profile(ctx, j)
	if err != nil {
		return j, err
	}
	if j.Enabled {
		if err = s.allow(p); err != nil {
			return j, err
		}
	}
	if expected > 0 {
		if _, err = s.job(ctx, j.ID, expected); err != nil {
			return j, err
		}
		if s.manager != nil {
			if _, active := s.manager.Status(j.ID); active {
				return j, &Error{Code: "job_running", Message: "stop this job before editing"}
			}
		}
	}
	return s.repo.SaveTranscodeJob(ctx, j, expected)
}
func (s *Service) Start(ctx context.Context, id string, generation uint64) (Runtime, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, err := s.job(ctx, id, generation)
	if err != nil {
		return Runtime{}, err
	}
	p, err := s.profile(ctx, j)
	if err != nil {
		return Runtime{}, err
	}
	if err = s.allow(p); err != nil {
		return Runtime{}, err
	}
	_, alreadyActive := s.manager.Status(id)
	runtime, err := s.manager.Acquire(j, p)
	if err == nil && j.StoppedByUser {
		if err = s.repo.SetTranscodeJobStopped(ctx, id, generation, false); err != nil {
			if !alreadyActive {
				cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_ = s.manager.Release(cleanup, id, generation)
				cancel()
			}
			return Runtime{}, err
		}
	}
	return runtime, err
}
func (s *Service) Stop(ctx context.Context, id string, generation uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.job(ctx, id, generation); err != nil {
		return err
	}
	if err := s.repo.SetTranscodeJobStopped(ctx, id, generation, true); err != nil {
		return err
	}
	if s.manager == nil {
		return nil
	}
	return s.manager.Release(ctx, id, generation)
}
func (s *Service) Delete(ctx context.Context, id string, generation uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.job(ctx, id, generation); err != nil {
		return err
	}
	if s.manager != nil {
		if err := s.manager.Release(ctx, id, generation); err != nil {
			return err
		}
	}
	if err := s.repo.DeleteTranscodeJob(ctx, id, generation); err != nil {
		return err
	}
	delete(s.retry, id)
	return nil
}

type retryState struct {
	generation uint64
	attempts   int
	next       time.Time
	errorCode  string
}

// Run periodically reconciles persisted enabled always jobs. Previously enabled
// jobs revalidate expired capabilities before recovery; new enablement still
// requires an administrator's successful interactive probe.
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			attempt, cancel := context.WithTimeout(ctx, 45*time.Second)
			for id, err := range s.ReconcileAlways(attempt) {
				code := "reconcile_failed"
				var domain *Error
				if errors.As(err, &domain) {
					code = domain.Code
				}
				slog.Warn("transcoding reconciliation deferred", "job_id", id, "code", code)
			}
			cancel()
		}
	}
}

// ReconcileAlways is called by the application coordinator after startup and
// configuration changes. It never implicitly starts manual jobs. Per-job errors
// are returned so one unhealthy source cannot block all other jobs.
func (s *Service) ReconcileAlways(ctx context.Context) map[string]error {
	s.mu.Lock()
	defer s.mu.Unlock()
	results := map[string]error{}
	jobs, err := s.repo.ListTranscodeJobs(ctx)
	if err != nil {
		results["repository"] = err
		return results
	}
	for _, j := range jobs {
		if !j.Enabled || j.RunPolicy != Always || j.StoppedByUser {
			continue
		}
		if s.manager != nil {
			if runtime, ok := s.manager.Status(j.ID); ok && (runtime.State == Running || runtime.State == Starting) {
				continue
			}
		}
		retry := s.retry[j.ID]
		if retry.generation != j.Generation {
			retry = retryState{generation: j.Generation}
		}
		if time.Now().Before(retry.next) {
			continue
		}
		p, err := s.profile(ctx, j)
		if err == nil {
			err = s.allow(p)
			if err != nil {
				var gate *Error
				if errors.As(err, &gate) && gate.Code == "probe_required" {
					// Always jobs may recover after restart or probe expiry. The
					// probe is bounded by the reconciliation context and never
					// accepts arbitrary user commands or URLs.
					_, probeErr := s.probes.Probe(ctx, s.config.Tools(), p)
					if probeErr == nil {
						err = s.allow(p)
					} else {
						err = probeErr
					}
				}
			}
		}
		if err == nil {
			_, err = s.manager.Acquire(j, p)
		}
		retry.attempts++
		delay := 5 * time.Second
		for n := 1; n < retry.attempts && delay < time.Minute; n++ {
			delay *= 2
		}
		if delay > time.Minute {
			delay = time.Minute
		}
		retry.next = time.Now().Add(delay)
		retry.errorCode = ""
		if err != nil {
			retry.errorCode = "reconcile_failed"
			var domain *Error
			if errors.As(err, &domain) {
				retry.errorCode = domain.Code
			}
		}
		s.retry[j.ID] = retry
		if err != nil {
			results[j.ID] = err
		}
	}
	return results
}
