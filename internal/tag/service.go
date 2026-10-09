// Package tag 实现标签 CRUD、排序与软删除/恢复。
//
// 标签必须属于某个项目；所有跨模块调用都必须校验标签与账号同项目。
package tag

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"passpal/internal/database"
	"passpal/internal/model"
)

// NameMaxRunes 是标签名长度上限。
const NameMaxRunes = 64

var colorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// Service 提供标签操作。
type Service struct{ db *database.DB }

// New 构造标签服务。
func New(db *database.DB) *Service { return &Service{db: db} }

// Input 是创建/编辑标签的输入。
type Input struct {
	Name  string
	Color string
}

const tagColumns = `
    t.id, t.project_id, t.name, COALESCE(t.color, ''),
    t.sort_order, t.created_at, t.updated_at,
    (SELECT COUNT(*) FROM account_tags at
       JOIN accounts a ON a.id = at.account_id
      WHERE at.tag_id = t.id AND a.deleted_at IS NULL)`

func scanTag(row interface{ Scan(...any) error }) (model.Tag, error) {
	var t model.Tag
	err := row.Scan(&t.ID, &t.ProjectID, &t.Name, &t.Color, &t.SortOrder,
		&t.CreatedAt, &t.UpdatedAt, &t.AccountCount)
	return t, err
}

// ListByProject 返回某项目下的活动标签。
func (s *Service) ListByProject(ctx context.Context, projectID int64) ([]model.Tag, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+tagColumns+`
        FROM tags t
        JOIN projects p ON p.id = t.project_id
        WHERE t.project_id = ? AND t.deleted_at IS NULL AND p.deleted_at IS NULL
        ORDER BY t.sort_order, t.id`, projectID)
	if err != nil {
		return nil, model.WrapError(500, "internal", "查询标签失败", err)
	}
	defer rows.Close()
	out := make([]model.Tag, 0)
	for rows.Next() {
		t, err := scanTag(rows)
		if err != nil {
			return nil, model.WrapError(500, "internal", "读取标签失败", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Get 返回单个活动标签。
func (s *Service) Get(ctx context.Context, id int64) (*model.Tag, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+tagColumns+`
        FROM tags t WHERE t.id = ? AND t.deleted_at IS NULL`, id)
	t, err := scanTag(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFoundCode("tag_not_found", "标签不存在")
	}
	if err != nil {
		return nil, model.WrapError(500, "internal", "读取标签失败", err)
	}
	return &t, nil
}

// MustBeActiveSameProject 校验标签活动、属于活动项目，且与给定项目一致。
func (s *Service) MustBeActiveSameProject(ctx context.Context, q database.Queryer, tagID, projectID int64) error {
	var got int64
	err := q.QueryRowContext(ctx, `
        SELECT t.project_id FROM tags t
        JOIN projects p ON p.id = t.project_id
        WHERE t.id = ? AND t.deleted_at IS NULL AND p.deleted_at IS NULL`, tagID).Scan(&got)
	if errors.Is(err, sql.ErrNoRows) {
		return model.ErrValidation("tag_inactive", "标签不存在、已回收或其项目已回收")
	}
	if err != nil {
		return model.WrapError(500, "internal", "校验标签状态失败", err)
	}
	if got != projectID {
		return model.ErrValidation("tag_project_mismatch", "标签与账号必须属于同一项目")
	}
	return nil
}

// Create 在指定项目下新建标签。
func (s *Service) Create(ctx context.Context, projectID int64, in Input) (*model.Tag, error) {
	name, color, err := normalize(in)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	var id int64
	txErr := s.db.Tx(ctx, func(tx *sql.Tx) error {
		var projActive int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(1) FROM projects WHERE id = ? AND deleted_at IS NULL`, projectID).Scan(&projActive); err != nil {
			return err
		}
		if projActive == 0 {
			return model.ErrValidation("project_inactive", "目标项目不存在或已移入回收站")
		}
		var dup int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(1) FROM tags WHERE project_id = ? AND name = ?`, projectID, name).Scan(&dup); err != nil {
			return err
		}
		if dup > 0 {
			return model.ErrConflictCode("name_taken", "该项目下已存在同名标签（可能在回收站中）")
		}
		var maxOrder sql.NullInt64
		if err := tx.QueryRowContext(ctx,
			`SELECT MAX(sort_order) FROM tags WHERE project_id = ? AND deleted_at IS NULL`, projectID).Scan(&maxOrder); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `
            INSERT INTO tags (project_id, name, color, sort_order, created_at, updated_at)
            VALUES (?, ?, ?, ?, ?, ?)`,
			projectID, name, nullIfEmpty(color), int(maxOrder.Int64)+10, now, now)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	if txErr != nil {
		if _, ok := txErr.(*model.Error); ok {
			return nil, txErr
		}
		return nil, model.WrapError(500, "internal", "创建标签失败", txErr)
	}
	return s.Get(ctx, id)
}

// Update 编辑标签名与颜色。
func (s *Service) Update(ctx context.Context, id int64, in Input) (*model.Tag, error) {
	name, color, err := normalize(in)
	if err != nil {
		return nil, err
	}
	txErr := s.db.Tx(ctx, func(tx *sql.Tx) error {
		var projectID int64
		err := tx.QueryRowContext(ctx,
			`SELECT project_id FROM tags WHERE id = ? AND deleted_at IS NULL`, id).Scan(&projectID)
		if errors.Is(err, sql.ErrNoRows) {
			return model.ErrNotFoundCode("tag_not_found", "标签不存在")
		}
		if err != nil {
			return err
		}
		var dup int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(1) FROM tags WHERE project_id = ? AND name = ? AND id <> ?`,
			projectID, name, id).Scan(&dup); err != nil {
			return err
		}
		if dup > 0 {
			return model.ErrConflictCode("name_taken", "该项目下已存在同名标签")
		}
		_, err = tx.ExecContext(ctx,
			`UPDATE tags SET name = ?, color = ?, updated_at = ? WHERE id = ?`,
			name, nullIfEmpty(color), time.Now().Unix(), id)
		return err
	})
	if txErr != nil {
		if _, ok := txErr.(*model.Error); ok {
			return nil, txErr
		}
		return nil, model.WrapError(500, "internal", "更新标签失败", txErr)
	}
	return s.Get(ctx, id)
}

// SoftDelete 软删除标签。不删除账号，关联保留但标签不出现在活动视图。
func (s *Service) SoftDelete(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE tags SET deleted_at = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
		time.Now().Unix(), time.Now().Unix(), id)
	if err != nil {
		return model.WrapError(500, "internal", "删除标签失败", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return model.ErrNotFoundCode("tag_not_found", "标签不存在或已在回收站")
	}
	return nil
}

// Restore 恢复标签，要求其父项目处于活动状态。
func (s *Service) Restore(ctx context.Context, id int64) (*model.Tag, error) {
	txErr := s.db.Tx(ctx, func(tx *sql.Tx) error {
		var projectID int64
		var deleted sql.NullInt64
		err := tx.QueryRowContext(ctx,
			`SELECT project_id, deleted_at FROM tags WHERE id = ?`, id).Scan(&projectID, &deleted)
		if errors.Is(err, sql.ErrNoRows) {
			return model.ErrNotFoundCode("tag_not_found", "标签不存在")
		}
		if err != nil {
			return err
		}
		var projActive int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(1) FROM projects WHERE id = ? AND deleted_at IS NULL`, projectID).Scan(&projActive); err != nil {
			return err
		}
		if projActive == 0 {
			return model.ErrValidation("project_inactive", "请先恢复该标签所属的项目")
		}
		if !deleted.Valid {
			return nil // 幂等：已恢复
		}
		_, err = tx.ExecContext(ctx,
			`UPDATE tags SET deleted_at = NULL, updated_at = ? WHERE id = ?`, time.Now().Unix(), id)
		return err
	})
	if txErr != nil {
		if _, ok := txErr.(*model.Error); ok {
			return nil, txErr
		}
		return nil, model.WrapError(500, "internal", "恢复标签失败", txErr)
	}
	return s.Get(ctx, id)
}

// ListDeleted 返回回收站中的标签（父项目被回收的除外）。
func (s *Service) ListDeleted(ctx context.Context) ([]model.Tag, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+tagColumns+`
        FROM tags t
        JOIN projects p ON p.id = t.project_id
        WHERE t.deleted_at IS NOT NULL AND p.deleted_at IS NULL
        ORDER BY t.deleted_at DESC, t.id`)
	if err != nil {
		return nil, model.WrapError(500, "internal", "查询回收标签失败", err)
	}
	defer rows.Close()
	out := make([]model.Tag, 0)
	for rows.Next() {
		t, err := scanTag(rows)
		if err != nil {
			return nil, model.WrapError(500, "internal", "读取回收标签失败", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Reorder 按给定 ID 顺序重排某项目下的标签。
func (s *Service) Reorder(ctx context.Context, projectID int64, ids []int64) error {
	if len(ids) == 0 {
		return model.ErrValidation("empty_order", "排序列表不能为空")
	}
	txErr := s.db.Tx(ctx, func(tx *sql.Tx) error {
		for i, id := range ids {
			if _, err := tx.ExecContext(ctx, `
                UPDATE tags SET sort_order = ?, updated_at = ?
                WHERE id = ? AND project_id = ? AND deleted_at IS NULL`,
				(i+1)*10, time.Now().Unix(), id, projectID); err != nil {
				return err
			}
		}
		return nil
	})
	if txErr != nil {
		return model.WrapError(500, "internal", "标签排序失败", txErr)
	}
	return nil
}

func normalize(in Input) (name, color string, err error) {
	name = strings.TrimSpace(in.Name)
	if name == "" {
		return "", "", model.ErrValidation("name_required", "标签名不能为空")
	}
	if len([]rune(name)) > NameMaxRunes {
		return "", "", model.ErrValidation("name_too_long", fmt.Sprintf("标签名不能超过 %d 个字符", NameMaxRunes))
	}
	color = strings.TrimSpace(in.Color)
	if color != "" && !colorRe.MatchString(color) {
		return "", "", model.ErrValidation("color_invalid", "颜色必须是 #RRGGBB 格式")
	}
	return name, color, nil
}

func nullIfEmpty(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}
