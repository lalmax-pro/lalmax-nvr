package storage

import (
	"context"
	"time"
)

type DownloadRecordRow struct {
	ID        int64     `json:"id"`
	DeviceID  string    `json:"device_id"`
	ChannelID string    `json:"channel_id"`
	FilePath  string    `json:"file_path"`
	StartTime time.Time `json:"start_time"`
	EndTime   time.Time `json:"end_time"`
	FileSize  int64     `json:"file_size"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (d *DB) createDownloadTable(ctx context.Context) error {
	downloadSQL := `CREATE TABLE IF NOT EXISTS gb28181_downloads (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		device_id TEXT NOT NULL,
		channel_id TEXT NOT NULL,
		file_path TEXT DEFAULT '',
		start_time DATETIME,
		end_time DATETIME,
		file_size INTEGER DEFAULT 0,
		status TEXT DEFAULT 'pending',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);`
	if _, err := d.db.ExecContext(ctx, downloadSQL); err != nil {
		return err
	}
	return nil
}

func (d *DB) CreateDownload(ctx context.Context, dl *DownloadRecordRow) (int64, error) {
	res, err := d.db.ExecContext(ctx, `
		INSERT INTO gb28181_downloads (device_id, channel_id, file_path, start_time, end_time, status)
		VALUES (?, ?, ?, ?, ?, ?);`,
		dl.DeviceID, dl.ChannelID, dl.FilePath, dl.StartTime, dl.EndTime, dl.Status)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *DB) UpdateDownloadStatus(ctx context.Context, id int64, status string, fileSize int64) error {
	_, err := d.db.ExecContext(ctx, `
		UPDATE gb28181_downloads SET status=?, file_size=?, updated_at=CURRENT_TIMESTAMP WHERE id=?;`,
		status, fileSize, id)
	return err
}

func (d *DB) GetDownload(ctx context.Context, id int64) (*DownloadRecordRow, error) {
	var dl DownloadRecordRow
	err := d.db.QueryRowContext(ctx, `
		SELECT id, device_id, channel_id, file_path, start_time, end_time, file_size, status, created_at, updated_at
		FROM gb28181_downloads WHERE id = ?;`, id).Scan(
		&dl.ID, &dl.DeviceID, &dl.ChannelID, &dl.FilePath, &dl.StartTime, &dl.EndTime,
		&dl.FileSize, &dl.Status, &dl.CreatedAt, &dl.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &dl, nil
}

func (d *DB) ListDownloads(ctx context.Context, deviceID, channelID string, limit, offset int) ([]DownloadRecordRow, int, error) {
	countQuery := "SELECT COUNT(*) FROM gb28181_downloads WHERE 1=1"
	listQuery := `SELECT id, device_id, channel_id, file_path, start_time, end_time, file_size, status, created_at, updated_at
		FROM gb28181_downloads WHERE 1=1`
	args := []interface{}{}

	if deviceID != "" {
		countQuery += " AND device_id = ?"
		listQuery += " AND device_id = ?"
		args = append(args, deviceID)
	}
	if channelID != "" {
		countQuery += " AND channel_id = ?"
		listQuery += " AND channel_id = ?"
		args = append(args, channelID)
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

	var downloads []DownloadRecordRow
	for rows.Next() {
		var dl DownloadRecordRow
		if err := rows.Scan(&dl.ID, &dl.DeviceID, &dl.ChannelID, &dl.FilePath, &dl.StartTime,
			&dl.EndTime, &dl.FileSize, &dl.Status, &dl.CreatedAt, &dl.UpdatedAt); err != nil {
			return nil, 0, err
		}
		downloads = append(downloads, dl)
	}
	return downloads, total, nil
}
