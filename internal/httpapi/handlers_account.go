package httpapi

import (
	"net/http"
	"strings"

	"passpal/internal/account"
	"passpal/internal/audit"
	"passpal/internal/cryptoutil"
	"passpal/internal/model"
)

// listAccounts 的默认与最大页大小（doc §20）。
const (
	accountDefaultPageSize = 100
	accountMaxPageSize     = 100
)

func (s *Server) handleListAccounts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := account.Filter{
		ProjectID: queryInt64(r, "project_id"),
		TagID:     queryInt64(r, "tag_id"),
		Query:     q.Get("q"),
		Status:    q.Get("status"),
		Sort:      account.NormalizeSort(q.Get("sort")),
		Page:      pageFromQuery(r, accountDefaultPageSize, accountMaxPageSize),
	}
	res, err := s.accounts.List(r.Context(), filter)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, res)
}

func (s *Server) handleGetAccount(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	a, err := s.accounts.Get(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, a)
}

type createAccountBody struct {
	ProjectID      int64        `json:"project_id"`
	Email          string       `json:"email"`
	Username       *string      `json:"username"`
	Password       *string      `json:"password"`
	BackupEmail    *string      `json:"backup_email"`
	F2A            *string      `json:"f2a"`
	CredentialJSON *string      `json:"credential_json"`
	Notes          *string      `json:"notes"`
	RefreshToken   *string      `json:"refresh_token"`
	SMSLink        *string      `json:"sms_link"`
	Status         *string      `json:"status"`
	Extra          *model.Extra `json:"extra"`
	TagIDs         []int64      `json:"tag_ids"`
}

func (s *Server) handleCreateAccount(w http.ResponseWriter, r *http.Request) {
	var body createAccountBody
	if err := decodeJSON(w, r, maxJSONBody, &body); err != nil {
		writeError(w, r, err)
		return
	}
	if body.ProjectID <= 0 {
		writeError(w, r, model.ErrValidation("project_required", "必须指定项目"))
		return
	}
	in := model.AccountInput{
		Email:          &body.Email,
		Username:       body.Username,
		Password:       body.Password,
		BackupEmail:    body.BackupEmail,
		F2A:            body.F2A,
		CredentialJSON: body.CredentialJSON,
		Notes:          body.Notes,
		RefreshToken:   body.RefreshToken,
		SMSLink:        body.SMSLink,
		Status:         body.Status,
		Extra:          body.Extra,
	}
	tagIDs := body.TagIDs
	in.TagIDs = &tagIDs

	a, err := s.accounts.Create(r.Context(), body.ProjectID, in)
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.writeAudit(r, audit.ActionCreateAccount, audit.TargetAccount, &a.ID)
	writeData(w, http.StatusCreated, a)
}

// patchAccountBody 使用 nullableString 区分「未出现 / null / 有值」。
type patchAccountBody struct {
	Revision       int64          `json:"revision"`
	Email          *string        `json:"email"`
	Username       *string        `json:"username"`
	Password       nullableString `json:"password"`
	BackupEmail    nullableString `json:"backup_email"`
	F2A            nullableString `json:"f2a"`
	CredentialJSON nullableString `json:"credential_json"`
	Notes          nullableString `json:"notes"`
	RefreshToken   nullableString `json:"refresh_token"`
	SMSLink        nullableString `json:"sms_link"`
	Status         *string        `json:"status"`
	Extra          *model.Extra   `json:"extra"`
	TagIDs         *[]int64       `json:"tag_ids"`
}

func (b patchAccountBody) toInput() model.AccountInput {
	in := model.AccountInput{
		Email:    b.Email,
		Username: b.Username,
		Status:   b.Status,
		Extra:    b.Extra,
		TagIDs:   b.TagIDs,
	}
	if b.Password.Set {
		in.Password = b.Password.resolved()
	}
	if b.BackupEmail.Set {
		in.BackupEmail = b.BackupEmail.resolved()
	}
	if b.F2A.Set {
		in.F2A = b.F2A.resolved()
	}
	if b.CredentialJSON.Set {
		in.CredentialJSON = b.CredentialJSON.resolved()
	}
	if b.Notes.Set {
		in.Notes = b.Notes.resolved()
	}
	if b.RefreshToken.Set {
		in.RefreshToken = b.RefreshToken.resolved()
	}
	if b.SMSLink.Set {
		in.SMSLink = b.SMSLink.resolved()
	}
	return in
}

func (s *Server) handleUpdateAccount(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	var body patchAccountBody
	if err := decodeJSON(w, r, maxJSONBody, &body); err != nil {
		writeError(w, r, err)
		return
	}
	a, err := s.accounts.Update(r.Context(), id, body.Revision, body.toInput())
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.writeAudit(r, audit.ActionUpdateAccount, audit.TargetAccount, &id)
	writeData(w, http.StatusOK, a)
}

func (s *Server) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.accounts.SoftDelete(r.Context(), id); err != nil {
		writeError(w, r, err)
		return
	}
	s.writeAudit(r, audit.ActionDeleteAccount, audit.TargetAccount, &id)
	writeData(w, http.StatusOK, map[string]any{"ok": true})
}

type batchDeleteBody struct {
	IDs []int64 `json:"ids"`
}

func (s *Server) handleBatchDeleteAccounts(w http.ResponseWriter, r *http.Request) {
	var body batchDeleteBody
	if err := decodeJSON(w, r, maxJSONBody, &body); err != nil {
		writeError(w, r, err)
		return
	}
	n, err := s.accounts.BatchSoftDelete(r.Context(), body.IDs)
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.writeAudit(r, audit.ActionBatchDelete, audit.TargetAccount, nil)
	writeData(w, http.StatusOK, map[string]any{"deleted": n})
}

func (s *Server) handleRestoreAccount(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	a, err := s.accounts.Restore(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.writeAudit(r, audit.ActionRestoreItem, audit.TargetAccount, &id)
	writeData(w, http.StatusOK, a)
}

type revealBody struct {
	Field string `json:"field"`
}

// handleRevealAccountField 一次只返回一个字段的明文。
//
// 响应已由安全头统一设置 no-store；这里只返回当前字段值，
// 不附带账号其余秘密。
func (s *Server) handleRevealAccountField(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, r, err)
		return
	}
	var body revealBody
	if err := decodeJSON(w, r, maxJSONBody, &body); err != nil {
		writeError(w, r, err)
		return
	}
	plain, err := s.accounts.Reveal(r.Context(), id, body.Field)
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.writeAuditField(r, audit.ActionRevealSecret, audit.TargetAccount, &id, body.Field)
	writeData(w, http.StatusOK, map[string]any{
		"field": body.Field,
		"value": plain,
	})
}

type batchExportRTBody struct {
	IDs []int64 `json:"ids"`
}

// handleExportRefreshTokens 批量导出选中账号的 refresh token。
//
// 返回一份可直接复制的文本（每行 email----refresh_token）以及结构化条目，
// 由前端决定是展示、复制还是下载。响应 no-store，并写审计。
func (s *Server) handleExportRefreshTokens(w http.ResponseWriter, r *http.Request) {
	var body batchExportRTBody
	if err := decodeJSON(w, r, maxJSONBody, &body); err != nil {
		writeError(w, r, err)
		return
	}
	entries, err := s.accounts.ExportRefreshTokens(r.Context(), body.IDs)
	if err != nil {
		writeError(w, r, err)
		return
	}

	lines := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.RefreshToken == "" {
			continue
		}
		lines = append(lines, e.Email+"----"+e.RefreshToken)
	}

	_ = audit.Write(r.Context(), s.db, audit.Entry{
		Action:     audit.ActionBatchExportRT,
		TargetType: audit.TargetAccount,
		Field:      cryptoutil.FieldRefreshToken,
		TargetRef:  itoaStr(len(entries)) + " accounts",
		IP:         clientIPFrom(r.Context()),
	})

	writeData(w, http.StatusOK, map[string]any{
		"requested": len(entries),
		"exported":  len(lines),
		"text":      strings.Join(lines, "\n"),
		"items":     entries,
	})
}

func itoaStr(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
