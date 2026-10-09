package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"passpal/internal/audit"
	"passpal/internal/cryptoutil"
	"passpal/internal/database"
)

// RestoreOptions 描述一次离线恢复。
type RestoreOptions struct {
	// From 是备份文件的绝对路径。
	From string
	// DatabasePath 是本工具当前使用的数据库路径。
	DatabasePath string
	// Cipher 提供密钥版本与解密能力。
	Cipher *cryptoutil.Cipher
	// AdminHash 是当前环境的管理员 hash；恢复后写入指纹，避免复活旧会话。
	AdminHash string
	// MaxSchemaVersion 是当前二进制支持的最高迁移版本。
	MaxSchemaVersion int
	// Logf 是进度输出（不得包含秘密）。
	Logf func(string, ...any)
}

// requiredTables 是恢复时必须存在的表。
var requiredTables = []string{
	"projects", "tags", "accounts", "account_tags", "import_batches",
	"sessions", "auth_state", "audit_logs", "settings", "schema_migrations",
}

// Restore 在服务停止的前提下，用备份替换当前数据库。
//
// 契约（doc §14.3）：
//  1. 验证路径与格式，在本工具 data 同一文件系统创建候选副本，不覆盖原库；
//  2. 只读校验候选库的 integrity、外键与 schema 版本；
//  3. 逐字段验证密文头部密钥版本与 GCM tag/AAD；
//  4. 保存当前库与 WAL/SHM 到回滚目录；
//  5. 关闭句柄后替换，保留完整回滚材料；
//  6. 调用方启动后验证业务，失败可回滚。
func Restore(ctx context.Context, opt RestoreOptions) error {
	logf := opt.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if opt.Cipher == nil {
		return errors.New("恢复需要数据密钥")
	}
	if strings.TrimSpace(opt.From) == "" {
		return errors.New("必须指定备份文件路径")
	}

	src, err := filepath.Abs(opt.From)
	if err != nil {
		return fmt.Errorf("解析备份路径失败：%w", err)
	}
	target, err := filepath.Abs(opt.DatabasePath)
	if err != nil {
		return fmt.Errorf("解析数据库路径失败：%w", err)
	}
	if src == target {
		return errors.New("备份文件不能与目标数据库是同一个文件")
	}

	fi, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("备份文件不可读：%w", err)
	}
	if fi.IsDir() {
		return errors.New("备份路径是目录，不是文件")
	}

	dataDir := filepath.Dir(target)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("创建数据目录失败：%w", err)
	}

	// 1) 同文件系统候选副本
	candidate := filepath.Join(dataDir, ".restore-candidate.sqlite")
	_ = os.Remove(candidate)
	if err := copyFile(src, candidate, 0o600); err != nil {
		return fmt.Errorf("创建候选副本失败：%w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(candidate)
		}
	}()
	logf("候选副本已创建")

	// 2) 只读校验结构与版本
	if err := validateCandidate(ctx, candidate, opt.MaxSchemaVersion); err != nil {
		return err
	}
	logf("候选库结构与版本校验通过")

	// 3) 密钥与密文校验
	if err := verifySecrets(ctx, candidate, opt.Cipher); err != nil {
		return err
	}
	logf("全部密文字段校验通过")

	// 4) 回滚材料
	rollbackDir := filepath.Join(dataDir, "restore-rollback", time.Now().UTC().Format("20060102-150405"))
	if err := saveRollback(target, rollbackDir); err != nil {
		return fmt.Errorf("保存回滚材料失败：%w", err)
	}
	logf("回滚材料已保存到 %s", rollbackDir)

	// 5) 清理候选库中的旧会话状态，写入当前管理员指纹
	if err := sanitizeCandidate(ctx, candidate, opt.AdminHash); err != nil {
		return err
	}

	// 6) 替换
	if err := replaceDatabase(candidate, target); err != nil {
		return fmt.Errorf("替换数据库失败（原文件组仍在 %s）：%w", rollbackDir, err)
	}
	committed = true

	// 7) 审计摘要（写入恢复后的库）
	if db, err := database.Open(ctx, target); err == nil {
		_ = audit.Write(ctx, db, audit.Entry{
			Action:     audit.ActionRestoreDB,
			TargetType: audit.TargetSystem,
			TargetRef:  filepath.Base(src),
		})
		_ = db.Close()
	}
	logf("恢复完成：%s -> %s", filepath.Base(src), target)
	return nil
}

// validateCandidate 检查候选库的完整性、外键、表结构与迁移版本。
func validateCandidate(ctx context.Context, path string, maxVersion int) error {
	db, err := database.OpenReadOnly(ctx, path)
	if err != nil {
		return fmt.Errorf("候选库无法打开：%w", err)
	}
	defer db.Close()

	if err := db.IntegrityCheck(ctx); err != nil {
		return fmt.Errorf("候选库完整性校验失败：%w", err)
	}
	if err := db.ForeignKeyCheck(ctx); err != nil {
		return fmt.Errorf("候选库外键校验失败：%w", err)
	}

	// 拒绝未知触发器。
	var triggers int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger'`).Scan(&triggers); err != nil {
		return fmt.Errorf("检查触发器失败：%w", err)
	}
	if triggers > 0 {
		return fmt.Errorf("候选库含 %d 个未知触发器，拒绝恢复", triggers)
	}

	// 必需表齐全。
	for _, t := range requiredTables {
		var n int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, t).Scan(&n); err != nil {
			return fmt.Errorf("检查表 %s 失败：%w", t, err)
		}
		if n == 0 {
			return fmt.Errorf("候选库缺少必需的表：%s", t)
		}
	}

	// 迁移版本不高于当前二进制支持的版本。
	var maxApplied sql.NullInt64
	if err := db.QueryRowContext(ctx,
		`SELECT MAX(version) FROM schema_migrations`).Scan(&maxApplied); err != nil {
		return fmt.Errorf("读取迁移版本失败：%w", err)
	}
	if maxApplied.Valid && maxVersion > 0 && int(maxApplied.Int64) > maxVersion {
		return fmt.Errorf("候选库迁移版本 %d 高于当前支持的 %d，拒绝恢复",
			maxApplied.Int64, maxVersion)
	}
	return nil
}

// verifySecrets 遍历账号密文列，检查密钥版本可用且能通过 GCM 认证。
func verifySecrets(ctx context.Context, path string, cipher *cryptoutil.Cipher) error {
	db, err := database.OpenReadOnly(ctx, path)
	if err != nil {
		return err
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, `
        SELECT id, password_encrypted, backup_email_encrypted, f2a_encrypted,
               credential_json_encrypted, notes_encrypted FROM accounts`)
	if err != nil {
		return fmt.Errorf("读取账号密文失败：%w", err)
	}
	defer rows.Close()

	checked := 0
	for rows.Next() {
		var id int64
		var blobs [5][]byte
		if err := rows.Scan(&id, &blobs[0], &blobs[1], &blobs[2], &blobs[3], &blobs[4]); err != nil {
			return fmt.Errorf("读取账号密文失败：%w", err)
		}
		for i, field := range []string{
			cryptoutil.FieldPassword, cryptoutil.FieldBackupEmail, cryptoutil.FieldF2A,
			cryptoutil.FieldCredentialJSON, cryptoutil.FieldNotes,
		} {
			if len(blobs[i]) == 0 {
				continue
			}
			ver, err := cryptoutil.KeyVersionOf(blobs[i])
			if err != nil {
				return fmt.Errorf("账号 %d 的 %s 密文损坏：%w", id, field, err)
			}
			if !cipher.HasVersion(ver) {
				return fmt.Errorf("账号 %d 的 %s 需要密钥版本 %d，当前未配置", id, field, ver)
			}
			if _, err := cipher.Decrypt(blobs[i], cryptoutil.AccountAAD(id, field)); err != nil {
				return fmt.Errorf("账号 %d 的 %s 校验失败：%w", id, field, err)
			}
			checked++
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return nil
}

// sanitizeCandidate 清除候选库中的旧会话与来源锁定，并写入当前管理员指纹。
func sanitizeCandidate(ctx context.Context, path, adminHash string) error {
	db, err := database.Open(ctx, path)
	if err != nil {
		return fmt.Errorf("打开候选库失败：%w", err)
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx, `DELETE FROM sessions`); err != nil {
		return fmt.Errorf("清除旧会话失败：%w", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM auth_state`); err != nil {
		return fmt.Errorf("清除来源锁定失败：%w", err)
	}
	if adminHash != "" {
		fp := cryptoutil.SHA256Hex([]byte(adminHash))
		if _, err := db.ExecContext(ctx, `
            INSERT INTO settings (key, value, updated_at, updated_by) VALUES ('admin_hash_fingerprint', ?, ?, 'restore')
            ON CONFLICT(key) DO UPDATE SET value = excluded.value,
                                           updated_at = excluded.updated_at,
                                           updated_by = excluded.updated_by`,
			fp, time.Now().Unix()); err != nil {
			return fmt.Errorf("更新管理员指纹失败：%w", err)
		}
	}
	return nil
}

// saveRollback 把当前数据库及其 WAL/SHM 复制到回滚目录。
func saveRollback(target, rollbackDir string) error {
	if err := os.MkdirAll(rollbackDir, 0o700); err != nil {
		return err
	}
	saved := false
	for _, suffix := range []string{"", "-wal", "-shm"} {
		src := target + suffix
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if err := copyFile(src, filepath.Join(rollbackDir, filepath.Base(src)), 0o600); err != nil {
			return err
		}
		saved = true
	}
	if !saved {
		// 目标库不存在（首次恢复）也允许继续。
		return nil
	}
	return nil
}

// replaceDatabase 用候选库替换目标库。
func replaceDatabase(candidate, target string) error {
	for _, suffix := range []string{"-wal", "-shm", ""} {
		if err := os.Remove(target + suffix); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return os.Rename(candidate, target)
}

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

var _ = sql.ErrNoRows
