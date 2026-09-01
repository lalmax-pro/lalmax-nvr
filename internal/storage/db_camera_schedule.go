package storage

import (
	"context"
	"time"
)

// Recording modes for a camera.
const (
	RecordingModeContinuous = "continuous" // record 24/7 (default)
	RecordingModeScheduled  = "scheduled"  // record only within the weekly schedule
	RecordingModeOff        = "off"        // live preview only, no recording
	RecordingModeEvent    = "event"    // record on MQTT / ONVIF motion trigger
	RecordingModeAdaptive = "adaptive" // sparse IDR when calm, full speed on activity
)

// CameraScheduleRange is one weekly time window for a camera's recording schedule.
type CameraScheduleRange struct {
	DayOfWeek int    `json:"day_of_week"` // 0=Sunday .. 6=Saturday
	StartTime string `json:"start_time"`  // "HH:MM"
	EndTime   string `json:"end_time"`    // "HH:MM"
}

// migrateRecordingMode adds the recording_mode column to cameras and creates the
// camera_recording_schedules table. Idempotent. New cameras default to 'continuous'.
func (d *DB) migrateRecordingMode(ctx context.Context) error {
	if _, err := d.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS camera_recording_schedules (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		camera_id TEXT NOT NULL,
		day_of_week INTEGER NOT NULL,
		start_time TEXT NOT NULL,
		end_time TEXT NOT NULL,
		FOREIGN KEY (camera_id) REFERENCES cameras(id) ON DELETE CASCADE
	);`); err != nil {
		return err
	}
	if _, err := d.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_camera_recording_schedules_camera ON camera_recording_schedules(camera_id);`); err != nil {
		return err
	}

	var colExists int
	_ = d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('cameras') WHERE name='recording_mode'`).Scan(&colExists)
	if colExists == 0 {
		if _, err := d.db.ExecContext(ctx, "ALTER TABLE cameras ADD COLUMN recording_mode TEXT DEFAULT 'continuous'"); err != nil {
			return err
		}
	}
	return nil
}

// GetCameraSchedule returns a camera's weekly recording schedule.
func (d *DB) GetCameraSchedule(ctx context.Context, cameraID string) ([]CameraScheduleRange, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT day_of_week, start_time, end_time FROM camera_recording_schedules WHERE camera_id=? ORDER BY day_of_week, start_time;`,
		cameraID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ranges := make([]CameraScheduleRange, 0)
	for rows.Next() {
		var r CameraScheduleRange
		if err := rows.Scan(&r.DayOfWeek, &r.StartTime, &r.EndTime); err != nil {
			return nil, err
		}
		ranges = append(ranges, r)
	}
	return ranges, rows.Err()
}

// SetCameraSchedule replaces a camera's weekly recording schedule.
func (d *DB) SetCameraSchedule(ctx context.Context, cameraID string, ranges []CameraScheduleRange) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM camera_recording_schedules WHERE camera_id=?;`, cameraID); err != nil {
		return err
	}
	for _, r := range ranges {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO camera_recording_schedules (camera_id, day_of_week, start_time, end_time) VALUES (?, ?, ?, ?);`,
			cameraID, r.DayOfWeek, r.StartTime, r.EndTime); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetDesiredRecordingState returns, for every non-archived camera, whether it
// should be recording RIGHT NOW based on its recording_mode and weekly schedule.
//   - continuous (or empty) → true
//   - off / event           → false (event is handled separately in phase 2)
//   - scheduled             → true only when the current time falls in a schedule window
func (d *DB) GetDesiredRecordingState(ctx context.Context) (map[string]bool, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT id, COALESCE(recording_mode, 'continuous') FROM cameras WHERE archived = 0;`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type cam struct {
		id   string
		mode string
	}
	var cams []cam
	for rows.Next() {
		var c cam
		if err := rows.Scan(&c.id, &c.mode); err != nil {
			return nil, err
		}
		cams = append(cams, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	now := time.Now()
	activeScheduled, err := d.scheduledActiveNow(ctx, int(now.Weekday()), now.Format("15:04"))
	if err != nil {
		return nil, err
	}

	desired := make(map[string]bool, len(cams))
	for _, c := range cams {
		switch c.mode {
		case RecordingModeOff, RecordingModeEvent:
			desired[c.id] = false
		case RecordingModeScheduled:
			desired[c.id] = activeScheduled[c.id]
		default: // continuous, adaptive, or unknown → record (adaptive gate handles density)
			desired[c.id] = true
		}
	}
	return desired, nil
}

// scheduledActiveNow returns the set of camera IDs whose schedule covers the given moment.
func (d *DB) scheduledActiveNow(ctx context.Context, dayOfWeek int, hhmm string) (map[string]bool, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT DISTINCT camera_id FROM camera_recording_schedules
		WHERE day_of_week = ? AND start_time <= ? AND end_time > ?;`,
		dayOfWeek, hhmm, hhmm)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	active := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		active[id] = true
	}
	return active, rows.Err()
}
