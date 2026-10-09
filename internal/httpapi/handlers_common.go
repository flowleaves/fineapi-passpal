package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"passpal/internal/audit"
	"passpal/internal/model"
)

// nowUnix 返回当前 Unix 秒。
func nowUnix() int64 { return time.Now().Unix() }

// nullableString 区分 JSON 字段的三种状态：缺失、null、有值。
//
// 这是 PATCH 语义的基础：缺失表示不修改，null 表示显式清空。
type nullableString struct {
	Set   bool
	Value *string
}

func (n *nullableString) UnmarshalJSON(b []byte) error {
	n.Set = true
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		n.Value = nil
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	n.Value = &s
	return nil
}

// resolved 返回「已设置」时的目标值：null 与空串都表示清空。
func (n nullableString) resolved() *string {
	v := ""
	if n.Value != nil {
		v = *n.Value
	}
	return &v
}

// writeAudit 写一条不含敏感内容的审计记录。
func (s *Server) writeAudit(r *http.Request, action, targetType string, targetID *int64) {
	_ = audit.Write(r.Context(), s.db, audit.Entry{
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		IP:         clientIPFrom(r.Context()),
	})
}

// writeAuditField 记录取密动作：只写字段枚举，不写字段值。
func (s *Server) writeAuditField(r *http.Request, action, targetType string, targetID *int64, field string) {
	_ = audit.Write(r.Context(), s.db, audit.Entry{
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		Field:      field,
		IP:         clientIPFrom(r.Context()),
	})
}

// 请求体上限。
const (
	maxLoginBody    = 4 << 10 // 4 KiB
	maxJSONBody     = 1 << 20 // 1 MiB（普通写接口）
	maxImportBody   = 8 << 20 // 8 MiB（导入原文）
	maxPasswordBody = 4 << 10 // 4 KiB（重新认证）
)

// decodeJSON 解析 JSON 请求体并限制大小。
func decodeJSON(w http.ResponseWriter, r *http.Request, limit int64, dst any) error {
	ct := strings.ToLower(r.Header.Get("Content-Type"))
	if i := strings.Index(ct, ";"); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	if ct != "application/json" {
		return model.ErrValidation("content_type_invalid", "请求必须是 application/json")
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		switch {
		case errors.As(err, &maxErr):
			return model.ErrTooLargeCode("body_too_large", "请求体过大")
		case errors.Is(err, io.EOF):
			return model.ErrValidation("empty_body", "请求体为空")
		default:
			return model.ErrValidation("invalid_json", "请求体不是合法 JSON")
		}
	}
	return nil
}

// pathID 读取路径中的正整数参数。
func pathID(r *http.Request, name string) (int64, error) {
	raw := r.PathValue(name)
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v <= 0 {
		return 0, model.ErrValidation("invalid_id", "路径参数非法")
	}
	return v, nil
}

// queryInt 读取整型查询参数，缺失或非法时返回默认值。
func queryInt(r *http.Request, name string, def int) int {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return v
}

// queryInt64 读取 int64 查询参数。
func queryInt64(r *http.Request, name string) int64 {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return 0
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 0 {
		return 0
	}
	return v
}

// pageFromQuery 解析统一分页参数（服务端限额）。
func pageFromQuery(r *http.Request, defSize, maxSize int) model.Page {
	return model.NormalizePage(
		queryInt(r, "page", 1),
		queryInt(r, "page_size", defSize),
		defSize, maxSize,
	)
}
