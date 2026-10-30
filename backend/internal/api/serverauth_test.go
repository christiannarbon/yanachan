package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"log"
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

	"github.com/christiannarbon/yanachan-4k/backend/internal/config"
	"github.com/christiannarbon/yanachan-4k/backend/internal/github"
	"github.com/christiannarbon/yanachan-4k/backend/internal/githubfake"
	"github.com/christiannarbon/yanachan-4k/backend/internal/kv"
	"github.com/christiannarbon/yanachan-4k/backend/internal/seal"
	"github.com/christiannarbon/yanachan-4k/backend/internal/session"
	"github.com/christiannarbon/yanachan-4k/backend/internal/state"
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

type harness struct {
	clock *fakeClock
	kv    *kv.Memory
}

var harnesses sync.Map // *serverAuth → *harness

// harnessClock is the clock shared by the harness's kv store, session store
// and serverAuth.
func harnessClock(sa *serverAuth) *fakeClock {
	h, _ := harnesses.Load(sa)
	return h.(*harness).clock
}

func harnessKV(sa *serverAuth) *kv.Memory {
	h, _ := harnesses.Load(sa)
	return h.(*harness).kv
}

// testSessionKey returns 32 fresh random bytes; keys are never committed.
func testSessionKey(t *testing.T) []byte {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

// newServerAuthHarness starts a fake GitHub and an httptest server whose mux
// routes /api/auth/login and /api/auth/callback to a fresh serverAuth.
func newServerAuthHarness(t *testing.T) (sa *serverAuth, ts *httptest.Server, fake *githubfake.Server) {
	t.Helper()
	fake = githubfake.New(t)
	fake.AddUser(githubfake.User{Login: "alice", ID: 1, Orgs: []string{"acme"}})
	fake.AddUser(githubfake.User{Login: "bob", ID: 2, Orgs: []string{"acme"}})
	fake.AddUser(githubfake.User{Login: "carol", ID: 3, Orgs: []string{"other"}})

	mux := http.NewServeMux()
	ts = httptest.NewUnstartedServer(mux)
	cfg := config.Config{
		AuthMode:        config.AuthServer,
		ClientID:        githubfake.ClientID,
		ClientSecret:    githubfake.ClientSecret,
		WebEndpoint:     fake.URL,
		GraphQLEndpoint: fake.URL + "/graphql",
		RESTEndpoint:    fake.URL,
		PublicURL:       "http://" + ts.Listener.Addr().String(),
		SessionKeys:     [][]byte{testSessionKey(t)},
		SessionIdle:     168 * time.Hour,
		SessionMax:      720 * time.Hour,
		TokenMaxAge:     8 * time.Hour,
	}
	ring, err := seal.NewKeyring(cfg.SessionKeys)
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{t: time.Now()}
	store := kv.NewMemory(clock.Now)
	sessions := session.New(store, ring, cfg.SessionIdle, cfg.SessionMax)
	sessions.SetClock(clock.Now)
	sa = newServerAuth(cfg, sessions, state.NewUserSettings(store, state.DefaultSettings(25)), ring)
	sa.now = clock.Now
	harnesses.Store(sa, &harness{clock: clock, kv: store})
	t.Cleanup(func() { harnesses.Delete(sa) })

	mux.HandleFunc("GET /api/auth/login", sa.handleLogin)
	mux.HandleFunc("GET /api/auth/callback", sa.handleCallback)
	ts.Start()
	t.Cleanup(ts.Close)
	return sa, ts, fake
}

// newBrowser returns a client with a cookie jar that follows redirects through
// sign-in and stops when it would land on a page of the app.
func newBrowser(t *testing.T, ts *httptest.Server) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	app := ts.Listener.Addr().String()
	return &http.Client{
		Jar: jar,
		CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			if req.URL.Host == app && !strings.HasPrefix(req.URL.Path, "/api/auth/") {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
}

// noFollow shares cl's cookies but stops at every redirect.
func noFollow(cl *http.Client) *http.Client {
	return &http.Client{
		Jar:           cl.Jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// signIn walks login → fake authorize → callback with a cookie jar, stops at
// the redirect to "/", and returns the client (now carrying the session
// cookie) and the final Location.
func signIn(t *testing.T, ts *httptest.Server, fake *githubfake.Server, login string) (*http.Client, string) {
	t.Helper()
	cl := newBrowser(t, ts)
	fake.SignInAs(login)
	return cl, walkSignIn(t, cl, ts, "").Header.Get("Location")
}

// walkSignIn runs the round trip with cl and returns the callback's response.
func walkSignIn(t *testing.T, cl *http.Client, ts *httptest.Server, query string) *http.Response {
	t.Helper()
	target := ts.URL + "/api/auth/login"
	if query != "" {
		target += "?" + query
	}
	resp, err := cl.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Request.URL.Path != "/api/auth/callback" {
		t.Fatalf("sign-in stopped at %s with %d", resp.Request.URL, resp.StatusCode)
	}
	return resp
}

func get(t *testing.T, cl *http.Client, target string) *http.Response {
	t.Helper()
	resp, err := cl.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func sessionCookie(t *testing.T, cl *http.Client, ts *httptest.Server) string {
	t.Helper()
	u, _ := url.Parse(ts.URL)
	for _, c := range cl.Jar.Cookies(u) {
		if c.Name == sessionCookieBase {
			return c.Value
		}
	}
	return ""
}

// startLogin follows login to GitHub and back, and returns the callback URL
// the browser was sent to, not yet visited.
func startLogin(t *testing.T, cl *http.Client, ts *httptest.Server) *url.URL {
	t.Helper()
	nf := noFollow(cl)
	authorize := get(t, nf, ts.URL+"/api/auth/login").Header.Get("Location")
	callback, err := url.Parse(get(t, nf, authorize).Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	return callback
}

func TestCookieName(t *testing.T) {
	if got := cookieName("yana-session", true); got != "__Host-yana-session" {
		t.Errorf("secure = %q", got)
	}
	if got := cookieName("yana-session", false); got != "yana-session" {
		t.Errorf("insecure = %q", got)
	}
}

func TestSetCookieAttributesSecure(t *testing.T) {
	sa := newServerAuth(config.Config{PublicURL: "https://yana.example.com"}, nil, nil, nil)
	rec := httptest.NewRecorder()
	sa.setCookie(rec, sessionCookieBase, "v", 60)

	raw := rec.Header().Get("Set-Cookie")
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %v", cookies)
	}
	c := cookies[0]
	if c.Name != "__Host-yana-session" || !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" {
		t.Errorf("cookie = %s", raw)
	}
	if strings.Contains(strings.ToLower(raw), "domain=") {
		t.Errorf("cookie has a Domain: %s", raw)
	}
}

func TestLoginRedirectsToGitHub(t *testing.T) {
	_, ts, fake := newServerAuthHarness(t)
	resp := get(t, noFollow(newBrowser(t, ts)), ts.URL+"/api/auth/login")
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302", resp.StatusCode)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	fu, _ := url.Parse(fake.URL)
	if loc.Host != fu.Host || loc.Path != "/login/oauth/authorize" {
		t.Errorf("location = %s", loc)
	}
	if loc.Query().Get("state") == "" || loc.Query().Get("code_challenge") == "" {
		t.Errorf("location lacks state or code_challenge: %s", loc)
	}
	var oauth *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == oauthCookieBase {
			oauth = c
		}
	}
	if oauth == nil || oauth.MaxAge != 600 {
		t.Fatalf("oauth cookie = %v", oauth)
	}
}

func TestCallbackCreatesSession(t *testing.T) {
	sa, ts, fake := newServerAuthHarness(t)
	cl, loc := signIn(t, ts, fake, "alice")
	if loc != "/" {
		t.Fatalf("location = %q, want /", loc)
	}
	sid := sessionCookie(t, cl, ts)
	if sid == "" {
		t.Fatal("no session cookie")
	}
	rec, err := sa.sessions.Get(context.Background(), sid)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Login != "alice" || rec.UserID != 1 {
		t.Errorf("session = %s (%d)", rec.Login, rec.UserID)
	}
	if d := harnessClock(sa).Now().Sub(rec.TokenIssuedAt); d < 0 || d > time.Second {
		t.Errorf("token issued at %v", rec.TokenIssuedAt)
	}
}

func TestCallbackDenied(t *testing.T) {
	_, ts, fake := newServerAuthHarness(t)
	fake.Deny()
	cl, loc := signIn(t, ts, fake, "alice")
	if loc != "/?authError=denied" {
		t.Fatalf("location = %q", loc)
	}
	if sessionCookie(t, cl, ts) != "" {
		t.Fatal("denied sign-in left a session cookie")
	}
}

var returnPaths = []struct{ in, want string }{
	{"/board/team-x", "/board/team-x"},
	{"//evil.example", "/"},
	{"https://evil.example", "/"},
	{"/a?b=c", "/"},
	{"/a/../b", "/"},
	{"%2F%2Fevil", "/"},
	{"", "/"},
}

func TestLoginReturnPath(t *testing.T) {
	for _, tc := range returnPaths {
		want := tc.want
		if want == "/" {
			want = ""
		}
		if got := safeReturn(tc.in); got != want {
			t.Errorf("safeReturn(%q) = %q, want %q", tc.in, got, want)
		}
	}

	_, ts, fake := newServerAuthHarness(t)
	fake.SignInAs("alice")
	for _, tc := range returnPaths {
		query := ""
		if tc.in != "" {
			query = "return=" + url.QueryEscape(tc.in)
		}
		loc := walkSignIn(t, newBrowser(t, ts), ts, query).Header.Get("Location")
		if loc != tc.want {
			t.Errorf("return=%q landed on %q, want %q", tc.in, loc, tc.want)
		}
	}
}

func TestCallbackStateMismatch(t *testing.T) {
	_, ts, fake := newServerAuthHarness(t)
	fake.SignInAs("alice")
	cl := newBrowser(t, ts)
	callback := startLogin(t, cl, ts)
	q := callback.Query()
	q.Set("state", "tampered")
	callback.RawQuery = q.Encode()
	if loc := get(t, noFollow(cl), callback.String()).Header.Get("Location"); loc != "/?authError=state" {
		t.Fatalf("location = %q", loc)
	}
}

func TestCallbackWithoutOAuthCookie(t *testing.T) {
	_, ts, fake := newServerAuthHarness(t)
	fake.SignInAs("alice")
	callback := startLogin(t, newBrowser(t, ts), ts)
	if loc := get(t, noFollow(newBrowser(t, ts)), callback.String()).Header.Get("Location"); loc != "/?authError=state" {
		t.Fatalf("location = %q", loc)
	}
}

func TestCallbackExpiredRoundTrip(t *testing.T) {
	sa, ts, fake := newServerAuthHarness(t)
	fake.SignInAs("alice")
	cl := newBrowser(t, ts)
	callback := startLogin(t, cl, ts)
	harnessClock(sa).Advance(11 * time.Minute)
	if loc := get(t, noFollow(cl), callback.String()).Header.Get("Location"); loc != "/?authError=state" {
		t.Fatalf("location = %q", loc)
	}
}

func TestCallbackBadCode(t *testing.T) {
	_, ts, fake := newServerAuthHarness(t)
	fake.SignInAs("alice")
	cl := newBrowser(t, ts)
	nf := noFollow(cl)

	first := startLogin(t, cl, ts)
	if loc := get(t, nf, first.String()).Header.Get("Location"); loc != "/" {
		t.Fatalf("first sign-in landed on %q", loc)
	}
	second := startLogin(t, cl, ts)
	replay := *second
	q := replay.Query()
	q.Set("code", first.Query().Get("code"))
	replay.RawQuery = q.Encode()
	if loc := get(t, nf, replay.String()).Header.Get("Location"); loc != "/?authError=exchange" {
		t.Fatalf("location = %q", loc)
	}
}

func TestCallbackNotAllowed(t *testing.T) {
	sa, ts, fake := newServerAuthHarness(t)
	sa.allow = func(context.Context, *github.Client, string) error { return errors.New("not on the list") }
	cl, loc := signIn(t, ts, fake, "alice")
	if loc != "/?authError=not_allowed" {
		t.Fatalf("location = %q", loc)
	}
	if sessionCookie(t, cl, ts) != "" {
		t.Fatal("refused sign-in left a session cookie")
	}
}

func TestCallbackClearsOAuthCookie(t *testing.T) {
	_, ts, fake := newServerAuthHarness(t)
	fake.SignInAs("alice")
	ok := walkSignIn(t, newBrowser(t, ts), ts, "")
	fake.Deny()
	denied := walkSignIn(t, newBrowser(t, ts), ts, "")

	for name, resp := range map[string]*http.Response{"success": ok, "failure": denied} {
		cleared := false
		for _, c := range resp.Cookies() {
			if c.Name == oauthCookieBase && c.MaxAge < 0 {
				cleared = true
			}
		}
		if !cleared {
			t.Errorf("%s: oauth cookie not cleared: %v", name, resp.Header["Set-Cookie"])
		}
	}
}

func TestCallbackSameUserRotatesToken(t *testing.T) {
	sa, ts, fake := newServerAuthHarness(t)
	ctx := context.Background()
	cl, _ := signIn(t, ts, fake, "alice")
	sid := sessionCookie(t, cl, ts)
	oldTok := fake.AccessTokenFor("alice")
	before, err := sa.sessions.Get(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}

	harnessClock(sa).Advance(time.Minute)
	if loc := walkSignIn(t, cl, ts, "").Header.Get("Location"); loc != "/" {
		t.Fatalf("second sign-in landed on %q", loc)
	}
	if n := fake.AuthorizeCount(); n != 2 {
		t.Fatalf("authorize count = %d, want 2", n)
	}
	if got := sessionCookie(t, cl, ts); got != sid {
		t.Fatal("session cookie changed on re-sign-in")
	}
	newTok := fake.AccessTokenFor("alice")
	after, err := sa.sessions.Get(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	if after.Token != newTok || newTok == oldTok {
		t.Error("session does not hold the new token")
	}
	if !after.TokenIssuedAt.After(before.TokenIssuedAt) {
		t.Errorf("TokenIssuedAt %v not after %v", after.TokenIssuedAt, before.TokenIssuedAt)
	}
	revoked := fake.Revoked()
	if !slices.Contains(revoked, oldTok) || slices.Contains(revoked, newTok) {
		t.Errorf("revoked = %v", revoked)
	}
}

func TestCallbackDifferentUserEndsOldSession(t *testing.T) {
	sa, ts, fake := newServerAuthHarness(t)
	cl, _ := signIn(t, ts, fake, "alice")
	aliceSID := sessionCookie(t, cl, ts)
	aliceTok := fake.AccessTokenFor("alice")

	fake.SignInAs("bob")
	walkSignIn(t, cl, ts, "")
	bobSID := sessionCookie(t, cl, ts)
	if bobSID == "" || bobSID == aliceSID {
		t.Fatal("session cookie did not change")
	}
	if _, err := sa.sessions.Get(context.Background(), aliceSID); !errors.Is(err, session.ErrNotFound) {
		t.Errorf("old session err = %v, want ErrNotFound", err)
	}
	if !slices.Contains(fake.Revoked(), aliceTok) {
		t.Error("alice's token was not revoked")
	}
}

func TestSignInNeverLogsSecrets(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	_, ts, fake := newServerAuthHarness(t)
	cl, _ := signIn(t, ts, fake, "alice")
	first := fake.AccessTokenFor("alice")
	walkSignIn(t, cl, ts, "")
	second := fake.AccessTokenFor("alice")
	sid := sessionCookie(t, cl, ts)

	out := buf.String()
	for _, want := range []string{"sign-in: alice", "token rotated: alice"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
	for _, secret := range []string{first, second, sid, githubfake.ClientSecret} {
		if strings.Contains(out, secret) {
			t.Errorf("log contains a secret:\n%s", out)
		}
	}
}
