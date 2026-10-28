package session

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/christiannarbon/yanachan-4k/backend/internal/kv"
	"github.com/christiannarbon/yanachan-4k/backend/internal/seal"
)

const (
	testIdle = 2 * time.Hour
	testMax  = 24 * time.Hour
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return k
}

func newRing(t *testing.T, keys ...[]byte) *seal.Keyring {
	t.Helper()
	r, err := seal.NewKeyring(keys)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

type fixture struct {
	c   *clock
	mem *kv.Memory
	s   *Store
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	c := &clock{t: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)}
	mem := kv.NewMemory(c.now)
	return &fixture{c: c, mem: mem, s: newStore(mem, newRing(t, newKey(t)), c)}
}

func newStore(store kv.Store, ring *seal.Keyring, c *clock) *Store {
	s := New(store, ring, testIdle, testMax)
	s.now = c.now
	return s
}

func sampleRecord() Record {
	return Record{
		UserID:       42,
		Login:        "octocat",
		Token:        "gho_access_token_value",
		RefreshToken: "ghr_refresh_token_value",
	}
}

func mustCreate(t *testing.T, s *Store, rec Record) string {
	t.Helper()
	id, err := s.Create(context.Background(), rec)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func storedLastSeen(t *testing.T, f *fixture, id string) time.Time {
	t.Helper()
	rec, err := f.s.LoadForRevocation(context.Background(), KeyHash(id))
	if err != nil {
		t.Fatal(err)
	}
	return rec.LastSeenAt
}

func indexed(t *testing.T, store kv.Store, max time.Time) []string {
	t.Helper()
	got, err := store.ZRangeByScore(context.Background(), expiryIndex, float64(max.Unix()), 0)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestCreateGetRoundTrip(t *testing.T) {
	f := newFixture(t)
	now := f.c.t
	rec := sampleRecord()
	rec.TokenExpiresAt = now.Add(8 * time.Hour)
	rec.RefreshExpiresAt = now.Add(180 * 24 * time.Hour)
	id := mustCreate(t, f.s, rec)

	got, err := f.s.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	want := rec
	want.CreatedAt, want.LastSeenAt, want.VerifiedAt, want.TokenIssuedAt = now, now, now, now
	if got.UserID != want.UserID || got.Login != want.Login || got.Token != want.Token || got.RefreshToken != want.RefreshToken {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for _, tc := range []struct {
		name      string
		got, want time.Time
	}{
		{"TokenIssuedAt", got.TokenIssuedAt, want.TokenIssuedAt},
		{"TokenExpiresAt", got.TokenExpiresAt, want.TokenExpiresAt},
		{"RefreshExpiresAt", got.RefreshExpiresAt, want.RefreshExpiresAt},
		{"CreatedAt", got.CreatedAt, want.CreatedAt},
		{"LastSeenAt", got.LastSeenAt, want.LastSeenAt},
		{"VerifiedAt", got.VerifiedAt, want.VerifiedAt},
	} {
		if !tc.got.Equal(tc.want) {
			t.Errorf("%s = %v, want %v", tc.name, tc.got, tc.want)
		}
	}
}

func TestCreateKeepsGivenStamps(t *testing.T) {
	f := newFixture(t)
	earlier := f.c.t.Add(-time.Minute)
	rec := sampleRecord()
	rec.VerifiedAt, rec.TokenIssuedAt = earlier, earlier
	got, err := f.s.Get(context.Background(), mustCreate(t, f.s, rec))
	if err != nil {
		t.Fatal(err)
	}
	if !got.VerifiedAt.Equal(earlier) || !got.TokenIssuedAt.Equal(earlier) {
		t.Fatalf("VerifiedAt %v, TokenIssuedAt %v; want both %v", got.VerifiedAt, got.TokenIssuedAt, earlier)
	}
}

func TestIDShape(t *testing.T) {
	f := newFixture(t)
	a := mustCreate(t, f.s, sampleRecord())
	b := mustCreate(t, f.s, sampleRecord())
	for _, id := range []string{a, b} {
		if !validID(id) {
			t.Fatalf("id %d chars is not 43 URL-safe characters", len(id))
		}
	}
	if a == b {
		t.Fatal("two creates gave the same id")
	}
}

func TestStoredValuesHoldNoSecrets(t *testing.T) {
	f := newFixture(t)
	rec := sampleRecord()
	id := mustCreate(t, f.s, rec)
	k := key(KeyHash(id))
	if strings.Contains(k, id) {
		t.Fatal("the key name contains the session id")
	}
	raw, err := f.mem.Get(context.Background(), k)
	if err != nil {
		t.Fatal(err)
	}
	for name, secret := range map[string]string{"session id": id, "access token": rec.Token, "refresh token": rec.RefreshToken} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("the stored value contains the %s", name)
		}
	}
}

func TestSealedTokenBoundToKey(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a := mustCreate(t, f.s, sampleRecord())
	b := mustCreate(t, f.s, sampleRecord())
	raw, err := f.mem.Get(ctx, key(KeyHash(a)))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.mem.Set(ctx, key(KeyHash(b)), raw, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Get(ctx, b); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get with a copied value err = %v, want ErrNotFound", err)
	}
}

func TestTTLIsExpiryPlusGrace(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	k := key(KeyHash(mustCreate(t, f.s, sampleRecord())))

	f.c.t = f.c.t.Add(testIdle + RevokeGrace - time.Second)
	if _, err := f.mem.Get(ctx, k); err != nil {
		t.Fatalf("key gone before expiry plus grace: %v", err)
	}
	f.c.t = f.c.t.Add(2 * time.Second)
	if _, err := f.mem.Get(ctx, k); !errors.Is(err, kv.ErrNotFound) {
		t.Fatalf("key still there after expiry plus grace: %v", err)
	}
}

func TestIdleExpiry(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	rec := sampleRecord()
	id := mustCreate(t, f.s, rec)

	f.c.t = f.c.t.Add(testIdle + time.Second)
	if _, err := f.s.Get(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get past idle err = %v, want ErrNotFound", err)
	}
	got, err := f.s.LoadForRevocation(ctx, KeyHash(id))
	if err != nil {
		t.Fatalf("LoadForRevocation past idle: %v", err)
	}
	if got.Token != rec.Token {
		t.Fatal("LoadForRevocation did not return the token")
	}
}

func TestMaxExpiryDespiteActivity(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	id := mustCreate(t, f.s, sampleRecord())
	start := f.c.t
	for f.c.t.Sub(start) < testMax {
		f.c.t = f.c.t.Add(time.Hour)
		err := f.s.Touch(ctx, id)
		if f.c.t.Sub(start) < testMax && err != nil {
			t.Fatalf("Touch at +%v: %v", f.c.t.Sub(start), err)
		}
	}
	if _, err := f.s.Get(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get past max err = %v, want ErrNotFound", err)
	}
}

func TestTouchWritesAtMostOncePerMinute(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	id := mustCreate(t, f.s, sampleRecord())
	created := f.c.t

	f.c.t = created.Add(10 * time.Second)
	if err := f.s.Touch(ctx, id); err != nil {
		t.Fatal(err)
	}
	if got := storedLastSeen(t, f, id); !got.Equal(created) {
		t.Fatalf("LastSeenAt moved to %v after 10s", got)
	}

	f.c.t = created.Add(61 * time.Second)
	if err := f.s.Touch(ctx, id); err != nil {
		t.Fatal(err)
	}
	if got := storedLastSeen(t, f, id); !got.Equal(f.c.t) {
		t.Fatalf("LastSeenAt = %v after 61s, want %v", got, f.c.t)
	}
	if slices.Contains(indexed(t, f.mem, created.Add(testIdle)), KeyHash(id)) {
		t.Fatal("the expiry index score did not move forward")
	}
}

func TestUpdateKeepsCreatedAt(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	id := mustCreate(t, f.s, sampleRecord())
	created := f.c.t

	rec, err := f.s.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	rec.CreatedAt = created.Add(12 * time.Hour)
	rec.Token = "gho_rotated"
	if err := f.s.Update(ctx, id, rec); err != nil {
		t.Fatal(err)
	}
	got, err := f.s.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !got.CreatedAt.Equal(created) {
		t.Fatalf("CreatedAt = %v, want %v", got.CreatedAt, created)
	}
	if got.Token != "gho_rotated" {
		t.Fatal("Update did not store the new token")
	}
}

func TestUpdateMissing(t *testing.T) {
	f := newFixture(t)
	id, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.Update(context.Background(), id, sampleRecord()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Update(missing) err = %v, want ErrNotFound", err)
	}
}

func TestDeleteIsIdempotent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	id := mustCreate(t, f.s, sampleRecord())
	for _, del := range []string{id, id, "not a session id"} {
		if err := f.s.Delete(ctx, del); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.s.Get(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete err = %v, want ErrNotFound", err)
	}
	if got := indexed(t, f.mem, f.c.t.Add(testMax)); len(got) != 0 {
		t.Fatalf("index still holds %d entries", len(got))
	}
}

type countingStore struct {
	kv.Store
	calls atomic.Int64
}

func (c *countingStore) Get(ctx context.Context, key string) ([]byte, error) {
	c.calls.Add(1)
	return c.Store.Get(ctx, key)
}

func (c *countingStore) Set(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	c.calls.Add(1)
	return c.Store.Set(ctx, key, val, ttl)
}

func (c *countingStore) Delete(ctx context.Context, keys ...string) error {
	c.calls.Add(1)
	return c.Store.Delete(ctx, keys...)
}

func (c *countingStore) ZAdd(ctx context.Context, key string, score float64, member string) error {
	c.calls.Add(1)
	return c.Store.ZAdd(ctx, key, score, member)
}

func (c *countingStore) ZRem(ctx context.Context, key string, members ...string) error {
	c.calls.Add(1)
	return c.Store.ZRem(ctx, key, members...)
}

func TestInvalidIDsNeverReachTheStore(t *testing.T) {
	c := &clock{t: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)}
	counting := &countingStore{Store: kv.NewMemory(c.now)}
	s := newStore(counting, newRing(t, newKey(t)), c)
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		id   string
	}{
		{"empty", ""},
		{"path", "../../etc/passwd"},
		{"42 chars", strings.Repeat("a", 42)},
		{"44 chars", strings.Repeat("a", 44)},
		{"slash", strings.Repeat("a", 42) + "/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.Get(ctx, tc.id); !errors.Is(err, ErrNotFound) {
				t.Fatalf("Get err = %v, want ErrNotFound", err)
			}
			if err := s.Touch(ctx, tc.id); !errors.Is(err, ErrNotFound) {
				t.Fatalf("Touch err = %v, want ErrNotFound", err)
			}
			if err := s.Update(ctx, tc.id, sampleRecord()); !errors.Is(err, ErrNotFound) {
				t.Fatalf("Update err = %v, want ErrNotFound", err)
			}
			if err := s.Delete(ctx, tc.id); err != nil {
				t.Fatalf("Delete err = %v, want nil", err)
			}
		})
	}
	if n := counting.calls.Load(); n != 0 {
		t.Fatalf("invalid ids made %d store calls, want 0", n)
	}
}

func TestKeyRotatedOutMeansSignedOut(t *testing.T) {
	c := &clock{t: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)}
	mem := kv.NewMemory(c.now)
	ctx := context.Background()
	k1, k2 := newKey(t), newKey(t)

	id := mustCreate(t, newStore(mem, newRing(t, k1), c), sampleRecord())
	if _, err := newStore(mem, newRing(t, k2, k1), c).Get(ctx, id); err != nil {
		t.Fatalf("Get with [k2, k1]: %v", err)
	}
	if _, err := newStore(mem, newRing(t, k2), c).Get(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get with [k2] err = %v, want ErrNotFound", err)
	}
}

func TestExpiredListsOnlyDead(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	start := f.c.t

	oldest := mustCreate(t, f.s, sampleRecord())
	f.c.t = start.Add(30 * time.Minute)
	older := mustCreate(t, f.s, sampleRecord())
	f.c.t = start.Add(2 * time.Hour)
	alive := mustCreate(t, f.s, sampleRecord())

	now := start.Add(testIdle + time.Hour)
	got, err := f.s.Expired(ctx, now, 0)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{KeyHash(oldest), KeyHash(older)}; !slices.Equal(got, want) {
		t.Fatalf("Expired = %d hashes, want the two dead ones oldest first", len(got))
	}
	if slices.Contains(got, KeyHash(alive)) {
		t.Fatal("Expired listed a live session")
	}

	got, err = f.s.Expired(ctx, now, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{KeyHash(oldest)}) {
		t.Fatalf("Expired with limit 1 = %d hashes, want the oldest", len(got))
	}
}

func TestDeleteHash(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	h := KeyHash(mustCreate(t, f.s, sampleRecord()))
	if err := f.s.DeleteHash(ctx, h); err != nil {
		t.Fatal(err)
	}
	if _, err := f.mem.Get(ctx, key(h)); !errors.Is(err, kv.ErrNotFound) {
		t.Fatalf("key still there: %v", err)
	}
	if got := indexed(t, f.mem, f.c.t.Add(testMax)); len(got) != 0 {
		t.Fatalf("index still holds %d entries", len(got))
	}
	for _, bad := range []string{"", "session:abc", strings.Repeat("A", 64), "../" + strings.Repeat("a", 61)} {
		if err := f.s.DeleteHash(ctx, bad); err != nil {
			t.Fatalf("DeleteHash(invalid) = %v, want nil", err)
		}
	}
}

func TestConcurrentUse(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := sampleRecord()
			rec.Login = fmt.Sprintf("user%d", i)
			id, err := f.s.Create(ctx, rec)
			if err != nil {
				t.Error(err)
				return
			}
			for range 50 {
				if _, err := f.s.Get(ctx, id); err != nil {
					t.Error(err)
					return
				}
				if err := f.s.Touch(ctx, id); err != nil {
					t.Error(err)
					return
				}
			}
			if err := f.s.Delete(ctx, id); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}
