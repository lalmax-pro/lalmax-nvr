package storage

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// GB28181DeviceRow represents a GB28181 device in the database.
type GB28181DeviceRow struct {
	DeviceID        string     `json:"device_id"`
	Name            string     `json:"name"`
	Manufacturer    string     `json:"manufacturer"`
	Model           string     `json:"model"`
	Firmware        string     `json:"firmware"`
	GBVersion       string     `json:"gb_version"`
	IsOnline        bool       `json:"is_online"`
	Address         string     `json:"address"`
	LastKeepaliveAt *time.Time `json:"last_keepalive_at,omitempty"`
	LastRegisterAt  *time.Time `json:"last_register_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// GB28181ChannelRow represents a channel of a GB28181 device.
type GB28181ChannelRow struct {
	DeviceID     string     `json:"device_id"`
	ChannelID    string     `json:"channel_id"`
	Name         string     `json:"name"`
	LastSeenAt   *time.Time `json:"last_seen_at,omitempty"`
	Status       string     `json:"status"`
	MissingCount int        `json:"missing_count"`
	CreatedAt    time.Time  `json:"created_at"`

	// 2016标准字段
	Manufacturer    string  `json:"manufacturer"`
	Model           string  `json:"model"`
	Owner           string  `json:"owner"`
	CivilCode       string  `json:"civil_code"`
	Block           string  `json:"block"`
	Address         string  `json:"address"`
	Parental        int     `json:"parental"`
	ParentID        string  `json:"parent_id"`
	SafetyWay       int     `json:"safety_way"`
	RegisterWay     int     `json:"register_way"`
	CertNum         string  `json:"cert_num"`
	Certifiable     int     `json:"certifiable"`
	ErrCode         int     `json:"err_code"`
	EndTime         string  `json:"end_time"`
	Secrecy         int     `json:"secrecy"`
	IPAddress       string  `json:"ip_address"`
	Port            int     `json:"port"`
	Password        string  `json:"password"`
	Longitude       float64 `json:"longitude"`
	Latitude        float64 `json:"latitude"`
	PTZType         int     `json:"ptz_type"`
	PositionType    int     `json:"position_type"`
	RoomType        int     `json:"room_type"`
	UseType         int     `json:"use_type"`
	SupplyLightType int     `json:"supply_light_type"`
	DirectionType   int     `json:"direction_type"`
	Resolution      string  `json:"resolution"`

	// 2022标准新增字段
	SecurityLevelCode        string  `json:"security_level_code"`
	StreamNumberList         string  `json:"stream_number_list"`
	DownloadSpeed            string  `json:"download_speed"`
	SVCSpaceSupportMod       int     `json:"svc_space_support_mod"`
	SVCTimeSupportMode       int     `json:"svc_time_support_mode"`
	SSVCRatioSupportList     string  `json:"ssvc_ratio_support_list"`
	MobileDeviceType         int     `json:"mobile_device_type"`
	HorizontalFieldAngle     float64 `json:"horizontal_field_angle"`
	VerticalFieldAngle       float64 `json:"vertical_field_angle"`
	MaxViewDistance          float64 `json:"max_view_distance"`
	GrassrootsCode           string  `json:"grassroots_code"`
	PoType                   int     `json:"po_type"`
	PoCommonName             string  `json:"po_common_name"`
	Mac                      string  `json:"mac"`
	FunctionType             string  `json:"function_type"`
	EncodeType               string  `json:"encode_type"`
	InstallTime              string  `json:"install_time"`
	ManagementUnit           string  `json:"management_unit"`
	ContactInfo              string  `json:"contact_info"`
	RecordSaveDays           int     `json:"record_save_days"`
	IndustrialClassification string  `json:"industrial_classification"`
	BusinessGroupID          string  `json:"business_group_id"`
}

// createGB28181Tables creates the GB28181 device and channel tables.
func (d *DB) createGB28181Tables(ctx context.Context) error {
	deviceSQL := `CREATE TABLE IF NOT EXISTS gb28181_devices (
		device_id TEXT PRIMARY KEY,
		name TEXT DEFAULT '',
		manufacturer TEXT DEFAULT '',
		model TEXT DEFAULT '',
		firmware TEXT DEFAULT '',
		gb_version TEXT DEFAULT '2016',
		is_online INTEGER DEFAULT 0,
		address TEXT DEFAULT '',
		last_keepalive_at DATETIME,
		last_register_at DATETIME,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);`

	channelSQL := `CREATE TABLE IF NOT EXISTS gb28181_channels (
		device_id TEXT NOT NULL,
		channel_id TEXT NOT NULL,
		name TEXT DEFAULT '',
		last_seen_at DATETIME,
		status TEXT DEFAULT '',
		missing_count INTEGER DEFAULT 0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		-- 2016标准字段
		manufacturer TEXT DEFAULT '',
		model TEXT DEFAULT '',
		owner TEXT DEFAULT '',
		civil_code TEXT DEFAULT '',
		block TEXT DEFAULT '',
		address TEXT DEFAULT '',
		parental INTEGER DEFAULT 0,
		parent_id TEXT DEFAULT '',
		safety_way INTEGER DEFAULT 0,
		register_way INTEGER DEFAULT 0,
		cert_num TEXT DEFAULT '',
		certifiable INTEGER DEFAULT 0,
		err_code INTEGER DEFAULT 0,
		end_time TEXT DEFAULT '',
		secrecy INTEGER DEFAULT 0,
		ip_address TEXT DEFAULT '',
		port INTEGER DEFAULT 0,
		password TEXT DEFAULT '',
		longitude REAL DEFAULT 0,
		latitude REAL DEFAULT 0,
		ptz_type INTEGER DEFAULT 0,
		position_type INTEGER DEFAULT 0,
		room_type INTEGER DEFAULT 0,
		use_type INTEGER DEFAULT 0,
		supply_light_type INTEGER DEFAULT 0,
		direction_type INTEGER DEFAULT 0,
		resolution TEXT DEFAULT '',
		-- 2022标准新增字段
		security_level_code TEXT DEFAULT '',
		stream_number_list TEXT DEFAULT '',
		download_speed TEXT DEFAULT '',
		svc_space_support_mod INTEGER DEFAULT 0,
		svc_time_support_mode INTEGER DEFAULT 0,
		ssvc_ratio_support_list TEXT DEFAULT '',
		mobile_device_type INTEGER DEFAULT 0,
		horizontal_field_angle REAL DEFAULT 0,
		vertical_field_angle REAL DEFAULT 0,
		max_view_distance REAL DEFAULT 0,
		grassroots_code TEXT DEFAULT '',
		po_type INTEGER DEFAULT 0,
		po_common_name TEXT DEFAULT '',
		mac TEXT DEFAULT '',
		function_type TEXT DEFAULT '',
		encode_type TEXT DEFAULT '',
		install_time TEXT DEFAULT '',
		management_unit TEXT DEFAULT '',
		contact_info TEXT DEFAULT '',
		record_save_days INTEGER DEFAULT 0,
		industrial_classification TEXT DEFAULT '',
		business_group_id TEXT DEFAULT '',
		PRIMARY KEY (device_id, channel_id),
		FOREIGN KEY (device_id) REFERENCES gb28181_devices(device_id) ON DELETE CASCADE
	);`

	if _, err := d.db.ExecContext(ctx, deviceSQL); err != nil {
		return err
	}
	if _, err := d.db.ExecContext(ctx, channelSQL); err != nil {
		return err
	}

	// 迁移：添加新列（如果不存在）
	migrations := []string{
		`ALTER TABLE gb28181_channels ADD COLUMN last_seen_at DATETIME;`,
		`ALTER TABLE gb28181_channels ADD COLUMN status TEXT DEFAULT '';`,
		`ALTER TABLE gb28181_channels ADD COLUMN missing_count INTEGER DEFAULT 0;`,
		// 2016标准字段迁移
		`ALTER TABLE gb28181_channels ADD COLUMN manufacturer TEXT DEFAULT '';`,
		`ALTER TABLE gb28181_channels ADD COLUMN model TEXT DEFAULT '';`,
		`ALTER TABLE gb28181_channels ADD COLUMN owner TEXT DEFAULT '';`,
		`ALTER TABLE gb28181_channels ADD COLUMN civil_code TEXT DEFAULT '';`,
		`ALTER TABLE gb28181_channels ADD COLUMN block TEXT DEFAULT '';`,
		`ALTER TABLE gb28181_channels ADD COLUMN address TEXT DEFAULT '';`,
		`ALTER TABLE gb28181_channels ADD COLUMN parental INTEGER DEFAULT 0;`,
		`ALTER TABLE gb28181_channels ADD COLUMN parent_id TEXT DEFAULT '';`,
		`ALTER TABLE gb28181_channels ADD COLUMN safety_way INTEGER DEFAULT 0;`,
		`ALTER TABLE gb28181_channels ADD COLUMN register_way INTEGER DEFAULT 0;`,
		`ALTER TABLE gb28181_channels ADD COLUMN cert_num TEXT DEFAULT '';`,
		`ALTER TABLE gb28181_channels ADD COLUMN certifiable INTEGER DEFAULT 0;`,
		`ALTER TABLE gb28181_channels ADD COLUMN err_code INTEGER DEFAULT 0;`,
		`ALTER TABLE gb28181_channels ADD COLUMN end_time TEXT DEFAULT '';`,
		`ALTER TABLE gb28181_channels ADD COLUMN secrecy INTEGER DEFAULT 0;`,
		`ALTER TABLE gb28181_channels ADD COLUMN ip_address TEXT DEFAULT '';`,
		`ALTER TABLE gb28181_channels ADD COLUMN port INTEGER DEFAULT 0;`,
		`ALTER TABLE gb28181_channels ADD COLUMN password TEXT DEFAULT '';`,
		`ALTER TABLE gb28181_channels ADD COLUMN longitude REAL DEFAULT 0;`,
		`ALTER TABLE gb28181_channels ADD COLUMN latitude REAL DEFAULT 0;`,
		`ALTER TABLE gb28181_channels ADD COLUMN ptz_type INTEGER DEFAULT 0;`,
		`ALTER TABLE gb28181_channels ADD COLUMN position_type INTEGER DEFAULT 0;`,
		`ALTER TABLE gb28181_channels ADD COLUMN room_type INTEGER DEFAULT 0;`,
		`ALTER TABLE gb28181_channels ADD COLUMN use_type INTEGER DEFAULT 0;`,
		`ALTER TABLE gb28181_channels ADD COLUMN supply_light_type INTEGER DEFAULT 0;`,
		`ALTER TABLE gb28181_channels ADD COLUMN direction_type INTEGER DEFAULT 0;`,
		`ALTER TABLE gb28181_channels ADD COLUMN resolution TEXT DEFAULT '';`,
		// 2022标准字段迁移
		`ALTER TABLE gb28181_channels ADD COLUMN security_level_code TEXT DEFAULT '';`,
		`ALTER TABLE gb28181_channels ADD COLUMN stream_number_list TEXT DEFAULT '';`,
		`ALTER TABLE gb28181_channels ADD COLUMN download_speed TEXT DEFAULT '';`,
		`ALTER TABLE gb28181_channels ADD COLUMN svc_space_support_mod INTEGER DEFAULT 0;`,
		`ALTER TABLE gb28181_channels ADD COLUMN svc_time_support_mode INTEGER DEFAULT 0;`,
		`ALTER TABLE gb28181_channels ADD COLUMN ssvc_ratio_support_list TEXT DEFAULT '';`,
		`ALTER TABLE gb28181_channels ADD COLUMN mobile_device_type INTEGER DEFAULT 0;`,
		`ALTER TABLE gb28181_channels ADD COLUMN horizontal_field_angle REAL DEFAULT 0;`,
		`ALTER TABLE gb28181_channels ADD COLUMN vertical_field_angle REAL DEFAULT 0;`,
		`ALTER TABLE gb28181_channels ADD COLUMN max_view_distance REAL DEFAULT 0;`,
		`ALTER TABLE gb28181_channels ADD COLUMN grassroots_code TEXT DEFAULT '';`,
		`ALTER TABLE gb28181_channels ADD COLUMN po_type INTEGER DEFAULT 0;`,
		`ALTER TABLE gb28181_channels ADD COLUMN po_common_name TEXT DEFAULT '';`,
		`ALTER TABLE gb28181_channels ADD COLUMN mac TEXT DEFAULT '';`,
		`ALTER TABLE gb28181_channels ADD COLUMN function_type TEXT DEFAULT '';`,
		`ALTER TABLE gb28181_channels ADD COLUMN encode_type TEXT DEFAULT '';`,
		`ALTER TABLE gb28181_channels ADD COLUMN install_time TEXT DEFAULT '';`,
		`ALTER TABLE gb28181_channels ADD COLUMN management_unit TEXT DEFAULT '';`,
		`ALTER TABLE gb28181_channels ADD COLUMN contact_info TEXT DEFAULT '';`,
		`ALTER TABLE gb28181_channels ADD COLUMN record_save_days INTEGER DEFAULT 0;`,
		`ALTER TABLE gb28181_channels ADD COLUMN industrial_classification TEXT DEFAULT '';`,
		`ALTER TABLE gb28181_channels ADD COLUMN business_group_id TEXT DEFAULT '';`,
	}

	for _, migration := range migrations {
		_, err := d.db.ExecContext(ctx, migration)
		if err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			return err
		}
	}

	return nil
}

// ListGB28181Devices returns all GB28181 devices.
func (d *DB) ListGB28181Devices(ctx context.Context) ([]GB28181DeviceRow, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT device_id, name, manufacturer, model, firmware, gb_version, is_online, address,
			   last_keepalive_at, last_register_at, created_at, updated_at
		FROM gb28181_devices 
		ORDER BY is_online DESC, device_id;`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var devices []GB28181DeviceRow
	for rows.Next() {
		var d GB28181DeviceRow
		var lastKeepalive, lastRegister sql.NullString
		if err := rows.Scan(&d.DeviceID, &d.Name, &d.Manufacturer, &d.Model, &d.Firmware, &d.GBVersion,
			&d.IsOnline, &d.Address, &lastKeepalive, &lastRegister, &d.CreatedAt, &d.UpdatedAt); err != nil {
			return nil, err
		}
		if lastKeepalive.Valid {
			t, _ := parseTime(lastKeepalive.String)
			d.LastKeepaliveAt = &t
		}
		if lastRegister.Valid {
			t, _ := parseTime(lastRegister.String)
			d.LastRegisterAt = &t
		}
		devices = append(devices, d)
	}
	return devices, nil
}

// GetGB28181Device returns a single GB28181 device by ID.
func (d *DB) GetGB28181Device(ctx context.Context, deviceID string) (*GB28181DeviceRow, error) {
	var dev GB28181DeviceRow
	var lastKeepalive, lastRegister sql.NullString
	err := d.db.QueryRowContext(ctx, `
		SELECT device_id, name, manufacturer, model, firmware, gb_version, is_online, address,
			   last_keepalive_at, last_register_at, created_at, updated_at
		FROM gb28181_devices WHERE device_id = ?;`, deviceID).Scan(
		&dev.DeviceID, &dev.Name, &dev.Manufacturer, &dev.Model, &dev.Firmware, &dev.GBVersion,
		&dev.IsOnline, &dev.Address, &lastKeepalive, &lastRegister, &dev.CreatedAt, &dev.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if lastKeepalive.Valid {
		t, _ := parseTime(lastKeepalive.String)
		dev.LastKeepaliveAt = &t
	}
	if lastRegister.Valid {
		t, _ := parseTime(lastRegister.String)
		dev.LastRegisterAt = &t
	}
	return &dev, nil
}

// UpsertGB28181Device creates or updates a GB28181 device.
func (d *DB) UpsertGB28181Device(ctx context.Context, device *GB28181DeviceRow) error {
	_, err := d.db.ExecContext(ctx, `
		INSERT INTO gb28181_devices (device_id, name, manufacturer, model, firmware, gb_version, is_online, address, last_keepalive_at, last_register_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(device_id) DO UPDATE SET
			name = COALESCE(NULLIF(excluded.name, ''), name),
			manufacturer = COALESCE(NULLIF(excluded.manufacturer, ''), manufacturer),
			model = COALESCE(NULLIF(excluded.model, ''), model),
			firmware = COALESCE(NULLIF(excluded.firmware, ''), firmware),
			gb_version = COALESCE(NULLIF(excluded.gb_version, ''), gb_version),
			is_online = excluded.is_online,
			address = COALESCE(NULLIF(excluded.address, ''), address),
			last_keepalive_at = COALESCE(excluded.last_keepalive_at, last_keepalive_at),
			last_register_at = COALESCE(excluded.last_register_at, last_register_at),
			updated_at = CURRENT_TIMESTAMP;`,
		device.DeviceID, device.Name, device.Manufacturer, device.Model, device.Firmware, device.GBVersion,
		device.IsOnline, device.Address, device.LastKeepaliveAt, device.LastRegisterAt)
	return err
}

// UpdateGB28181DeviceStatus updates the online status and address of a device.
func (d *DB) UpdateGB28181DeviceStatus(ctx context.Context, deviceID string, isOnline bool, address string) error {
	_, err := d.db.ExecContext(ctx, `
		INSERT INTO gb28181_devices (device_id, is_online, address, last_keepalive_at, updated_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		ON CONFLICT(device_id) DO UPDATE SET
			is_online = excluded.is_online,
			address = excluded.address,
			last_keepalive_at = CURRENT_TIMESTAMP,
			updated_at = CURRENT_TIMESTAMP;`, deviceID, isOnline, address)
	return err
}

// UpdateGB28181DeviceRegistration updates device info after registration.
func (d *DB) UpdateGB28181DeviceRegistration(ctx context.Context, deviceID, address string) error {
	_, err := d.db.ExecContext(ctx, `
		INSERT INTO gb28181_devices (device_id, is_online, address, last_register_at, updated_at)
		VALUES (?, 1, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		ON CONFLICT(device_id) DO UPDATE SET
			is_online = 1,
			address = excluded.address,
			last_register_at = CURRENT_TIMESTAMP,
			updated_at = CURRENT_TIMESTAMP;`, deviceID, address)
	return err
}

// UpdateGB28181DeviceOnlineStatus sets a device offline.
func (d *DB) UpdateGB28181DeviceOnlineStatus(ctx context.Context, deviceID string, isOnline bool) error {
	_, err := d.db.ExecContext(ctx, `
		UPDATE gb28181_devices 
		SET is_online = ?, updated_at = CURRENT_TIMESTAMP
		WHERE device_id = ?;`, isOnline, deviceID)
	return err
}

// DeleteGB28181Device removes a GB28181 device and its channels.
func (d *DB) DeleteGB28181Device(ctx context.Context, deviceID string) error {
	_, err := d.db.ExecContext(ctx, "DELETE FROM gb28181_devices WHERE device_id = ?;", deviceID)
	return err
}

// ListGB28181Channels returns all channels for a device.
func (d *DB) ListGB28181Channels(ctx context.Context, deviceID string) ([]GB28181ChannelRow, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT device_id, channel_id, name, last_seen_at, status, missing_count, created_at,
			manufacturer, model, owner, civil_code, block, address, parental, parent_id,
			safety_way, register_way, cert_num, certifiable, err_code, end_time, secrecy,
			ip_address, port, password, longitude, latitude, ptz_type, position_type,
			room_type, use_type, supply_light_type, direction_type, resolution,
			security_level_code, stream_number_list, download_speed, svc_space_support_mod,
			svc_time_support_mode, ssvc_ratio_support_list, mobile_device_type,
			horizontal_field_angle, vertical_field_angle, max_view_distance, grassroots_code,
			po_type, po_common_name, mac, function_type, encode_type, install_time,
			management_unit, contact_info, record_save_days, industrial_classification,
			business_group_id
		FROM gb28181_channels 
		WHERE device_id = ?
		ORDER BY channel_id;`, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var channels []GB28181ChannelRow
	for rows.Next() {
		var ch GB28181ChannelRow
		var lastSeenAt sql.NullString
		if err := rows.Scan(&ch.DeviceID, &ch.ChannelID, &ch.Name, &lastSeenAt, &ch.Status, &ch.MissingCount, &ch.CreatedAt,
			&ch.Manufacturer, &ch.Model, &ch.Owner, &ch.CivilCode, &ch.Block, &ch.Address, &ch.Parental, &ch.ParentID,
			&ch.SafetyWay, &ch.RegisterWay, &ch.CertNum, &ch.Certifiable, &ch.ErrCode, &ch.EndTime, &ch.Secrecy,
			&ch.IPAddress, &ch.Port, &ch.Password, &ch.Longitude, &ch.Latitude, &ch.PTZType, &ch.PositionType,
			&ch.RoomType, &ch.UseType, &ch.SupplyLightType, &ch.DirectionType, &ch.Resolution,
			&ch.SecurityLevelCode, &ch.StreamNumberList, &ch.DownloadSpeed, &ch.SVCSpaceSupportMod,
			&ch.SVCTimeSupportMode, &ch.SSVCRatioSupportList, &ch.MobileDeviceType,
			&ch.HorizontalFieldAngle, &ch.VerticalFieldAngle, &ch.MaxViewDistance, &ch.GrassrootsCode,
			&ch.PoType, &ch.PoCommonName, &ch.Mac, &ch.FunctionType, &ch.EncodeType, &ch.InstallTime,
			&ch.ManagementUnit, &ch.ContactInfo, &ch.RecordSaveDays, &ch.IndustrialClassification,
			&ch.BusinessGroupID); err != nil {
			return nil, err
		}
		if lastSeenAt.Valid {
			t, _ := parseTime(lastSeenAt.String)
			ch.LastSeenAt = &t
		}
		channels = append(channels, ch)
	}
	return channels, nil
}

// GetGB28181Channel returns a single channel by device ID and channel ID.
func (d *DB) GetGB28181Channel(ctx context.Context, deviceID, channelID string) (*GB28181ChannelRow, error) {
	var ch GB28181ChannelRow
	var lastSeenAt sql.NullString
	err := d.db.QueryRowContext(ctx, `
		SELECT device_id, channel_id, name, last_seen_at, status, missing_count, created_at,
			manufacturer, model, owner, civil_code, block, address, parental, parent_id,
			safety_way, register_way, cert_num, certifiable, err_code, end_time, secrecy,
			ip_address, port, password, longitude, latitude, ptz_type, position_type,
			room_type, use_type, supply_light_type, direction_type, resolution,
			security_level_code, stream_number_list, download_speed, svc_space_support_mod,
			svc_time_support_mode, ssvc_ratio_support_list, mobile_device_type,
			horizontal_field_angle, vertical_field_angle, max_view_distance, grassroots_code,
			po_type, po_common_name, mac, function_type, encode_type, install_time,
			management_unit, contact_info, record_save_days, industrial_classification,
			business_group_id
		FROM gb28181_channels 
		WHERE device_id = ? AND channel_id = ?;`, deviceID, channelID).Scan(
		&ch.DeviceID, &ch.ChannelID, &ch.Name, &lastSeenAt, &ch.Status, &ch.MissingCount, &ch.CreatedAt,
		&ch.Manufacturer, &ch.Model, &ch.Owner, &ch.CivilCode, &ch.Block, &ch.Address, &ch.Parental, &ch.ParentID,
		&ch.SafetyWay, &ch.RegisterWay, &ch.CertNum, &ch.Certifiable, &ch.ErrCode, &ch.EndTime, &ch.Secrecy,
		&ch.IPAddress, &ch.Port, &ch.Password, &ch.Longitude, &ch.Latitude, &ch.PTZType, &ch.PositionType,
		&ch.RoomType, &ch.UseType, &ch.SupplyLightType, &ch.DirectionType, &ch.Resolution,
		&ch.SecurityLevelCode, &ch.StreamNumberList, &ch.DownloadSpeed, &ch.SVCSpaceSupportMod,
		&ch.SVCTimeSupportMode, &ch.SSVCRatioSupportList, &ch.MobileDeviceType,
		&ch.HorizontalFieldAngle, &ch.VerticalFieldAngle, &ch.MaxViewDistance, &ch.GrassrootsCode,
		&ch.PoType, &ch.PoCommonName, &ch.Mac, &ch.FunctionType, &ch.EncodeType, &ch.InstallTime,
		&ch.ManagementUnit, &ch.ContactInfo, &ch.RecordSaveDays, &ch.IndustrialClassification,
		&ch.BusinessGroupID)
	if err != nil {
		return nil, err
	}
	if lastSeenAt.Valid {
		t, _ := parseTime(lastSeenAt.String)
		ch.LastSeenAt = &t
	}
	return &ch, nil
}

// UpsertGB28181Channel creates or updates a channel.
func (d *DB) UpsertGB28181Channel(ctx context.Context, channel *GB28181ChannelRow) error {
	_, err := d.db.ExecContext(ctx, `
		INSERT INTO gb28181_channels (
			device_id, channel_id, name, last_seen_at, status, missing_count,
			manufacturer, model, owner, civil_code, block, address, parental, parent_id,
			safety_way, register_way, cert_num, certifiable, err_code, end_time, secrecy,
			ip_address, port, password, longitude, latitude, ptz_type, position_type,
			room_type, use_type, supply_light_type, direction_type, resolution,
			security_level_code, stream_number_list, download_speed, svc_space_support_mod,
			svc_time_support_mode, ssvc_ratio_support_list, mobile_device_type,
			horizontal_field_angle, vertical_field_angle, max_view_distance, grassroots_code,
			po_type, po_common_name, mac, function_type, encode_type, install_time,
			management_unit, contact_info, record_save_days, industrial_classification,
			business_group_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(device_id, channel_id) DO UPDATE SET
			name = COALESCE(NULLIF(excluded.name, ''), name),
			last_seen_at = CASE
				WHEN excluded.last_seen_at > gb28181_channels.last_seen_at OR gb28181_channels.last_seen_at IS NULL
				THEN excluded.last_seen_at
				ELSE gb28181_channels.last_seen_at
			END,
			status = CASE
				WHEN excluded.last_seen_at > gb28181_channels.last_seen_at OR gb28181_channels.last_seen_at IS NULL
				THEN excluded.status
				ELSE gb28181_channels.status
			END,
			missing_count = CASE
				WHEN excluded.last_seen_at > gb28181_channels.last_seen_at OR gb28181_channels.last_seen_at IS NULL
				THEN excluded.missing_count
				ELSE gb28181_channels.missing_count
			END,
			manufacturer = COALESCE(NULLIF(excluded.manufacturer, ''), manufacturer),
			model = COALESCE(NULLIF(excluded.model, ''), model),
			owner = COALESCE(NULLIF(excluded.owner, ''), owner),
			civil_code = COALESCE(NULLIF(excluded.civil_code, ''), civil_code),
			block = COALESCE(NULLIF(excluded.block, ''), block),
			address = COALESCE(NULLIF(excluded.address, ''), address),
			parental = excluded.parental,
			parent_id = COALESCE(NULLIF(excluded.parent_id, ''), parent_id),
			safety_way = excluded.safety_way,
			register_way = excluded.register_way,
			cert_num = COALESCE(NULLIF(excluded.cert_num, ''), cert_num),
			certifiable = excluded.certifiable,
			err_code = excluded.err_code,
			end_time = COALESCE(NULLIF(excluded.end_time, ''), end_time),
			secrecy = excluded.secrecy,
			ip_address = COALESCE(NULLIF(excluded.ip_address, ''), ip_address),
			port = excluded.port,
			password = COALESCE(NULLIF(excluded.password, ''), password),
			longitude = excluded.longitude,
			latitude = excluded.latitude,
			ptz_type = excluded.ptz_type,
			position_type = excluded.position_type,
			room_type = excluded.room_type,
			use_type = excluded.use_type,
			supply_light_type = excluded.supply_light_type,
			direction_type = excluded.direction_type,
			resolution = COALESCE(NULLIF(excluded.resolution, ''), resolution),
			security_level_code = COALESCE(NULLIF(excluded.security_level_code, ''), security_level_code),
			stream_number_list = COALESCE(NULLIF(excluded.stream_number_list, ''), stream_number_list),
			download_speed = COALESCE(NULLIF(excluded.download_speed, ''), download_speed),
			svc_space_support_mod = excluded.svc_space_support_mod,
			svc_time_support_mode = excluded.svc_time_support_mode,
			ssvc_ratio_support_list = COALESCE(NULLIF(excluded.ssvc_ratio_support_list, ''), ssvc_ratio_support_list),
			mobile_device_type = excluded.mobile_device_type,
			horizontal_field_angle = excluded.horizontal_field_angle,
			vertical_field_angle = excluded.vertical_field_angle,
			max_view_distance = excluded.max_view_distance,
			grassroots_code = COALESCE(NULLIF(excluded.grassroots_code, ''), grassroots_code),
			po_type = excluded.po_type,
			po_common_name = COALESCE(NULLIF(excluded.po_common_name, ''), po_common_name),
			mac = COALESCE(NULLIF(excluded.mac, ''), mac),
			function_type = COALESCE(NULLIF(excluded.function_type, ''), function_type),
			encode_type = COALESCE(NULLIF(excluded.encode_type, ''), encode_type),
			install_time = COALESCE(NULLIF(excluded.install_time, ''), install_time),
			management_unit = COALESCE(NULLIF(excluded.management_unit, ''), management_unit),
			contact_info = COALESCE(NULLIF(excluded.contact_info, ''), contact_info),
			record_save_days = excluded.record_save_days,
			industrial_classification = COALESCE(NULLIF(excluded.industrial_classification, ''), industrial_classification),
			business_group_id = COALESCE(NULLIF(excluded.business_group_id, ''), business_group_id);`,
		channel.DeviceID, channel.ChannelID, channel.Name, channel.LastSeenAt, channel.Status, channel.MissingCount,
		channel.Manufacturer, channel.Model, channel.Owner, channel.CivilCode, channel.Block, channel.Address,
		channel.Parental, channel.ParentID, channel.SafetyWay, channel.RegisterWay, channel.CertNum,
		channel.Certifiable, channel.ErrCode, channel.EndTime, channel.Secrecy, channel.IPAddress, channel.Port,
		channel.Password, channel.Longitude, channel.Latitude, channel.PTZType, channel.PositionType,
		channel.RoomType, channel.UseType, channel.SupplyLightType, channel.DirectionType, channel.Resolution,
		channel.SecurityLevelCode, channel.StreamNumberList, channel.DownloadSpeed, channel.SVCSpaceSupportMod,
		channel.SVCTimeSupportMode, channel.SSVCRatioSupportList, channel.MobileDeviceType,
		channel.HorizontalFieldAngle, channel.VerticalFieldAngle, channel.MaxViewDistance, channel.GrassrootsCode,
		channel.PoType, channel.PoCommonName, channel.Mac, channel.FunctionType, channel.EncodeType,
		channel.InstallTime, channel.ManagementUnit, channel.ContactInfo, channel.RecordSaveDays,
		channel.IndustrialClassification, channel.BusinessGroupID)
	return err
}

// BatchUpsertChannels performs incremental update of channels for a device.
// It deletes channels that no longer exist and inserts/updates new channels.
func (d *DB) BatchUpsertChannels(ctx context.Context, deviceID string, channels []GB28181ChannelRow) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// 1. 查询现有通道
	existingRows, err := tx.QueryContext(ctx,
		"SELECT channel_id FROM gb28181_channels WHERE device_id = ?;", deviceID)
	if err != nil {
		return err
	}

	existing := make(map[string]bool)
	for existingRows.Next() {
		var chID string
		if err := existingRows.Scan(&chID); err != nil {
			existingRows.Close()
			return err
		}
		existing[chID] = true
	}
	existingRows.Close()

	// 2. 构建新通道集合
	newSet := make(map[string]bool)
	for _, ch := range channels {
		newSet[ch.ChannelID] = true
	}

	// 3. 删除不存在的通道
	for chID := range existing {
		if !newSet[chID] {
			if _, err := tx.ExecContext(ctx,
				"DELETE FROM gb28181_channels WHERE device_id = ? AND channel_id = ?;",
				deviceID, chID); err != nil {
				return err
			}
		}
	}

	// 4. 批量插入/更新通道
	for _, ch := range channels {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO gb28181_channels (
				device_id, channel_id, name, last_seen_at, status, missing_count,
				manufacturer, model, owner, civil_code, block, address, parental, parent_id,
				safety_way, register_way, cert_num, certifiable, err_code, end_time, secrecy,
				ip_address, port, password, longitude, latitude, ptz_type, position_type,
				room_type, use_type, supply_light_type, direction_type, resolution,
				security_level_code, stream_number_list, download_speed, svc_space_support_mod,
				svc_time_support_mode, ssvc_ratio_support_list, mobile_device_type,
				horizontal_field_angle, vertical_field_angle, max_view_distance, grassroots_code,
				po_type, po_common_name, mac, function_type, encode_type, install_time,
				management_unit, contact_info, record_save_days, industrial_classification,
				business_group_id
			) VALUES (?, ?, ?, CURRENT_TIMESTAMP, 'online', 0, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(device_id, channel_id) DO UPDATE SET
				name = COALESCE(NULLIF(excluded.name, ''), name),
				last_seen_at = CURRENT_TIMESTAMP,
				status = 'online',
				missing_count = 0,
				manufacturer = COALESCE(NULLIF(excluded.manufacturer, ''), manufacturer),
				model = COALESCE(NULLIF(excluded.model, ''), model),
				owner = COALESCE(NULLIF(excluded.owner, ''), owner),
				civil_code = COALESCE(NULLIF(excluded.civil_code, ''), civil_code),
				block = COALESCE(NULLIF(excluded.block, ''), block),
				address = COALESCE(NULLIF(excluded.address, ''), address),
				parental = excluded.parental,
				parent_id = COALESCE(NULLIF(excluded.parent_id, ''), parent_id),
				safety_way = excluded.safety_way,
				register_way = excluded.register_way,
				cert_num = COALESCE(NULLIF(excluded.cert_num, ''), cert_num),
				certifiable = excluded.certifiable,
				err_code = excluded.err_code,
				end_time = COALESCE(NULLIF(excluded.end_time, ''), end_time),
				secrecy = excluded.secrecy,
				ip_address = COALESCE(NULLIF(excluded.ip_address, ''), ip_address),
				port = excluded.port,
				password = COALESCE(NULLIF(excluded.password, ''), password),
				longitude = excluded.longitude,
				latitude = excluded.latitude,
				ptz_type = excluded.ptz_type,
				position_type = excluded.position_type,
				room_type = excluded.room_type,
				use_type = excluded.use_type,
				supply_light_type = excluded.supply_light_type,
				direction_type = excluded.direction_type,
				resolution = COALESCE(NULLIF(excluded.resolution, ''), resolution),
				security_level_code = COALESCE(NULLIF(excluded.security_level_code, ''), security_level_code),
				stream_number_list = COALESCE(NULLIF(excluded.stream_number_list, ''), stream_number_list),
				download_speed = COALESCE(NULLIF(excluded.download_speed, ''), download_speed),
				svc_space_support_mod = excluded.svc_space_support_mod,
				svc_time_support_mode = excluded.svc_time_support_mode,
				ssvc_ratio_support_list = COALESCE(NULLIF(excluded.ssvc_ratio_support_list, ''), ssvc_ratio_support_list),
				mobile_device_type = excluded.mobile_device_type,
				horizontal_field_angle = excluded.horizontal_field_angle,
				vertical_field_angle = excluded.vertical_field_angle,
				max_view_distance = excluded.max_view_distance,
				grassroots_code = COALESCE(NULLIF(excluded.grassroots_code, ''), grassroots_code),
				po_type = excluded.po_type,
				po_common_name = COALESCE(NULLIF(excluded.po_common_name, ''), po_common_name),
				mac = COALESCE(NULLIF(excluded.mac, ''), mac),
				function_type = COALESCE(NULLIF(excluded.function_type, ''), function_type),
				encode_type = COALESCE(NULLIF(excluded.encode_type, ''), encode_type),
				install_time = COALESCE(NULLIF(excluded.install_time, ''), install_time),
				management_unit = COALESCE(NULLIF(excluded.management_unit, ''), management_unit),
				contact_info = COALESCE(NULLIF(excluded.contact_info, ''), contact_info),
				record_save_days = excluded.record_save_days,
				industrial_classification = COALESCE(NULLIF(excluded.industrial_classification, ''), industrial_classification),
				business_group_id = COALESCE(NULLIF(excluded.business_group_id, ''), business_group_id);`,
			ch.DeviceID, ch.ChannelID, ch.Name,
			ch.Manufacturer, ch.Model, ch.Owner, ch.CivilCode, ch.Block, ch.Address,
			ch.Parental, ch.ParentID, ch.SafetyWay, ch.RegisterWay, ch.CertNum,
			ch.Certifiable, ch.ErrCode, ch.EndTime, ch.Secrecy, ch.IPAddress, ch.Port,
			ch.Password, ch.Longitude, ch.Latitude, ch.PTZType, ch.PositionType,
			ch.RoomType, ch.UseType, ch.SupplyLightType, ch.DirectionType, ch.Resolution,
			ch.SecurityLevelCode, ch.StreamNumberList, ch.DownloadSpeed, ch.SVCSpaceSupportMod,
			ch.SVCTimeSupportMode, ch.SSVCRatioSupportList, ch.MobileDeviceType,
			ch.HorizontalFieldAngle, ch.VerticalFieldAngle, ch.MaxViewDistance, ch.GrassrootsCode,
			ch.PoType, ch.PoCommonName, ch.Mac, ch.FunctionType, ch.EncodeType,
			ch.InstallTime, ch.ManagementUnit, ch.ContactInfo, ch.RecordSaveDays,
			ch.IndustrialClassification, ch.BusinessGroupID); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// ReplaceGB28181Channels replaces all channels for a device.
func (d *DB) ReplaceGB28181Channels(ctx context.Context, deviceID string, channels []GB28181ChannelRow) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Delete existing channels
	if _, err := tx.ExecContext(ctx, "DELETE FROM gb28181_channels WHERE device_id = ?;", deviceID); err != nil {
		return err
	}

	// Insert new channels
	for _, ch := range channels {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO gb28181_channels (
				device_id, channel_id, name,
				manufacturer, model, owner, civil_code, block, address, parental, parent_id,
				safety_way, register_way, cert_num, certifiable, err_code, end_time, secrecy,
				ip_address, port, password, longitude, latitude, ptz_type, position_type,
				room_type, use_type, supply_light_type, direction_type, resolution,
				security_level_code, stream_number_list, download_speed, svc_space_support_mod,
				svc_time_support_mode, ssvc_ratio_support_list, mobile_device_type,
				horizontal_field_angle, vertical_field_angle, max_view_distance, grassroots_code,
				po_type, po_common_name, mac, function_type, encode_type, install_time,
				management_unit, contact_info, record_save_days, industrial_classification,
				business_group_id
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);`,
			ch.DeviceID, ch.ChannelID, ch.Name,
			ch.Manufacturer, ch.Model, ch.Owner, ch.CivilCode, ch.Block, ch.Address,
			ch.Parental, ch.ParentID, ch.SafetyWay, ch.RegisterWay, ch.CertNum,
			ch.Certifiable, ch.ErrCode, ch.EndTime, ch.Secrecy, ch.IPAddress, ch.Port,
			ch.Password, ch.Longitude, ch.Latitude, ch.PTZType, ch.PositionType,
			ch.RoomType, ch.UseType, ch.SupplyLightType, ch.DirectionType, ch.Resolution,
			ch.SecurityLevelCode, ch.StreamNumberList, ch.DownloadSpeed, ch.SVCSpaceSupportMod,
			ch.SVCTimeSupportMode, ch.SSVCRatioSupportList, ch.MobileDeviceType,
			ch.HorizontalFieldAngle, ch.VerticalFieldAngle, ch.MaxViewDistance, ch.GrassrootsCode,
			ch.PoType, ch.PoCommonName, ch.Mac, ch.FunctionType, ch.EncodeType,
			ch.InstallTime, ch.ManagementUnit, ch.ContactInfo, ch.RecordSaveDays,
			ch.IndustrialClassification, ch.BusinessGroupID); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// DeleteGB28181Channel removes a specific channel.
func (d *DB) DeleteGB28181Channel(ctx context.Context, deviceID, channelID string) error {
	_, err := d.db.ExecContext(ctx, "DELETE FROM gb28181_channels WHERE device_id = ? AND channel_id = ?;", deviceID, channelID)
	return err
}

// DeleteGB28181ChannelsForDevice removes all channels for a device.
func (d *DB) DeleteGB28181ChannelsForDevice(ctx context.Context, deviceID string) error {
	_, err := d.db.ExecContext(ctx, "DELETE FROM gb28181_channels WHERE device_id = ?;", deviceID)
	return err
}

// ListMissingChannels returns channels with missing_count >= threshold.
func (d *DB) ListMissingChannels(ctx context.Context, threshold int) ([]GB28181ChannelRow, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT device_id, channel_id, name, last_seen_at, status, missing_count, created_at,
			manufacturer, model, owner, civil_code, block, address, parental, parent_id,
			safety_way, register_way, cert_num, certifiable, err_code, end_time, secrecy,
			ip_address, port, password, longitude, latitude, ptz_type, position_type,
			room_type, use_type, supply_light_type, direction_type, resolution,
			security_level_code, stream_number_list, download_speed, svc_space_support_mod,
			svc_time_support_mode, ssvc_ratio_support_list, mobile_device_type,
			horizontal_field_angle, vertical_field_angle, max_view_distance, grassroots_code,
			po_type, po_common_name, mac, function_type, encode_type, install_time,
			management_unit, contact_info, record_save_days, industrial_classification,
			business_group_id
		FROM gb28181_channels 
		WHERE missing_count >= ?
		ORDER BY device_id, channel_id;`, threshold)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var channels []GB28181ChannelRow
	for rows.Next() {
		var ch GB28181ChannelRow
		var lastSeenAt sql.NullString
		if err := rows.Scan(&ch.DeviceID, &ch.ChannelID, &ch.Name, &lastSeenAt, &ch.Status, &ch.MissingCount, &ch.CreatedAt,
			&ch.Manufacturer, &ch.Model, &ch.Owner, &ch.CivilCode, &ch.Block, &ch.Address, &ch.Parental, &ch.ParentID,
			&ch.SafetyWay, &ch.RegisterWay, &ch.CertNum, &ch.Certifiable, &ch.ErrCode, &ch.EndTime, &ch.Secrecy,
			&ch.IPAddress, &ch.Port, &ch.Password, &ch.Longitude, &ch.Latitude, &ch.PTZType, &ch.PositionType,
			&ch.RoomType, &ch.UseType, &ch.SupplyLightType, &ch.DirectionType, &ch.Resolution,
			&ch.SecurityLevelCode, &ch.StreamNumberList, &ch.DownloadSpeed, &ch.SVCSpaceSupportMod,
			&ch.SVCTimeSupportMode, &ch.SSVCRatioSupportList, &ch.MobileDeviceType,
			&ch.HorizontalFieldAngle, &ch.VerticalFieldAngle, &ch.MaxViewDistance, &ch.GrassrootsCode,
			&ch.PoType, &ch.PoCommonName, &ch.Mac, &ch.FunctionType, &ch.EncodeType, &ch.InstallTime,
			&ch.ManagementUnit, &ch.ContactInfo, &ch.RecordSaveDays, &ch.IndustrialClassification,
			&ch.BusinessGroupID); err != nil {
			return nil, err
		}
		if lastSeenAt.Valid {
			t, _ := parseTime(lastSeenAt.String)
			ch.LastSeenAt = &t
		}
		channels = append(channels, ch)
	}
	return channels, nil
}

// UpdateChannelStatus updates the status of a channel.
func (d *DB) UpdateChannelStatus(ctx context.Context, deviceID, channelID, status string) error {
	_, err := d.db.ExecContext(ctx, `
		UPDATE gb28181_channels 
		SET status = ?
		WHERE device_id = ? AND channel_id = ?;`, status, deviceID, channelID)
	return err
}

// IncrementMissingCount increments the missing_count for a channel.
func (d *DB) IncrementMissingCount(ctx context.Context, deviceID, channelID string) error {
	_, err := d.db.ExecContext(ctx, `
		UPDATE gb28181_channels 
		SET missing_count = missing_count + 1
		WHERE device_id = ? AND channel_id = ?;`, deviceID, channelID)
	return err
}
