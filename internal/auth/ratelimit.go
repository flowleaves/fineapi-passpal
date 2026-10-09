package auth

import (
	"sync"
	"time"
)

// tokenBucket 是固定容量的令牌桶，用于限制 Argon2 校验的发起频率。
//
// 不引 Redis；容量固定、状态有界，不会因伪造来源无界增长。
type tokenBucket struct {
	mu     sync.Mutex
	tokens float64
	last   time.Time
	rate   float64 // 每秒补充令牌数
	burst  float64 // 桶容量
}

func newTokenBucket(ratePerSecond, burst float64) *tokenBucket {
	return &tokenBucket{tokens: burst, last: time.Now(), rate: ratePerSecond, burst: burst}
}

// allow 消耗一个令牌；不足则返回 false（不排队、不等待）。
func (b *tokenBucket) allow(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	if now.Before(b.last) { // 时钟回拨保护
		b.last = now
	}
	elapsed := now.Sub(b.last).Seconds()
	b.tokens += elapsed * b.rate
	if b.tokens > b.burst {
		b.tokens = b.burst
	}
	b.last = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// retryAfter 返回补满一个令牌所需的秒数（向上取整，至少 1 秒）。
func (b *tokenBucket) retryAfter() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.rate <= 0 {
		return 1
	}
	need := 1 - b.tokens
	if need <= 0 {
		return 1
	}
	secs := int(need/b.rate) + 1
	if secs < 1 {
		secs = 1
	}
	return secs
}
