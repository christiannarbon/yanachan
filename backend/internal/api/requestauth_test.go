package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/christiannarbon/yanachan-4k/backend/internal/config"
	"github.com/christiannarbon/yanachan-4k/backend/internal/github"
	"github.com/christiannarbon/yanachan-4k/backend/internal/state"
)

type memSettings struct {
	mu     sync.Mutex
	s      state.Settings
	getErr error
}

func (m *memSettings) Get(ctx context.Context) (state.Settings, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getErr != nil {
		return state.Settings{}, m.getErr
	}
	return m.s, nil
}

func (m *memSettings) Save(ctx context.Context, in state.Settings) (state.Settings, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.s = in
	return in, nil
}

type stubAuth struct {
	settings  *memSettings
	clientErr error
}

func (s *stubAuth) Client(r *http.Request) (*github.Client, viewer, error) {
	if s.clientErr != nil {
		return nil, viewer{}, s.clientErr
	}
	return nil, viewer{}, errors.New("stub has no client")
}

func (s *stubAuth) Settings(r *http.Request) (settingsStore, error) {
	return s.settings, nil
}

func newStubbedHandler(t *testing.T, ra requestAuth) (http.Handler, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := state.New(
		filepath.Join(dir, "settings.json"),
		filepath.Join(dir, "session.json"),
		state.DefaultSettings(25),
	)
	if err != nil {
		t.Fatalf("state.New: %v", err)
	}
	srv := NewServer(config.Config{Addr: "127.0.0.1:19080"}, store, nil)
	srv.ra = ra
	return srv.Handler(), dir
}

const testHost = "127.0.0.1:19080"

func TestHandlersGoThroughRequestAuth(t *testing.T) {
	mem := &memSettings{s: state.DefaultSettings(25)}
	stub := &stubAuth{settings: mem, clientErr: errNoSession}
	h, dir := newStubbedHandler(t, stub)

	body := `{"teams":["acme/web"],"orgs":["acme"],"limit":10,"windowHours":0,"onlyActive":false,"showUrls":true}`
	if w := do(t, h, http.MethodPut, "/api/settings", testHost, nil, body); w.Code != http.StatusOK {
		t.Fatalf("PUT /api/settings = %d: %s", w.Code, w.Body)
	}
	if got := mem.s; got.Limit != 10 || len(got.Orgs) != 1 || got.Orgs[0] != "acme" {
		t.Fatalf("stub settings = %+v, want the PUT body", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "settings.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("settings.json was written to disk: %v", err)
	}

	w := do(t, h, http.MethodGet, "/api/settings", testHost, nil, "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/settings = %d: %s", w.Code, w.Body)
	}
	var got state.Settings
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Limit != 10 || len(got.Teams) != 1 || got.Teams[0] != "acme/web" {
		t.Fatalf("GET /api/settings = %+v, want the stub's settings", got)
	}

	for _, path := range []string{"/api/board", "/api/stats", "/api/suggestions"} {
		if w := do(t, h, http.MethodGet, path, testHost, nil, ""); w.Code != http.StatusUnauthorized {
			t.Errorf("GET %s = %d, want 401", path, w.Code)
		}
	}

	mem.getErr = errors.New("redis is down")
	w = do(t, h, http.MethodGet, "/api/settings", testHost, nil, "")
	if w.Code != http.StatusBadGateway {
		t.Fatalf("GET /api/settings with a failing store = %d, want 502", w.Code)
	}
	if !strings.Contains(w.Body.String(), "redis is down") {
		t.Fatalf("body = %s, want the store's error", w.Body)
	}
}

func TestLocalSettingsNeedNoSession(t *testing.T) {
	h := newTestHandler(t, config.Config{Addr: testHost})

	if w := do(t, h, http.MethodGet, "/api/settings", testHost, nil, ""); w.Code != http.StatusOK {
		t.Fatalf("GET /api/settings = %d: %s", w.Code, w.Body)
	}
	body := `{"teams":[],"orgs":["acme"],"limit":25,"windowHours":0,"onlyActive":false,"showUrls":true}`
	if w := do(t, h, http.MethodPut, "/api/settings", testHost, nil, body); w.Code != http.StatusOK {
		t.Fatalf("PUT /api/settings = %d: %s", w.Code, w.Body)
	}
}
