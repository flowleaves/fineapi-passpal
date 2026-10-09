package database

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const migrationsTableDDL = `CREATE TABLE IF NOT EXISTS schema_migrations (
  version    INTEGER PRIMARY KEY,
  name       TEXT    NOT NULL,
  applied_at INTEGER NOT NULL
)`

// MigrateSnapshotKeep 是迁移前快照的保留份数（doc §5.11）。
const MigrateSnapshotKeep = 3

type migration struct {
	version int
	name    string
	body    string
}

// Migrate 按序应用未执行的迁移。
//
// 规则（doc §5.11）：只有存在待执行迁移且库中已有业务数据时才做快照；
// 无迁移不备份。先完成快照与校验，再在事务内迁移；失败即返回错误，
// 调用方必须停止就绪，不继续服务。
//
// 返回本次实际应用的迁移数量。
func Migrate(ctx context.Context, db *DB, fsys fs.FS, dir, backupDir string, logf func(string, ...any)) (int, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if _, err := db.ExecContext(ctx, migrationsTableDDL); err != nil {
		return 0, fmt.Errorf("创建 schema_migrations 失败：%w", err)
	}

	all, err := loadMigrations(fsys, dir)
	if err != nil {
		return 0, err
	}
	if len(all) == 0 {
		return 0, fmt.Errorf("没有在 %s 下找到任何迁移文件", dir)
	}

	applied, err := appliedVersions(ctx, db)
	if err != nil {
		return 0, err
	}

	var pending []migration
	for _, m := range all {
		if !applied[m.version] {
			pending = append(pending, m)
		}
	}
	if len(pending) == 0 {
		logf("数据库 schema 已是最新（version %d）", all[len(all)-1].version)
		return 0, nil
	}

	// 已有业务数据时才做迁移前快照。
	hasData, err := hasBusinessData(ctx, db)
	if err != nil {
		return 0, err
	}
	if hasData {
		snap, err := snapshotBeforeMigrate(ctx, db, backupDir)
		if err != nil {
			return 0, fmt.Errorf("迁移前快照失败，已中止迁移：%w", err)
		}
		logf("迁移前快照完成：%s", filepath.Base(snap))
	}

	for _, m := range pending {
		if err := applyOne(ctx, db, m); err != nil {
			return 0, fmt.Errorf("应用迁移 %s 失败：%w", m.name, err)
		}
		logf("已应用迁移 %s", m.name)
	}
	return len(pending), nil
}

func applyOne(ctx context.Context, db *DB, m migration) error {
	return db.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, m.body); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
			m.version, m.name, time.Now().Unix())
		return err
	})
}

func loadMigrations(fsys fs.FS, dir string) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("读取迁移目录失败：%w", err)
	}
	var out []migration
	seen := make(map[int]string)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		idx := strings.Index(e.Name(), "_")
		if idx <= 0 {
			return nil, fmt.Errorf("迁移文件名必须以 <版本>_ 开头：%s", e.Name())
		}
		version, err := strconv.Atoi(e.Name()[:idx])
		if err != nil || version <= 0 {
			return nil, fmt.Errorf("迁移文件 %s 的版本号非法", e.Name())
		}
		if prev, dup := seen[version]; dup {
			return nil, fmt.Errorf("迁移版本 %d 重复：%s 与 %s", version, prev, e.Name())
		}
		seen[version] = e.Name()
		body, err := fs.ReadFile(fsys, dir+"/"+e.Name())
		if err != nil {
			return nil, fmt.Errorf("读取迁移文件 %s 失败：%w", e.Name(), err)
		}
		out = append(out, migration{version: version, name: e.Name(), body: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

func appliedVersions(ctx context.Context, db *DB) (map[int]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("读取已应用迁移失败：%w", err)
	}
	defer rows.Close()
	out := make(map[int]bool)
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out[v] = true
	}
	return out, rows.Err()
}

// hasBusinessData 判断库中是否已有业务数据（迁移前是否需要快照）。
func hasBusinessData(ctx context.Context, db *DB) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT
        (SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='projects') +
        (SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='accounts')`).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("检查业务表失败：%w", err)
	}
	if n < 2 {
		return false, nil // 全新库，无需快照
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT
        (SELECT COUNT(*) FROM projects) + (SELECT COUNT(*) FROM accounts)`).Scan(&count); err != nil {
		return false, fmt.Errorf("统计业务数据失败：%w", err)
	}
	return count > 0, nil
}

// snapshotBeforeMigrate 用 VACUUM INTO 生成一致性快照，保留最近 MigrateSnapshotKeep 份。
func snapshotBeforeMigrate(ctx context.Context, db *DB, backupDir string) (string, error) {
	dir := filepath.Join(backupDir, "migrations")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("创建快照目录失败：%w", err)
	}
	name := fmt.Sprintf("pre-migrate-%s.sqlite", time.Now().UTC().Format("20060102-150405"))
	target := filepath.Join(dir, name)
	_ = os.Remove(target) // VACUUM INTO 要求目标不存在

	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, target); err != nil {
		return "", fmt.Errorf("VACUUM INTO 失败：%w", err)
	}

	// 校验快照本身，再决定是否保留。
	snap, err := OpenReadOnly(ctx, target)
	if err != nil {
		_ = os.Remove(target)
		return "", err
	}
	defer snap.Close()
	if err := snap.IntegrityCheck(ctx); err != nil {
		_ = os.Remove(target)
		return "", fmt.Errorf("快照完整性校验失败：%w", err)
	}

	if err := pruneSnapshots(dir, MigrateSnapshotKeep); err != nil {
		// 清理失败不影响迁移正确性，只记录。
		return target, nil
	}
	return target, nil
}

func pruneSnapshots(dir string, keep int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "pre-migrate-") && strings.HasSuffix(e.Name(), ".sqlite") {
			names = append(names, e.Name())
		}
	}
	if len(names) <= keep {
		return nil
	}
	sort.Strings(names) // 时间戳字典序即时间序
	for _, n := range names[:len(names)-keep] {
		_ = os.Remove(filepath.Join(dir, n))
	}
	return nil
}
