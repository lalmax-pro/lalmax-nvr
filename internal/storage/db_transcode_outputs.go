package storage

import "context"

// RegisterTranscodeOutput records trusted physical identities before publication.
// This registry is not inferred from prefixes and survives process restarts.
func (d *DB) RegisterTranscodeOutput(ctx context.Context, id, sourceID string) error {
	_, err := d.execContext(ctx, `INSERT INTO transcode_outputs_v1 (id, source_id) VALUES (?, ?)`, id, sourceID)
	return err
}
func (d *DB) ListTranscodeOutputIDs(ctx context.Context) ([]string, error) {
	rows, err := d.queryContext(ctx, `SELECT id FROM transcode_outputs_v1 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
