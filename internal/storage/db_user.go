package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/lalmax-pro/lalmax-nvr/internal/model"
)

func (d *DB) CreateUserTable(ctx context.Context) error {
	tblSQL := `CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		username TEXT NOT NULL UNIQUE,
		password_hash TEXT NOT NULL,
		role TEXT NOT NULL DEFAULT 'user',
		display_name TEXT DEFAULT '',
		enabled INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);`
	if _, err := d.execContext(ctx, tblSQL); err != nil {
		return err
	}
	_, _ = d.execContext(ctx, "CREATE INDEX IF NOT EXISTS idx_users_username ON users(username)")
	_, _ = d.execContext(ctx, "CREATE INDEX IF NOT EXISTS idx_users_role ON users(role)")
	return nil
}

func (d *DB) GetUserByUsername(ctx context.Context, username string) (*model.User, error) {
	row := d.queryRowContext(ctx,
		`SELECT id, username, password_hash, role, display_name, enabled, created_at, updated_at
		 FROM users WHERE username = ?`, strings.TrimSpace(username))
	u := &model.User{}
	var enabled int
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.DisplayName, &enabled, &u.CreatedAt, &u.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	u.Enabled = enabled == 1
	return u, nil
}

func (d *DB) GetUserByID(ctx context.Context, id int64) (*model.User, error) {
	row := d.queryRowContext(ctx,
		`SELECT id, username, password_hash, role, display_name, enabled, created_at, updated_at
		 FROM users WHERE id = ?`, id)
	u := &model.User{}
	var enabled int
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.DisplayName, &enabled, &u.CreatedAt, &u.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	u.Enabled = enabled == 1
	return u, nil
}

func (d *DB) CreateUser(ctx context.Context, u *model.User) error {
	enabled := 0
	if u.Enabled {
		enabled = 1
	}
	if u.DisplayName == "" {
		u.DisplayName = u.Username
	}
	id, err := d.insertID(ctx,
		`INSERT INTO users (username, password_hash, role, display_name, enabled, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		u.Username, u.PasswordHash, string(u.Role), u.DisplayName, enabled,
		timeToDB(u.CreatedAt), timeToDB(u.UpdatedAt))
	if err != nil {
		return err
	}
	if err == nil {
		u.ID = id
	}
	return nil
}

func (d *DB) UpdateUser(ctx context.Context, u *model.User) error {
	enabled := 0
	if u.Enabled {
		enabled = 1
	}
	u.UpdatedAt = time.Now().UTC()
	_, err := d.execContext(ctx,
		`UPDATE users SET password_hash=?, role=?, display_name=?, enabled=?, updated_at=? WHERE id=?`,
		u.PasswordHash, string(u.Role), u.DisplayName, enabled, timeToDB(u.UpdatedAt), u.ID)
	return err
}

func (d *DB) UpdateUserPassword(ctx context.Context, id int64, passwordHash string) error {
	_, err := d.execContext(ctx,
		`UPDATE users SET password_hash=?, updated_at=? WHERE id=?`,
		passwordHash, timeToDB(time.Now().UTC()), id)
	return err
}

func (d *DB) DeleteUser(ctx context.Context, id int64) error {
	result, err := d.execContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("user not found")
	}
	return nil
}

func (d *DB) ListUsers(ctx context.Context) ([]*model.User, error) {
	rows, err := d.queryContext(ctx,
		`SELECT id, username, password_hash, role, display_name, enabled, created_at, updated_at
		 FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []*model.User
	for rows.Next() {
		u := &model.User{}
		var enabled int
		if err := rows.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.DisplayName, &enabled, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
		u.Enabled = enabled == 1
		users = append(users, u)
	}
	return users, nil
}

func (d *DB) CountUsers(ctx context.Context) (int, error) {
	var count int
	err := d.queryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&count)
	return count, err
}

func (d *DB) HasSuperAdmin(ctx context.Context) (bool, error) {
	var count int
	err := d.queryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role = 'super_admin'`).Scan(&count)
	return count > 0, err
}
