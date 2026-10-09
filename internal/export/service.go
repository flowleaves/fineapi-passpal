// Package export 实现凭据导出。
//
// 导出是高风险操作：必须重新认证管理员密码，流式生成，
// 不在服务器写明文临时文件，响应禁止缓存。
package export

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"passpal/internal/account"
	"passpal/internal/cryptoutil"
	"passpal/internal/database"
	"passpal/internal/model"
)

// 导出范围类型。
const (
	ScopeAll     = "all"
	ScopeProject = "project"
	ScopeTag     = "tag"
)

// 导出格式。
const (
	FormatJSON = "json"
	FormatCSV  = "csv"
)

// Scope 描述导出范围。
type Scope struct {
	Type string
	ID   int64
}

// Service 提供导出能力。
type Service struct {
	db       *database.DB
	accounts *account.Service
}

// New 构造导出服务。
func New(db *database.DB, accounts *account.Service) *Service {
	return &Service{db: db, accounts: accounts}
}

// Record 是一条导出记录（含明文，仅存在于流式写出过程中）。
type Record struct {
	Email          string            `json:"email"`
	Username       string            `json:"username,omitempty"`
	Password       string            `json:"password,omitempty"`
	BackupEmail    string            `json:"backup_email,omitempty"`
	F2A            string            `json:"f2a,omitempty"`
	CredentialJSON string            `json:"credential_json,omitempty"`
	Notes          string            `json:"notes,omitempty"`
	RefreshToken   string            `json:"refresh_token,omitempty"`
	SMSLink        string            `json:"sms_link,omitempty"`
	Extra          map[string]string `json:"extra,omitempty"`
	Tags           []string          `json:"tags,omitempty"`
}

// Count 统计导出范围内的账号数，供确认前展示。
func (s *Service) Count(ctx context.Context, scope Scope) (int, error) {
	where, args, err := s.scopeWhere(scope)
	if err != nil {
		return 0, err
	}
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM accounts a JOIN projects p ON p.id = a.project_id WHERE `+where,
		args...).Scan(&n); err != nil {
		return 0, model.WrapError(500, "internal", "统计导出范围失败", err)
	}
	return n, nil
}

// Write 流式写出导出内容。
func (s *Service) Write(ctx context.Context, scope Scope, format string, w io.Writer) (int, error) {
	if format != FormatJSON && format != FormatCSV {
		return 0, model.ErrValidation("format_invalid", "只支持 json 或 csv")
	}
	where, args, err := s.scopeWhere(scope)
	if err != nil {
		return 0, err
	}

	tagIndex, err := s.tagIndex(ctx)
	if err != nil {
		return 0, err
	}

	rows, err := s.db.QueryContext(ctx, `
        SELECT a.id, a.email, COALESCE(a.username, ''), COALESCE(a.extra_json, ''),
               a.password_encrypted, a.backup_email_encrypted, a.f2a_encrypted,
               a.credential_json_encrypted, a.notes_encrypted, a.refresh_token_encrypted,
               a.sms_link_encrypted
        FROM accounts a JOIN projects p ON p.id = a.project_id
        WHERE `+where+`
        ORDER BY a.id`, args...)
	if err != nil {
		return 0, model.WrapError(500, "internal", "读取导出数据失败", err)
	}
	defer rows.Close()

	switch format {
	case FormatJSON:
		return s.writeJSON(ctx, rows, tagIndex, w)
	default:
		return s.writeCSV(ctx, rows, tagIndex, w)
	}
}

func (s *Service) writeJSON(ctx context.Context, rows *sql.Rows, tagIndex map[int64][]string, w io.Writer) (int, error) {
	if _, err := io.WriteString(w, "[\n"); err != nil {
		return 0, err
	}
	enc := json.NewEncoder(w)
	count := 0
	for rows.Next() {
		rec, err := s.scanRecord(ctx, rows, tagIndex)
		if err != nil {
			return count, err
		}
		if count > 0 {
			if _, err := io.WriteString(w, ",\n"); err != nil {
				return count, err
			}
		}
		if err := enc.Encode(rec); err != nil {
			return count, model.WrapError(500, "internal", "写出导出数据失败", err)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return count, model.WrapError(500, "internal", "读取导出数据失败", err)
	}
	if _, err := io.WriteString(w, "\n]\n"); err != nil {
		return count, err
	}
	return count, nil
}

func (s *Service) writeCSV(ctx context.Context, rows *sql.Rows, tagIndex map[int64][]string, w io.Writer) (int, error) {
	cw := csv.NewWriter(w)
	header := []string{"email", "username", "password", "backup_email", "f2a",
		"credential_json", "refresh_token", "sms_link", "notes", "tags"}
	if err := cw.Write(header); err != nil {
		return 0, err
	}
	count := 0
	for rows.Next() {
		rec, err := s.scanRecord(ctx, rows, tagIndex)
		if err != nil {
			return count, err
		}
		line := []string{
			rec.Email, rec.Username, rec.Password, rec.BackupEmail,
			rec.F2A, rec.CredentialJSON, rec.RefreshToken, rec.SMSLink, rec.Notes,
			strings.Join(rec.Tags, "|"),
		}
		for i := range line {
			line[i] = csvSafe(line[i])
		}
		if err := cw.Write(line); err != nil {
			return count, err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return count, model.WrapError(500, "internal", "读取导出数据失败", err)
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return count, model.WrapError(500, "internal", "写出导出数据失败", err)
	}
	return count, nil
}

func (s *Service) scanRecord(ctx context.Context, rows *sql.Rows, tagIndex map[int64][]string) (*Record, error) {
	var (
		id    int64
		rec   Record
		extra string
		blobs [6][]byte
	)
	if err := rows.Scan(&id, &rec.Email, &rec.Username, &extra,
		&blobs[0], &blobs[1], &blobs[2], &blobs[3], &blobs[4], &blobs[5]); err != nil {
		return nil, model.WrapError(500, "internal", "读取导出数据失败", err)
	}

	fields := []struct {
		field string
		dst   *string
	}{
		{cryptoutil.FieldPassword, &rec.Password},
		{cryptoutil.FieldBackupEmail, &rec.BackupEmail},
		{cryptoutil.FieldF2A, &rec.F2A},
		{cryptoutil.FieldCredentialJSON, &rec.CredentialJSON},
		{cryptoutil.FieldNotes, &rec.Notes},
		{cryptoutil.FieldRefreshToken, &rec.RefreshToken},
		{cryptoutil.FieldSMSLink, &rec.SMSLink},
	}
	for i, f := range fields {
		if len(blobs[i]) == 0 {
			continue
		}
		plain, err := s.accounts.DecryptValue(id, f.field, blobs[i])
		if err != nil {
			return nil, err
		}
		*f.dst = plain
	}

	if extra != "" {
		var parsed map[string]string
		if err := json.Unmarshal([]byte(extra), &parsed); err == nil {
			rec.Extra = parsed
		}
	}
	if tags, ok := tagIndex[id]; ok {
		rec.Tags = tags
	}
	return &rec, nil
}

// tagIndex 一次性取出账号 -> 标签名，避免 N+1。
func (s *Service) tagIndex(ctx context.Context) (map[int64][]string, error) {
	rows, err := s.db.QueryContext(ctx, `
        SELECT at.account_id, t.name
        FROM account_tags at
        JOIN tags t ON t.id = at.tag_id
        JOIN projects p ON p.id = t.project_id
        WHERE t.deleted_at IS NULL AND p.deleted_at IS NULL
        ORDER BY t.sort_order, t.id`)
	if err != nil {
		return nil, model.WrapError(500, "internal", "读取标签索引失败", err)
	}
	defer rows.Close()
	out := make(map[int64][]string)
	for rows.Next() {
		var accountID int64
		var name string
		if err := rows.Scan(&accountID, &name); err != nil {
			return nil, model.WrapError(500, "internal", "读取标签索引失败", err)
		}
		out[accountID] = append(out[accountID], name)
	}
	return out, rows.Err()
}

func (s *Service) scopeWhere(scope Scope) (string, []any, error) {
	base := "a.deleted_at IS NULL AND p.deleted_at IS NULL"
	switch scope.Type {
	case "", ScopeAll:
		return base, nil, nil
	case ScopeProject:
		if scope.ID <= 0 {
			return "", nil, model.ErrValidation("scope_invalid", "缺少项目 ID")
		}
		return base + " AND a.project_id = ?", []any{scope.ID}, nil
	case ScopeTag:
		if scope.ID <= 0 {
			return "", nil, model.ErrValidation("scope_invalid", "缺少标签 ID")
		}
		return base + ` AND EXISTS (SELECT 1 FROM account_tags at
            JOIN tags t ON t.id = at.tag_id
            WHERE at.account_id = a.id AND at.tag_id = ? AND t.deleted_at IS NULL)`,
			[]any{scope.ID}, nil
	default:
		return "", nil, model.ErrValidation("scope_invalid", "不支持的导出范围")
	}
}

// csvSafe 对可能被电子表格执行的公式前缀做转义。
func csvSafe(v string) string {
	if v == "" {
		return v
	}
	switch v[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + v
	}
	return v
}

var _ = fmt.Sprintf
