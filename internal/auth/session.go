package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"passpal/internal/cryptoutil"
)

// Session 是一条服务端会话记录。库中存的是 token 的 SHA-256，不存原文。
type Session struct {
	ID         string // SHA-256(cookie token) 的 hex
	CreatedAt  int64
	ExpiresAt  int64
	LastSeenAt int64
	IP         string
	UserAgent  string
}

// touchInterval 限制 last_seen_at 的写入频率，避免写放大。
const touchInterval = 5 * time.Minute

func tokenHash(token string) string {
	return cryptoutil.SHA256Hex([]byte(token))
}

func (s *Service) insertSession(ctx context.Context, token, ip, ua string, expiresAt int64) error {
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO sessions (id, created_at, expires_at, last_seen_at, ip, user_agent)
        VALUES (?, ?, ?, ?, ?, ?)`,
		tokenHash(token), now, expiresAt, now, nullIfEmpty(ip), nullIfEmpty(ua))
	if err != nil {
		return fmt.Errorf("创建会话失败：%w", err)
	}
	return nil
}

// sessionByToken 按 token 查询未过期会话；不存在或已过期返回 nil。
func (s *Service) sessionByToken(ctx context.Context, token string) (*Session, error) {
	if token == "" {
		return nil, nil
	}
	var (
		sess      Session
		ip, ua    sql.NullString
		expiresAt int64
	)
	err := s.db.QueryRowContext(ctx, `
        SELECT id, created_at, expires_at, last_seen_at, ip, user_agent
        FROM sessions WHERE id = ?`, tokenHash(token)).
		Scan(&sess.ID, &sess.CreatedAt, &expiresAt, &sess.LastSeenAt, &ip, &ua)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("查询会话失败：%w", err)
	}
	sess.ExpiresAt = expiresAt
	sess.IP = ip.String
	sess.UserAgent = ua.String
	if sess.ExpiresAt <= time.Now().Unix() {
		// 过期即清理，避免惰性数据堆积。
		_ = s.deleteSessionByID(ctx, sess.ID)
		return nil, nil
	}
	return &sess, nil
}

// touchSession 在超过 touchInterval 后刷新 last_seen_at。
func (s *Service) touchSession(ctx context.Context, sess *Session) error {
	now := time.Now().Unix()
	if now-sess.LastSeenAt < int64(touchInterval.Seconds()) {
		return nil
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET last_seen_at = ? WHERE id = ?`, now, sess.ID); err != nil {
		return fmt.Errorf("刷新会话活跃时间失败：%w", err)
	}
	sess.LastSeenAt = now
	return nil
}

func (s *Service) deleteSessionByID(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	return err
}

// DeleteSession 按 cookie token 登出。
func (s *Service) DeleteSession(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, tokenHash(token))
	if err != nil {
		return fmt.Errorf("删除会话失败：%w", err)
	}
	return nil
}

// DeleteAllSessions 撤销全部会话（管理员密码变更时使用）。
func (s *Service) DeleteAllSessions(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions`)
	if err != nil {
		return 0, fmt.Errorf("撤销全部会话失败：%w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// CleanupSessions 清理过期会话与过期来源记录。
func (s *Service) CleanupSessions(ctx context.Context) error {
	now := time.Now().Unix()
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, now); err != nil {
		return fmt.Errorf("清理过期会话失败：%w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM auth_state WHERE expires_at <= ?`, now); err != nil {
		return fmt.Errorf("清理过期来源记录失败：%w", err)
	}
	return nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
