# Security model

This is the document to read before changing anything in
`api.withMiddleware` or `setSecurityHeaders`.

## The premise

There is no login and no cookie. The GitHub token lives on the server, and every
request that arrives is served with it — so **anything that can reach the port is
already authenticated**.

That is a deliberate trade for a tool you run on your own machine: a login would
mean a user store, a password, a session cookie and a CSRF token, all to protect
a single-user process from itself. The cost is that the perimeter is the network
boundary, and three guards make that boundary mean something.

## Guard 1 — the server refuses to be framed

Every response carries:

```
X-Frame-Options: DENY
Content-Security-Policy: ... frame-ancestors 'none' ...
X-Content-Type-Options: nosniff
Referrer-Policy: no-referrer
```

`frame-ancestors` is the load-bearing one. Without it, a remote page could put
the dashboard in an invisible iframe and bait a click onto *Use my gh CLI
session*. The resulting POST is **same-origin** — it really did come from the
dashboard's own page — so no CSRF check would ever see it, and the backend would
read your real token.

Refusing to be framed is what stops that. Clickjacking is the attack this
application is most exposed to, precisely because it has no login.

## Guard 2 — mutating requests must be same-origin

Anything that is not a `GET` or `HEAD` is rejected unless `Origin` matches, or
there is no `Origin` and no browser fetch metadata — which is what `curl` looks
like, and is allowed on purpose so the API stays scriptable from the same
machine.

`GHDASH_DEV_ORIGIN` adds one extra permitted origin, which `make dev` sets to
the vite server. It should never be set in a deployment.

## Guard 3 — the `Host` header is pinned to loopback

This is the DNS-rebinding guard, and it is the least obvious of the three.

An attacker who points a domain of their own at `127.0.0.1` arrives with
`Origin` and `Host` in agreement, so guard 2 passes and the browser treats the
replies as same-origin — the whole board, readable, by a page the user merely
visited. Refusing an unrecognised `Host` is what stops it.

Out of the box the app answers only to `localhost`, `127.0.0.0/8` and `::1`. To
serve it under a real name, list the names:

```sh
GHDASH_ALLOWED_HOSTS=dash.example.internal make run
```

The allowlist is also seeded with the address the server was told to listen on
(a wildcard excepted, since `0.0.0.0` never appears in a `Host` header) and with
the dev origin's host when one is set.

`/api/health` is exempt, so Kubernetes probes can address the pod by IP. It
returns a status and a timestamp and nothing else.

## Token custody

| Rule | Where it is enforced |
| --- | --- |
| No token is read until the user approves | `api.authService`, `ghcli` reports installed/logged-in without reading it |
| Stored `0600` in the state directory | `state.Store.SaveSession` |
| Sent only to `api.github.com` | `github.Client`, one endpoint from config |
| Never returned to the browser | asserted in `api/server_test.go` |
| Deleted on sign-out | `POST /api/auth/logout` |

The response shape for `/api/auth/status` carries a `mode` and a `login` — what
the UI needs to say "signed in as X using Y" — and no token. If you add a field
there, check the test still passes; it is looking for the token string in the
serialized body.

In server mode, a session that expires has its token revoked at GitHub within
about five minutes: every replica runs a reaper, one at a time under the
`lock:reaper` key, that revokes and deletes sessions past their idle or maximum
age. A failed revocation is logged and the session is deleted anyway. If the
reaper cannot run, Redis deletes the session an hour later, but the token is
then only invalidated by GitHub's own expiry or by the user.

## Deployment posture

- Bind to loopback. The defaults do.
- The container publishes on `127.0.0.1` and runs as uid 10001 with a read-only
  root filesystem and all capabilities dropped.
- The Kubernetes service is ClusterIP with no Ingress, deliberately. Reach it
  through `make k8s-tunnel`.
- If you do put an authenticating proxy in front, its hostname must be in
  `GHDASH_ALLOWED_HOSTS` — and *authenticating* is not optional. Guard 3 stops
  DNS rebinding; it does not stop a colleague who can reach the URL.
- `GHDASH_ALLOW_GH_CLI=false` removes the ambient-session path entirely. Both
  the image and the Kubernetes deployment set it.

## Scope of the GitHub token

The OAuth device flow requests `repo read:org`. `repo` is broader than this
dashboard needs — it reads pull request metadata only — but GitHub does not
offer a narrower classic scope that covers private repositories. A fine-grained
personal access token with read-only pull request and organization permissions
works, and passing it in through `GH_TOKEN` is the way to use one.
