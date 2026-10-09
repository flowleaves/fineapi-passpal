// Package backup 实现基于 VACUUM INTO 的在线备份与离线恢复。
//
// 不使用 cp database.sqlite：WAL 模式下直接复制主库文件会漏掉 -wal 中
// 尚未 checkpoint 的最新事务，备份可能过期甚至损坏。
package backup

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"passpal/internal/audit"
	"passpal/internal/database"
	"passpal/internal/model"
)

// 备份文件名格式：backup-20060102-150405.tar.gz
//
// 归档内是「加密快照 + 密钥 + 清单」，恢复时不需要任何外部密钥（见 archive.go）。
const (
	namePrefix = "backup-"
	nameSuffix = ArchiveSuffix
	timeLayout = "20060102-150405"
)

// legacySuffix 是旧版纯快照备份的扩展名。
// 仍然能被列举、下载与恢复（恢复时按外部密钥处理），只是不再新产出。
const legacySuffix = ".sqlite"

// Info 描述一个备份文件。
type Info struct {
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	CreatedAt int64  `json:"created_at"`
	// SelfContained 报告该备份是否自带密钥（即归档格式）。
	// 旧版 .sqlite 快照为 false —— 恢复时必须另外提供密钥。
	SelfContained bool `json:"self_contained"`
}

// Service 提供备份的生成、列举与清理。
type Service struct {
	db     *database.DB
	dir    string
	retain time.Duration
	// keys / currentKey 用于把密钥写进自包含归档。
	keys       map[int][]byte
	currentKey int
	mu         sync.RWMutex
}

// New 构造备份服务。
//
// keys 与 currentKey 来自配置；归档会把它们一起打包，
// 这样恢复端无需依赖任何外部密钥。
func New(db *database.DB, dir string, retain time.Duration,
	keys map[int][]byte, currentKey int) (*Service, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("备份目录不能为空")
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("缺少数据加密密钥，无法生成可恢复的备份")
	}
	if _, ok := keys[currentKey]; !ok {
		return nil, fmt.Errorf("当前密钥版本 %d 未配置", currentKey)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("创建备份目录失败：%w", err)
	}
	return &Service{db: db, dir: dir, retain: retain, keys: keys, currentKey: currentKey}, nil
}

// SetRetain 更新保留期（设置页修改后调用）。
func (s *Service) SetRetain(d time.Duration) {
	if d <= 0 {
		return
	}
	s.mu.Lock()
	s.retain = d
	s.mu.Unlock()
}

func (s *Service) retention() time.Duration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.retain
}

// Dir 返回备份目录。
func (s *Service) Dir() string { return s.dir }

// Run 生成一次自包含备份归档。
//
// 流程：VACUUM INTO 未发布临时快照 -> 校验快照 -> 统计内容 -> 打包归档
// （快照 + 密钥 + 清单）-> **解包回读再校验** -> 原子发布。
// 任何一步失败都不会留下可下载的成品文件；最后的回读校验保证产出的归档
// 真的能恢复，而不是「看起来生成了」。
func (s *Service) Run(ctx context.Context) (*Info, error) {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return nil, model.WrapError(500, "backup_failed", "创建备份目录失败", err)
	}

	name := namePrefix + time.Now().UTC().Format(timeLayout) + nameSuffix
	final := filepath.Join(s.dir, name)
	snap := filepath.Join(s.dir, ".tmp-snap-"+name+legacySuffix)
	arch := filepath.Join(s.dir, ".tmp-"+name)

	_ = os.Remove(snap)
	_ = os.Remove(arch)
	// 同名文件（同一秒内重复触发）不应被覆盖。
	if _, err := os.Stat(final); err == nil {
		return nil, model.ErrConflictCode("backup_exists", "同一秒内已有备份，请稍后重试")
	}

	// 1) 一致性快照。用 VACUUM INTO 而非复制文件：WAL 模式下复制主库会漏掉
	//    尚未 checkpoint 的事务。
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, snap); err != nil {
		_ = os.Remove(snap)
		return nil, model.WrapError(500, "backup_failed", "生成快照失败", err)
	}
	defer func() { _ = os.Remove(snap) }()

	// 2) 校验的是快照文件本身，不是源库。
	if err := verifyFile(ctx, snap); err != nil {
		return nil, err
	}

	// 3) 内容概览，写进清单便于恢复前核对。
	counts, schemaVersion, err := collectCounts(ctx, snap)
	if err != nil {
		return nil, model.WrapError(500, "backup_failed", "统计备份内容失败", err)
	}

	// 4) 打包成自包含归档（含密钥）。
	if err := writeArchive(arch, snap, s.keys, s.currentKey, counts, schemaVersion); err != nil {
		_ = os.Remove(arch)
		return nil, model.WrapError(500, "backup_failed", "生成备份归档失败", err)
	}

	// 5) 解包回读并校验，确认归档真的可恢复。
	if err := verifyArchive(ctx, arch); err != nil {
		_ = os.Remove(arch)
		return nil, err
	}

	if err := os.Rename(arch, final); err != nil {
		_ = os.Remove(arch)
		return nil, model.WrapError(500, "backup_failed", "发布备份文件失败", err)
	}
	_ = os.Chmod(final, 0o600)

	if err := database.SetSetting(ctx, s.db, "last_backup_at",
		strconv.FormatInt(time.Now().Unix(), 10), "backup"); err != nil {
		// 记录失败不影响备份本身的有效性。
		_ = err
	}
	_ = audit.Write(ctx, s.db, audit.Entry{
		Action:     audit.ActionBackup,
		TargetType: audit.TargetBackup,
		TargetRef:  name,
	})

	s.prune()

	return statFile(final)
}

// collectCounts 统计备份内容概览与 schema 版本。
func collectCounts(ctx context.Context, path string) (map[string]int, int64, error) {
	db, err := database.OpenReadOnly(ctx, path)
	if err != nil {
		return nil, 0, err
	}
	defer db.Close()

	counts := make(map[string]int, 3)
	for _, t := range []string{"projects", "tags", "accounts"} {
		var n int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+t).Scan(&n); err != nil {
			return nil, 0, err
		}
		counts[t] = n
	}
	var ver sql.NullInt64
	if err := db.QueryRowContext(ctx,
		`SELECT MAX(version) FROM schema_migrations`).Scan(&ver); err != nil {
		return nil, 0, err
	}
	return counts, ver.Int64, nil
}

// verifyArchive 解包归档到临时目录并校验其中的数据库与密钥。
func verifyArchive(ctx context.Context, path string) error {
	dir, err := os.MkdirTemp(filepath.Dir(path), ".verify-")
	if err != nil {
		return model.WrapError(500, "backup_failed", "创建校验临时目录失败", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	got, err := extractArchive(path, dir)
	if err != nil {
		return model.WrapError(500, "backup_invalid", "归档无法解包", err)
	}
	if len(got.Keys) == 0 {
		return model.NewError(500, "backup_invalid", "归档内未包含密钥，无法保证可恢复")
	}
	return verifyFile(ctx, got.DBPath)
}

// List 返回可下载的备份清单（不含临时文件）。
func (s *Service) List() ([]Info, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []Info{}, nil
		}
		return nil, model.WrapError(500, "internal", "读取备份目录失败", err)
	}
	out := make([]Info, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !isBackupName(e.Name()) {
			continue
		}
		info, err := statFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		out = append(out, *info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out, nil
}

// Path 返回某个备份的绝对路径，并拒绝路径穿越。
func (s *Service) Path(name string) (string, error) {
	if !isBackupName(name) {
		return "", model.ErrValidation("invalid_backup_name", "备份文件名非法")
	}
	full := filepath.Join(s.dir, filepath.Base(name))
	rel, err := filepath.Rel(s.dir, full)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", model.ErrValidation("invalid_backup_name", "备份文件名非法")
	}
	if _, err := os.Stat(full); err != nil {
		return "", model.ErrNotFoundCode("backup_not_found", "备份文件不存在")
	}
	return full, nil
}

// prune 删除超出保留期的备份。删除失败不影响已生成的备份。
func (s *Service) prune() {
	cutoff := time.Now().Add(-s.retention())
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !isBackupName(e.Name()) {
			continue
		}
		ts, err := parseNameTime(e.Name())
		if err != nil {
			continue
		}
		if ts.Before(cutoff) {
			_ = os.Remove(filepath.Join(s.dir, e.Name()))
		}
	}
}

// isBackupName 接受新版归档与旧版纯快照两种命名，并要求时间戳可解析。
// 旧文件仍会被列举、下载与清理，只是不再新产出。
func isBackupName(name string) bool {
	if strings.HasPrefix(name, ".") || !strings.HasPrefix(name, namePrefix) {
		return false
	}
	if !strings.HasSuffix(name, ArchiveSuffix) && !strings.HasSuffix(name, legacySuffix) {
		return false
	}
	// 时间戳必须合法，否则 backup-.tar.gz 这类垃圾名也会被当成备份。
	_, err := parseNameTime(name)
	return err == nil
}

func parseNameTime(name string) (time.Time, error) {
	core := strings.TrimPrefix(name, namePrefix)
	core = strings.TrimSuffix(core, ArchiveSuffix)
	core = strings.TrimSuffix(core, legacySuffix)
	return time.ParseInLocation(timeLayout, core, time.UTC)
}

func statFile(path string) (*Info, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, model.WrapError(500, "internal", "读取备份文件信息失败", err)
	}
	ts, _ := parseNameTime(fi.Name())
	return &Info{
		Name:          fi.Name(),
		Size:          fi.Size(),
		CreatedAt:     ts.Unix(),
		SelfContained: IsArchivePath(fi.Name()),
	}, nil
}

// verifyFile 用只读连接校验候选备份的完整性与外键。
func verifyFile(ctx context.Context, path string) error {
	db, err := database.OpenReadOnly(ctx, path)
	if err != nil {
		return model.WrapError(500, "backup_invalid", "备份文件无法打开", err)
	}
	defer db.Close()
	if err := db.IntegrityCheck(ctx); err != nil {
		return model.WrapError(500, "backup_invalid", "备份完整性校验未通过", err)
	}
	if err := db.ForeignKeyCheck(ctx); err != nil {
		return model.WrapError(500, "backup_invalid", "备份外键校验未通过", err)
	}
	return nil
}
