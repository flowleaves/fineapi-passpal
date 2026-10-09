package httpapi

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"passpal/internal/audit"
	"passpal/internal/database"
	"passpal/internal/export"
	"passpal/internal/model"
)

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	res, err := s.search.Search(r.Context(), q.Get("q"), q.Get("sort"),
		pageFromQuery(r, accountDefaultPageSize, accountMaxPageSize))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, res)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var out struct {
		Projects   int `json:"projects"`
		Tags       int `json:"tags"`
		Accounts   int `json:"accounts"`
		AddedToday int `json:"added_today"`
	}
	queries := []struct {
		dst   *int
		query string
		args  []any
	}{
		{&out.Projects, `SELECT COUNT(*) FROM projects WHERE deleted_at IS NULL`, nil},
		{&out.Tags, `SELECT COUNT(*) FROM tags t JOIN projects p ON p.id = t.project_id
                     WHERE t.deleted_at IS NULL AND p.deleted_at IS NULL`, nil},
		{&out.Accounts, `SELECT COUNT(*) FROM accounts a JOIN projects p ON p.id = a.project_id
                         WHERE a.deleted_at IS NULL AND p.deleted_at IS NULL`, nil},
		{&out.AddedToday, `SELECT COUNT(*) FROM accounts a JOIN projects p ON p.id = a.project_id
                           WHERE a.deleted_at IS NULL AND p.deleted_at IS NULL AND a.created_at >= ?`,
			[]any{startOfTodayUnix()}},
	}
	for _, q := range queries {
		if err := s.db.QueryRowContext(ctx, q.query, q.args...).Scan(q.dst); err != nil {
			writeError(w, r, model.WrapError(500, "internal", "统计数据失败", err))
			return
		}
	}
	writeData(w, http.StatusOK, out)
}

func startOfTodayUnix() int64 {
	n := time.Now()
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, n.Location()).Unix()
}

// hiddenSettings 是不对外暴露的键。
var hiddenSettings = map[string]bool{"admin_hash_fingerprint": true, "schema_seeded": true}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	all, err := database.AllSettings(r.Context(), s.db)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := make(map[string]string, len(all))
	for k, v := range all {
		if hiddenSettings[k] {
			continue
		}
		out[k] = v
	}
	// 密钥状态只报告「是否就绪」与版本列表，不返回值。
	writeData(w, http.StatusOK, map[string]any{
		"settings": out,
		"encryption": map[string]any{
			"current_key_version": s.cfg.CurrentKeyVersion,
			"available_versions":  s.cipherVersions(),
		},
	})
}

type settingsPatchBody struct {
	SessionTTLHours  *int `json:"session_ttl_hours"`
	LoginMaxAttempts *int `json:"login_max_attempts"`
	LoginLockMinutes *int `json:"login_lock_minutes"`
	BackupRetainDays *int `json:"backup_retain_days"`
}

func (s *Server) handlePatchSettings(w http.ResponseWriter, r *http.Request) {
	var body settingsPatchBody
	if err := decodeJSON(w, r, maxJSONBody, &body); err != nil {
		writeError(w, r, err)
		return
	}
	type item struct {
		key   string
		value *int
		min   int
		max   int
	}
	items := []item{
		{"session_ttl_hours", body.SessionTTLHours, 1, 24 * 365},
		{"login_max_attempts", body.LoginMaxAttempts, 1, 100},
		{"login_lock_minutes", body.LoginLockMinutes, 1, 24 * 60},
		{"backup_retain_days", body.BackupRetainDays, 1, 3650},
	}
	changed := false
	for _, it := range items {
		if it.value == nil {
			continue
		}
		if *it.value < it.min || *it.value > it.max {
			writeError(w, r, model.ErrValidation("value_out_of_range",
				fmt.Sprintf("%s 必须在 %d..%d 之间", it.key, it.min, it.max)))
			return
		}
		if err := database.SetSetting(r.Context(), s.db, it.key,
			strconv.Itoa(*it.value), "settings_api"); err != nil {
			writeError(w, r, err)
			return
		}
		changed = true
	}
	if !changed {
		writeError(w, r, model.ErrValidation("nothing_to_update", "没有需要更新的配置"))
		return
	}
	// 让运行期组件重新读取库值。
	if err := s.auth.Reload(r.Context()); err != nil {
		writeError(w, r, err)
		return
	}
	if body.BackupRetainDays != nil {
		s.backups.SetRetain(time.Duration(*body.BackupRetainDays) * 24 * time.Hour)
	}
	s.writeAudit(r, audit.ActionUpdateSettings, audit.TargetSystem, nil)

	all, err := database.AllSettings(r.Context(), s.db)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := make(map[string]string, len(all))
	for k, v := range all {
		if hiddenSettings[k] {
			continue
		}
		out[k] = v
	}
	writeData(w, http.StatusOK, map[string]any{"settings": out})
}

func (s *Server) cipherVersions() []int {
	if s.cipher == nil {
		return nil
	}
	return s.cipher.Versions()
}

func (s *Server) handleTrash(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kind := r.URL.Query().Get("type")
	page := pageFromQuery(r, 100, 100)

	switch kind {
	case "", "project":
		items, err := s.projects.ListDeleted(ctx)
		if err != nil {
			writeError(w, r, err)
			return
		}
		if kind == "project" {
			writeData(w, http.StatusOK, map[string]any{"items": items})
			return
		}
	case "tag":
		items, err := s.tags.ListDeleted(ctx)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeData(w, http.StatusOK, map[string]any{"items": items})
		return
	case "account":
		res, err := s.accounts.ListDeleted(ctx, page)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeData(w, http.StatusOK, res)
		return
	default:
		writeError(w, r, model.ErrValidation("invalid_type", "type 必须是 project / tag / account"))
		return
	}

	// 默认返回三类合并视图。
	projects, err := s.projects.ListDeleted(ctx)
	if err != nil {
		writeError(w, r, err)
		return
	}
	tags, err := s.tags.ListDeleted(ctx)
	if err != nil {
		writeError(w, r, err)
		return
	}
	accounts, err := s.accounts.ListDeleted(ctx, page)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{
		"projects": projects,
		"tags":     tags,
		"accounts": accounts,
	})
}

func (s *Server) handleBackupCreate(w http.ResponseWriter, r *http.Request) {
	if !s.taskLock.Try() {
		writeError(w, r, model.ErrServiceBusy("task_busy", "正在处理其他导入或备份任务，请稍后再试"))
		return
	}
	defer s.taskLock.Release()

	info, err := s.backups.Run(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeData(w, http.StatusCreated, info)
}

func (s *Server) handleBackupList(w http.ResponseWriter, r *http.Request) {
	items, err := s.backups.List()
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"items": items})
}

type passwordBody struct {
	AdminPassword string `json:"admin_password"`
}

// handleBackupDownload 需要重新认证管理员密码。
func (s *Server) handleBackupDownload(w http.ResponseWriter, r *http.Request) {
	var body passwordBody
	if err := decodeJSON(w, r, maxPasswordBody, &body); err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.auth.VerifyAdminPassword(r.Context(), body.AdminPassword); err != nil {
		writeError(w, r, err)
		return
	}
	path, err := s.backups.Path(r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition",
		`attachment; filename="`+filepath.Base(path)+`"`)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, path)
}

type exportBody struct {
	Scope         string `json:"scope"`
	ScopeID       int64  `json:"scope_id"`
	Format        string `json:"format"`
	AdminPassword string `json:"admin_password"`
}

// handleExport 流式生成导出文件，不在服务器落明文临时文件。
func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	var body exportBody
	if err := decodeJSON(w, r, maxPasswordBody, &body); err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.auth.VerifyAdminPassword(r.Context(), body.AdminPassword); err != nil {
		writeError(w, r, err)
		return
	}
	format := strings.ToLower(strings.TrimSpace(body.Format))
	if format == "" {
		format = export.FormatJSON
	}
	scope := export.Scope{Type: body.Scope, ID: body.ScopeID}

	stamp := time.Now().UTC().Format("20060102-150405")
	ext := "json"
	contentType := "application/json; charset=utf-8"
	if format == export.FormatCSV {
		ext = "csv"
		contentType = "text/csv; charset=utf-8"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="passpal-export-%s.%s"`, stamp, ext))
	w.Header().Set("Cache-Control", "no-store")

	// 响应体已经开始流式写出，之后发生的错误只能记录日志。
	if _, err := s.exports.Write(r.Context(), scope, format, w); err != nil {
		s.writeAudit(r, audit.ActionExport, audit.TargetSystem, nil)
		return
	}
	s.writeAudit(r, audit.ActionExport, audit.TargetSystem, nil)
}
