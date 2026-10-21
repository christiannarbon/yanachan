module github.com/christiannarbon/yanachan-4k/backend

// The go directive is the toolchain pin, so a local build, the Docker build
// and CI all agree: CI reads it through `go-version-file: backend/go.mod`, and
// setup-go exports GOTOOLCHAIN=local so nothing silently fetches another one.
// A separate `toolchain` line cannot do that job -- once it matches this
// directive, `go mod tidy` deletes it as redundant.
go 1.27.3
