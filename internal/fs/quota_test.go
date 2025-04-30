package fs_test

import (
	"sync"
	"testing"

	"nssc/internal/fs"
)

func TestQuota(t *testing.T) {
	q := fs.NewQuota(1000)

	if q.Remain() != 1000 {
		t.Errorf("Expected 1000, got %d", q.Remain())
	}

	q.AddUsage(300)
	if q.Used() != 300 {
		t.Errorf("Expected 300, получено %d", q.Used())
	}
	if q.Remain() != 700 {
		t.Errorf("Expected 700, получено %d", q.Remain())
	}

	total, used, remain := q.Values()
	if total != 1000 {
		t.Errorf("Expected 1000, got %d", q.Used())
	}
	if used != 300 {
		t.Errorf("Expected 300, got %d", q.Used())
	}
	if remain != 700 {
		t.Errorf("Expected 700, got %d", q.Used())
	}
}

func TestQuotaConcurrentAccess(t *testing.T) {
	q := fs.NewQuota(1000000)
	var wg sync.WaitGroup

	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func() {
			q.AddUsage(100)
			wg.Done()
		}()
	}

	wg.Wait()
	if q.Used() != 100000 {
		t.Errorf("Concurent access failed")
	}
}
