package storage

import (
	"context"
	"database/sql"
	"time"
)

// StreamHistory represents a stream publish session record.
type StreamHistory struct {
	ID           int64      `json:"id"`
	StreamID     string     `json:"stream_id"`
	AppName      string     `json:"app_name"`
	Protocol     string     `json:"protocol"`
	RemoteAddr   string     `json:"remote_addr"`
	SessionID    string     `json:"session_id"`
	StartedAt    time.Time  `json:"started_at"`
	EndedAt      *time.Time `json:"ended_at,omitempty"`
	DurationSec  float64    `json:"duration_sec"`
	BytesRead    uint64     `json:"bytes_read"`
	BytesWritten uint64     `json:"bytes_written"`
}

// StreamBan represents a banned stream.
type StreamBan struct {
	ID        int64      `json:"id"`
	StreamID  string     `json:"stream_id"`
	Reason    string     `json:"reason"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// InsertStreamHistory inserts a new stream history record (on pub start).
func (d *DB) InsertStreamHistory(ctx context.Context, h *StreamHistory) error {
	q := `INSERT INTO stream_history (stream_id, app_name, protocol, remote_addr, session_id, started_at)
	      VALUES (?, ?, ?, ?, ?, ?);`
	id, err := d.insertID(ctx, q, h.StreamID, h.AppName, h.Protocol, h.RemoteAddr, h.SessionID, timeToDB(h.StartedAt))
	if err != nil {
		return err
	}
	h.ID = id
	return nil
}

// FinishStreamHistory updates a stream history record on pub stop.
// Matches by session_id.
func (d *DB) FinishStreamHistory(ctx context.Context, sessionID string, endedAt time.Time, bytesRead, bytesWritten uint64) error {
	var startedAt time.Time
	err := d.queryRowContext(ctx,
		`SELECT started_at FROM stream_history WHERE session_id = ? AND ended_at IS NULL ORDER BY id DESC LIMIT 1;`,
		sessionID).Scan(parseTimeScan(&startedAt))
	if err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	}

	duration := endedAt.Sub(startedAt).Seconds()
	q := `UPDATE stream_history SET ended_at = ?, duration_sec = ?, bytes_read = ?, bytes_written = ?
	      WHERE session_id = ? AND ended_at IS NULL;`
	_, err = d.execContext(ctx, q, timeToDB(endedAt), duration, bytesRead, bytesWritten, sessionID)
	return err
}

// ListStreamHistory returns stream history records, optionally filtered by stream_id.
func (d *DB) ListStreamHistory(ctx context.Context, streamID string, limit, offset int) ([]StreamHistory, int, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	var countQ, listQ string
	var countArgs, listArgs []any

	if streamID != "" {
		countQ = `SELECT COUNT(*) FROM stream_history WHERE stream_id = ?;`
		countArgs = []any{streamID}
		listQ = `SELECT id, stream_id, app_name, protocol, remote_addr, session_id, started_at, ended_at, duration_sec, bytes_read, bytes_written
		         FROM stream_history WHERE stream_id = ? ORDER BY id DESC LIMIT ? OFFSET ?;`
		listArgs = []any{streamID, limit, offset}
	} else {
		countQ = `SELECT COUNT(*) FROM stream_history;`
		listQ = `SELECT id, stream_id, app_name, protocol, remote_addr, session_id, started_at, ended_at, duration_sec, bytes_read, bytes_written
		         FROM stream_history ORDER BY id DESC LIMIT ? OFFSET ?;`
		listArgs = []any{limit, offset}
	}

	var total int
	if err := d.queryRowContext(ctx, countQ, countArgs...).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := d.queryContext(ctx, listQ, listArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var items []StreamHistory
	for rows.Next() {
		var h StreamHistory
		var endedAt sql.NullTime
		if err := rows.Scan(&h.ID, &h.StreamID, &h.AppName, &h.Protocol, &h.RemoteAddr, &h.SessionID,
			parseTimeScan(&h.StartedAt), &endedAt, &h.DurationSec, &h.BytesRead, &h.BytesWritten); err != nil {
			return nil, 0, err
		}
		if endedAt.Valid {
			t := endedAt.Time
			h.EndedAt = &t
		}
		items = append(items, h)
	}
	return items, total, nil
}

// RecentStreamSnapshot is the latest session snapshot per stream_id within a time window.
type RecentStreamSnapshot struct {
	StreamID   string
	AppName    string
	Protocol   string
	RemoteAddr string
	StartedAt  time.Time
	EndedAt    *time.Time
}

// ListRecentStreamSnapshots returns the most recent history record per stream_id
// where started_at is on or after since, ordered by last activity descending.
func (d *DB) ListRecentStreamSnapshots(ctx context.Context, since time.Time, limit int) ([]RecentStreamSnapshot, error) {
	if limit <= 0 {
		limit = 200
	}
	q := `SELECT h.stream_id, h.app_name, h.protocol, h.remote_addr, h.started_at, h.ended_at
	      FROM stream_history h
	      INNER JOIN (
	        SELECT stream_id, MAX(id) AS max_id
	        FROM stream_history
	        WHERE started_at >= ?
	        GROUP BY stream_id
	      ) latest ON h.id = latest.max_id
	      ORDER BY COALESCE(h.ended_at, h.started_at) DESC
	      LIMIT ?;`
	rows, err := d.queryContext(ctx, q, timeToDB(since), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []RecentStreamSnapshot
	for rows.Next() {
		var snap RecentStreamSnapshot
		var endedAt sql.NullTime
		if err := rows.Scan(&snap.StreamID, &snap.AppName, &snap.Protocol, &snap.RemoteAddr,
			parseTimeScan(&snap.StartedAt), &endedAt); err != nil {
			return nil, err
		}
		if endedAt.Valid {
			t := endedAt.Time
			snap.EndedAt = &t
		}
		items = append(items, snap)
	}
	return items, nil
}

// DeleteStreamHistory deletes all history records for a stream.
func (d *DB) DeleteStreamHistory(ctx context.Context, streamID string) error {
	_, err := d.execContext(ctx, `DELETE FROM stream_history WHERE stream_id = ?;`, streamID)
	return err
}

// InsertStreamBan inserts a new stream ban.
func (d *DB) InsertStreamBan(ctx context.Context, ban *StreamBan) error {
	q := `INSERT INTO stream_bans (stream_id, reason, created_at, expires_at) VALUES (?, ?, ?, ?);`
	id, err := d.insertID(ctx, q, ban.StreamID, ban.Reason, timeToDB(ban.CreatedAt), timeToDBPtr(ban.ExpiresAt))
	if err != nil {
		return err
	}
	ban.ID = id
	return nil
}

// DeleteStreamBan removes a ban for a stream.
func (d *DB) DeleteStreamBan(ctx context.Context, streamID string) error {
	res, err := d.execContext(ctx, `DELETE FROM stream_bans WHERE stream_id = ?;`, streamID)
	if err != nil {
		return err
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// GetStreamBan returns the ban for a stream, or nil if not banned.
// Also checks expiration.
func (d *DB) GetStreamBan(ctx context.Context, streamID string) (*StreamBan, error) {
	var ban StreamBan
	var expiresAt sql.NullTime
	err := d.queryRowContext(ctx,
		`SELECT id, stream_id, reason, created_at, expires_at FROM stream_bans WHERE stream_id = ?;`,
		streamID).Scan(&ban.ID, &ban.StreamID, &ban.Reason, parseTimeScan(&ban.CreatedAt), &expiresAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if expiresAt.Valid {
		t := expiresAt.Time
		ban.ExpiresAt = &t
		// Check if ban has expired
		if time.Now().After(t) {
			// Auto-delete expired ban
			_, _ = d.execContext(ctx, `DELETE FROM stream_bans WHERE id = ?;`, ban.ID)
			return nil, nil
		}
	}
	return &ban, nil
}

// ListStreamBans returns all active (non-expired) bans.
func (d *DB) ListStreamBans(ctx context.Context) ([]StreamBan, error) {
	rows, err := d.queryContext(ctx,
		`SELECT id, stream_id, reason, created_at, expires_at FROM stream_bans ORDER BY created_at DESC;`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []StreamBan
	now := time.Now()
	for rows.Next() {
		var ban StreamBan
		var expiresAt sql.NullTime
		if err := rows.Scan(&ban.ID, &ban.StreamID, &ban.Reason, parseTimeScan(&ban.CreatedAt), &expiresAt); err != nil {
			return nil, err
		}
		if expiresAt.Valid {
			t := expiresAt.Time
			ban.ExpiresAt = &t
			if now.After(t) {
				continue // skip expired
			}
		}
		items = append(items, ban)
	}
	return items, nil
}

// IsStreamBanned checks if a stream is currently banned.
func (d *DB) IsStreamBanned(ctx context.Context, streamID string) (bool, error) {
	ban, err := d.GetStreamBan(ctx, streamID)
	if err != nil {
		return false, err
	}
	return ban != nil, nil
}

func timeToDBPtr(t *time.Time) interface{} {
	if t == nil || t.IsZero() {
		return nil
	}
	return t.UTC().Format(sqliteTimeFormat)
}

func parseTimeScan(dest *time.Time) *timeScanner {
	return &timeScanner{dest: dest}
}

type timeScanner struct {
	dest *time.Time
}

func (s *timeScanner) Scan(src interface{}) error {
	if src == nil {
		*s.dest = time.Time{}
		return nil
	}
	switch v := src.(type) {
	case string:
		t, err := parseTimeValue(v)
		if err != nil {
			return err
		}
		*s.dest = t
	case time.Time:
		*s.dest = v
	}
	return nil
}

func parseTimeValue(s string) (time.Time, error) {
	formats := []string{
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05.999999",
		"2006-01-02 15:04:05.999",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05.999999999Z",
		"2006-01-02T15:04:05Z",
		time.RFC3339,
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, nil
}
