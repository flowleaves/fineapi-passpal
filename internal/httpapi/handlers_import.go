package httpapi

import (
	"net/http"

	"passpal/internal/audit"
	"passpal/internal/importer"
	"passpal/internal/model"
)

type importParseBody struct {
	ProjectID int64   `json:"project_id"`
	TagIDs    []int64 `json:"tag_ids"`
	Text      string  `json:"text"`
}

// handleImportParse 解析原文并在内存中创建草稿。不写业务库。
func (s *Server) handleImportParse(w http.ResponseWriter, r *http.Request) {
	var body importParseBody
	if err := decodeJSON(w, r, maxImportBody, &body); err != nil {
		writeError(w, r, err)
		return
	}
	if body.ProjectID <= 0 {
		writeError(w, r, model.ErrValidation("project_required", "必须指定项目"))
		return
	}
	if _, err := s.projects.Get(r.Context(), body.ProjectID); err != nil {
		writeError(w, r, err)
		return
	}
	if len(body.Text) > importer.MaxRawBytes {
		writeError(w, r, model.ErrTooLargeCode("raw_too_large", "导入原文超过 8 MiB 上限"))
		return
	}

	// 全进程同时只允许一个解析/提交/备份任务。
	if !s.taskLock.Try() {
		writeError(w, r, model.ErrServiceBusy("task_busy", "正在处理其他导入或备份任务，请稍后再试"))
		return
	}
	defer s.taskLock.Release()

	res, err := importer.Parse(body.Text)
	if err != nil {
		if err == importer.ErrTooLarge {
			writeError(w, r, model.ErrTooLargeCode("import_too_large", "导入内容超过服务端上限"))
			return
		}
		var pe *importer.ParseError
		if ok := asParseError(err, &pe); ok {
			writeError(w, r, model.ErrValidation(pe.Code, pe.Message))
			return
		}
		writeError(w, r, model.ErrValidation("parse_failed", "解析失败"))
		return
	}

	sess := sessionFrom(r.Context())
	rawHash := s.auth.RawFingerprint([]byte(body.Text))
	draft, err := s.imports.Create(sess.ID, body.ProjectID, body.TagIDs, rawHash, res.Rows)
	if err != nil {
		writeError(w, r, model.ErrServiceBusy("draft_limit", "草稿容量不足，请先提交或关闭已有草稿"))
		return
	}
	if err := s.deduper.Mark(r.Context(), draft); err != nil {
		s.imports.Delete(draft.ID)
		writeError(w, r, err)
		return
	}

	items, total := draft.Page(1, importer.DefaultPreviewPageSize)
	writeData(w, http.StatusOK, map[string]any{
		"preview_id":       draft.ID,
		"expires_at":       draft.ExpiresAt.Unix(),
		"preview_revision": draft.Revision,
		"commit_key":       draft.CommitKey,
		"format":           res.Format,
		"summary":          draft.Summary(),
		"strategy":         draft.Strategy,
		"items":            items,
		"page":             1,
		"page_size":        importer.DefaultPreviewPageSize,
		"total":            total,
	})
}

func (s *Server) handleImportPreview(w http.ResponseWriter, r *http.Request) {
	draft, err := s.draftFromRequest(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	page := pageFromQuery(r, importer.DefaultPreviewPageSize, importer.MaxPreviewPageSize)
	items, total := draft.Page(page.Page, page.PageSize)
	writeData(w, http.StatusOK, map[string]any{
		"preview_id":       draft.ID,
		"expires_at":       draft.ExpiresAt.Unix(),
		"preview_revision": draft.Revision,
		"commit_key":       draft.CommitKey,
		"summary":          draft.Summary(),
		"strategy":         draft.Strategy,
		"items":            items,
		"page":             page.Page,
		"page_size":        page.PageSize,
		"total":            total,
	})
}

type importStrategyBody struct {
	Revision int64  `json:"preview_revision"`
	Strategy string `json:"strategy"`
}

func (s *Server) handleImportPreviewPatch(w http.ResponseWriter, r *http.Request) {
	draft, err := s.draftFromRequest(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var body importStrategyBody
	if err := decodeJSON(w, r, maxJSONBody, &body); err != nil {
		writeError(w, r, err)
		return
	}
	if err := draft.SetStrategy(body.Strategy, body.Revision); err != nil {
		writeError(w, r, model.ErrConflictCode("preview_revision_conflict", "预览内容已变化，请刷新后重试"))
		return
	}
	page := pageFromQuery(r, importer.DefaultPreviewPageSize, importer.MaxPreviewPageSize)
	items, total := draft.Page(page.Page, page.PageSize)
	writeData(w, http.StatusOK, map[string]any{
		"preview_id":       draft.ID,
		"expires_at":       draft.ExpiresAt.Unix(),
		"preview_revision": draft.Revision,
		"commit_key":       draft.CommitKey,
		"summary":          draft.Summary(),
		"strategy":         draft.Strategy,
		"items":            items,
		"page":             page.Page,
		"page_size":        page.PageSize,
		"total":            total,
	})
}

type importRowPatchBody struct {
	Revision       int64          `json:"preview_revision"`
	Email          *string        `json:"email"`
	Username       *string        `json:"username"`
	Password       nullableString `json:"password"`
	BackupEmail    nullableString `json:"backup_email"`
	F2A            nullableString `json:"f2a"`
	CredentialJSON nullableString `json:"credential_json"`
	RefreshToken   nullableString `json:"refresh_token"`
	SMSLink        nullableString `json:"sms_link"`
	Notes          nullableString `json:"notes"`
	Skip           *bool          `json:"skip"`
	TargetID       *int64         `json:"target_id"`
}

func (s *Server) handleImportRowPatch(w http.ResponseWriter, r *http.Request) {
	draft, err := s.draftFromRequest(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	rowID := queryInt(r, "row_id", 0)
	if rowID <= 0 {
		if v, e := pathID(r, "row_id"); e == nil {
			rowID = int(v)
		}
	}
	if rowID <= 0 {
		writeError(w, r, model.ErrValidation("invalid_row_id", "行号非法"))
		return
	}

	var body importRowPatchBody
	if err := decodeJSON(w, r, maxJSONBody, &body); err != nil {
		writeError(w, r, err)
		return
	}
	patch := importer.RowPatch{
		Email:    body.Email,
		Username: body.Username,
		Skip:     body.Skip,
		TargetID: body.TargetID,
	}
	if body.Password.Set {
		patch.Password = body.Password.resolved()
	}
	if body.BackupEmail.Set {
		patch.BackupEmail = body.BackupEmail.resolved()
	}
	if body.F2A.Set {
		patch.F2A = body.F2A.resolved()
	}
	if body.CredentialJSON.Set {
		patch.CredentialJSON = body.CredentialJSON.resolved()
	}
	if body.RefreshToken.Set {
		patch.RefreshToken = body.RefreshToken.resolved()
	}
	if body.SMSLink.Set {
		patch.SMSLink = body.SMSLink.resolved()
	}
	if body.Notes.Set {
		patch.Notes = body.Notes.resolved()
	}

	if err := draft.UpdateRow(rowID, patch, body.Revision); err != nil {
		if err == importer.ErrRowNotFound {
			writeError(w, r, model.ErrNotFoundCode("row_not_found", "该行不存在"))
			return
		}
		writeError(w, r, model.ErrConflictCode("preview_revision_conflict", "预览内容已变化，请刷新后重试"))
		return
	}
	// 编辑后重新评估去重状态。
	if err := s.deduper.Mark(r.Context(), draft); err != nil {
		writeError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{
		"preview_revision": draft.Revision,
		"summary":          draft.Summary(),
	})
}

type importRowRevealBody struct {
	Field string `json:"field"`
}

// handleImportRowReveal 只读取用户当前核对行的单个字段。
func (s *Server) handleImportRowReveal(w http.ResponseWriter, r *http.Request) {
	draft, err := s.draftFromRequest(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	rowID, err := pathID(r, "row_id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	var body importRowRevealBody
	if err := decodeJSON(w, r, maxJSONBody, &body); err != nil {
		writeError(w, r, err)
		return
	}
	row := draft.Row(int(rowID))
	if row == nil {
		writeError(w, r, model.ErrNotFoundCode("row_not_found", "该行不存在"))
		return
	}
	var value string
	switch body.Field {
	case "password":
		value = row.Password
	case "backup_email":
		value = row.BackupEmail
	case "f2a":
		value = row.F2A
	case "credential_json":
		value = row.CredentialJSON
	case "refresh_token":
		value = row.RefreshToken
	case "sms_link":
		value = row.SMSLink
	case "notes":
		value = row.Notes
	default:
		writeError(w, r, model.ErrValidation("field_invalid", "不支持的字段"))
		return
	}
	// 草稿取密使用随机预览 ID 的哈希与行号作为目标摘要，不记录原文。
	_ = audit.Write(r.Context(), s.db, audit.Entry{
		Action:     audit.ActionRevealSecret,
		TargetType: audit.TargetImport,
		TargetRef:  s.auth.RawFingerprint([]byte(draft.ID))[:16],
		Field:      body.Field,
		IP:         clientIPFrom(r.Context()),
	})
	writeData(w, http.StatusOK, map[string]any{"field": body.Field, "value": value})
}

func (s *Server) handleImportCancel(w http.ResponseWriter, r *http.Request) {
	draft, err := s.draftFromRequest(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.imports.Delete(draft.ID)
	s.writeAudit(r, audit.ActionCancelImport, audit.TargetImport, nil)
	writeData(w, http.StatusOK, map[string]any{"ok": true})
}

type importCommitBody struct {
	PreviewID string `json:"preview_id"`
	Revision  int64  `json:"preview_revision"`
	CommitKey string `json:"commit_key"`
	Strategy  string `json:"strategy"`
}

func (s *Server) handleImportCommit(w http.ResponseWriter, r *http.Request) {
	var body importCommitBody
	if err := decodeJSON(w, r, maxJSONBody, &body); err != nil {
		writeError(w, r, err)
		return
	}
	sess := sessionFrom(r.Context())

	draft, err := s.imports.Get(body.PreviewID, sess.ID)
	if err != nil {
		// 草稿可能已过期；先看批次是否已经写成。
		lookup, lerr := s.committer.Lookup(r.Context(), body.CommitKey)
		if lerr == nil && lookup.Status == "committed" {
			writeData(w, http.StatusOK, map[string]any{
				"batch_id":          lookup.BatchID,
				"summary":           lookup.Summary,
				"already_committed": true,
			})
			return
		}
		writeError(w, r, model.ErrGoneCode("draft_gone", "草稿已失效，请重新粘贴并解析"))
		return
	}

	if !s.taskLock.Try() {
		writeError(w, r, model.ErrServiceBusy("task_busy", "正在处理其他导入或备份任务，请稍后再试"))
		return
	}
	defer s.taskLock.Release()

	res, err := s.committer.Commit(r.Context(), draft, importer.CommitRequest{
		PreviewID: body.PreviewID,
		Revision:  body.Revision,
		CommitKey: body.CommitKey,
		Strategy:  body.Strategy,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	if !res.AlreadyCommitted {
		s.imports.Delete(draft.ID)
		s.writeAudit(r, audit.ActionImport, audit.TargetImport, &res.BatchID)
	}
	writeData(w, http.StatusOK, map[string]any{
		"batch_id":          res.BatchID,
		"summary":           res.Summary,
		"already_committed": res.AlreadyCommitted,
	})
}

type importResultBody struct {
	CommitKey string `json:"commit_key"`
}

// handleImportResult 让客户端在超时后查询真实结果。
func (s *Server) handleImportResult(w http.ResponseWriter, r *http.Request) {
	var body importResultBody
	if err := decodeJSON(w, r, maxJSONBody, &body); err != nil {
		writeError(w, r, err)
		return
	}
	lookup, err := s.committer.Lookup(r.Context(), body.CommitKey)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if lookup.Status == "committed" {
		writeData(w, http.StatusOK, map[string]any{
			"status":   "committed",
			"batch_id": lookup.BatchID,
			"summary":  lookup.Summary,
		})
		return
	}
	sess := sessionFrom(r.Context())
	if _, ok := s.imports.FindByCommitKey(sess.ID, body.CommitKey); ok {
		writeData(w, http.StatusOK, map[string]any{"status": "pending"})
		return
	}
	writeData(w, http.StatusOK, map[string]any{"status": "not_found"})
}

// draftFromRequest 取出并校验路径中的草稿，绑定当前会话。
func (s *Server) draftFromRequest(r *http.Request) (*importer.Draft, error) {
	id := r.PathValue("id")
	if id == "" {
		return nil, model.ErrValidation("missing_preview_id", "缺少 preview_id")
	}
	sess := sessionFrom(r.Context())
	if sess == nil {
		return nil, model.ErrUnauthorized("session_invalid", "会话无效")
	}
	draft, err := s.imports.Get(id, sess.ID)
	if err != nil {
		return nil, model.ErrGoneCode("draft_gone", "草稿已失效，请重新粘贴并解析")
	}
	return draft, nil
}

func asParseError(err error, target **importer.ParseError) bool {
	pe, ok := err.(*importer.ParseError)
	if ok {
		*target = pe
	}
	return ok
}
