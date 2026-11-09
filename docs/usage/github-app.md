# GitHub OAuth App or GitHub App

Server mode signs each person in with GitHub. For that it needs an app
registered on GitHub: an **OAuth App** (the default) or a **GitHub App**. Both
use the same sign-in flow and the same settings; only what the token can see
differs.

Local mode needs none of this. See [authentication](authentication.md).

## Which kind of app

| | OAuth App (default) | GitHub App |
| --- | --- | --- |
| What the token can see | Everything the user can see: every org, every repository they have access to | Only repositories where **the App is installed** and the user has access |
| Permissions | Scopes `repo read:org`, requested at sign-in | Pull requests: read-only, organization Members: read-only |
| Token lifetime | Never expires on its own. The app rotates it every `GHDASH_OAUTH_APP_TOKEN_MAX_AGE` with a brief pass through GitHub | Expires after 8 hours. The app refreshes it invisibly every `GHDASH_GITHUB_APP_TOKEN_MAX_AGE` |
| Who approves it | Each user on first sign-in; an org owner, if the org restricts OAuth Apps | An org owner, by installing it on the org |
| When to choose it | People review across several orgs or in personal repositories | Everyone reviews inside the orgs that install it |

**The GitHub App limitation, plainly.** A GitHub App user token only sees
repositories where the App is installed. Pull requests in other orgs and in
personal repositories are **silently missing** from the board and from the
weekly numbers. GitHub returns no error; they are simply not in the search
results.

Choose the GitHub App only if everyone reviews inside the orgs that install it.

## Create an OAuth App

For a team, create the app under the **organisation**, not a person, so it
survives people leaving: the org's **Settings → Developer settings → OAuth
Apps → New OAuth App**. For yourself: your **Settings → Developer settings →
OAuth Apps → New OAuth App**.

| Field | Value |
| --- | --- |
| Application name | Anything your users will recognise on GitHub's consent page |
| Homepage URL | Your `GHDASH_PUBLIC_URL`, e.g. `https://yana.example.com` |
| Authorization callback URL | `<GHDASH_PUBLIC_URL>/api/auth/callback`, e.g. `https://yana.example.com/api/auth/callback` |
| Enable Device Flow | Off |

Register it, then **Generate a new client secret**. Copy the **Client ID** and
the secret; GitHub shows the secret once.

There are no scopes to configure. The app asks for `repo read:org` when each
user signs in.

If an org restricts OAuth App access, its members cannot use the app on that
org's data until an org owner approves it: **Settings → Third-party Access →
OAuth app policy**, then **Review** and **Grant access** next to the app. Until
then, the org's membership is hidden from the app and members of that org are
refused as "not allowed". See GitHub's
[Approving OAuth apps for your organization](https://docs.github.com/en/organizations/managing-oauth-access-to-your-organizations-data/approving-oauth-apps-for-your-organization).

## Create a GitHub App instead

Under the org: **Settings → Developer settings → GitHub Apps → New GitHub App**.

1. **GitHub App name**: anything unique on GitHub.
2. **Homepage URL**: your `GHDASH_PUBLIC_URL`.
3. **Callback URL**: `<GHDASH_PUBLIC_URL>/api/auth/callback`.
4. **Expire user authorization tokens**: on.
5. **Request user authorization (OAuth) during installation**: off.
6. **Enable Device Flow**: off.
7. **Webhook → Active**: off.
8. **Repository permissions → Pull requests**: Read-only.
   **Metadata: Read-only** is added automatically.
9. **Organization permissions → Members**: Read-only. Without it, team review
   requests, suggestions and the org access check do not work.
10. **Account permissions**: none.
11. **Where can this GitHub App be installed**: Only on this account.
12. **Create GitHub App**, then **Generate a new client secret**.
13. **Install App** on **every** org whose pull requests people review.

Use the **Client ID** (it starts with `Iv`), not the numeric App ID.

## Wire it up

```sh
GHDASH_AUTH_MODE=server
GITHUB_CLIENT_ID=<client-id>
GITHUB_CLIENT_SECRET=<client-secret>
GHDASH_PUBLIC_URL=https://yana.example.com
GHDASH_SESSION_KEYS=$(openssl rand -base64 32)
GHDASH_REDIS_URL=rediss://<redis-host>:6378/0
GHDASH_REDIS_PASSWORD=<redis-password>
GHDASH_ALLOWED_ORGS=<your-org>
```

Keep the session key: generate it once and store it with the client secret. A
new key on every start signs everyone out.

Redis or Valkey: [Redis or Valkey for server mode](redis.md). Every variable:
[configuration reference](../tech/low-level-design/configuration-reference.md#server-mode).
With Docker: [server mode in Docker](docker.md#server-mode).

## Local development

An OAuth App accepts exactly one callback URL. Use a **separate development
OAuth App** with `http://localhost:19080/api/auth/callback`, never the
production one, and open the dashboard on `http://localhost:19080`, not
`127.0.0.1`: the callback must come back to the host the sign-in cookie was set
on.

A GitHub App accepts up to 10 callback URLs, so one App can serve production
and `http://localhost:19080/api/auth/callback`. A separate development App is
still cleaner.

## GitHub Enterprise Server

Point the three endpoints at your instance:

```sh
GHDASH_GITHUB_WEB=https://github.example.com
GHDASH_GRAPHQL_ENDPOINT=https://github.example.com/api/graphql
GHDASH_REST_ENDPOINT=https://github.example.com/api/v3
```

Server mode against GitHub Enterprise Server is **untested**.
