package api

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
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

func tokenAges(oauthApp, githubApp time.Duration) func(*config.Config) {
	return func(c *config.Config) {
		c.TokenMaxAge = time.Hour
		c.OAuthAppTokenMaxAge = oauthApp
		c.GitHubAppTokenMaxAge = githubApp
	}
}

// rotationHarness signs alice in on a harness with the given token ages and
// app kind, and returns her client and session ID.
func rotationHarness(t *testing.T, expiring bool, edit func(*config.Config)) (*serverAuth, *httptest.Server, *githubfake.Server, *http.Client, string) {
	t.Helper()
	if edit == nil {
		edit = tokenAges(time.Hour, time.Hour)
	}
	sa, ts, fake := newServerAuthHarnessWith(t, edit)
	fake.ExpiringTokens = expiring
	cl, _ := signIn(t, ts, fake, "alice")
	sid := sessionCookie(t, cl, ts)
	if sid == "" {
		t.Fatal("no session cookie after sign-in")
	}
	return sa, ts, fake, cl, sid
}

func loadSession(t *testing.T, sa *serverAuth, sid string) session.Record {
	t.Helper()
	rec, err := sa.sessions.Get(context.Background(), sid)
	if err != nil {
		t.Fatalf("session lookup: %v", err)
	}
	return rec
}

func editSession(t *testing.T, sa *serverAuth, sid string, change func(*session.Record)) {
	t.Helper()
	rec := loadSession(t, sa, sid)
	change(&rec)
	if err := sa.sessions.Update(context.Background(), sid, rec); err != nil {
		t.Fatal(err)
	}
}

func lastTokenSeen(fake *githubfake.Server) string {
	seen := fake.TokensSeen()
	if len(seen) == 0 {
		return ""
	}
	return seen[len(seen)-1]
}

func cookieExpired(resp *http.Response, name string) bool {
	for _, c := range resp.Cookies() {
		if c.Name == name && c.MaxAge < 0 {
			return true
		}
	}
	return false
}

func TestNeedsRotation(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		issued  time.Duration // before now
		expires time.Duration // after now; 0 = no expiry
		noExp   bool
		want    bool
	}{
		{"fresh token", time.Minute, 0, true, false},
		{"age equals max age", time.Hour, 0, true, true},
		{"age past max age", 2 * time.Hour, 0, true, true},
		{"zero expiry", 10 * time.Minute, 0, true, false},
		{"expiry in 10m", 10 * time.Minute, 10 * time.Minute, false, false},
		{"expiry in 4m", 10 * time.Minute, 4 * time.Minute, false, true},
		{"expiry passed", 10 * time.Minute, -time.Minute, false, true},
	} {
		rec := session.Record{TokenIssuedAt: now.Add(-tc.issued)}
		if !tc.noExp {
			rec.TokenExpiresAt = now.Add(tc.expires)
		}
		if got := needsRotation(rec, now, time.Hour); got != tc.want {
			t.Errorf("%s: needsRotation = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestOAuthAppTokenPastMaxAgeAsksForReauth(t *testing.T) {
	sa, ts, _, cl, _ := rotationHarness(t, false, nil)
	harnessClock(sa).Advance(61 * time.Minute)

	code, body := call(t, cl, http.MethodGet, ts.URL+"/api/suggestions", "", nil)
	if code != http.StatusUnauthorized || !strings.Contains(body, `"reauth":true`) {
		t.Fatalf("suggestions = %d %s", code, body)
	}
	if code, _ := call(t, cl, http.MethodGet, ts.URL+"/api/settings", "", nil); code != http.StatusOK {
		t.Fatalf("settings = %d, want 200: the session must survive", code)
	}
}

func TestReauthReplacesTokenInSameSession(t *testing.T) {
	sa, ts, fake, cl, sid := rotationHarness(t, false, nil)
	oldTok := fake.AccessTokenFor("alice")
	harnessClock(sa).Advance(61 * time.Minute)
	if code, _ := call(t, cl, http.MethodGet, ts.URL+"/api/suggestions", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("suggestions before reauth = %d, want 401", code)
	}

	walkSignIn(t, cl, ts, "")
	if sessionCookie(t, cl, ts) != sid {
		t.Fatal("session cookie changed on reauth")
	}
	if code, body := call(t, cl, http.MethodGet, ts.URL+"/api/suggestions", "", nil); code != http.StatusOK {
		t.Fatalf("suggestions after reauth = %d %s", code, body)
	}
	newTok := fake.AccessTokenFor("alice")
	if newTok == oldTok || lastTokenSeen(fake) != newTok {
		t.Error("token was not rotated")
	}
	if !slices.Contains(fake.Revoked(), oldTok) {
		t.Error("old token was not revoked")
	}
}

func TestReauthAsAnotherUserReplacesSession(t *testing.T) {
	sa, ts, fake, cl, sid := rotationHarness(t, false, nil)
	aliceTok := fake.AccessTokenFor("alice")

	fake.SignInAs("bob")
	walkSignIn(t, cl, ts, "")
	if got := sessionCookie(t, cl, ts); got == "" || got == sid {
		t.Fatal("session cookie did not change for another user")
	}
	if _, err := sa.sessions.Get(context.Background(), sid); !errors.Is(err, session.ErrNotFound) {
		t.Errorf("alice's session err = %v, want ErrNotFound", err)
	}
	if !slices.Contains(fake.Revoked(), aliceTok) {
		t.Error("alice's token was not revoked")
	}
}

func TestReauthReturnPath(t *testing.T) {
	_, ts, _, cl, _ := rotationHarness(t, false, nil)
	if loc := walkSignIn(t, cl, ts, "return=/board").Header.Get("Location"); loc != "/board" {
		t.Fatalf("location = %q, want /board", loc)
	}
}

func TestGitHubAppTokenRefreshedPastMaxAge(t *testing.T) {
	sa, ts, fake, cl, sid := rotationHarness(t, true, nil)
	before := loadSession(t, sa, sid)
	clock := harnessClock(sa)
	clock.Advance(61 * time.Minute)

	code, body := call(t, cl, http.MethodGet, ts.URL+"/api/suggestions", "", nil)
	if code != http.StatusOK || strings.Contains(body, "reauth") {
		t.Fatalf("suggestions = %d %s", code, body)
	}
	after := loadSession(t, sa, sid)
	if !after.TokenIssuedAt.Equal(clock.Now()) {
		t.Errorf("TokenIssuedAt = %v, want %v", after.TokenIssuedAt, clock.Now())
	}
	if after.Token == before.Token || after.RefreshToken == before.RefreshToken {
		t.Error("token was not rotated")
	}
	if after.Token != fake.AccessTokenFor("alice") || lastTokenSeen(fake) != after.Token {
		t.Error("request did not use the refreshed token")
	}
}

func TestGitHubAppRefreshBeforeGitHubExpiry(t *testing.T) {
	sa, ts, fake, cl, sid := rotationHarness(t, true, nil)
	before := loadSession(t, sa, sid)
	editSession(t, sa, sid, func(r *session.Record) { r.TokenExpiresAt = harnessClock(sa).Now().Add(time.Minute) })

	if code, _ := call(t, cl, http.MethodGet, ts.URL+"/api/suggestions", "", nil); code != http.StatusOK {
		t.Fatalf("suggestions = %d", code)
	}
	if fake.RefreshCount() != 1 || loadSession(t, sa, sid).Token == before.Token {
		t.Error("token was not refreshed ahead of GitHub's expiry")
	}
}

func TestConcurrentRequestsRefreshOnce(t *testing.T) {
	sa, ts, fake, cl, _ := rotationHarness(t, true, nil)
	clock := harnessClock(sa)
	other, err := newServerModeWith(sa.cfg, harnessKV(sa), nil, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	ts2 := httptest.NewServer(other.Handler())
	t.Cleanup(ts2.Close)
	clock.Advance(61 * time.Minute)

	codes := make(chan int, 10)
	var wg sync.WaitGroup
	for i := range 10 {
		base := ts.URL
		if i%2 == 1 {
			base = ts2.URL
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := cl.Get(base + "/api/suggestions")
			if err != nil {
				codes <- 0
				return
			}
			resp.Body.Close()
			codes <- resp.StatusCode
		}()
	}
	wg.Wait()
	close(codes)
	for code := range codes {
		if code != http.StatusOK {
			t.Errorf("a request answered %d, want 200", code)
		}
	}
	if n := fake.RefreshCount(); n != 1 {
		t.Errorf("github saw %d refresh grants, want 1", n)
	}
}

func TestOAuthAppOverrideApplies(t *testing.T) {
	sa, ts, _, cl, _ := rotationHarness(t, false, tokenAges(3*time.Hour, time.Hour))
	clock := harnessClock(sa)
	clock.Advance(61 * time.Minute)
	if code, _ := call(t, cl, http.MethodGet, ts.URL+"/api/suggestions", "", nil); code != http.StatusOK {
		t.Fatalf("at 61m = %d, want 200", code)
	}
	clock.Advance(2 * time.Hour)
	code, body := call(t, cl, http.MethodGet, ts.URL+"/api/suggestions", "", nil)
	if code != http.StatusUnauthorized || !strings.Contains(body, `"reauth":true`) {
		t.Fatalf("at 3h01m = %d %s", code, body)
	}
}

func TestGitHubAppOverrideApplies(t *testing.T) {
	sa, ts, fake, cl, _ := rotationHarness(t, true, tokenAges(3*time.Hour, 30*time.Minute))
	harnessClock(sa).Advance(31 * time.Minute)
	if code, _ := call(t, cl, http.MethodGet, ts.URL+"/api/suggestions", "", nil); code != http.StatusOK {
		t.Fatalf("suggestions = %d, want 200", code)
	}
	if n := fake.RefreshCount(); n != 1 {
		t.Errorf("github saw %d refresh grants, want 1", n)
	}
}

func TestOverridesInheritSharedAge(t *testing.T) {
	t.Setenv("GHDASH_AUTH_MODE", "server")
	t.Setenv("GHDASH_TOKEN_MAX_AGE", "2h")
	t.Setenv("GHDASH_OAUTH_APP_TOKEN_MAX_AGE", "")
	t.Setenv("GHDASH_GITHUB_APP_TOKEN_MAX_AGE", "")
	t.Setenv("GITHUB_CLIENT_ID", githubfake.ClientID)
	t.Setenv("GITHUB_CLIENT_SECRET", githubfake.ClientSecret)
	t.Setenv("GHDASH_PUBLIC_URL", "http://127.0.0.1:19080")
	t.Setenv("GHDASH_ALLOWED_ORGS", "acme")
	t.Setenv("GHDASH_REDIS_URL", "redis://127.0.0.1:6379/0")
	t.Setenv("GHDASH_SESSION_KEYS", base64.StdEncoding.EncodeToString(testSessionKey(t)))
	loaded := config.Load()
	if err := loaded.Validate(); err != nil {
		t.Fatalf("config: %v", err)
	}
	inherit := func(c *config.Config) {
		c.TokenMaxAge = loaded.TokenMaxAge
		c.OAuthAppTokenMaxAge = loaded.OAuthAppTokenMaxAge
		c.GitHubAppTokenMaxAge = loaded.GitHubAppTokenMaxAge
	}

	sa, ts, _, cl, _ := rotationHarness(t, false, inherit)
	harnessClock(sa).Advance(2*time.Hour + time.Minute)
	if code, body := call(t, cl, http.MethodGet, ts.URL+"/api/suggestions", "", nil); code != http.StatusUnauthorized || !strings.Contains(body, `"reauth":true`) {
		t.Errorf("oauth app at 2h01m = %d %s", code, body)
	}

	sa, ts, fake, cl, _ := rotationHarness(t, true, inherit)
	harnessClock(sa).Advance(2*time.Hour + time.Minute)
	if code, _ := call(t, cl, http.MethodGet, ts.URL+"/api/suggestions", "", nil); code != http.StatusOK {
		t.Errorf("github app at 2h01m = %d, want 200", code)
	}
	if n := fake.RefreshCount(); n != 1 {
		t.Errorf("github saw %d refresh grants, want 1", n)
	}
}

func TestRefreshLockReleased(t *testing.T) {
	sa, ts, _, cl, sid := rotationHarness(t, true, nil)
	harnessClock(sa).Advance(61 * time.Minute)
	if code, _ := call(t, cl, http.MethodGet, ts.URL+"/api/suggestions", "", nil); code != http.StatusOK {
		t.Fatalf("suggestions = %d", code)
	}
	_, err := harnessKV(sa).Get(context.Background(), "lock:refresh:"+session.KeyHash(sid))
	if !errors.Is(err, kv.ErrNotFound) {
		t.Fatalf("lock err = %v, want ErrNotFound", err)
	}
}

func TestBadRefreshEndsSession(t *testing.T) {
	sa, ts, _, cl, sid := rotationHarness(t, true, nil)
	editSession(t, sa, sid, func(r *session.Record) { r.RefreshToken = "bogus" })
	harnessClock(sa).Advance(61 * time.Minute)

	code, body := call(t, cl, http.MethodGet, ts.URL+"/api/suggestions", "", nil)
	if code != http.StatusUnauthorized || strings.Contains(body, "reauth") {
		t.Fatalf("suggestions = %d %s", code, body)
	}
	if _, err := sa.sessions.Get(context.Background(), sid); !errors.Is(err, session.ErrNotFound) {
		t.Errorf("session err = %v, want ErrNotFound", err)
	}
}

func TestExpiredRefreshTokenEndsSession(t *testing.T) {
	sa, ts, fake, cl, sid := rotationHarness(t, true, nil)
	clock := harnessClock(sa)
	clock.Advance(61 * time.Minute)
	editSession(t, sa, sid, func(r *session.Record) { r.RefreshExpiresAt = clock.Now().Add(-time.Minute) })

	if code, _ := call(t, cl, http.MethodGet, ts.URL+"/api/suggestions", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("suggestions = %d, want 401", code)
	}
	if n := fake.RefreshCount(); n != 0 {
		t.Errorf("github saw %d refresh grants, want 0", n)
	}
}

type failTokenEndpoint struct{ next http.RoundTripper }

func (f failTokenEndpoint) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path == "/login/oauth/access_token" {
		return nil, errors.New("connection reset")
	}
	return f.next.RoundTrip(r)
}

func TestRefreshTransientFailureKeepsValidToken(t *testing.T) {
	sa, ts, fake, cl, sid := rotationHarness(t, true, nil)
	before := loadSession(t, sa, sid)
	sa.web.HTTP.Transport = failTokenEndpoint{next: http.DefaultTransport}
	editSession(t, sa, sid, func(r *session.Record) { r.TokenExpiresAt = harnessClock(sa).Now().Add(time.Minute) })

	if code, _ := call(t, cl, http.MethodGet, ts.URL+"/api/suggestions", "", nil); code != http.StatusOK {
		t.Fatalf("suggestions = %d, want 200", code)
	}
	if lastTokenSeen(fake) != before.Token {
		t.Error("request did not use the current token")
	}
}

func TestRevokedAtGitHubEndsSession(t *testing.T) {
	sa, ts, fake, cl, sid := rotationHarness(t, false, nil)
	fake.ExpireAccessToken(fake.AccessTokenFor("alice"))

	resp := get(t, noFollow(cl), ts.URL+"/api/suggestions")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("suggestions = %d, want 401", resp.StatusCode)
	}
	if !cookieExpired(resp, sessionCookieBase) {
		t.Error("session cookie was not expired")
	}
	if _, err := sa.sessions.Get(context.Background(), sid); !errors.Is(err, session.ErrNotFound) {
		t.Errorf("session err = %v, want ErrNotFound", err)
	}
}

func TestLogoutRevokesAndExpiresCookies(t *testing.T) {
	_, ts, fake, cl, sid := rotationHarness(t, false, nil)
	token := fake.AccessTokenFor("alice")

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/auth/logout", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", ts.URL)
	req.AddCookie(&http.Cookie{Name: sessionCookieBase, Value: sid})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("logout = %d", resp.StatusCode)
	}
	if !slices.Contains(fake.Revoked(), token) {
		t.Error("token was not revoked")
	}
	if !cookieExpired(resp, sessionCookieBase) || !cookieExpired(resp, oauthCookieBase) {
		t.Errorf("cookies not both expired: %v", resp.Header["Set-Cookie"])
	}
	if code, _ := call(t, cl, http.MethodGet, ts.URL+"/api/settings", "", nil); code != http.StatusUnauthorized {
		t.Errorf("settings with the old cookie = %d, want 401", code)
	}
}

func TestLogoutWithoutSession(t *testing.T) {
	_, ts, fake := newServerAuthHarness(t)
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/auth/logout", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", ts.URL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("logout = %d", resp.StatusCode)
	}
	if !cookieExpired(resp, sessionCookieBase) || !cookieExpired(resp, oauthCookieBase) {
		t.Errorf("cookies not both expired: %v", resp.Header["Set-Cookie"])
	}
	if n := len(fake.Revoked()); n != 0 {
		t.Errorf("revoked %d tokens", n)
	}
}

func TestLogoutRevocationFailureStillSignsOut(t *testing.T) {
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	sa, ts, _, cl, sid := rotationHarness(t, false, func(c *config.Config) {
		tokenAges(time.Hour, time.Hour)(c)
		c.RESTEndpoint = closed.URL
	})
	code, _ := call(t, cl, http.MethodPost, ts.URL+"/api/auth/logout", "", map[string]string{"Origin": ts.URL})
	if code != http.StatusOK {
		t.Fatalf("logout = %d, want 200", code)
	}
	if _, err := sa.sessions.Get(context.Background(), sid); !errors.Is(err, session.ErrNotFound) {
		t.Errorf("session err = %v, want ErrNotFound", err)
	}
}
