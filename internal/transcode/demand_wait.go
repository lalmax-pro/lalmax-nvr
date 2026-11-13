package transcode

import (
    "context"
    "time"
)

// WaitDemandReady waits for an already acquired demand lease to become usable.
// The caller remains responsible for releasing the lease. The wait never
// creates another lease, starts another encoder, or grants playback access.
func (s *Service) WaitDemandReady(ctx context.Context, token string) (Runtime, error) {
    ticker := time.NewTicker(100 * time.Millisecond)
    defer ticker.Stop()
    for {
        s.mu.Lock()
        lease, found := s.leases[token]
        if !found || !time.Now().Before(lease.ExpiresAt) {
            s.mu.Unlock()
            return Runtime{}, &Error{Code: "lease_not_found", Message: "consumer lease is missing or expired"}
        }
        if s.manager == nil || !s.config.Enabled {
            s.mu.Unlock()
            return Runtime{}, &Error{Code: "demand_unavailable", Message: "transcoding is unavailable"}
        }
        job, err := s.job(ctx, lease.JobID, lease.Generation)
        if err != nil || !job.Enabled || job.RunPolicy != OnDemand || job.StoppedByUser {
            s.mu.Unlock()
            return Runtime{}, &Error{Code: "demand_unavailable", Message: "on-demand job is no longer enabled"}
        }
        runtime, active := s.manager.Status(lease.JobID)
        s.mu.Unlock()

        if !active {
            return Runtime{}, &Error{Code: "output_unavailable", Message: "media process is no longer active"}
        }
        switch runtime.State {
        case Running:
            return runtime, nil
        case Failed, Stopped, Stopping, Disabled:
            return Runtime{}, &Error{Code: "output_unavailable", Message: "derived media did not become ready"}
        }
        select {
        case <-ctx.Done():
            return Runtime{}, &Error{Code: "output_timeout", Message: "timed out waiting for the first derived media frame"}
        case <-ticker.C:
        }
    }
}
