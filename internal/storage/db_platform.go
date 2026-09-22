package storage

import (
	"context"
	"time"
)

type PlatformRow struct {
	ID              int64      `json:"id"`
	Name            string     `json:"name"`
	Enable          bool       `json:"enable"`
	ServerGBID      string     `json:"server_gb_id"`
	ServerGBDomain  string     `json:"server_gb_domain"`
	ServerIP        string     `json:"server_ip"`
	ServerPort      int        `json:"server_port"`
	DeviceGBID      string     `json:"device_gb_id"`
	DeviceGBDomain  string     `json:"device_gb_domain"`
	DeviceIP        string     `json:"device_ip"`
	DevicePort      int        `json:"device_port"`
	Username        string     `json:"username"`
	Password        string     `json:"password"`
	Transport       string     `json:"transport"`
	CharacterSet    string     `json:"character_set"`
	Expires         int        `json:"expires"`
	KeepTimeout     int        `json:"keep_timeout"`
	MaxTimeoutCount int        `json:"max_timeout_count"`
	Status          bool       `json:"status"`
	LastRegisterAt  *time.Time `json:"last_register_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type PlatformChannelRow struct {
	ID         int64  `json:"id"`
	PlatformID int64  `json:"platform_id"`
	ChannelID  string `json:"channel_id"`
	DeviceID   string `json:"device_id"`
	CustomID   string `json:"custom_id"`
	CustomName string `json:"custom_name"`
	StreamPath string `json:"stream_path"`
	IsShared   bool   `json:"is_shared"`
}

func (d *DB) createPlatformTables(ctx context.Context) error {
	platformSQL := `CREATE TABLE IF NOT EXISTS gb28181_platforms (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT DEFAULT '',
		enable INTEGER DEFAULT 0,
		server_gb_id TEXT NOT NULL,
		server_gb_domain TEXT DEFAULT '',
		server_ip TEXT NOT NULL,
		server_port INTEGER DEFAULT 5060,
		device_gb_id TEXT NOT NULL,
		device_gb_domain TEXT DEFAULT '',
		device_ip TEXT NOT NULL,
		device_port INTEGER DEFAULT 5060,
		username TEXT DEFAULT '',
		password TEXT DEFAULT '',
		transport TEXT DEFAULT 'UDP',
		character_set TEXT DEFAULT 'GB2312',
		expires INTEGER DEFAULT 3600,
		keep_timeout INTEGER DEFAULT 60,
		max_timeout_count INTEGER DEFAULT 3,
		status INTEGER DEFAULT 0,
		last_register_at DATETIME,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);`

	channelSQL := `CREATE TABLE IF NOT EXISTS gb28181_platform_channels (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		platform_id INTEGER NOT NULL,
		channel_id TEXT NOT NULL,
		device_id TEXT DEFAULT '',
		custom_id TEXT DEFAULT '',
		custom_name TEXT DEFAULT '',
		stream_path TEXT DEFAULT '',
		is_shared INTEGER DEFAULT 0,
		FOREIGN KEY (platform_id) REFERENCES gb28181_platforms(id) ON DELETE CASCADE
	);`

	if _, err := d.db.ExecContext(ctx, platformSQL); err != nil {
		return err
	}
	if _, err := d.db.ExecContext(ctx, channelSQL); err != nil {
		return err
	}
	return nil
}

func (d *DB) ListPlatforms(ctx context.Context) ([]PlatformRow, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT id, name, enable, server_gb_id, server_gb_domain, server_ip, server_port,
			   device_gb_id, device_gb_domain, device_ip, device_port, username, password,
			   transport, character_set, expires, keep_timeout, max_timeout_count, status,
			   last_register_at, created_at, updated_at
		FROM gb28181_platforms ORDER BY id;`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var platforms []PlatformRow
	for rows.Next() {
		var p PlatformRow
		var lastReg *string
		if err := rows.Scan(&p.ID, &p.Name, &p.Enable, &p.ServerGBID, &p.ServerGBDomain,
			&p.ServerIP, &p.ServerPort, &p.DeviceGBID, &p.DeviceGBDomain, &p.DeviceIP,
			&p.DevicePort, &p.Username, &p.Password, &p.Transport, &p.CharacterSet,
			&p.Expires, &p.KeepTimeout, &p.MaxTimeoutCount, &p.Status, &lastReg,
			&p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		if lastReg != nil {
			t, _ := parseTime(*lastReg)
			p.LastRegisterAt = &t
		}
		platforms = append(platforms, p)
	}
	return platforms, nil
}

func (d *DB) GetPlatform(ctx context.Context, id int64) (*PlatformRow, error) {
	var p PlatformRow
	var lastReg *string
	err := d.db.QueryRowContext(ctx, `
		SELECT id, name, enable, server_gb_id, server_gb_domain, server_ip, server_port,
			   device_gb_id, device_gb_domain, device_ip, device_port, username, password,
			   transport, character_set, expires, keep_timeout, max_timeout_count, status,
			   last_register_at, created_at, updated_at
		FROM gb28181_platforms WHERE id = ?;`, id).Scan(
		&p.ID, &p.Name, &p.Enable, &p.ServerGBID, &p.ServerGBDomain,
		&p.ServerIP, &p.ServerPort, &p.DeviceGBID, &p.DeviceGBDomain, &p.DeviceIP,
		&p.DevicePort, &p.Username, &p.Password, &p.Transport, &p.CharacterSet,
		&p.Expires, &p.KeepTimeout, &p.MaxTimeoutCount, &p.Status, &lastReg,
		&p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if lastReg != nil {
		t, _ := parseTime(*lastReg)
		p.LastRegisterAt = &t
	}
	return &p, nil
}

func (d *DB) CreatePlatform(ctx context.Context, p *PlatformRow) (int64, error) {
	res, err := d.db.ExecContext(ctx, `
		INSERT INTO gb28181_platforms (name, enable, server_gb_id, server_gb_domain, server_ip, server_port,
			device_gb_id, device_gb_domain, device_ip, device_port, username, password, transport,
			character_set, expires, keep_timeout, max_timeout_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);`,
		p.Name, p.Enable, p.ServerGBID, p.ServerGBDomain, p.ServerIP, p.ServerPort,
		p.DeviceGBID, p.DeviceGBDomain, p.DeviceIP, p.DevicePort, p.Username, p.Password,
		p.Transport, p.CharacterSet, p.Expires, p.KeepTimeout, p.MaxTimeoutCount)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *DB) UpdatePlatform(ctx context.Context, p *PlatformRow) error {
	_, err := d.db.ExecContext(ctx, `
		UPDATE gb28181_platforms SET name=?, enable=?, server_gb_id=?, server_gb_domain=?,
			server_ip=?, server_port=?, device_gb_id=?, device_gb_domain=?, device_ip=?,
			device_port=?, username=?, password=?, transport=?, character_set=?, expires=?,
			keep_timeout=?, max_timeout_count=?, updated_at=CURRENT_TIMESTAMP
		WHERE id=?;`,
		p.Name, p.Enable, p.ServerGBID, p.ServerGBDomain, p.ServerIP, p.ServerPort,
		p.DeviceGBID, p.DeviceGBDomain, p.DeviceIP, p.DevicePort, p.Username, p.Password,
		p.Transport, p.CharacterSet, p.Expires, p.KeepTimeout, p.MaxTimeoutCount, p.ID)
	return err
}

func (d *DB) DeletePlatform(ctx context.Context, id int64) error {
	_, err := d.db.ExecContext(ctx, "DELETE FROM gb28181_platforms WHERE id = ?;", id)
	return err
}

func (d *DB) UpdatePlatformStatus(ctx context.Context, id int64, status bool) error {
	_, err := d.db.ExecContext(ctx, `
		UPDATE gb28181_platforms SET status=?, last_register_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
		WHERE id=?;`, status, id)
	return err
}

func (d *DB) ListPlatformChannels(ctx context.Context, platformID int64, shared *bool) ([]PlatformChannelRow, error) {
	query := `SELECT id, platform_id, channel_id, device_id, custom_id, custom_name, stream_path, is_shared
		FROM gb28181_platform_channels WHERE platform_id = ?`
	args := []interface{}{platformID}
	if shared != nil {
		query += " AND is_shared = ?"
		if *shared {
			args = append(args, 1)
		} else {
			args = append(args, 0)
		}
	}
	query += " ORDER BY id;"

	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var channels []PlatformChannelRow
	for rows.Next() {
		var ch PlatformChannelRow
		if err := rows.Scan(&ch.ID, &ch.PlatformID, &ch.ChannelID, &ch.DeviceID,
			&ch.CustomID, &ch.CustomName, &ch.StreamPath, &ch.IsShared); err != nil {
			return nil, err
		}
		channels = append(channels, ch)
	}
	return channels, nil
}

func (d *DB) UpsertPlatformChannel(ctx context.Context, ch *PlatformChannelRow) error {
	_, err := d.db.ExecContext(ctx, `
		INSERT INTO gb28181_platform_channels (platform_id, channel_id, device_id, custom_id, custom_name, stream_path, is_shared)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(platform_id, channel_id) DO UPDATE SET
			device_id = excluded.device_id,
			custom_id = excluded.custom_id,
			custom_name = excluded.custom_name,
			stream_path = excluded.stream_path,
			is_shared = excluded.is_shared;`,
		ch.PlatformID, ch.ChannelID, ch.DeviceID, ch.CustomID, ch.CustomName, ch.StreamPath, ch.IsShared)
	return err
}

func (d *DB) DeletePlatformChannel(ctx context.Context, platformID int64, channelID string) error {
	_, err := d.db.ExecContext(ctx, "DELETE FROM gb28181_platform_channels WHERE platform_id = ? AND channel_id = ?;",
		platformID, channelID)
	return err
}

func (d *DB) SetPlatformChannelShared(ctx context.Context, platformID int64, channelID string, shared bool) error {
	_, err := d.db.ExecContext(ctx, `
		UPDATE gb28181_platform_channels SET is_shared = ? WHERE platform_id = ? AND channel_id = ?;`,
		shared, platformID, channelID)
	return err
}
