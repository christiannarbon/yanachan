package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := New(filepath.Join(dir, "settings.json"), filepath.Join(dir, "session.json"), DefaultSettings(25))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s, dir
}

// The session file holds a live GitHub token, so its mode is load-bearing.
func TestPersistedFilesAreOwnerOnly(t *testing.T) {
	s, dir := newTestStore(t)

	if err := s.SaveSession(Session{Mode: "oauth", Login: "octocat", Token: "ghp_secret"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveSettings(DefaultSettings(25)); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"session.json", "settings.json"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("%s mode = %o, want 600", name, got)
		}
	}

	if info, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	} else if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("state dir mode = %o, want 700", got)
	}
}

// A write must not leave a half-written file, nor a .tmp file holding the token
// after the rename.
func TestSaveLeavesNoTempFile(t *testing.T) {
	s, dir := newTestStore(t)
	if err := s.SaveSession(Session{Mode: "oauth", Login: "octocat", Token: "ghp_secret"}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

func TestClearSessionRemovesTheFile(t *testing.T) {
	s, dir := newTestStore(t)
	if err := s.SaveSession(Session{Mode: "oauth", Login: "octocat", Token: "ghp_secret"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearSession(); err != nil {
		t.Fatal(err)
	}
	if s.Session() != nil {
		t.Error("Session() still returns a session after ClearSession")
	}
	if _, err := os.Stat(filepath.Join(dir, "session.json")); !os.IsNotExist(err) {
		t.Errorf("session.json still on disk: %v", err)
	}
	// Signing out twice must not error.
	if err := s.ClearSession(); err != nil {
		t.Errorf("second ClearSession: %v", err)
	}
}

// Session() hands back a copy; a caller mutating it must not reach the store.
func TestSessionReturnsCopy(t *testing.T) {
	s, _ := newTestStore(t)
	if err := s.SaveSession(Session{Mode: "oauth", Login: "octocat", Token: "ghp_secret"}); err != nil {
		t.Fatal(err)
	}
	got := s.Session()
	got.Token = "tampered"
	if s.Session().Token != "ghp_secret" {
		t.Error("mutating the returned session changed the stored one")
	}
}

func TestCorruptSessionIsNotFatal(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "session.json")
	if err := os.WriteFile(sessionPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := New(filepath.Join(dir, "settings.json"), sessionPath, DefaultSettings(25))
	if err != nil {
		t.Fatalf("New with a corrupt session: %v", err)
	}
	if s.Session() != nil {
		t.Error("want no session from corrupt JSON")
	}
}

func TestNormalize(t *testing.T) {
	tooMany := make([]string, MaxRefs+50)
	for i := range tooMany {
		tooMany[i] = "org" + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}

	cases := []struct {
		name  string
		in    Settings
		check func(t *testing.T, got Settings)
	}{
		{
			name: "limit out of range falls back",
			in:   Settings{Limit: 5000},
			check: func(t *testing.T, got Settings) {
				if got.Limit != 25 {
					t.Errorf("Limit = %d, want the 25 fallback", got.Limit)
				}
			},
		},
		{
			name: "window beyond the cap resets to the business-day rule",
			in:   Settings{WindowHours: MaxWindowHours + 1},
			check: func(t *testing.T, got Settings) {
				if got.WindowHours != 0 {
					t.Errorf("WindowHours = %d, want 0", got.WindowHours)
				}
			},
		},
		{
			name: "refs are capped",
			in:   Settings{Orgs: tooMany},
			check: func(t *testing.T, got Settings) {
				if len(got.Orgs) != MaxRefs {
					t.Errorf("len(Orgs) = %d, want %d", len(got.Orgs), MaxRefs)
				}
			},
		},
		{
			name: "duplicates and blanks are dropped, case-insensitively",
			in:   Settings{Orgs: []string{"Acme", "acme", "  ", "beta"}},
			check: func(t *testing.T, got Settings) {
				if len(got.Orgs) != 2 || got.Orgs[0] != "Acme" || got.Orgs[1] != "beta" {
					t.Errorf("Orgs = %v, want [Acme beta]", got.Orgs)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.check(t, normalize(tc.in, 25))
		})
	}
}

func TestSettingsRoundTripThroughDisk(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	sessionPath := filepath.Join(dir, "session.json")

	s, err := New(settingsPath, sessionPath, DefaultSettings(25))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveSettings(Settings{Teams: []string{"acme/platform"}, Orgs: []string{"acme"}, Limit: 50, WindowHours: 12}); err != nil {
		t.Fatal(err)
	}

	reopened, err := New(settingsPath, sessionPath, DefaultSettings(25))
	if err != nil {
		t.Fatal(err)
	}
	got := reopened.Settings()
	if got.Limit != 50 || got.WindowHours != 12 {
		t.Errorf("limit/window not persisted: %+v", got)
	}
	if len(got.Teams) != 1 || got.Teams[0] != "acme/platform" {
		t.Errorf("Teams = %v", got.Teams)
	}
}

// A temp file left behind with a wider mode, or a symlink planted in its place, must
// never decide where the token goes or who can read it.
func TestALeftoverTempFileCannotWidenTheSessionMode(t *testing.T) {
	s, dir := newTestStore(t)
	session := filepath.Join(dir, "session.json")
	if err := os.WriteFile(session+".tmp", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(session+".tmp", 0o644); err != nil {
		t.Fatal(err)
	}

	if err := s.SaveSession(Session{Mode: "oauth", Login: "octocat", Token: "ghp_secret"}); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(session)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("session.json mode = %o, want 600", got)
	}
}

func TestATempPathSymlinkIsNotWrittenThrough(t *testing.T) {
	s, dir := newTestStore(t)
	session := filepath.Join(dir, "session.json")
	elsewhere := filepath.Join(t.TempDir(), "collected")
	if err := os.WriteFile(elsewhere, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, session+".tmp"); err != nil {
		t.Fatal(err)
	}

	if err := s.SaveSession(Session{Mode: "oauth", Login: "octocat", Token: "ghp_secret"}); err != nil {
		t.Fatal(err)
	}

	if b, _ := os.ReadFile(elsewhere); len(b) != 0 {
		t.Fatalf("the token was written through the symlink: %q", b)
	}
	info, err := os.Lstat(session)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("session.json is now a symlink")
	}
}
