package storage

import (
	"context"
	"database/sql"
	"strings"

	"github.com/lalmax-pro/lalmax-nvr/internal/config"
)

func (d *DB) migrateActivationIdentity(ctx context.Context) error {
	var activationCol int
	_ = d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('cameras') WHERE name='activation_state'`).Scan(&activationCol)
	if activationCol == 0 {
		if _, err := d.db.ExecContext(ctx, `ALTER TABLE cameras ADD COLUMN activation_state TEXT NOT NULL DEFAULT 'active'`); err != nil {
			return err
		}
	}
	var stableCol int
	_ = d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('cameras') WHERE name='stable_id'`).Scan(&stableCol)
	if stableCol == 0 {
		if _, err := d.db.ExecContext(ctx, `ALTER TABLE cameras ADD COLUMN stable_id TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	_, _ = d.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_cameras_stable_id ON cameras(stable_id)`)
	_, _ = d.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_cameras_serial_number ON cameras(serial_number)`)
	_, _ = d.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_cameras_onvif_endpoint ON cameras(onvif_endpoint)`)
	_, _ = d.db.ExecContext(ctx, "UPDATE schema_meta SET value='26' WHERE key='schema_version'")
	return nil
}

func (d *DB) migrateMergeRolling(ctx context.Context) error {
	var col int
	_ = d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('cameras') WHERE name='merge_rolling_enabled'`).Scan(&col)
	if col == 0 {
		if _, err := d.db.ExecContext(ctx, `ALTER TABLE cameras ADD COLUMN merge_rolling_enabled INTEGER`); err != nil {
			return err
		}
	}
	_ = d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('cameras') WHERE name='merge_rolling_debounce'`).Scan(&col)
	if col == 0 {
		if _, err := d.db.ExecContext(ctx, `ALTER TABLE cameras ADD COLUMN merge_rolling_debounce TEXT`); err != nil {
			return err
		}
	}
	_, _ = d.db.ExecContext(ctx, "UPDATE schema_meta SET value='27' WHERE key='schema_version'")
	return nil
}

// UpdateCameraActivation persists activation_state for a camera.
func (d *DB) UpdateCameraActivation(ctx context.Context, id, state string) error {
	if state == "" {
		state = config.ActivationActive
	}
	_, err := d.db.ExecContext(ctx, `UPDATE cameras SET activation_state=? WHERE id=?`, state, id)
	return err
}

// UpdateCameraStableID persists the ONVIF serial used for IP self-healing.
func (d *DB) UpdateCameraStableID(ctx context.Context, id, stableID string) error {
	_, err := d.db.ExecContext(ctx, `UPDATE cameras SET stable_id=? WHERE id=?`, stableID, id)
	return err
}

// FindCameraByEndpoint returns a camera (including archived) matching the ONVIF endpoint.
func (d *DB) FindCameraByEndpoint(ctx context.Context, endpoint string) (*CameraRow, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return nil, nil
	}
	return d.getCameraByQuery(ctx, `SELECT id FROM cameras WHERE onvif_endpoint = ? OR url = ? LIMIT 1`, endpoint, endpoint)
}

// FindCameraBySerial returns a camera (including archived) matching serial_number or stable_id.
func (d *DB) FindCameraBySerial(ctx context.Context, serial string) (*CameraRow, error) {
	serial = strings.TrimSpace(serial)
	if serial == "" {
		return nil, nil
	}
	return d.getCameraByQuery(ctx, `SELECT id FROM cameras WHERE serial_number = ? OR stable_id = ? LIMIT 1`, serial, serial)
}

func (d *DB) getCameraByQuery(ctx context.Context, query string, args ...any) (*CameraRow, error) {
	var id string
	err := d.db.QueryRowContext(ctx, query, args...).Scan(&id)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return d.GetCamera(ctx, id)
}
