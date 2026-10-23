// Package atomicfile writes small JSON files so that a reader never sees a
// half-written file and a planted temp file or symlink is never written through.
package atomicfile

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// WriteJSON marshals v with two-space indentation and replaces path with it,
// 0600, via a fresh O_EXCL temp file in the same directory.
func WriteJSON(path string, v any) error {
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
