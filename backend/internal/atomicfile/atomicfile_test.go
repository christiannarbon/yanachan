package atomicfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type payload struct {
	Token string `json:"token"`
}

func TestWriteJSONIsOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	if err := WriteJSON(path, payload{Token: "ghp_secret"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %o, want 600", got)
	}
}

func TestWriteJSONLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	if err := WriteJSON(filepath.Join(dir, "session.json"), payload{Token: "ghp_secret"}); err != nil {
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

func TestWriteJSONIgnoresALeftoverTempFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	if err := os.WriteFile(path+".tmp", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path+".tmp", 0o644); err != nil {
		t.Fatal(err)
	}

	if err := WriteJSON(path, payload{Token: "ghp_secret"}); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o, want 600", got)
	}
}

func TestWriteJSONDoesNotFollowATempPathSymlink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	elsewhere := filepath.Join(t.TempDir(), "collected")
	if err := os.WriteFile(elsewhere, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, path+".tmp"); err != nil {
		t.Fatal(err)
	}

	if err := WriteJSON(path, payload{Token: "ghp_secret"}); err != nil {
		t.Fatal(err)
	}

	if b, _ := os.ReadFile(elsewhere); len(b) != 0 {
		t.Fatalf("written through the symlink: %q", b)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("the target is now a symlink")
	}
}
