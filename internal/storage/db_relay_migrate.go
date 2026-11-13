package storage

import (
    "context"
    "fmt"
)

// migrateRelayTranscodeJob is idempotent on all supported SQL drivers. Old
// source-only relay tasks retain the empty job reference and old semantics.
func (d *DB) migrateRelayTranscodeJob(ctx context.Context) error {
    var count int
    var query string
    switch d.driver {
    case DriverSQLite:
        query = "SELECT COUNT(*) FROM pragma_table_info('relay_tasks') WHERE name = 'transcode_job_id'"
    case DriverPostgres:
        query = "SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'relay_tasks' AND column_name = 'transcode_job_id'"
    case DriverMySQL:
        query = "SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'relay_tasks' AND column_name = 'transcode_job_id'"
    default:
        return fmt.Errorf("unsupported database driver for relay migration")
    }
    if err := d.queryRowContext(ctx, query).Scan(&count); err != nil { return fmt.Errorf("inspect relay schema: %w",err) }
    if count == 0 {
        if _, err := d.execContext(ctx, "ALTER TABLE relay_tasks ADD COLUMN transcode_job_id TEXT NOT NULL DEFAULT ''"); err != nil {
            return fmt.Errorf("add relay transcode job column: %w",err)
        }
    }
    return nil
}
