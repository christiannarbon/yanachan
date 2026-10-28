module github.com/christiannarbon/yanachan-4k/backend

// The go directive is the toolchain pin, so a local build, the Docker build
// and CI all agree: CI reads it through `go-version-file: backend/go.mod`, and
// setup-go exports GOTOOLCHAIN=local so nothing silently fetches another one.
// A separate `toolchain` line cannot do that job -- once it matches this
// directive, `go mod tidy` deletes it as redundant.
go 1.27.3

require github.com/redis/go-redis/v9 v9.23.0

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	go.uber.org/atomic v1.12.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
)
