package transcode

import (
	"context"
	"sync"
	"time"
)

// ProbeService serializes expensive self-tests and stores only completed results.
// A caller must match the configuration fingerprint and TTL before using a result.
type ProbeService struct {
	runner  ProbeRunner
	gate    chan struct{}
	mu      sync.RWMutex
	reports map[string]CapabilityReport
	ttl     time.Duration
}

func NewProbeService(runner ProbeRunner) *ProbeService {
	if runner == nil {
		runner = CommandProbeRunner{}
	}
	return &ProbeService{runner: runner, gate: make(chan struct{}, 1), reports: make(map[string]CapabilityReport), ttl: 10 * time.Minute}
}
func (s *ProbeService) Probe(ctx context.Context, paths ToolPaths, p Profile) (CapabilityReport, error) {
	if err := p.Validate(); err != nil {
		return CapabilityReport{}, err
	}
	select {
	case s.gate <- struct{}{}:
		defer func() { <-s.gate }()
	default:
		return CapabilityReport{}, &Error{Code: "probe_busy", Message: "another capability check is already running"}
	}
	// Rechecking invalidates previous approval immediately. A canceled or failed
	// replacement probe must not leave an earlier success usable for activation.
	s.mu.Lock()
	delete(s.reports, p.ID)
	s.mu.Unlock()
	var report CapabilityReport
	var err error
	if p.Backend == "ffmpeg" {
		report, err = ProbeFFmpeg(ctx, paths, p, s.runner)
	} else {
		report, err = ProbeGStreamer(ctx, paths, p, s.runner)
	}
	if err != nil {
		return report, err
	}
	if ctx.Err() != nil {
		return report, &Error{Code: "probe_canceled", Message: "capability check canceled"}
	}
	s.mu.Lock()
	// One bounded result per profile, not an unbounded cache per config hash.
	if len(s.reports) >= 256 {
		s.reports = make(map[string]CapabilityReport)
	}
	s.reports[p.ID] = cloneReport(report)
	s.mu.Unlock()
	return report, nil
}
func (s *ProbeService) Result(profileID string) (CapabilityReport, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	report, ok := s.reports[profileID]
	return cloneReport(report), ok
}

// CurrentResult invalidates stale results for both API display and activation.
func (s *ProbeService) CurrentResult(paths ToolPaths, profile Profile) (CapabilityReport, bool) {
	report, ok := s.Result(profile.ID)
	if !ok {
		return report, false
	}
	fingerprint, err := ProbeFingerprint(paths, profile)
	age := time.Since(report.CheckedAt)
	report.Stale = err != nil || report.Fingerprint != fingerprint || age < 0 || age >= s.ttl
	if report.Stale {
		report.CanEnable = false
	}
	return report, true
}

func (s *ProbeService) Allows(profileID, fingerprint string) bool {
	report, ok := s.Result(profileID)
	return ok && fingerprint != "" && report.Fingerprint == fingerprint && report.CanEnable && time.Since(report.CheckedAt) >= 0 && time.Since(report.CheckedAt) < s.ttl
}
func cloneReport(r CapabilityReport) CapabilityReport {
	r.Checks = append([]CapabilityCheck(nil), r.Checks...)
	return r
}
