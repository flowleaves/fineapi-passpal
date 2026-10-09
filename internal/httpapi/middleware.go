package httpapi

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"passpal/internal/auth"
	"passpal/internal/model"
)

type ctxKey int

const (
	ctxSessionToken ctxKey = iota
	ctxSession
	ctxClientIP
)

// contentSecurityPolicy 不允许内联脚本：主题初始化放在独立的同步脚本文件里。
const contentSecurityPolicy = "default-src 'self'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; " +
	"font-src 'self'; " +
	"connect-src 'self'; " +
	"object-src 'none'; " +
	"base-uri 'none'; " +
	"form-action 'self'; " +
	"frame-ancestors 'none'"

// securityHeaders 统一安全响应头与 no-store。
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		// 所有 API 与页面默认不缓存：秘密、草稿、会话与搜索结果都不应被缓存。
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// recoverPanic 捕获 panic，避免进程退出。
func (s *Server) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("请求处理 panic", "path", r.URL.Path, "panic", rec)
				writeError(w, r, model.NewError(500, "internal", "服务器内部错误"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// accessLog 只记录方法、路径（不含查询串）、状态与耗时。
func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		slog.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"dur_ms", time.Since(start).Milliseconds(),
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wrote {
		r.status = code
		r.wrote = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wrote {
		r.wrote = true
	}
	return r.ResponseWriter.Write(b)
}

// Unwrap 让 http.ResponseWriter 的扩展接口（如 Flush）透传。
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// protected 同时完成会话校验、CSRF 校验与同源校验。
func (s *Server) protected(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := cookieValue(r, auth.CookieSession)
		sess, err := s.auth.Authenticate(r.Context(), token)
		if err != nil {
			clearSessionCookies(w, r, s.cfg.CookieSecure())
			writeError(w, r, err)
			return
		}

		if isWriteMethod(r.Method) {
			header := r.Header.Get(auth.HeaderCSRF)
			cookie := cookieValue(r, auth.CookieCSRF)
			if !s.auth.VerifyCSRF(token, header, cookie) {
				writeError(w, r, model.ErrForbiddenCode("csrf_failed", "CSRF 校验失败，请刷新页面重试"))
				return
			}
			if !s.sameOrigin(r) {
				writeError(w, r, model.ErrForbiddenCode("origin_mismatch", "请求来源不被信任"))
				return
			}
		}

		ctx := context.WithValue(r.Context(), ctxSessionToken, token)
		ctx = context.WithValue(ctx, ctxSession, sess)
		ctx = context.WithValue(ctx, ctxClientIP, s.clientIP(r))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// sameOrigin 校验 Origin 与配置一致。
//
// 生产环境要求完全一致（字符串比对）。
// 开发环境额外允许 localhost / 127.0.0.1 / [::1] 三者互相等价 ——
// 本地调试时这两个写法混用是常态，但 scheme 与端口仍必须一致。
func (s *Server) sameOrigin(r *http.Request) bool {
	origin := strings.TrimRight(strings.TrimSpace(r.Header.Get("Origin")), "/")
	if origin == "" {
		return false
	}
	if strings.EqualFold(origin, s.cfg.AppOrigin) {
		return true
	}
	if s.cfg.IsProduction() {
		return false
	}
	return sameLoopbackOrigin(origin, s.cfg.AppOrigin)
}

// sameLoopbackOrigin 判断两个地址是否为等价的回环地址。
func sameLoopbackOrigin(a, b string) bool {
	ua, errA := url.Parse(a)
	ub, errB := url.Parse(b)
	if errA != nil || errB != nil {
		return false
	}
	if ua.Scheme != ub.Scheme || ua.Port() != ub.Port() {
		return false
	}
	return isLoopbackHost(ua.Hostname()) && isLoopbackHost(ub.Hostname())
}

func isLoopbackHost(host string) bool {
	switch strings.ToLower(strings.Trim(host, "[]")) {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

func isWriteMethod(m string) bool {
	switch m {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// clientIP 返回已验证的客户端地址。
//
// 仅当 socket 对端属于 TRUSTED_PROXY_CIDRS 时才读取代理提供的转发头，
// 否则直接使用 socket 地址，不接受任意 X-Forwarded-For。
func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	socketIP := net.ParseIP(strings.Trim(host, "[]"))
	if socketIP == nil {
		return host
	}
	if !s.isTrustedProxy(socketIP) {
		return socketIP.String()
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			cand := net.ParseIP(strings.TrimSpace(parts[i]))
			if cand == nil {
				continue
			}
			if !s.isTrustedProxy(cand) {
				return cand.String()
			}
		}
	}
	if xr := r.Header.Get("X-Real-IP"); xr != "" {
		if cand := net.ParseIP(strings.TrimSpace(xr)); cand != nil {
			return cand.String()
		}
	}
	return socketIP.String()
}

func (s *Server) isTrustedProxy(ip net.IP) bool {
	for _, n := range s.cfg.TrustedProxyCIDRs {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func cookieValue(r *http.Request, name string) string {
	c, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return c.Value
}

// setSessionCookies 写入会话与 CSRF cookie。
func setSessionCookies(w http.ResponseWriter, sessionToken, csrfToken string, maxAge int, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.CookieSession,
		Value:    sessionToken,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     auth.CookieCSRF,
		Value:    csrfToken,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: false, // 前端需要读取它做双提交
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// clearSessionCookies 清除会话与 CSRF cookie。
func clearSessionCookies(w http.ResponseWriter, r *http.Request, secure bool) {
	for _, name := range []string{auth.CookieSession, auth.CookieCSRF} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: name == auth.CookieSession,
			Secure:   secure,
			SameSite: http.SameSiteLaxMode,
		})
	}
}

func sessionFrom(ctx context.Context) *auth.Session {
	sess, _ := ctx.Value(ctxSession).(*auth.Session)
	return sess
}

func sessionTokenFrom(ctx context.Context) string {
	token, _ := ctx.Value(ctxSessionToken).(string)
	return token
}

func clientIPFrom(ctx context.Context) string {
	ip, _ := ctx.Value(ctxClientIP).(string)
	return ip
}
