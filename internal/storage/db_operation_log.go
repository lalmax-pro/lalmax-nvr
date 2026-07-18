package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/model"
)

// OperationLogsFilter defines filters for audit-log queries.
type OperationLogsFilter struct {
	Username string
	Action   string
	Resource string
	Status   string
	Since    string
	Until    string
	Limit    int
	Offset   int
}

// InsertOperationLog persists an audit log entry.
func (d *DB) InsertOperationLog(ctx context.Context, log model.OperationLog) (int64, error) {
	if log.ActorType == "" {
		if log.UserID > 0 || log.Username != "" {
			log.ActorType = "user"
		} else {
			log.ActorType = "system"
		}
	}
	if log.Status == "" {
		log.Status = "success"
	}
	if log.Metadata == "" {
		log.Metadata = "{}"
	}
	if log.CreatedAt.IsZero() {
		log.CreatedAt = time.Now().UTC()
	}

	result, err := d.db.ExecContext(ctx, `
		INSERT INTO operation_logs
			(user_id, username, actor_type, action, resource, resource_id, status, message, metadata, ip_address, user_agent, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		nullableInt64(log.UserID), log.Username, log.ActorType, log.Action, log.Resource,
		log.ResourceID, log.Status, log.Message, log.Metadata, log.IPAddress, log.UserAgent, timeToDB(log.CreatedAt))
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// ListOperationLogs returns audit logs ordered from newest to oldest.
func (d *DB) ListOperationLogs(ctx context.Context, filter OperationLogsFilter) ([]model.OperationLog, int, error) {
	where, args := operationLogsWhere(filter)
	whereClause := ""
	if len(where) > 0 {
		whereClause = " WHERE " + strings.Join(where, " AND ")
	}

	var total int
	if err := d.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM operation_logs"+whereClause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	query := `SELECT id, user_id, username, actor_type, action, resource, resource_id,
		status, message, metadata, ip_address, user_agent, created_at
		FROM operation_logs` + whereClause + " ORDER BY created_at DESC, id DESC"
	limit := filter.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}
	query += fmt.Sprintf(" LIMIT %d OFFSET %d", limit, offset)

	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	logs := make([]model.OperationLog, 0)
	for rows.Next() {
		var entry model.OperationLog
		var userID sql.NullInt64
		var createdAt sql.NullString
		if err := rows.Scan(&entry.ID, &userID, &entry.Username, &entry.ActorType, &entry.Action,
			&entry.Resource, &entry.ResourceID, &entry.Status, &entry.Message, &entry.Metadata,
			&entry.IPAddress, &entry.UserAgent, &createdAt); err != nil {
			return nil, 0, err
		}
		if userID.Valid {
			entry.UserID = userID.Int64
		}
		entry.CreatedAt = scanTime(createdAt)
		logs = append(logs, entry)
	}
	return logs, total, rows.Err()
}

func operationLogsWhere(filter OperationLogsFilter) ([]string, []any) {
	where := make([]string, 0, 6)
	args := make([]any, 0, 6)
	if filter.Username != "" {
		where = append(where, "username = ?")
		args = append(args, filter.Username)
	}
	if filter.Action != "" {
		where = append(where, "action = ?")
		args = append(args, filter.Action)
	}
	if filter.Resource != "" {
		where = append(where, "resource = ?")
		args = append(args, filter.Resource)
	}
	if filter.Status != "" {
		where = append(where, "status = ?")
		args = append(args, filter.Status)
	}
	if filter.Since != "" {
		where = append(where, "created_at >= ?")
		args = append(args, filter.Since)
	}
	if filter.Until != "" {
		where = append(where, "created_at <= ?")
		args = append(args, filter.Until)
	}
	return where, args
}

func nullableInt64(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}
