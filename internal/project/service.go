// Package project 实现项目 CRUD、排序与软删除/恢复。
package project

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"passpal/internal/database"
	"passpal/internal/model"
)

// NameMaxRunes 是项目名长度上限。
const NameMaxRunes = 128

// Service 提供项目操作。
type Service struct{ db *database.DB }

// New 构造项目服务。
func New(db *database.DB) *Service { return &Service{db: db} }

// Input 是创建/编辑项目的输入。
type Input struct {
	Name        string
	Description string
	Icon        string
}

const projectColumns = `
    p.id, p.name, COALESCE(p.description, ''), COALESCE(p.icon, ''),
    p.sort_order, p.created_at, p.updated_at,
    (SELECT COUNT(*) FROM accounts a WHERE a.project_id = p.id AND a.deleted_at IS NULL),
    (SELECT COUNT(*) FROM tags t WHERE t.project_id = p.id AND t.deleted_at IS NULL)`

func scanProject(row interface{ Scan(...any) error }) (model.Project, error) {
	var p model.Project
	err := row.Scan(&p.ID, &p.Name, &p.Description, &p.Icon, &p.SortOrder,
		&p.CreatedAt, &p.UpdatedAt, &p.AccountCount, &p.TagCount)
	return p, err
}

// List 返回全部活动项目。
func (s *Service) List(ctx context.Context) ([]model.Project, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+projectColumns+`
        FROM projects p WHERE p.deleted_at IS NULL
        ORDER BY p.sort_order, p.id`)
	if err != nil {
		return nil, model.WrapError(500, "internal", "查询项目失败", err)
	}
	defer rows.Close()
	out := make([]model.Project, 0)
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, model.WrapError(500, "internal", "读取项目失败", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Get 返回单个活动项目。
func (s *Service) Get(ctx context.Context, id int64) (*model.Project, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+projectColumns+`
        FROM projects p WHERE p.id = ? AND p.deleted_at IS NULL`, id)
	p, err := scanProject(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFoundCode("project_not_found", "项目不存在")
	}
	if err != nil {
		return nil, model.WrapError(500, "internal", "读取项目失败", err)
	}
	return &p, nil
}

// MustBeActive 校验项目处于活动状态，供账号/标签/导入等跨模块使用。
func (s *Service) MustBeActive(ctx context.Context, q database.Queryer, id int64) error {
	var exists int
	err := q.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM projects WHERE id = ? AND deleted_at IS NULL`, id).Scan(&exists)
	if err != nil {
		return model.WrapError(500, "internal", "校验项目状态失败", err)
	}
	if exists == 0 {
		return model.ErrValidation("project_inactive", "目标项目不存在或已移入回收站")
	}
	return nil
}

// Create 新建项目。
func (s *Service) Create(ctx context.Context, in Input) (*model.Project, error) {
	name, err := normalizeName(in.Name)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	var id int64
	txErr := s.db.Tx(ctx, func(tx *sql.Tx) error {
		var dup int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(1) FROM projects WHERE name = ?`, name).Scan(&dup); err != nil {
			return err
		}
		if dup > 0 {
			// 回收中的同名项目仍占用名称，不静默重用。
			return model.ErrConflictCode("name_taken", "已存在同名项目（可能在回收站中）")
		}
		var maxOrder sql.NullInt64
		if err := tx.QueryRowContext(ctx,
			`SELECT MAX(sort_order) FROM projects WHERE deleted_at IS NULL`).Scan(&maxOrder); err != nil {
			return err
		}
		order := int(maxOrder.Int64) + 10
		res, err := tx.ExecContext(ctx, `
            INSERT INTO projects (name, description, icon, sort_order, created_at, updated_at)
            VALUES (?, ?, ?, ?, ?, ?)`,
			name, nullIfEmpty(in.Description), nullIfEmpty(in.Icon), order, now, now)
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
		return nil, model.WrapError(500, "internal", "创建项目失败", txErr)
	}
	return s.Get(ctx, id)
}

// Update 编辑项目。
func (s *Service) Update(ctx context.Context, id int64, in Input) (*model.Project, error) {
	name, err := normalizeName(in.Name)
	if err != nil {
		return nil, err
	}
	txErr := s.db.Tx(ctx, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(1) FROM projects WHERE id = ? AND deleted_at IS NULL`, id).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return model.ErrNotFoundCode("project_not_found", "项目不存在")
		}
		var dup int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(1) FROM projects WHERE name = ? AND id <> ?`, name, id).Scan(&dup); err != nil {
			return err
		}
		if dup > 0 {
			return model.ErrConflictCode("name_taken", "已存在同名项目")
		}
		_, err := tx.ExecContext(ctx, `
            UPDATE projects SET name = ?, description = ?, icon = ?, updated_at = ?
            WHERE id = ?`,
			name, nullIfEmpty(in.Description), nullIfEmpty(in.Icon), time.Now().Unix(), id)
		return err
	})
	if txErr != nil {
		if _, ok := txErr.(*model.Error); ok {
			return nil, txErr
		}
		return nil, model.WrapError(500, "internal", "更新项目失败", txErr)
	}
	return s.Get(ctx, id)
}

// DeleteResult 描述一次项目软删除的影响范围。
type DeleteResult struct {
	ProjectID    int64 `json:"project_id"`
	AccountCount int   `json:"account_count"`
	TagCount     int   `json:"tag_count"`
}

// SoftDelete 只设置项目自身的 deleted_at。
// 标签/账号因父项目被回收而不可见，不批量改写子项的独立删除标记。
func (s *Service) SoftDelete(ctx context.Context, id int64) (*DeleteResult, error) {
	res := &DeleteResult{ProjectID: id}
	txErr := s.db.Tx(ctx, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(1) FROM projects WHERE id = ? AND deleted_at IS NULL`, id).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return model.ErrNotFoundCode("project_not_found", "项目不存在或已在回收站")
		}
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM accounts WHERE project_id = ? AND deleted_at IS NULL`, id).Scan(&res.AccountCount); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM tags WHERE project_id = ? AND deleted_at IS NULL`, id).Scan(&res.TagCount); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`UPDATE projects SET deleted_at = ?, updated_at = ? WHERE id = ?`,
			time.Now().Unix(), time.Now().Unix(), id)
		return err
	})
	if txErr != nil {
		if _, ok := txErr.(*model.Error); ok {
			return nil, txErr
		}
		return nil, model.WrapError(500, "internal", "删除项目失败", txErr)
	}
	return res, nil
}

// Restore 恢复项目。重复恢复幂等返回当前结果。
// 只重新显示原本未独立删除的子项，不复活此前单独回收的账号。
func (s *Service) Restore(ctx context.Context, id int64) (*model.Project, error) {
	txErr := s.db.Tx(ctx, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM projects WHERE id = ?`, id).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return model.ErrNotFoundCode("project_not_found", "项目不存在")
		}
		_, err := tx.ExecContext(ctx,
			`UPDATE projects SET deleted_at = NULL, updated_at = ? WHERE id = ? AND deleted_at IS NOT NULL`,
			time.Now().Unix(), id)
		return err
	})
	if txErr != nil {
		if _, ok := txErr.(*model.Error); ok {
			return nil, txErr
		}
		return nil, model.WrapError(500, "internal", "恢复项目失败", txErr)
	}
	return s.Get(ctx, id)
}

// ListDeleted 返回回收站中的项目。
func (s *Service) ListDeleted(ctx context.Context) ([]model.Project, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+projectColumns+`
        FROM projects p WHERE p.deleted_at IS NOT NULL
        ORDER BY p.deleted_at DESC, p.id`)
	if err != nil {
		return nil, model.WrapError(500, "internal", "查询回收项目失败", err)
	}
	defer rows.Close()
	out := make([]model.Project, 0)
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, model.WrapError(500, "internal", "读取回收项目失败", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Reorder 按给定 ID 顺序重排项目。
func (s *Service) Reorder(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return model.ErrValidation("empty_order", "排序列表不能为空")
	}
	txErr := s.db.Tx(ctx, func(tx *sql.Tx) error {
		for i, id := range ids {
			if _, err := tx.ExecContext(ctx,
				`UPDATE projects SET sort_order = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
				(i+1)*10, time.Now().Unix(), id); err != nil {
				return err
			}
		}
		return nil
	})
	if txErr != nil {
		return model.WrapError(500, "internal", "项目排序失败", txErr)
	}
	return nil
}

func normalizeName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", model.ErrValidation("name_required", "项目名不能为空")
	}
	if len([]rune(name)) > NameMaxRunes {
		return "", model.ErrValidation("name_too_long", fmt.Sprintf("项目名不能超过 %d 个字符", NameMaxRunes))
	}
	return name, nil
}

func nullIfEmpty(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}
