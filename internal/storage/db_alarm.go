package storage

import (
	"context"
	"time"
)

type AlarmRow struct {
	ID          int64     `json:"id"`
	DeviceID    string    `json:"device_id"`
	ChannelID   string    `json:"channel_id"`
	AlarmType   string    `json:"alarm_type"`
	AlarmTime   time.Time `json:"alarm_time"`
	Priority    int       `json:"priority"`
	Method      string    `json:"method"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}

func (d *DB) createAlarmTable(ctx context.Context) error {
	alarmSQL := `CREATE TABLE IF NOT EXISTS gb28181_alarms (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		device_id TEXT NOT NULL,
		channel_id TEXT DEFAULT '',
		alarm_type TEXT DEFAULT '',
		alarm_time DATETIME DEFAULT CURRENT_TIMESTAMP,
		priority INTEGER DEFAULT 0,
		method TEXT DEFAULT '',
		description TEXT DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);`
	if _, err := d.db.ExecContext(ctx, alarmSQL); err != nil {
		return err
	}
	return nil
}

func (d *DB) ListAlarms(ctx context.Context, deviceID string, limit, offset int) ([]AlarmRow, int, error) {
	countQuery := "SELECT COUNT(*) FROM gb28181_alarms WHERE 1=1"
	listQuery := `SELECT id, device_id, channel_id, alarm_type, alarm_time, priority, method, description, created_at
		FROM gb28181_alarms WHERE 1=1`
	args := []interface{}{}

	if deviceID != "" {
		countQuery += " AND device_id = ?"
		listQuery += " AND device_id = ?"
		args = append(args, deviceID)
	}

	var total int
	if err := d.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	listQuery += " ORDER BY created_at DESC LIMIT ? OFFSET ?"
	queryArgs := append(args, limit, offset)

	rows, err := d.db.QueryContext(ctx, listQuery, queryArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var alarms []AlarmRow
	for rows.Next() {
		var a AlarmRow
		if err := rows.Scan(&a.ID, &a.DeviceID, &a.ChannelID, &a.AlarmType, &a.AlarmTime,
			&a.Priority, &a.Method, &a.Description, &a.CreatedAt); err != nil {
			return nil, 0, err
		}
		alarms = append(alarms, a)
	}
	return alarms, total, nil
}

func (d *DB) CreateAlarm(ctx context.Context, a *AlarmRow) (int64, error) {
	res, err := d.db.ExecContext(ctx, `
		INSERT INTO gb28181_alarms (device_id, channel_id, alarm_type, alarm_time, priority, method, description)
		VALUES (?, ?, ?, ?, ?, ?, ?);`,
		a.DeviceID, a.ChannelID, a.AlarmType, a.AlarmTime, a.Priority, a.Method, a.Description)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}
