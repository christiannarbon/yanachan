# Docker

```sh
make docker-build
make docker-up      # asks whether to forward your gh token, then starts compose
make docker-logs
make docker-down
```

`make docker-up` publishes the dashboard on <http://127.0.0.1:19080>. Override
with `make docker-up API_PORT=18080`.

## The image

Multi-stage, and small at the end of it:

1. **node** builds the Vue bundle.
2. **golang** embeds that bundle into the server binary with `go:embed`, built
   `CGO_ENABLED=0` and trimmed.
3. **alpine** carries the binary, `ca-certificates` and `tzdata` — no node, no
   web server, nothing to serve the frontend with because the frontend is inside
   the binary.

The runtime layer runs as a non-root user (uid 10001) and the port is published
on `127.0.0.1` only. Base images are pinned by digest rather than tag, so a
rebuilt upstream cannot silently change what goes into the binary. Refresh a
digest with:

```sh
docker buildx imagetools inspect node:24-alpine --format '{{.Manifest.Digest}}'
```

There is a `HEALTHCHECK` hitting `/api/health` every 30 seconds.

## The token

`gh` is not installed in the image, so the container cannot inherit your local
session the way `make run` does. `make docker-up` offers to forward it instead:

```
  Forward your gh CLI token into the container? [y/N]
```

Answer yes and the token goes in as `GH_TOKEN` — and the app still makes you
approve it on the first screen before it is used. Answer no and the container
comes up with no token, ready for the [OAuth device flow](authentication.md#2-oauth-device-flow):

```sh
GITHUB_CLIENT_ID=Iv1.xxxx make docker-up
```

`GHDASH_ALLOW_GH_CLI` is `false` in the image, so the gh CLI option is never
offered inside a container even if something mounts a `gh` binary in.

## State

State lives on the `dashboard-state` volume, mounted at `/data`. It survives
`make docker-down`; it does not survive `docker compose down -v`.

## Server mode

Server mode needs a Redis or Valkey next to the app. Put both on one
user-defined network and publish only the app, on loopback:

```sh
export GHDASH_REDIS_PASSWORD="$(openssl rand -base64 24)"
export GHDASH_SESSION_KEYS="$(openssl rand -base64 32)"
export GITHUB_CLIENT_SECRET=<client-secret>

docker network create yana
docker run -d --rm --name yana-valkey --network yana \
  valkey/valkey:9.1.3-alpine@sha256:bf09d58e96d6b4524a15396b65b242bbe18beebbcc15a04d8ee75ddac62fae0b \
  valkey-server --requirepass "$GHDASH_REDIS_PASSWORD"
docker run --rm --network yana -p 127.0.0.1:19080:19080 \
  -e GHDASH_AUTH_MODE=server \
  -e GITHUB_CLIENT_ID=<client-id> -e GITHUB_CLIENT_SECRET \
  -e GHDASH_PUBLIC_URL=http://localhost:19080 \
  -e GHDASH_SESSION_KEYS -e GHDASH_ALLOWED_ORGS=<your-org> \
  -e GHDASH_REDIS_URL=redis://yana-valkey:6379/0 -e GHDASH_REDIS_PASSWORD \
  yana-chan-4k:dev
```

Build the image first with `make docker-build`. Open
<http://localhost:19080>, not `127.0.0.1`: the callback URL registered on
GitHub must match the host the sign-in started on. The GitHub side is in
[OAuth App or GitHub App](github-app.md).

- `-e NAME` with no value passes the variable from your shell, so the secrets
  never appear on the command line or in shell history.
- Valkey is not published on any host port; only the app can reach it.
- `--requirepass` puts the password in the container's arguments, where
  `docker inspect` shows it. To avoid that, mount an ACL file and start
  Valkey with `--aclfile`; see [Redis or Valkey](redis.md#hardening).
- Keep `GHDASH_SESSION_KEYS` the same across restarts. A new key signs
  everyone out.
- Server mode writes nothing to disk. The image declares a `/data` volume
  for local mode, so Docker still creates an anonymous one; it stays empty,
  and `--rm` removes it with the container.
- This Valkey has no persistence: restarting it signs everyone out and
  resets their settings.

Stop both with `docker stop yana-valkey` and Ctrl-C, then
`docker network rm yana`.

## Compose file

`deploy/docker/docker-compose.yml`. The build context is the repository root, so
run compose through the Makefile targets or from `deploy/docker/` with
`GHDASH_IMAGE` set.
