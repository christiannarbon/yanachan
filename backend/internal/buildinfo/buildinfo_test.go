package buildinfo

import (
	"strings"
	"testing"
)

func TestStringDefaults(t *testing.T) {
	if got, want := String(), "yanachan dev (commit none, built unknown)"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestStringStamped(t *testing.T) {
	v, c, d := Version, Commit, Date
	t.Cleanup(func() { Version, Commit, Date = v, c, d })
	Version, Commit, Date = "0.1.0", "abc1234", "2026-11-11T00:00:00Z"
	got := String()
	for _, want := range []string{"0.1.0", "abc1234", "2026-11-11T00:00:00Z"} {
		if !strings.Contains(got, want) {
			t.Errorf("String() = %q, missing %q", got, want)
		}
	}
}
