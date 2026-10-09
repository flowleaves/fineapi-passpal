// Package httpapi 提供 HTTP 路由、中间件与处理器。
package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"passpal/internal/model"
)

type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// writeData 写出成功响应。
func writeData(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if data == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(map[string]any{"data": data}); err != nil {
		slog.Error("写出响应失败", "err", err)
	}
}

// writeError 把错误映射成统一结构。
//
// 只暴露固定的 code 与面向用户的 message，不回显底层错误细节，
// 避免把可能含敏感内容的解析错误带到响应里。
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message, retryAfter := classify(err)
	if retryAfter > 0 {
		w.Header().Set("Retry-After", itoa(retryAfter))
	}
	if status >= 500 {
		slog.Error("请求处理失败",
			"method", r.Method, "path", r.URL.Path, "code", code, "err", err)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)

	var body errorBody
	body.Error.Code = code
	body.Error.Message = message
	_ = json.NewEncoder(w).Encode(body)
}

func classify(err error) (status int, code, message string, retryAfter int) {
	var me *model.Error
	if errors.As(err, &me) {
		msg := me.Message
		if msg == "" {
			msg = http.StatusText(me.Status)
		}
		return me.Status, me.Code, msg, me.RetryAfter
	}
	switch {
	case errors.Is(err, model.ErrNotFound):
		return 404, "not_found", "资源不存在", 0
	case errors.Is(err, model.ErrConflict):
		return 409, "conflict", "资源已被修改", 0
	case errors.Is(err, model.ErrInvalid):
		return 422, "validation_failed", "输入校验失败", 0
	case errors.Is(err, model.ErrForbidden):
		return 403, "forbidden", "操作不被允许", 0
	case errors.Is(err, model.ErrGone):
		return 410, "gone", "资源已失效", 0
	case errors.Is(err, model.ErrTooLarge):
		return 413, "too_large", "请求过大", 0
	case errors.Is(err, model.ErrBusy):
		return 503, "busy", "服务繁忙，请稍后再试", 0
	}
	return 500, "internal", "服务器内部错误", 0
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
