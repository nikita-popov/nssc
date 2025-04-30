package fs

import (
	"sync"
)

// Represents the FS quota
type Quota struct {
	total  int64
	used   int64
	remain int64
	mu     sync.Mutex
}

// Creates a new quota
func NewQuota(total int64) *Quota {
	return &Quota{
		total:  total,
		used:   0,
		remain: total,
	}
}

// Return remain quota
func (q *Quota) Remain() int64 {
	return q.remain
}

// Return remain quota
func (q *Quota) Used() int64 {
	return q.used
}

// Calculate remain quota
func (q *Quota) calculateRemain() {
	if q.total == 0 {
		return
	}
	q.remain = q.total - q.used
	if q.remain < 0 {
		q.remain = 0
	}
}

// Update total size
func (q *Quota) SetTotal(total int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.total = total
	if q.used > q.total {
		q.used = q.total
	}
	q.calculateRemain()
}

// Update used size
func (q *Quota) SetUsed(used int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.total == 0 {
		q.used = used
		return
	}
	if used > q.total {
		used = q.total
	}
	q.used = used
	q.calculateRemain()
}

func (q *Quota) AddUsage(delta int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.used += delta
	if q.total == 0 {
		return
	}
	if q.used > q.total {
		q.used = q.total
	}
	q.calculateRemain()
}

// Return current quota data
func (q *Quota) Values() (total, used, remain int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.total, q.used, q.remain
}
