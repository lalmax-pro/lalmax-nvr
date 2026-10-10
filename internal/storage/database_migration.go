package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"regexp"
	"strings"
)

var migrationIdentifierRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

const migrationMarkerTable = "lalmax_migration_meta"

// MigrationSummary reports the number of rows copied per table.
type MigrationSummary struct {
	SourceDriver      DatabaseDriver   `json:"source_driver"`
	DestinationDriver DatabaseDriver   `json:"destination_driver"`
	RowsCopied        map[string]int64 `json:"rows_copied"`
	TotalRows         int64            `json:"total_rows"`
}

type migrationExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (d *DB) backupAsSQLite(ctx context.Context, destPath string) (retErr error) {
	if destPath == "" {
		return fmt.Errorf("storage: backup destination path must not be empty")
	}
	if _, err := os.Stat(destPath); err == nil {
		return fmt.Errorf("storage: backup destination already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	backup, err := New(destPath)
	if err != nil {
		return err
	}
	completed := false
	defer func() {
		closeErr := backup.Close()
		if retErr == nil && closeErr != nil {
			retErr = closeErr
		}
		if !completed {
			_ = os.Remove(destPath)
		}
	}()
	if err := backup.Init(ctx); err != nil {
		return err
	}

	d.dbMu.Lock()
	defer d.dbMu.Unlock()
	tables, err := listTables(ctx, d.db, d.driver)
	if err != nil {
		return err
	}
	targetTables, err := listDatabaseTables(ctx, backup)
	if err != nil {
		return err
	}
	tx, err := backup.beginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := clearDatabaseTables(ctx, tx, DriverSQLite, targetTables); err != nil {
		return err
	}
	for _, table := range tables {
		if table == migrationMarkerTable {
			continue
		}
		copied, err := copyTable(ctx, d, DriverSQLite, tx, table)
		if err != nil {
			return fmt.Errorf("storage: backup table %s: %w", table, err)
		}
		count, err := countTable(ctx, DriverSQLite, tx, table)
		if err != nil {
			return err
		}
		if copied != count {
			return fmt.Errorf("storage: backup row count mismatch for %s", table)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	completed = true
	return nil
}

// MigrateTo copies a SQLite database into a new, empty PostgreSQL or MySQL
// database and switches this DB instance to the new connection. The callback
// lets the caller persist the active DSN after validation and before cutover.
func (d *DB) MigrateTo(ctx context.Context, driver DatabaseDriver, dsn string, beforeCutover func() error) (MigrationSummary, error) {
	if driver != DriverPostgres && driver != DriverMySQL {
		return MigrationSummary{}, fmt.Errorf("storage: migration destination must be postgres or mysql")
	}
	destination, err := Open(ctx, DatabaseOptions{Driver: driver, DSN: dsn, MaxOpenConns: 1, MaxIdleConns: 1})
	if err != nil {
		return MigrationSummary{}, err
	}
	defer func() {
		if destination.db != nil {
			_ = destination.Close()
		}
	}()

	// The exclusive lock pauses storage operations during the copy. This avoids
	// missing concurrent writes between the final row validation and cutover.
	d.dbMu.Lock()
	defer d.dbMu.Unlock()
	if d.driver != DriverSQLite {
		return MigrationSummary{}, fmt.Errorf("storage: only SQLite can be used as a migration source currently")
	}
	if d.db == nil {
		return MigrationSummary{}, fmt.Errorf("storage: source database is closed")
	}
	tables, err := listTables(ctx, d.db, d.driver)
	if err != nil {
		return MigrationSummary{}, err
	}

	preexisting, err := listDatabaseTables(ctx, destination)
	if err != nil {
		return MigrationSummary{}, err
	}
	migrationID := migrationIdentity(d.path, driver)
	resuming, err := isResumableMigrationTarget(ctx, destination, preexisting, tables, migrationID)
	if err != nil {
		return MigrationSummary{}, err
	}
	if len(preexisting) != 0 && !resuming {
		return MigrationSummary{}, fmt.Errorf("storage: migration destination must be an empty database")
	}
	if !resuming {
		if _, err := destination.execContext(ctx, `CREATE TABLE lalmax_migration_meta (migration_id TEXT PRIMARY KEY NOT NULL)`); err != nil {
			return MigrationSummary{}, fmt.Errorf("storage: mark migration destination: %w", err)
		}
		if _, err := destination.execContext(ctx, `INSERT INTO lalmax_migration_meta (migration_id) VALUES (?)`, migrationID); err != nil {
			return MigrationSummary{}, err
		}
	}
	if err := destination.Init(ctx); err != nil {
		return MigrationSummary{}, fmt.Errorf("storage: initialize migration destination: %w", err)
	}
	targetTables, err := listDatabaseTables(ctx, destination)
	if err != nil {
		return MigrationSummary{}, err
	}
	tx, err := destination.beginTx(ctx, nil)
	if err != nil {
		return MigrationSummary{}, fmt.Errorf("storage: begin destination migration transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := clearDatabaseTables(ctx, tx, destination.driver, targetTables); err != nil {
		return MigrationSummary{}, fmt.Errorf("storage: clear destination initialization data: %w", err)
	}

	summary := MigrationSummary{SourceDriver: d.driver, DestinationDriver: driver, RowsCopied: make(map[string]int64)}
	for _, table := range tables {
		copied, err := copyTable(ctx, d, destination.driver, tx, table)
		if err != nil {
			return MigrationSummary{}, fmt.Errorf("storage: copy table %s: %w", table, err)
		}
		count, err := countTable(ctx, destination.driver, tx, table)
		if err != nil {
			return MigrationSummary{}, fmt.Errorf("storage: verify table %s: %w", table, err)
		}
		if count != copied {
			return MigrationSummary{}, fmt.Errorf("storage: row count mismatch for %s: copied %d, found %d", table, copied, count)
		}
		summary.RowsCopied[table] = copied
		summary.TotalRows += copied
	}
	if driver == DriverPostgres {
		if err := resetPostgresSequences(ctx, tx, tables); err != nil {
			return MigrationSummary{}, fmt.Errorf("storage: reset destination sequences: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return MigrationSummary{}, fmt.Errorf("storage: commit destination migration: %w", err)
	}
	committed = true
	if beforeCutover != nil {
		if err := beforeCutover(); err != nil {
			return MigrationSummary{}, fmt.Errorf("storage: persist destination configuration: %w", err)
		}
	}

	oldDB := d.db
	d.db = destination.db
	d.retired = append(d.retired, oldDB)
	d.driver = driver
	d.path = dsn
	destination.db = nil // ownership moves to d
	d.invalidateRecordingsCache()
	return summary, nil
}

func listDatabaseTables(ctx context.Context, db *DB) ([]string, error) {
	db.dbMu.RLock()
	defer db.dbMu.RUnlock()
	return listTables(ctx, db.db, db.driver)
}

func listTables(ctx context.Context, connection *sql.DB, driver DatabaseDriver) ([]string, error) {
	var query string
	switch driver {
	case DriverSQLite:
		query = `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY rowid`
	case DriverPostgres:
		query = `SELECT tablename FROM pg_tables WHERE schemaname = current_schema() ORDER BY tablename`
	case DriverMySQL:
		query = `SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE() AND table_type = 'BASE TABLE' ORDER BY table_name`
	default:
		return nil, fmt.Errorf("storage: unsupported driver %s", driver)
	}
	rows, err := connection.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return nil, err
		}
		if !migrationIdentifierRE.MatchString(table) {
			return nil, fmt.Errorf("storage: unsafe table identifier %q", table)
		}
		tables = append(tables, table)
	}
	return tables, rows.Err()
}

func migrationIdentity(source string, driver DatabaseDriver) string {
	sum := sha256.Sum256([]byte(string(driver) + "\x00" + source))
	return fmt.Sprintf("%x", sum[:])
}

func isResumableMigrationTarget(ctx context.Context, destination *DB, existing, source []string, migrationID string) (bool, error) {
	markerFound := false
	sourceSet := make(map[string]struct{}, len(source))
	for _, table := range source {
		if table == migrationMarkerTable {
			return false, fmt.Errorf("storage: source database contains reserved table %s", migrationMarkerTable)
		}
		sourceSet[table] = struct{}{}
	}
	for _, table := range existing {
		if table == migrationMarkerTable {
			markerFound = true
			continue
		}
		if _, ok := sourceSet[table]; !ok {
			return false, nil
		}
	}
	if !markerFound {
		return false, nil
	}
	var foundID string
	if err := destination.queryRowContext(ctx, `SELECT migration_id FROM lalmax_migration_meta LIMIT 1`).Scan(&foundID); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	if foundID != migrationID {
		return false, nil
	}
	return true, nil
}

func migrationQuoteIdentifier(identifier string, driver DatabaseDriver) (string, error) {
	if !migrationIdentifierRE.MatchString(identifier) {
		return "", fmt.Errorf("unsafe SQL identifier %q", identifier)
	}
	if driver == DriverMySQL {
		return "`" + identifier + "`", nil
	}
	return `"` + identifier + `"`, nil
}

func tableColumns(ctx context.Context, source *DB, table string) ([]string, map[string]string, error) {
	quoted, err := migrationQuoteIdentifier(table, source.driver)
	if err != nil {
		return nil, nil, err
	}
	query := `PRAGMA table_info(` + quoted + `)`
	if source.driver == DriverPostgres {
		query = `SELECT column_name, data_type FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = $1 ORDER BY ordinal_position`
	} else if source.driver == DriverMySQL {
		query = `SELECT column_name, data_type FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position`
	}
	var rows *sql.Rows
	if source.driver == DriverSQLite {
		rows, err = source.db.QueryContext(ctx, query)
	} else {
		rows, err = source.db.QueryContext(ctx, source.dialectQuery(query), table)
	}
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	columns := make([]string, 0)
	types := make(map[string]string)
	for rows.Next() {
		var name, dataType string
		if source.driver == DriverSQLite {
			var cid, notNull, primaryKey int
			var defaultValue any
			if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
				return nil, nil, err
			}
		} else if err := rows.Scan(&name, &dataType); err != nil {
			return nil, nil, err
		}
		columns = append(columns, name)
		types[name] = strings.ToUpper(strings.TrimSpace(dataType))
	}
	return columns, types, rows.Err()
}

func clearDatabaseTables(ctx context.Context, executor migrationExecutor, driver DatabaseDriver, tables []string) error {
	for i := len(tables) - 1; i >= 0; i-- {
		if tables[i] == migrationMarkerTable {
			continue
		}
		quoted, err := migrationQuoteIdentifier(tables[i], driver)
		if err != nil {
			return err
		}
		if _, err := executor.ExecContext(ctx, "DELETE FROM "+quoted); err != nil {
			return fmt.Errorf("delete %s: %w", tables[i], err)
		}
	}
	return nil
}

func copyTable(ctx context.Context, source *DB, destinationDriver DatabaseDriver, executor migrationExecutor, table string) (int64, error) {
	sourceTable, err := migrationQuoteIdentifier(table, source.driver)
	if err != nil {
		return 0, err
	}
	destinationTable, err := migrationQuoteIdentifier(table, destinationDriver)
	if err != nil {
		return 0, err
	}
	columns, columnTypes, err := tableColumns(ctx, source, table)
	if err != nil {
		return 0, err
	}
	if len(columns) == 0 {
		return 0, nil
	}
	sourceColumns := make([]string, len(columns))
	destinationColumns := make([]string, len(columns))
	for i, column := range columns {
		sourceColumns[i], err = migrationQuoteIdentifier(column, source.driver)
		if err != nil {
			return 0, err
		}
		destinationColumns[i], err = migrationQuoteIdentifier(column, destinationDriver)
		if err != nil {
			return 0, err
		}
	}
	rows, err := source.db.QueryContext(ctx, "SELECT "+strings.Join(sourceColumns, ",")+" FROM "+sourceTable)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	rowPlaceholders := "(" + strings.TrimSuffix(strings.Repeat("?,", len(columns)), ",") + ")"
	count := int64(0)
	values := make([]any, len(columns))
	scanTargets := make([]any, len(columns))
	for i := range values {
		scanTargets[i] = &values[i]
	}
	batch := make([][]any, 0, 50)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		placeholders := make([]string, len(batch))
		args := make([]any, 0, len(batch)*len(columns))
		for i, row := range batch {
			placeholders[i] = rowPlaceholders
			args = append(args, row...)
		}
		insertSQL := "INSERT INTO " + destinationTable + " (" + strings.Join(destinationColumns, ",") + ") VALUES " + strings.Join(placeholders, ",")
		if _, err := executor.ExecContext(ctx, insertSQL, args...); err != nil {
			return err
		}
		count += int64(len(batch))
		batch = batch[:0]
		return nil
	}
	for rows.Next() {
		if err := rows.Scan(scanTargets...); err != nil {
			return count, err
		}
		args := append([]any(nil), values...)
		for i, value := range args {
			if strings.Contains(columnTypes[columns[i]], "BOOL") {
				args[i] = sqliteBool(value)
			}
		}
		batch = append(batch, args)
		if len(batch) == cap(batch) {
			if err := flush(); err != nil {
				return count, err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return count, err
	}
	if err := flush(); err != nil {
		return count, err
	}
	return count, nil
}

func sqliteBool(value any) any {
	switch v := value.(type) {
	case int64:
		return v != 0
	case int:
		return v != 0
	case []byte:
		return string(v) == "1" || strings.EqualFold(string(v), "true")
	case string:
		return v == "1" || strings.EqualFold(v, "true")
	default:
		return value
	}
}

func countTable(ctx context.Context, driver DatabaseDriver, executor migrationExecutor, table string) (int64, error) {
	quoted, err := migrationQuoteIdentifier(table, driver)
	if err != nil {
		return 0, err
	}
	var count int64
	if err := executor.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+quoted).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func resetPostgresSequences(ctx context.Context, executor migrationExecutor, tables []string) error {
	for _, table := range tables {
		quoted, err := migrationQuoteIdentifier(table, DriverPostgres)
		if err != nil {
			return err
		}
		var hasID bool
		if err := executor.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = $1 AND column_name = 'id')`, table).Scan(&hasID); err != nil {
			return err
		}
		if !hasID {
			continue
		}
		var sequence sql.NullString
		if err := executor.QueryRowContext(ctx, `SELECT pg_get_serial_sequence($1, 'id')`, table).Scan(&sequence); err != nil {
			return err
		}
		if !sequence.Valid || sequence.String == "" {
			continue
		}
		query := `SELECT setval(pg_get_serial_sequence($1, 'id'), COALESCE(MAX(id), 1), MAX(id) IS NOT NULL) FROM ` + quoted
		var ignored any
		if err := executor.QueryRowContext(ctx, query, table).Scan(&ignored); err != nil {
			return err
		}
	}
	return nil
}
