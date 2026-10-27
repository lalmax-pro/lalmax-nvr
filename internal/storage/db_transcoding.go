package storage

import (
	"context"
	"encoding/json"
	"errors"
	"math"

	"github.com/lalmax-pro/lalmax-nvr/internal/transcode"
)

// One versioned intent document makes profile references and edits atomic across
// all supported SQL drivers. Runtime state and credentials are never stored here.
type transcodeIntent struct {
	Profiles []transcode.Profile `json:"profiles"`
	Jobs     []transcode.Job     `json:"jobs"`
}

func (d *DB) createTranscodeTable(ctx context.Context) error {
	_, err := d.execContext(ctx, `CREATE TABLE IF NOT EXISTS transcode_intent_v1 (id TEXT PRIMARY KEY, revision INTEGER NOT NULL, payload TEXT NOT NULL)`)
	if err != nil {
		return err
	}
	if _, err = d.execContext(ctx, `CREATE TABLE IF NOT EXISTS transcode_outputs_v1 (id TEXT PRIMARY KEY, source_id TEXT NOT NULL)`); err != nil {
		return err
	}
	_, err = d.execContext(ctx, `INSERT OR IGNORE INTO transcode_intent_v1 (id, revision, payload) VALUES (?, ?, ?)`, "main", 1, `{"profiles":[],"jobs":[]}`)
	return err
}
func (d *DB) loadTranscodeIntent(ctx context.Context) (transcodeIntent, int64, error) {
	var state transcodeIntent
	var revision int64
	var payload string
	err := d.queryRowContext(ctx, `SELECT revision, payload FROM transcode_intent_v1 WHERE id = ?`, "main").Scan(&revision, &payload)
	if err != nil {
		return state, 0, err
	}
	err = json.Unmarshal([]byte(payload), &state)
	if state.Profiles == nil {
		state.Profiles = []transcode.Profile{}
	}
	if state.Jobs == nil {
		state.Jobs = []transcode.Job{}
	}
	return state, revision, err
}
func conflict() error {
	return &transcode.Error{Code: "revision_conflict", Message: "configuration changed; reload and retry"}
}
func (d *DB) changeTranscodeIntent(ctx context.Context, change func(*transcodeIntent) error) error {
	for attempt := 0; attempt < 8; attempt++ {
		state, revision, err := d.loadTranscodeIntent(ctx)
		if err != nil {
			return err
		}
		if err = change(&state); err != nil {
			return err
		}
		if revision == math.MaxInt64 {
			return errors.New("transcode revision exhausted")
		}
		payload, err := json.Marshal(state)
		if err != nil {
			return err
		}
		result, err := d.execContext(ctx, `UPDATE transcode_intent_v1 SET revision = ?, payload = ? WHERE id = ? AND revision = ?`, revision+1, string(payload), "main", revision)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n == 1 {
			return nil
		}
	}
	return conflict()
}
func (d *DB) ListTranscodeProfiles(ctx context.Context) ([]transcode.Profile, error) {
	s, _, err := d.loadTranscodeIntent(ctx)
	return s.Profiles, err
}
func (d *DB) ListTranscodeJobs(ctx context.Context) ([]transcode.Job, error) {
	s, _, err := d.loadTranscodeIntent(ctx)
	return s.Jobs, err
}
func (d *DB) SaveTranscodeProfile(ctx context.Context, p transcode.Profile, expected uint64) (transcode.Profile, error) {
	if err := p.Validate(); err != nil {
		return p, err
	}
	if expected >= math.MaxInt64 {
		return p, conflict()
	}
	p.Revision = expected + 1
	err := d.changeTranscodeIntent(ctx, func(s *transcodeIntent) error {
		index := -1
		for i, old := range s.Profiles {
			if old.ID == p.ID {
				if old.Revision != expected {
					return conflict()
				}
				index = i
				break
			}
		}
		if index < 0 {
			if expected != 0 {
				return conflict()
			}
			if len(s.Profiles) >= 256 {
				return &transcode.Error{Code: "capacity_exceeded", Message: "profile limit reached"}
			}
			s.Profiles = append(s.Profiles, p)
			return nil
		}
		// First version keeps pinned task profiles immutable until references are removed.
		for _, job := range s.Jobs {
			if job.ProfileID == p.ID {
				return &transcode.Error{Code: "profile_in_use", Message: "remove referencing jobs before changing this profile"}
			}
		}
		s.Profiles[index] = p
		return nil
	})
	return p, err
}
func (d *DB) DeleteTranscodeProfile(ctx context.Context, id string, expected uint64) error {
	return d.changeTranscodeIntent(ctx, func(s *transcodeIntent) error {
		for i, p := range s.Profiles {
			if p.ID == id {
				if p.Revision != expected {
					return conflict()
				}
				for _, j := range s.Jobs {
					if j.ProfileID == id {
						return &transcode.Error{Code: "profile_in_use", Message: "profile is referenced by a job"}
					}
				}
				s.Profiles = append(s.Profiles[:i], s.Profiles[i+1:]...)
				return nil
			}
		}
		return conflict()
	})
}
func (d *DB) SaveTranscodeJob(ctx context.Context, j transcode.Job, expected uint64) (transcode.Job, error) {
	if err := j.Validate(); err != nil {
		return j, err
	}
	if expected >= math.MaxInt64 {
		return j, conflict()
	}
	j.Generation = expected + 1
	err := d.changeTranscodeIntent(ctx, func(s *transcodeIntent) error {
		found := false
		for _, p := range s.Profiles {
			if p.ID == j.ProfileID && p.Revision == j.ProfileRevision {
				found = true
			}
		}
		if !found {
			return &transcode.Error{Code: "profile_not_found", Message: "selected profile revision does not exist"}
		}
		for i, old := range s.Jobs {
			if old.ID == j.ID {
				if old.Generation != expected {
					return conflict()
				}
				s.Jobs[i] = j
				return nil
			}
		}
		if expected != 0 {
			return conflict()
		}
		if len(s.Jobs) >= 256 {
			return &transcode.Error{Code: "capacity_exceeded", Message: "job limit reached"}
		}
		s.Jobs = append(s.Jobs, j)
		return nil
	})
	return j, err
}
func (d *DB) DeleteTranscodeJob(ctx context.Context, id string, expected uint64) error {
	return d.changeTranscodeIntent(ctx, func(s *transcodeIntent) error {
		for i, j := range s.Jobs {
			if j.ID == id {
				if j.Generation != expected {
					return conflict()
				}
				s.Jobs = append(s.Jobs[:i], s.Jobs[i+1:]...)
				return nil
			}
		}
		return conflict()
	})
}

var _ transcode.Repository = (*DB)(nil)
