package account

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"passpal/internal/cryptoutil"
	"passpal/internal/model"
)

// secretColumn 把字段枚举映射到数据库列名。
// 列名来自白名单，不接受任意字符串，因此可以安全拼接进 SQL。
func secretColumn(field string) (string, bool) {
	switch field {
	case cryptoutil.FieldPassword:
		return "password_encrypted", true
	case cryptoutil.FieldBackupEmail:
		return "backup_email_encrypted", true
	case cryptoutil.FieldF2A:
		return "f2a_encrypted", true
	case cryptoutil.FieldCredentialJSON:
		return "credential_json_encrypted", true
	case cryptoutil.FieldNotes:
		return "notes_encrypted", true
	case cryptoutil.FieldRefreshToken:
		return "refresh_token_encrypted", true
	case cryptoutil.FieldSMSLink:
		return "sms_link_encrypted", true
	}
	return "", false
}

// EncryptValue 加密单个字段值。空串表示清空，写入 NULL。
func (s *Service) EncryptValue(accountID int64, field, plain string) (any, error) {
	if plain == "" {
		return nil, nil
	}
	blob, err := s.cipher.Encrypt([]byte(plain), cryptoutil.AccountAAD(accountID, field))
	if err != nil {
		return nil, model.WrapError(500, "encrypt_failed", "加密失败", err)
	}
	return blob, nil
}

// DecryptValue 解密单个字段值。
func (s *Service) DecryptValue(accountID int64, field string, blob []byte) (string, error) {
	if len(blob) == 0 {
		return "", nil
	}
	pt, err := s.cipher.Decrypt(blob, cryptoutil.AccountAAD(accountID, field))
	if err != nil {
		return "", model.WrapError(500, "decrypt_failed", "解密失败", err)
	}
	return string(pt), nil
}

// secretSetClauses 生成敏感字段的 SET 片段。
//
// PATCH 语义：字段为 nil 表示不修改；非 nil（含空串）表示设置，空串等价清空。
func (s *Service) secretSetClauses(accountID int64, in model.AccountInput) ([]string, []any, error) {
	pairs := []struct {
		field string
		col   string
		val   *string
	}{
		{cryptoutil.FieldPassword, "password_encrypted", in.Password},
		{cryptoutil.FieldBackupEmail, "backup_email_encrypted", in.BackupEmail},
		{cryptoutil.FieldF2A, "f2a_encrypted", in.F2A},
		{cryptoutil.FieldCredentialJSON, "credential_json_encrypted", in.CredentialJSON},
		{cryptoutil.FieldNotes, "notes_encrypted", in.Notes},
		{cryptoutil.FieldRefreshToken, "refresh_token_encrypted", in.RefreshToken},
		{cryptoutil.FieldSMSLink, "sms_link_encrypted", in.SMSLink},
	}

	var sets []string
	var args []any
	for _, p := range pairs {
		if p.val == nil {
			continue
		}
		v, err := s.EncryptValue(accountID, p.field, *p.val)
		if err != nil {
			return nil, nil, err
		}
		sets = append(sets, p.col+" = ?")
		args = append(args, v)
	}
	return sets, args, nil
}

// writeSecrets 在创建路径上写入敏感字段。
func (s *Service) writeSecrets(ctx context.Context, tx *sql.Tx, accountID int64, in model.AccountInput, _ bool) error {
	sets, args, err := s.secretSetClauses(accountID, in)
	if err != nil {
		return err
	}
	if len(sets) == 0 {
		return nil
	}
	args = append(args, accountID)
	_, err = tx.ExecContext(ctx,
		`UPDATE accounts SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		return fmt.Errorf("写入加密字段失败：%w", err)
	}
	return nil
}

// secretUpdates 是 Update 路径的包装。
func (s *Service) secretUpdates(_ context.Context, accountID int64, in model.AccountInput) ([]string, []any, error) {
	return s.secretSetClauses(accountID, in)
}

// Reveal 解密并返回单个字段的明文。
//
// 调用方负责写审计（REVEAL_SECRET）并保证响应 no-store。
func (s *Service) Reveal(ctx context.Context, id int64, field string) (string, error) {
	col, ok := secretColumn(field)
	if !ok {
		return "", model.ErrValidation("field_invalid", "不支持的字段")
	}

	var blob []byte
	err := s.db.QueryRowContext(ctx, `
        SELECT a.`+col+`
        FROM accounts a JOIN projects p ON p.id = a.project_id
        WHERE a.id = ? AND a.deleted_at IS NULL AND p.deleted_at IS NULL`, id).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return "", model.ErrNotFoundCode("account_not_found", "账号不存在或已回收")
	}
	if err != nil {
		return "", model.WrapError(500, "internal", "读取字段失败", err)
	}

	plain, err := s.DecryptValue(id, field, blob)
	if err != nil {
		return "", err
	}

	// 成功取密才更新 last_used_at：表示本工具内取密时间，不代表第三方登录时间。
	if _, err := s.db.ExecContext(ctx,
		`UPDATE accounts SET last_used_at = ? WHERE id = ?`, time.Now().Unix(), id); err != nil {
		// 不因统计失败而否定已经成功的取密。
		_ = err
	}
	return plain, nil
}

// Secrets 读取账号的全部明文秘密（导出与「复制全部」使用）。
func (s *Service) Secrets(ctx context.Context, id int64) (*model.AccountSecrets, error) {
	var (
		pw, be, f2a, cj, nt, rt, sl []byte
	)
	err := s.db.QueryRowContext(ctx, `
        SELECT a.password_encrypted, a.backup_email_encrypted, a.f2a_encrypted,
               a.credential_json_encrypted, a.notes_encrypted, a.refresh_token_encrypted,
               a.sms_link_encrypted
        FROM accounts a JOIN projects p ON p.id = a.project_id
        WHERE a.id = ? AND a.deleted_at IS NULL AND p.deleted_at IS NULL`, id).
		Scan(&pw, &be, &f2a, &cj, &nt, &rt, &sl)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFoundCode("account_not_found", "账号不存在或已回收")
	}
	if err != nil {
		return nil, model.WrapError(500, "internal", "读取账号秘密失败", err)
	}

	out := &model.AccountSecrets{}
	for _, f := range []struct {
		field string
		blob  []byte
		dst   **string
	}{
		{cryptoutil.FieldPassword, pw, &out.Password},
		{cryptoutil.FieldBackupEmail, be, &out.BackupEmail},
		{cryptoutil.FieldF2A, f2a, &out.F2A},
		{cryptoutil.FieldCredentialJSON, cj, &out.CredentialJSON},
		{cryptoutil.FieldNotes, nt, &out.Notes},
		{cryptoutil.FieldRefreshToken, rt, &out.RefreshToken},
		{cryptoutil.FieldSMSLink, sl, &out.SMSLink},
	} {
		if len(f.blob) == 0 {
			continue
		}
		plain, err := s.DecryptValue(id, f.field, f.blob)
		if err != nil {
			return nil, err
		}
		v := plain
		*f.dst = &v
	}
	return out, nil
}

// MarkUsed 更新 last_used_at。
func (s *Service) MarkUsed(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE accounts SET last_used_at = ? WHERE id = ?`, time.Now().Unix(), id)
	if err != nil {
		return model.WrapError(500, "internal", "更新使用时间失败", err)
	}
	return nil
}

// RefreshTokenEntry 是批量导出的一行。
type RefreshTokenEntry struct {
	Email        string `json:"email"`
	RefreshToken string `json:"refresh_token,omitempty"`
}

// MaxBatchExport 是单次批量导出的账号数上限。
const MaxBatchExport = 5000

// ExportRefreshTokens 批量读取指定账号的 refresh token，保持传入顺序。
//
// 没有 refresh token 的账号也会返回（值为空），方便使用者看出哪些没抓到。
func (s *Service) ExportRefreshTokens(ctx context.Context, ids []int64) ([]RefreshTokenEntry, error) {
	if len(ids) == 0 {
		return nil, model.ErrValidation("empty_selection", "请先选择账号")
	}
	if len(ids) > MaxBatchExport {
		return nil, model.ErrValidation("too_many",
			"单次最多导出 "+itoaInt(MaxBatchExport)+" 个账号")
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}

	rows, err := s.db.QueryContext(ctx, `
        SELECT a.id, a.email, a.refresh_token_encrypted
        FROM accounts a JOIN projects p ON p.id = a.project_id
        WHERE a.deleted_at IS NULL AND p.deleted_at IS NULL
          AND a.id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, model.WrapError(500, "internal", "读取账号失败", err)
	}
	defer rows.Close()

	byID := make(map[int64]RefreshTokenEntry, len(ids))
	for rows.Next() {
		var (
			id    int64
			email string
			blob  []byte
		)
		if err := rows.Scan(&id, &email, &blob); err != nil {
			return nil, model.WrapError(500, "internal", "读取账号失败", err)
		}
		entry := RefreshTokenEntry{Email: email}
		if len(blob) > 0 {
			plain, derr := s.DecryptValue(id, cryptoutil.FieldRefreshToken, blob)
			if derr != nil {
				return nil, derr
			}
			entry.RefreshToken = plain
		}
		byID[id] = entry
	}
	if err := rows.Err(); err != nil {
		return nil, model.WrapError(500, "internal", "读取账号失败", err)
	}

	out := make([]RefreshTokenEntry, 0, len(ids))
	for _, id := range ids {
		if e, ok := byID[id]; ok {
			out = append(out, e)
		}
	}
	return out, nil
}

func itoaInt(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
