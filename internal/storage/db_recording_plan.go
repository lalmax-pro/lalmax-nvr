package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// Recording modes for a plan.
const (
	RecordingModeContinuous = "continuous" // record 24/7 (default)
	RecordingModeScheduled  = "scheduled"  // record only within the plan's weekly windows
	RecordingModeOff        = "off"        // live preview only, no recording
	RecordingModeEvent      = "event"      // record on MQTT / ONVIF motion trigger
	RecordingModeAdaptive   = "adaptive"   // sparse IDR when calm, full speed on activity
)

// ScheduleWindow is one weekly time window inside a recording plan.
type ScheduleWindow struct {
	DayOfWeek int    `json:"day_of_week"` // 0=Sunday .. 6=Saturday
	StartTime string `json:"start_time"`  // "HH:MM"
	EndTime   string `json:"end_time"`    // "HH:MM"
}

// RecordingPlan is a recording policy attached to a lalmax stream.
// Plans are keyed by stream_id, not camera_id: a stream can be recorded
// without a promoted camera/device.
type RecordingPlan struct {
	ID        string           `json:"id"`
	StreamID  string           `json:"stream_id"`
	Name      string           `json:"name"`
	Mode      string           `json:"mode"`
	Enabled   bool             `json:"enabled"`
	Windows   []ScheduleWindow `json:"windows,omitempty"`
	CreatedAt time.Time        `json:"created_at"`
	UpdatedAt time.Time        `json:"updated_at"`
}

// NewRecordingPlanID generates a plan ID.
func NewRecordingPlanID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("plan-%d", time.Now().UnixNano())
	}
	return "plan-" + hex.EncodeToString(buf)
}

// migrateRecordingPlans creates the stream-keyed recording plan tables.
// Idempotent.
func (d *DB) migrateRecordingPlans(ctx context.Context) error {
	if _, err := d.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS recording_plans (
		id TEXT PRIMARY KEY,
		stream_id TEXT NOT NULL UNIQUE,
		name TEXT NOT NULL DEFAULT '',
		mode TEXT NOT NULL DEFAULT 'continuous',
		enabled INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);`); err != nil {
		return err
	}
	if _, err := d.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_recording_plans_stream ON recording_plans(stream_id);`); err != nil {
		return err
	}
	if _, err := d.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS recording_plan_windows (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		plan_id TEXT NOT NULL,
		day_of_week INTEGER NOT NULL,
		start_time TEXT NOT NULL,
		end_time TEXT NOT NULL,
		FOREIGN KEY (plan_id) REFERENCES recording_plans(id) ON DELETE CASCADE
	);`); err != nil {
		return err
	}
	if _, err := d.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_recording_plan_windows_plan ON recording_plan_windows(plan_id);`); err != nil {
		return err
	}
	return nil
}

// ListRecordingPlans returns all recording plans ordered by stream ID.
func (d *DB) ListRecordingPlans(ctx context.Context) ([]RecordingPlan, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT id, stream_id, name, mode, enabled, created_at, updated_at FROM recording_plans ORDER BY stream_id;`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var plans []RecordingPlan
	for rows.Next() {
		var p RecordingPlan
		var createdAt, updatedAt sql.NullString
		if err := rows.Scan(&p.ID, &p.StreamID, &p.Name, &p.Mode, &p.Enabled, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		p.CreatedAt = scanTime(createdAt)
		p.UpdatedAt = scanTime(updatedAt)
		plans = append(plans, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range plans {
		windows, err := d.listPlanWindows(ctx, plans[i].ID)
		if err != nil {
			return nil, err
		}
		plans[i].Windows = windows
	}
	return plans, nil
}

// GetRecordingPlan returns a plan by ID, or nil when absent.
func (d *DB) GetRecordingPlan(ctx context.Context, id string) (*RecordingPlan, error) {
	var p RecordingPlan
	var createdAt, updatedAt sql.NullString
	err := d.db.QueryRowContext(ctx,
		`SELECT id, stream_id, name, mode, enabled, created_at, updated_at FROM recording_plans WHERE id=?;`, id).
		Scan(&p.ID, &p.StreamID, &p.Name, &p.Mode, &p.Enabled, &createdAt, &updatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	p.CreatedAt = scanTime(createdAt)
	p.UpdatedAt = scanTime(updatedAt)
	windows, err := d.listPlanWindows(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	p.Windows = windows
	return &p, nil
}

// GetRecordingPlanByStream returns the plan for a stream, or nil when absent.
func (d *DB) GetRecordingPlanByStream(ctx context.Context, streamID string) (*RecordingPlan, error) {
	var id string
	err := d.db.QueryRowContext(ctx, `SELECT id FROM recording_plans WHERE stream_id=?;`, streamID).Scan(&id)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return d.GetRecordingPlan(ctx, id)
}

// UpsertRecordingPlan creates or replaces the plan for a stream, including its windows.
func (d *DB) UpsertRecordingPlan(ctx context.Context, p *RecordingPlan) error {
	if p == nil {
		return fmt.Errorf("recording plan is nil")
	}
	p.StreamID = strings.TrimSpace(p.StreamID)
	if p.StreamID == "" {
		return fmt.Errorf("stream_id is required")
	}
	if p.Mode == "" {
		p.Mode = RecordingModeContinuous
	}
	if p.ID == "" {
		p.ID = NewRecordingPlanID()
	}
	if p.Name == "" {
		p.Name = p.StreamID
	}

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// A stream has exactly one plan: drop any other plan holding the same stream.
	if _, err := tx.ExecContext(ctx, `DELETE FROM recording_plans WHERE stream_id=? AND id<>?;`, p.StreamID, p.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO recording_plans (id, stream_id, name, mode, enabled, created_at, updated_at)
		VALUES (?,?,?,?,?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		ON CONFLICT(id) DO UPDATE SET stream_id=excluded.stream_id, name=excluded.name, mode=excluded.mode, enabled=excluded.enabled, updated_at=CURRENT_TIMESTAMP;`,
		p.ID, p.StreamID, p.Name, p.Mode, p.Enabled); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM recording_plan_windows WHERE plan_id=?;`, p.ID); err != nil {
		return err
	}
	for _, w := range p.Windows {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO recording_plan_windows (plan_id, day_of_week, start_time, end_time) VALUES (?,?,?,?);`,
			p.ID, w.DayOfWeek, w.StartTime, w.EndTime); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteRecordingPlan removes a plan and its windows.
func (d *DB) DeleteRecordingPlan(ctx context.Context, id string) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM recording_plan_windows WHERE plan_id=?;`, id); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM recording_plans WHERE id=?;`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

// DeleteRecordingPlanByStream removes the plan for a stream, if any.
func (d *DB) DeleteRecordingPlanByStream(ctx context.Context, streamID string) error {
	plan, err := d.GetRecordingPlanByStream(ctx, streamID)
	if err != nil || plan == nil {
		return err
	}
	return d.DeleteRecordingPlan(ctx, plan.ID)
}

func (d *DB) listPlanWindows(ctx context.Context, planID string) ([]ScheduleWindow, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT day_of_week, start_time, end_time FROM recording_plan_windows WHERE plan_id=? ORDER BY day_of_week, start_time;`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	windows := make([]ScheduleWindow, 0)
	for rows.Next() {
		var w ScheduleWindow
		if err := rows.Scan(&w.DayOfWeek, &w.StartTime, &w.EndTime); err != nil {
			return nil, err
		}
		windows = append(windows, w)
	}
	return windows, rows.Err()
}

// DesiredRecordingStreams returns, for every enabled plan, whether the stream
// should be recording right now.
//   - disabled plan  → false
//   - off / event    → false (event windows are handled elsewhere)
//   - scheduled      → true only inside one of the plan's weekly windows
//   - continuous / adaptive / unknown → true
func (d *DB) DesiredRecordingStreams(ctx context.Context) (map[string]bool, error) {
	plans, err := d.ListRecordingPlans(ctx)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	activeWindows, err := d.plansActiveNow(ctx, int(now.Weekday()), now.Format("15:04"))
	if err != nil {
		return nil, err
	}

	desired := make(map[string]bool, len(plans))
	for _, p := range plans {
		if !p.Enabled {
			desired[p.StreamID] = false
			continue
		}
		switch p.Mode {
		case RecordingModeOff, RecordingModeEvent:
			desired[p.StreamID] = false
		case RecordingModeScheduled:
			desired[p.StreamID] = activeWindows[p.ID]
		default:
			desired[p.StreamID] = true
		}
	}
	return desired, nil
}

// plansActiveNow returns plan IDs whose schedule covers the given moment.
func (d *DB) plansActiveNow(ctx context.Context, dayOfWeek int, hhmm string) (map[string]bool, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT DISTINCT plan_id FROM recording_plan_windows
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
