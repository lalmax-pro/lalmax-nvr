package storage

import (
	"context"
	"database/sql"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
	"github.com/lalmax-pro/lalmax-nvr/internal/model"
)

// CameraRow represents a camera record from the SQLite database.
// Shared fields with config.CameraConfig: ID, Name, Protocol, Encoding, URL, Username,
// ONVIFEndpoint, ProfileToken, StreamEncoding, Enabled.
// CameraRow adds DB-only fields: Description, Location, Brand, Model, SerialNumber,
// RetentionDays, Status, LastSeen, HasPassword, merge config, archive fields.
type CameraRow struct {
	ID            string               `json:"id"`
	Name          string               `json:"name"`
	Protocol      string               `json:"protocol"`
	Encoding      string               `json:"encoding"`
	RTSPTransport string               `json:"rtsp_transport"`
	URL           string               `json:"url"`
	Enabled       bool                 `json:"enabled"`
	Description   string               `json:"description"`
	Location      string               `json:"location"`
	Brand         string               `json:"brand"`
	Model         string               `json:"model"`
	SerialNumber  string               `json:"serial_number"`
	RetentionDays int                  `json:"retention_days"`
	Status        model.RecorderStatus `json:"status"`
	ErrorType     *string              `json:"error_type"`
	ErrorDetail   *string              `json:"error_detail"`
	LastSeen      *time.Time           `json:"last_seen,omitempty"`
	Username      string               `json:"username"`
	HasPassword   bool                 `json:"has_password"`
	// Per-camera merge config (nil = use global)
	MergeEnabled            *bool      `json:"merge_enabled,omitempty"`
	MergeCheckInterval      *string    `json:"merge_check_interval,omitempty"`
	MergeWindowSize         *string    `json:"merge_window_size,omitempty"`
	MergeBatchLimit         *int       `json:"merge_batch_limit,omitempty"`
	MergeMinSegmentAge      *string    `json:"merge_min_segment_age,omitempty"`
	MergeMinSegmentsToMerge *int       `json:"merge_min_segments_to_merge,omitempty"`
	MergeRollingEnabled     *bool      `json:"merge_rolling_enabled,omitempty"`
	MergeRollingDebounce    *string    `json:"merge_rolling_debounce,omitempty"`
	ONVIFEndpoint           string     `json:"onvif_endpoint"`
	ProfileToken            string     `json:"profile_token"`
	ProfileName             string     `json:"profile_name"`
	StreamEncoding          string     `json:"stream_encoding"`
	Archived                bool       `json:"archived"`
	ArchivedAt              *time.Time `json:"archived_at,omitempty"`
	ArchiveRetentionDays    int        `json:"archive_retention_days"`
	// AudioEnabled is injected from camera config extras at API response time.
	AudioEnabled bool `json:"audio_enabled,omitempty"`
	// SourceType describes ingest origin (rtmp_push, srt_push, relay_pull, etc.).
	SourceType string `json:"source_type,omitempty"`
	// RecordingPaused is injected at API response time (not stored in DB)
	RecordingPaused bool `json:"recording_paused"`
	// RecordingMode: continuous (default) | scheduled | off | event | adaptive
	RecordingMode string `json:"recording_mode"`
	// ActivationState: active (default) | pending_activation
	ActivationState string `json:"activation_state,omitempty"`
	// StableID is the ONVIF serial used for IP self-healing.
	StableID string `json:"stable_id,omitempty"`
	// SubStreamURL is injected from extras at API response time.
	SubStreamURL string `json:"sub_stream_url,omitempty"`
	// SubProfileToken is the ONVIF sub-stream profile token.
	SubProfileToken string `json:"sub_profile_token,omitempty"`
	// SubnetHints are extra /24 (or smaller) CIDRs for IP rediscovery.
	SubnetHints []string `json:"subnet_hints,omitempty"`
	// Adaptive holds sparse-recording settings when recording_mode=adaptive.
	Adaptive *config.CameraAdaptiveConfig `json:"adaptive,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

func (d *DB) ListCameras(ctx context.Context) ([]CameraRow, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT id, name, protocol, encoding, rtsp_transport, url, enabled, description, location, brand, model, serial_number, retention_days, username, CASE WHEN password IS NOT NULL AND password != '' THEN 1 ELSE 0 END as has_password,
		merge_enabled, merge_check_interval, merge_window_size, merge_batch_limit, merge_min_segment_age, merge_min_segments_to_merge, merge_rolling_enabled, merge_rolling_debounce,
		onvif_endpoint, profile_token, profile_name, stream_encoding,
		archived, archived_at, archive_retention_days, COALESCE(recording_mode,'continuous'),
		COALESCE(activation_state,'active'), COALESCE(stable_id,''), created_at
		FROM cameras WHERE archived=0 ORDER BY id;`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []CameraRow
	for rows.Next() {
		var c CameraRow
		var mergeEnabled, mergeRollingEnabled sql.NullBool
		var mergeCheckInterval, mergeWindowSize, mergeMinSegmentAge, mergeRollingDebounce sql.NullString
		var mergeBatchLimit, mergeMinSegmentsToMerge sql.NullInt64
		var archivedAtStr, createdAtStr sql.NullString
		if err := rows.Scan(&c.ID, &c.Name, &c.Protocol, &c.Encoding, &c.RTSPTransport, &c.URL, &c.Enabled, &c.Description, &c.Location, &c.Brand, &c.Model, &c.SerialNumber, &c.RetentionDays, &c.Username, &c.HasPassword,
			&mergeEnabled, &mergeCheckInterval, &mergeWindowSize, &mergeBatchLimit, &mergeMinSegmentAge, &mergeMinSegmentsToMerge, &mergeRollingEnabled, &mergeRollingDebounce,
			&c.ONVIFEndpoint, &c.ProfileToken, &c.ProfileName, &c.StreamEncoding,
			&c.Archived, &archivedAtStr, &c.ArchiveRetentionDays, &c.RecordingMode, &c.ActivationState, &c.StableID, &createdAtStr); err != nil {
			return nil, err
		}
		c.MergeEnabled = nullBoolToPtr(mergeEnabled)
		c.MergeCheckInterval = nullStringToPtr(mergeCheckInterval)
		c.MergeWindowSize = nullStringToPtr(mergeWindowSize)
		c.MergeBatchLimit = nullInt64ToPtr(mergeBatchLimit)
		c.MergeMinSegmentAge = nullStringToPtr(mergeMinSegmentAge)
		c.MergeMinSegmentsToMerge = nullInt64ToPtr(mergeMinSegmentsToMerge)
		c.MergeRollingEnabled = nullBoolToPtr(mergeRollingEnabled)
		c.MergeRollingDebounce = nullStringToPtr(mergeRollingDebounce)
		if archivedAtStr.Valid && archivedAtStr.String != "" {
			t := scanTime(archivedAtStr)
			c.ArchivedAt = &t
		}
		if createdAtStr.Valid && createdAtStr.String != "" {
			c.CreatedAt = scanTime(createdAtStr)
		}
		res = append(res, c)
	}
	return res, nil
}

// ListArchivedCameras returns only cameras marked as archived.
func (d *DB) ListArchivedCameras(ctx context.Context) ([]CameraRow, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT id, name, protocol, encoding, rtsp_transport, url, enabled, description, location, brand, model, serial_number, retention_days, username, CASE WHEN password IS NOT NULL AND password != '' THEN 1 ELSE 0 END as has_password,
		merge_enabled, merge_check_interval, merge_window_size, merge_batch_limit, merge_min_segment_age, merge_min_segments_to_merge, merge_rolling_enabled, merge_rolling_debounce,
		onvif_endpoint, profile_token, profile_name, stream_encoding,
		archived, archived_at, archive_retention_days, COALESCE(recording_mode,'continuous'),
		COALESCE(activation_state,'active'), COALESCE(stable_id,''), created_at
		FROM cameras WHERE archived=1 ORDER BY id;`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []CameraRow
	for rows.Next() {
		var c CameraRow
		var mergeEnabled, mergeRollingEnabled sql.NullBool
		var mergeCheckInterval, mergeWindowSize, mergeMinSegmentAge, mergeRollingDebounce sql.NullString
		var mergeBatchLimit, mergeMinSegmentsToMerge sql.NullInt64
		var archivedAtStr, createdAtStr sql.NullString
		if err := rows.Scan(&c.ID, &c.Name, &c.Protocol, &c.Encoding, &c.RTSPTransport, &c.URL, &c.Enabled, &c.Description, &c.Location, &c.Brand, &c.Model, &c.SerialNumber, &c.RetentionDays, &c.Username, &c.HasPassword,
			&mergeEnabled, &mergeCheckInterval, &mergeWindowSize, &mergeBatchLimit, &mergeMinSegmentAge, &mergeMinSegmentsToMerge, &mergeRollingEnabled, &mergeRollingDebounce,
			&c.ONVIFEndpoint, &c.ProfileToken, &c.ProfileName, &c.StreamEncoding,
			&c.Archived, &archivedAtStr, &c.ArchiveRetentionDays, &c.RecordingMode, &c.ActivationState, &c.StableID, &createdAtStr); err != nil {
			return nil, err
		}
		c.MergeEnabled = nullBoolToPtr(mergeEnabled)
		c.MergeCheckInterval = nullStringToPtr(mergeCheckInterval)
		c.MergeWindowSize = nullStringToPtr(mergeWindowSize)
		c.MergeBatchLimit = nullInt64ToPtr(mergeBatchLimit)
		c.MergeMinSegmentAge = nullStringToPtr(mergeMinSegmentAge)
		c.MergeMinSegmentsToMerge = nullInt64ToPtr(mergeMinSegmentsToMerge)
		c.MergeRollingEnabled = nullBoolToPtr(mergeRollingEnabled)
		c.MergeRollingDebounce = nullStringToPtr(mergeRollingDebounce)
		if archivedAtStr.Valid && archivedAtStr.String != "" {
			t := scanTime(archivedAtStr)
			c.ArchivedAt = &t
		}
		if createdAtStr.Valid && createdAtStr.String != "" {
			c.CreatedAt = scanTime(createdAtStr)
		}
		res = append(res, c)
	}
	return res, nil
}

// UpsertCamera inserts or updates a camera record in the database
func (d *DB) UpsertCamera(ctx context.Context, id, name, protocol, encoding, url, username, password string, enabled bool, onvifEndpoint, profileToken, streamEncoding string, extras ...string) error {
	rtspTransport := "tcp"
	if len(extras) > 0 && extras[0] != "" {
		rtspTransport = extras[0]
	}

	q := `INSERT INTO cameras(id, name, protocol, encoding, rtsp_transport, url, username, password, enabled, onvif_endpoint, profile_token, stream_encoding) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)

         ON CONFLICT(id) DO UPDATE SET name=excluded.name, protocol=excluded.protocol, encoding=excluded.encoding, rtsp_transport=excluded.rtsp_transport, url=excluded.url, username=excluded.username, password=excluded.password, enabled=excluded.enabled, onvif_endpoint=excluded.onvif_endpoint, profile_token=excluded.profile_token, stream_encoding=excluded.stream_encoding;`

	_, err := d.db.ExecContext(ctx, q, id, name, protocol, encoding, rtspTransport, url, username, password, enabled, onvifEndpoint, profileToken, streamEncoding)

	return err
}

// UpdateCameraProfileName updates the profile_name for a camera.
func (d *DB) UpdateCameraProfileName(ctx context.Context, id, profileName string) error {
	q := `UPDATE cameras SET profile_name=? WHERE id=?;`
	_, err := d.db.ExecContext(ctx, q, profileName, id)
	return err
}

// UpdateCameraRecordingMode updates the recording_mode for a camera.
func (d *DB) UpdateCameraRecordingMode(ctx context.Context, id, mode string) error {
	_, err := d.db.ExecContext(ctx, `UPDATE cameras SET recording_mode=? WHERE id=?;`, mode, id)
	return err
}

func (d *DB) GetCamera(ctx context.Context, cameraID string) (*CameraRow, error) {
	var c CameraRow
	var mergeEnabled, mergeRollingEnabled sql.NullBool
	var mergeCheckInterval, mergeWindowSize, mergeMinSegmentAge, mergeRollingDebounce sql.NullString
	var mergeBatchLimit, mergeMinSegmentsToMerge sql.NullInt64
	var archivedAtStr, createdAtStr sql.NullString
	err := d.db.QueryRowContext(ctx, `SELECT id, name, protocol, encoding, rtsp_transport, url, enabled, description, location, brand, model, serial_number, retention_days, username, CASE WHEN password IS NOT NULL AND password != '' THEN 1 ELSE 0 END as has_password,
		merge_enabled, merge_check_interval, merge_window_size, merge_batch_limit, merge_min_segment_age, merge_min_segments_to_merge, merge_rolling_enabled, merge_rolling_debounce,
		onvif_endpoint, profile_token, profile_name, stream_encoding,
		archived, archived_at, archive_retention_days, COALESCE(recording_mode,'continuous'),
		COALESCE(activation_state,'active'), COALESCE(stable_id,''), created_at
		FROM cameras WHERE id = ?`, cameraID).Scan(
		&c.ID, &c.Name, &c.Protocol, &c.Encoding, &c.RTSPTransport, &c.URL, &c.Enabled, &c.Description, &c.Location, &c.Brand, &c.Model, &c.SerialNumber, &c.RetentionDays, &c.Username, &c.HasPassword,
		&mergeEnabled, &mergeCheckInterval, &mergeWindowSize, &mergeBatchLimit, &mergeMinSegmentAge, &mergeMinSegmentsToMerge, &mergeRollingEnabled, &mergeRollingDebounce,
		&c.ONVIFEndpoint, &c.ProfileToken, &c.ProfileName, &c.StreamEncoding,
		&c.Archived, &archivedAtStr, &c.ArchiveRetentionDays, &c.RecordingMode, &c.ActivationState, &c.StableID, &createdAtStr)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	c.MergeEnabled = nullBoolToPtr(mergeEnabled)
	c.MergeCheckInterval = nullStringToPtr(mergeCheckInterval)
	c.MergeWindowSize = nullStringToPtr(mergeWindowSize)
	c.MergeBatchLimit = nullInt64ToPtr(mergeBatchLimit)
	c.MergeMinSegmentAge = nullStringToPtr(mergeMinSegmentAge)
	c.MergeMinSegmentsToMerge = nullInt64ToPtr(mergeMinSegmentsToMerge)
	c.MergeRollingEnabled = nullBoolToPtr(mergeRollingEnabled)
	c.MergeRollingDebounce = nullStringToPtr(mergeRollingDebounce)
	if archivedAtStr.Valid && archivedAtStr.String != "" {
		t := scanTime(archivedAtStr)
		c.ArchivedAt = &t
	}
	if createdAtStr.Valid && createdAtStr.String != "" {
		c.CreatedAt = scanTime(createdAtStr)
	}
	return &c, nil
}

// DeleteCamera removes a camera record from the database.
// Returns an error if the camera does not exist.
func (d *DB) DeleteCamera(ctx context.Context, cameraID string) error {
	res, err := d.db.ExecContext(ctx, `DELETE FROM cameras WHERE id = ?;`, cameraID)
	if err != nil {
		return err
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// UpdateCameraMetadata updates DB-only metadata fields for a camera.
func (d *DB) UpdateCameraMetadata(ctx context.Context, id, description, location, brand, model, serialNumber string, retentionDays int) error {
	q := `UPDATE cameras SET description=?, location=?, brand=?, model=?, serial_number=?, retention_days=? WHERE id=?;`
	_, err := d.db.ExecContext(ctx, q, description, location, brand, model, serialNumber, retentionDays, id)
	return err
}

// StreamBinding represents a binding between a stream and a camera.
type StreamBinding struct {
	StreamID  string    `json:"stream_id"`
	CameraID  string    `json:"camera_id"`
	CreatedAt time.Time `json:"created_at"`
}

// BindStreamToCamera creates a binding between a stream and a camera.
func (d *DB) BindStreamToCamera(ctx context.Context, streamID, cameraID string) error {
	q := `INSERT OR REPLACE INTO stream_bindings (stream_id, camera_id) VALUES (?, ?);`
	_, err := d.db.ExecContext(ctx, q, streamID, cameraID)
	return err
}

// UnbindStreamFromCamera removes the binding between a stream and a camera.
func (d *DB) UnbindStreamFromCamera(ctx context.Context, streamID string) error {
	res, err := d.db.ExecContext(ctx, `DELETE FROM stream_bindings WHERE stream_id = ?;`, streamID)
	if err != nil {
		return err
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// GetStreamBinding returns the binding for a given stream.
func (d *DB) GetStreamBinding(ctx context.Context, streamID string) (*StreamBinding, error) {
	var b StreamBinding
	err := d.db.QueryRowContext(ctx,
		`SELECT stream_id, camera_id, created_at FROM stream_bindings WHERE stream_id = ?;`,
		streamID).Scan(&b.StreamID, &b.CameraID, &b.CreatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &b, nil
}

// ListStreamBindings returns all stream-camera bindings.
func (d *DB) ListStreamBindings(ctx context.Context) ([]StreamBinding, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT stream_id, camera_id, created_at FROM stream_bindings ORDER BY created_at DESC;`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var bindings []StreamBinding
	for rows.Next() {
		var b StreamBinding
		if err := rows.Scan(&b.StreamID, &b.CameraID, &b.CreatedAt); err != nil {
			return nil, err
		}
		bindings = append(bindings, b)
	}
	return bindings, nil
}

// GetBindingByCameraID returns the binding for a given camera.
func (d *DB) GetBindingByCameraID(ctx context.Context, cameraID string) (*StreamBinding, error) {
	var b StreamBinding
	err := d.db.QueryRowContext(ctx,
		`SELECT stream_id, camera_id, created_at FROM stream_bindings WHERE camera_id = ?;`,
		cameraID).Scan(&b.StreamID, &b.CameraID, &b.CreatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &b, nil
}
