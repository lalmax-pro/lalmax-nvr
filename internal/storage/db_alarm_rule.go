package storage

import (
	"context"
	"database/sql"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/model"
)

func (d *DB) InsertAlarmRule(ctx context.Context, rule *model.AlarmRule) error {
	res, err := d.db.ExecContext(ctx, `INSERT INTO alarm_rules(name, enabled, camera_id, source, event_type, severity, action, action_target, created_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		rule.Name, boolToInt(rule.Enabled), rule.CameraID, rule.Source, rule.EventType, rule.Severity, rule.Action, rule.ActionTarget, timeToDB(time.Now().UTC()))
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	rule.ID = id
	return nil
}

func (d *DB) ListAlarmRules(ctx context.Context) ([]model.AlarmRule, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT id, name, enabled, camera_id, source, event_type, severity, action, action_target, created_at FROM alarm_rules ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.AlarmRule
	for rows.Next() {
		var r model.AlarmRule
		var enabled int
		var created sql.NullString
		if err := rows.Scan(&r.ID, &r.Name, &enabled, &r.CameraID, &r.Source, &r.EventType, &r.Severity, &r.Action, &r.ActionTarget, &created); err != nil {
			return nil, err
		}
		r.Enabled = enabled != 0
		r.CreatedAt = scanTime(created)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (d *DB) DeleteAlarmRule(ctx context.Context, id int64) error {
	res, err := d.db.ExecContext(ctx, `DELETE FROM alarm_rules WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (d *DB) SetAlarmRuleEnabled(ctx context.Context, id int64, enabled bool) error {
	_, err := d.db.ExecContext(ctx, `UPDATE alarm_rules SET enabled=? WHERE id=?`, boolToInt(enabled), id)
	return err
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
