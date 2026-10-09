package httpapi

import (
	"io/fs"
	"net/http"
	"strings"

	"passpal/internal/account"
	"passpal/internal/auth"
	"passpal/internal/backup"
	"passpal/internal/config"
	"passpal/internal/cryptoutil"
	"passpal/internal/database"
	"passpal/internal/export"
	"passpal/internal/importer"
	"passpal/internal/project"
	"passpal/internal/search"
	"passpal/internal/tag"
	"passpal/internal/tasklock"
)

// Options 汇总 Server 的依赖。
type Options struct {
	Config    *config.Config
	DB        *database.DB
	Cipher    *cryptoutil.Cipher
	Auth      *auth.Service
	Projects  *project.Service
	Tags      *tag.Service
	Accounts  *account.Service
	Imports   *importer.Store
	Deduper   *importer.Deduper
	Committer *importer.Committer
	Search    *search.Service
	Backups   *backup.Service
	Exports   *export.Service
	TaskLock  *tasklock.Lock
	Static    fs.FS
}

// Server 持有全部依赖并暴露 HTTP 处理器。
type Server struct {
	cfg       *config.Config
	db        *database.DB
	cipher    *cryptoutil.Cipher
	auth      *auth.Service
	projects  *project.Service
	tags      *tag.Service
	accounts  *account.Service
	imports   *importer.Store
	deduper   *importer.Deduper
	committer *importer.Committer
	search    *search.Service
	backups   *backup.Service
	exports   *export.Service
	taskLock  *tasklock.Lock
	static    fs.FS
}

// New 构造 Server。
func New(opt Options) *Server {
	return &Server{
		cfg:       opt.Config,
		db:        opt.DB,
		cipher:    opt.Cipher,
		auth:      opt.Auth,
		projects:  opt.Projects,
		tags:      opt.Tags,
		accounts:  opt.Accounts,
		imports:   opt.Imports,
		deduper:   opt.Deduper,
		committer: opt.Committer,
		search:    opt.Search,
		backups:   opt.Backups,
		exports:   opt.Exports,
		taskLock:  opt.TaskLock,
		static:    opt.Static,
	}
}

// Handler 返回装配好中间件的根处理器。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// ── 公开端点 ───────────────────────────────────────────
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	mux.HandleFunc("GET /api/auth/session", s.handleSession)

	// ── 需要登录 ───────────────────────────────────────────
	mux.Handle("POST /api/auth/logout", s.protected(http.HandlerFunc(s.handleLogout)))

	mux.Handle("GET /api/projects", s.protected(http.HandlerFunc(s.handleListProjects)))
	mux.Handle("POST /api/projects", s.protected(http.HandlerFunc(s.handleCreateProject)))
	mux.Handle("PUT /api/projects/{id}", s.protected(http.HandlerFunc(s.handleUpdateProject)))
	mux.Handle("DELETE /api/projects/{id}", s.protected(http.HandlerFunc(s.handleDeleteProject)))
	mux.Handle("POST /api/projects/reorder", s.protected(http.HandlerFunc(s.handleReorderProjects)))
	mux.Handle("POST /api/projects/{id}/restore", s.protected(http.HandlerFunc(s.handleRestoreProject)))

	mux.Handle("GET /api/projects/{id}/tags", s.protected(http.HandlerFunc(s.handleListTags)))
	mux.Handle("POST /api/projects/{id}/tags", s.protected(http.HandlerFunc(s.handleCreateTag)))
	mux.Handle("PUT /api/tags/{id}", s.protected(http.HandlerFunc(s.handleUpdateTag)))
	mux.Handle("DELETE /api/tags/{id}", s.protected(http.HandlerFunc(s.handleDeleteTag)))
	mux.Handle("POST /api/tags/reorder", s.protected(http.HandlerFunc(s.handleReorderTags)))
	mux.Handle("POST /api/tags/{id}/restore", s.protected(http.HandlerFunc(s.handleRestoreTag)))

	mux.Handle("GET /api/accounts", s.protected(http.HandlerFunc(s.handleListAccounts)))
	mux.Handle("POST /api/accounts", s.protected(http.HandlerFunc(s.handleCreateAccount)))
	mux.Handle("GET /api/accounts/{id}", s.protected(http.HandlerFunc(s.handleGetAccount)))
	mux.Handle("PATCH /api/accounts/{id}", s.protected(http.HandlerFunc(s.handleUpdateAccount)))
	mux.Handle("DELETE /api/accounts/{id}", s.protected(http.HandlerFunc(s.handleDeleteAccount)))
	mux.Handle("POST /api/accounts/batch-delete", s.protected(http.HandlerFunc(s.handleBatchDeleteAccounts)))
	mux.Handle("POST /api/accounts/export-refresh-tokens", s.protected(http.HandlerFunc(s.handleExportRefreshTokens)))
	mux.Handle("POST /api/accounts/{id}/restore", s.protected(http.HandlerFunc(s.handleRestoreAccount)))
	mux.Handle("POST /api/accounts/{id}/reveal", s.protected(http.HandlerFunc(s.handleRevealAccountField)))

	mux.Handle("POST /api/import/parse", s.protected(http.HandlerFunc(s.handleImportParse)))
	mux.Handle("GET /api/import/previews/{id}", s.protected(http.HandlerFunc(s.handleImportPreview)))
	mux.Handle("PATCH /api/import/previews/{id}", s.protected(http.HandlerFunc(s.handleImportPreviewPatch)))
	mux.Handle("PATCH /api/import/previews/{id}/rows/{row_id}", s.protected(http.HandlerFunc(s.handleImportRowPatch)))
	mux.Handle("POST /api/import/previews/{id}/rows/{row_id}/reveal", s.protected(http.HandlerFunc(s.handleImportRowReveal)))
	mux.Handle("DELETE /api/import/previews/{id}", s.protected(http.HandlerFunc(s.handleImportCancel)))
	mux.Handle("POST /api/import/commit", s.protected(http.HandlerFunc(s.handleImportCommit)))
	mux.Handle("POST /api/import/result", s.protected(http.HandlerFunc(s.handleImportResult)))

	mux.Handle("GET /api/search", s.protected(http.HandlerFunc(s.handleSearch)))
	mux.Handle("GET /api/stats", s.protected(http.HandlerFunc(s.handleStats)))
	mux.Handle("GET /api/settings", s.protected(http.HandlerFunc(s.handleGetSettings)))
	mux.Handle("PATCH /api/settings", s.protected(http.HandlerFunc(s.handlePatchSettings)))
	mux.Handle("GET /api/trash", s.protected(http.HandlerFunc(s.handleTrash)))

	mux.Handle("POST /api/backup", s.protected(http.HandlerFunc(s.handleBackupCreate)))
	mux.Handle("GET /api/backup/list", s.protected(http.HandlerFunc(s.handleBackupList)))
	mux.Handle("POST /api/backup/{id}/download", s.protected(http.HandlerFunc(s.handleBackupDownload)))

	mux.Handle("POST /api/export", s.protected(http.HandlerFunc(s.handleExport)))

	// ── 静态资源与 SPA 回退 ────────────────────────────────
	mux.Handle("GET /", s.staticHandler())

	// 中间件自外向内：安全头 -> panic 恢复 -> 访问日志。
	var h http.Handler = mux
	h = s.accessLog(h)
	h = s.recoverPanic(h)
	h = s.securityHeaders(h)
	return h
}

// staticHandler 提供内嵌前端资源，并对未知路径回退到 index.html。
func (s *Server) staticHandler() http.Handler {
	if s.static == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "前端资源未构建", http.StatusNotFound)
		})
	}
	fileServer := http.FileServer(http.FS(s.static))
	indexHTML, indexErr := fs.ReadFile(s.static, "index.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		if _, err := fs.Stat(s.static, name); err != nil {
			// SPA 回退：直接把 index.html 写出去。
			// 不能交给 FileServer 处理 /index.html —— 它会 301 重定向到 /。
			if indexErr != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write(indexHTML)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}
