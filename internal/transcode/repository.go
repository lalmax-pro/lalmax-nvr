package transcode

import "context"

// Repository persists intent, not PIDs or running state. Implementations must
// atomically compare revisions and reject deletion of referenced profiles.
type Repository interface {
	ListTranscodeProfiles(context.Context) ([]Profile, error)
	SaveTranscodeProfile(context.Context, Profile, uint64) (Profile, error)
	DeleteTranscodeProfile(context.Context, string, uint64) error
	ListTranscodeJobs(context.Context) ([]Job, error)
	SaveTranscodeJob(context.Context, Job, uint64) (Job, error)
	DeleteTranscodeJob(context.Context, string, uint64) error
	SetTranscodeJobStopped(context.Context, string, uint64, bool) error
}

// VariantID is the logical job output identity, independent of process generations.
// Manager.Runtime.VariantID is the shared physical media group: multiple logical
// outputs may resolve to it. Never use this logical ID as a publication target.
//
// Authoritative registry membership, not this prefix, determines internal status.
func VariantID(jobID string) (string, error) {
	if !identifier.MatchString(jobID) {
		return "", invalid("invalid job id")
	}
	return "nvr_tc_" + jobID, nil
}
