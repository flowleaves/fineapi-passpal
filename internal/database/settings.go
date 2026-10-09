package database

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// SeedSettings 只在 key 不存在时写入初值。
//
// 这实现了工作区铁律「库值 > 环境变量」：环境变量只是首次播种的初值，
// 之后面板/API 改过的值不会被下次启动静默打回。
func SeedSettings(ctx context.Context, db *DB, seeds map[string]string, updatedBy string) error {
	if len(seeds) == 0 {
		return nil
	}
	now := time.Now().Unix()
	return db.Tx(ctx, func(tx *sql.Tx) error {
		for k, v := range seeds {
			var exists int
			err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM settings WHERE key = ?`, k).Scan(&exists)
			if err != nil {
				return fmt.Errorf("检查配置 %s 失败：%w", k, err)
			}
			if exists > 0 {
				continue
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO settings (key, value, updated_at, updated_by) VALUES (?, ?, ?, ?)`,
				k, v, now, updatedBy); err != nil {
				return fmt.Errorf("播种配置 %s 失败：%w", k, err)
			}
		}
		return nil
	})
}

// GetSetting 读取一个配置项。
func GetSetting(ctx context.Context, q Queryer, key string) (string, bool, error) {
	var v sql.NullString
	err := q.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("读取配置 %s 失败：%w", key, err)
	}
	return v.String, true, nil
}

// SetSetting 写入或覆盖一个配置项。
func SetSetting(ctx context.Context, db *DB, key, value, updatedBy string) error {
	_, err := db.ExecContext(ctx, `
        INSERT INTO settings (key, value, updated_at, updated_by) VALUES (?, ?, ?, ?)
        ON CONFLICT(key) DO UPDATE SET value = excluded.value,
                                       updated_at = excluded.updated_at,
                                       updated_by = excluded.updated_by`,
		key, value, time.Now().Unix(), updatedBy)
	if err != nil {
		return fmt.Errorf("写入配置 %s 失败：%w", key, err)
	}
	return nil
}

// AllSettings 返回全部配置项。
func AllSettings(ctx context.Context, q Queryer) (map[string]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return nil, fmt.Errorf("读取配置失败：%w", err)
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var k string
		var v sql.NullString
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v.String
	}
	return out, rows.Err()
}

// Queryer 抽象 *sql.DB 与 *sql.Tx 的公共查询能力。
type Queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}
