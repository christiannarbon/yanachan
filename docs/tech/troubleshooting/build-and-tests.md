# Troubleshooting: build and tests

```sh
make doctor    # is the toolchain there at all
make deps      # go mod download + npm install
make check     # gofmt, go vet, go test ./..., vue-tsc --noEmit
```

## `make build` fails in the Go step

**Missing embed directory.** `internal/webui/dist` must exist, and contain at
least one file, for `//go:embed all:dist` to compile. Only `.gitkeep` is
tracked there, and it is what makes a clean checkout build.

Note that `make build-frontend` starts with `rm -rf` on that directory, so a
local build **deletes the tracked `.gitkeep`** and leaves the worktree dirty.
`git checkout backend/internal/webui/dist/.gitkeep` puts it back.

**Toolchain mismatch.** `go.mod` declares `go 1.24` and pins
`toolchain go1.25.0`, so a local build, the Docker build and CI agree. If your
Go is older than the toolchain directive can satisfy, upgrade rather than
editing the pin.

## `vue-tsc` fails on a locale key

```
Property 'somethingNew' is missing in type ... but required in type 'Messages'
```

Working as designed. `ja.ts` is typed as the shape of `en.ts`, so a key added to
one language and not the other is a compile error. Add it to both. See
[internationalization](../low-level-design/i18n.md).

The same check catches a string that should be a function: anything with a count
or a date is a function in the catalog, in **both** languages.

## `vue-tsc` fails on an API field

`frontend/src/lib/types.ts` mirrors the backend's JSON by hand — there is no
code generation. If you added a field to a Go struct, add it to the TypeScript
type in the same change.

## Go tests fail after a logic change

The tests here are mostly *characterisation* tests: they pin behaviour that was
ported from a shell script, not behaviour that was designed. A failure means one
of two things, and it is worth knowing which:

| Test | Pins |
| --- | --- |
| `TestResolveWindow` | the business-day rule, every weekday |
| `TestIsBot` | the bot pattern list |
| `TestReviewEntryDetectsReplyAfterYourComment` | the branch order that causes [the inherited quirk](../low-level-design/board-classification.md#the-inherited-quirk) |
| `TestMakeSectionCountsBeforeFiltering` | counts are taken before `onlyActive` drops anything |
| `TestDayKeyBucketsInTheWindowsZone` | day buckets use the window's zone, not UTC |
| `TestRhythmCountsActiveDaysAndTheRun` | a streak survives an empty today |
| `TestAuthStatusNeverLeaksToken` | the token is not in any auth response |
| `TestPersistedFilesAreOwnerOnly` | `0600` on both state files |

If you *meant* to change the behaviour, change the test in the same commit and
say why in the message. If you did not, you have found a regression.

`TestAuthStatusNeverLeaksToken` is the one never to "fix" by adjusting the test.

## The Redis tests skip

`TestRedisConformance` and `TestRedisPrefix` need a real server and skip when
`GHDASH_TEST_REDIS_URL` is not set, so a plain `go test ./...` reports them as
`SKIP`. Start the throwaway Valkey and point the tests at it:

```sh
make valkey-up
make test-redis
make valkey-down
```

The Valkey listens on `127.0.0.1` only, with persistence off. `make down` stops
it too.

## `make valkey-up` says port 16379 is in use

Something else, often a Valkey left over from an earlier run, is on the port.
`make valkey-down` stops the old one; otherwise pick another port and pass it to
both targets:

```sh
VALKEY_PORT=16380 make valkey-up
VALKEY_PORT=16380 make test-redis
```

## Tests pass locally, fail in Docker

The Docker build pins its base images by **digest**, not tag, so it may be on a
different Go patch release than your machine. Refresh a digest deliberately:

```sh
docker buildx imagetools inspect golang:1.25-alpine --format '{{.Manifest.Digest}}'
```

## `npm ci` fails in the image but `npm install` works locally

`npm ci` requires `package-lock.json` to agree with `package.json`. If you added
a dependency with `npm install` and did not commit the updated lockfile, the
image build is the first thing that notices.

## The theme files keep reverting

`art-themes.css`, `art-themes.meta.json` and `art-themes.audit.md` are
**generated**. Edit `scripts/gen-art-themes.mjs` and regenerate:

```sh
cd frontend && node scripts/gen-art-themes.mjs <upstream-checkout>/themes
```

Commit all three together. See [theming system](../low-level-design/theming-system.md).

## `make dev` says something is already running

The dev targets keep pids in `.run/`. If a process died without cleaning up:

```sh
make down
```

Logs are `.run/api.log` and `.run/web.log`; `make logs` tails both.
