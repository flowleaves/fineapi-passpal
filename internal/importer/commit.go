package importer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"passpal/internal/account"
	"passpal/internal/cryptoutil"
	"passpal/internal/database"
	"passpal/internal/model"
	"passpal/internal/project"
	"passpal/internal/tag"
)

// CommitRequest 是一次提交请求。
//
// 客户端只传 preview_id / revision / commit_key / strategy，
// 服务端以草稿为准重验，不信任客户端传来的“解析成功行”。
type CommitRequest struct {
	PreviewID string
	Revision  int64
	CommitKey string
	Strategy  string
}

// CommitResult 是一次提交的结果。
type CommitResult struct {
	BatchID          int64               `json:"batch_id"`
	Summary          model.ImportSummary `json:"summary"`
	AlreadyCommitted bool                `json:"already_committed"`
}

// Committer 执行导入提交。
type Committer struct {
	db       *database.DB
	accounts *account.Service
	projects *project.Service
	tags     *tag.Service
}

// NewCommitter 构造提交器。
func NewCommitter(db *database.DB, accounts *account.Service, projects *project.Service, tags *tag.Service) *Committer {
	return &Committer{db: db, accounts: accounts, projects: projects, tags: tags}
}

// Commit 按草稿提交。同一 commit_key 只写一次。
func (c *Committer) Commit(ctx context.Context, d *Draft, req CommitRequest) (*CommitResult, error) {
	if req.PreviewID == "" || req.CommitKey == "" {
		return nil, model.ErrValidation("missing_fields", "缺少 preview_id 或 commit_key")
	}
	if req.Strategy == "" {
		req.Strategy = StrategySkip
	}
	if !IsValidStrategy(req.Strategy) {
		return nil, model.ErrValidation("strategy_invalid", "不支持的重复策略")
	}

	requestHash := hashCommitRequest(req, d)

	// 先查已成功批次：重启或草稿过期后仍能找回结果。
	existing, err := c.findBatchByKey(ctx, req.CommitKey)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if existing.requestHash != requestHash {
			return nil, model.ErrConflictCode("commit_key_reused",
				"该 commit_key 已用于不同的提交参数")
		}
		return &CommitResult{
			BatchID:          existing.id,
			Summary:          existing.summary,
			AlreadyCommitted: true,
		}, nil
	}

	if req.Revision != d.Revision {
		return nil, model.ErrConflictCode("preview_revision_conflict",
			"预览内容已变化，请重新确认后再提交")
	}

	rows := d.RowsCopy()
	summary := model.ImportSummary{}
	batchID, err := c.writeBatch(ctx, d, req, requestHash, rows, &summary)
	if err != nil {
		return nil, err
	}
	return &CommitResult{BatchID: batchID, Summary: summary}, nil
}

type batchRecord struct {
	id          int64
	requestHash string
	summary     model.ImportSummary
}

// LookupResult 描述按 commit_key 查询的结果。
//
// Status 为 committed 或 not_found；pending 由 HTTP 层结合草稿状态判定。
type LookupResult struct {
	Status  string
	BatchID int64
	Summary *model.ImportSummary
}

// Lookup 按 commit_key 查询已成功批次。已成功批次优先于草稿状态。
func (c *Committer) Lookup(ctx context.Context, commitKey string) (*LookupResult, error) {
	if commitKey == "" {
		return nil, model.ErrValidation("commit_key_required", "缺少 commit_key")
	}
	rec, err := c.findBatchByKey(ctx, commitKey)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return &LookupResult{Status: "not_found"}, nil
	}
	s := rec.summary
	return &LookupResult{Status: "committed", BatchID: rec.id, Summary: &s}, nil
}

func (c *Committer) findBatchByKey(ctx context.Context, key string) (*batchRecord, error) {
	var rec batchRecord
	err := c.db.QueryRowContext(ctx, `
        SELECT id, request_hash, total_count, success_count, updated_count,
               skipped_count, failed_count, duplicate_count
        FROM import_batches WHERE commit_key = ?`, key).
		Scan(&rec.id, &rec.requestHash, &rec.summary.Total, &rec.summary.Created,
			&rec.summary.Updated, &rec.summary.Skipped, &rec.summary.Invalid, &rec.summary.Duplicates)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, model.WrapError(500, "internal", "查询导入批次失败", err)
	}
	return &rec, nil
}

// writeBatch 在单个事务内写入账号、标签、批次计数与幂等记录。
func (c *Committer) writeBatch(ctx context.Context, d *Draft, req CommitRequest,
	requestHash string, rows []*DraftRow, summary *model.ImportSummary) (int64, error) {

	var batchID int64
	txErr := c.db.Tx(ctx, func(tx *sql.Tx) error {
		// 重新校验目标项目与标签（环境可能已变化）。
		if err := c.projects.MustBeActive(ctx, tx, d.ProjectID); err != nil {
			return err
		}
		for _, tid := range d.TagIDs {
			if err := c.tags.MustBeActiveSameProject(ctx, tx, tid, d.ProjectID); err != nil {
				return err
			}
		}

		now := time.Now().Unix()
		// 先占位插入批次以拿到 ID（计数稍后回填）。
		res, err := tx.ExecContext(ctx, `
            INSERT INTO import_batches (project_id, tag_id, commit_key, request_hash, preview_revision,
                                        total_count, success_count, failed_count, duplicate_count,
                                        skipped_count, updated_count, raw_source_hash, summary_json, created_at)
            VALUES (?, ?, ?, ?, ?, 0, 0, 0, 0, 0, 0, ?, ?, ?)`,
			d.ProjectID, firstTagOrNil(d.TagIDs), req.CommitKey, requestHash, req.Revision,
			nullIfEmptyStr(d.RawHash), nil, now)
		if err != nil {
			return model.WrapError(409, "commit_key_reused", "该提交已在进行或已完成", err)
		}
		batchID, err = res.LastInsertId()
		if err != nil {
			return err
		}

		summary.Total = len(rows)
		for _, r := range rows {
			if r.Skip {
				summary.Skipped++
				continue
			}
			switch r.State {
			case RowStateInvalid:
				summary.Invalid++
			case RowStateDuplicate:
				summary.Duplicates++
				switch req.Strategy {
				case StrategySkip:
					summary.Skipped++
				case StrategyKeep:
					if err := c.createRow(ctx, tx, d, r, batchID, now); err != nil {
						return err
					}
					summary.Created++
				case StrategyOverwrite:
					if r.TargetID == 0 {
						return model.ErrConflictCode("overwrite_target_required",
							"覆盖策略需要为每一行指定唯一目标账号")
					}
					if err := c.overwriteRow(ctx, tx, d, r, now); err != nil {
						return err
					}
					summary.Updated++
				}
			default: // RowStateNew
				if err := c.createRow(ctx, tx, d, r, batchID, now); err != nil {
					return err
				}
				summary.Created++
			}
		}

		if _, err := tx.ExecContext(ctx, `
            UPDATE import_batches
            SET total_count = ?, success_count = ?, failed_count = ?,
                duplicate_count = ?, skipped_count = ?, updated_count = ?, summary_json = ?
            WHERE id = ?`,
			summary.Total, summary.Created, summary.Invalid,
			summary.Duplicates, summary.Skipped, summary.Updated,
			buildSummaryJSON(rows), batchID); err != nil {
			return err
		}
		return nil
	})
	if txErr != nil {
		if _, ok := txErr.(*model.Error); ok {
			return 0, txErr
		}
		return 0, model.WrapError(500, "internal", "提交导入失败", txErr)
	}
	return batchID, nil
}

// createRow 新建一个账号（加密写入 + 关联标签）。
func (c *Committer) createRow(ctx context.Context, tx *sql.Tx, d *Draft, r *DraftRow, batchID, now int64) error {
	display, normalized, err := account.NormalizeEmail(r.Email)
	if err != nil {
		return model.ErrValidation("email_invalid", "第 "+itoa(r.Line)+" 行的邮箱无效")
	}
	res, err := tx.ExecContext(ctx, `
        INSERT INTO accounts (project_id, email, email_normalized, username,
                              status, revision, source_import_id, created_at, updated_at)
        VALUES (?, ?, ?, ?, 'normal', 1, ?, ?, ?)`,
		d.ProjectID, display, normalized, nullIfEmptyStr(r.Username), batchID, now, now)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	if err := c.writeSecrets(ctx, tx, id, r); err != nil {
		return err
	}
	return c.attachTags(ctx, tx, id, d.TagIDs, now)
}

// overwriteRow 覆盖已有账号：只应用非空字段，保留旧值与其他标签。
func (c *Committer) overwriteRow(ctx context.Context, tx *sql.Tx, d *Draft, r *DraftRow, now int64) error {
	var currentRev int64
	var currentProject int64
	err := tx.QueryRowContext(ctx, `
        SELECT revision, project_id FROM accounts
        WHERE id = ? AND deleted_at IS NULL`, r.TargetID).Scan(&currentRev, &currentProject)
	if errors.Is(err, sql.ErrNoRows) {
		return model.ErrConflictCode("target_gone", "目标账号已不存在或被回收")
	}
	if err != nil {
		return err
	}
	if currentProject != d.ProjectID {
		return model.ErrConflictCode("target_project_changed", "目标账号已不属于当前项目")
	}
	if r.TargetRevision != 0 && currentRev != r.TargetRevision {
		return model.ErrConflictCode("target_revision_conflict",
			"目标账号已被修改，请重新预览后再提交")
	}

	sets := []string{}
	args := []any{}
	if r.Username != "" {
		sets = append(sets, "username = ?")
		args = append(args, r.Username)
	}
	// 敏感字段：非空才覆盖，空值保留旧值（导入不负责清空秘密）。
	for _, f := range []struct {
		field string
		val   string
		col   string
	}{
		{cryptoutil.FieldPassword, r.Password, "password_encrypted"},
		{cryptoutil.FieldBackupEmail, r.BackupEmail, "backup_email_encrypted"},
		{cryptoutil.FieldF2A, r.F2A, "f2a_encrypted"},
		{cryptoutil.FieldCredentialJSON, r.CredentialJSON, "credential_json_encrypted"},
		{cryptoutil.FieldNotes, r.Notes, "notes_encrypted"},
		{cryptoutil.FieldRefreshToken, r.RefreshToken, "refresh_token_encrypted"},
		{cryptoutil.FieldSMSLink, r.SMSLink, "sms_link_encrypted"},
	} {
		if f.val == "" {
			continue
		}
		blob, err := c.accounts.EncryptValue(r.TargetID, f.field, f.val)
		if err != nil {
			return err
		}
		sets = append(sets, f.col+" = ?")
		args = append(args, blob)
	}

	sets = append(sets, "revision = revision + 1", "updated_at = ?")
	args = append(args, now, r.TargetID, currentRev)

	if _, err := tx.ExecContext(ctx,
		`UPDATE accounts SET `+joinComma(sets)+` WHERE id = ? AND revision = ?`, args...); err != nil {
		return err
	}
	// 覆盖保留原有其他标签，只追加目标标签。
	return c.attachTags(ctx, tx, r.TargetID, d.TagIDs, now)
}

func (c *Committer) writeSecrets(ctx context.Context, tx *sql.Tx, id int64, r *DraftRow) error {
	sets := []string{}
	args := []any{}
	for _, f := range []struct {
		field string
		val   string
		col   string
	}{
		{cryptoutil.FieldPassword, r.Password, "password_encrypted"},
		{cryptoutil.FieldBackupEmail, r.BackupEmail, "backup_email_encrypted"},
		{cryptoutil.FieldF2A, r.F2A, "f2a_encrypted"},
		{cryptoutil.FieldCredentialJSON, r.CredentialJSON, "credential_json_encrypted"},
		{cryptoutil.FieldNotes, r.Notes, "notes_encrypted"},
		{cryptoutil.FieldRefreshToken, r.RefreshToken, "refresh_token_encrypted"},
		{cryptoutil.FieldSMSLink, r.SMSLink, "sms_link_encrypted"},
	} {
		if f.val == "" {
			continue
		}
		blob, err := c.accounts.EncryptValue(id, f.field, f.val)
		if err != nil {
			return err
		}
		sets = append(sets, f.col+" = ?")
		args = append(args, blob)
	}
	if len(sets) == 0 {
		return nil
	}
	args = append(args, id)
	_, err := tx.ExecContext(ctx,
		`UPDATE accounts SET `+joinComma(sets)+` WHERE id = ?`, args...)
	return err
}

func (c *Committer) attachTags(ctx context.Context, tx *sql.Tx, accountID int64, tagIDs []int64, now int64) error {
	for _, tid := range tagIDs {
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO account_tags (account_id, tag_id, created_at) VALUES (?, ?, ?)
            ON CONFLICT(account_id, tag_id) DO NOTHING`, accountID, tid, now); err != nil {
			return err
		}
	}
	return nil
}

// hashCommitRequest 用规范 JSON 编码计算提交参数哈希。
func hashCommitRequest(req CommitRequest, d *Draft) string {
	type target struct {
		Row    int   `json:"row"`
		Target int64 `json:"target"`
	}
	targets := make([]target, 0)
	for _, r := range d.RowsCopy() {
		if r.TargetID != 0 {
			targets = append(targets, target{Row: r.ID, Target: r.TargetID})
		}
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].Row < targets[j].Row })

	payload := struct {
		PreviewID string   `json:"preview_id"`
		Revision  int64    `json:"preview_revision"`
		Strategy  string   `json:"strategy"`
		Targets   []target `json:"targets"`
	}{req.PreviewID, req.Revision, req.Strategy, targets}

	b, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return cryptoutil.SHA256Hex(b)
}

// buildSummaryJSON 只写白名单结构摘要：行号、校验后的主邮箱、固定错误码。
// 不含任何原文片段或敏感字段。
func buildSummaryJSON(rows []*DraftRow) string {
	type item struct {
		Line   int      `json:"line"`
		Email  string   `json:"email,omitempty"`
		State  RowState `json:"state"`
		Issues []string `json:"issues,omitempty"`
	}
	const limit = 20
	out := make([]item, 0, limit)
	for _, r := range rows {
		if len(out) >= limit {
			break
		}
		it := item{Line: r.Line, State: r.State}
		if r.State != RowStateInvalid {
			it.Email = r.Email
		}
		for _, is := range r.Issues {
			it.Issues = append(it.Issues, is.Code)
		}
		out = append(out, it)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return ""
	}
	return string(b)
}

func firstTagOrNil(ids []int64) any {
	if len(ids) == 0 {
		return nil
	}
	return ids[0]
}

func nullIfEmptyStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }
