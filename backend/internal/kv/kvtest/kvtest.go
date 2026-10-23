// Package kvtest is the conformance suite every kv.Store must pass.
package kvtest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/christiannarbon/yanachan-4k/backend/internal/kv"
)

// Clock lets the suite move time. Implementations that cannot control time
// (a real Redis) pass nil, and the TTL cases fall back to short real sleeps.
type Clock interface{ Advance(d time.Duration) }

const ttl = 1500 * time.Millisecond

type suite struct {
	newStore func(t *testing.T) kv.Store
	clock    Clock
	prefix   string
}

func (s *suite) advance(d time.Duration) {
	if s.clock == nil {
		time.Sleep(d)
		return
	}
	s.clock.Advance(d)
}

// Run runs every case against fresh stores from newStore. Each case uses its
// own key names, so a shared server is fine.
func Run(t *testing.T, newStore func(t *testing.T) kv.Store, clock Clock) {
	s := &suite{
		newStore: newStore,
		clock:    clock,
		prefix:   fmt.Sprintf("kvtest:%d:", rand.Int64()),
	}
	cases := []struct {
		name string
		fn   func(t *testing.T, st kv.Store, key func(string) string)
	}{
		{"GetMissing", s.getMissing},
		{"SetGet", s.setGet},
		{"SetOverwrites", s.setOverwrites},
		{"SetCopiesValue", s.setCopiesValue},
		{"TTLExpires", s.ttlExpires},
		{"SetWithoutTTLClearsExpiry", s.setWithoutTTLClearsExpiry},
		{"SetNX", s.setNX},
		{"DeleteIfEqual", s.deleteIfEqual},
		{"DeleteManyAndMissing", s.deleteManyAndMissing},
		{"Expire", s.expire},
		{"ZSet", s.zset},
		{"ZSetTieBreak", s.zsetTieBreak},
		{"Ping", s.ping},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			key := func(name string) string { return s.prefix + c.name + ":" + name }
			c.fn(t, s.newStore(t), key)
		})
	}
}

func mustGet(t *testing.T, st kv.Store, key string) []byte {
	t.Helper()
	v, err := st.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("Get(%q): %v", key, err)
	}
	return v
}

func wantMissing(t *testing.T, st kv.Store, key string) {
	t.Helper()
	if _, err := st.Get(context.Background(), key); !errors.Is(err, kv.ErrNotFound) {
		t.Fatalf("Get(%q) err = %v, want ErrNotFound", key, err)
	}
}

func mustSet(t *testing.T, st kv.Store, key, val string, ttl time.Duration) {
	t.Helper()
	if err := st.Set(context.Background(), key, []byte(val), ttl); err != nil {
		t.Fatalf("Set(%q): %v", key, err)
	}
}

func wantRange(t *testing.T, st kv.Store, key string, max float64, limit int64, want []string) {
	t.Helper()
	got, err := st.ZRangeByScore(context.Background(), key, max, limit)
	if err != nil {
		t.Fatalf("ZRangeByScore(%q): %v", key, err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("ZRangeByScore(%q, %v, %d) = %q, want %q", key, max, limit, got, want)
	}
}

func (s *suite) getMissing(t *testing.T, st kv.Store, key func(string) string) {
	wantMissing(t, st, key("a"))
}

func (s *suite) setGet(t *testing.T, st kv.Store, key func(string) string) {
	mustSet(t, st, key("a"), "one", 0)
	if got := mustGet(t, st, key("a")); string(got) != "one" {
		t.Fatalf("got %q, want one", got)
	}
}

func (s *suite) setOverwrites(t *testing.T, st kv.Store, key func(string) string) {
	mustSet(t, st, key("a"), "one", 0)
	mustSet(t, st, key("a"), "two", 0)
	if got := mustGet(t, st, key("a")); string(got) != "two" {
		t.Fatalf("got %q, want two", got)
	}
}

func (s *suite) setCopiesValue(t *testing.T, st kv.Store, key func(string) string) {
	in := []byte("one")
	if err := st.Set(context.Background(), key("a"), in, 0); err != nil {
		t.Fatal(err)
	}
	in[0] = 'X'
	out := mustGet(t, st, key("a"))
	out[0] = 'Y'
	if got := mustGet(t, st, key("a")); !bytes.Equal(got, []byte("one")) {
		t.Fatalf("got %q, want one", got)
	}
}

func (s *suite) ttlExpires(t *testing.T, st kv.Store, key func(string) string) {
	mustSet(t, st, key("a"), "one", ttl)
	mustGet(t, st, key("a"))
	s.advance(ttl + 500*time.Millisecond)
	wantMissing(t, st, key("a"))
}

func (s *suite) setWithoutTTLClearsExpiry(t *testing.T, st kv.Store, key func(string) string) {
	mustSet(t, st, key("a"), "one", ttl)
	mustSet(t, st, key("a"), "two", 0)
	s.advance(ttl + 500*time.Millisecond)
	if got := mustGet(t, st, key("a")); string(got) != "two" {
		t.Fatalf("got %q, want two", got)
	}
}

func (s *suite) setNX(t *testing.T, st kv.Store, key func(string) string) {
	ctx := context.Background()
	ok, err := st.SetNX(ctx, key("lock"), []byte("first"), ttl)
	if err != nil || !ok {
		t.Fatalf("first SetNX = %v, %v; want true, nil", ok, err)
	}
	ok, err = st.SetNX(ctx, key("lock"), []byte("second"), ttl)
	if err != nil || ok {
		t.Fatalf("second SetNX = %v, %v; want false, nil", ok, err)
	}
	if got := mustGet(t, st, key("lock")); string(got) != "first" {
		t.Fatalf("got %q, want first", got)
	}
	s.advance(ttl + 500*time.Millisecond)
	ok, err = st.SetNX(ctx, key("lock"), []byte("third"), ttl)
	if err != nil || !ok {
		t.Fatalf("SetNX after expiry = %v, %v; want true, nil", ok, err)
	}
}

func (s *suite) deleteIfEqual(t *testing.T, st kv.Store, key func(string) string) {
	ctx := context.Background()
	mustSet(t, st, key("lock"), "mine", 0)
	ok, err := st.DeleteIfEqual(ctx, key("lock"), []byte("theirs"))
	if err != nil || ok {
		t.Fatalf("DeleteIfEqual(wrong) = %v, %v; want false, nil", ok, err)
	}
	mustGet(t, st, key("lock"))
	ok, err = st.DeleteIfEqual(ctx, key("lock"), []byte("mine"))
	if err != nil || !ok {
		t.Fatalf("DeleteIfEqual(right) = %v, %v; want true, nil", ok, err)
	}
	wantMissing(t, st, key("lock"))
	ok, err = st.DeleteIfEqual(ctx, key("missing"), []byte("mine"))
	if err != nil || ok {
		t.Fatalf("DeleteIfEqual(missing) = %v, %v; want false, nil", ok, err)
	}
}

func (s *suite) deleteManyAndMissing(t *testing.T, st kv.Store, key func(string) string) {
	mustSet(t, st, key("a"), "one", 0)
	mustSet(t, st, key("b"), "two", 0)
	if err := st.Delete(context.Background(), key("a"), key("b"), key("missing")); err != nil {
		t.Fatal(err)
	}
	wantMissing(t, st, key("a"))
	wantMissing(t, st, key("b"))
}

func (s *suite) expire(t *testing.T, st kv.Store, key func(string) string) {
	ctx := context.Background()
	mustSet(t, st, key("a"), "one", ttl)
	if err := st.Expire(ctx, key("a"), 3*ttl); err != nil {
		t.Fatal(err)
	}
	s.advance(ttl + 500*time.Millisecond)
	mustGet(t, st, key("a"))

	if err := st.Expire(ctx, key("missing"), ttl); !errors.Is(err, kv.ErrNotFound) {
		t.Fatalf("Expire(missing) err = %v, want ErrNotFound", err)
	}

	mustSet(t, st, key("b"), "two", 0)
	if err := st.Expire(ctx, key("b"), 0); err != nil {
		t.Fatal(err)
	}
	wantMissing(t, st, key("b"))
}

func (s *suite) zset(t *testing.T, st kv.Store, key func(string) string) {
	ctx := context.Background()
	z := key("z")
	for _, m := range []struct {
		score  float64
		member string
	}{{3, "s3"}, {1, "s1"}, {2, "s2"}} {
		if err := st.ZAdd(ctx, z, m.score, m.member); err != nil {
			t.Fatal(err)
		}
	}
	wantRange(t, st, z, 2, 0, []string{"s1", "s2"})
	wantRange(t, st, z, 2, 1, []string{"s1"})

	if err := st.ZAdd(ctx, z, 10, "s1"); err != nil {
		t.Fatal(err)
	}
	wantRange(t, st, z, 2, 0, []string{"s2"})

	if err := st.ZRem(ctx, z, "s2"); err != nil {
		t.Fatal(err)
	}
	wantRange(t, st, z, 100, 0, []string{"s3", "s1"})

	wantRange(t, st, key("missing"), 100, 0, nil)
}

func (s *suite) zsetTieBreak(t *testing.T, st kv.Store, key func(string) string) {
	ctx := context.Background()
	z := key("z")
	for _, m := range []string{"b", "c", "a"} {
		if err := st.ZAdd(ctx, z, 1, m); err != nil {
			t.Fatal(err)
		}
	}
	wantRange(t, st, z, 1, 0, []string{"a", "b", "c"})
}

func (s *suite) ping(t *testing.T, st kv.Store, _ func(string) string) {
	if err := st.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}
