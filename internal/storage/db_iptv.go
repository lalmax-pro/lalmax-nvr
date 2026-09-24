package storage

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

type IPTVSource struct {
	ID                 string     `json:"id"`
	Name               string     `json:"name"`
	PlaylistURL        string     `json:"playlist_url"`
	RequestHeaders     string     `json:"-"`
	Enabled            bool       `json:"enabled"`
	RefreshIntervalSec int        `json:"refresh_interval_sec"`
	LastRefreshedAt    *time.Time `json:"last_refreshed_at,omitempty"`
	LastError          string     `json:"last_error,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

type IPTVChannel struct {
	ID             string     `json:"id"`
	SourceID       string     `json:"source_id"`
	ExternalID     string     `json:"external_id"`
	StreamID       string     `json:"stream_id"`
	ChannelNo      string     `json:"channel_no,omitempty"`
	Name           string     `json:"name"`
	GroupName      string     `json:"group_name,omitempty"`
	LogoURL        string     `json:"logo_url,omitempty"`
	SourceURL      string     `json:"-"`
	Enabled        bool       `json:"enabled"`
	Favorite       bool       `json:"favorite"`
	ProbeStatus    string     `json:"probe_status"`
	ProbeError     string     `json:"probe_error,omitempty"`
	VideoCodec     string     `json:"video_codec,omitempty"`
	AudioCodec     string     `json:"audio_codec,omitempty"`
	Playable       bool       `json:"playable"`
	Recordable     bool       `json:"recordable"`
	PublishEnabled bool       `json:"publish_enabled"`
	LastCheckedAt  *time.Time `json:"last_checked_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	RequestHeaders string     `json:"-"`
}

type IPTVImportJob struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	PlaylistURL    string    `json:"playlist_url,omitempty"`
	RequestHeaders string    `json:"-"`
	Status         string    `json:"status"`
	Error          string    `json:"error,omitempty"`
	TotalItems     int       `json:"total_items"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type IPTVImportItem struct {
	ID             string     `json:"id"`
	JobID          string     `json:"job_id"`
	RowNo          int        `json:"row_no"`
	ExternalID     string     `json:"external_id"`
	Name           string     `json:"name"`
	GroupName      string     `json:"group_name,omitempty"`
	LogoURL        string     `json:"logo_url,omitempty"`
	ChannelNo      string     `json:"channel_no,omitempty"`
	SourceURL      string     `json:"-"`
	Status         string     `json:"status"`
	Error          string     `json:"error,omitempty"`
	HTTPStatus     int        `json:"http_status,omitempty"`
	ContentType    string     `json:"content_type,omitempty"`
	VideoCodec     string     `json:"video_codec,omitempty"`
	AudioCodec     string     `json:"audio_codec,omitempty"`
	Encrypted      bool       `json:"encrypted"`
	DRM            bool       `json:"drm"`
	Playable       bool       `json:"playable"`
	Recordable     bool       `json:"recordable"`
	CheckedAt      *time.Time `json:"checked_at,omitempty"`
	RequestHeaders string     `json:"-"`
}

func (d *DB) migrateIPTV(ctx context.Context) error {
	sourceSQL := `CREATE TABLE IF NOT EXISTS iptv_sources (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		playlist_url TEXT NOT NULL,
		request_headers TEXT NOT NULL DEFAULT '',
		enabled INTEGER NOT NULL DEFAULT 1,
		refresh_interval_sec INTEGER NOT NULL DEFAULT 21600,
		last_refreshed_at DATETIME,
		last_error TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL
	);`
	if _, err := d.db.ExecContext(ctx, sourceSQL); err != nil {
		return err
	}
	channelSQL := `CREATE TABLE IF NOT EXISTS iptv_channels (
		id TEXT PRIMARY KEY,
		source_id TEXT NOT NULL,
		external_id TEXT NOT NULL,
		stream_id TEXT NOT NULL UNIQUE,
		channel_no TEXT NOT NULL DEFAULT '',
		name TEXT NOT NULL,
		group_name TEXT NOT NULL DEFAULT '',
		logo_url TEXT NOT NULL DEFAULT '',
		source_url TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		favorite INTEGER NOT NULL DEFAULT 0,
		probe_status TEXT NOT NULL DEFAULT '',
		probe_error TEXT NOT NULL DEFAULT '',
		video_codec TEXT NOT NULL DEFAULT '',
		audio_codec TEXT NOT NULL DEFAULT '',
		playable INTEGER NOT NULL DEFAULT 0,
		recordable INTEGER NOT NULL DEFAULT 0,
		publish_enabled INTEGER NOT NULL DEFAULT 0,
		last_checked_at DATETIME,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL,
		UNIQUE(source_id, external_id)
	);`
	if _, err := d.db.ExecContext(ctx, channelSQL); err != nil {
		return err
	}
	jobSQL := `CREATE TABLE IF NOT EXISTS iptv_import_jobs (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL DEFAULT '',
		playlist_url TEXT NOT NULL DEFAULT '',
		request_headers TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL,
		error TEXT NOT NULL DEFAULT '',
		total_items INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL
	);`
	if _, err := d.db.ExecContext(ctx, jobSQL); err != nil {
		return err
	}
	itemSQL := `CREATE TABLE IF NOT EXISTS iptv_import_items (
		id TEXT PRIMARY KEY,
		job_id TEXT NOT NULL,
		row_no INTEGER NOT NULL DEFAULT 0,
		external_id TEXT NOT NULL DEFAULT '',
		name TEXT NOT NULL,
		group_name TEXT NOT NULL DEFAULT '',
		logo_url TEXT NOT NULL DEFAULT '',
		channel_no TEXT NOT NULL DEFAULT '',
		source_url TEXT NOT NULL,
		status TEXT NOT NULL,
		error TEXT NOT NULL DEFAULT '',
		http_status INTEGER NOT NULL DEFAULT 0,
		content_type TEXT NOT NULL DEFAULT '',
		video_codec TEXT NOT NULL DEFAULT '',
		audio_codec TEXT NOT NULL DEFAULT '',
		encrypted INTEGER NOT NULL DEFAULT 0,
		drm INTEGER NOT NULL DEFAULT 0,
		playable INTEGER NOT NULL DEFAULT 0,
		recordable INTEGER NOT NULL DEFAULT 0,
		checked_at DATETIME
	);`
	if _, err := d.db.ExecContext(ctx, itemSQL); err != nil {
		return err
	}
	_, _ = d.db.ExecContext(ctx, `ALTER TABLE iptv_import_items ADD COLUMN request_headers TEXT NOT NULL DEFAULT ''`)
	_, _ = d.db.ExecContext(ctx, `ALTER TABLE iptv_channels ADD COLUMN request_headers TEXT NOT NULL DEFAULT ''`)
	_, _ = d.db.ExecContext(ctx, `ALTER TABLE iptv_channels ADD COLUMN publish_enabled INTEGER NOT NULL DEFAULT 0`)
	_, _ = d.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_iptv_channels_source ON iptv_channels(source_id, group_name)`)
	_, _ = d.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_iptv_import_items_job ON iptv_import_items(job_id, row_no)`)
	_, _ = d.db.ExecContext(ctx, "UPDATE schema_meta SET value='32' WHERE key='schema_version'")
	return nil
}

func (d *DB) InsertIPTVImportJob(ctx context.Context, job IPTVImportJob) error {
	if job.CreatedAt.IsZero() {
		job.CreatedAt = time.Now()
	}
	if job.UpdatedAt.IsZero() {
		job.UpdatedAt = job.CreatedAt
	}
	_, err := d.db.ExecContext(ctx, `INSERT INTO iptv_import_jobs(id, name, playlist_url, request_headers, status, error, total_items, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?)`, job.ID, job.Name, job.PlaylistURL, job.RequestHeaders, job.Status, job.Error, job.TotalItems, timeToDB(job.CreatedAt), timeToDB(job.UpdatedAt))
	return err
}

func (d *DB) UpdateIPTVImportJob(ctx context.Context, job IPTVImportJob) error {
	job.UpdatedAt = time.Now()
	_, err := d.db.ExecContext(ctx, `UPDATE iptv_import_jobs SET name=?, status=?, error=?, total_items=?, updated_at=? WHERE id=?`,
		job.Name, job.Status, job.Error, job.TotalItems, timeToDB(job.UpdatedAt), job.ID)
	return err
}

func (d *DB) GetIPTVImportJob(ctx context.Context, id string) (*IPTVImportJob, error) {
	row := d.db.QueryRowContext(ctx, `SELECT id, name, playlist_url, request_headers, status, error, total_items, created_at, updated_at FROM iptv_import_jobs WHERE id=?`, id)
	var job IPTVImportJob
	var created, updated sql.NullString
	err := row.Scan(&job.ID, &job.Name, &job.PlaylistURL, &job.RequestHeaders, &job.Status, &job.Error, &job.TotalItems, &created, &updated)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	job.CreatedAt = scanTime(created)
	job.UpdatedAt = scanTime(updated)
	return &job, nil
}

func (d *DB) InsertIPTVImportItems(ctx context.Context, items []IPTVImportItem) error {
	if len(items) == 0 {
		return nil
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO iptv_import_items(id, job_id, row_no, external_id, name, group_name, logo_url, channel_no, source_url, status, error, http_status, content_type, video_codec, audio_codec, encrypted, drm, playable, recordable, checked_at, request_headers)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, item := range items {
		if _, err := stmt.ExecContext(ctx, item.ID, item.JobID, item.RowNo, item.ExternalID, item.Name, item.GroupName, item.LogoURL, item.ChannelNo, item.SourceURL, item.Status, item.Error, item.HTTPStatus, item.ContentType, item.VideoCodec, item.AudioCodec, boolToInt(item.Encrypted), boolToInt(item.DRM), boolToInt(item.Playable), boolToInt(item.Recordable), timeToDBPtr(item.CheckedAt), item.RequestHeaders); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (d *DB) UpdateIPTVImportItem(ctx context.Context, item IPTVImportItem) error {
	_, err := d.db.ExecContext(ctx, `UPDATE iptv_import_items SET status=?, error=?, http_status=?, content_type=?, video_codec=?, audio_codec=?, encrypted=?, drm=?, playable=?, recordable=?, checked_at=? WHERE id=?`,
		item.Status, item.Error, item.HTTPStatus, item.ContentType, item.VideoCodec, item.AudioCodec, boolToInt(item.Encrypted), boolToInt(item.DRM), boolToInt(item.Playable), boolToInt(item.Recordable), timeToDBPtr(item.CheckedAt), item.ID)
	return err
}

func (d *DB) ListIPTVImportItems(ctx context.Context, jobID, status, q string) ([]IPTVImportItem, error) {
	query := `SELECT id, job_id, row_no, external_id, name, group_name, logo_url, channel_no, source_url, status, error, http_status, content_type, video_codec, audio_codec, encrypted, drm, playable, recordable, checked_at, request_headers FROM iptv_import_items WHERE job_id=?`
	args := []any{jobID}
	if status != "" {
		query += ` AND status=?`
		args = append(args, status)
	}
	if q = strings.TrimSpace(q); q != "" {
		query += ` AND (name LIKE ? ESCAPE '\' OR group_name LIKE ? ESCAPE '\' OR external_id LIKE ? ESCAPE '\')`
		like := "%" + escapeLike(q) + "%"
		args = append(args, like, like, like)
	}
	query += ` ORDER BY row_no ASC`
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IPTVImportItem
	for rows.Next() {
		item, err := scanIPTVImportItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (d *DB) GetIPTVImportItem(ctx context.Context, id string) (*IPTVImportItem, error) {
	row := d.db.QueryRowContext(ctx, `SELECT id, job_id, row_no, external_id, name, group_name, logo_url, channel_no, source_url, status, error, http_status, content_type, video_codec, audio_codec, encrypted, drm, playable, recordable, checked_at, request_headers FROM iptv_import_items WHERE id=?`, id)
	item, err := scanIPTVImportItem(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func (d *DB) GetIPTVImportItemsByIDs(ctx context.Context, jobID string, ids []string) ([]IPTVImportItem, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(ids))
	args := []any{jobID}
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	query := `SELECT id, job_id, row_no, external_id, name, group_name, logo_url, channel_no, source_url, status, error, http_status, content_type, video_codec, audio_codec, encrypted, drm, playable, recordable, checked_at, request_headers FROM iptv_import_items WHERE job_id=? AND id IN (` + strings.Join(placeholders, ",") + `)`
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IPTVImportItem
	for rows.Next() {
		item, err := scanIPTVImportItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (d *DB) InsertIPTVSource(ctx context.Context, src IPTVSource) error {
	now := time.Now()
	if src.CreatedAt.IsZero() {
		src.CreatedAt = now
	}
	if src.UpdatedAt.IsZero() {
		src.UpdatedAt = now
	}
	_, err := d.db.ExecContext(ctx, `INSERT INTO iptv_sources(id, name, playlist_url, request_headers, enabled, refresh_interval_sec, last_refreshed_at, last_error, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, src.ID, src.Name, src.PlaylistURL, src.RequestHeaders, boolToInt(src.Enabled), src.RefreshIntervalSec, timeToDBPtr(src.LastRefreshedAt), src.LastError, timeToDB(src.CreatedAt), timeToDB(src.UpdatedAt))
	return err
}

func (d *DB) ListIPTVSources(ctx context.Context) ([]IPTVSource, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT id, name, playlist_url, request_headers, enabled, refresh_interval_sec, last_refreshed_at, last_error, created_at, updated_at FROM iptv_sources ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IPTVSource
	for rows.Next() {
		src, err := scanIPTVSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

func (d *DB) GetIPTVSource(ctx context.Context, id string) (*IPTVSource, error) {
	row := d.db.QueryRowContext(ctx, `SELECT id, name, playlist_url, request_headers, enabled, refresh_interval_sec, last_refreshed_at, last_error, created_at, updated_at FROM iptv_sources WHERE id=?`, id)
	src, err := scanIPTVSource(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &src, nil
}

func (d *DB) DeleteIPTVSource(ctx context.Context, id string) error {
	if _, err := d.db.ExecContext(ctx, `DELETE FROM iptv_channels WHERE source_id=?`, id); err != nil {
		return err
	}
	_, err := d.db.ExecContext(ctx, `DELETE FROM iptv_sources WHERE id=?`, id)
	return err
}

func (d *DB) UpsertIPTVChannel(ctx context.Context, ch IPTVChannel) error {
	now := time.Now()
	if ch.CreatedAt.IsZero() {
		ch.CreatedAt = now
	}
	ch.UpdatedAt = now
	_, err := d.db.ExecContext(ctx, `INSERT INTO iptv_channels(id, source_id, external_id, stream_id, channel_no, name, group_name, logo_url, source_url, enabled, favorite, probe_status, probe_error, video_codec, audio_codec, playable, recordable, publish_enabled, last_checked_at, created_at, updated_at, request_headers)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(source_id, external_id) DO UPDATE SET
			name=excluded.name, group_name=excluded.group_name, logo_url=excluded.logo_url, channel_no=excluded.channel_no,
			source_url=excluded.source_url, probe_status=excluded.probe_status, probe_error=excluded.probe_error,
			video_codec=excluded.video_codec, audio_codec=excluded.audio_codec, playable=excluded.playable,
			recordable=excluded.recordable, last_checked_at=excluded.last_checked_at, updated_at=excluded.updated_at,
			request_headers=excluded.request_headers`,
		ch.ID, ch.SourceID, ch.ExternalID, ch.StreamID, ch.ChannelNo, ch.Name, ch.GroupName, ch.LogoURL, ch.SourceURL, boolToInt(ch.Enabled), boolToInt(ch.Favorite), ch.ProbeStatus, ch.ProbeError, ch.VideoCodec, ch.AudioCodec, boolToInt(ch.Playable), boolToInt(ch.Recordable), boolToInt(ch.PublishEnabled), timeToDBPtr(ch.LastCheckedAt), timeToDB(ch.CreatedAt), timeToDB(ch.UpdatedAt), ch.RequestHeaders)
	return err
}

func (d *DB) ListIPTVChannels(ctx context.Context, sourceID, group, q string, favorite *bool) ([]IPTVChannel, error) {
	query := `SELECT id, source_id, external_id, stream_id, channel_no, name, group_name, logo_url, source_url, enabled, favorite, probe_status, probe_error, video_codec, audio_codec, playable, recordable, publish_enabled, last_checked_at, created_at, updated_at, request_headers FROM iptv_channels WHERE 1=1`
	var args []any
	if sourceID != "" {
		query += ` AND source_id=?`
		args = append(args, sourceID)
	}
	if group != "" {
		query += ` AND group_name=?`
		args = append(args, group)
	}
	if favorite != nil {
		query += ` AND favorite=?`
		args = append(args, boolToInt(*favorite))
	}
	if q = strings.TrimSpace(q); q != "" {
		query += ` AND (name LIKE ? ESCAPE '\' OR group_name LIKE ? ESCAPE '\' OR external_id LIKE ? ESCAPE '\')`
		like := "%" + escapeLike(q) + "%"
		args = append(args, like, like, like)
	}
	query += ` ORDER BY group_name, name`
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IPTVChannel
	for rows.Next() {
		ch, err := scanIPTVChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ch)
	}
	return out, rows.Err()
}

func (d *DB) GetIPTVChannel(ctx context.Context, id string) (*IPTVChannel, error) {
	row := d.db.QueryRowContext(ctx, `SELECT id, source_id, external_id, stream_id, channel_no, name, group_name, logo_url, source_url, enabled, favorite, probe_status, probe_error, video_codec, audio_codec, playable, recordable, publish_enabled, last_checked_at, created_at, updated_at, request_headers FROM iptv_channels WHERE id=?`, id)
	ch, err := scanIPTVChannel(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ch, nil
}

func (d *DB) UpdateIPTVChannelMeta(ctx context.Context, id, name string, enabled, favorite, publishEnabled bool) error {
	_, err := d.db.ExecContext(ctx, `UPDATE iptv_channels SET name=?, enabled=?, favorite=?, publish_enabled=?, updated_at=? WHERE id=?`,
		name, boolToInt(enabled), boolToInt(favorite), boolToInt(publishEnabled), timeToDB(time.Now()), id)
	return err
}

func (d *DB) DeleteIPTVChannel(ctx context.Context, id string) error {
	_, err := d.db.ExecContext(ctx, `DELETE FROM iptv_channels WHERE id=?`, id)
	return err
}

func (d *DB) ListIPTVGroups(ctx context.Context) ([]string, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT DISTINCT group_name FROM iptv_channels WHERE group_name != '' ORDER BY group_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

type iptvRowScanner interface {
	Scan(dest ...any) error
}

func scanIPTVImportItem(row iptvRowScanner) (IPTVImportItem, error) {
	var item IPTVImportItem
	var checked sql.NullString
	var encrypted, drm, playable, recordable int
	err := row.Scan(&item.ID, &item.JobID, &item.RowNo, &item.ExternalID, &item.Name, &item.GroupName, &item.LogoURL, &item.ChannelNo, &item.SourceURL, &item.Status, &item.Error, &item.HTTPStatus, &item.ContentType, &item.VideoCodec, &item.AudioCodec, &encrypted, &drm, &playable, &recordable, &checked, &item.RequestHeaders)
	if err != nil {
		return IPTVImportItem{}, err
	}
	item.Encrypted = encrypted != 0
	item.DRM = drm != 0
	item.Playable = playable != 0
	item.Recordable = recordable != 0
	if checked.Valid && checked.String != "" {
		t := scanTime(checked)
		item.CheckedAt = &t
	}
	return item, nil
}

func scanIPTVSource(row iptvRowScanner) (IPTVSource, error) {
	var src IPTVSource
	var lastRef, created, updated sql.NullString
	var enabled int
	err := row.Scan(&src.ID, &src.Name, &src.PlaylistURL, &src.RequestHeaders, &enabled, &src.RefreshIntervalSec, &lastRef, &src.LastError, &created, &updated)
	if err != nil {
		return IPTVSource{}, err
	}
	src.Enabled = enabled != 0
	src.CreatedAt = scanTime(created)
	src.UpdatedAt = scanTime(updated)
	if lastRef.Valid && lastRef.String != "" {
		t := scanTime(lastRef)
		src.LastRefreshedAt = &t
	}
	return src, nil
}

func scanIPTVChannel(row iptvRowScanner) (IPTVChannel, error) {
	var ch IPTVChannel
	var lastChecked, created, updated sql.NullString
	var enabled, favorite, playable, recordable int
	var publishEnabled int
	err := row.Scan(&ch.ID, &ch.SourceID, &ch.ExternalID, &ch.StreamID, &ch.ChannelNo, &ch.Name, &ch.GroupName, &ch.LogoURL, &ch.SourceURL, &enabled, &favorite, &ch.ProbeStatus, &ch.ProbeError, &ch.VideoCodec, &ch.AudioCodec, &playable, &recordable, &publishEnabled, &lastChecked, &created, &updated, &ch.RequestHeaders)
	if err != nil {
		return IPTVChannel{}, err
	}
	ch.Enabled = enabled != 0
	ch.Favorite = favorite != 0
	ch.Playable = playable != 0
	ch.Recordable = recordable != 0
	ch.PublishEnabled = publishEnabled != 0
	ch.CreatedAt = scanTime(created)
	ch.UpdatedAt = scanTime(updated)
	if lastChecked.Valid && lastChecked.String != "" {
		t := scanTime(lastChecked)
		ch.LastCheckedAt = &t
	}
	return ch, nil
}
