package storage

import (
	"context"
	"database/sql"
	"time"
)

// DeviceGroup represents a device group in the database.
type DeviceGroup struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	ParentID  int64     `json:"parent_id"`
	Level     int       `json:"level"`
	SortOrder int       `json:"sort_order"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// DeviceGroupTreeNode represents a group with its children for tree structure.
type DeviceGroupTreeNode struct {
	DeviceGroup
	Children []DeviceGroupTreeNode `json:"children,omitempty"`
}

// DeviceGroupChannel represents a channel associated with a group.
type DeviceGroupChannel struct {
	ID        int64     `json:"id"`
	GroupID   int64     `json:"group_id"`
	DeviceID  string    `json:"device_id"`
	ChannelID string    `json:"channel_id"`
	CreatedAt time.Time `json:"created_at"`
}

// DeviceGroupChannelDetail represents a channel with device info for display.
type DeviceGroupChannelDetail struct {
	DeviceGroupChannel
	DeviceName  string `json:"device_name"`
	ChannelName string `json:"channel_name"`
	IsOnline    bool   `json:"is_online"`
}

// createGroupTables creates the device group and group channel tables.
func (d *DB) createGroupTables(ctx context.Context) error {
	groupSQL := `CREATE TABLE IF NOT EXISTS device_groups (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		parent_id INTEGER DEFAULT 0,
		level INTEGER DEFAULT 0,
		sort_order INTEGER DEFAULT 0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);`

	channelSQL := `CREATE TABLE IF NOT EXISTS device_group_channels (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		group_id INTEGER NOT NULL,
		device_id TEXT NOT NULL,
		channel_id TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(group_id, device_id, channel_id),
		FOREIGN KEY (group_id) REFERENCES device_groups(id) ON DELETE CASCADE
	);`

	if _, err := d.execContext(ctx, groupSQL); err != nil {
		return err
	}
	if _, err := d.execContext(ctx, channelSQL); err != nil {
		return err
	}

	// Create indexes
	if _, err := d.execContext(ctx, "CREATE INDEX IF NOT EXISTS idx_device_groups_parent ON device_groups(parent_id);"); err != nil {
		return err
	}
	if _, err := d.execContext(ctx, "CREATE INDEX IF NOT EXISTS idx_device_group_channels_group ON device_group_channels(group_id);"); err != nil {
		return err
	}
	if _, err := d.execContext(ctx, "CREATE INDEX IF NOT EXISTS idx_device_group_channels_device ON device_group_channels(device_id);"); err != nil {
		return err
	}

	// Insert default root group if not exists
	var count int
	err := d.queryRowContext(ctx, "SELECT COUNT(*) FROM device_groups WHERE parent_id = 0 AND name = '默认分组';").Scan(&count)
	if err == nil && count == 0 {
		_, _ = d.execContext(ctx, `INSERT INTO device_groups (name, parent_id, level, sort_order) VALUES ('默认分组', 0, 0, 0);`)
	}

	return nil
}

// ListDeviceGroups returns all device groups.
func (d *DB) ListDeviceGroups(ctx context.Context) ([]DeviceGroup, error) {
	rows, err := d.queryContext(ctx, `
		SELECT id, name, parent_id, level, sort_order, created_at, updated_at
		FROM device_groups
		ORDER BY level, sort_order, id;`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []DeviceGroup
	for rows.Next() {
		var g DeviceGroup
		if err := rows.Scan(&g.ID, &g.Name, &g.ParentID, &g.Level, &g.SortOrder, &g.CreatedAt, &g.UpdatedAt); err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	return groups, nil
}

// GetDeviceGroup returns a single device group by ID.
func (d *DB) GetDeviceGroup(ctx context.Context, id int64) (*DeviceGroup, error) {
	var g DeviceGroup
	err := d.queryRowContext(ctx, `
		SELECT id, name, parent_id, level, sort_order, created_at, updated_at
		FROM device_groups WHERE id = ?;`, id).Scan(
		&g.ID, &g.Name, &g.ParentID, &g.Level, &g.SortOrder, &g.CreatedAt, &g.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &g, nil
}

// CreateDeviceGroup creates a new device group.
func (d *DB) CreateDeviceGroup(ctx context.Context, group *DeviceGroup) (int64, error) {
	return d.insertID(ctx, `
		INSERT INTO device_groups (name, parent_id, level, sort_order)
		VALUES (?, ?, ?, ?);`,
		group.Name, group.ParentID, group.Level, group.SortOrder)
}

// UpdateDeviceGroup updates an existing device group.
func (d *DB) UpdateDeviceGroup(ctx context.Context, group *DeviceGroup) error {
	_, err := d.execContext(ctx, `
		UPDATE device_groups
		SET name = ?, parent_id = ?, level = ?, sort_order = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?;`,
		group.Name, group.ParentID, group.Level, group.SortOrder, group.ID)
	return err
}

// DeleteDeviceGroup deletes a device group and its children.
func (d *DB) DeleteDeviceGroup(ctx context.Context, id int64) error {
	// Delete children first
	_, err := d.execContext(ctx, "DELETE FROM device_groups WHERE parent_id = ?;", id)
	if err != nil {
		return err
	}
	// Delete the group itself
	_, err = d.execContext(ctx, "DELETE FROM device_groups WHERE id = ?;", id)
	return err
}

// ListGroupChannels returns all channels in a group.
func (d *DB) ListGroupChannels(ctx context.Context, groupID int64) ([]DeviceGroupChannelDetail, error) {
	rows, err := d.queryContext(ctx, `
		SELECT
			gc.id, gc.group_id, gc.device_id, gc.channel_id, gc.created_at,
			COALESCE(NULLIF(d.name, ''), cam.name, '') as device_name,
			COALESCE(NULLIF(c.name, ''), cam.name, '') as channel_name,
			CASE
				WHEN d.device_id IS NOT NULL THEN d.is_online
				WHEN cam.id IS NOT NULL THEN cam.enabled
				ELSE 0
			END as is_online
		FROM device_group_channels gc
		LEFT JOIN gb28181_devices d ON gc.device_id = d.device_id
		LEFT JOIN gb28181_channels c ON gc.device_id = c.device_id AND gc.channel_id = c.channel_id
		LEFT JOIN cameras cam ON gc.device_id = cam.id AND gc.device_id = gc.channel_id
		WHERE gc.group_id = ?
		ORDER BY gc.device_id, gc.channel_id;`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var channels []DeviceGroupChannelDetail
	for rows.Next() {
		var ch DeviceGroupChannelDetail
		if err := rows.Scan(&ch.ID, &ch.GroupID, &ch.DeviceID, &ch.ChannelID, &ch.CreatedAt,
			&ch.DeviceName, &ch.ChannelName, &ch.IsOnline); err != nil {
			return nil, err
		}
		channels = append(channels, ch)
	}
	return channels, nil
}

// AddGroupChannel adds a channel to a group.
func (d *DB) AddGroupChannel(ctx context.Context, groupID int64, deviceID, channelID string) error {
	_, err := d.execContext(ctx, `
		INSERT OR IGNORE INTO device_group_channels (group_id, device_id, channel_id)
		VALUES (?, ?, ?);`, groupID, deviceID, channelID)
	return err
}

// RemoveGroupChannel removes a channel from a group.
func (d *DB) RemoveGroupChannel(ctx context.Context, groupID int64, deviceID, channelID string) error {
	_, err := d.execContext(ctx, `
		DELETE FROM device_group_channels
		WHERE group_id = ? AND device_id = ? AND channel_id = ?;`,
		groupID, deviceID, channelID)
	return err
}

// RemoveGroupChannelsByDeviceID removes all group channel associations for a device.
func (d *DB) RemoveGroupChannelsByDeviceID(ctx context.Context, deviceID string) error {
	_, err := d.execContext(ctx, "DELETE FROM device_group_channels WHERE device_id = ?;", deviceID)
	return err
}

// RemoveGroupChannelByID removes a channel from a group by its ID.
func (d *DB) RemoveGroupChannelByID(ctx context.Context, id int64) error {
	_, err := d.execContext(ctx, "DELETE FROM device_group_channels WHERE id = ?;", id)
	return err
}

// CountGroupChannels returns the number of channels in a group.
func (d *DB) CountGroupChannels(ctx context.Context, groupID int64) (int, error) {
	var count int
	err := d.queryRowContext(ctx, "SELECT COUNT(*) FROM device_group_channels WHERE group_id = ?;", groupID).Scan(&count)
	return count, err
}

// GetGroupChannelStats returns channel count and online count for a group.
func (d *DB) GetGroupChannelStats(ctx context.Context, groupID int64) (total int, online int, err error) {
	err = d.queryRowContext(ctx, `
		SELECT
			COUNT(*),
			SUM(CASE WHEN COALESCE(d.is_online, 0) = 1 THEN 1 ELSE 0 END)
		FROM device_group_channels gc
		LEFT JOIN gb28181_devices d ON gc.device_id = d.device_id
		WHERE gc.group_id = ?;`, groupID).Scan(&total, &online)
	return
}

// sql.NullString helper
type nullString struct {
	sql.NullString
}
