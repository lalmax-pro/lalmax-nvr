package storage

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/civilcode"
)

// scannable 抽象 *sql.Row 与 *sql.Rows 的 Scan。
type scannable interface {
	Scan(dest ...any) error
}

func isNoRows(err error) bool { return err == sql.ErrNoRows }

// int64Placeholders 生成 "?,?,?" 占位符与对应参数。
func int64Placeholders(ids []int64) (string, []any) {
	parts := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		parts[i] = "?"
		args[i] = id
	}
	return strings.Join(parts, ","), args
}

// GBRegion 行政区划节点 (对应 wvp wvp_common_region)。
type GBRegion struct {
	ID             int64     `json:"id"`
	DeviceID       string    `json:"device_id"` // 区划编码 GB/T 2260
	Name           string    `json:"name"`
	ParentDeviceID string    `json:"parent_device_id"`
	ParentID       int64     `json:"parent_id"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// ChannelKey 通道复合主键 (device_id + channel_id)。
type ChannelKey struct {
	DeviceID  string `json:"device_id"`
	ChannelID string `json:"channel_id"`
}

// createGBRegionTables 创建行政区划表。
func (d *DB) createGBRegionTables(ctx context.Context) error {
	regionSQL := `CREATE TABLE IF NOT EXISTS gb28181_regions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		device_id TEXT NOT NULL UNIQUE,
		name TEXT NOT NULL,
		parent_device_id TEXT DEFAULT '',
		parent_id INTEGER DEFAULT 0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);`
	if _, err := d.db.ExecContext(ctx, regionSQL); err != nil {
		return err
	}
	if _, err := d.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_gb_regions_parent ON gb28181_regions(parent_id);"); err != nil {
		return err
	}
	if _, err := d.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_gb_regions_parent_device ON gb28181_regions(parent_device_id);"); err != nil {
		return err
	}
	return nil
}

func scanGBRegion(rows scannable) (GBRegion, error) {
	var r GBRegion
	err := rows.Scan(&r.ID, &r.DeviceID, &r.Name, &r.ParentDeviceID, &r.ParentID, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

const gbRegionCols = "id, device_id, name, parent_device_id, parent_id, created_at, updated_at"

// ListGBRegionsByParent 返回某父节点的直接子区划; parentID=0 取顶层。
func (d *DB) ListGBRegionsByParent(ctx context.Context, parentID int64) ([]GBRegion, error) {
	rows, err := d.db.QueryContext(ctx,
		"SELECT "+gbRegionCols+" FROM gb28181_regions WHERE parent_id = ? ORDER BY device_id;", parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []GBRegion
	for rows.Next() {
		r, err := scanGBRegion(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, r)
	}
	return list, rows.Err()
}

// GetGBRegion 按自增ID查询。
func (d *DB) GetGBRegion(ctx context.Context, id int64) (*GBRegion, error) {
	row := d.db.QueryRowContext(ctx, "SELECT "+gbRegionCols+" FROM gb28181_regions WHERE id = ?;", id)
	r, err := scanGBRegion(row)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// GetGBRegionByDeviceID 按区划编码查询, 不存在返回 nil,nil。
func (d *DB) GetGBRegionByDeviceID(ctx context.Context, deviceID string) (*GBRegion, error) {
	row := d.db.QueryRowContext(ctx, "SELECT "+gbRegionCols+" FROM gb28181_regions WHERE device_id = ?;", deviceID)
	r, err := scanGBRegion(row)
	if err != nil {
		if isNoRows(err) {
			return nil, nil
		}
		return nil, err
	}
	return &r, nil
}

// CreateGBRegion 新建区划节点, 回填 parent_id。
func (d *DB) CreateGBRegion(ctx context.Context, r *GBRegion) (int64, error) {
	if r.ParentDeviceID != "" && r.ParentID == 0 {
		if parent, _ := d.GetGBRegionByDeviceID(ctx, r.ParentDeviceID); parent != nil {
			r.ParentID = parent.ID
		}
	}
	res, err := d.db.ExecContext(ctx,
		`INSERT INTO gb28181_regions (device_id, name, parent_device_id, parent_id) VALUES (?, ?, ?, ?);`,
		r.DeviceID, r.Name, r.ParentDeviceID, r.ParentID)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	r.ID = id
	// 回填以本节点为父的孤立子节点
	_, _ = d.db.ExecContext(ctx,
		"UPDATE gb28181_regions SET parent_id = ? WHERE parent_device_id = ? AND parent_id = 0;", id, r.DeviceID)
	return id, nil
}

// UpdateGBRegion 更新区划 (名称/编码)。编码变化时同步子节点与通道由上层处理。
func (d *DB) UpdateGBRegion(ctx context.Context, r *GBRegion) error {
	_, err := d.db.ExecContext(ctx,
		`UPDATE gb28181_regions SET device_id = ?, name = ?, parent_device_id = ?, parent_id = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?;`,
		r.DeviceID, r.Name, r.ParentDeviceID, r.ParentID, r.ID)
	return err
}

// GetGBRegionDescendants 递归返回某节点的所有后代 (不含自身)。
func (d *DB) GetGBRegionDescendants(ctx context.Context, id int64) ([]GBRegion, error) {
	var result []GBRegion
	children, err := d.ListGBRegionsByParent(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, c := range children {
		result = append(result, c)
		sub, err := d.GetGBRegionDescendants(ctx, c.ID)
		if err != nil {
			return nil, err
		}
		result = append(result, sub...)
	}
	return result, nil
}

// DeleteGBRegions 批量删除区划节点。
func (d *DB) DeleteGBRegions(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	ph, args := int64Placeholders(ids)
	_, err := d.db.ExecContext(ctx, "DELETE FROM gb28181_regions WHERE id IN ("+ph+");", args...)
	return err
}

// SyncRegionsFromChannels 从通道已有的 civil_code 反向补建缺失的区划节点，
// 借助内置 GB/T 2260 编码表补全名称与完整父链。返回新建节点数。
func (d *DB) SyncRegionsFromChannels(ctx context.Context) (int, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT DISTINCT civil_code FROM gb28181_channels
		WHERE civil_code <> '' AND civil_code NOT IN (SELECT device_id FROM gb28181_regions);`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var codes []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return 0, err
		}
		codes = append(codes, c)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return d.ensureRegions(ctx, codes)
}

// AddRegionByCivilCode 按标准编码新增区划节点，并自动补全其全部上级节点。
// 返回新建节点数。
func (d *DB) AddRegionByCivilCode(ctx context.Context, code string) (int, error) {
	if code == "" {
		return 0, nil
	}
	return d.ensureRegions(ctx, []string{code})
}

// ensureRegions 确保给定编码及其全部上级节点存在；父节点先于子节点插入，
// 以便 CreateGBRegion 正确回填 parent_id。已存在的节点跳过。
func (d *DB) ensureRegions(ctx context.Context, codes []string) (int, error) {
	if len(codes) == 0 {
		return 0, nil
	}
	needed := map[string]civilcode.Code{}
	for _, code := range codes {
		if code == "" {
			continue
		}
		if c, ok := civilcode.Get(code); ok {
			needed[code] = c
		} else {
			// 不在标准表中：以编码作名称，按长度规则推断父级
			needed[code] = civilcode.Code{Code: code, Name: code, ParentCode: parentCivilCode(code)}
		}
		for _, p := range civilcode.AllParents(code) {
			needed[p.Code] = p
		}
	}

	list := make([]civilcode.Code, 0, len(needed))
	for _, c := range needed {
		list = append(list, c)
	}
	// 按编码长度升序（同长按编码），保证父节点先插入
	sort.Slice(list, func(i, j int) bool {
		if len(list[i].Code) != len(list[j].Code) {
			return len(list[i].Code) < len(list[j].Code)
		}
		return list[i].Code < list[j].Code
	})

	count := 0
	for _, c := range list {
		if existing, _ := d.GetGBRegionByDeviceID(ctx, c.Code); existing != nil {
			continue
		}
		name := c.Name
		if name == "" {
			name = c.Code
		}
		r := &GBRegion{DeviceID: c.Code, Name: name, ParentDeviceID: c.ParentCode}
		if _, err := d.CreateGBRegion(ctx, r); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// ==================== 通道 <-> 行政区划 挂接 ====================

// CountChannelsByCivilCode 统计挂接在某区划下的通道数。
func (d *DB) CountChannelsByCivilCode(ctx context.Context, civilCode string) (int, error) {
	var n int
	err := d.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM gb28181_channels WHERE civil_code = ?;", civilCode).Scan(&n)
	return n, err
}

// AttachChannelsToRegion 把通道批量挂接到区划 (写 civil_code)。
func (d *DB) AttachChannelsToRegion(ctx context.Context, civilCode string, keys []ChannelKey) error {
	return d.updateChannelsField(ctx, "civil_code", civilCode, keys)
}

// DetachChannelsFromRegion 解绑通道的区划 (civil_code 置空)。
func (d *DB) DetachChannelsFromRegion(ctx context.Context, keys []ChannelKey) error {
	return d.updateChannelsField(ctx, "civil_code", "", keys)
}

// DetachChannelsFromRegionByCode 解绑某区划下全部通道。
func (d *DB) DetachChannelsFromRegionByCode(ctx context.Context, civilCode string) error {
	_, err := d.db.ExecContext(ctx,
		"UPDATE gb28181_channels SET civil_code = '' WHERE civil_code = ?;", civilCode)
	return err
}

// updateChannelsField 按复合键批量更新通道单字段。
func (d *DB) updateChannelsField(ctx context.Context, field, value string, keys []ChannelKey) error {
	if len(keys) == 0 {
		return nil
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx,
		"UPDATE gb28181_channels SET "+field+" = ? WHERE device_id = ? AND channel_id = ?;")
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, k := range keys {
		if _, err := stmt.ExecContext(ctx, value, k.DeviceID, k.ChannelID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ==================== 通道精简列表 (用于目录页右侧) ====================

// GBChannelBrief 目录页展示用的通道精简信息。
type GBChannelBrief struct {
	DeviceID        string `json:"device_id"`
	ChannelID       string `json:"channel_id"`
	Name            string `json:"name"`
	Status          string `json:"status"`
	CivilCode       string `json:"civil_code"`
	ParentID        string `json:"parent_id"`
	BusinessGroupID string `json:"business_group_id"`
}

const gbChannelBriefCols = "device_id, channel_id, name, status, civil_code, parent_id, business_group_id"

func scanGBChannelBriefs(rows *sql.Rows) ([]GBChannelBrief, error) {
	defer rows.Close()
	var list []GBChannelBrief
	for rows.Next() {
		var c GBChannelBrief
		if err := rows.Scan(&c.DeviceID, &c.ChannelID, &c.Name, &c.Status,
			&c.CivilCode, &c.ParentID, &c.BusinessGroupID); err != nil {
			return nil, err
		}
		list = append(list, c)
	}
	return list, rows.Err()
}

// ListChannelsByCivilCode 列出挂接到某区划的通道。
func (d *DB) ListChannelsByCivilCode(ctx context.Context, civilCode string) ([]GBChannelBrief, error) {
	rows, err := d.db.QueryContext(ctx,
		"SELECT "+gbChannelBriefCols+" FROM gb28181_channels WHERE civil_code = ? ORDER BY device_id, channel_id;", civilCode)
	if err != nil {
		return nil, err
	}
	return scanGBChannelBriefs(rows)
}

// ListChannelsByParentID 列出挂接到某虚拟组织的通道。
func (d *DB) ListChannelsByParentID(ctx context.Context, parentID string) ([]GBChannelBrief, error) {
	rows, err := d.db.QueryContext(ctx,
		"SELECT "+gbChannelBriefCols+" FROM gb28181_channels WHERE parent_id = ? ORDER BY device_id, channel_id;", parentID)
	if err != nil {
		return nil, err
	}
	return scanGBChannelBriefs(rows)
}

// SearchChannelsBrief 按名称/编号模糊搜索通道 (q 为空返回全部); unassignedRegion/unassignedGroup
// 为 true 时分别过滤未挂接区划/分组的通道。
func (d *DB) SearchChannelsBrief(ctx context.Context, q string, unassignedRegion, unassignedGroup bool) ([]GBChannelBrief, error) {
	sb := "SELECT " + gbChannelBriefCols + " FROM gb28181_channels WHERE 1=1"
	var args []any
	if q != "" {
		sb += " AND (name LIKE ? OR channel_id LIKE ?)"
		like := "%" + q + "%"
		args = append(args, like, like)
	}
	if unassignedRegion {
		sb += " AND civil_code = ''"
	}
	if unassignedGroup {
		sb += " AND parent_id = ''"
	}
	sb += " ORDER BY device_id, channel_id;"
	rows, err := d.db.QueryContext(ctx, sb, args...)
	if err != nil {
		return nil, err
	}
	return scanGBChannelBriefs(rows)
}

// parentCivilCode 按 GB/T 2260 长度规则推断父区划编码; 无父返回空串。
// 规则: 长度 >2 时, 末两位归零向上取整为更短的有效层级。
func parentCivilCode(code string) string {
	code = strings.TrimRight(code, " ")
	switch {
	case len(code) >= 8:
		return code[:6]
	case len(code) >= 6:
		return code[:4]
	case len(code) >= 4:
		return code[:2]
	default:
		return ""
	}
}
