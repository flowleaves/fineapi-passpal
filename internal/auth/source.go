package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"passpal/internal/audit"
	"passpal/internal/model"
)

// maxSources 是来源记录的硬上限。达到上限时对新来源返回 429，
// 而不是淘汰仍在锁定的来源。
const maxSources = 4096

type sourceState struct {
	SourceKey       string
	WindowStartedAt int64
	FailedAttempts  int
	LockedUntil     sql.NullInt64
	LastFailedAt    sql.NullInt64
	ExpiresAt       int64
}

// SourceKey 把已验证的来源 IP 变成不可逆的存储键。
func (s *Service) SourceKey(ip string) string {
	return s.HMACSource(ip)
}

func (s *Service) loadSourceState(ctx context.Context, sk string) (*sourceState, error) {
	var st sourceState
	err := s.db.QueryRowContext(ctx, `
        SELECT source_key, window_started_at, failed_attempts, locked_until, last_failed_at, expires_at
        FROM auth_state WHERE source_key = ?`, sk).
		Scan(&st.SourceKey, &st.WindowStartedAt, &st.FailedAttempts, &st.LockedUntil, &st.LastFailedAt, &st.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取来源锁定状态失败：%w", err)
	}
	return &st, nil
}

// LockRemaining 返回该来源还需锁定的秒数；未锁定返回 0。
func (s *Service) LockRemaining(ctx context.Context, ip string) (int, error) {
	st, err := s.loadSourceState(ctx, s.SourceKey(ip))
	if err != nil || st == nil {
		return 0, err
	}
	now := time.Now().Unix()
	if st.LockedUntil.Valid && st.LockedUntil.Int64 > now {
		return int(st.LockedUntil.Int64 - now), nil
	}
	return 0, nil
}

// recordFailure 在事务内累加当前来源的失败计数，必要时锁定。
// 返回是否已锁定与该来源的剩余可尝试次数。
func (s *Service) recordFailure(ctx context.Context, sk string, now time.Time) (locked bool, remaining int, err error) {
	maxAttempt, lockFor, _ := s.limits()
	nowUnix := now.Unix()
	windowSecs := int64(lockFor.Seconds())

	txErr := s.db.Tx(ctx, func(tx *sql.Tx) error {
		var st sourceState
		scanErr := tx.QueryRowContext(ctx, `
            SELECT source_key, window_started_at, failed_attempts, locked_until, last_failed_at, expires_at
            FROM auth_state WHERE source_key = ?`, sk).
			Scan(&st.SourceKey, &st.WindowStartedAt, &st.FailedAttempts, &st.LockedUntil, &st.LastFailedAt, &st.ExpiresAt)

		if errors.Is(scanErr, sql.ErrNoRows) {
			// 新来源：容量检查，避免伪造来源无界增长。
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM auth_state`).Scan(&n); err != nil {
				return err
			}
			if n >= maxSources {
				return model.ErrRateLimited("source_capacity", "来源记录已达上限，请稍后再试")
			}
			st = sourceState{SourceKey: sk, WindowStartedAt: nowUnix}
		} else if scanErr != nil {
			return scanErr
		}

		// 窗口过期则重新开始计数。
		if nowUnix-st.WindowStartedAt >= windowSecs {
			st.WindowStartedAt = nowUnix
			st.FailedAttempts = 0
			st.LockedUntil = sql.NullInt64{}
		}

		st.FailedAttempts++
		st.LastFailedAt = sql.NullInt64{Int64: nowUnix, Valid: true}

		expires := st.WindowStartedAt + windowSecs
		if st.FailedAttempts >= maxAttempt {
			lockUntil := nowUnix + windowSecs
			// 锁定期间不延长既有截止时间。
			if !st.LockedUntil.Valid || st.LockedUntil.Int64 < lockUntil {
				st.LockedUntil = sql.NullInt64{Int64: lockUntil, Valid: true}
			}
			locked = true
			expires = st.LockedUntil.Int64
		} else {
			remaining = maxAttempt - st.FailedAttempts
		}
		if st.LockedUntil.Valid && st.LockedUntil.Int64 > expires {
			expires = st.LockedUntil.Int64
		}
		st.ExpiresAt = expires

		_, err := tx.ExecContext(ctx, `
            INSERT INTO auth_state (source_key, window_started_at, failed_attempts, locked_until,
                                    last_failed_at, expires_at, updated_at)
            VALUES (?, ?, ?, ?, ?, ?, ?)
            ON CONFLICT(source_key) DO UPDATE SET
                window_started_at = excluded.window_started_at,
                failed_attempts   = excluded.failed_attempts,
                locked_until      = excluded.locked_until,
                last_failed_at    = excluded.last_failed_at,
                expires_at        = excluded.expires_at,
                updated_at        = excluded.updated_at`,
			st.SourceKey, st.WindowStartedAt, st.FailedAttempts, st.LockedUntil,
			st.LastFailedAt, st.ExpiresAt, nowUnix)
		return err
	})
	if txErr != nil {
		return false, 0, txErr
	}
	return locked, remaining, nil
}

// clearFailures 登录成功后仅清除当前来源的失败状态。
func (s *Service) clearFailures(ctx context.Context, sk string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM auth_state WHERE source_key = ?`, sk)
	if err != nil {
		return fmt.Errorf("清除来源失败状态失败：%w", err)
	}
	return nil
}

// UnlockSource 解除某个来源的锁定（本地 CLI 使用）。
// 返回该来源此前是否存在。
func (s *Service) UnlockSource(ctx context.Context, ip string) (bool, error) {
	sk := s.SourceKey(ip)
	res, err := s.db.ExecContext(ctx, `DELETE FROM auth_state WHERE source_key = ?`, sk)
	if err != nil {
		return false, fmt.Errorf("解除来源锁定失败：%w", err)
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		if err := audit.Write(ctx, s.db, audit.Entry{
			Action:     audit.ActionAuthUnlock,
			TargetType: audit.TargetSystem,
			TargetRef:  sk[:16], // 只留指纹前缀，不写明文 IP
			IP:         ip,
		}); err != nil {
			return true, err
		}
	}
	return n > 0, nil
}
