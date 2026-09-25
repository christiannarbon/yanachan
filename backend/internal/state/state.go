// Package state persists user settings and the authenticated session to disk.
//
// Both files live in the state directory with 0600 permissions: settings.json
// holds the followed teams/orgs and view preferences, session.json holds the
// GitHub token the user approved.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// MaxWindowHours bounds a fixed activity window at 30 days. Beyond roughly
// 292 years the hours-to-Duration multiplication overflows outright, and
// nothing about this dashboard is useful at that range anyway.
const MaxWindowHours = 24 * 30

// MaxRefs bounds how many teams or orgs may be followed. Each one becomes an
// aliased sub-query inside the single batched GraphQL document, so an
// unbounded list turns every refresh into one enormous request.
const MaxRefs = 200

// Settings is the user-controlled part of the dashboard. Teams and orgs start
// empty on purpose: nothing is followed until the user adds it.
type Settings struct {
	Teams       []string `json:"teams"`
	Orgs        []string `json:"orgs"`
	Limit       int      `json:"limit"`
	WindowHours int      `json:"windowHours"` // 0 means "business day" rule
	OnlyActive  bool     `json:"onlyActive"`
	ShowURLs    bool     `json:"showUrls"`
}

func DefaultSettings(limit int) Settings {
	return Settings{
		Teams:       []string{},
		Orgs:        []string{},
		Limit:       limit,
		WindowHours: 0,
		OnlyActive:  false,
		ShowURLs:    true,
	}
}

// Session records how the user authenticated and the token to use.
type Session struct {
	Mode      string `json:"mode"` // "gh-cli" | "oauth" | "env-token"
	Login     string `json:"login"`
	Token     string `json:"token"`
	CreatedAt string `json:"createdAt"`
}

type Store struct {
	mu           sync.RWMutex
	settingsPath string
	sessionPath  string
	settings     Settings
	session      *Session
}

func New(settingsPath, sessionPath string, defaults Settings) (*Store, error) {
	s := &Store{settingsPath: settingsPath, sessionPath: sessionPath, settings: defaults}
	dir := filepath.Dir(settingsPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	// MkdirAll only applies the mode to directories it creates. A state
	// directory that already existed -- the pre-rename github-dashboarder one,
	// or a path handed in through GHDASH_STATE_DIR -- keeps whatever it had,
	// which may well be world-readable. The files inside are 0600 either way,
	// so this is defence in depth and a failure is not worth aborting over.
	_ = os.Chmod(dir, 0o700)
	if err := s.loadSettings(); err != nil {
		return nil, err
	}
	if err := s.loadSession(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) loadSettings() error {
	b, err := os.ReadFile(s.settingsPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read settings: %w", err)
	}
	var loaded Settings
	if err := json.Unmarshal(b, &loaded); err != nil {
		return fmt.Errorf("parse settings: %w", err)
	}
	s.settings = normalize(loaded, s.settings.Limit)
	return nil
}

func (s *Store) loadSession() error {
	b, err := os.ReadFile(s.sessionPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read session: %w", err)
	}
	var sess Session
	if err := json.Unmarshal(b, &sess); err != nil {
		// A corrupt session is not fatal; the user can simply sign in again.
		return nil
	}
	if sess.Token != "" {
		s.session = &sess
	}
	return nil
}

func normalize(in Settings, fallbackLimit int) Settings {
	out := in
	if out.Limit < 1 || out.Limit > 100 {
		out.Limit = fallbackLimit
	}
	if out.WindowHours < 0 || out.WindowHours > MaxWindowHours {
		out.WindowHours = 0
	}
	out.Teams = truncate(dedupe(out.Teams, strings.ToLower), MaxRefs)
	out.Orgs = truncate(dedupe(out.Orgs, strings.ToLower), MaxRefs)
	return out
}

func dedupe(in []string, key func(string) string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		k := key(v)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, v)
	}
	return out
}

// truncate caps a ref list. The API rejects an oversized list outright; this is
// the second line of defence, for a settings.json edited by hand.
func truncate(in []string, max int) []string {
	if len(in) <= max {
		return in
	}
	return in[:max]
}

func (s *Store) Settings() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c := s.settings
	c.Teams = append([]string{}, s.settings.Teams...)
	c.Orgs = append([]string{}, s.settings.Orgs...)
	return c
}

func (s *Store) SaveSettings(in Settings) (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings = normalize(in, s.settings.Limit)
	if err := writeJSON(s.settingsPath, s.settings); err != nil {
		return s.settings, err
	}
	return s.settings, nil
}

func (s *Store) Session() *Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.session == nil {
		return nil
	}
	c := *s.session
	return &c
}

func (s *Store) SaveSession(sess Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.session = &sess
	return writeJSON(s.sessionPath, sess)
}

func (s *Store) ClearSession() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.session = nil
	if err := os.Remove(s.sessionPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	// A fresh, randomly named file opened O_EXCL at 0600: a leftover temp file or a
	// symlink in its place is never written through.
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	// Without it a crash just after the rename can leave an empty file.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
