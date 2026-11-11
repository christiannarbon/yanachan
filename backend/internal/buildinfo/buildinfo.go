// Package buildinfo holds what the build stamped into the binary.
package buildinfo

import "fmt"

// Set with -ldflags -X at build time. Variables, not constants: -X cannot write a const.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// String is what --version prints and the startup log repeats.
func String() string {
	return fmt.Sprintf("yanachan %s (commit %s, built %s)", Version, Commit, Date)
}
