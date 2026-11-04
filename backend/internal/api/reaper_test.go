package api

import (
	"bytes"
	"context"
	"errors"
	"log"
	"math"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/christiannarbon/yanachan-4k/backend/internal/config"
	"github.com/christiannarbon/yanachan-4k/backend/internal/githubfake"
	"github.com/christiannarbon/yanachan-4k/backend/internal/kv"
	"github.com/christiannarbon/yanachan-4k/backend/internal/session"
)

func expiryIndex(t *testing.T, sa *serverAuth) []string {
	t.Helper()
	got, err := harnessKV(sa).ZRangeByScore(context.Background(), "session-expiry", math.MaxFloat64, 0)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func reapNow(t *testing.T, sa *serverAuth) int {
	t.Helper()
	n, err := sa.reapOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func expireAll(sa *serverAuth) {
	harnessClock(sa).Advance(sa.cfg.SessionIdle + time.Minute)
}

func lockValue(t *testing.T, sa *serverAuth) string {
	t.Helper()
	v, err := harnessKV(sa).Get(context.Background(), reaperLock)
	if errors.Is(err, kv.ErrNotFound) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(v)
}

func TestReaperRevokesExpiredSessions(t *testing.T) {
	sa, ts, fake := newServerAuthHarness(t)
	ctx := context.Background()
	aliceCl, _ := signIn(t, ts, fake, "alice")
	alice, aliceTok := sessionCookie(t, aliceCl, ts), fake.AccessTokenFor("alice")
	bobCl, _ := signIn(t, ts, fake, "bob")
	bob, bobTok := sessionCookie(t, bobCl, ts), fake.AccessTokenFor("bob")

	clock := harnessClock(sa)
	clock.Advance(sa.cfg.SessionIdle / 2)
	if err := sa.sessions.Touch(ctx, bob); err != nil {
		t.Fatal(err)
	}
	clock.Advance(sa.cfg.SessionIdle/2 + time.Minute)

	if n := reapNow(t, sa); n != 1 {
		t.Fatalf("reaped %d, want 1", n)
	}
	revoked := fake.Revoked()
	if !slices.Contains(revoked, aliceTok) || slices.Contains(revoked, bobTok) {
		t.Fatalf("revoked %v, want alice's token only", revoked)
	}
	if _, err := sa.sessions.LoadForRevocation(ctx, session.KeyHash(alice)); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("alice's session: %v, want ErrNotFound", err)
	}
	if slices.Contains(expiryIndex(t, sa), session.KeyHash(alice)) {
		t.Fatal("expiry index still holds alice")
	}
	if code := getSettings(t, bobCl, ts); code != http.StatusOK {
		t.Fatalf("bob's settings = %d, want 200", code)
	}
}

func TestReaperRevokesAfterMaxAge(t *testing.T) {
	sa, ts, fake := newServerAuthHarness(t)
	ctx := context.Background()
	cl, _ := signIn(t, ts, fake, "alice")
	sid, tok := sessionCookie(t, cl, ts), fake.AccessTokenFor("alice")

	clock := harnessClock(sa)
	for {
		clock.Advance(time.Hour)
		if err := sa.sessions.Touch(ctx, sid); err != nil {
			break
		}
		if n := reapNow(t, sa); n != 0 {
			t.Fatalf("reaped an active session at %s", clock.Now())
		}
	}
	if n := reapNow(t, sa); n != 1 {
		t.Fatalf("reaped %d, want 1", n)
	}
	if !slices.Contains(fake.Revoked(), tok) {
		t.Fatal("token not revoked")
	}
}

func TestReaperDeletesEvenIfRevokeFails(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	sa, ts, fake := newServerAuthHarness(t)
	cl, _ := signIn(t, ts, fake, "alice")
	sid, tok := sessionCookie(t, cl, ts), fake.AccessTokenFor("alice")
	fake.FailRevoke()
	expireAll(sa)

	if n := reapNow(t, sa); n != 1 {
		t.Fatalf("reaped %d, want 1", n)
	}
	if _, err := sa.sessions.LoadForRevocation(context.Background(), session.KeyHash(sid)); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("session: %v, want ErrNotFound", err)
	}
	out := buf.String()
	if !strings.Contains(out, "token revocation failed for alice (1)") {
		t.Errorf("log lacks the failure:\n%s", out)
	}
	if strings.Contains(out, tok) {
		t.Errorf("log contains the token:\n%s", out)
	}
}

func TestReaperSkipsWhenLocked(t *testing.T) {
	sa, ts, fake := newServerAuthHarness(t)
	signIn(t, ts, fake, "alice")
	expireAll(sa)
	if err := harnessKV(sa).Set(context.Background(), reaperLock, []byte("other"), reapLockTTL); err != nil {
		t.Fatal(err)
	}

	if n := reapNow(t, sa); n != 0 {
		t.Fatalf("reaped %d, want 0", n)
	}
	if got := fake.Revoked(); len(got) != 0 {
		t.Fatalf("revoked %v", got)
	}
	if got := lockValue(t, sa); got != "other" {
		t.Fatalf("lock = %q, want other", got)
	}
}

// takeover lets the first revocation through after another replica has taken
// the reaper lock.
type takeover struct {
	once  sync.Once
	store kv.Store
}

func (tk *takeover) RoundTrip(r *http.Request) (*http.Response, error) {
	tk.once.Do(func() {
		_ = tk.store.Set(r.Context(), reaperLock, []byte("other"), reapLockTTL)
	})
	return http.DefaultTransport.RoundTrip(r)
}

func TestReaperReleasesOnlyItsOwnLock(t *testing.T) {
	sa, ts, fake := newServerAuthHarness(t)
	signIn(t, ts, fake, "alice")
	expireAll(sa)
	sa.web.HTTP = &http.Client{Transport: &takeover{store: harnessKV(sa)}}

	if n := reapNow(t, sa); n != 1 {
		t.Fatalf("reaped %d, want 1", n)
	}
	if got := lockValue(t, sa); got != "other" {
		t.Fatalf("lock = %q, want the other holder's value", got)
	}
}

func TestReaperReleasesLockAfterPass(t *testing.T) {
	sa, _, _ := newServerAuthHarness(t)
	reapNow(t, sa)
	if got := lockValue(t, sa); got != "" {
		t.Fatalf("lock = %q, want released", got)
	}
}

func TestTwoReapersRevokeOnce(t *testing.T) {
	sa, ts, fake := newServerAuthHarness(t)
	for range 30 {
		signIn(t, ts, fake, "alice")
	}
	other, err := newServerModeWith(sa.cfg, harnessKV(sa), nil, harnessClock(sa).Now)
	if err != nil {
		t.Fatal(err)
	}
	expireAll(sa)

	var wg sync.WaitGroup
	counts := make([]int, 2)
	for i, a := range []*serverAuth{sa, other.server} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := a.reapOnce(context.Background())
			if err != nil {
				t.Error(err)
			}
			counts[i] = n
		}()
	}
	wg.Wait()

	revoked := fake.Revoked()
	if len(revoked) != 30 {
		t.Fatalf("revoked %d tokens, want 30", len(revoked))
	}
	if len(slices.Compact(slices.Sorted(slices.Values(revoked)))) != 30 {
		t.Fatalf("a token was revoked twice: %v", revoked)
	}
	if counts[0]+counts[1] != 30 {
		t.Fatalf("reapers ended %v sessions, want 30 in all", counts)
	}
}

func TestReaperBatches(t *testing.T) {
	sa, ts, fake := newServerAuthHarness(t)
	for range 250 {
		signIn(t, ts, fake, "alice")
	}
	expireAll(sa)

	if n := reapNow(t, sa); n != 250 {
		t.Fatalf("reaped %d, want 250", n)
	}
	if got := len(fake.Revoked()); got != 250 {
		t.Fatalf("revoked %d, want 250", got)
	}
	if got := expiryIndex(t, sa); len(got) != 0 {
		t.Fatalf("index still holds %d entries", len(got))
	}
}

func TestReaperIndexEntryWithoutKey(t *testing.T) {
	sa, _, fake := newServerAuthHarness(t)
	orphan := session.KeyHash(strings.Repeat("x", 43))
	if err := harnessKV(sa).ZAdd(context.Background(), "session-expiry", float64(harnessClock(sa).Now().Unix()-60), orphan); err != nil {
		t.Fatal(err)
	}

	if n := reapNow(t, sa); n != 0 {
		t.Fatalf("reaped %d, want 0", n)
	}
	if got := expiryIndex(t, sa); len(got) != 0 {
		t.Fatalf("index still holds %v", got)
	}
	if got := fake.Revoked(); len(got) != 0 {
		t.Fatalf("revoked %v", got)
	}
}

// brokenIndex is a kv store whose sorted-set reads fail, as when Redis goes away.
type brokenIndex struct{ kv.Store }

func (brokenIndex) ZRangeByScore(context.Context, string, float64, int64) ([]string, error) {
	return nil, errors.New("connection refused")
}

func TestReaperStopsOnKVError(t *testing.T) {
	sa, ts, fake := newServerAuthHarness(t)
	signIn(t, ts, fake, "alice")
	expireAll(sa)
	broken, err := newServerModeWith(sa.cfg, brokenIndex{harnessKV(sa)}, nil, harnessClock(sa).Now)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := broken.server.reapOnce(context.Background()); err == nil {
		t.Fatal("reapOnce returned no error")
	}
	if got := fake.Revoked(); len(got) != 0 {
		t.Fatalf("revoked %v", got)
	}
	if got := lockValue(t, sa); got != "" {
		t.Fatalf("lock = %q, want released", got)
	}
}

func TestRunReaperLocalModeReturns(t *testing.T) {
	s := newServerBase(config.Config{}, nil)
	done := make(chan struct{})
	go func() {
		s.RunReaper(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RunReaper did not return in local mode")
	}
}

func TestRunReaperStopsWithContext(t *testing.T) {
	sa, _, _ := newServerAuthHarness(t)
	s := &Server{server: sa}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.RunReaper(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RunReaper kept running after cancel")
	}
}

func TestReaperLogsNoSecrets(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	sa, ts, fake := newServerAuthHarness(t)
	cl, _ := signIn(t, ts, fake, "alice")
	sid, tok := sessionCookie(t, cl, ts), fake.AccessTokenFor("alice")
	expireAll(sa)
	buf.Reset()
	reapNow(t, sa)

	out := buf.String()
	if !strings.Contains(out, "session expired: alice (1)") {
		t.Errorf("log lacks the expiry:\n%s", out)
	}
	for _, secret := range []string{tok, sid, session.KeyHash(sid), githubfake.ClientSecret} {
		if strings.Contains(out, secret) {
			t.Errorf("log contains a secret:\n%s", out)
		}
	}
}
