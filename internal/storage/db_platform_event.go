package storage

import (
	"context"
	"time"
)

// PlatformEventType represents the type of platform event.
type PlatformEventType string

const (
	PlatformEventRegister    PlatformEventType = "register"
	PlatformEventUnregister  PlatformEventType = "unregister"
	PlatformEventKeepAlive   PlatformEventType = "keepalive"
	PlatformEventOnline      PlatformEventType = "online"
	PlatformEventOffline     PlatformEventType = "offline"
	PlatformEventStreamStart PlatformEventType = "stream_start"
	PlatformEventStreamStop  PlatformEventType = "stream_stop"
)

// PlatformEventRow represents a platform event record.
type PlatformEventRow struct {
	ID           int64             `json:"id"`
	PlatformID   int64             `json:"platform_id"`
	PlatformName string            `json:"platform_name"`
	EventType    PlatformEventType `json:"event_type"`
	ServerIP     string            `json:"server_ip"`
	ServerPort   int               `json:"server_port"`
	ChannelID    string            `json:"channel_id,omitempty"`
	StreamID     string            `json:"stream_id,omitempty"`
	Details      string            `json:"details,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
}

// platformEventsSQL creates the platform_events table.
const platformEventsSQL = `CREATE TABLE IF NOT EXISTS platform_events (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	platform_id INTEGER NOT NULL,
	platform_name TEXT NOT NULL DEFAULT '',
	event_type TEXT NOT NULL,
	server_ip TEXT NOT NULL DEFAULT '',
	server_port INTEGER NOT NULL DEFAULT 0,
	channel_id TEXT NOT NULL DEFAULT '',
	stream_id TEXT NOT NULL DEFAULT '',
	details TEXT NOT NULL DEFAULT '',
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
)`

func (db *DB) initPlatformEvents() error {
	_, err := db.db.Exec(platformEventsSQL)
	if err != nil {
		return err
	}
	// Index for faster queries
	_, err = db.db.Exec(`CREATE INDEX IF NOT EXISTS idx_platform_events_platform_id ON platform_events(platform_id)`)
	if err != nil {
		return err
	}
	_, err = db.db.Exec(`CREATE INDEX IF NOT EXISTS idx_platform_events_created_at ON platform_events(created_at)`)
	return err
}

// AddPlatformEvent adds a new platform event record.
func (db *DB) AddPlatformEvent(ctx context.Context, event PlatformEventRow) error {
	_, err := db.db.ExecContext(ctx,
		`INSERT INTO platform_events (platform_id, platform_name, event_type, server_ip, server_port, channel_id, stream_id, details)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		event.PlatformID, event.PlatformName, event.EventType,
		event.ServerIP, event.ServerPort, event.ChannelID, event.StreamID, event.Details)
	return err
}

// ListPlatformEvents returns platform events with pagination.
func (db *DB) ListPlatformEvents(ctx context.Context, platformID int64, eventType string, limit, offset int) ([]PlatformEventRow, int, error) {
	countQuery := "SELECT COUNT(*) FROM platform_events WHERE 1=1"
	query := "SELECT id, platform_id, platform_name, event_type, server_ip, server_port, channel_id, stream_id, details, created_at FROM platform_events WHERE 1=1"

	var args []interface{}
	if platformID > 0 {
		countQuery += " AND platform_id = ?"
		query += " AND platform_id = ?"
		args = append(args, platformID)
	}
	if eventType != "" {
		countQuery += " AND event_type = ?"
		query += " AND event_type = ?"
		args = append(args, eventType)
	}

	var total int
	err := db.db.QueryRowContext(ctx, countQuery, args...).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	query += " ORDER BY created_at DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := db.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var events []PlatformEventRow
	for rows.Next() {
		var e PlatformEventRow
		err := rows.Scan(&e.ID, &e.PlatformID, &e.PlatformName, &e.EventType,
			&e.ServerIP, &e.ServerPort, &e.ChannelID, &e.StreamID, &e.Details, &e.CreatedAt)
		if err != nil {
			return nil, 0, err
		}
		events = append(events, e)
	}
	if events == nil {
		events = []PlatformEventRow{}
	}
	return events, total, nil
}

// GetPlatformStatus returns the current status of all platforms.
func (db *DB) GetPlatformStatus(ctx context.Context) ([]PlatformStatusRow, error) {
	rows, err := db.db.QueryContext(ctx, `
		SELECT 
			p.id,
			p.name,
			p.server_ip,
			p.server_port,
			p.enable,
			COALESCE(e.event_type, '') as last_event_type,
			COALESCE(e.created_at, p.created_at) as last_event_time
		FROM gb28181_platforms p
		LEFT JOIN (
			SELECT platform_id, event_type, created_at,
				   ROW_NUMBER() OVER (PARTITION BY platform_id ORDER BY created_at DESC) as rn
			FROM platform_events
		) e ON e.platform_id = p.id AND e.rn = 1
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var statuses []PlatformStatusRow
	for rows.Next() {
		var s PlatformStatusRow
		err := rows.Scan(&s.PlatformID, &s.PlatformName, &s.ServerIP, &s.ServerPort,
			&s.Enable, &s.LastEventType, &s.LastEventTime)
		if err != nil {
			return nil, err
		}
		s.IsOnline = s.LastEventType == string(PlatformEventOnline) || s.LastEventType == string(PlatformEventRegister)
		statuses = append(statuses, s)
	}
	if statuses == nil {
		statuses = []PlatformStatusRow{}
	}
	return statuses, nil
}

// PlatformStatusRow represents the current status of a platform.
type PlatformStatusRow struct {
	PlatformID    int64     `json:"platform_id"`
	PlatformName  string    `json:"platform_name"`
	ServerIP      string    `json:"server_ip"`
	ServerPort    int       `json:"server_port"`
	Enable        bool      `json:"enable"`
	IsOnline      bool      `json:"is_online"`
	LastEventType string    `json:"last_event_type"`
	LastEventTime time.Time `json:"last_event_time"`
}
