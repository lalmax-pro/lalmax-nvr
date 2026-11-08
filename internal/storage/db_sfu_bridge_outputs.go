package storage

import "context"

// SFU bridge output identities outlive the process to prevent a retired stream
// name from falling back to an open publish/playback policy after a restart.
// Credentials and participant information are never persisted here.
func (d *DB) createSFUBridgeOutputsTable(ctx context.Context) error {
	_, err := d.execContext(ctx, `CREATE TABLE IF NOT EXISTS sfu_bridge_outputs_v1 (id TEXT PRIMARY KEY)`)
	return err
}
func (d *DB) RegisterSFUBridgeOutput(ctx context.Context, id string) error {
	_, err := d.execContext(ctx, `INSERT INTO sfu_bridge_outputs_v1 (id) VALUES (?)`, id)
	return err
}

// IsSFUBridgeOutput distinguishes bridge identities from other internal streams.
func (d *DB) IsSFUBridgeOutput(ctx context.Context, id string) (bool, error) {
	var exists bool
	err := d.queryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sfu_bridge_outputs_v1 WHERE id = ?)`, id).Scan(&exists)
	return exists, err
}
func (d *DB) ListSFUBridgeOutputIDs(ctx context.Context) ([]string, error) {
	rows, err := d.queryContext(ctx, `SELECT id FROM sfu_bridge_outputs_v1 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
