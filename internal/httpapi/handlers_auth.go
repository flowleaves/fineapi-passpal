package httpapi

import (
	"net/http"

	"passpal/internal/audit"
	"passpal/internal/auth"
	"passpal/internal/model"
)

// handleHealth 只返回就绪状态，不暴露任何配置。
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeData(w, http.StatusOK, map[string]any{"status": "ok"})
}

type loginRequest struct {
	Password string `json:"password"`
}

// handleLogin 是 CSRF token 引导例外：只接受 JSON，且必须带匹配的 Origin。
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.sameOrigin(r) {
		writeError(w, r, model.ErrForbiddenCode("origin_mismatch", "请求来源不被信任"))
		return
	}
	var req loginRequest
	if err := decodeJSON(w, r, maxLoginBody, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if req.Password == "" {
		writeError(w, r, model.ErrValidation("password_required", "请输入管理员密码"))
		return
	}

	ip := s.clientIP(r)
	result, err := s.auth.Login(r.Context(), req.Password, ip, r.UserAgent())
	if err != nil {
		writeError(w, r, err)
		return
	}

	maxAge := int(result.ExpiresAt - nowUnix())
	setSessionCookies(w, result.SessionToken, result.CSRFToken, maxAge, s.cfg.IsProduction())
	writeData(w, http.StatusOK, map[string]any{
		"expires_at": result.ExpiresAt,
		"csrf_token": result.CSRFToken,
	})
}

// handleLogout 删除会话并释放该会话的导入草稿。
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	token := sessionTokenFrom(r.Context())
	if err := s.auth.DeleteSession(r.Context(), token); err != nil {
		writeError(w, r, err)
		return
	}
	if sess := sessionFrom(r.Context()); sess != nil {
		s.imports.DeleteBySession(sess.ID)
	}
	_ = audit.Write(r.Context(), s.db, audit.Entry{
		Action:     audit.ActionLogout,
		TargetType: audit.TargetSystem,
		IP:         clientIPFrom(r.Context()),
	})
	clearSessionCookies(w, r, s.cfg.IsProduction())
	writeData(w, http.StatusOK, map[string]any{"ok": true})
}

// handleSession 可匿名调用，只返回是否登录。
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	token := cookieValue(r, auth.CookieSession)
	sess, err := s.auth.Authenticate(r.Context(), token)
	if err != nil || sess == nil {
		clearSessionCookies(w, r, s.cfg.IsProduction())
		writeData(w, http.StatusOK, map[string]any{"authenticated": false})
		return
	}
	// 有效会话时补发 CSRF cookie，覆盖浏览器清掉 cookie 的情况。
	maxAge := int(sess.ExpiresAt - nowUnix())
	setSessionCookies(w, token, s.auth.CSRFFor(token), maxAge, s.cfg.IsProduction())
	writeData(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"expires_at":    sess.ExpiresAt,
		"csrf_token":    s.auth.CSRFFor(token),
	})
}
