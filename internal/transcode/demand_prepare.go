package transcode

import (
    "context"
    "errors"
)

// PrepareDemandCapability revalidates an explicitly enabled on-demand job's
// configured media tools. HA preload uses this after a cold restart when the
// in-memory probe cache is empty. It does not start an encoder or grant access.
func (s *Service) PrepareDemandCapability(ctx context.Context, jobID string, generation uint64) error {
    s.mu.Lock()
    job, err := s.job(ctx, jobID, generation)
    if err != nil { s.mu.Unlock(); return err }
    if job.RunPolicy != OnDemand || !job.Enabled || job.StoppedByUser || !s.config.Enabled {
        s.mu.Unlock()
        return &Error{Code:"demand_unavailable",Message:"on-demand job is not enabled"}
    }
    profile, err := s.profile(ctx, job)
    if err != nil { s.mu.Unlock(); return err }
    check := s.allow(profile)
    if check == nil { s.mu.Unlock(); return nil }
    var domain *Error
    if !errors.As(check, &domain) || domain.Code != "probe_required" || s.probes == nil {
        s.mu.Unlock()
        return check
    }
    paths := s.config.Tools()
    s.mu.Unlock()

    // Probe can execute FFmpeg/GStreamer for tens of seconds. Never keep
    // Service.mu locked while probing so stop/delete remain responsive.
    report, err := s.probes.Probe(ctx, paths, profile)
    if err != nil { return err }
    if !report.CanEnable || report.Stale {
        return &Error{Code:"probe_required",Message:"the required media capability check did not pass"}
    }

    s.mu.Lock()
    defer s.mu.Unlock()
    current, err := s.job(ctx, jobID, generation)
    if err != nil { return err }
    if current.RunPolicy != OnDemand || !current.Enabled || current.StoppedByUser {
        return &Error{Code:"demand_unavailable",Message:"on-demand intent changed during probe"}
    }
    refreshed, err := s.profile(ctx, current)
    if err != nil { return err }
    return s.allow(refreshed)
}
