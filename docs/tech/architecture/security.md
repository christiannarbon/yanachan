# Security model

This is the document to read before changing anything in
`api.withMiddleware`, `setSecurityHeaders` or `api/serverauth.go`.

## Two modes

One binary, two ways to decide who a request acts as. `GHDASH_AUTH_MODE` picks
one; it is never inferred.

| | `local` (default) | `server` |
| --- | --- | --- |
| Who signs in | One person, on their own machine | Everyone in an organisation, each with their own GitHub account |
| Whose token serves a request | The one stored token | The token of the user whose session cookie came with the request |
| Where state lives | `settings.json` and `session.json` in the state directory | Redis or Valkey; nothing on disk |
| Exposure | Loopback only, or behind an authenticating proxy | Meant to be exposed on a hostname, over HTTPS |

## Local mode: the premise

There is no login and no cookie. The GitHub token lives on the server, and every
request that arrives is served with it — so **anything that can reach the port is
already authenticated**.

That is a deliberate trade for a tool you run on your own machine: a login would
mean a user store, a password, a session cookie and a CSRF token, all to protect
a single-user process from itself. The cost is that the perimeter is the network
boundary, and three guards make that boundary mean something.

## Server mode: the premise

Every request carries a session cookie, and the server maps that cookie to one
user's GitHub token. No request is ever served with a token other than the one
belonging to its cookie. The process writes nothing to disk. The app
authenticates its own users, so it is meant to be exposed on a hostname, over
HTTPS.

The three guards below still apply in server mode. The session cookie is added
on top of them, not instead of them.

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

In server mode over `https`, responses also carry
`Strict-Transport-Security: max-age=31536000`.

## Guard 2 — mutating requests must be same-origin

Anything that is not a `GET` or `HEAD` is rejected unless `Origin` matches, or
there is no `Origin` and no browser fetch metadata — which is what `curl` looks
like, and is allowed on purpose so the API stays scriptable from the same
machine.

`GHDASH_DEV_ORIGIN` adds one extra permitted origin, which `make dev` sets to
the vite server. It should never be set in a deployment.

In server mode this guard, together with `SameSite=Lax` on the session cookie,
is the whole CSRF defence. There is no CSRF token. Every route that changes
state is non-`GET`, and the two `GET` routes that take part in sign-in change
nothing an attacker can use: the callback refuses to run without the matching
sign-in cookie.

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
the dev origin's host when one is set. In server mode the host of
`GHDASH_PUBLIC_URL` is added too, so a deployment needs nothing else.

`/api/health` and `/api/ready` are exempt, so Kubernetes probes can address the
pod by IP. They return a status and a timestamp and nothing else.

## Sessions (server mode)

`backend/internal/session/`, `backend/internal/seal/`

- **The cookie holds only an ID.** It is 32 random bytes (256 bits),
  base64url-encoded. No token, login or expiry is in it.
- **Redis never sees the ID.** The session is stored under
  `session:<hex sha256(id)>`, so a dump of Redis does not yield a cookie anyone
  can present.
- **Tokens are sealed, even inside Redis.** The access and refresh tokens are
  sealed with AES-256-GCM, using the hashed key as additional data, so a sealed
  token copied into another session's value does not open. Redis persistence,
  replicas, backups and `MONITOR` only ever see sealed values.
- **Every session key has a TTL.** It is the session's effective expiry (idle
  or absolute, whichever comes first) plus a day, so a session disappears even
  if the app is gone. The day is there so the reaper can still revoke it.
- **Writes cannot undo each other.** Every change to a session is a
  compare-and-set that touches only the fields its writer owns, so a request
  updating "last seen" on one replica can never put back a token that another
  replica has just rotated.
- **Per-user settings hold no secret** and have no TTL.
- **Key rotation.** `GHDASH_SESSION_KEYS` is a list: the first key seals,
  every key opens. Rotate in three deploys:
  1. **Add** the new key at the **end** (`old,new`). Every replica can now open
     both, and nothing is sealed with the new key yet.
  2. Once every replica runs step 1, **move** the new key to the **front**
     (`new,old`).
  3. After `GHDASH_SESSION_MAX`, **remove** the old key.

  Skipping step 1 is safe but rough: during the rollout, replicas that do not
  have the new key yet cannot read sessions sealed with it, and those users are
  asked to sign in again. Such sessions are never deleted by a replica that
  cannot read them; a replica with the key still revokes them at expiry.
- **Cookie attributes.** `HttpOnly` and `SameSite=Lax` always. When
  `GHDASH_PUBLIC_URL` is `https`, also `Secure` and the `__Host-` prefix, which
  makes the browser refuse the cookie unless it is `Secure`, `Path=/` and has no
  `Domain`. Not `Strict`: the sign-in callback is a top-level navigation from
  github.com, and `Strict` would strip the sign-in cookie from it.

Plain `http` is accepted so server mode can be tried on `http://localhost`. The
startup log warns when the public URL is `http` on a host that is not loopback.

## Token lifetime (server mode)

No GitHub token serves a session past its maximum age. The default is
`GHDASH_TOKEN_MAX_AGE` (`8h`). Two overrides replace it per kind of session:

- `GHDASH_OAUTH_APP_TOKEN_MAX_AGE` for sessions **without** a refresh token
  (OAuth Apps).
- `GHDASH_GITHUB_APP_TOKEN_MAX_AGE` for sessions **with** one (GitHub Apps with
  expiring user tokens).

The kind is decided per session by whether GitHub returned a refresh token, not
by the client ID. A GitHub App with token expiry turned off therefore follows
the OAuth App age.

**When to set them separately.** A short GitHub App age is free: the refresh is
invisible to the user, so `30m` costs nothing. The OAuth App age trades exposure
for interruptions, because each rotation is one brief redirect through GitHub.
Lower it for stricter environments; raise it if users complain. Leave both
unset to keep one policy.

How a token is replaced:

- **GitHub App:** refreshed server-side, invisibly, when it reaches its maximum
  age or comes within five minutes of GitHub's own expiry. One replica
  refreshes at a time; the others wait for the result.
- **OAuth App:** GitHub issues no refresh token, so the API answers
  `401 {"reauth": true}`. The page passes through GitHub, which approves
  silently because the app is already authorised, and comes back. The callback
  replaces the token **in the same session** and revokes the old one.

A token is also revoked at GitHub when the user signs out, when a session
expires unattended (the reaper runs every five minutes, one replica at a time,
and revokes every session past its idle or maximum age), when access is
withdrawn, and — for an OAuth App — the moment it passes its maximum age, even
if the user never comes back. If GitHub rejects the token a session currently
holds, the session ends; a request that fails with a token the session has
already replaced leaves it alone.

A revocation that fails is not dropped: the token is sealed into a retry queue
that the reaper works through with backoff, for up to eight attempts over about
a day. If no reaper runs for a whole day, Redis deletes the session and the
token is then only invalidated by GitHub or the user.

**Why:** an OAuth App token never expires on its own. Without rotation, a token
copied out of a running process would stay valid indefinitely. GitHub also keeps
at most ten tokens per user and app, revoking the oldest silently; revoking on
every rotation keeps each user at one.

## Who may sign in (server mode)

`backend/internal/access/`

Server mode refuses to start without an access policy. Set at least one of:

| Variable | Admits |
| --- | --- |
| `GHDASH_ALLOWED_ORGS` | members of these orgs |
| `GHDASH_ALLOWED_USERS` | these logins, or these numeric user IDs written `id:583231` |
| `GHDASH_ALLOW_ANY_USER=true` | anyone with a GitHub account, written down on purpose |

Allowing everyone is legitimate — each user only sees what their own token can
see — but it has to be a decision, not an accident.

A login can be renamed, and someone else can then register the old one. Where
that matters, list users by ID (`id:583231`, the `databaseId` GitHub reports
for the account) rather than by login.

A refused sign-in has its token revoked straight away. Access is re-checked
every 12 hours on a live session, against the login and ID GitHub reports now,
not the ones stored at sign-in; someone who left the org is signed out and
their token revoked at the next check. A GitHub outage during the check never
signs anyone out.

An OAuth App that an org's third-party access policy has not approved cannot
see that org's membership, so the user looks like "not a member". See
[the setup guide](../../usage/github-app.md#create-an-oauth-app).

## Threat model (server mode)

What each kind of access to a running deployment is worth to an attacker.

| Attacker has | They get | They do not get | Mitigation |
| --- | --- | --- | --- |
| Read access to Redis (dump, replica, backup, `MONITOR`) | Logins, GitHub user IDs, settings, session expiry times | Any token (sealed), any cookie (only hashes are stored) | Redis AUTH, TLS and a network policy; see [Redis or Valkey](../../usage/redis.md#hardening) |
| The Kubernetes Secret (`session-keys`, client secret) | The ability to run a look-alike app with your OAuth App | Any user's token: the sealed tokens live in Redis | Keep the Secret and Redis out of the same backup; rotate the client secret and the session keys |
| Both Redis and `session-keys` | Every token still stored: at most its maximum age old, because OAuth App tokens are revoked and dropped at that age and GitHub App tokens are refreshed. A GitHub App refresh token lives longer (months), until its session ends | Cookies; tokens of sessions already ended or retired (revoked at GitHub) | A shorter `GHDASH_TOKEN_MAX_AGE`, or the per-kind overrides, shrinks the window. Keys wrapped by a KMS are not supported yet |
| A user's session cookie | That user's dashboard until sign-out or expiry | Other users' data; the token itself | `HttpOnly`, `Secure`, `__Host-`; sign-out revokes |
| A running process's memory | Tokens of users active on that process | — | Pod hardening; token maximum age |

If a secret leaks:

- **The session keys.** Assume every stored token is known, and revoke them at
  GitHub: removing the key only stops *this* app from reading the tokens, which
  stay valid — and an OAuth App token never expires on its own. The app's owner
  can revoke each account's authorization of the app, which revokes that
  account's tokens (GitHub: [Token expiration and
  revocation](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/token-expiration-and-revocation)).
  Then deploy a new key **alone** (this signs everyone out, which is the point)
  and change the Redis password.
- **The client secret.** Generate a new one in the app's settings, deploy it,
  then delete the old one. A new client secret does not revoke tokens already
  issued; if Redis or the session keys may have leaked with it, revoke them as
  above.
- **The Redis password.** Change it, and treat Redis as read: if the session
  keys are safe, no token is exposed.

## Token custody

| Rule | Where it is enforced |
| --- | --- |
| Local: no token is read until the user approves | `api.authService`, `ghcli` reports installed/logged-in without reading it |
| Local: stored `0600` in the state directory | `state.Store.SaveSession` |
| Local: deleted on sign-out | `POST /api/auth/logout` |
| Sent only to the configured GitHub endpoints | `github.Client`, `ghauth.WebClient`, endpoints from config |
| Never returned to the browser | asserted in `api/server_test.go` and `api/servermode_e2e_test.go` |
| Server: one token per user, sealed wherever it is stored | `session.Store` |
| Server: never written to disk | asserted in `api/servermode_e2e_test.go` |
| Server: revoked on sign-out, rotation and expiry | `api/serverauth.go`, `api/reaper.go` |

The response shape for `/api/auth/status` carries a `mode` and a `login` — what
the UI needs to say "signed in as X using Y" — and no token. If you add a field
there, check the tests still pass; they look for the token string in the
serialized body, and in server mode for every token, session ID, the client
secret and the session key in every response.

## Deployment posture

Local mode:

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

Server mode:

- Serve it over HTTPS, with `GHDASH_PUBLIC_URL` set to the `https://` origin.
- Set an access policy that is as narrow as your users allow.
- Give Redis a password, TLS where the provider supports it, and a network
  policy that admits only the app. See [Redis or Valkey](../../usage/redis.md).
- Keep `GHDASH_SESSION_KEYS` in a Secret, never in a values file or an image.
- `GHDASH_ALLOW_GH_CLI` is forced off, and an environment token is a startup
  error.

## Scope of the GitHub token

**Local mode** and **server mode with an OAuth App** request `repo read:org`.
`repo` is broader than this dashboard needs — it reads pull request metadata
only — but GitHub does not offer a narrower classic scope that covers private
repositories. In local mode, a fine-grained personal access token with
read-only pull request and organization permissions works, and passing it in
through `GH_TOKEN` is the way to use one.

**Server mode with a GitHub App** ignores scopes and uses the App's permissions
instead: Pull requests read-only and organization Members read-only. The token
then only sees repositories where the App is installed. The trade-off is in
[the setup guide](../../usage/github-app.md#which-kind-of-app).
