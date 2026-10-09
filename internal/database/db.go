// Package database 负责 SQLite 连接、PRAGMA、迁移与运行期配置播种。
package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite" // 纯 Go 驱动，CGO_ENABLED=0 可静态编译
)

// DB 包装 *sql.DB 并记录文件路径，供备份与恢复使用。
type DB struct {
	*sql.DB
	Path string
}

// 连接建立时必须生效的 PRAGMA。
//
// 凭据工具优先持久性，synchronous 默认 FULL：WAL + NORMAL 在系统掉电时
// 可能丢失多个尚未同步的已提交事务，不能宣称“最多丢最后一个事务”。
var requiredPragmas = []struct {
	name  string
	value string
}{
	{"journal_mode", "WAL"},
	{"foreign_keys", "1"},
	{"busy_timeout", "5000"},
	{"synchronous", "FULL"},
}

// Open 打开（必要时创建）数据库文件，设置 PRAGMA 并校验生效结果。
//
// MVP 统一使用单连接（SetMaxOpenConns(1)），让写入串行、PRAGMA 覆盖明确。
// 若以后扩连接池，每个连接都必须重新初始化 foreign_keys / busy_timeout / synchronous。
func Open(ctx context.Context, path string) (*DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("解析数据库路径失败：%w", err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return nil, fmt.Errorf("创建数据库目录失败：%w", err)
	}

	sqlDB, err := sql.Open("sqlite", buildDSN(abs))
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败：%w", err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	sqlDB.SetConnMaxLifetime(0)

	db := &DB{DB: sqlDB, Path: abs}

	// database/sql 是惰性连接，这里主动建立唯一连接并落 PRAGMA。
	if err := db.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("连接数据库失败（dsn=%s）：%w", buildDSN(abs), err)
	}
	if err := db.verifyPragmas(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return db, nil
}

// OpenReadOnly 以只读方式打开数据库，供恢复流程校验候选备份。
func OpenReadOnly(ctx context.Context, path string) (*DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("解析数据库路径失败：%w", err)
	}
	if _, err := os.Stat(abs); err != nil {
		return nil, fmt.Errorf("备份文件不可读：%w", err)
	}
	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(5000)", uriPath(abs))
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("以只读方式打开数据库失败：%w", err)
	}
	sqlDB.SetMaxOpenConns(1)
	db := &DB{DB: sqlDB, Path: abs}
	if err := db.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("只读连接失败：%w", err)
	}
	return db, nil
}

func (db *DB) verifyPragmas(ctx context.Context) error {
	for _, p := range requiredPragmas {
		var got string
		if err := db.QueryRowContext(ctx, "PRAGMA "+p.name).Scan(&got); err != nil {
			return fmt.Errorf("读取 PRAGMA %s 失败：%w", p.name, err)
		}
		if !pragmaMatches(p.name, got, p.value) {
			return fmt.Errorf("PRAGMA %s 未生效：期望 %s，实际 %s", p.name, p.value, got)
		}
	}
	return nil
}

// pragmaMatches 处理 SQLite 对 journal_mode 的大小写差异与 synchronous 的数字返回。
func pragmaMatches(name, got, want string) bool {
	got = strings.TrimSpace(got)
	switch name {
	case "journal_mode":
		return strings.EqualFold(got, want)
	case "synchronous":
		// FULL == 2；部分驱动返回数字。
		return got == want || got == "2"
	default:
		return got == want
	}
}

func buildDSN(absPath string) string {
	parts := make([]string, 0, len(requiredPragmas))
	for _, p := range requiredPragmas {
		parts = append(parts, fmt.Sprintf("_pragma=%s(%s)", p.name, p.value))
	}
	return fmt.Sprintf("file:%s?%s", uriPath(absPath), strings.Join(parts, "&"))
}

// uriPath 把本地路径转成可安全放进 file: URI 的形式。
//
// 注意：Windows 盘符路径必须写成 file:C:/dir/db.sqlite，
// 不能写成 file:///C:/dir/db.sqlite —— 后者会被解析成 authority "C:" 并报错。
func uriPath(absPath string) string {
	p := filepath.ToSlash(absPath)
	return strings.NewReplacer(
		" ", "%20",
		"#", "%23",
		"?", "%3F",
	).Replace(p)
}

// Tx 在事务中执行 fn，出错自动回滚。
func (db *DB) Tx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开启事务失败：%w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()
	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			return fmt.Errorf("%w（回滚也失败：%v）", err, rbErr)
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交事务失败：%w", err)
	}
	return nil
}

// IntegrityCheck 运行 PRAGMA integrity_check，返回 "ok" 才算通过。
func (db *DB) IntegrityCheck(ctx context.Context) error {
	var result string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return fmt.Errorf("integrity_check 执行失败：%w", err)
	}
	if !strings.EqualFold(result, "ok") {
		return fmt.Errorf("integrity_check 未通过：%s", result)
	}
	return nil
}

// ForeignKeyCheck 运行 PRAGMA foreign_key_check，无输出才算通过。
func (db *DB) ForeignKeyCheck(ctx context.Context) error {
	rows, err := db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("foreign_key_check 执行失败：%w", err)
	}
	defer rows.Close()
	if rows.Next() {
		return errors.New("foreign_key_check 发现外键违规")
	}
	return rows.Err()
}
