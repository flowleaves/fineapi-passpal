// Package audit 写审计日志。
//
// 只记动作、目标引用与字段枚举，绝不记录字段值、原文或任何敏感内容。
package audit

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// 审计动作枚举（doc §15）。
const (
	ActionLoginSuccess   = "LOGIN_SUCCESS"
	ActionLoginFailed    = "LOGIN_FAILED"
	ActionLogout         = "LOGOUT"
	ActionAuthUnlock     = "AUTH_UNLOCK"
	ActionRevealSecret   = "REVEAL_SECRET"
	ActionBatchExportRT  = "BATCH_EXPORT_RT"
	ActionImport         = "IMPORT"
	ActionCancelImport   = "CANCEL_IMPORT"
	ActionExport         = "EXPORT"
	ActionBackup         = "BACKUP"
	ActionRestoreDB      = "RESTORE_DATABASE"
	ActionCreateProject  = "CREATE_PROJECT"
	ActionUpdateProject  = "UPDATE_PROJECT"
	ActionDeleteProject  = "DELETE_PROJECT"
	ActionCreateTag      = "CREATE_TAG"
	ActionUpdateTag      = "UPDATE_TAG"
	ActionDeleteTag      = "DELETE_TAG"
	ActionCreateAccount  = "CREATE_ACCOUNT"
	ActionUpdateAccount  = "UPDATE_ACCOUNT"
	ActionDeleteAccount  = "DELETE_ACCOUNT"
	ActionRestoreItem    = "RESTORE_ITEM"
	ActionUpdateSettings = "UPDATE_SETTINGS"
	ActionBatchDelete    = "BATCH_DELETE_ACCOUNTS"
)

// 目标类型枚举。
const (
	TargetProject = "project"
	TargetTag     = "tag"
	TargetAccount = "account"
	TargetImport  = "import"
	TargetBackup  = "backup"
	TargetSystem  = "system"
)

// 审计保留策略：默认 90 天，最多 100,000 条。
const (
	RetentionDays = 90
	MaxRows       = 100_000
)

// Entry 是一条审计记录。
type Entry struct {
	Action     string
	TargetType string
	TargetID   *int64
	TargetRef  string // 非敏感引用（如草稿 ID 哈希），不含原文
	Field      string // 取密字段枚举，不含字段值
	IP         string
}

// Execer 抽象 *sql.DB 与 *sql.Tx。
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Write 写一条审计记录。
func Write(ctx context.Context, q Execer, e Entry) error {
	_, err := q.ExecContext(ctx, `
        INSERT INTO audit_logs (action, target_type, target_id, target_ref, field, ip, created_at)
        VALUES (?, ?, ?, ?, ?, ?, ?)`,
		e.Action, nullStr(e.TargetType), e.TargetID, nullStr(e.TargetRef), nullStr(e.Field), nullStr(e.IP), time.Now().Unix())
	if err != nil {
		return fmt.Errorf("写审计日志失败：%w", err)
	}
	return nil
}

// Cleanup 按保留策略清理历史审计记录。
func Cleanup(ctx context.Context, q Execer, now time.Time) error {
	if _, err := q.ExecContext(ctx,
		`DELETE FROM audit_logs WHERE created_at < ?`, now.AddDate(0, 0, -RetentionDays).Unix()); err != nil {
		return fmt.Errorf("清理过期审计失败：%w", err)
	}
	if _, err := q.ExecContext(ctx, `
        DELETE FROM audit_logs WHERE id NOT IN (
            SELECT id FROM audit_logs ORDER BY id DESC LIMIT ?
        )`, MaxRows); err != nil {
		return fmt.Errorf("裁剪超量审计失败：%w", err)
	}
	return nil
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
