# Troubleshooting: authentication

## The first screen offers nothing

All three paths are opt-in and each has a precondition.

| Path | Precondition | Check |
| --- | --- | --- |
| gh CLI | `gh` on `PATH`, logged in, and `GHDASH_ALLOW_GH_CLI` not `false` | `gh auth status` |
| OAuth device flow | `GITHUB_CLIENT_ID` set | the startup log says if it is not |
| Environment token | `GHDASH_GITHUB_TOKEN`, `GH_TOKEN` or `GITHUB_TOKEN` non-empty | `echo $GH_TOKEN` |

The backend prints this on startup when the second is missing:

```
GITHUB_CLIENT_ID is unset: OAuth device sign-in is disabled
```

Inside a container all three can be false at once: the image sets
`GHDASH_ALLOW_GH_CLI=false` and forwards a token only if you agreed to it. Run
`make docker-up` again and answer `y`, or pass `GITHUB_CLIENT_ID`.

## "gh CLI is installed but not logged in"

```sh
gh auth login
```

Then reload the page. The status is re-read per request, so no restart is needed.

## "reading the local gh CLI session is disabled by configuration"

`GHDASH_ALLOW_GH_CLI=false`. That is the default in the image and the pod, and
it is deliberate — a container should not inherit an ambient session. Use the
device flow or an environment token there.

## "no token was supplied by the environment"

The variable is empty or whitespace. Note that `GH_TOKEN=` in a compose file
counts as unset: empty values are treated as absent.

Under Kubernetes, check the secret actually has content:

```sh
kubectl -n yana-chan-4k get secret yana-chan-4k -o jsonpath='{.data.gh-token}' | base64 -d | wc -c
```

Zero means `make k8s-secret` ran without a token. Re-run it, then
`make k8s-restart` — the pod reads the secret at startup.

## "token check failed"

The token was read but GitHub rejected it or the call did not complete.

- Expired or revoked token — `gh auth refresh`, or issue a new one.
- No network route to `api.github.com` from wherever the server is running.
- GitHub Enterprise: `GHDASH_GRAPHQL_ENDPOINT` and `GHDASH_GITHUB_WEB` both need
  pointing at your instance.

## The device flow never completes

`POST /api/auth/device/poll` returns a `state`:

| `state` | Meaning |
| --- | --- |
| `pending` | you have not approved it in the browser yet |
| `slow_down` | you polled too fast; the UI backs off automatically |
| `expired` | the code timed out — start again |
| `denied` | you declined it on github.com |

"no device authorization is in progress" means the server has no pending flow —
the backend restarted, or the poll arrived before a start. Click sign-in again.

## Signed in, but the board is empty

- Check whether the sections exist at all. Team and org sections only appear
  once you add them in [Settings](../../usage/settings.md).
- `Your open PRs` genuinely empty is a correct answer if you have no open pull
  requests.
- A `warning` on the board means a partial GitHub failure — one search resolved,
  another did not. The message is GitHub's own.

## Suggestions do not load in Settings

```
Could not list your memberships: ...
```

Almost always a token without the `read:org` scope. Fine-grained tokens need an
organization read permission. You can still type teams and orgs in by hand;
suggestions are a convenience.

## Signing out did not sign me out

`POST /api/auth/logout` deletes `session.json`. If you are immediately signed in
again, you are re-approving an *environment* token that is still present —
`GH_TOKEN` in your shell, the compose file, or the secret. Remove it there.

## Signed in with GitHub but told *not allowed*

Server mode only. The browser lands on `/?authError=not_allowed` after GitHub
let you through: the deployment's access policy did not admit you.

- **The policy.** `GHDASH_ALLOWED_USERS` (logins, case-insensitive, or `id:<n>`
  for a numeric user ID),
  `GHDASH_ALLOWED_ORGS` (org logins) or `GHDASH_ALLOW_ANY_USER=true`. Check the
  login or org is spelled the way GitHub spells it.
- **An OAuth App (the default) and an org with third-party access
  restrictions.** Until an org owner approves the app under the org's *Third-party
  access* settings, the org hides itself from the app's tokens, so the
  membership check sees "not a member". Ask an owner to approve it, or use
  `GHDASH_ALLOWED_USERS` meanwhile.
- **A GitHub App.** It must be installed in the org with the
  **Members: read** organization permission; without it the org is invisible in
  the same way.

`authError=server` instead means something failed on the server side; see
[below](#autherrorserver).

Access is re-checked every 12 hours on an existing session. Someone removed from
the org keeps working for at most that long, then is signed out and their token
is revoked. A GitHub outage during the re-check never signs anyone out.

## Server mode refuses to start

The process prints one `config:` line per problem and exits 1. It reports every
problem at once, never a secret's value.

| Message | Fix |
| --- | --- |
| `GHDASH_AUTH_MODE must be "local" or "server"` | A typo in the mode |
| `GITHUB_CLIENT_ID is required in server mode`, `GITHUB_CLIENT_SECRET is required…` | Set both from your [GitHub app](../../usage/github-app.md) |
| `GHDASH_PUBLIC_URL is required…`, `…must be an origin such as https://yana.example.com, with no path` | Scheme and host only; no trailing path |
| `GHDASH_SESSION_KEYS is required…`, `…entry N is not valid base64`, `…entry N is X bytes, want 32` | `openssl rand -base64 32`, comma-separated if more than one |
| `server mode needs an access policy…` | Set `GHDASH_ALLOWED_ORGS`, `GHDASH_ALLOWED_USERS` or `GHDASH_ALLOW_ANY_USER=true` |
| `GH_TOKEN, GITHUB_TOKEN and GHDASH_GITHUB_TOKEN must be unset in server mode…` | Unset them; each user signs in with their own account |
| `GHDASH_SESSION_MAX must not be shorter than GHDASH_SESSION_IDLE` | Raise the maximum or lower the idle time |
| `GHDASH_TOKEN_MAX_AGE must be between 15m and GHDASH_SESSION_MAX` (or the `OAUTH_APP` / `GITHUB_APP` variants) | Pick an age in range |
| `GHDASH_SESSION_IDLE: "…" is not a duration such as 168h` | Go durations: `30m`, `8h`, `168h` |
| `GHDASH_REDIS_URL is required…`, `…must be redis://host:port[/db] or rediss://…` | See [Redis or Valkey](../../usage/redis.md) |
| `GHDASH_REDIS_URL must not contain a password…` | Move it to `GHDASH_REDIS_PASSWORD`; a username may stay in the URL |
| `GHDASH_REDIS_CA_FILE needs a rediss:// URL` | A CA only makes sense with TLS |

If the configuration passes, the server pings Redis for up to 10 seconds. A
failure ends with `server: connect to redis: kv: ping redis at <host:port>: …`:

- `connection refused` or a timeout — wrong host or port, or a network policy
  in the way.
- `NOAUTH` or `WRONGPASS` — `GHDASH_REDIS_PASSWORD` is missing or wrong, or the
  ACL username in the URL is.
- A certificate error — the server needs `GHDASH_REDIS_CA_FILE`, or the URL
  should be `rediss://`.
- `NOPERM` — the ACL user lacks a command; the list is in
  [Redis hardening](../../usage/redis.md#hardening).

## `authError=state`

The callback arrived without the matching sign-in cookie.

- **The sign-in started on one host and came back on another.** For example
  `http://127.0.0.1:19080` in the address bar, but `http://localhost:19080` in
  `GHDASH_PUBLIC_URL` and the app's callback URL. Use the public URL's host.
- **The browser blocks cookies** for the site.
- **It took longer than 10 minutes** between clicking *Sign in* and approving on
  GitHub. Start again.

## `authError=exchange`

GitHub refused to swap the code for a token. The server log has
`sign-in failed: code exchange: …` with GitHub's reason.

- **Wrong client secret**, or the secret was regenerated on GitHub and not
  updated here.
- **Callback URL mismatch.** The app's callback URL on GitHub must be exactly
  `<GHDASH_PUBLIC_URL>/api/auth/callback`.
- **Client ID and secret from different apps**, for example the development
  OAuth App's ID with the production secret.

## `authError=not_allowed`

See [Signed in with GitHub but told *not allowed*](#signed-in-with-github-but-told-not-allowed)
above. In short: not in an allowed org, an OAuth App the org has not approved,
or a GitHub App not installed on the org.

## `authError=server`

Anything that went wrong on the server's side of the sign-in: GitHub could not
be asked who you are or which orgs you are in, or the session could not be
stored because Redis failed. The server log has the reason on a
`sign-in failed:` line. A token that was issued but could not be stored is
revoked.

## `503` *the sign-in was renewed while this request ran*

A request started with the old token, and the session was re-authorised in
another tab before GitHub answered. The session is fine; the next refresh uses
the new token.

## The page flicks to GitHub and back every few hours

Expected, with an OAuth App. Its tokens never expire on their own, so the
dashboard replaces them every `GHDASH_OAUTH_APP_TOKEN_MAX_AGE` (default `8h`)
by passing through GitHub, which approves without asking. The session and
settings are untouched. For fewer redirects, raise
`GHDASH_OAUTH_APP_TOKEN_MAX_AGE`; the trade-off is in
[token lifetime](../architecture/security.md#token-lifetime-server-mode).

## *Your GitHub sign-in could not be renewed*

The page tried to renew the sign-in through GitHub and came back with an error,
or it already went once in the last minute and going again would loop. It stops
and offers **Renew sign-in** and **Sign out**; the reason, if GitHub gave one,
is shown above them. Renewing by hand is safe: a click is not a loop.

If renewing fails every time, look at the reason, then at
[`authError=exchange`](#autherrorexchange) and
[`authError=server`](#autherrorserver) below.

## It keeps going to GitHub and back

The page goes to GitHub at most once a minute on its own, and never straight
after coming back with an error. If the sign-in screen keeps coming back after
signing in:

- **Cookies are blocked** for the site, so the session never sticks.
- **The address bar host differs from `GHDASH_PUBLIC_URL`'s.** The session
  cookie is set on the public host; a page loaded under another name never sees
  it.

## Everyone was signed out

- **The session keys changed.** A key removed from `GHDASH_SESSION_KEYS`, or a
  new key generated on every start, makes every sealed session unreadable.
  Keep the keys in a Secret and rotate by prepending.
- **Redis restarted without persistence.** Sessions and settings were only in
  memory.
- **`GHDASH_REDIS_PREFIX` changed.** The old sessions are still there, under the
  old prefix.

## PRs from some orgs are missing

With a GitHub App, the token only sees repositories where the App is installed.
Install it on that org, or switch to an OAuth App. See
[which kind of app](../../usage/github-app.md#which-kind-of-app).

## `503` from `/api/ready`

The server cannot reach Redis. Check, in order: `GHDASH_REDIS_URL`,
`GHDASH_REDIS_PASSWORD`, the TLS CA (`GHDASH_REDIS_CA_FILE`), and any network
policy between the app and Redis. `/api/health` stays `200` meanwhile, so the
app is not restarted; it just receives no traffic.

## `403 unrecognised Host header` in server mode

The hostname in the address bar is not `GHDASH_PUBLIC_URL`'s host. Use the
public URL, or add the other name to `GHDASH_ALLOWED_HOSTS`.
