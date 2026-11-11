package storage

import (
 "context"
 "database/sql"
 "errors"
)

// Preferences are scoped to the authenticated username and namespace.
// The username also works with legacy single-account deployments.
func (d *DB) InitUserPreferences(ctx context.Context) error {
 _, err := d.execContext(ctx, `CREATE TABLE IF NOT EXISTS user_preferences (
 username VARCHAR(255) NOT NULL,
 namespace VARCHAR(64) NOT NULL,
 config_json TEXT NOT NULL,
 PRIMARY KEY (username, namespace)
 )`)
 return err
}

func (d *DB) GetUserPreference(ctx context.Context, username, namespace string) (string, bool, error) {
 var value string
 err := d.queryRowContext(ctx, "SELECT config_json FROM user_preferences WHERE username=? AND namespace=?", username, namespace).Scan(&value)
 if errors.Is(err, sql.ErrNoRows) { return "", false, nil }
 return value, err == nil, err
}

func (d *DB) SetUserPreference(ctx context.Context, username, namespace, value string) error {
 // Single-statement UPSERT is atomic and works when the new value equals
 // the stored value. MySQL RowsAffected() reports changed rows by default,
 // so UPDATE + RowsAffected() == 0 + INSERT can hit duplicate primary key.
 switch d.driver {
 case DriverMySQL:
  _, err := d.execContext(ctx,"INSERT INTO user_preferences (username, namespace, config_json) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE config_json=?",username,namespace,value,value)
  return err
 default:
  _, err := d.execContext(ctx,"INSERT INTO user_preferences (username, namespace, config_json) VALUES (?, ?, ?) ON CONFLICT (username, namespace) DO UPDATE SET config_json=excluded.config_json",username,namespace,value)
  return err
 }
}
