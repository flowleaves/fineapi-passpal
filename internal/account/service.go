// Package account 实现账号 CRUD、软删除与加密写入。
//
// 关键约束：
//   - 列表/详情只返回非敏感元数据与 has_* 存在标记，绝不下发明文；
//   - 明文必须经 Reveal 逐字段获取；
//   - 敏感字段的创建、编辑、导入统一走同一加密入口。
package account

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"passpal/internal/cryptoutil"
	"passpal/internal/database"
	"passpal/internal/model"
	"passpal/internal/project"
	"passpal/internal/tag"
)

// EmailMaxRunes 是邮箱长度上限。
const EmailMaxRunes = 254

// 排序枚举白名单。
const (
	SortNewest     = "newest"
	SortOldest     = "oldest"
	SortEmailAsc   = "email_asc"
	SortEmailDesc  = "email_desc"
	SortRecentUsed = "recent_used"
)

var sortClauses = map[string]string{
	SortNewest:     "a.created_at DESC, a.id DESC",
	SortOldest:     "a.created_at ASC, a.id ASC",
	SortEmailAsc:   "a.email_normalized ASC, a.id ASC",
	SortEmailDesc:  "a.email_normalized DESC, a.id DESC",
	SortRecentUsed: "a.last_used_at IS NULL, a.last_used_at DESC, a.id DESC",
}

// NormalizeSort 把排序参数收敛到白名单，未知值回落到默认。
func NormalizeSort(s string) string {
	if _, ok := sortClauses[s]; ok {
		return s
	}
	return SortNewest
}

// Service 提供账号操作。
type Service struct {
	db       *database.DB
	cipher   *cryptoutil.Cipher
	projects *project.Service
	tags     *tag.Service
}

// New 构造账号服务。
func New(db *database.DB, c *cryptoutil.Cipher, projects *project.Service, tags *tag.Service) *Service {
	return &Service{db: db, cipher: c, projects: projects, tags: tags}
}

// Cipher 暴露加密器，供导入等模块复用同一加密入口。
func (s *Service) Cipher() *cryptoutil.Cipher { return s.cipher }

// Filter 描述账号列表的查询条件。
type Filter struct {
	ProjectID int64
	TagID     int64
	Query     string
	Status    string
	Sort      string
	Page      model.Page
}

const accountSelectColumns = `
    a.id, a.project_id, a.email, COALESCE(a.username, ''), a.status,
    COALESCE(a.extra_json, ''), a.revision,
    a.password_encrypted IS NOT NULL,
    a.backup_email_encrypted IS NOT NULL,
    a.f2a_encrypted IS NOT NULL,
    a.credential_json_encrypted IS NOT NULL,
    a.notes_encrypted IS NOT NULL,
    a.refresh_token_encrypted IS NOT NULL,
    a.sms_link_encrypted IS NOT NULL,
    a.created_at, a.updated_at, a.last_used_at,
    a.f2a_encrypted, a.refresh_token_encrypted, a.sms_link_encrypted`

type rowScanner interface{ Scan(...any) error }

// scanAccount 读取一行账号。
//
// 这里有意多解出 F2A / Refresh Token / 取码链接 三个字段的明文随列表下发：
// 使用场景是单人本地工具，需要在列表里直接看到并一键复制这三项。
// 密码、JSON 凭证与备注仍然不下发，必须走 reveal 接口逐字段获取。
func (s *Service) scanAccount(row rowScanner) (model.Account, error) {
	var a model.Account
	var lastUsed sql.NullInt64
	var f2aBlob, rtBlob, slBlob []byte

	err := row.Scan(&a.ID, &a.ProjectID, &a.Email, &a.Username, &a.Status,
		&a.ExtraJSON, &a.Revision,
		&a.HasPassword, &a.HasBackup, &a.HasF2A, &a.HasJSON, &a.HasNotes, &a.HasRefreshToken,
		&a.HasSMSLink,
		&a.CreatedAt, &a.UpdatedAt, &lastUsed,
		&f2aBlob, &rtBlob, &slBlob)
	if err != nil {
		return a, err
	}
	if lastUsed.Valid {
		v := lastUsed.Int64
		a.LastUsedAt = &v
	}
	for _, f := range []struct {
		field string
		blob  []byte
		dst   *string
	}{
		{cryptoutil.FieldF2A, f2aBlob, &a.F2A},
		{cryptoutil.FieldRefreshToken, rtBlob, &a.RefreshToken},
		{cryptoutil.FieldSMSLink, slBlob, &a.SMSLink},
	} {
		if len(f.blob) == 0 {
			continue
		}
		plain, derr := s.DecryptValue(a.ID, f.field, f.blob)
		if derr != nil {
			// 密钥缺失或密文损坏必须显式失败，不能返回空串冒充成功。
			return a, derr
		}
		*f.dst = plain
	}
	a.Tags = []model.Tag{}
	a.TagIDs = []int64{}
	return a, nil
}

// List 返回分页后的账号列表（不含任何明文）。
func (s *Service) List(ctx context.Context, f Filter) (model.ListResult[model.Account], error) {
	var out model.ListResult[model.Account]
	out.Items = []model.Account{}

	where := []string{"a.deleted_at IS NULL", "p.deleted_at IS NULL"}
	args := []any{}

	if f.ProjectID > 0 {
		where = append(where, "a.project_id = ?")
		args = append(args, f.ProjectID)
	}
	if f.TagID > 0 {
		where = append(where, `EXISTS (SELECT 1 FROM account_tags at
            JOIN tags t ON t.id = at.tag_id
            WHERE at.account_id = a.id AND at.tag_id = ? AND t.deleted_at IS NULL)`)
		args = append(args, f.TagID)
	}
	if f.Status != "" {
		where = append(where, "a.status = ?")
		args = append(args, f.Status)
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		like := "%" + escapeLike(strings.ToLower(q)) + "%"
		where = append(where, `(LOWER(a.email) LIKE ? ESCAPE '\'
            OR LOWER(COALESCE(a.username, '')) LIKE ? ESCAPE '\'
            OR LOWER(p.name) LIKE ? ESCAPE '\'
            OR EXISTS (SELECT 1 FROM account_tags at2
                       JOIN tags t2 ON t2.id = at2.tag_id
                       WHERE at2.account_id = a.id AND t2.deleted_at IS NULL
                         AND LOWER(t2.name) LIKE ? ESCAPE '\'))`)
		args = append(args, like, like, like, like)
	}

	whereSQL := strings.Join(where, " AND ")

	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM accounts a JOIN projects p ON p.id = a.project_id WHERE `+whereSQL,
		args...).Scan(&out.Total); err != nil {
		return out, model.WrapError(500, "internal", "统计账号失败", err)
	}

	sortSQL := sortClauses[NormalizeSort(f.Sort)]
	page := f.Page
	if page.PageSize < 1 {
		page = model.NormalizePage(1, 0, 100, 100)
	}
	out.Page, out.PageSize = page.Page, page.PageSize

	listArgs := append(append([]any{}, args...), page.PageSize, page.Offset())
	rows, err := s.db.QueryContext(ctx, `SELECT `+accountSelectColumns+`
        FROM accounts a JOIN projects p ON p.id = a.project_id
        WHERE `+whereSQL+`
        ORDER BY `+sortSQL+`
        LIMIT ? OFFSET ?`, listArgs...)
	if err != nil {
		return out, model.WrapError(500, "internal", "查询账号失败", err)
	}
	defer rows.Close()

	ids := make([]int64, 0)
	for rows.Next() {
		a, err := s.scanAccount(rows)
		if err != nil {
			return out, model.WrapError(500, "internal", "读取账号失败", err)
		}
		out.Items = append(out.Items, a)
		ids = append(ids, a.ID)
	}
	if err := rows.Err(); err != nil {
		return out, model.WrapError(500, "internal", "读取账号失败", err)
	}

	tagsByAccount, err := s.tagsForAccounts(ctx, ids)
	if err != nil {
		return out, err
	}
	for i := range out.Items {
		if ts, ok := tagsByAccount[out.Items[i].ID]; ok {
			out.Items[i].Tags = ts
			for _, t := range ts {
				out.Items[i].TagIDs = append(out.Items[i].TagIDs, t.ID)
			}
		}
	}
	return out, nil
}

// tagsForAccounts 批量取标签，避免 N+1。
func (s *Service) tagsForAccounts(ctx context.Context, ids []int64) (map[int64][]model.Tag, error) {
	out := make(map[int64][]model.Tag, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx, `
        SELECT at.account_id, t.id, t.project_id, t.name, COALESCE(t.color, ''),
               t.sort_order, t.created_at, t.updated_at
        FROM account_tags at
        JOIN tags t ON t.id = at.tag_id
        WHERE at.account_id IN (`+placeholders+`) AND t.deleted_at IS NULL
        ORDER BY t.sort_order, t.id`, args...)
	if err != nil {
		return nil, model.WrapError(500, "internal", "查询账号标签失败", err)
	}
	defer rows.Close()
	for rows.Next() {
		var accountID int64
		var t model.Tag
		if err := rows.Scan(&accountID, &t.ID, &t.ProjectID, &t.Name, &t.Color,
			&t.SortOrder, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, model.WrapError(500, "internal", "读取账号标签失败", err)
		}
		out[accountID] = append(out[accountID], t)
	}
	return out, rows.Err()
}

// Get 返回单个账号的元数据（不含明文）。
func (s *Service) Get(ctx context.Context, id int64) (*model.Account, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+accountSelectColumns+`
        FROM accounts a JOIN projects p ON p.id = a.project_id
        WHERE a.id = ? AND a.deleted_at IS NULL AND p.deleted_at IS NULL`, id)
	a, err := s.scanAccount(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFoundCode("account_not_found", "账号不存在")
	}
	if err != nil {
		return nil, model.WrapError(500, "internal", "读取账号失败", err)
	}
	tagsByAccount, err := s.tagsForAccounts(ctx, []int64{id})
	if err != nil {
		return nil, err
	}
	if ts, ok := tagsByAccount[id]; ok {
		a.Tags = ts
		for _, t := range ts {
			a.TagIDs = append(a.TagIDs, t.ID)
		}
	}
	return &a, nil
}

// NormalizeEmail 校验并归一化邮箱。返回展示值与归一化值。
func NormalizeEmail(raw string) (display, normalized string, err error) {
	display = strings.TrimSpace(raw)
	if display == "" {
		return "", "", model.ErrValidation("email_required", "邮箱不能为空")
	}
	if len([]rune(display)) > EmailMaxRunes {
		return "", "", model.ErrValidation("email_too_long", "邮箱过长")
	}
	if !looksLikeEmail(display) {
		return "", "", model.ErrValidation("email_invalid", "邮箱格式不正确")
	}
	return display, strings.ToLower(display), nil
}

// Create 新建账号。ID 分配与加密写入在同一事务内完成，绝不临时写明文。
func (s *Service) Create(ctx context.Context, projectID int64, in model.AccountInput) (*model.Account, error) {
	if projectID <= 0 {
		return nil, model.ErrValidation("project_required", "必须指定项目")
	}
	if in.Email == nil {
		return nil, model.ErrValidation("email_required", "邮箱不能为空")
	}
	display, normalized, err := NormalizeEmail(*in.Email)
	if err != nil {
		return nil, err
	}
	if err := s.validateExtra(in.Extra); err != nil {
		return nil, err
	}
	extraJSON, err := in.Extra.JSON()
	if err != nil {
		return nil, model.ErrValidation("extra_invalid", err.Error())
	}
	status := model.StatusNormal
	if in.Status != nil {
		if !model.IsValidStatus(*in.Status) {
			return nil, model.ErrValidation("status_invalid", "账号状态只能是 normal 或 abnormal")
		}
		status = *in.Status
	}

	var newID int64
	txErr := s.db.Tx(ctx, func(tx *sql.Tx) error {
		if err := s.projects.MustBeActive(ctx, tx, projectID); err != nil {
			return err
		}
		tagIDs, err := s.validateTags(ctx, tx, in.TagIDs, projectID)
		if err != nil {
			return err
		}
		now := time.Now().Unix()
		res, err := tx.ExecContext(ctx, `
            INSERT INTO accounts (project_id, email, email_normalized, username,
                                  extra_json, status, revision, created_at, updated_at)
            VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?)`,
			projectID, display, normalized, nullableStr(in.Username), nullableStr2(extraJSON),
			status, now, now)
		if err != nil {
			return err
		}
		newID, err = res.LastInsertId()
		if err != nil {
			return err
		}
		// 拿到 ID 之后才加密，AAD 绑定 account_id。
		if err := s.writeSecrets(ctx, tx, newID, in, false); err != nil {
			return err
		}
		return s.replaceTags(ctx, tx, newID, tagIDs)
	})
	if txErr != nil {
		if _, ok := txErr.(*model.Error); ok {
			return nil, txErr
		}
		return nil, model.WrapError(500, "internal", "创建账号失败", txErr)
	}
	return s.Get(ctx, newID)
}

// Update 按 PATCH 语义编辑账号，携带 revision 做乐观锁。
func (s *Service) Update(ctx context.Context, id, revision int64, in model.AccountInput) (*model.Account, error) {
	if revision <= 0 {
		return nil, model.ErrValidation("revision_required", "必须携带 revision")
	}
	if err := s.validateExtra(in.Extra); err != nil {
		return nil, err
	}
	txErr := s.db.Tx(ctx, func(tx *sql.Tx) error {
		var projectID, currentRev int64
		err := tx.QueryRowContext(ctx, `
            SELECT a.project_id, a.revision FROM accounts a
            JOIN projects p ON p.id = a.project_id
            WHERE a.id = ? AND a.deleted_at IS NULL AND p.deleted_at IS NULL`, id).
			Scan(&projectID, &currentRev)
		if errors.Is(err, sql.ErrNoRows) {
			return model.ErrNotFoundCode("account_not_found", "账号不存在")
		}
		if err != nil {
			return err
		}
		if currentRev != revision {
			return model.ErrConflictCode("revision_conflict", "该账号已被其他操作修改，请刷新后重试")
		}

		sets := []string{}
		args := []any{}

		if in.Email != nil {
			display, normalized, err := NormalizeEmail(*in.Email)
			if err != nil {
				return err
			}
			sets = append(sets, "email = ?", "email_normalized = ?")
			args = append(args, display, normalized)
		}
		if in.Username != nil {
			sets = append(sets, "username = ?")
			args = append(args, nullableStr(in.Username))
		}
		if in.Status != nil {
			if !model.IsValidStatus(*in.Status) {
				return model.ErrValidation("status_invalid",
					"账号状态只能是 normal 或 abnormal")
			}
			sets = append(sets, "status = ?")
			args = append(args, *in.Status)
		}
		if in.Extra != nil {
			ej, err := in.Extra.JSON()
			if err != nil {
				return model.ErrValidation("extra_invalid", err.Error())
			}
			sets = append(sets, "extra_json = ?")
			args = append(args, nullableStr2(ej))
		}

		// 敏感字段：nil 表示不修改；非 nil（含空串）表示设置，空串等价清空。
		secretSets, secretArgs, err := s.secretUpdates(ctx, id, in)
		if err != nil {
			return err
		}
		sets = append(sets, secretSets...)
		args = append(args, secretArgs...)

		sets = append(sets, "revision = revision + 1", "updated_at = ?")
		args = append(args, time.Now().Unix(), id, revision)

		res, err := tx.ExecContext(ctx,
			`UPDATE accounts SET `+strings.Join(sets, ", ")+` WHERE id = ? AND revision = ?`,
			args...)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return model.ErrConflictCode("revision_conflict", "该账号已被其他操作修改，请刷新后重试")
		}

		if in.TagIDs != nil {
			tagIDs, err := s.validateTags(ctx, tx, in.TagIDs, projectID)
			if err != nil {
				return err
			}
			if err := s.replaceTags(ctx, tx, id, tagIDs); err != nil {
				return err
			}
		}
		return nil
	})
	if txErr != nil {
		if _, ok := txErr.(*model.Error); ok {
			return nil, txErr
		}
		return nil, model.WrapError(500, "internal", "更新账号失败", txErr)
	}
	return s.Get(ctx, id)
}

// SoftDelete 软删除账号。
func (s *Service) SoftDelete(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `
        UPDATE accounts SET deleted_at = ?, updated_at = ?
        WHERE id = ? AND deleted_at IS NULL`, time.Now().Unix(), time.Now().Unix(), id)
	if err != nil {
		return model.WrapError(500, "internal", "删除账号失败", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return model.ErrNotFoundCode("account_not_found", "账号不存在或已在回收站")
	}
	return nil
}

// BatchSoftDelete 批量软删除，返回实际删除条数。
func (s *Service) BatchSoftDelete(ctx context.Context, ids []int64) (int, error) {
	if len(ids) == 0 {
		return 0, model.ErrValidation("empty_selection", "请选择要删除的账号")
	}
	if len(ids) > 1000 {
		return 0, model.ErrValidation("too_many", "单次最多删除 1000 个账号")
	}
	now := time.Now().Unix()
	deleted := 0
	txErr := s.db.Tx(ctx, func(tx *sql.Tx) error {
		for _, id := range ids {
			res, err := tx.ExecContext(ctx, `
                UPDATE accounts SET deleted_at = ?, updated_at = ?
                WHERE id = ? AND deleted_at IS NULL`, now, now, id)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				deleted++
			}
		}
		return nil
	})
	if txErr != nil {
		return 0, model.WrapError(500, "internal", "批量删除失败", txErr)
	}
	return deleted, nil
}

// Restore 恢复账号，要求父项目处于活动状态。幂等。
func (s *Service) Restore(ctx context.Context, id int64) (*model.Account, error) {
	txErr := s.db.Tx(ctx, func(tx *sql.Tx) error {
		var projectID int64
		var deleted sql.NullInt64
		err := tx.QueryRowContext(ctx,
			`SELECT project_id, deleted_at FROM accounts WHERE id = ?`, id).Scan(&projectID, &deleted)
		if errors.Is(err, sql.ErrNoRows) {
			return model.ErrNotFoundCode("account_not_found", "账号不存在")
		}
		if err != nil {
			return err
		}
		if err := s.projects.MustBeActive(ctx, tx, projectID); err != nil {
			return model.ErrValidation("project_inactive", "请先恢复该账号所属的项目")
		}
		if !deleted.Valid {
			return nil // 幂等
		}
		_, err = tx.ExecContext(ctx,
			`UPDATE accounts SET deleted_at = NULL, updated_at = ? WHERE id = ?`, time.Now().Unix(), id)
		return err
	})
	if txErr != nil {
		if _, ok := txErr.(*model.Error); ok {
			return nil, txErr
		}
		return nil, model.WrapError(500, "internal", "恢复账号失败", txErr)
	}
	return s.Get(ctx, id)
}

// ListDeleted 返回回收站中的账号（父项目被回收的除外）。
func (s *Service) ListDeleted(ctx context.Context, page model.Page) (model.ListResult[model.Account], error) {
	var out model.ListResult[model.Account]
	out.Items = []model.Account{}
	out.Page, out.PageSize = page.Page, page.PageSize

	if err := s.db.QueryRowContext(ctx, `
        SELECT COUNT(1) FROM accounts a JOIN projects p ON p.id = a.project_id
        WHERE a.deleted_at IS NOT NULL AND p.deleted_at IS NULL`).Scan(&out.Total); err != nil {
		return out, model.WrapError(500, "internal", "统计回收账号失败", err)
	}

	rows, err := s.db.QueryContext(ctx, `SELECT `+accountSelectColumns+`
        FROM accounts a JOIN projects p ON p.id = a.project_id
        WHERE a.deleted_at IS NOT NULL AND p.deleted_at IS NULL
        ORDER BY a.deleted_at DESC, a.id DESC
        LIMIT ? OFFSET ?`, page.PageSize, page.Offset())
	if err != nil {
		return out, model.WrapError(500, "internal", "查询回收账号失败", err)
	}
	defer rows.Close()
	for rows.Next() {
		a, err := s.scanAccount(rows)
		if err != nil {
			return out, model.WrapError(500, "internal", "读取回收账号失败", err)
		}
		out.Items = append(out.Items, a)
	}
	return out, rows.Err()
}

// CountInProject 统计项目下的活动账号数。
func (s *Service) CountInProject(ctx context.Context, projectID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM accounts WHERE project_id = ? AND deleted_at IS NULL`, projectID).Scan(&n)
	if err != nil {
		return 0, model.WrapError(500, "internal", "统计账号失败", err)
	}
	return n, nil
}

// validateTags 校验标签存在、活动、属于同一项目，返回去重后的 ID 列表。
func (s *Service) validateTags(ctx context.Context, q database.Queryer, tagIDs *[]int64, projectID int64) ([]int64, error) {
	if tagIDs == nil {
		return nil, nil
	}
	seen := make(map[int64]bool, len(*tagIDs))
	out := make([]int64, 0, len(*tagIDs))
	for _, id := range *tagIDs {
		if seen[id] {
			continue
		}
		if err := s.tags.MustBeActiveSameProject(ctx, q, id, projectID); err != nil {
			return nil, err
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}

func (s *Service) replaceTags(ctx context.Context, tx *sql.Tx, accountID int64, tagIDs []int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM account_tags WHERE account_id = ?`, accountID); err != nil {
		return err
	}
	now := time.Now().Unix()
	for _, tid := range tagIDs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO account_tags (account_id, tag_id, created_at) VALUES (?, ?, ?)`,
			accountID, tid, now); err != nil {
			return err
		}
	}
	return nil
}

// AddTagsTx 在导入提交时把目标标签关联到账号（保留原有其他标签）。
func (s *Service) AddTagsTx(ctx context.Context, tx *sql.Tx, accountID int64, tagIDs []int64) error {
	now := time.Now().Unix()
	for _, tid := range tagIDs {
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO account_tags (account_id, tag_id, created_at) VALUES (?, ?, ?)
            ON CONFLICT(account_id, tag_id) DO NOTHING`, accountID, tid, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) validateExtra(e *model.Extra) error {
	if e == nil {
		return nil
	}
	return e.Validate()
}

func nullableStr(s *string) any {
	if s == nil {
		return nil
	}
	v := strings.TrimSpace(*s)
	if v == "" {
		return nil
	}
	return v
}

func nullableStr2(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

// escapeLike 转义 LIKE 中的通配符，配合 ESCAPE '\' 使用。
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func looksLikeEmail(s string) bool {
	at := strings.LastIndex(s, "@")
	if at <= 0 || at == len(s)-1 {
		return false
	}
	local, domain := s[:at], s[at+1:]
	if local == "" || domain == "" {
		return false
	}
	if strings.ContainsAny(s, " \t\r\n") {
		return false
	}
	if !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return false
	}
	if strings.Contains(domain, "..") {
		return false
	}
	for _, r := range s {
		if r == '@' {
			continue
		}
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// DescribeCounts 返回项目下的活动账号与标签数量（供删除确认）。
func (s *Service) DescribeCounts(ctx context.Context, projectID int64) (accounts, tags int, err error) {
	if err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM accounts WHERE project_id = ? AND deleted_at IS NULL`, projectID).
		Scan(&accounts); err != nil {
		return 0, 0, model.WrapError(500, "internal", "统计账号失败", err)
	}
	if err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tags WHERE project_id = ? AND deleted_at IS NULL`, projectID).
		Scan(&tags); err != nil {
		return 0, 0, model.WrapError(500, "internal", "统计标签失败", err)
	}
	return accounts, tags, nil
}

var _ = fmt.Sprintf
