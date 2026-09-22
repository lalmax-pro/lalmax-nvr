package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/model"
)

// MergeWindow represents a group of consecutive recordings eligible for merging.
type MergeWindow struct {
	StartTime    time.Time `json:"start_time"`
	EndTime      time.Time `json:"end_time"`
	SegmentCount int       `json:"segment_count"`
	Format       string    `json:"format"`
}

// MergeAndReplaceRecordings atomically inserts a merged recording and deletes old recordings in a single transaction.
// This reduces SQLITE_BUSY contention compared to separate INSERT + SetMerged + DeleteBatch calls.
func (d *DB) MergeAndReplaceRecordings(ctx context.Context, merged *model.Recording, oldIDs []string) error {
	if len(oldIDs) == 0 {
		return d.InsertRecording(ctx, merged)
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	q := `INSERT INTO recordings(id, camera_id, stream_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merged, merge_status) VALUES(?,?,?,?,?,?,?,?,?,?,?,?);`
	_, err = tx.ExecContext(ctx, q, merged.ID, merged.CameraID, recordingStreamID(merged), merged.FilePath, merged.Format, timeToDB(merged.StartedAt), timeToDB(merged.EndedAt), merged.Duration, merged.FileSize, merged.FrameCount, true, model.MergeStatusMerged)
	if err != nil {
		return err
	}

	for _, id := range oldIDs {
		_, err = tx.ExecContext(ctx, `DELETE FROM recordings WHERE id = ?;`, id)
		if err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	d.invalidateRecordingsCache()
	return nil
}

// ListMergeableSegments returns recordings for a camera within a time window,
// excluding merged and incomplete segments.
func (d *DB) ListMergeableSegments(ctx context.Context, cameraID string, windowStart, windowEnd time.Time) ([]*model.Recording, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merged, merge_status, archived FROM recordings WHERE camera_id = ? AND merge_status = 'pending' AND ended_at IS NOT NULL AND started_at >= ? AND started_at < ? ORDER BY started_at ASC;`,
		cameraID, formatTime(windowStart), formatTime(windowEnd))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []*model.Recording
	for rows.Next() {
		var r model.Recording
		var startedAtStr, endedAtStr, mergeStatusStr sql.NullString
		if err := rows.Scan(&r.ID, &r.CameraID, &r.FilePath, &r.Format, &startedAtStr, &endedAtStr, &r.Duration, &r.FileSize, &r.FrameCount, &r.Merged, &mergeStatusStr, &r.Archived); err != nil {
			return nil, err
		}
		scanRecording(&r, startedAtStr, endedAtStr, mergeStatusStr, sql.NullString{})
		res = append(res, &r)
	}
	return res, nil
}

func mergeWindowSeconds(window time.Duration) int64 {
	s := int64(window / time.Second)
	if s < 60 {
		s = 3600
	}
	return s
}

// ListCameraMergeWindows returns merge windows for a camera with 2+ segments.
// Windows are UTC epoch buckets of `window` (default 1h). Only includes recordings older than minAge.
func (d *DB) ListCameraMergeWindows(ctx context.Context, cameraID string, minAge, window time.Duration) ([]MergeWindow, error) {
	cutoff := time.Now().Add(-minAge).Format(sqliteTimeFormat)
	secs := mergeWindowSeconds(window)
	query := `SELECT (CAST(strftime('%s', substr(started_at, 1, 19)) AS INTEGER) / ?) * ? as bucket, MIN(started_at), MAX(ended_at), COUNT(*), format FROM recordings WHERE camera_id = ? AND merge_status = 'pending' AND ended_at IS NOT NULL AND ended_at < ? GROUP BY bucket, format HAVING COUNT(*) >= 2 ORDER BY bucket ASC;`
	rows, err := d.db.QueryContext(ctx, query, secs, secs, cameraID, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []MergeWindow
	for rows.Next() {
		var w MergeWindow
		var bucket int64
		var minStart, maxEnd sql.NullString
		if err := rows.Scan(&bucket, &minStart, &maxEnd, &w.SegmentCount, &w.Format); err != nil {
			return nil, err
		}
		w.StartTime = scanTime(minStart)
		w.EndTime = scanTime(maxEnd)
		res = append(res, w)
	}
	return res, nil
}

// ListHourMergedRecordings returns already-merged recordings whose started_at falls in [hourStart, hourEnd).
func (d *DB) ListHourMergedRecordings(ctx context.Context, cameraID string, hourStart, hourEnd time.Time) ([]*model.Recording, error) {
	return d.ListWindowMergedRecordings(ctx, cameraID, hourStart, hourEnd)
}

// ListWindowMergedRecordings returns already-merged recordings whose started_at falls in [windowStart, windowEnd).
func (d *DB) ListWindowMergedRecordings(ctx context.Context, cameraID string, windowStart, windowEnd time.Time) ([]*model.Recording, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT id, camera_id, file_path, format, started_at, ended_at, duration, file_size, frame_count, merged, merge_status, archived FROM recordings WHERE camera_id = ? AND merge_status = 'merged' AND COALESCE(archived,0) = 0 AND ended_at IS NOT NULL AND started_at >= ? AND started_at < ? ORDER BY started_at ASC;`,
		cameraID, formatTime(windowStart), formatTime(windowEnd))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []*model.Recording
	for rows.Next() {
		var r model.Recording
		var startedAtStr, endedAtStr, mergeStatusStr sql.NullString
		if err := rows.Scan(&r.ID, &r.CameraID, &r.FilePath, &r.Format, &startedAtStr, &endedAtStr, &r.Duration, &r.FileSize, &r.FrameCount, &r.Merged, &mergeStatusStr, &r.Archived); err != nil {
			return nil, err
		}
		scanRecording(&r, startedAtStr, endedAtStr, mergeStatusStr, sql.NullString{})
		res = append(res, &r)
	}
	return res, nil
}

// CountPendingMerges returns how many pending (unmerged) closed segments a camera has.
func (d *DB) CountPendingMerges(ctx context.Context, cameraID string) (int, error) {
	var n int
	err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM recordings WHERE camera_id = ? AND merge_status = 'pending' AND ended_at IS NOT NULL AND COALESCE(archived,0) = 0;`,
		cameraID).Scan(&n)
	return n, err
}

// TimelineEntry is a lightweight recording row for the 24h DVR bar.
type TimelineEntry struct {
	ID        string       `json:"id"`
	CameraID  string       `json:"camera_id"`
	StartedAt time.Time    `json:"started_at"`
	EndedAt   time.Time    `json:"ended_at"`
	Duration  float64      `json:"duration"`
	Format    model.Format `json:"format"`
	Merged    bool         `json:"merged"`
	GapReason string       `json:"gap_reason,omitempty"`
	Locked    bool         `json:"locked"`
}

// ListRecordingTimeline returns a compact projection of recordings in [start, end], capped at limit.
func (d *DB) ListRecordingTimeline(ctx context.Context, cameraID string, start, end time.Time, limit int) ([]TimelineEntry, error) {
	if limit <= 0 || limit > 10000 {
		limit = 10000
	}
	rows, err := d.db.QueryContext(ctx,
		`SELECT id, camera_id, started_at, ended_at, duration, format, merged, COALESCE(gap_reason,''), COALESCE(locked,0) FROM recordings WHERE camera_id = ? AND COALESCE(archived,0) = 0 AND ended_at IS NOT NULL AND started_at < ? AND ended_at > ? ORDER BY started_at ASC LIMIT ?;`,
		cameraID, formatTime(end), formatTime(start), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []TimelineEntry
	for rows.Next() {
		var e TimelineEntry
		var startedAtStr, endedAtStr sql.NullString
		var locked int
		if err := rows.Scan(&e.ID, &e.CameraID, &startedAtStr, &endedAtStr, &e.Duration, &e.Format, &e.Merged, &e.GapReason, &locked); err != nil {
			return nil, err
		}
		e.StartedAt = scanTime(startedAtStr)
		e.EndedAt = scanTime(endedAtStr)
		e.Locked = locked != 0
		res = append(res, e)
	}
	return res, nil
}

// UpsertCameraMerge writes per-camera merge config columns.
// Pass nil pointers to leave fields unchanged (keep existing values).
func (d *DB) UpsertCameraMerge(ctx context.Context, cameraID string, mergeEnabled *bool, mergeCheckInterval, mergeWindowSize, mergeMinSegmentAge *string, mergeBatchLimit, mergeMinSegmentsToMerge *int, mergeRollingEnabled *bool, mergeRollingDebounce *string) error {
	q := `UPDATE cameras SET
		merge_enabled = COALESCE(?, merge_enabled),
		merge_check_interval = COALESCE(?, merge_check_interval),
		merge_window_size = COALESCE(?, merge_window_size),
		merge_batch_limit = COALESCE(?, merge_batch_limit),
		merge_min_segment_age = COALESCE(?, merge_min_segment_age),
		merge_min_segments_to_merge = COALESCE(?, merge_min_segments_to_merge),
		merge_rolling_enabled = COALESCE(?, merge_rolling_enabled),
		merge_rolling_debounce = COALESCE(?, merge_rolling_debounce)
		WHERE id = ?;`
	_, err := d.db.ExecContext(ctx, q,
		ptrToNullBool(mergeEnabled),
		ptrToNullString(mergeCheckInterval),
		ptrToNullString(mergeWindowSize),
		ptrToNullInt64(mergeBatchLimit),
		ptrToNullString(mergeMinSegmentAge),
		ptrToNullInt64(mergeMinSegmentsToMerge),
		ptrToNullBool(mergeRollingEnabled),
		ptrToNullString(mergeRollingDebounce),
		cameraID)
	return err
}

// ListPendingMergeCameraIDs returns camera or stream ids that still have closed segments waiting to merge.
func (d *DB) ListPendingMergeCameraIDs(ctx context.Context) ([]string, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT DISTINCT camera_id FROM recordings WHERE merge_status = 'pending' AND ended_at IS NOT NULL AND COALESCE(archived,0) = 0 ORDER BY camera_id;`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}

// GrowMergedRecording updates an existing hour file's row and deletes the short segments it absorbed.
func (d *DB) GrowMergedRecording(ctx context.Context, rec *model.Recording, deleteIDs []string) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `UPDATE recordings SET ended_at=?, duration=?, file_size=?, frame_count=? WHERE id=?;`,
		timeToDB(rec.EndedAt), rec.Duration, rec.FileSize, rec.FrameCount, rec.ID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("merged recording %s not found", rec.ID)
	}
	for _, id := range deleteIDs {
		if _, err := tx.ExecContext(ctx, `DELETE FROM recordings WHERE id = ?;`, id); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	d.invalidateRecordingsCache()
	return nil
}

// SetMergeStatus updates merge_status for the given recording IDs in a transaction.
// Empty ids slice is a no-op.
func (d *DB) SetMergeStatus(ctx context.Context, ids []string, status string) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	q := `UPDATE recordings SET merge_status = ? WHERE id = ?;`
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, q, status, id); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	d.invalidateRecordingsCache()
	return nil
}

// ListSingletonPendingRecordings returns pending recordings for a camera that are
// older than minAge but are NOT part of any multi-segment merge window.
// These are window-boundary orphans that will never be merged.
func (d *DB) ListSingletonPendingRecordings(ctx context.Context, cameraID string, minAge, window time.Duration) ([]*model.Recording, error) {
	cutoff := time.Now().Add(-minAge).Format(sqliteTimeFormat)
	secs := mergeWindowSeconds(window)
	query := `
		SELECT r.id, r.camera_id, r.file_path, r.format, r.started_at, r.ended_at, r.duration, r.file_size, r.frame_count, r.merged, r.merge_status, r.archived
		FROM recordings r
		WHERE r.camera_id = ?
			AND r.merge_status = 'pending'
			AND r.ended_at IS NOT NULL
			AND r.ended_at < ?
			AND (
				SELECT COUNT(*)
				FROM recordings r2
				WHERE r2.camera_id = r.camera_id
					AND r2.merge_status = 'pending'
					AND r2.ended_at IS NOT NULL
					AND r2.format = r.format
					AND (CAST(strftime('%s', substr(r2.started_at, 1, 19)) AS INTEGER) / ?) * ?
					  = (CAST(strftime('%s', substr(r.started_at, 1, 19)) AS INTEGER) / ?) * ?
			) = 1;
		`
	rows, err := d.db.QueryContext(ctx, query, cameraID, cutoff, secs, secs, secs, secs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []*model.Recording
	for rows.Next() {
		var r model.Recording
		var startedAtStr, endedAtStr, mergeStatusStr sql.NullString
		if err := rows.Scan(&r.ID, &r.CameraID, &r.FilePath, &r.Format, &startedAtStr, &endedAtStr, &r.Duration, &r.FileSize, &r.FrameCount, &r.Merged, &mergeStatusStr, &r.Archived); err != nil {
			return nil, err
		}
		scanRecording(&r, startedAtStr, endedAtStr, mergeStatusStr, sql.NullString{})
		res = append(res, &r)
	}
	return res, nil
}
