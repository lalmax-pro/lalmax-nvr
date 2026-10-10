package storage

import (
	"context"
	"database/sql"
	"time"
)

// RelayTaskRecord is the persisted representation of a relay task.
type RelayTaskRecord struct {
	ID        string
	StreamID  string
	TargetURL string
	Status    string
	ErrorMsg  string
	CreatedAt time.Time
	StartedAt *time.Time
	StoppedAt *time.Time
}

func (d *DB) SaveRelayTask(ctx context.Context, task RelayTaskRecord) error {
	_, err := d.execContext(ctx, `INSERT INTO relay_tasks
		(id, stream_id, target_url, status, error_msg, created_at, started_at, stopped_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET stream_id=excluded.stream_id, target_url=excluded.target_url,
		status=excluded.status, error_msg=excluded.error_msg, created_at=excluded.created_at,
		started_at=excluded.started_at, stopped_at=excluded.stopped_at`,
		task.ID, task.StreamID, task.TargetURL, task.Status, task.ErrorMsg,
		timeToDB(task.CreatedAt), relayNullableTime(task.StartedAt), relayNullableTime(task.StoppedAt))
	return err
}

func (d *DB) GetRelayTask(ctx context.Context, taskID string) (*RelayTaskRecord, error) {
	var task RelayTaskRecord
	var createdAt, startedAt, stoppedAt sql.NullString
	err := d.queryRowContext(ctx, `SELECT id, stream_id, target_url, status, error_msg,
		created_at, started_at, stopped_at FROM relay_tasks WHERE id = ?`, taskID).Scan(
		&task.ID, &task.StreamID, &task.TargetURL, &task.Status, &task.ErrorMsg,
		&createdAt, &startedAt, &stoppedAt)
	if err != nil {
		return nil, err
	}
	task.CreatedAt = scanTime(createdAt)
	if startedAt.Valid {
		value := scanTime(startedAt)
		task.StartedAt = &value
	}
	if stoppedAt.Valid {
		value := scanTime(stoppedAt)
		task.StoppedAt = &value
	}
	return &task, nil
}

func (d *DB) ListRelayTasks(ctx context.Context) ([]RelayTaskRecord, error) {
	rows, err := d.queryContext(ctx, `SELECT id, stream_id, target_url, status, error_msg,
		created_at, started_at, stopped_at FROM relay_tasks ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tasks := make([]RelayTaskRecord, 0)
	for rows.Next() {
		var task RelayTaskRecord
		var createdAt, startedAt, stoppedAt sql.NullString
		if err := rows.Scan(&task.ID, &task.StreamID, &task.TargetURL, &task.Status,
			&task.ErrorMsg, &createdAt, &startedAt, &stoppedAt); err != nil {
			return nil, err
		}
		task.CreatedAt = scanTime(createdAt)
		if startedAt.Valid {
			value := scanTime(startedAt)
			task.StartedAt = &value
		}
		if stoppedAt.Valid {
			value := scanTime(stoppedAt)
			task.StoppedAt = &value
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func (d *DB) DeleteRelayTask(ctx context.Context, taskID string) error {
	_, err := d.execContext(ctx, `DELETE FROM relay_tasks WHERE id = ?`, taskID)
	return err
}

func relayNullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return timeToDB(*value)
}
