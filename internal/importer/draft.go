package importer

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"passpal/internal/cryptoutil"
)

// 草稿上限（doc §10.9）。
const (
	// MaxDrafts 是全进程最多同时存在的已发布草稿数。
	MaxDrafts = 2
	// MaxDraftsPerSession 是同一会话最多同时存在的草稿数。
	MaxDraftsPerSession = 1
	// MaxDraftTotalBytes 是所有活动草稿的原文 + 标准化字段总字节上限。
	MaxDraftTotalBytes = 32 << 20 // 32 MiB
	// DraftTTL 是草稿的绝对存活期限，从解析开始计时。
	DraftTTL = 15 * time.Minute
	// DefaultPreviewPageSize 是预览默认页大小。
	DefaultPreviewPageSize = 50
	// MaxPreviewPageSize 是预览最大页大小。
	MaxPreviewPageSize = 100
)

// RowState 是一行在预览中的互斥状态。
type RowState string

const (
	RowStateNew       RowState = "new"
	RowStateDuplicate RowState = "duplicate"
	RowStateInvalid   RowState = "invalid"
)

// 去重策略。
const (
	StrategySkip      = "skip"      // 跳过重复（默认）
	StrategyOverwrite = "overwrite" // 覆盖旧账号
	StrategyKeep      = "keep"      // 全部保留为新账号
)

// IsValidStrategy 判断策略是否受支持。
func IsValidStrategy(s string) bool {
	return s == StrategySkip || s == StrategyOverwrite || s == StrategyKeep
}

// DraftRow 是草稿中的一行。含明文，只允许存在于内存。
type DraftRow struct {
	ID             int
	Line           int
	Email          string
	Username       string
	Password       string
	BackupEmail    string
	F2A            string
	CredentialJSON string
	RefreshToken   string
	SMSLink        string
	Notes          string
	Issues         []Issue

	State          RowState
	Duplicate      bool
	TargetID       int64
	TargetRevision int64
	TargetCount    int

	Skip bool
}

// HasPassword 等方法供脱敏视图使用。
func (r *DraftRow) HasPassword() bool { return r.Password != "" }

// PreviewRow 是下发给前端的脱敏行。
type PreviewRow struct {
	ID              int      `json:"row_id"`
	Line            int      `json:"line"`
	Email           string   `json:"email"`
	Username        string   `json:"username"`
	HasPassword     bool     `json:"has_password"`
	HasBackupEmail  bool     `json:"has_backup_email"`
	HasF2A          bool     `json:"has_f2a"`
	HasJSON         bool     `json:"has_json"`
	HasNotes        bool     `json:"has_notes"`
	HasRefreshToken bool     `json:"has_refresh_token"`
	HasSMSLink      bool     `json:"has_sms_link"`
	State           RowState `json:"state"`
	Issues          []Issue  `json:"issue_codes"`
	Skip            bool     `json:"skip"`
	DuplicateCount  int      `json:"duplicate_count,omitempty"`
}

// ToPreview 把内部行转成脱敏视图。
func (r *DraftRow) ToPreview() PreviewRow {
	issues := r.Issues
	if issues == nil {
		issues = []Issue{}
	}
	return PreviewRow{
		ID:              r.ID,
		Line:            r.Line,
		Email:           r.Email,
		Username:        r.Username,
		HasPassword:     r.Password != "",
		HasBackupEmail:  r.BackupEmail != "",
		HasF2A:          r.F2A != "",
		HasJSON:         r.CredentialJSON != "",
		HasNotes:        r.Notes != "",
		HasRefreshToken: r.RefreshToken != "",
		HasSMSLink:      r.SMSLink != "",
		State:           r.State,
		Issues:          issues,
		Skip:            r.Skip,
		DuplicateCount:  r.TargetCount,
	}
}

// Summary 是预览统计。三类互斥且之和等于总数。
type Summary struct {
	Total         int `json:"total"`
	ValidNew      int `json:"valid_new"`
	Duplicates    int `json:"duplicates"`
	Invalid       int `json:"invalid"`
	PasswordEmpty int `json:"password_empty"` // 有效行上的警告子集，不加进总数
}

// Draft 是一份内存草稿。绑定创建它的会话。
type Draft struct {
	ID        string
	SessionID string
	ProjectID int64
	TagIDs    []int64

	CreatedAt time.Time
	ExpiresAt time.Time

	Revision  int64
	CommitKey string
	RawHash   string

	Strategy string
	Rows     []*DraftRow

	// bytes 是本草稿占用的估算字节数，用于总量预算。
	bytes int

	mu sync.Mutex
}

// Bytes 返回草稿占用的估算字节数。
func (d *Draft) Bytes() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.bytes
}

// Summary 计算当前统计。
func (d *Draft) Summary() Summary {
	d.mu.Lock()
	defer d.mu.Unlock()
	var s Summary
	s.Total = len(d.Rows)
	for _, r := range d.Rows {
		switch r.State {
		case RowStateInvalid:
			s.Invalid++
		case RowStateDuplicate:
			s.Duplicates++
		default:
			s.ValidNew++
		}
		for _, is := range r.Issues {
			if is.Code == IssuePasswordEmpty {
				s.PasswordEmpty++
				break
			}
		}
	}
	return s
}

// Page 返回分页后的脱敏行。
func (d *Draft) Page(page, size int) ([]PreviewRow, int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if size < 1 {
		size = DefaultPreviewPageSize
	}
	if size > MaxPreviewPageSize {
		size = MaxPreviewPageSize
	}
	if page < 1 {
		page = 1
	}
	start := (page - 1) * size
	if start >= len(d.Rows) {
		return []PreviewRow{}, len(d.Rows)
	}
	end := start + size
	if end > len(d.Rows) {
		end = len(d.Rows)
	}
	out := make([]PreviewRow, 0, end-start)
	for _, r := range d.Rows[start:end] {
		out = append(out, r.ToPreview())
	}
	return out, len(d.Rows)
}

// RowPatch 是行编辑请求。
type RowPatch struct {
	Email          *string `json:"email"`
	Username       *string `json:"username"`
	Password       *string `json:"password"`
	BackupEmail    *string `json:"backup_email"`
	F2A            *string `json:"f2a"`
	CredentialJSON *string `json:"credential_json"`
	RefreshToken   *string `json:"refresh_token"`
	SMSLink        *string `json:"sms_link"`
	Notes          *string `json:"notes"`
	Skip           *bool   `json:"skip"`
	TargetID       *int64  `json:"target_id"`
}

// ErrRowNotFound 表示草稿中不存在该行。
var ErrRowNotFound = errors.New("草稿行不存在")

// UpdateRow 修改一行，成功后递增草稿 revision。
func (d *Draft) UpdateRow(rowID int, patch RowPatch, revision int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if revision != d.Revision {
		return fmt.Errorf("revision 不匹配")
	}
	row := d.findRow(rowID)
	if row == nil {
		return ErrRowNotFound
	}
	if patch.Email != nil {
		row.Email = *patch.Email
	}
	if patch.Username != nil {
		row.Username = *patch.Username
	}
	if patch.Password != nil {
		row.Password = *patch.Password
	}
	if patch.BackupEmail != nil {
		row.BackupEmail = *patch.BackupEmail
	}
	if patch.F2A != nil {
		row.F2A = *patch.F2A
	}
	if patch.CredentialJSON != nil {
		row.CredentialJSON = *patch.CredentialJSON
	}
	if patch.Notes != nil {
		row.Notes = *patch.Notes
	}
	if patch.RefreshToken != nil {
		row.RefreshToken = *patch.RefreshToken
	}
	if patch.SMSLink != nil {
		row.SMSLink = *patch.SMSLink
	}
	if patch.Skip != nil {
		row.Skip = *patch.Skip
	}
	if patch.TargetID != nil {
		row.TargetID = *patch.TargetID
	}
	d.reclassify(row)
	d.recount()
	d.Revision++
	return nil
}

// Row 返回内部行（含明文），仅供服务端内部使用。
func (d *Draft) Row(rowID int) *DraftRow {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.findRow(rowID)
}

// RowsCopy 返回行的浅拷贝切片，避免调用方在锁外遍历。
func (d *Draft) RowsCopy() []*DraftRow {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]*DraftRow, len(d.Rows))
	copy(out, d.Rows)
	return out
}

// SetStrategy 设置去重策略并重新分类。
func (d *Draft) SetStrategy(strategy string, revision int64) error {
	if !IsValidStrategy(strategy) {
		return fmt.Errorf("不支持的重复策略")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if revision != d.Revision {
		return fmt.Errorf("revision 不匹配")
	}
	d.Strategy = strategy
	for _, r := range d.Rows {
		d.reclassify(r)
	}
	d.Revision++
	return nil
}

func (d *Draft) findRow(rowID int) *DraftRow {
	for _, r := range d.Rows {
		if r.ID == rowID {
			return r
		}
	}
	return nil
}

// reclassify 依据邮箱有效性与去重结果重算状态。
func (d *Draft) reclassify(r *DraftRow) {
	if hasBlockingIssue(r.Issues) || !looksLikeEmail(r.Email) {
		r.State = RowStateInvalid
		return
	}
	if r.Duplicate && d.Strategy == StrategySkip {
		r.State = RowStateDuplicate
		return
	}
	r.State = RowStateNew
}

func hasBlockingIssue(issues []Issue) bool {
	for _, is := range issues {
		switch is.Code {
		case IssueEmailMissing, IssueEmailInvalid, IssueRowTooLarge, IssueFieldTooLarge:
			return true
		}
	}
	return false
}

// recount 重算草稿占用字节数。
func (d *Draft) recount() {
	total := 0
	for _, r := range d.Rows {
		total += len(r.Email) + len(r.Username) + len(r.Password) + len(r.BackupEmail) +
			len(r.F2A) + len(r.CredentialJSON) + len(r.RefreshToken) + len(r.SMSLink) + len(r.Notes) + 64
	}
	d.bytes = total
}

// Store 是进程内草稿仓库。
type Store struct {
	mu     sync.Mutex
	drafts map[string]*Draft
	bytes  int
	nowFn  func() time.Time
}

// NewStore 构造草稿仓库。
func NewStore() *Store {
	return &Store{
		drafts: make(map[string]*Draft),
		nowFn:  time.Now,
	}
}

// ErrDraftLimit 表示草稿容量不足。
var ErrDraftLimit = errors.New("草稿容量不足")

// Create 用解析结果创建草稿。
//
// 同会话最多 1 个草稿；全进程最多 MaxDrafts 个；总量受 MaxDraftTotalBytes 约束。
// 容量检查包含新旧草稿重叠期间，不足时提示用户关闭旧草稿。
func (s *Store) Create(sessionID string, projectID int64, tagIDs []int64, rawHash string, rows []Row) (*Draft, error) {
	now := s.nowFn()

	estimated := 0
	for i := range rows {
		estimated += len(rows[i].Email) + len(rows[i].Username) + len(rows[i].Password) +
			len(rows[i].BackupEmail) + len(rows[i].F2A) + len(rows[i].CredentialJSON) +
			len(rows[i].RefreshToken) + len(rows[i].SMSLink) + len(rows[i].Notes) + 64
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.evictExpiredLocked(now)

	if s.bytes+estimated > MaxDraftTotalBytes {
		return nil, fmt.Errorf("%w：请先关闭或提交已有草稿", ErrDraftLimit)
	}

	id, err := cryptoutil.RandomHex(16)
	if err != nil {
		return nil, err
	}
	commitKey, err := cryptoutil.RandomHex(32)
	if err != nil {
		return nil, err
	}

	d := &Draft{
		ID:        id,
		SessionID: sessionID,
		ProjectID: projectID,
		TagIDs:    tagIDs,
		CreatedAt: now,
		ExpiresAt: now.Add(DraftTTL),
		Revision:  1,
		CommitKey: commitKey,
		RawHash:   rawHash,
		Strategy:  StrategySkip,
		Rows:      make([]*DraftRow, 0, len(rows)),
		bytes:     estimated,
	}
	for i := range rows {
		r := rows[i]
		state := RowStateNew
		if hasBlockingIssue(r.Issues) || !looksLikeEmail(r.Email) {
			state = RowStateInvalid
		}
		d.Rows = append(d.Rows, &DraftRow{
			ID:             i + 1,
			Line:           r.Line,
			Email:          r.Email,
			Username:       r.Username,
			Password:       r.Password,
			BackupEmail:    r.BackupEmail,
			F2A:            r.F2A,
			CredentialJSON: r.CredentialJSON,
			RefreshToken:   r.RefreshToken,
			SMSLink:        r.SMSLink,
			Notes:          r.Notes,
			Issues:         r.Issues,
			State:          state,
		})
	}

	// 同会话旧草稿立即失效。
	for key, old := range s.drafts {
		if old.SessionID == sessionID {
			s.bytes -= old.bytes
			delete(s.drafts, key)
		}
	}
	// 超出全进程上限时淘汰最旧的草稿。
	for len(s.drafts) >= MaxDrafts {
		oldestKey := ""
		var oldest time.Time
		for key, dr := range s.drafts {
			if oldestKey == "" || dr.CreatedAt.Before(oldest) {
				oldestKey, oldest = key, dr.CreatedAt
			}
		}
		if oldestKey == "" {
			break
		}
		s.bytes -= s.drafts[oldestKey].bytes
		delete(s.drafts, oldestKey)
	}

	s.drafts[d.ID] = d
	s.bytes += estimated
	return d, nil
}

// Get 取回草稿并校验会话绑定与 TTL。
func (s *Store) Get(id, sessionID string) (*Draft, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowFn()
	s.evictExpiredLocked(now)

	d, ok := s.drafts[id]
	if !ok {
		return nil, ErrDraftNotFound
	}
	if d.SessionID != sessionID {
		// 不泄露其他会话草稿的存在。
		return nil, ErrDraftNotFound
	}
	return d, nil
}

// Delete 释放草稿（取消、登出、提交完成后调用）。
func (s *Store) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d, ok := s.drafts[id]; ok {
		s.bytes -= d.bytes
		delete(s.drafts, id)
	}
}

// DeleteBySession 释放某个会话的全部草稿（登出时调用）。
func (s *Store) DeleteBySession(sessionID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for key, d := range s.drafts {
		if d.SessionID == sessionID {
			s.bytes -= d.bytes
			delete(s.drafts, key)
			n++
		}
	}
	return n
}

// FindByCommitKey 按 commit_key 查找该会话仍然有效的草稿。
//
// 用于超时后查询提交状态：先查已成功批次，再落到这里判断 pending。
func (s *Store) FindByCommitKey(sessionID, commitKey string) (*Draft, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evictExpiredLocked(s.nowFn())
	for _, d := range s.drafts {
		if d.SessionID == sessionID && d.CommitKey == commitKey {
			return d, true
		}
	}
	return nil, false
}

// Cleanup 清理过期草稿。
func (s *Store) Cleanup() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.evictExpiredLocked(s.nowFn())
}

func (s *Store) evictExpiredLocked(now time.Time) int {
	n := 0
	for key, d := range s.drafts {
		if !now.Before(d.ExpiresAt) {
			s.bytes -= d.bytes
			delete(s.drafts, key)
			n++
		}
	}
	return n
}

// Stats 返回草稿仓库的当前用量（供运维观察）。
func (s *Store) Stats() (count, bytes int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.drafts), s.bytes
}

// ErrDraftNotFound 表示草稿不存在、已过期或不属于当前会话。
var ErrDraftNotFound = errors.New("草稿不存在或已过期")

// SortRowsByLine 保证行顺序稳定（行号升序，其次行 ID）。
func SortRowsByLine(rows []*DraftRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Line != rows[j].Line {
			return rows[i].Line < rows[j].Line
		}
		return rows[i].ID < rows[j].ID
	})
}
