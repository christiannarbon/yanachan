package kv

import (
	"bytes"
	"context"
	"sort"
	"sync"
	"time"
)

// Memory is a Store held in process memory. It is for tests and for trying
// server mode without a Redis; state is lost when the process exits and is
// not shared between replicas.
type Memory struct {
	mu   sync.Mutex
	now  func() time.Time
	vals map[string]entry
	sets map[string]map[string]float64
}

type entry struct {
	val       []byte
	expiresAt time.Time // zero = never
}

var _ Store = (*Memory)(nil)

// NewMemory uses time.Now when now is nil.
func NewMemory(now func() time.Time) *Memory {
	if now == nil {
		now = time.Now
	}
	return &Memory{
		now:  now,
		vals: map[string]entry{},
		sets: map[string]map[string]float64{},
	}
}

// live drops key if it has expired. Callers hold m.mu.
func (m *Memory) live(key string) (entry, bool) {
	e, ok := m.vals[key]
	if !ok {
		return entry{}, false
	}
	if !e.expiresAt.IsZero() && !m.now().Before(e.expiresAt) {
		delete(m.vals, key)
		return entry{}, false
	}
	return e, true
}

func (m *Memory) expiry(ttl time.Duration) time.Time {
	if ttl <= 0 {
		return time.Time{}
	}
	return m.now().Add(ttl)
}

func (m *Memory) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.live(key)
	if !ok {
		return nil, ErrNotFound
	}
	return bytes.Clone(e.val), nil
}

func (m *Memory) Set(_ context.Context, key string, val []byte, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.vals[key] = entry{val: bytes.Clone(val), expiresAt: m.expiry(ttl)}
	return nil
}

func (m *Memory) SetNX(_ context.Context, key string, val []byte, ttl time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.live(key); ok {
		return false, nil
	}
	m.vals[key] = entry{val: bytes.Clone(val), expiresAt: m.expiry(ttl)}
	return true, nil
}

func (m *Memory) DeleteIfEqual(_ context.Context, key string, val []byte) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.live(key)
	if !ok || !bytes.Equal(e.val, val) {
		return false, nil
	}
	delete(m.vals, key)
	return true, nil
}

func (m *Memory) Delete(_ context.Context, keys ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range keys {
		delete(m.vals, k)
		delete(m.sets, k)
	}
	return nil
}

func (m *Memory) Expire(_ context.Context, key string, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.live(key)
	if !ok {
		return ErrNotFound
	}
	if ttl <= 0 {
		delete(m.vals, key)
		return nil
	}
	e.expiresAt = m.now().Add(ttl)
	m.vals[key] = e
	return nil
}

func (m *Memory) ZAdd(_ context.Context, key string, score float64, member string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	set, ok := m.sets[key]
	if !ok {
		set = map[string]float64{}
		m.sets[key] = set
	}
	set[member] = score
	return nil
}

func (m *Memory) ZRangeByScore(_ context.Context, key string, max float64, limit int64) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	type scored struct {
		member string
		score  float64
	}
	var in []scored
	for member, score := range m.sets[key] {
		if score <= max {
			in = append(in, scored{member, score})
		}
	}
	sort.Slice(in, func(i, j int) bool {
		if in[i].score != in[j].score {
			return in[i].score < in[j].score
		}
		return in[i].member < in[j].member
	})
	if limit > 0 && int64(len(in)) > limit {
		in = in[:limit]
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = s.member
	}
	return out, nil
}

func (m *Memory) ZRem(_ context.Context, key string, members ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	set := m.sets[key]
	for _, member := range members {
		delete(set, member)
	}
	if len(set) == 0 {
		delete(m.sets, key)
	}
	return nil
}

func (m *Memory) Ping(context.Context) error { return nil }

func (m *Memory) Close() error { return nil }
