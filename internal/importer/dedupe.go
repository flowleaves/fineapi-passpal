package importer

import (
	"context"
	"strings"

	"passpal/internal/database"
	"passpal/internal/model"
)

// Deduper 在草稿创建后标记重复候选。
//
// 判据（doc §10.7）：同一项目 + 归一化邮箱。
// 不同项目、回收中的账号不参与。
type Deduper struct{ db *database.DB }

// NewDeduper 构造去重器。
func NewDeduper(db *database.DB) *Deduper { return &Deduper{db: db} }

type existingAccount struct {
	ID       int64
	Revision int64
}

// Mark 标记草稿中的重复行并重算状态。
func (d *Deduper) Mark(ctx context.Context, draft *Draft) error {
	draft.mu.Lock()
	defer draft.mu.Unlock()
	rows := draft.Rows

	// 收集有效行的归一化邮箱。
	normalized := make(map[string][]int) // normalized -> 草稿内行下标
	seen := make([]string, 0, len(rows))
	for i, r := range rows {
		if r.State == RowStateInvalid {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(r.Email))
		if key == "" {
			continue
		}
		if _, ok := normalized[key]; !ok {
			seen = append(seen, key)
		}
		normalized[key] = append(normalized[key], i)
	}

	if len(seen) == 0 {
		return nil
	}

	// 批量查库，避免 N+1。
	existing := make(map[string][]existingAccount)
	const chunk = 400
	for start := 0; start < len(seen); start += chunk {
		end := start + chunk
		if end > len(seen) {
			end = len(seen)
		}
		part := seen[start:end]
		args := make([]any, 0, len(part)+1)
		args = append(args, draft.ProjectID)
		for _, k := range part {
			args = append(args, k)
		}
		query := `SELECT email_normalized, id, revision FROM accounts
                  WHERE project_id = ? AND deleted_at IS NULL
                    AND email_normalized IN (` + placeholders(len(part)) + `)`
		rs, err := d.db.QueryContext(ctx, query, args...)
		if err != nil {
			return model.WrapError(500, "internal", "去重查询失败", err)
		}
		for rs.Next() {
			var key string
			var acc existingAccount
			if err := rs.Scan(&key, &acc.ID, &acc.Revision); err != nil {
				rs.Close()
				return model.WrapError(500, "internal", "读取去重结果失败", err)
			}
			existing[key] = append(existing[key], acc)
		}
		if err := rs.Err(); err != nil {
			rs.Close()
			return model.WrapError(500, "internal", "读取去重结果失败", err)
		}
		rs.Close()
	}

	for key, idxs := range normalized {
		hits := existing[key]
		inBatch := len(idxs) > 1

		for order, i := range idxs {
			r := rows[i]
			switch {
			case len(hits) == 1 && !inBatch:
				// 明确的单一目标，可以覆盖。
				r.Duplicate = true
				r.TargetID = hits[0].ID
				r.TargetRevision = hits[0].Revision
				r.TargetCount = 1
			case len(hits) == 0 && !inBatch:
				r.Duplicate = false
				r.TargetCount = 0
			default:
				// 库中多条同邮箱，或批内出现相同邮箱：标为冲突，不自动选目标。
				r.Duplicate = true
				r.TargetCount = len(hits)
				if inBatch {
					r.TargetCount += len(idxs)
				}
				if order == 0 && len(hits) == 1 {
					// 批内第一条仍可指向唯一的库内目标，但需用户确认。
					r.TargetID = hits[0].ID
					r.TargetRevision = hits[0].Revision
				}
			}
		}
	}

	draft.recount()
	for _, r := range draft.Rows {
		draft.reclassify(r)
	}
	return nil
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
