package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/model"
)

// EventsFilter defines query parameters for listing product events.
type EventsFilter struct {
	CameraID string
	Source   string
	Type     string
	Status   string
	Since    string
	Until    string
	Limit    int
	Offset   int
}

// InsertEvent inserts a product-level event and returns its ID.
func (d *DB) InsertEvent(ctx context.Context, event model.Event) (int64, error) {
	if event.StartedAt.IsZero() {
		event.StartedAt = time.Now().UTC()
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	if event.Severity == "" {
		event.Severity = model.EventSeverityInfo
	}
	if event.Status == "" {
		event.Status = model.EventStatusOpen
	}
	if event.Metadata == "" {
		event.Metadata = "{}"
	}

	var endedAt any
	if event.EndedAt != nil && !event.EndedAt.IsZero() {
		endedAt = formatTime(*event.EndedAt)
	}
	var acknowledgedAt any
	if event.AcknowledgedAt != nil && !event.AcknowledgedAt.IsZero() {
		acknowledgedAt = formatTime(*event.AcknowledgedAt)
	}

	q := `INSERT INTO events(camera_id, source, type, severity, status, message, metadata, recording_id, snapshot_path, started_at, ended_at, acknowledged_at, created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?);`
	res, err := d.db.ExecContext(ctx, q,
		event.CameraID,
		event.Source,
		event.Type,
		event.Severity,
		event.Status,
		event.Message,
		event.Metadata,
		event.RecordingID,
		event.SnapshotPath,
		formatTime(event.StartedAt),
		endedAt,
		acknowledgedAt,
		formatTime(event.CreatedAt),
	)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	event.ID = id
	d.NotifyEvent(event)
	return id, nil
}

// ListEvents returns events matching the filter, ordered by started_at DESC.
func (d *DB) ListEvents(ctx context.Context, filter EventsFilter) ([]model.Event, int, error) {
	where, args := buildEventsWhere(filter)
	whereClause := ""
	if len(where) > 0 {
		whereClause = " WHERE " + strings.Join(where, " AND ")
	}

	countSQL := "SELECT COUNT(*) FROM events" + whereClause
	var total int
	if err := d.db.QueryRowContext(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	dataSQL := `SELECT id, camera_id, source, type, severity, status, message, metadata, recording_id, snapshot_path, started_at, ended_at, acknowledged_at, created_at FROM events` + whereClause
	dataSQL += " ORDER BY started_at DESC, id DESC"
	if filter.Limit > 0 {
		dataSQL += fmt.Sprintf(" LIMIT %d", filter.Limit)
	}
	if filter.Offset > 0 {
		dataSQL += fmt.Sprintf(" OFFSET %d", filter.Offset)
	}
	dataSQL += ";"

	rows, err := d.db.QueryContext(ctx, dataSQL, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	events := []model.Event{}
	for rows.Next() {
		event, err := scanEvent(rows)
		if err != nil {
			return nil, 0, err
		}
		events = append(events, event)
	}
	return events, total, rows.Err()
}

// GetEvent returns a single event by ID.
func (d *DB) GetEvent(ctx context.Context, id int64) (*model.Event, error) {
	q := `SELECT id, camera_id, source, type, severity, status, message, metadata, recording_id, snapshot_path, started_at, ended_at, acknowledged_at, created_at FROM events WHERE id=?;`
	event, err := scanEvent(d.db.QueryRowContext(ctx, q, id))
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &event, nil
}

// AcknowledgeEvent marks an event as acknowledged.
func (d *DB) AcknowledgeEvent(ctx context.Context, id int64, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	res, err := d.db.ExecContext(ctx,
		`UPDATE events SET status=?, acknowledged_at=? WHERE id=?;`,
		model.EventStatusAcknowledged,
		formatTime(at),
		id,
	)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// DeleteEvent deletes an event by ID.
func (d *DB) DeleteEvent(ctx context.Context, id int64) error {
	res, err := d.db.ExecContext(ctx, `DELETE FROM events WHERE id=?;`, id)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func buildEventsWhere(filter EventsFilter) ([]string, []any) {
	where := []string{}
	args := []any{}
	if filter.CameraID != "" {
		where = append(where, "camera_id=?")
		args = append(args, filter.CameraID)
	}
	if filter.Source != "" {
		where = append(where, "source=?")
		args = append(args, filter.Source)
	}
	if filter.Type != "" {
		where = append(where, "type=?")
		args = append(args, filter.Type)
	}
	if filter.Status != "" {
		where = append(where, "status=?")
		args = append(args, filter.Status)
	}
	if filter.Since != "" {
		where = append(where, "started_at>=?")
		args = append(args, filter.Since)
	}
	if filter.Until != "" {
		where = append(where, "started_at<=?")
		args = append(args, filter.Until)
	}
	return where, args
}

type eventScanner interface {
	Scan(dest ...any) error
}

func scanEvent(row eventScanner) (model.Event, error) {
	var event model.Event
	var startedAt, endedAt, acknowledgedAt, createdAt sql.NullString
	err := row.Scan(
		&event.ID,
		&event.CameraID,
		&event.Source,
		&event.Type,
		&event.Severity,
		&event.Status,
		&event.Message,
		&event.Metadata,
		&event.RecordingID,
		&event.SnapshotPath,
		&startedAt,
		&endedAt,
		&acknowledgedAt,
		&createdAt,
	)
	if err != nil {
		return model.Event{}, err
	}
	event.StartedAt = scanTime(startedAt)
	if t := scanTime(endedAt); !t.IsZero() {
		event.EndedAt = &t
	}
	if t := scanTime(acknowledgedAt); !t.IsZero() {
		event.AcknowledgedAt = &t
	}
	event.CreatedAt = scanTime(createdAt)
	return event, nil
}
