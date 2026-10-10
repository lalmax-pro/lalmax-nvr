package storage

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

var (
	integerAutoPrimaryKeyRE = regexp.MustCompile(`(?i)INTEGER\s+PRIMARY\s+KEY\s+AUTOINCREMENT`)
	textPrimaryKeyRE        = regexp.MustCompile(`(?i)TEXT\s+(NOT\s+NULL\s+)?PRIMARY\s+KEY`)
	textDefaultRE           = regexp.MustCompile(`(?i)\bTEXT(\s+(?:NOT\s+NULL\s+)?DEFAULT\b)`)
	textUniqueRE            = regexp.MustCompile(`(?i)\bTEXT(\s+(?:NOT\s+NULL\s+)?UNIQUE\b)`)
	primaryKeyColumnsRE     = regexp.MustCompile(`(?i)PRIMARY\s+KEY\s*\(([^)]*)\)`)
	foreignKeyColumnsRE     = regexp.MustCompile(`(?i)FOREIGN\s+KEY\s*\(([^)]*)\)`)
	uniqueKeyColumnsRE      = regexp.MustCompile(`(?i)UNIQUE\s*\(([^)]*)\)`)
	integerTypeRE           = regexp.MustCompile(`(?i)\bINTEGER\b`)
	datetimeTypeRE          = regexp.MustCompile(`(?i)\bDATETIME\b`)
	ignoreInsertPrefixRE    = regexp.MustCompile(`(?i)^\s*INSERT\s+OR\s+IGNORE\s+INTO\b`)
	ignoreConflictRE        = regexp.MustCompile(`(?i)\s+ON\s+CONFLICT\s+DO\s+NOTHING\s*;?\s*$`)
	indexIfNotExistsRE      = regexp.MustCompile(`(?i)^\s*CREATE\s+INDEX\s+IF\s+NOT\s+EXISTS\s+`)
	conflictUpdateRE        = regexp.MustCompile(`(?is)\s+ON\s+CONFLICT(?:\s*\([^)]*\))?\s+DO\s+UPDATE\s+SET\s+`)
	excludedColumnRE        = regexp.MustCompile(`(?i)excluded\.([a-z_][a-z0-9_]*)`)
	createIndexColumnsRE    = regexp.MustCompile(`(?i)^(CREATE\s+INDEX\s+\S+\s+ON\s+\S+)\s*\(([^)]*)\)(.*)$`)
	epochStartedAtRE        = regexp.MustCompile(`(?i)CAST\(strftime\('%s',\s*substr\((\w+\.)?started_at,\s*1,\s*19\)\)\s+AS\s+INTEGER\)`)
)

// execContext, queryContext, and queryRowContext keep placeholder and SQL
// compatibility rules at the storage boundary instead of in repositories.
func (d *DB) execContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if d.driver != DriverSQLite && len(args) == 0 {
		statements := splitSQLStatements(query)
		if len(statements) > 1 {
			var result sql.Result
			for _, statement := range statements {
				var err error
				result, err = d.execContext(ctx, statement)
				if err != nil {
					return nil, err
				}
			}
			return result, nil
		}
	}
	d.dbMu.RLock()
	defer d.dbMu.RUnlock()
	dialectSQL := d.dialectQuery(query)
	result, err := d.db.ExecContext(ctx, dialectSQL, args...)
	if err != nil {
		errorText := strings.ToLower(err.Error())
		if d.driver == DriverMySQL && isCreateIndexQuery(dialectSQL) && strings.Contains(errorText, "duplicate key name") {
			return emptySQLResult{}, nil
		}
		if d.driver == DriverMySQL && isCreateIndexQuery(dialectSQL) && strings.Contains(errorText, "without a key length") {
			if prefixedQuery, changed := d.mysqlTextIndexPrefixes(ctx, dialectSQL); changed {
				return d.db.ExecContext(ctx, prefixedQuery, args...)
			}
		}
		if isAddColumnQuery(dialectSQL) && (strings.Contains(errorText, "duplicate column") || strings.Contains(errorText, "already exists")) {
			return emptySQLResult{}, nil
		}
	}
	if err != nil {
		return nil, fmt.Errorf("storage: execute SQL %q: %w", dialectSQL, err)
	}
	return result, nil
}

func splitSQLStatements(query string) []string {
	var statements []string
	start := 0
	var quote byte
	for i := 0; i < len(query); i++ {
		c := query[i]
		if quote != 0 {
			if c == quote {
				if i+1 < len(query) && query[i+1] == quote {
					i++
				} else {
					quote = 0
				}
			} else if c == '\\' && quote == '\'' && i+1 < len(query) {
				i++
			}
			continue
		}
		if c == '\'' || c == '"' || c == '`' {
			quote = c
			continue
		}
		if c == ';' {
			if statement := strings.TrimSpace(query[start:i]); statement != "" {
				statements = append(statements, statement)
			}
			start = i + 1
		}
	}
	if statement := strings.TrimSpace(query[start:]); statement != "" {
		statements = append(statements, statement)
	}
	return statements
}

func (d *DB) queryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	d.dbMu.RLock()
	defer d.dbMu.RUnlock()
	return d.db.QueryContext(ctx, d.dialectQuery(query), args...)
}

func (d *DB) queryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	d.dbMu.RLock()
	defer d.dbMu.RUnlock()
	return d.db.QueryRowContext(ctx, d.dialectQuery(query), args...)
}

func (d *DB) exec(query string, args ...any) (sql.Result, error) {
	return d.execContext(context.Background(), query, args...)
}

// insertID executes an INSERT and returns its generated integer primary key.
// PostgreSQL requires RETURNING; SQLite and MySQL expose LastInsertId.
func (d *DB) insertID(ctx context.Context, query string, args ...any) (int64, error) {
	if d.driver == DriverPostgres {
		query = strings.TrimSpace(query)
		query = strings.TrimSuffix(query, ";") + " RETURNING id"
		var id int64
		if err := d.queryRowContext(ctx, query, args...).Scan(&id); err != nil {
			return 0, err
		}
		return id, nil
	}
	result, err := d.execContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (d *DB) query(query string, args ...any) (*sql.Rows, error) {
	return d.queryContext(context.Background(), query, args...)
}

func (d *DB) beginTx(ctx context.Context, opts *sql.TxOptions) (*dialectTx, error) {
	d.dbMu.RLock()
	tx, err := d.db.BeginTx(ctx, opts)
	if err != nil {
		d.dbMu.RUnlock()
		return nil, err
	}
	return &dialectTx{db: d, tx: tx, unlock: d.dbMu.RUnlock}, nil
}

type dialectTx struct {
	db     *DB
	tx     *sql.Tx
	unlock func()
	once   sync.Once
}

func (t *dialectTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return t.tx.ExecContext(ctx, t.db.dialectQuery(query), args...)
}

func (t *dialectTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return t.tx.QueryContext(ctx, t.db.dialectQuery(query), args...)
}

func (t *dialectTx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return t.tx.QueryRowContext(ctx, t.db.dialectQuery(query), args...)
}

func (t *dialectTx) PrepareContext(ctx context.Context, query string) (*dialectStmt, error) {
	stmt, err := t.tx.PrepareContext(ctx, t.db.dialectQuery(query))
	if err != nil {
		return nil, err
	}
	return &dialectStmt{stmt: stmt}, nil
}

func (t *dialectTx) Commit() error {
	err := t.tx.Commit()
	t.once.Do(t.unlock)
	return err
}
func (t *dialectTx) Rollback() error {
	err := t.tx.Rollback()
	t.once.Do(t.unlock)
	return err
}

type dialectStmt struct{ stmt *sql.Stmt }

func (s *dialectStmt) ExecContext(ctx context.Context, args ...any) (sql.Result, error) {
	return s.stmt.ExecContext(ctx, args...)
}

func (s *dialectStmt) Close() error { return s.stmt.Close() }

func (d *DB) dialectQuery(query string) string {
	query = adaptSQLForDriver(query, d.driver)
	if d.driver == DriverPostgres {
		return rebindDollarPlaceholders(query)
	}
	return query
}

func adaptSQLForDriver(query string, driver DatabaseDriver) string {
	if driver == DriverSQLite {
		return query
	}
	query = integerAutoPrimaryKeyRE.ReplaceAllString(query, "BIGSERIAL PRIMARY KEY")
	query = strings.ReplaceAll(query, "strftime('%Y-%m-%d %H:%M:%f', 'now')", "CURRENT_TIMESTAMP")
	query = strings.ReplaceAll(query, "datetime('now')", "CURRENT_TIMESTAMP")
	if driver == DriverMySQL {
		query = strings.ReplaceAll(query, "strftime('%Y-%m', started_at)", "DATE_FORMAT(started_at, '%Y-%m')")
		query = strings.ReplaceAll(query, "strftime('%Y-%m-%dT%H:00:00Z', started_at)", "DATE_FORMAT(started_at, '%Y-%m-%dT%H:00:00Z')")
		query = epochStartedAtRE.ReplaceAllString(query, "CAST(FLOOR(UNIX_TIMESTAMP(SUBSTRING(${1}started_at, 1, 19))) AS SIGNED)")
		query = regexp.MustCompile(`(?i)AS\s+BIGINT\)`).ReplaceAllString(query, "AS SIGNED)")
		query = strings.ReplaceAll(query, "BIGSERIAL PRIMARY KEY", "BIGINT AUTO_INCREMENT PRIMARY KEY")
		query = regexp.MustCompile(`(?i)\bBOOLEAN\b`).ReplaceAllString(query, "TINYINT(1)")
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(query)), "CREATE TABLE") || strings.HasPrefix(strings.ToUpper(strings.TrimSpace(query)), "ALTER TABLE") {
			query = integerTypeRE.ReplaceAllString(query, "BIGINT")
		}
		query = textPrimaryKeyRE.ReplaceAllString(query, "VARCHAR(191) PRIMARY KEY")
		query = textDefaultRE.ReplaceAllString(query, "VARCHAR(191)$1")
		query = textUniqueRE.ReplaceAllString(query, "VARCHAR(191)$1")
		keyConstraints := append(primaryKeyColumnsRE.FindAllStringSubmatch(query, -1), foreignKeyColumnsRE.FindAllStringSubmatch(query, -1)...)
		keyConstraints = append(keyConstraints, uniqueKeyColumnsRE.FindAllStringSubmatch(query, -1)...)
		for _, match := range keyConstraints {
			for _, column := range strings.Split(match[1], ",") {
				column = strings.TrimSpace(column)
				if column == "" {
					continue
				}
				columnRE := regexp.MustCompile(`(?i)(\b` + regexp.QuoteMeta(column) + `\s+)TEXT\b`)
				query = columnRE.ReplaceAllString(query, "${1}VARCHAR(191)")
			}
		}
		query = indexIfNotExistsRE.ReplaceAllString(query, "CREATE INDEX ")
		if ignoreInsertPrefixRE.MatchString(query) {
			query = ignoreInsertPrefixRE.ReplaceAllString(query, "INSERT IGNORE INTO")
		}
		if conflictUpdateRE.MatchString(query) {
			query = conflictUpdateRE.ReplaceAllString(query, " ON DUPLICATE KEY UPDATE ")
			query = excludedColumnRE.ReplaceAllString(query, "VALUES($1)")
		}
		return quoteMySQLKeyIdentifier(query)
	}
	if driver == DriverPostgres {
		query = datetimeTypeRE.ReplaceAllString(query, "TIMESTAMP")
		query = strings.ReplaceAll(query, "strftime('%Y-%m', started_at)", "TO_CHAR(started_at, 'YYYY-MM')")
		query = strings.ReplaceAll(query, "strftime('%Y-%m-%dT%H:00:00Z', started_at)", `TO_CHAR(started_at, 'YYYY-MM-DD"T"HH24:00:00"Z"')`)
		query = epochStartedAtRE.ReplaceAllString(query, "CAST(FLOOR(EXTRACT(EPOCH FROM CAST(SUBSTRING(CAST(${1}started_at AS TEXT) FROM 1 FOR 19) AS TIMESTAMP))) AS BIGINT)")
		if ignoreInsertPrefixRE.MatchString(query) {
			query = ignoreInsertPrefixRE.ReplaceAllString(query, "INSERT INTO")
			trimmed := strings.TrimSpace(query)
			semicolon := strings.HasSuffix(trimmed, ";")
			trimmed = strings.TrimRight(trimmed, "; \t\r\n")
			if !ignoreConflictRE.MatchString(trimmed) {
				query = trimmed + " ON CONFLICT DO NOTHING"
				if semicolon {
					query += ";"
				}
			}
		}
	}
	return query
}

func quoteMySQLKeyIdentifier(query string) string {
	var out strings.Builder
	var quote byte
	previousWord := ""
	for i := 0; i < len(query); i++ {
		c := query[i]
		if quote != 0 {
			out.WriteByte(c)
			if c == quote {
				if i+1 < len(query) && query[i+1] == quote {
					i++
					out.WriteByte(query[i])
				} else {
					quote = 0
				}
			} else if c == '\\' && quote == '\'' && i+1 < len(query) {
				i++
				out.WriteByte(query[i])
			}
			continue
		}
		if c == '\'' || c == '"' || c == '`' {
			quote = c
			out.WriteByte(c)
			continue
		}
		if isSQLWordStart(c) {
			start := i
			for i+1 < len(query) && isSQLWordPart(query[i+1]) {
				i++
			}
			word := query[start : i+1]
			if strings.EqualFold(word, "key") {
				if strings.EqualFold(previousWord, "primary") || strings.EqualFold(previousWord, "foreign") || strings.EqualFold(previousWord, "duplicate") {
					out.WriteString(word)
				} else {
					out.WriteByte('`')
					out.WriteString(word)
					out.WriteByte('`')
				}
			} else {
				out.WriteString(word)
			}
			previousWord = word
			continue
		}
		out.WriteByte(c)
	}
	return out.String()
}

func isSQLWordStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isSQLWordPart(c byte) bool {
	return isSQLWordStart(c) || c >= '0' && c <= '9'
}

func isCreateIndexQuery(query string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(query)), "create index")
}

func isAddColumnQuery(query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	return strings.HasPrefix(query, "alter table") && strings.Contains(query, " add column ")
}

func (d *DB) mysqlTextIndexPrefixes(ctx context.Context, query string) (string, bool) {
	match := createIndexColumnsRE.FindStringSubmatch(query)
	if match == nil {
		return query, false
	}
	table := strings.Trim(match[1][strings.LastIndex(strings.ToLower(match[1]), " on ")+4:], " `")
	parts := strings.Split(match[2], ",")
	changed := false
	for i, part := range parts {
		column := strings.TrimSpace(part)
		columnName := strings.Trim(strings.TrimSuffix(strings.TrimSuffix(column, " DESC"), " ASC"), " `")
		var dataType string
		err := d.db.QueryRowContext(ctx, `SELECT data_type FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name=? AND column_name=?`, table, columnName).Scan(&dataType)
		if err != nil {
			continue
		}
		switch strings.ToLower(dataType) {
		case "text", "tinytext", "mediumtext", "longtext", "blob", "tinyblob", "mediumblob", "longblob":
			parts[i] = columnName + "(191)"
			if strings.HasSuffix(strings.ToUpper(column), " DESC") {
				parts[i] += " DESC"
			} else if strings.HasSuffix(strings.ToUpper(column), " ASC") {
				parts[i] += " ASC"
			}
			changed = true
		}
	}
	if !changed {
		return query, false
	}
	return match[1] + " (" + strings.Join(parts, ", ") + ")" + match[3], true
}

type emptySQLResult struct{}

func (emptySQLResult) LastInsertId() (int64, error) { return 0, nil }
func (emptySQLResult) RowsAffected() (int64, error) { return 0, nil }

func boolToDBInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// rebindDollarPlaceholders converts positional question mark parameters while
// preserving question marks inside SQL string and identifier literals.
func rebindDollarPlaceholders(query string) string {
	var out strings.Builder
	out.Grow(len(query) + 8)
	var quote byte
	placeholder := 0
	for i := 0; i < len(query); i++ {
		c := query[i]
		if quote != 0 {
			out.WriteByte(c)
			if c == quote {
				if i+1 < len(query) && query[i+1] == quote {
					i++
					out.WriteByte(query[i])
				} else {
					quote = 0
				}
			} else if c == '\\' && quote == '\'' && i+1 < len(query) {
				i++
				out.WriteByte(query[i])
			}
			continue
		}
		switch c {
		case '\'', '"', '`':
			quote = c
			out.WriteByte(c)
		case '?':
			placeholder++
			out.WriteByte('$')
			out.WriteString(sqlPlaceholderNumber(placeholder))
		default:
			out.WriteByte(c)
		}
	}
	return out.String()
}

func sqlPlaceholderNumber(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	var digits [20]byte
	i := len(digits)
	for n > 0 {
		i--
		digits[i] = byte('0' + n%10)
		n /= 10
	}
	return string(digits[i:])
}
