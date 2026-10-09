// Package backup 实现基于 VACUUM INTO 的在线备份与离线恢复。
//
// 不使用 cp database.sqlite：WAL 模式下直接复制主库文件会漏掉 -wal 中
// 尚未 checkpoint 的最新事务，备份可能过期甚至损坏。
package backup

import (
	"context"
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

// 备份文件名格式：backup-20060102-150405.sqlite
const (
	namePrefix = "backup-"
	nameSuffix = ".sqlite"
	timeLayout = "20060102-150405"
)

// Info 描述一个备份文件。
type Info struct {
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	CreatedAt int64  `json:"created_at"`
}

// Service 提供备份的生成、列举与清理。
type Service struct {
	db     *database.DB
	dir    string
	retain time.Duration
	mu     sync.RWMutex
}

// New 构造备份服务。
func New(db *database.DB, dir string, retain time.Duration) (*Service, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("备份目录不能为空")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("创建备份目录失败：%w", err)
	}
	return &Service{db: db, dir: dir, retain: retain}, nil
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

// Run 生成一次备份。
//
// 流程：VACUUM INTO 未发布临时快照 -> 打开备份文件执行 integrity_check 与
// foreign_key_check -> 通过后在同一文件系统原子发布。失败文件不出现在可下载清单。
func (s *Service) Run(ctx context.Context) (*Info, error) {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return nil, model.WrapError(500, "backup_failed", "创建备份目录失败", err)
	}

	name := namePrefix + time.Now().UTC().Format(timeLayout) + nameSuffix
	final := filepath.Join(s.dir, name)
	tmp := filepath.Join(s.dir, ".tmp-"+name)

	_ = os.Remove(tmp)
	// 同名文件（同一秒内重复触发）不应被覆盖。
	if _, err := os.Stat(final); err == nil {
		return nil, model.ErrConflictCode("backup_exists", "同一秒内已有备份，请稍后重试")
	}

	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, tmp); err != nil {
		_ = os.Remove(tmp)
		return nil, model.WrapError(500, "backup_failed", "生成快照失败", err)
	}

	// 校验的是备份文件本身，不是源库。
	if err := verifyFile(ctx, tmp); err != nil {
		_ = os.Remove(tmp)
		return nil, err
	}

	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
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

func isBackupName(name string) bool {
	return strings.HasPrefix(name, namePrefix) && strings.HasSuffix(name, nameSuffix) &&
		!strings.HasPrefix(name, ".")
}

func parseNameTime(name string) (time.Time, error) {
	core := strings.TrimSuffix(strings.TrimPrefix(name, namePrefix), nameSuffix)
	return time.ParseInLocation(timeLayout, core, time.UTC)
}

func statFile(path string) (*Info, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, model.WrapError(500, "internal", "读取备份文件信息失败", err)
	}
	ts, _ := parseNameTime(fi.Name())
	return &Info{Name: fi.Name(), Size: fi.Size(), CreatedAt: ts.Unix()}, nil
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
