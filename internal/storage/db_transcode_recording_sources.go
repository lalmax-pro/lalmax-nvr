package storage

import (
    "context"
    "database/sql"
    "errors"
    "fmt"
    "time"
)

// DerivedRecordingSource is immutable provenance. No playback credentials,
// file paths or ephemeral capability tokens are stored in this catalog.
type DerivedRecordingSource struct {
    PhysicalStreamID string `json:"physical_stream_id"`
    SourceStreamID string `json:"source_stream_id"`
    JobID string `json:"job_id"`
    Generation uint64 `json:"generation"`
    ProfileID string `json:"profile_id"`
    ProfileRevision uint64 `json:"profile_revision"`
    VideoCodec string `json:"video_codec"`
    AudioCodec string `json:"audio_codec"`
    RequestedAt string `json:"requested_at"`
}

func (d *DB) SaveDerivedRecordingSource(ctx context.Context, source DerivedRecordingSource) error {
    if source.PhysicalStreamID=="" || source.SourceStreamID=="" || source.JobID=="" ||
        source.ProfileID=="" || source.Generation==0 || source.ProfileRevision==0 ||
        source.VideoCodec=="" {
        return fmt.Errorf("incomplete derived recording provenance")
    }
    var existing DerivedRecordingSource
    err:=d.queryRowContext(ctx,`SELECT physical_stream_id,source_stream_id,job_id,generation,
        profile_id,profile_revision,video_codec,audio_codec,requested_at
        FROM transcode_recording_sources_v1 WHERE physical_stream_id=?`,
        source.PhysicalStreamID).Scan(&existing.PhysicalStreamID,&existing.SourceStreamID,
        &existing.JobID,&existing.Generation,&existing.ProfileID,&existing.ProfileRevision,
        &existing.VideoCodec,&existing.AudioCodec,&existing.RequestedAt)
    if err==nil {
        // A stable output identity may be recorded more than once, but must
        // never be rebound to a different logical source, job or codec.
        existing.RequestedAt=source.RequestedAt
        if existing==source {return nil}
        return fmt.Errorf("derived physical stream provenance collision")
    }
    if !errors.Is(err,sql.ErrNoRows) {return err}
    if source.RequestedAt=="" {source.RequestedAt=time.Now().UTC().Format(time.RFC3339Nano)}
    _,err=d.execContext(ctx,`INSERT INTO transcode_recording_sources_v1
        (physical_stream_id,source_stream_id,job_id,generation,profile_id,
         profile_revision,video_codec,audio_codec,requested_at)
         VALUES (?,?,?,?,?,?,?,?,?)`,
        source.PhysicalStreamID,source.SourceStreamID,source.JobID,source.Generation,
        source.ProfileID,source.ProfileRevision,source.VideoCodec,source.AudioCodec,source.RequestedAt)
    return err
}

func (d *DB) GetDerivedRecordingSource(ctx context.Context, physicalID string) (DerivedRecordingSource,error) {
    var rec DerivedRecordingSource
    err:=d.queryRowContext(ctx,`SELECT physical_stream_id,source_stream_id,job_id,generation,
        profile_id,profile_revision,video_codec,audio_codec,requested_at
        FROM transcode_recording_sources_v1 WHERE physical_stream_id=?`,physicalID).
        Scan(&rec.PhysicalStreamID,&rec.SourceStreamID,&rec.JobID,&rec.Generation,
            &rec.ProfileID,&rec.ProfileRevision,&rec.VideoCodec,&rec.AudioCodec,&rec.RequestedAt)
    return rec,err
}
