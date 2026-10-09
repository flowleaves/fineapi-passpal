package auth

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"sync"
	"time"

	"passpal/internal/audit"
	"passpal/internal/config"
	"passpal/internal/cryptoutil"
	"passpal/internal/database"
	"passpal/internal/model"
)

// Cookie 与请求头名称。
const (
	CookieSession     = "pp_session"
	CookieCSRF        = "pp_csrf"
	HeaderCSRF        = "X-CSRF-Token"
	SessionTokenBytes = 32
)

// 全局 Argon2 校验预算：每秒补充 2 个令牌、桶容量 2，且同时只允许 1 次校验。
const (
	verifyRatePerSecond = 2
	verifyBurst         = 2
)

// Service 负责登录、会话、来源锁定与 CSRF。
type Service struct {
	db  *database.DB
	cfg *config.Config

	sourceHMACKey []byte
	csrfHMACKey   []byte
	rawHMACKey    []byte

	slot   chan struct{}
	bucket *tokenBucket

	mu         sync.RWMutex
	maxAttempt int
	lockFor    time.Duration
	ttl        time.Duration
}

// LoginResult 是一次成功登录的结果。
type LoginResult struct {
	SessionToken string
	CSRFToken    string
	ExpiresAt    int64
}

// New 构造认证服务并加载运行期配置。
func New(ctx context.Context, db *database.DB, cfg *config.Config) (*Service, error) {
	master := cfg.EncryptionKeys[cfg.CurrentKeyVersion]
	s := &Service{
		db:  db,
		cfg: cfg,
		// 三个派生用途互不复用，且都不参与凭据解密。
		sourceHMACKey: cryptoutil.DeriveKey(master, cryptoutil.PurposeSourceFingerprint),
		csrfHMACKey:   cryptoutil.DeriveKey(master, cryptoutil.PurposeCSRF),
		rawHMACKey:    cryptoutil.DeriveKey(master, cryptoutil.PurposeRawFingerprint),
		slot:          make(chan struct{}, 1),
		bucket:        newTokenBucket(verifyRatePerSecond, verifyBurst),
	}
	if err := s.Reload(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// Reload 从 settings 重新读取运行期可调项（库值优先于环境变量）。
func (s *Service) Reload(ctx context.Context) error {
	maxAttempt := s.cfg.LoginMaxAttempt
	lockFor := s.cfg.LoginLockFor
	ttl := s.cfg.SessionTTL

	if v, ok, err := database.GetSetting(ctx, s.db, "login_max_attempts"); err != nil {
		return err
	} else if ok {
		if n, e := strconv.Atoi(v); e == nil && n > 0 {
			maxAttempt = n
		}
	}
	if v, ok, err := database.GetSetting(ctx, s.db, "login_lock_minutes"); err != nil {
		return err
	} else if ok {
		if n, e := strconv.Atoi(v); e == nil && n > 0 {
			lockFor = time.Duration(n) * time.Minute
		}
	}
	if v, ok, err := database.GetSetting(ctx, s.db, "session_ttl_hours"); err != nil {
		return err
	} else if ok {
		if n, e := strconv.Atoi(v); e == nil && n > 0 {
			ttl = time.Duration(n) * time.Hour
		}
	}

	s.mu.Lock()
	s.maxAttempt, s.lockFor, s.ttl = maxAttempt, lockFor, ttl
	s.mu.Unlock()
	return nil
}

func (s *Service) limits() (maxAttempt int, lockFor, ttl time.Duration) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.maxAttempt, s.lockFor, s.ttl
}

// HMACSource 返回来源指纹（供审计引用）。
func (s *Service) HMACSource(ip string) string {
	return cryptoutil.HMACHex(s.sourceHMACKey, []byte(ip))
}

// RawFingerprint 返回导入原文的 HMAC 指纹。避免无密钥离线猜测原文。
func (s *Service) RawFingerprint(raw []byte) string {
	return cryptoutil.HMACHex(s.rawHMACKey, raw)
}

// CSRFFor 为某个会话 token 生成绑定的 CSRF 值。
func (s *Service) CSRFFor(sessionToken string) string {
	return cryptoutil.HMACHex(s.csrfHMACKey, []byte(sessionToken))
}

// VerifyCSRF 常量时间比较 header、cookie 与服务端计算值。
func (s *Service) VerifyCSRF(sessionToken, headerVal, cookieVal string) bool {
	if sessionToken == "" || headerVal == "" || cookieVal == "" {
		return false
	}
	want := s.CSRFFor(sessionToken)
	return cryptoutil.ConstantTimeEqualString(headerVal, want) &&
		cryptoutil.ConstantTimeEqualString(cookieVal, want)
}

func (s *Service) acquireSlot() bool {
	select {
	case s.slot <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *Service) releaseSlot() { <-s.slot }

// Login 校验管理员密码并创建会话。
func (s *Service) Login(ctx context.Context, password, ip, ua string) (*LoginResult, error) {
	now := time.Now()
	sk := s.SourceKey(ip)

	// 1) 来源锁定：直接 429，不做 Argon2，也不刷新锁定截止时间。
	remain, err := s.LockRemaining(ctx, ip)
	if err != nil {
		return nil, model.WrapError(500, "internal", "读取登录状态失败", err)
	}
	if remain > 0 {
		return nil, model.ErrRateLimitedRetry("source_locked",
			fmt.Sprintf("当前来源登录尝试次数过多，请 %d 分钟后再次尝试。", ceilMinutes(remain)), remain)
	}

	// 2) 全局频率保护。
	if !s.bucket.allow(now) {
		return nil, model.ErrRateLimitedRetry("rate_limited", "请求过于频繁，请稍后再试。", s.bucket.retryAfter())
	}

	// 3) 并发槽：繁忙立即 429，不排队。
	if !s.acquireSlot() {
		return nil, model.ErrRateLimitedRetry("verify_busy", "正在校验其他登录请求，请稍后再试。", 2)
	}
	ok, verifyErr := cryptoutil.VerifyPassword(password, s.cfg.AdminPasswordHash)
	s.releaseSlot()
	if verifyErr != nil {
		return nil, model.WrapError(500, "internal", "密码校验失败", verifyErr)
	}

	// 4) 失败路径。
	if !ok {
		locked, remaining, err := s.recordFailure(ctx, sk, now)
		if err != nil {
			return nil, model.WrapError(500, "internal", "记录登录失败状态失败", err)
		}
		_ = audit.Write(ctx, s.db, audit.Entry{
			Action: audit.ActionLoginFailed, TargetType: audit.TargetSystem, IP: ip,
		})
		if locked {
			_, lockFor, _ := s.limits()
			return nil, model.ErrRateLimitedRetry("source_locked",
				fmt.Sprintf("当前来源登录尝试次数过多，请 %d 分钟后再次尝试。", ceilMinutes(int(lockFor.Seconds()))),
				int(lockFor.Seconds()))
		}
		return nil, &model.Error{
			Status:  401,
			Code:    "bad_credentials",
			Message: fmt.Sprintf("密码错误。剩余尝试次数：%d", remaining),
		}
	}

	// 5) 成功路径。
	if err := s.clearFailures(ctx, sk); err != nil {
		return nil, model.WrapError(500, "internal", "清除登录失败状态失败", err)
	}
	_, _, ttl := s.limits()
	token, err := cryptoutil.RandomHex(SessionTokenBytes)
	if err != nil {
		return nil, model.WrapError(500, "internal", "生成会话标识失败", err)
	}
	expiresAt := now.Add(ttl).Unix()
	if err := s.insertSession(ctx, token, ip, ua, expiresAt); err != nil {
		return nil, model.WrapError(500, "internal", "创建会话失败", err)
	}
	_ = audit.Write(ctx, s.db, audit.Entry{
		Action: audit.ActionLoginSuccess, TargetType: audit.TargetSystem, IP: ip,
	})
	return &LoginResult{SessionToken: token, CSRFToken: s.CSRFFor(token), ExpiresAt: expiresAt}, nil
}

// Authenticate 校验会话 token 并顺带刷新活跃时间。
func (s *Service) Authenticate(ctx context.Context, token string) (*Session, error) {
	sess, err := s.sessionByToken(ctx, token)
	if err != nil {
		return nil, model.WrapError(500, "internal", "校验会话失败", err)
	}
	if sess == nil {
		return nil, model.ErrUnauthorized("session_invalid", "会话无效或已过期，请重新登录")
	}
	_ = s.touchSession(ctx, sess) // 刷新失败不影响本次请求
	return sess, nil
}

// VerifyAdminPassword 用于导出/备份下载等需要重新认证的高风险操作。
// 与登录共用频率与并发预算，但不影响来源失败计数。
func (s *Service) VerifyAdminPassword(ctx context.Context, password string) error {
	now := time.Now()
	if !s.bucket.allow(now) {
		return model.ErrRateLimitedRetry("rate_limited", "请求过于频繁，请稍后再试。", s.bucket.retryAfter())
	}
	if !s.acquireSlot() {
		return model.ErrRateLimitedRetry("verify_busy", "正在校验其他请求，请稍后再试。", 2)
	}
	ok, err := cryptoutil.VerifyPassword(password, s.cfg.AdminPasswordHash)
	s.releaseSlot()
	if err != nil {
		return model.WrapError(500, "internal", "密码校验失败", err)
	}
	if !ok {
		return model.NewError(401, "bad_credentials", "管理员密码不正确")
	}
	return nil
}

// SyncAdminHashFingerprint 在启动时比较管理员 hash 指纹；
// 变化时在事务内撤销全部会话并更新指纹。
func (s *Service) SyncAdminHashFingerprint(ctx context.Context) error {
	fp := cryptoutil.SHA256Hex([]byte(s.cfg.AdminPasswordHash))
	if fp == "" || s.cfg.AdminPasswordHash == "" {
		return nil // 开发模式允许未配置
	}
	return s.db.Tx(ctx, func(tx *sql.Tx) error {
		var cur sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'admin_hash_fingerprint'`).Scan(&cur)
		if err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("读取管理员指纹失败：%w", err)
		}
		if cur.Valid && cur.String == fp {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions`); err != nil {
			return fmt.Errorf("撤销全部会话失败：%w", err)
		}
		if _, err := tx.ExecContext(ctx, `
            INSERT INTO settings (key, value, updated_at, updated_by) VALUES ('admin_hash_fingerprint', ?, ?, 'startup')
            ON CONFLICT(key) DO UPDATE SET value = excluded.value,
                                           updated_at = excluded.updated_at,
                                           updated_by = excluded.updated_by`,
			fp, time.Now().Unix()); err != nil {
			return fmt.Errorf("更新管理员指纹失败：%w", err)
		}
		return nil
	})
}

func ceilMinutes(seconds int) int {
	if seconds <= 0 {
		return 1
	}
	return (seconds + 59) / 60
}
