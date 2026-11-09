package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/christiannarbon/yanachan-4k/backend/internal/config"
	"github.com/christiannarbon/yanachan-4k/backend/internal/githubfake"
	"github.com/christiannarbon/yanachan-4k/backend/internal/kv"
)

// These tests drive server mode the way a browser does: a real HTTP server, a
// cookie jar per user, and real redirects through the fake GitHub. With
// GHDASH_TEST_REDIS_URL set they run against a real Valkey instead of memory.

type e2e struct {
	t     *testing.T
	app   *httptest.Server
	gh    *githubfake.Server
	store kv.Store // for assertions only
	srv   *Server
	clock *fakeClock
	cfg   config.Config

	mu sync.Mutex
	// seen holds every app response body and every header value but
	// Set-Cookie; cookies holds every session cookie value the app set.
	seen    []string
	cookies []string
}

func newE2E(t *testing.T, mutate func(*config.Config)) *e2e {
	t.Helper()
	clock := &fakeClock{t: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)}

	gh := githubfake.New(t)
	gh.AddUser(githubfake.User{Login: "alice", ID: 1001, Orgs: []string{"acme"}})
	gh.AddUser(githubfake.User{Login: "bob", ID: 1002, Orgs: []string{"acme"}})
	gh.AddUser(githubfake.User{Login: "carol", ID: 1003, Orgs: []string{"other"}})

	// The public URL has to be known before the server is built; the
	// listener's address is, before Start.
	app := httptest.NewUnstartedServer(nil)
	cfg := config.Config{
		AuthMode:             config.AuthServer,
		ClientID:             githubfake.ClientID,
		ClientSecret:         githubfake.ClientSecret,
		PublicURL:            "http://" + app.Listener.Addr().String(),
		SessionKeys:          [][]byte{testSessionKey(t)},
		RedisURL:             "redis://127.0.0.1:1/0", // passes Validate; the injected store is used
		AllowedOrgs:          []string{"acme"},
		SessionIdle:          time.Hour,
		SessionMax:           24 * time.Hour,
		TokenMaxAge:          8 * time.Hour,
		OAuthAppTokenMaxAge:  8 * time.Hour,
		GitHubAppTokenMaxAge: 8 * time.Hour,
		WebEndpoint:          gh.URL,
		GraphQLEndpoint:      gh.URL + "/graphql",
		RESTEndpoint:         gh.URL,
		DefaultLimit:         25,
		Addr:                 "127.0.0.1:0",
	}
	if mutate != nil {
		mutate(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config: %v", err)
	}

	store := newTestKV(t, clock.Now)
	srv, err := newServerModeWith(cfg, store, nil, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	app.Config.Handler = srv.Handler()
	app.Start()
	t.Cleanup(app.Close)

	return &e2e{t: t, app: app, gh: gh, store: store, srv: srv, clock: clock, cfg: cfg}
}

// newTestKV is memory, or Valkey under a prefix of its own that is wiped
// when the test ends.
func newTestKV(t *testing.T, now func() time.Time) kv.Store {
	t.Helper()
	addr := os.Getenv("GHDASH_TEST_REDIS_URL")
	if addr == "" {
		return kv.NewMemory(now)
	}
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	opts := kv.RedisOptions{
		URL:      addr,
		Password: os.Getenv("GHDASH_TEST_REDIS_PASSWORD"),
		Prefix:   "yana-e2e:" + hex.EncodeToString(b) + ":",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, err := kv.NewRedis(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	t.Cleanup(func() { deletePrefix(t, opts) })
	t.Log("e2e store: redis")
	return r
}

func deletePrefix(t *testing.T, opts kv.RedisOptions) {
	o, err := redis.ParseURL(opts.URL)
	if err != nil {
		t.Error(err)
		return
	}
	if opts.Password != "" {
		o.Password = opts.Password
	}
	raw := redis.NewClient(o)
	defer raw.Close()
	ctx := context.Background()
	iter := raw.Scan(ctx, 0, opts.Prefix+"*", 100).Iterator()
	for iter.Next(ctx) {
		if err := raw.Del(ctx, iter.Val()).Err(); err != nil {
			t.Error(err)
		}
	}
	if err := iter.Err(); err != nil {
		t.Error(err)
	}
}

// recorder keeps a copy of every response the app sends, for the leak check.
type recorder struct {
	e    *e2e
	next http.RoundTripper
}

func (rt recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := rt.next.RoundTrip(req)
	if err != nil || req.URL.Host != rt.e.app.Listener.Addr().String() {
		return resp, err
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))

	rt.e.mu.Lock()
	defer rt.e.mu.Unlock()
	rt.e.seen = append(rt.e.seen, string(body))
	for name, vals := range resp.Header {
		if name == "Set-Cookie" {
			continue
		}
		for _, v := range vals {
			rt.e.seen = append(rt.e.seen, name+": "+v)
		}
	}
	for _, c := range resp.Cookies() {
		if c.Name == cookieName(sessionCookieBase, rt.e.cfg.Secure()) && c.Value != "" {
			rt.e.cookies = append(rt.e.cookies, c.Value)
		}
	}
	return resp, nil
}

// client is a fresh browser: its own cookie jar, following redirects through
// sign-in and stopping at the app's own pages.
func (e *e2e) client() *http.Client {
	e.t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		e.t.Fatal(err)
	}
	host := e.app.Listener.Addr().String()
	return &http.Client{
		Jar:       jar,
		Transport: recorder{e: e, next: http.DefaultTransport},
		CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			if req.URL.Host == host && !strings.HasPrefix(req.URL.Path, "/api/") {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
}

// noFollow shares c's cookies but stops at every redirect.
func (e *e2e) noFollow(c *http.Client) *http.Client {
	return &http.Client{
		Jar:           c.Jar,
		Transport:     c.Transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// signIn signs login in at the fake GitHub and walks the app's sign-in; the
// response is the redirect to one of the app's pages.
func (e *e2e) signIn(c *http.Client, login string) *http.Response {
	e.t.Helper()
	e.gh.SignInAs(login)
	return e.do(c, http.MethodGet, "/api/auth/login", "")
}

// do sends a request to the app. Non-GET requests carry the app's Origin, as
// a browser's would.
func (e *e2e) do(c *http.Client, method, path, body string) *http.Response {
	e.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, e.app.URL+path, rd)
	if err != nil {
		e.t.Fatal(err)
	}
	if method != http.MethodGet {
		req.Header.Set("Origin", e.app.URL)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	return resp
}

// sessionCookie is the jar's yana-session value, "" if none.
func (e *e2e) sessionCookie(c *http.Client) string {
	u, _ := url.Parse(e.app.URL)
	for _, ck := range c.Jar.Cookies(u) {
		if ck.Name == sessionCookieBase {
			return ck.Value
		}
	}
	return ""
}

// lastToken is the token on the most recent GraphQL call to the fake.
func (e *e2e) lastToken() string {
	seen := e.gh.TokensSeen()
	if len(seen) == 0 {
		return ""
	}
	return seen[len(seen)-1]
}

func wantStatus(t *testing.T, resp *http.Response, want int, what string) {
	t.Helper()
	if resp.StatusCode != want {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s: status %d, want %d (%s)", what, resp.StatusCode, want, b)
	}
}

func wantLocation(t *testing.T, resp *http.Response, want string) {
	t.Helper()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != want {
		t.Fatalf("landed with %d at %q, want 302 to %q", resp.StatusCode, resp.Header.Get("Location"), want)
	}
}

func decodeJSON[T any](t *testing.T, resp *http.Response) T {
	t.Helper()
	var v T
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatalf("decode %s: %v", resp.Request.URL.Path, err)
	}
	return v
}

func wantReauth(t *testing.T, resp *http.Response, what string) {
	t.Helper()
	wantStatus(t, resp, http.StatusUnauthorized, what)
	if body := decodeJSON[map[string]any](t, resp); body["reauth"] != true {
		t.Fatalf("%s: body %v, want reauth", what, body)
	}
}

// longIdle lets the rotation tests advance past a token's age without the
// session going idle first.
func longIdle(c *config.Config) { c.SessionIdle = 12 * time.Hour }

func TestE2ESignInSetsSessionCookie(t *testing.T) {
	e := newE2E(t, nil)
	c := e.client()
	wantLocation(t, e.signIn(c, "alice"), "/")

	if e.sessionCookie(c) == "" {
		t.Fatal("no yana-session cookie after sign-in")
	}
	u, _ := url.Parse(e.app.URL)
	for _, ck := range c.Jar.Cookies(u) {
		if strings.Contains(ck.Name, oauthCookieBase) {
			t.Errorf("the OAuth round-trip cookie outlived sign-in")
		}
		if strings.HasPrefix(ck.Name, "__Host-") {
			t.Errorf("cookie %s has the __Host- prefix on an http public URL", ck.Name)
		}
	}

	resp := e.do(c, http.MethodGet, "/api/auth/status", "")
	wantStatus(t, resp, http.StatusOK, "status")
	st := decodeJSON[authStatus](t, resp)
	if st.AuthMode != config.AuthServer || !st.Authenticated || st.Session == nil ||
		st.Session.Login != "alice" || st.Session.Mode != "github" {
		t.Fatalf("status = %+v", st)
	}
}

func TestE2ETwoUsersAreIsolated(t *testing.T) {
	e := newE2E(t, nil)
	alice, bob := e.client(), e.client()
	wantLocation(t, e.signIn(alice, "alice"), "/")
	wantLocation(t, e.signIn(bob, "bob"), "/")

	body := `{"teams":[],"orgs":["acme"],"limit":30,"windowHours":0,"onlyActive":false,"showUrls":false}`
	wantStatus(t, e.do(alice, http.MethodPut, "/api/settings", body), http.StatusOK, "alice saves")

	resp := e.do(bob, http.MethodGet, "/api/settings", "")
	wantStatus(t, resp, http.StatusOK, "bob reads")
	if got := decodeJSON[settingsJSON](t, resp); len(got.Orgs) != 0 || got.Limit != 25 {
		t.Fatalf("bob sees %+v, want defaults", got)
	}

	resp = e.do(alice, http.MethodGet, "/api/settings", "")
	wantStatus(t, resp, http.StatusOK, "alice reads")
	if got := decodeJSON[settingsJSON](t, resp); !slices.Equal(got.Orgs, []string{"acme"}) || got.Limit != 30 {
		t.Fatalf("alice sees %+v", got)
	}

	ctx := context.Background()
	if _, err := e.store.Get(ctx, "user:1001:settings"); err != nil {
		t.Fatalf("alice's settings: %v", err)
	}
	if _, err := e.store.Get(ctx, "user:1002:settings"); !errors.Is(err, kv.ErrNotFound) {
		t.Fatalf("bob's settings: err = %v, want ErrNotFound", err)
	}
}

type settingsJSON struct {
	Orgs  []string `json:"orgs"`
	Limit int      `json:"limit"`
}

func TestE2EEachRequestUsesItsOwnToken(t *testing.T) {
	e := newE2E(t, nil)
	clients := map[string]*http.Client{"alice": e.client(), "bob": e.client()}
	for login, c := range clients {
		wantLocation(t, e.signIn(c, login), "/")
	}
	for i, login := range []string{"alice", "bob", "alice"} {
		wantStatus(t, e.do(clients[login], http.MethodGet, "/api/suggestions", ""), http.StatusOK, login)
		if e.lastToken() != e.gh.AccessTokenFor(login) {
			t.Fatalf("call %d for %s reached GitHub with another token", i, login)
		}
	}
}

func TestE2ENotAllowedUser(t *testing.T) {
	e := newE2E(t, nil)
	c := e.client()
	wantLocation(t, e.signIn(c, "carol"), "/?authError=not_allowed")
	if e.sessionCookie(c) != "" {
		t.Fatal("carol got a session cookie")
	}
	got, err := e.store.ZRangeByScore(context.Background(), "session-expiry", math.Inf(1), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("%d sessions stored for a refused user", len(got))
	}
}

func TestE2EDeniedOnGitHub(t *testing.T) {
	e := newE2E(t, nil)
	e.gh.Deny()
	wantLocation(t, e.signIn(e.client(), "alice"), "/?authError=denied")
}

func TestE2EStateMismatch(t *testing.T) {
	e := newE2E(t, nil)
	e.gh.SignInAs("alice")
	a := e.noFollow(e.client())
	resp := e.do(a, http.MethodGet, "/api/auth/login", "")
	wantStatus(t, resp, http.StatusFound, "login")

	resp, err := a.Get(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	wantStatus(t, resp, http.StatusFound, "authorize")
	callback, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}

	wantLocation(t, e.do(e.client(), http.MethodGet, callback.RequestURI(), ""), "/?authError=state")
}

func TestE2ELocalOnlyRoutesAre404(t *testing.T) {
	e := newE2E(t, nil)
	routes := []string{
		"/api/auth/gh-cli/approve",
		"/api/auth/env-token/approve",
		"/api/auth/device/start",
		"/api/auth/device/poll",
	}
	for _, signedIn := range []bool{false, true} {
		c := e.client()
		if signedIn {
			wantLocation(t, e.signIn(c, "alice"), "/")
		}
		for _, path := range routes {
			resp := e.do(c, http.MethodPost, path, "")
			wantStatus(t, resp, http.StatusNotFound, path)
			if body := decodeJSON[map[string]string](t, resp); body["error"] == "" {
				t.Errorf("%s (signed in %v): no JSON error", path, signedIn)
			}
		}
	}
}

func TestE2EUnauthenticatedIs401(t *testing.T) {
	e := newE2E(t, nil)
	c := e.client()
	for _, path := range []string{"/api/settings", "/api/board", "/api/stats", "/api/suggestions"} {
		wantStatus(t, e.do(c, http.MethodGet, path, ""), http.StatusUnauthorized, path)
	}
	resp := e.do(c, http.MethodGet, "/api/auth/status", "")
	wantStatus(t, resp, http.StatusOK, "status")
	if st := decodeJSON[authStatus](t, resp); st.Authenticated || st.LoginURL != "/api/auth/login" {
		t.Fatalf("status = %+v", st)
	}
}

func TestE2EForgedCookieIs401(t *testing.T) {
	e := newE2E(t, nil)
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	c := e.client()
	u, _ := url.Parse(e.app.URL)
	c.Jar.SetCookies(u, []*http.Cookie{{Name: sessionCookieBase, Value: base64.RawURLEncoding.EncodeToString(b)}})
	wantStatus(t, e.do(c, http.MethodGet, "/api/settings", ""), http.StatusUnauthorized, "forged cookie")
}

func TestE2ERotationReauthSameSession(t *testing.T) {
	e := newE2E(t, longIdle)
	c := e.client()
	wantLocation(t, e.signIn(c, "alice"), "/")
	cookie := e.sessionCookie(c)
	old := e.gh.AccessTokenFor("alice")

	e.clock.Advance(8*time.Hour + time.Minute)
	wantReauth(t, e.do(c, http.MethodGet, "/api/suggestions", ""), "past max age")

	wantLocation(t, e.do(c, http.MethodGet, "/api/auth/login?return=/board", ""), "/board")
	if e.sessionCookie(c) != cookie {
		t.Fatal("re-authorising replaced the session cookie")
	}
	wantStatus(t, e.do(c, http.MethodGet, "/api/suggestions", ""), http.StatusOK, "after reauth")
	if e.lastToken() == old {
		t.Fatal("the old token is still in use")
	}
	if !slices.Contains(e.gh.Revoked(), old) {
		t.Fatal("the old token was not revoked")
	}
}

func TestE2ERotationGitHubAppIsInvisible(t *testing.T) {
	e := newE2E(t, longIdle)
	e.gh.ExpiringTokens = true
	c := e.client()
	wantLocation(t, e.signIn(c, "alice"), "/")
	old := e.gh.AccessTokenFor("alice")

	e.clock.Advance(8*time.Hour + time.Minute)
	resp := e.do(c, http.MethodGet, "/api/suggestions", "")
	wantStatus(t, resp, http.StatusOK, "past max age")
	if body := decodeJSON[map[string]any](t, resp); body["reauth"] != nil {
		t.Fatalf("body asks to reauth: %v", body)
	}
	if e.lastToken() == old {
		t.Fatal("the token was not refreshed")
	}
}

func TestE2ERotationAgesPerKind(t *testing.T) {
	e := newE2E(t, func(c *config.Config) {
		longIdle(c)
		c.OAuthAppTokenMaxAge = 4 * time.Hour
		c.GitHubAppTokenMaxAge = 30 * time.Minute
	})
	alice, bob := e.client(), e.client()
	wantLocation(t, e.signIn(alice, "alice"), "/")
	aliceTok := e.gh.AccessTokenFor("alice")
	e.gh.ExpiringTokens = true
	wantLocation(t, e.signIn(bob, "bob"), "/")
	bobTok := e.gh.AccessTokenFor("bob")

	e.clock.Advance(31 * time.Minute)
	wantStatus(t, e.do(bob, http.MethodGet, "/api/suggestions", ""), http.StatusOK, "bob at 31m")
	if e.lastToken() == bobTok || e.lastToken() != e.gh.AccessTokenFor("bob") {
		t.Fatal("bob's token was not refreshed at 31m")
	}
	wantStatus(t, e.do(alice, http.MethodGet, "/api/suggestions", ""), http.StatusOK, "alice at 31m")
	if e.lastToken() != aliceTok {
		t.Fatal("alice's token changed at 31m")
	}

	e.clock.Advance(4*time.Hour + time.Minute - 31*time.Minute)
	wantReauth(t, e.do(alice, http.MethodGet, "/api/suggestions", ""), "alice at 4h1m")
	wantStatus(t, e.do(bob, http.MethodGet, "/api/suggestions", ""), http.StatusOK, "bob at 4h1m")
}

func TestE2EReaperRevokesExpiredSessions(t *testing.T) {
	e := newE2E(t, nil)
	alice, bob := e.client(), e.client()
	wantLocation(t, e.signIn(alice, "alice"), "/")
	wantLocation(t, e.signIn(bob, "bob"), "/")
	aliceTok, bobTok := e.gh.AccessTokenFor("alice"), e.gh.AccessTokenFor("bob")

	for i := 0; i < 3; i++ {
		e.clock.Advance(30 * time.Minute)
		wantStatus(t, e.do(bob, http.MethodGet, "/api/settings", ""), http.StatusOK, "bob stays active")
	}
	if _, err := e.srv.server.reapOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	revoked := e.gh.Revoked()
	if !slices.Contains(revoked, aliceTok) {
		t.Error("alice's idle session was not revoked")
	}
	if slices.Contains(revoked, bobTok) {
		t.Error("bob's active session was revoked")
	}
	wantStatus(t, e.do(alice, http.MethodGet, "/api/settings", ""), http.StatusUnauthorized, "alice after reap")
	wantStatus(t, e.do(bob, http.MethodGet, "/api/settings", ""), http.StatusOK, "bob after reap")
}

func TestE2ELogoutRevokes(t *testing.T) {
	e := newE2E(t, nil)
	alice, bob := e.client(), e.client()
	wantLocation(t, e.signIn(alice, "alice"), "/")
	wantLocation(t, e.signIn(bob, "bob"), "/")
	aliceTok := e.gh.AccessTokenFor("alice")

	wantStatus(t, e.do(alice, http.MethodPost, "/api/auth/logout", ""), http.StatusOK, "logout")
	if !slices.Contains(e.gh.Revoked(), aliceTok) {
		t.Fatal("sign-out did not revoke alice's token")
	}
	wantStatus(t, e.do(alice, http.MethodGet, "/api/settings", ""), http.StatusUnauthorized, "alice after logout")
	wantStatus(t, e.do(bob, http.MethodGet, "/api/settings", ""), http.StatusOK, "bob after alice's logout")
}

func TestE2ECookieAttributesHTTPS(t *testing.T) {
	e := newE2E(t, func(c *config.Config) { c.PublicURL = "https://yana.example.test" })
	req := httptest.NewRequest(http.MethodGet, "/api/auth/login", nil)
	req.Host = "yana.example.test"
	rec := httptest.NewRecorder()
	e.srv.Handler().ServeHTTP(rec, req)

	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("got %d cookies, want 1", len(cookies))
	}
	ck := cookies[0]
	if ck.Name != "__Host-yana-oauth" || !ck.Secure || !ck.HttpOnly ||
		ck.SameSite != http.SameSiteLaxMode || ck.Path != "/" || ck.MaxAge != 600 {
		t.Errorf("cookie %s: secure=%v httponly=%v samesite=%v path=%q max-age=%d",
			ck.Name, ck.Secure, ck.HttpOnly, ck.SameSite, ck.Path, ck.MaxAge)
	}
	if rec.Header().Get("Strict-Transport-Security") == "" {
		t.Error("no Strict-Transport-Security header")
	}
}

// rotateAndReturn takes c's session past its token age and back through GitHub.
func (e *e2e) rotateAndReturn(c *http.Client) {
	e.t.Helper()
	wantReauth(e.t, e.do(c, http.MethodGet, "/api/suggestions", ""), "past max age")
	wantLocation(e.t, e.do(c, http.MethodGet, "/api/auth/login?return=/board", ""), "/board")
}

func TestE2EWritesNoFiles(t *testing.T) {
	cwd, stateDir := t.TempDir(), t.TempDir()
	t.Chdir(cwd)
	e := newE2E(t, func(c *config.Config) {
		longIdle(c)
		c.StateDir = stateDir
	})
	c := e.client()
	wantLocation(t, e.signIn(c, "alice"), "/")
	body := `{"teams":[],"orgs":["acme"],"limit":30,"windowHours":0,"onlyActive":false,"showUrls":false}`
	wantStatus(t, e.do(c, http.MethodPut, "/api/settings", body), http.StatusOK, "save settings")
	wantStatus(t, e.do(c, http.MethodGet, "/api/settings", ""), http.StatusOK, "read settings")
	e.clock.Advance(8*time.Hour + time.Minute)
	e.rotateAndReturn(c)
	wantStatus(t, e.do(c, http.MethodPost, "/api/auth/logout", ""), http.StatusOK, "logout")

	for _, dir := range []string{cwd, stateDir} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Errorf("%s holds %d entries, want none", dir, len(entries))
		}
	}
}

// TestE2ENoSecretAnywhere stays last in the file.
func TestE2ENoSecretAnywhere(t *testing.T) {
	e := newE2E(t, longIdle)
	ctx := context.Background()
	clients := map[string]*http.Client{"alice": e.client(), "bob": e.client()}
	body := `{"teams":[],"orgs":["acme"],"limit":30,"windowHours":0,"onlyActive":false,"showUrls":false}`
	for login, c := range clients {
		wantLocation(t, e.signIn(c, login), "/")
		e.do(c, http.MethodGet, "/api/board", "")
		wantStatus(t, e.do(c, http.MethodPut, "/api/settings", body), http.StatusOK, login+" saves")
		wantStatus(t, e.do(c, http.MethodGet, "/api/settings", ""), http.StatusOK, login+" reads")
	}
	e.clock.Advance(8*time.Hour + time.Minute)
	for _, c := range clients {
		e.rotateAndReturn(c)
		e.do(c, http.MethodGet, "/api/board", "")
	}

	cookies := func() []string {
		e.mu.Lock()
		defer e.mu.Unlock()
		out := slices.Clone(e.cookies)
		for _, c := range clients {
			if v := e.sessionCookie(c); v != "" {
				out = append(out, v)
			}
		}
		return out
	}
	tokens := func() []string {
		out := append(e.gh.TokensSeen(), e.gh.Revoked()...)
		for login := range clients {
			out = append(out, e.gh.AccessTokenFor(login))
		}
		return slices.DeleteFunc(out, func(s string) bool { return s == "" })
	}

	// Tokens are sealed in the store, not just encoded.
	checkStore := func() {
		t.Helper()
		members, err := e.store.ZRangeByScore(ctx, "session-expiry", math.Inf(1), 0)
		if err != nil {
			t.Fatal(err)
		}
		toks, cks := tokens(), cookies()
		for i, m := range members {
			raw, err := e.store.Get(ctx, "session:"+m)
			if errors.Is(err, kv.ErrNotFound) {
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			if containsAny(string(raw), toks) {
				t.Errorf("stored session %d holds a plaintext token", i)
			}
			if containsAny(string(raw), cks) {
				t.Errorf("stored session %d holds a session cookie value", i)
			}
		}
	}
	checkStore()

	for _, c := range clients {
		wantStatus(t, e.do(c, http.MethodPost, "/api/auth/logout", ""), http.StatusOK, "logout")
	}
	checkStore()

	key := e.cfg.SessionKeys[0]
	secrets := []struct {
		kind   string
		values []string
	}{
		{"a GitHub token", tokens()},
		{"a session cookie value", cookies()},
		{"the client secret", []string{githubfake.ClientSecret}},
		{"the session key", []string{base64.StdEncoding.EncodeToString(key), base64.RawURLEncoding.EncodeToString(key)}},
	}
	e.mu.Lock()
	seen := slices.Clone(e.seen)
	e.mu.Unlock()
	if len(seen) == 0 {
		t.Fatal("no responses recorded")
	}
	for i, s := range seen {
		for _, sec := range secrets {
			if containsAny(s, sec.values) {
				t.Errorf("response item %d contains %s", i, sec.kind)
			}
		}
	}
}

func containsAny(s string, needles []string) bool {
	for _, n := range needles {
		if n != "" && strings.Contains(s, n) {
			return true
		}
	}
	return false
}
