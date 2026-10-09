// Package search 提供全局元数据搜索。
//
// 搜索只覆盖 email / username / 项目名 / 标签名。
// 加密字段（password / f2a / notes / backup_email / credential_json）
// 不参与搜索，也不为搜索保存明文索引或全库解密。
package search

import (
	"context"
	"strings"

	"passpal/internal/account"
	"passpal/internal/model"
)

// MaxQueryRunes 是搜索词长度上限。
const MaxQueryRunes = 256

// Service 提供搜索能力。
type Service struct{ accounts *account.Service }

// New 构造搜索服务。
func New(accounts *account.Service) *Service { return &Service{accounts: accounts} }

// Search 按关键词分页返回账号元数据。
func (s *Service) Search(ctx context.Context, q, sort string, page model.Page) (model.ListResult[model.Account], error) {
	q = strings.TrimSpace(q)
	if len([]rune(q)) > MaxQueryRunes {
		return model.ListResult[model.Account]{}, model.ErrValidation("query_too_long", "搜索词过长")
	}
	if q == "" {
		return model.ListResult[model.Account]{Items: []model.Account{}}, nil
	}
	return s.accounts.List(ctx, account.Filter{
		Query: q,
		Sort:  sort,
		Page:  page,
	})
}
