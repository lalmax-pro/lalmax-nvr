package storage

import (
	"context"
	"database/sql"
	"time"
)

type EmbyServer struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	BaseURL   string    `json:"base_url"`
	UserID    string    `json:"user_id"`
	Token     string    `json:"-"`
	UpdatedAt time.Time `json:"updated_at"`
}

type EmbyItem struct {
	ID             string    `json:"id"`
	EmbyItemID     string    `json:"emby_item_id"`
	Name           string    `json:"name"`
	ItemType       string    `json:"item_type"`
	Overview       string    `json:"overview,omitempty"`
	ImageTag       string    `json:"image_tag,omitempty"`
	RunTimeTicks   int64     `json:"run_time_ticks,omitempty"`
	StreamID       string    `json:"stream_id"`
	PublishEnabled bool      `json:"publish_enabled"`
	Loop           bool      `json:"loop"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func (d *DB) migrateEmby(ctx context.Context) error {
	if _, err := d.execContext(ctx, `CREATE TABLE IF NOT EXISTS emby_server (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL DEFAULT '',
		base_url TEXT NOT NULL,
		user_id TEXT NOT NULL,
		token TEXT NOT NULL,
		updated_at DATETIME NOT NULL
	);`); err != nil {
		return err
	}
	_, err := d.execContext(ctx, `CREATE TABLE IF NOT EXISTS emby_items (
		id TEXT PRIMARY KEY,
		emby_item_id TEXT NOT NULL UNIQUE,
		name TEXT NOT NULL,
		item_type TEXT NOT NULL DEFAULT '',
		overview TEXT NOT NULL DEFAULT '',
		image_tag TEXT NOT NULL DEFAULT '',
		run_time_ticks INTEGER NOT NULL DEFAULT 0,
		stream_id TEXT NOT NULL UNIQUE,
		publish_enabled INTEGER NOT NULL DEFAULT 0,
		loop INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL
	);`)
	if err != nil {
		return err
	}
	_, _ = d.execContext(ctx, "UPDATE schema_meta SET value='33' WHERE key='schema_version'")
	return nil
}

func (d *DB) GetEmbyServer(ctx context.Context) (*EmbyServer, error) {
	var s EmbyServer
	err := d.queryRowContext(ctx, `SELECT id, name, base_url, user_id, token, updated_at FROM emby_server LIMIT 1`).
		Scan(&s.ID, &s.Name, &s.BaseURL, &s.UserID, &s.Token, &s.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (d *DB) SaveEmbyServer(ctx context.Context, s EmbyServer) error {
	if s.UpdatedAt.IsZero() {
		s.UpdatedAt = time.Now()
	}
	_, err := d.execContext(ctx, `INSERT INTO emby_server(id,name,base_url,user_id,token,updated_at) VALUES(?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name,base_url=excluded.base_url,user_id=excluded.user_id,token=excluded.token,updated_at=excluded.updated_at`,
		s.ID, s.Name, s.BaseURL, s.UserID, s.Token, s.UpdatedAt)
	return err
}

func (d *DB) ListEmbyItems(ctx context.Context) ([]EmbyItem, error) {
	// LOWER() is available on SQLite, PostgreSQL and MySQL. SQLite's
	// COLLATE NOCASE is not portable and prevented Emby startup recovery.
	rows, err := d.queryContext(ctx, `SELECT id,emby_item_id,name,item_type,overview,image_tag,run_time_ticks,stream_id,publish_enabled,loop,created_at,updated_at FROM emby_items ORDER BY LOWER(name), name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]EmbyItem, 0)
	for rows.Next() {
		var i EmbyItem
		var enabled, loop int
		if err := rows.Scan(&i.ID, &i.EmbyItemID, &i.Name, &i.ItemType, &i.Overview, &i.ImageTag, &i.RunTimeTicks, &i.StreamID, &enabled, &loop, &i.CreatedAt, &i.UpdatedAt); err != nil {
			return nil, err
		}
		i.PublishEnabled, i.Loop = enabled != 0, loop != 0
		items = append(items, i)
	}
	return items, rows.Err()
}

func (d *DB) GetEmbyItem(ctx context.Context, id string) (*EmbyItem, error) {
	var i EmbyItem
	var enabled, loop int
	err := d.queryRowContext(ctx, `SELECT id,emby_item_id,name,item_type,overview,image_tag,run_time_ticks,stream_id,publish_enabled,loop,created_at,updated_at FROM emby_items WHERE id=? OR emby_item_id=?`, id, id).
		Scan(&i.ID, &i.EmbyItemID, &i.Name, &i.ItemType, &i.Overview, &i.ImageTag, &i.RunTimeTicks, &i.StreamID, &enabled, &loop, &i.CreatedAt, &i.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	i.PublishEnabled, i.Loop = enabled != 0, loop != 0
	return &i, nil
}

func (d *DB) SaveEmbyItem(ctx context.Context, i EmbyItem) error {
	now := time.Now()
	if i.CreatedAt.IsZero() {
		i.CreatedAt = now
	}
	i.UpdatedAt = now
	_, err := d.execContext(ctx, `INSERT INTO emby_items(id,emby_item_id,name,item_type,overview,image_tag,run_time_ticks,stream_id,publish_enabled,loop,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(emby_item_id) DO UPDATE SET name=excluded.name,item_type=excluded.item_type,overview=excluded.overview,image_tag=excluded.image_tag,run_time_ticks=excluded.run_time_ticks,updated_at=excluded.updated_at`,
		i.ID, i.EmbyItemID, i.Name, i.ItemType, i.Overview, i.ImageTag, i.RunTimeTicks, i.StreamID, boolToInt(i.PublishEnabled), boolToInt(i.Loop), i.CreatedAt, i.UpdatedAt)
	return err
}

func (d *DB) SetEmbyItemPublish(ctx context.Context, id string, enabled, loop bool) error {
	_, err := d.execContext(ctx, `UPDATE emby_items SET publish_enabled=?,loop=?,updated_at=? WHERE id=? OR emby_item_id=?`, boolToInt(enabled), boolToInt(loop), time.Now(), id, id)
	return err
}

func (d *DB) DeleteEmbyItem(ctx context.Context, id string) error {
	_, err := d.execContext(ctx, `DELETE FROM emby_items WHERE id=? OR emby_item_id=?`, id, id)
	return err
}
