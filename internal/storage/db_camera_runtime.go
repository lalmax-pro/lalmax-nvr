package storage

import (
	"context"
	"database/sql"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
)

// SaveCameraExtras persists non-column per-camera settings as JSON.
func (d *DB) SaveCameraExtras(ctx context.Context, cam config.CameraConfig) error {
	raw, err := marshalCameraExtras(cam)
	if err != nil {
		return err
	}
	_, err = d.db.ExecContext(ctx, `UPDATE cameras SET extras_json=? WHERE id=?`, raw, cam.ID)
	return err
}

// ListCameraConfigs returns runtime camera configs from the database (including passwords).
func (d *DB) ListCameraConfigs(ctx context.Context) ([]config.CameraConfig, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT id, name, protocol, encoding, rtsp_transport, url, username, password, enabled,
		onvif_endpoint, profile_token, stream_encoding, extras_json,
		merge_enabled, merge_check_interval, merge_window_size, merge_batch_limit, merge_min_segment_age, merge_min_segments_to_merge, merge_rolling_enabled, merge_rolling_debounce,
		COALESCE(recording_mode,'continuous'), COALESCE(activation_state,'active'), COALESCE(stable_id,'')
		FROM cameras WHERE archived=0 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var configs []config.CameraConfig
	for rows.Next() {
		var cam config.CameraConfig
		var extrasRaw sql.NullString
		var mergeEnabled, mergeRollingEnabled sql.NullBool
		var mergeCheckInterval, mergeWindowSize, mergeMinSegmentAge, mergeRollingDebounce sql.NullString
		var mergeBatchLimit, mergeMinSegmentsToMerge sql.NullInt64
		if err := rows.Scan(
			&cam.ID, &cam.Name, &cam.Protocol, &cam.Encoding, &cam.RTSPTransport, &cam.URL, &cam.Username, &cam.Password, &cam.Enabled,
			&cam.ONVIFEndpoint, &cam.ProfileToken, &cam.StreamEncoding, &extrasRaw,
			&mergeEnabled, &mergeCheckInterval, &mergeWindowSize, &mergeBatchLimit, &mergeMinSegmentAge, &mergeMinSegmentsToMerge, &mergeRollingEnabled, &mergeRollingDebounce,
			&cam.RecordingMode, &cam.ActivationState, &cam.StableID,
		); err != nil {
			return nil, err
		}
		cam.RTSPTransport = config.NormalizeRTSPTransport(cam.RTSPTransport)
		if extrasRaw.Valid {
			raw := extrasRaw.String
			extras, err := unmarshalCameraExtras(raw)
			if err != nil {
				return nil, err
			}
			applyExtrasToCamera(&cam, extras)
			if !extrasHasAudioEnabled(raw) {
				config.ApplyCameraAudioDefault(&cam)
			}
		} else {
			config.ApplyCameraAudioDefault(&cam)
		}
		row := CameraRow{
			MergeEnabled:            nullBoolToPtr(mergeEnabled),
			MergeCheckInterval:      nullStringToPtr(mergeCheckInterval),
			MergeWindowSize:         nullStringToPtr(mergeWindowSize),
			MergeBatchLimit:         nullInt64ToPtr(mergeBatchLimit),
			MergeMinSegmentAge:      nullStringToPtr(mergeMinSegmentAge),
			MergeMinSegmentsToMerge: nullInt64ToPtr(mergeMinSegmentsToMerge),
			MergeRollingEnabled:     nullBoolToPtr(mergeRollingEnabled),
			MergeRollingDebounce:    nullStringToPtr(mergeRollingDebounce),
		}
		cam.Merge = mergeConfigFromRow(row)
		configs = append(configs, cam)
	}
	return configs, rows.Err()
}
