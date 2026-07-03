package storage

import (
	"context"
	"time"
)

// GBGroup 业务分组 / 虚拟组织节点 (对应 wvp wvp_common_group)。
// TypeCode=215 为业务分组(顶层), 216 为虚拟组织。
type GBGroup struct {
	ID             int64     `json:"id"`
	DeviceID       string    `json:"device_id"`
	Name           string    `json:"name"`
	ParentDeviceID string    `json:"parent_device_id"`
	ParentID       int64     `json:"parent_id"`
	BusinessGroup  string    `json:"business_group"` // 所属215业务分组编号 (215节点指向自身)
	CivilCode      string    `json:"civil_code"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// createGBGroupTables 创建业务分组/虚拟组织表。
func (d *DB) createGBGroupTables(ctx context.Context) error {
	groupSQL := `CREATE TABLE IF NOT EXISTS gb28181_groups (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		device_id TEXT NOT NULL,
		name TEXT NOT NULL,
		parent_device_id TEXT DEFAULT '',
		parent_id INTEGER DEFAULT 0,
		business_group TEXT NOT NULL,
		civil_code TEXT DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(device_id, business_group)
	);`
	if _, err := d.db.ExecContext(ctx, groupSQL); err != nil {
		return err
	}
	if _, err := d.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_gb_groups_business ON gb28181_groups(business_group);"); err != nil {
		return err
	}
	if _, err := d.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_gb_groups_parent ON gb28181_groups(parent_id);"); err != nil {
		return err
	}
	return nil
}

const gbGroupCols = "id, device_id, name, parent_device_id, parent_id, business_group, civil_code, created_at, updated_at"

func scanGBGroup(rows scannable) (GBGroup, error) {
	var g GBGroup
	err := rows.Scan(&g.ID, &g.DeviceID, &g.Name, &g.ParentDeviceID, &g.ParentID,
		&g.BusinessGroup, &g.CivilCode, &g.CreatedAt, &g.UpdatedAt)
	return g, err
}

// ListGBGroupsByParent 返回某父节点的直接子分组; parentID=0 取顶层业务分组(215)。
func (d *DB) ListGBGroupsByParent(ctx context.Context, parentID int64) ([]GBGroup, error) {
	rows, err := d.db.QueryContext(ctx,
		"SELECT "+gbGroupCols+" FROM gb28181_groups WHERE parent_id = ? ORDER BY device_id;", parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []GBGroup
	for rows.Next() {
		g, err := scanGBGroup(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, g)
	}
	return list, rows.Err()
}

// GetGBGroup 按自增ID查询。
func (d *DB) GetGBGroup(ctx context.Context, id int64) (*GBGroup, error) {
	row := d.db.QueryRowContext(ctx, "SELECT "+gbGroupCols+" FROM gb28181_groups WHERE id = ?;", id)
	g, err := scanGBGroup(row)
	if err != nil {
		return nil, err
	}
	return &g, nil
}

// GetGBGroupByDeviceID 在指定业务分组内按编号查询, 不存在返回 nil,nil。
func (d *DB) GetGBGroupByDeviceID(ctx context.Context, deviceID, businessGroup string) (*GBGroup, error) {
	row := d.db.QueryRowContext(ctx,
		"SELECT "+gbGroupCols+" FROM gb28181_groups WHERE device_id = ? AND business_group = ?;", deviceID, businessGroup)
	g, err := scanGBGroup(row)
	if err != nil {
		if isNoRows(err) {
			return nil, nil
		}
		return nil, err
	}
	return &g, nil
}

// GetGBBusinessGroup 查询业务分组(215)节点。
func (d *DB) GetGBBusinessGroup(ctx context.Context, businessGroup string) (*GBGroup, error) {
	return d.GetGBGroupByDeviceID(ctx, businessGroup, businessGroup)
}

// CreateGBGroup 新建分组节点 (业务分组或虚拟组织), 回填 parent_id。
func (d *DB) CreateGBGroup(ctx context.Context, g *GBGroup) (int64, error) {
	if g.ParentDeviceID != "" && g.ParentID == 0 {
		if parent, _ := d.GetGBGroupByDeviceID(ctx, g.ParentDeviceID, g.BusinessGroup); parent != nil {
			g.ParentID = parent.ID
		}
	}
	res, err := d.db.ExecContext(ctx,
		`INSERT INTO gb28181_groups (device_id, name, parent_device_id, parent_id, business_group, civil_code)
		 VALUES (?, ?, ?, ?, ?, ?);`,
		g.DeviceID, g.Name, g.ParentDeviceID, g.ParentID, g.BusinessGroup, g.CivilCode)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	g.ID = id
	// 回填以本节点为父的孤立子节点
	_, _ = d.db.ExecContext(ctx,
		"UPDATE gb28181_groups SET parent_id = ? WHERE parent_device_id = ? AND business_group = ? AND parent_id = 0;",
		id, g.DeviceID, g.BusinessGroup)
	return id, nil
}

// UpdateGBGroup 更新分组 (名称/编码)。
func (d *DB) UpdateGBGroup(ctx context.Context, g *GBGroup) error {
	_, err := d.db.ExecContext(ctx,
		`UPDATE gb28181_groups SET device_id = ?, name = ?, parent_device_id = ?, parent_id = ?,
		 business_group = ?, civil_code = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?;`,
		g.DeviceID, g.Name, g.ParentDeviceID, g.ParentID, g.BusinessGroup, g.CivilCode, g.ID)
	return err
}

// GetGBGroupDescendants 递归返回某虚拟组织节点的所有后代 (不含自身)。
func (d *DB) GetGBGroupDescendants(ctx context.Context, id int64) ([]GBGroup, error) {
	var result []GBGroup
	children, err := d.ListGBGroupsByParent(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, c := range children {
		result = append(result, c)
		sub, err := d.GetGBGroupDescendants(ctx, c.ID)
		if err != nil {
			return nil, err
		}
		result = append(result, sub...)
	}
	return result, nil
}

// ListGBGroupsByBusinessGroup 返回某业务分组下全部节点 (含业务分组自身与所有虚拟组织)。
func (d *DB) ListGBGroupsByBusinessGroup(ctx context.Context, businessGroup string) ([]GBGroup, error) {
	rows, err := d.db.QueryContext(ctx,
		"SELECT "+gbGroupCols+" FROM gb28181_groups WHERE business_group = ? ORDER BY device_id;", businessGroup)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []GBGroup
	for rows.Next() {
		g, err := scanGBGroup(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, g)
	}
	return list, rows.Err()
}

// DeleteGBGroups 批量删除分组节点。
func (d *DB) DeleteGBGroups(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	ph, args := int64Placeholders(ids)
	_, err := d.db.ExecContext(ctx, "DELETE FROM gb28181_groups WHERE id IN ("+ph+");", args...)
	return err
}

// ==================== 通道 <-> 业务分组 挂接 ====================

// CountChannelsByParentID 统计挂接在某虚拟组织下的通道数。
func (d *DB) CountChannelsByParentID(ctx context.Context, parentID string) (int, error) {
	var n int
	err := d.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM gb28181_channels WHERE parent_id = ?;", parentID).Scan(&n)
	return n, err
}

// AttachChannelsToGroup 把通道批量挂接到虚拟组织 (写 parent_id + business_group_id)。
func (d *DB) AttachChannelsToGroup(ctx context.Context, parentID, businessGroup string, keys []ChannelKey) error {
	if len(keys) == 0 {
		return nil
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx,
		"UPDATE gb28181_channels SET parent_id = ?, business_group_id = ? WHERE device_id = ? AND channel_id = ?;")
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, k := range keys {
		if _, err := stmt.ExecContext(ctx, parentID, businessGroup, k.DeviceID, k.ChannelID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DetachChannelsFromGroup 解绑通道的业务分组 (parent_id + business_group_id 置空)。
func (d *DB) DetachChannelsFromGroup(ctx context.Context, keys []ChannelKey) error {
	if len(keys) == 0 {
		return nil
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx,
		"UPDATE gb28181_channels SET parent_id = '', business_group_id = '' WHERE device_id = ? AND channel_id = ?;")
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, k := range keys {
		if _, err := stmt.ExecContext(ctx, k.DeviceID, k.ChannelID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DetachChannelsFromGroupByParent 解绑某虚拟组织下全部通道。
func (d *DB) DetachChannelsFromGroupByParent(ctx context.Context, parentID string) error {
	_, err := d.db.ExecContext(ctx,
		"UPDATE gb28181_channels SET parent_id = '', business_group_id = '' WHERE parent_id = ?;", parentID)
	return err
}

// DetachChannelsFromBusinessGroup 解绑某业务分组下全部通道。
func (d *DB) DetachChannelsFromBusinessGroup(ctx context.Context, businessGroup string) error {
	_, err := d.db.ExecContext(ctx,
		"UPDATE gb28181_channels SET parent_id = '', business_group_id = '' WHERE business_group_id = ?;", businessGroup)
	return err
}
