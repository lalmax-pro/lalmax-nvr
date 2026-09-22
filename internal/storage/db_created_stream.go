package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const (
	// CreatedStreamPush waits for an external publisher and advertises ingest URLs.
	CreatedStreamPush = "push"
	// CreatedStreamPull is pulled into lalmax from SourceURL.
	CreatedStreamPull = "pull"
)

// CreatedStream is a stream created from the stream page.
// A push slot exists before anyone publishes, so the push URLs can be copied ahead of time.
// A pull slot records the source URL and is started through lalmax relay pull.
type CreatedStream struct {
	StreamID  string
	Name      string
	AppName   string
	InputMode string
	SourceURL string
	CreatedAt time.Time
}

func (d *DB) InsertCreatedStream(ctx context.Context, stream CreatedStream) error {
	if stream.AppName == "" {
		stream.AppName = "live"
	}
	if stream.CreatedAt.IsZero() {
		stream.CreatedAt = time.Now()
	}
	if stream.InputMode == "" {
		stream.InputMode = CreatedStreamPush
	}
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO created_streams(stream_id, name, app_name, input_mode, source_url, created_at) VALUES(?,?,?,?,?,?);`,
		stream.StreamID, stream.Name, stream.AppName, stream.InputMode, stream.SourceURL, timeToDB(stream.CreatedAt))
	return err
}

func (d *DB) GetCreatedStream(ctx context.Context, streamID string) (*CreatedStream, error) {
	row := d.db.QueryRowContext(ctx,
		`SELECT stream_id, name, app_name, input_mode, source_url, created_at FROM created_streams WHERE stream_id = ?;`,
		streamID)
	stream, err := scanCreatedStream(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &stream, nil
}

// SetCreatedStreamName updates the display name of a created stream.
// If the row does not exist, it inserts a push slot so unmanaged live streams
// can keep a display name after they go idle. Existing input_mode and source_url
// are preserved on conflict.
func (d *DB) SetCreatedStreamName(ctx context.Context, streamID, name string) error {
	streamID = strings.TrimSpace(streamID)
	if streamID == "" {
		return fmt.Errorf("stream_id is required")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = streamID
	}
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO created_streams(stream_id, name, app_name, input_mode, source_url, created_at)
		 VALUES(?,?,?,?,?,?)
		 ON CONFLICT(stream_id) DO UPDATE SET name=excluded.name;`,
		streamID, name, "live", CreatedStreamPush, "", timeToDB(time.Now()))
	return err
}

func (d *DB) ListCreatedStreams(ctx context.Context) ([]CreatedStream, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT stream_id, name, app_name, input_mode, source_url, created_at FROM created_streams ORDER BY created_at DESC;`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CreatedStream
	for rows.Next() {
		stream, err := scanCreatedStream(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, stream)
	}
	return out, rows.Err()
}

// DeleteCreatedStream removes a reserved push slot. The bool reports whether a row was deleted.
func (d *DB) DeleteCreatedStream(ctx context.Context, streamID string) (bool, error) {
	res, err := d.db.ExecContext(ctx, `DELETE FROM created_streams WHERE stream_id = ?;`, streamID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func IsUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE")
}

type createdStreamScanner interface {
	Scan(dest ...any) error
}

func scanCreatedStream(row createdStreamScanner) (CreatedStream, error) {
	var stream CreatedStream
	var createdAt sql.NullString
	if err := row.Scan(&stream.StreamID, &stream.Name, &stream.AppName, &stream.InputMode, &stream.SourceURL, &createdAt); err != nil {
		return CreatedStream{}, err
	}
	stream.CreatedAt = scanTime(createdAt)
	return stream, nil
}
