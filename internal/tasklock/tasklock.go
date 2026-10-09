// Package tasklock 提供进程内的重任务互斥锁。
//
// 导入解析、导入提交与备份共用一个槽位：同时只允许一个重任务，
// 其余请求立即返回繁忙，不静默排长队（doc §14.2 / §20）。
package tasklock

// Lock 是一个非阻塞的互斥锁。
type Lock struct {
	ch chan struct{}
}

// New 构造互斥锁。
func New() *Lock {
	return &Lock{ch: make(chan struct{}, 1)}
}

// Try 尝试获取锁；已被占用时返回 false，不阻塞。
func (l *Lock) Try() bool {
	select {
	case l.ch <- struct{}{}:
		return true
	default:
		return false
	}
}

// Release 释放锁。
func (l *Lock) Release() {
	select {
	case <-l.ch:
	default:
	}
}
