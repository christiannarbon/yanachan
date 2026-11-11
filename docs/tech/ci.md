# Continuous integration

`.github/workflows/ci.yml`

Runs on every pull request to `main`, every push to `main`, and on demand
(`workflow_dispatch`). Permissions are `contents: read` — nothing in the
pipeline writes to the repository, and the image job builds without pushing.

A new push to a pull request cancels the previous run; runs on `main` are left
alone so the record of what passed stays intact.

## The jobs

| Job | What it proves |
| --- | --- |
| **Backend** | `gofmt` clean, `go vet` clean, `go test -race` green |
| **Redis/Valkey** | the `kv` conformance suite and the server-mode end-to-end tests pass against a real Valkey with a password, and the binary boots in server mode |
| **Frontend** | the lockfile installs, `vue-tsc` passes, the unit tests pass, the bundle builds |
| **Vulnerabilities** | `govulncheck` finds no reachable vulnerability in the standard library or go-redis |
| **Build and smoke test** | `make build` produces a binary that serves the embedded app correctly |
| **Kubernetes manifests** | both overlays render, schema-check, and keep their security posture |
| **Container image** | the image builds for amd64 and arm64, both start, it is labelled and licensed, it runs in local and server mode, it has no fixable critical vulnerability, and it has not grown |
| **Documentation** | every internal link in the Markdown resolves |

They run in parallel. Nothing depends on anything else.

## Why each one is there

**Backend.** `-race` because `state.Store` is the only shared mutable state in
the process, guarded by an `RWMutex`, and a lost lock there is exactly the bug a
serial test run passes.

**Redis/Valkey.** The backend job runs the `kv` conformance suite and the
server-mode end-to-end tests against the in-memory store; this one runs both
against a real Valkey, pinned by digest to the same image as `make valkey-up`.
Valkey is started with `docker run` rather than as a service container, because
a service cannot be given a command line: this one has a random per-run
password and no persistence, like a real deployment's should. Without
`GHDASH_TEST_REDIS_URL` the `kv` tests skip and the end-to-end tests fall back
to memory, and both are green, so a step fails the job unless
`TestRedisConformance` passed and the end-to-end log says `e2e store: redis`.
Then `scripts/smoke-server-mode.sh` boots the binary in server mode against the
same Valkey and checks, over HTTP, that a half-configured start and a wrong
Redis password both refuse to start, that sign-in redirects to GitHub, that the
local-only routes are gone, and that `/api/ready` answers `503` within its one
second while Valkey is paused and recovers afterwards, while `/api/health`
stays `200`.

**Frontend.** `npm ci` rather than `npm install`, so a `package.json` that has
drifted from the lockfile fails here instead of in the image build. The type
check runs as its own step even though `npm run build` type-checks first,
because it is where a locale key added to `en.ts` and not `ja.ts` fails, and
that deserves to report as a type error. See [i18n](low-level-design/i18n.md).
The unit tests (`npm test`, vitest) cover the sign-in logic that has no server
to lean on: when to renew through GitHub, when to stop and offer a way out, and
reading `?authError=` off the address bar.

**Vulnerabilities.** This scans the standard library at the toolchain pinned
in `go.mod`, and go-redis, the one third-party module, which holds the session
store's connection. The standard library side is not a small surface either: the
GitHub token travels through `crypto/tls`, `crypto/x509` and `net/http`.
`govulncheck` reports only what the code actually reaches, which is what makes
it worth gating on.

**Build and smoke test.** The two halves compile in separate jobs; neither
proves the thing this project ships, which is one binary with the Vue bundle
inside it. So this job runs `make build`, starts the binary, and asserts over
HTTP that health answers, the frontend is embedded, an unmatched API path
answers a JSON 404, `/api/board` refuses an unauthenticated caller, the
[Host allowlist](architecture/security.md#guard-3--the-host-header-is-pinned-to-loopback)
refuses an unknown name, and health is exempt from that allowlist.

The embed assertion is not redundant with a successful compile: only `.gitkeep`
is tracked in `backend/internal/webui/dist`, so a binary built without a staged
bundle compiles fine and then answers `frontend bundle is missing`.

The last two assertions cover the DNS-rebinding guard end to end. It has unit
tests at the handler level; this is the one place it is exercised in the built
binary.

**Kubernetes manifests.** `kustomize` and `kubeconform` are downloaded at pinned
versions rather than taken from the runner image, because what is preinstalled
there changes without notice. Both overlays are rendered — `prod` is the one
nobody renders by hand, so it is the one that quietly breaks.

Beyond the schema, two assertions about things that fail silently: the
[pod hardening](../usage/kubernetes.md#pod-hardening) survives, and neither
overlay grows an `Ingress`, a `LoadBalancer` or a `NodePort`. The pod holds a
GitHub token and has no login of its own, so ClusterIP is a decision rather than
a default.

**Container image.** The only job that exercises the digest-pinned base images,
and the only one that sees what a release ships. In order:

- **Both platforms build** from one `buildx` call, as a release would build
  them. Nothing is loaded or pushed; the later steps load one platform at a time
  from the same cache. The build args are fixed (`VERSION=ci`, the commit, a
  1970 build date) so the label layer, and with it the cache, stays the same
  between runs.
- **The amd64 container behaves:** health answers, the frontend is embedded, it
  runs as uid 10001 (a rebuild that drops the `USER` line passes every other
  check in the file), `GHDASH_ALLOW_GH_CLI` is still `false`, and `--version`
  says `yanachan ci`.
- **Labels and licence:** all nine `org.opencontainers.image.*` labels are set,
  and `LICENSE` and `THIRD_PARTY_NOTICES.md` are in
  `/usr/share/licenses/yanachan/`. GHCR links a package to its repository
  through the `source` label, and the licence has to travel with every copy.
- **Server mode from the same image.** The image defaults to local mode, but a
  cluster runs it in server mode, so the job proves that too, against a
  throwaway Valkey on a private Docker network (`VALKEY_IMAGE`): a
  half-configured start refuses with `config:` lines; a fully configured start
  with no Redis refuses and says Redis; with Valkey up, `/api/ready` is `200`
  and `/api/auth/status` says `"authMode":"server"`; none of the three logs
  contains the session key, the Redis password or the client secret; with
  Valkey stopped, `/api/ready` is `503` while `/api/health` stays `200` and the
  process keeps running. The session key and password are generated per run
  and masked; the client ID and secret are placeholders, because nothing here
  talks to GitHub.
- **Size budget: 35 MiB.** Measured on 2026-11-11 at **22 MiB** (Alpine and its
  certificates about 13 MiB, the binary about 9 MiB), so the budget is roughly
  1.5×. It is measured with `du` inside the container rather than
  `docker image inspect .Size`, because that number depends on the image store:
  with the containerd store it can report compressed sizes. A sudden jump
  usually means a build tool or `node_modules` leaked into the runtime stage.
- **Vulnerability scan:** Trivy, over the Alpine packages and the Go binary.
  It fails only on a **CRITICAL** finding **with a fix available**. A HIGH, or
  anything without a fix, is printed but does not fail: nobody can act on a
  finding with no fix, and a gate that is red for reasons nobody can act on
  gets switched off. `govulncheck` remains the sharper gate for Go code,
  because it knows which vulnerable functions are actually reached.
- **arm64 starts.** The runners are amd64, so the arm64 image runs under QEMU.
  That is slow for building but fine for starting a Go binary: the build itself
  cross-compiles, and QEMU is only there for this step, which checks the
  image's architecture, `--version` and health.

**Documentation.** `make docs-check`, the same command you can run locally.

## Running the same checks locally

```sh
make check         # gofmt, go vet, go test, vue-tsc, and the doc links
make docs-check    # just the documentation links
make build         # what the build job builds
make k8s-validate  # renders and schema-checks both overlays
make docker-build  # the image
make valkey-up && make test-redis && make valkey-down   # the Redis/Valkey job
```

`make check` is the closest single command to what CI runs. It does not include
`govulncheck` or the smoke tests; those are:

```sh
cd backend && go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
```

## Pinned versions

Everything the pipeline installs is pinned, so a green tick means the same check
it meant last week.

| Tool | Version | Where |
| --- | --- | --- |
| Go | from `backend/go.mod` | the `toolchain` line is the single pin |
| Node | 24 | matching the Dockerfile's builder |
| govulncheck | `v1.9.0` | workflow |
| kustomize | `v5.4.3` | workflow |
| kubeconform | `v0.6.7` | workflow, `-kubernetes-version 1.33.0` |
| Valkey | `9.1.3-alpine`, by digest | `VALKEY_IMAGE` in the workflow's `env:` and in the Makefile; not seen by Dependabot, so refresh both by hand |
| Trivy | `aquasecurity/trivy-action` `v0.36.0`, by commit SHA | workflow; the action pins the Trivy release it installs |
| QEMU | `docker/setup-qemu-action` `v4.4.0`, by commit SHA | workflow |
| vitest | `5.0.5` | `frontend/package-lock.json` |
| Actions | by commit SHA, version in a trailing comment | workflow |

[Dependabot](../../.github/dependabot.yml) moves the action pins weekly, the
Dockerfile's base image digests weekly, and the frontend packages monthly as one
grouped pull request. The `gomod` entry moves go-redis weekly; the Go
toolchain itself is still moved by hand.

## Artefacts

The build job uploads the Linux binary as `yanachan-linux-amd64`, kept for
seven days. Useful for reproducing a smoke-test failure without building
locally.

## Troubleshooting a red run

Most failures map onto an existing guide, since every job runs a command you can
run yourself:

| Job | Start at |
| --- | --- |
| Backend, Redis/Valkey, Frontend, Build | [build and tests](troubleshooting/build-and-tests.md) |
| Kubernetes manifests | [Kubernetes](troubleshooting/kubernetes.md) |
| Container image | [Docker](../usage/docker.md) |
| Documentation | the checker names the file and the link |

`govulncheck` failing usually means the toolchain pin has aged. Its output names
the fixed-in version; bump `toolchain` in `backend/go.mod` and check the
Dockerfile's builder digest is at least that version.
