package transcode

import (
	"context"
	"time"
)

// Variant describes a logical output; credentials and physical media group names
// are deliberately excluded from catalog responses.
type Variant struct {
	ID         string     `json:"id"`
	JobID      string     `json:"job_id"`
	SourceID   string     `json:"source_id"`
	ProfileID  string     `json:"profile_id"`
	Generation uint64     `json:"generation"`
	State      State      `json:"state"`
	ErrorCode  string     `json:"error_code,omitempty"`
	RetryAt    *time.Time `json:"retry_at,omitempty"`
}

func (s *Service) Variants(ctx context.Context, sourceID string) ([]Variant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs, err := s.repo.ListTranscodeJobs(ctx)
	if err != nil {
		return nil, err
	}
	variants := []Variant{}
	for _, job := range jobs {
		if sourceID != "" && job.Source.StreamID != sourceID {
			continue
		}
		id, err := VariantID(job.ID)
		if err != nil {
			return nil, err
		}
		v := Variant{ID: id, JobID: job.ID, SourceID: job.Source.StreamID, ProfileID: job.ProfileID, Generation: job.Generation, State: Stopped}
		if !job.Enabled {
			v.State = Disabled
		}
		if s.manager != nil {
			if runtime, ok := s.manager.Status(job.ID); ok {
				v.State = runtime.State
				v.ErrorCode = runtime.ErrorCode
			}
		}
		if retry, ok := s.retry[job.ID]; ok && retry.generation == job.Generation && retry.errorCode != "" && job.Enabled && !job.StoppedByUser && v.State != Running && v.State != Starting {
			v.State = Backoff
			v.ErrorCode = retry.errorCode
			next := retry.next
			v.RetryAt = &next
		}
		variants = append(variants, v)
	}
	return variants, nil
}

// PlaybackTarget is internal-only. The API/consumer must authorize the logical
// source first; the adapter then issues a scoped URL for the physical output.
func (s *Service) PlaybackTarget(ctx context.Context, sourceID, jobID string) (SourceRef, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs, err := s.repo.ListTranscodeJobs(ctx)
	if err != nil {
		return SourceRef{}, "", err
	}
	for _, job := range jobs {
		if job.ID == jobID && job.Source.StreamID == sourceID {
			if !job.Enabled || s.manager == nil {
				return SourceRef{}, "", &Error{Code: "output_unavailable", Message: "derived output is not running"}
			}
			runtime, ok := s.manager.Status(job.ID)
			if !ok || runtime.State != Running {
				return SourceRef{}, "", &Error{Code: "output_unavailable", Message: "derived output is not ready"}
			}
			return job.Source, runtime.VariantID, nil
		}
	}
	return SourceRef{}, "", &Error{Code: "job_not_found", Message: "output does not belong to this source"}
}
