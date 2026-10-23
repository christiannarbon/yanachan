package kv_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/christiannarbon/yanachan-4k/backend/internal/kv"
	"github.com/christiannarbon/yanachan-4k/backend/internal/kv/kvtest"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestMemoryConformance(t *testing.T) {
	c := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	kvtest.Run(t, func(t *testing.T) kv.Store { return kv.NewMemory(c.Now) }, c)
}

func TestMemoryConcurrent(t *testing.T) {
	m := kv.NewMemory(nil)
	ctx := context.Background()
	var holders, maxHolders, acquired atomic.Int32
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			me := []byte(fmt.Sprint(i))
			for range 200 {
				ok, err := m.SetNX(ctx, "lock:refresh:test", me, time.Minute)
				if err != nil {
					t.Error(err)
					return
				}
				if !ok {
					continue
				}
				acquired.Add(1)
				n := holders.Add(1)
				for {
					old := maxHolders.Load()
					if n <= old || maxHolders.CompareAndSwap(old, n) {
						break
					}
				}
				holders.Add(-1)
				released, err := m.DeleteIfEqual(ctx, "lock:refresh:test", me)
				if err != nil || !released {
					t.Errorf("release = %v, %v; want true, nil", released, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if got := maxHolders.Load(); got != 1 {
		t.Errorf("at most %d goroutines held the lock at once, want 1", got)
	}
	if acquired.Load() == 0 {
		t.Error("nobody ever took the lock")
	}
}
