package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/christiannarbon/yanachan-4k/backend/internal/config"
	"github.com/christiannarbon/yanachan-4k/backend/internal/githubfake"
	"github.com/christiannarbon/yanachan-4k/backend/internal/kv"
)

// startServerMode serves the full server-mode handler over store. edit, when
// set, adjusts the config before the server is built.
func startServerMode(t *testing.T, store kv.Store, edit func(*config.Config)) (*Server, *httptest.Server, *githubfake.Server) {
	t.Helper()
	fake := newFakeGitHub(t)
	ts := httptest.NewUnstartedServer(nil)
	cfg := serverTestConfig(t, fake, "http://"+ts.Listener.Addr().String())
	if edit != nil {
		edit(&cfg)
	}
	s, err := newServerModeWith(cfg, store, nil, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	ts.Config.Handler = s.Handler()
	ts.Start()
	t.Cleanup(ts.Close)
	return s, ts, fake
}

// newServerModeHarness starts the full server-mode handler over kv.NewMemory.
// The Server's kv field holds the store for tests that inspect keys.
func newServerModeHarness(t *testing.T) (ts *httptest.Server, fake *githubfake.Server, cfg config.Config) {
	t.Helper()
	s, ts, fake := startServerMode(t, kv.NewMemory(time.Now), nil)
	return ts, fake, s.cfg
}

func call(t *testing.T, cl *http.Client, method, target, body string, headers map[string]string) (int, string) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, target, rd)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(b)
}

func decodeStatus(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return m
}

func TestServerModeLocalRoutesNotRegistered(t *testing.T) {
	ts, _, _ := newServerModeHarness(t)
	for _, path := range []string{"gh-cli/approve", "env-token/approve", "device/start", "device/poll"} {
		code, body := call(t, http.DefaultClient, http.MethodPost, ts.URL+"/api/auth/"+path, "", map[string]string{"Origin": ts.URL})
		if code != http.StatusNotFound || !strings.Contains(body, "no such endpoint") {
			t.Errorf("%s = %d %s", path, code, body)
		}
	}
}

func TestServerModeStatusSignedOut(t *testing.T) {
	ts, _, _ := newServerModeHarness(t)
	_, body := call(t, http.DefaultClient, http.MethodGet, ts.URL+"/api/auth/status", "", nil)
	st := decodeStatus(t, body)
	if st["authMode"] != "server" || st["authenticated"] != false || st["loginUrl"] != "/api/auth/login" ||
		st["ghCliAllowed"] != false || st["envTokenAvailable"] != false {
		t.Fatalf("status = %s", body)
	}
}

func TestServerModeStatusSignedIn(t *testing.T) {
	ts, fake, _ := newServerModeHarness(t)
	cl, _ := signIn(t, ts, fake, "alice")
	_, body := call(t, cl, http.MethodGet, ts.URL+"/api/auth/status", "", nil)
	sess, _ := decodeStatus(t, body)["session"].(map[string]any)
	if sess["mode"] != "github" || sess["login"] != "alice" {
		t.Fatalf("status = %s", body)
	}
}

func TestServerModeRequiresSession(t *testing.T) {
	ts, _, _ := newServerModeHarness(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/board"},
		{http.MethodGet, "/api/stats"},
		{http.MethodGet, "/api/suggestions"},
		{http.MethodGet, "/api/settings"},
		{http.MethodPut, "/api/settings"},
	} {
		code, body := call(t, http.DefaultClient, tc.method, ts.URL+tc.path, `{"orgs":["acme"]}`, nil)
		if code != http.StatusUnauthorized {
			t.Errorf("%s %s = %d %s", tc.method, tc.path, code, body)
		}
	}
}

func TestServerModeBogusCookie(t *testing.T) {
	ts, _, _ := newServerModeHarness(t)
	code, _ := call(t, http.DefaultClient, http.MethodGet, ts.URL+"/api/settings", "",
		map[string]string{"Cookie": sessionCookieBase + "=made-up-value"})
	if code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", code)
	}
}

func TestServerModeSettingsPerUser(t *testing.T) {
	s, ts, fake := startServerMode(t, kv.NewMemory(time.Now), nil)
	alice, _ := signIn(t, ts, fake, "alice")
	bob, _ := signIn(t, ts, fake, "bob")

	if code, body := call(t, alice, http.MethodPut, ts.URL+"/api/settings", `{"orgs":["acme"]}`, nil); code != http.StatusOK {
		t.Fatalf("alice PUT = %d %s", code, body)
	}
	_, body := call(t, bob, http.MethodGet, ts.URL+"/api/settings", "", nil)
	if orgs, _ := decodeStatus(t, body)["orgs"].([]any); len(orgs) != 0 {
		t.Errorf("bob sees orgs %v", orgs)
	}
	_, body = call(t, alice, http.MethodGet, ts.URL+"/api/settings", "", nil)
	if orgs, _ := decodeStatus(t, body)["orgs"].([]any); len(orgs) != 1 || orgs[0] != "acme" {
		t.Errorf("alice sees orgs %v", orgs)
	}

	ctx := context.Background()
	if _, err := s.kv.Get(ctx, "user:1:settings"); err != nil {
		t.Errorf("user:1:settings: %v", err)
	}
	if _, err := s.kv.Get(ctx, "user:2:settings"); !errors.Is(err, kv.ErrNotFound) {
		t.Errorf("user:2:settings err = %v, want ErrNotFound", err)
	}
}

func TestServerModeUsesOwnToken(t *testing.T) {
	ts, fake, _ := newServerModeHarness(t)
	alice, _ := signIn(t, ts, fake, "alice")
	bob, _ := signIn(t, ts, fake, "bob")
	for login, cl := range map[string]*http.Client{"alice": alice, "bob": bob} {
		if code, body := call(t, cl, http.MethodGet, ts.URL+"/api/suggestions", "", nil); code != http.StatusOK {
			t.Fatalf("%s suggestions = %d %s", login, code, body)
		}
		seen := fake.TokensSeen()
		if last := seen[len(seen)-1]; last != fake.AccessTokenFor(login) {
			t.Errorf("%s's request used someone else's token", login)
		}
	}
}

func TestServerModeOriginGuard(t *testing.T) {
	ts, fake, _ := newServerModeHarness(t)
	alice, _ := signIn(t, ts, fake, "alice")
	code, _ := call(t, alice, http.MethodPut, ts.URL+"/api/settings", `{"orgs":["acme"]}`,
		map[string]string{"Origin": "https://evil.example"})
	if code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", code)
	}
}

func TestServerModePublicHostAllowed(t *testing.T) {
	s, _, _ := startServerMode(t, kv.NewMemory(time.Now), func(c *config.Config) {
		c.PublicURL = "https://yana.example.com"
	})
	h := s.Handler()
	if w := do(t, h, http.MethodGet, "/api/auth/status", "yana.example.com", nil, ""); w.Code == http.StatusForbidden {
		t.Errorf("public host refused: %s", w.Body)
	}
	if w := do(t, h, http.MethodGet, "/api/auth/status", "other.example.com", nil, ""); w.Code != http.StatusForbidden {
		t.Errorf("other host = %d, want 403", w.Code)
	}
}

func TestServerModeHSTS(t *testing.T) {
	const header = "Strict-Transport-Security"
	secure, _, _ := startServerMode(t, kv.NewMemory(time.Now), func(c *config.Config) {
		c.PublicURL = "https://yana.example.com"
	})
	if got := do(t, secure.Handler(), http.MethodGet, "/api/health", "yana.example.com", nil, "").Header().Get(header); got != "max-age=31536000" {
		t.Errorf("https server mode: %s = %q", header, got)
	}
	plain, _, _ := startServerMode(t, kv.NewMemory(time.Now), nil)
	if got := do(t, plain.Handler(), http.MethodGet, "/api/health", "127.0.0.1", nil, "").Header().Get(header); got != "" {
		t.Errorf("http server mode: %s = %q", header, got)
	}
	local := newTestHandler(t, config.Config{})
	if got := do(t, local, http.MethodGet, "/api/health", "127.0.0.1", nil, "").Header().Get(header); got != "" {
		t.Errorf("local mode: %s = %q", header, got)
	}
}

func TestServerModeLogoutEndsSession(t *testing.T) {
	s, ts, fake := startServerMode(t, kv.NewMemory(time.Now), nil)
	alice, _ := signIn(t, ts, fake, "alice")
	sid := sessionCookie(t, alice, ts)

	if code, body := call(t, alice, http.MethodPost, ts.URL+"/api/auth/logout", "", nil); code != http.StatusOK {
		t.Fatalf("logout = %d %s", code, body)
	}
	if code, _ := call(t, alice, http.MethodGet, ts.URL+"/api/settings", "", nil); code != http.StatusUnauthorized {
		t.Errorf("settings after logout = %d, want 401", code)
	}
	if _, err := s.server.sessions.Get(context.Background(), sid); err == nil {
		t.Error("session still resolves after logout")
	}
}

func TestServerModeNeverLeaksToken(t *testing.T) {
	ts, fake, _ := newServerModeHarness(t)
	alice, _ := signIn(t, ts, fake, "alice")
	token := fake.AccessTokenFor("alice")
	sid := sessionCookie(t, alice, ts)
	for _, path := range []string{"/api/auth/status", "/api/settings", "/api/suggestions"} {
		_, body := call(t, alice, http.MethodGet, ts.URL+path, "", nil)
		if strings.Contains(body, token) || strings.Contains(body, sid) {
			t.Errorf("%s leaks a secret: %s", path, body)
		}
	}
}

func TestLocalModeStatusReportsAuthMode(t *testing.T) {
	w := do(t, newTestHandler(t, config.Config{}), http.MethodGet, "/api/auth/status", "127.0.0.1", nil, "")
	st := decodeStatus(t, w.Body.String())
	if st["authMode"] != "local" {
		t.Errorf("authMode = %v", st["authMode"])
	}
	if _, ok := st["loginUrl"]; ok {
		t.Error("local status has a loginUrl")
	}
}

type flakyKV struct {
	*kv.Memory
	down atomic.Bool
}

func (f *flakyKV) Ping(ctx context.Context) error {
	if f.down.Load() {
		return errors.New("dial tcp 10.0.0.9:6379: connection refused")
	}
	return nil
}

func TestReadyReflectsKV(t *testing.T) {
	store := &flakyKV{Memory: kv.NewMemory(time.Now)}
	s, _, _ := startServerMode(t, store, nil)
	h := s.Handler()
	check := func(path string, want int) {
		t.Helper()
		w := do(t, h, http.MethodGet, path, "unknown.example", nil, "")
		if w.Code != want {
			t.Errorf("%s = %d, want %d", path, w.Code, want)
		}
		if strings.Contains(w.Body.String(), "refused") {
			t.Errorf("%s leaks the error: %s", path, w.Body)
		}
	}
	check("/api/ready", http.StatusOK)
	check("/api/health", http.StatusOK)
	store.down.Store(true)
	check("/api/ready", http.StatusServiceUnavailable)
	check("/api/health", http.StatusOK)
	store.down.Store(false)
	check("/api/ready", http.StatusOK)

	if w := do(t, newTestHandler(t, config.Config{}), http.MethodGet, "/api/ready", "127.0.0.1", nil, ""); w.Code != http.StatusOK {
		t.Errorf("local ready = %d", w.Code)
	}
}

func TestServerModeWritesNoFiles(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOME", dir)
	_, ts, fake := startServerMode(t, kv.NewMemory(time.Now), func(c *config.Config) {
		c.StateDir = filepath.Join(dir, "state")
	})

	alice, _ := signIn(t, ts, fake, "alice")
	bob, _ := signIn(t, ts, fake, "bob")
	for _, cl := range []*http.Client{alice, bob} {
		call(t, cl, http.MethodPut, ts.URL+"/api/settings", `{"orgs":["acme"]}`, nil)
		call(t, cl, http.MethodGet, ts.URL+"/api/board", "", nil)
		call(t, cl, http.MethodPost, ts.URL+"/api/auth/logout", "", nil)
	}

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != dir {
			t.Errorf("server mode wrote %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNewServerModeFailsWithoutRedis(t *testing.T) {
	const password = "marker-password-do-not-print"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := NewServerMode(ctx, config.Config{
		AuthMode:      config.AuthServer,
		RedisURL:      "redis://127.0.0.1:1/0",
		RedisPassword: password,
		SessionKeys:   [][]byte{testSessionKey(t)},
	}, nil)
	if err == nil {
		t.Fatal("NewServerMode succeeded without redis")
	}
	if !strings.Contains(err.Error(), "redis") || strings.Contains(err.Error(), password) {
		t.Fatalf("err = %v", err)
	}
}
