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

// Creates a new quota object
func NewQuota(total, used int64) *Quota {
	q := &Quota{
		total: total,
		used:  used,
	}
	q.calculateRemain()
	return q
}

// Calculate remain quota
func (q *Quota) calculateRemain() {
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
	if used > q.total {
		used = q.total
	}
	q.used = used
	q.calculateRemain()
}

//
func (q *Quota) AddUsage(delta int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.used += delta
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
