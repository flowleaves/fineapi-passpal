// Package model 定义跨层共享的领域类型与错误。
package model

import (
	"errors"
	"fmt"
)

// 领域错误。HTTP 层据此映射状态码。
var (
	ErrNotFound  = errors.New("资源不存在")
	ErrConflict  = errors.New("资源已被修改")
	ErrInvalid   = errors.New("输入校验失败")
	ErrForbidden = errors.New("操作不被允许")
	ErrBusy      = errors.New("服务繁忙")
	ErrGone      = errors.New("草稿已失效")
	ErrTooLarge  = errors.New("请求过大")
)

// Error 携带稳定的机器可读错误码，便于前端做分支处理。
type Error struct {
	Status     int
	Code       string
	Message    string
	RetryAfter int // 秒；仅 429 / 503 有意义，由 HTTP 层写入 Retry-After 头
	Err        error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s (%v)", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Err }

// NewError 构造带状态码与错误码的错误。
func NewError(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

// WrapError 在保留状态码的前提下包装底层错误。
func WrapError(status int, code, message string, err error) *Error {
	return &Error{Status: status, Code: code, Message: message, Err: err}
}

// 常用错误的快捷构造。

func ErrBadRequest(code, msg string) *Error    { return NewError(400, code, msg) }
func ErrUnauthorized(code, msg string) *Error  { return NewError(401, code, msg) }
func ErrForbiddenCode(code, msg string) *Error { return NewError(403, code, msg) }
func ErrNotFoundCode(code, msg string) *Error  { return NewError(404, code, msg) }
func ErrConflictCode(code, msg string) *Error  { return NewError(409, code, msg) }
func ErrGoneCode(code, msg string) *Error      { return NewError(410, code, msg) }
func ErrTooLargeCode(code, msg string) *Error  { return NewError(413, code, msg) }
func ErrValidation(code, msg string) *Error    { return NewError(422, code, msg) }
func ErrRateLimited(code, msg string) *Error   { return NewError(429, code, msg) }
func ErrServiceBusy(code, msg string) *Error   { return NewError(503, code, msg) }

// ErrRateLimitedRetry 构造带 Retry-After 的 429。
func ErrRateLimitedRetry(code, msg string, retryAfterSeconds int) *Error {
	if retryAfterSeconds < 1 {
		retryAfterSeconds = 1
	}
	return &Error{Status: 429, Code: code, Message: msg, RetryAfter: retryAfterSeconds}
}

// 账号状态枚举。
//
// 计划文档把完整的状态管理列为第二阶段；这里先落地「正常 / 异常」，
// 枚举本身保留扩展空间（失效 / 封禁 / 待验证 / 未知），前端只暴露这两个。
const (
	StatusNormal   = "normal"
	StatusAbnormal = "abnormal"
)

// ValidStatuses 列出允许的账号状态。
var ValidStatuses = []string{StatusNormal, StatusAbnormal}

// IsValidStatus 判断状态是否在允许集合内。
func IsValidStatus(s string) bool {
	for _, v := range ValidStatuses {
		if v == s {
			return true
		}
	}
	return false
}

// Project 是项目。
type Project struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Icon         string `json:"icon"`
	SortOrder    int    `json:"sort_order"`
	AccountCount int    `json:"account_count"`
	TagCount     int    `json:"tag_count"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
}

// Tag 是标签，必须属于某个项目。
type Tag struct {
	ID           int64  `json:"id"`
	ProjectID    int64  `json:"project_id"`
	Name         string `json:"name"`
	Color        string `json:"color"`
	SortOrder    int    `json:"sort_order"`
	AccountCount int    `json:"account_count"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
}

// Account 是账号的非敏感视图。
//
// 任何 *_encrypted 字段都不出现在这里；只暴露 has_* 存在标记，
// 明文必须经 reveal 接口逐字段获取。
type Account struct {
	ID              int64   `json:"id"`
	ProjectID       int64   `json:"project_id"`
	Email           string  `json:"email"`
	Username        string  `json:"username"`
	Status          string  `json:"status"`
	ExtraJSON       string  `json:"extra_json"`
	Revision        int64   `json:"revision"`
	Tags            []Tag   `json:"tags"`
	TagIDs          []int64 `json:"tag_ids"`
	HasPassword     bool    `json:"has_password"`
	HasBackup       bool    `json:"has_backup_email"`
	HasF2A          bool    `json:"has_f2a"`
	HasJSON         bool    `json:"has_json"`
	HasNotes        bool    `json:"has_notes"`
	HasRefreshToken bool    `json:"has_refresh_token"`
	HasSMSLink      bool    `json:"has_sms_link"`
	// 以下三项按使用便利性要求随列表一并下发明文（密码与 JSON 不下发）。
	F2A          string `json:"f2a,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	SMSLink      string `json:"sms_link,omitempty"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
	LastUsedAt   *int64 `json:"last_used_at"`
}

// AccountSecrets 是账号的明文秘密，仅在明确需要时短暂存在于内存中。
type AccountSecrets struct {
	Password       *string
	BackupEmail    *string
	F2A            *string
	CredentialJSON *string
	Notes          *string
	RefreshToken   *string
	SMSLink        *string
}

// AccountInput 是创建/编辑账号的统一输入。
//
// 指针语义（doc §11.8）：
//   - nil        表示不修改（PATCH）或缺失（创建）
//   - 指向空串   表示显式清空
type AccountInput struct {
	Email          *string  `json:"email"`
	Username       *string  `json:"username"`
	Password       *string  `json:"password"`
	BackupEmail    *string  `json:"backup_email"`
	F2A            *string  `json:"f2a"`
	CredentialJSON *string  `json:"credential_json"`
	Notes          *string  `json:"notes"`
	RefreshToken   *string  `json:"refresh_token"`
	SMSLink        *string  `json:"sms_link"`
	Status         *string  `json:"status"`
	Extra          *Extra   `json:"extra"`
	TagIDs         *[]int64 `json:"tag_ids"`
}

// ImportSummary 是一次导入提交的计数结果。
type ImportSummary struct {
	Total      int `json:"total"`
	Created    int `json:"created"`
	Updated    int `json:"updated"`
	Skipped    int `json:"skipped"`
	Invalid    int `json:"invalid"`
	Duplicates int `json:"duplicates"`
}

// ListResult 是统一的分页响应体。
type ListResult[T any] struct {
	Items    []T `json:"items"`
	Page     int `json:"page"`
	PageSize int `json:"page_size"`
	Total    int `json:"total"`
}

// Page 描述分页参数。
type Page struct {
	Page     int
	PageSize int
}

// Offset 返回 SQL OFFSET。
func (p Page) Offset() int {
	if p.Page < 1 {
		return 0
	}
	return (p.Page - 1) * p.PageSize
}

// NormalizePage 把分页参数限制在服务端允许范围内。
func NormalizePage(page, pageSize, defSize, maxSize int) Page {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = defSize
	}
	if pageSize > maxSize {
		pageSize = maxSize
	}
	return Page{Page: page, PageSize: pageSize}
}
