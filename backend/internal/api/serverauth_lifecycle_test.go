package api

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/christiannarbon/yanachan-4k/backend/internal/config"
	"github.com/christiannarbon/yanachan-4k/backend/internal/github"
	"github.com/christiannarbon/yanachan-4k/backend/internal/githubfake"
	"github.com/christiannarbon/yanachan-4k/backend/internal/kv"
	"github.com/christiannarbon/yanachan-4k/backend/internal/seal"
	"github.com/christiannarbon/yanachan-4k/backend/internal/session"
	"github.com/christiannarbon/yanachan-4k/backend/internal/state"
)

func revokeQueueLen(t *testing.T, sa *serverAuth) int {
	t.Helper()
	ids, err := harnessKV(sa).ZRangeByScore(context.Background(), revokeQueue, math.MaxFloat64, 0)
	if err != nil {
		t.Fatal(err)
	}
	return len(ids)
}

func logout(t *testing.T, cl *http.Client, ts *httptest.Server) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/auth/logout", nil)
	req.Header.Set("Origin", ts.URL)
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("logout = %d", resp.StatusCode)
	}
}

func TestFailedRevokeIsRetried(t *testing.T) {
	sa, ts, fake := newServerAuthHarness(t)
	cl, _ := signIn(t, ts, fake, "alice")
	tok := fake.AccessTokenFor("alice")

	fake.FailRevoke()
	logout(t, cl, ts)
	if n := revokeQueueLen(t, sa); n != 1 {
		t.Fatalf("queue = %d, want 1", n)
	}

	// Not due yet: nothing happens.
	fake.ResumeRevoke()
	reapNow(t, sa)
	if slices.Contains(fake.Revoked(), tok) {
		t.Fatal("retried before its backoff")
	}

	harnessClock(sa).Advance(revokeFirstRetry + time.Second)
	reapNow(t, sa)
	if !slices.Contains(fake.Revoked(), tok) {
		t.Fatal("the queued token was not revoked")
	}
	if n := revokeQueueLen(t, sa); n != 0 {
		t.Fatalf("queue = %d after success, want 0", n)
	}
}

func TestFailedRevokeGivesUp(t *testing.T) {
	sa, ts, fake := newServerAuthHarness(t)
	cl, _ := signIn(t, ts, fake, "alice")
	fake.FailRevoke()
	logout(t, cl, ts)
	for i := 0; i < revokeMaxAttempts+2; i++ {
		harnessClock(sa).Advance(revokeMaxBackoff + time.Second)
		reapNow(t, sa)
	}
	if n := revokeQueueLen(t, sa); n != 0 {
		t.Fatalf("queue = %d, want 0 after giving up", n)
	}
}

func TestRevokeQueueHoldsNoPlaintextToken(t *testing.T) {
	sa, ts, fake := newServerAuthHarness(t)
	cl, _ := signIn(t, ts, fake, "alice")
	tok := fake.AccessTokenFor("alice")
	fake.FailRevoke()
	logout(t, cl, ts)
	ids, err := harnessKV(sa).ZRangeByScore(context.Background(), revokeQueue, math.MaxFloat64, 0)
	if err != nil || len(ids) != 1 {
		t.Fatalf("queue = %v, %v", ids, err)
	}
	raw, err := harnessKV(sa).Get(context.Background(), revokeKeyPrefix+ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), tok) {
		t.Fatal("the queued token is stored in plaintext")
	}
}

func TestRevokeBackoff(t *testing.T) {
	want := []time.Duration{5 * time.Minute, 10 * time.Minute, 20 * time.Minute, 40 * time.Minute,
		80 * time.Minute, 160 * time.Minute, 320 * time.Minute, 6 * time.Hour, 6 * time.Hour}
	for i, w := range want {
		if got := revokeBackoff(i + 1); got != w {
			t.Errorf("revokeBackoff(%d) = %v, want %v", i+1, got, w)
		}
	}
}

// An OAuth App token past its maximum age is revoked and dropped at once,
// not kept valid at GitHub until the user comes back.
func TestOAuthTokenRetiredAtMaxAge(t *testing.T) {
	sa, ts, fake := newServerAuthHarness(t)
	cl, _ := signIn(t, ts, fake, "alice")
	sid := sessionCookie(t, cl, ts)
	tok := fake.AccessTokenFor("alice")

	harnessClock(sa).Advance(8*time.Hour + time.Minute)
	resp := get(t, cl, ts.URL+"/api/suggestions")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("suggestions = %d, want 401", resp.StatusCode)
	}
	if !slices.Contains(fake.Revoked(), tok) {
		t.Fatal("the token past its maximum age is still valid at GitHub")
	}
	if rec := loadSession(t, sa, sid); rec.Token != "" {
		t.Fatal("the retired token is still stored")
	}
	if code := getSettings(t, cl, ts); code != http.StatusOK {
		t.Fatalf("settings = %d while waiting to re-authorise, want 200", code)
	}

	// Re-authorising fills the same session again.
	walkSignIn(t, cl, ts, "return=/board")
	if sessionCookie(t, cl, ts) != sid {
		t.Fatal("re-authorising replaced the session")
	}
	if rec := loadSession(t, sa, sid); rec.Token != fake.AccessTokenFor("alice") || rec.Token == tok {
		t.Fatal("the new token is not in the session")
	}
}

// A request still holding the old token when the session is re-authorised
// must not end the renewed session.
func TestRejectedOldTokenKeepsRenewedSession(t *testing.T) {
	sa, ts, fake := newServerAuthHarnessWith(t, longTokenAge)
	cl, _ := signIn(t, ts, fake, "alice")
	sid := sessionCookie(t, cl, ts)
	old := fake.AccessTokenFor("alice")
	walkSignIn(t, cl, ts, "")
	current := fake.AccessTokenFor("alice")
	if current == old {
		t.Fatal("re-sign-in did not issue a new token")
	}

	srv := &Server{server: sa}
	req := httptest.NewRequest(http.MethodGet, "/api/board", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieBase, Value: sid})

	w := httptest.NewRecorder()
	if !srv.tokenRejected(w, req, viewer{token: old}, github.ErrUnauthorized) {
		t.Fatal("tokenRejected did not handle a 401")
	}
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	if rec := loadSession(t, sa, sid); rec.Token != current {
		t.Fatal("the renewed session was changed or ended")
	}
	if slices.Contains(fake.Revoked(), current) {
		t.Fatal("the renewed token was revoked")
	}

	w = httptest.NewRecorder()
	srv.tokenRejected(w, req, viewer{token: current}, github.ErrUnauthorized)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if _, err := sa.sessions.Get(context.Background(), sid); !errors.Is(err, session.ErrNotFound) {
		t.Fatal("a session whose current token GitHub refused survived")
	}
}

// GitHub spends the refresh token as soon as it answers; a request cancelled
// mid-refresh must still store the new pair.
func TestRefreshSurvivesCancelledRequest(t *testing.T) {
	sa, ts, fake := newServerAuthHarness(t)
	fake.ExpiringTokens = true
	cl, _ := signIn(t, ts, fake, "alice")
	sid := sessionCookie(t, cl, ts)
	old := fake.AccessTokenFor("alice")

	harnessClock(sa).Advance(8*time.Hour + time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec, err := sa.rotate(ctx, sid, loadSession(t, sa, sid))
	if err != nil {
		t.Fatalf("rotate with a cancelled request: %v", err)
	}
	if rec.Token == old || loadSession(t, sa, sid).Token != fake.AccessTokenFor("alice") {
		t.Fatal("the refreshed token was not stored")
	}
}

// failingSets fails every Set, so a new session cannot be stored.
type failingSets struct{ kv.Store }

func (failingSets) Set(context.Context, string, []byte, time.Duration) error {
	return errors.New("redis is down")
}

func TestCallbackRevokesTokenItCannotStore(t *testing.T) {
	sa, ts, fake := newServerAuthHarness(t)
	ring, err := seal.NewKeyring(sa.cfg.SessionKeys)
	if err != nil {
		t.Fatal(err)
	}
	sa.sessions = session.New(failingSets{harnessKV(sa)}, ring, sa.cfg.SessionIdle, sa.cfg.SessionMax)
	sa.sessions.SetClock(harnessClock(sa).Now)

	if _, loc := signIn(t, ts, fake, "alice"); loc != "/?authError=server" {
		t.Fatalf("location = %q", loc)
	}
	if !slices.Contains(fake.Revoked(), fake.AccessTokenFor("alice")) {
		t.Fatal("a token that never reached a session is still valid")
	}
}

// Another site navigating to the callback with ?error= must not cancel a
// sign-in in progress.
func TestCallbackErrorNeedsState(t *testing.T) {
	_, ts, fake := newServerAuthHarness(t)
	fake.SignInAs("alice")
	cl := newBrowser(t, ts)
	callback := startLogin(t, cl, ts)

	resp := get(t, noFollow(cl), ts.URL+"/api/auth/callback?error=access_denied")
	if loc := resp.Header.Get("Location"); loc != "/?authError=state" {
		t.Fatalf("forged error landed at %q", loc)
	}
	resp = get(t, noFollow(cl), callback.String())
	if loc := resp.Header.Get("Location"); loc != "/" {
		t.Fatalf("the real callback landed at %q after a forged error", loc)
	}
	if sessionCookie(t, cl, ts) == "" {
		t.Fatal("no session after the real callback")
	}
}

// Reverify reads the live identity: a session whose token now answers for a
// different GitHub account ends.
func TestReverifyEndsSessionOnIdentityChange(t *testing.T) {
	sa, ts, fake := newServerAuthHarnessWith(t, longTokenAge)
	cl, _ := signIn(t, ts, fake, "alice")
	sid := sessionCookie(t, cl, ts)
	fake.AddUser(githubfake.User{Login: "alice", ID: 999, Orgs: []string{"acme"}})
	harnessClock(sa).Advance(13 * time.Hour)
	if code := getSettings(t, cl, ts); code != http.StatusUnauthorized {
		t.Fatalf("settings = %d, want 401", code)
	}
	if _, err := sa.sessions.Get(context.Background(), sid); err == nil {
		t.Fatal("session survived an identity change")
	}
}

func TestAllowlistByUserID(t *testing.T) {
	_, ts, fake := newServerAuthHarnessWith(t, func(c *config.Config) {
		c.AllowedOrgs = nil
		c.AllowedUsers = []string{"id:2"}
	})
	if _, loc := signIn(t, ts, fake, "bob"); loc != "/" {
		t.Fatalf("bob (id 2) landed at %q", loc)
	}
	fake.AddUser(githubfake.User{Login: "bob", ID: 77})
	if _, loc := signIn(t, ts, fake, "bob"); loc != "/?authError=not_allowed" {
		t.Fatalf("a re-registered bob landed at %q", loc)
	}
}

// A replica without the key a session was sealed with leaves it for one
// that has it, and does not spin on it.
func TestReaperSkipsUnreadableSessions(t *testing.T) {
	sa, ts, fake := newServerAuthHarness(t)
	cl, _ := signIn(t, ts, fake, "alice")
	sid := sessionCookie(t, cl, ts)
	tok := fake.AccessTokenFor("alice")

	readable := sa.sessions
	other, err := seal.NewKeyring([][]byte{testSessionKey(t)})
	if err != nil {
		t.Fatal(err)
	}
	sa.sessions = session.New(harnessKV(sa), other, sa.cfg.SessionIdle, sa.cfg.SessionMax)
	sa.sessions.SetClock(harnessClock(sa).Now)
	expireAll(sa)

	if n := reapNow(t, sa); n != 0 {
		t.Fatalf("reaped %d sessions it cannot read", n)
	}
	if slices.Contains(fake.Revoked(), tok) {
		t.Fatal("revoked without being able to read")
	}

	sa.sessions = readable
	if n := reapNow(t, sa); n != 1 {
		t.Fatalf("the replica with the key reaped %d, want 1", n)
	}
	if !slices.Contains(fake.Revoked(), tok) {
		t.Fatal("token not revoked by the replica with the key")
	}
	if _, err := readable.LoadForRevocation(context.Background(), session.KeyHash(sid)); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("session: %v", err)
	}
}

// GHDASH_PUBLIC_URL means nothing in local mode and must not widen Guard 3.
func TestPublicURLIgnoredInLocalMode(t *testing.T) {
	h := newTestHandler(t, config.Config{PublicURL: "https://yana.example.test"})
	if w := do(t, h, http.MethodGet, "/api/settings", "yana.example.test", nil, ""); w.Code != http.StatusForbidden {
		t.Fatalf("local mode answered a public-URL Host with %d, want 403", w.Code)
	}
}

type failingGets struct{ kv.Store }

func (failingGets) Get(context.Context, string) ([]byte, error) {
	return nil, errors.New("kv: get: dial tcp 10.0.3.7:6379: connect: connection refused")
}

func TestServerModeHidesStoreErrors(t *testing.T) {
	sa, ts, fake := newServerAuthHarness(t)
	cl, _ := signIn(t, ts, fake, "alice")
	sa.users = state.NewUserSettings(failingGets{harnessKV(sa)}, state.DefaultSettings(25))
	resp, err := cl.Get(ts.URL + "/api/settings")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
	if strings.Contains(string(body), "10.0.3.7") || strings.Contains(string(body), "dial tcp") {
		t.Fatalf("store detail reached the browser: %s", body)
	}
}
