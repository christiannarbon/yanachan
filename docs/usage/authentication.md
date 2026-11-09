# Authentication

The dashboard runs in one of two modes. Which one is set by `GHDASH_AUTH_MODE`
and is never guessed.

| | Local mode (default) | Server mode |
| --- | --- | --- |
| For | One person, on their own machine | A team, on a shared hostname |
| Signing in | gh CLI session, device flow, or a token from the environment | "Sign in with GitHub", each person with their own account |
| Where the token lives | `session.json` in the state directory | Redis or Valkey, sealed |
| Reachable from | Loopback only | Anywhere you publish it, over HTTPS |

Why the two differ so much: [the security model](../tech/architecture/security.md).

## Local mode

Three paths, all opt-in. Whichever you pick, the dashboard asks before it acts.

### 1. Local gh CLI session

On start the backend checks whether `gh` is on `PATH` and whether it is logged
in. It does **not** read the token to do that. The first screen reports what it
found and asks for approval; only when you approve does the backend run
`gh auth token` and use the result for GitHub calls.

This is the path to use on your own laptop. Nothing to configure.

Set `GHDASH_ALLOW_GH_CLI=false` to remove the option entirely — appropriate when
the app is running somewhere it should never inherit an ambient session.

### 2. OAuth device flow

Set `GITHUB_CLIENT_ID` to an OAuth app with the device flow enabled:

```sh
GITHUB_CLIENT_ID=Iv1.xxxxxxxxxxxx make run
```

GitHub shows a code, you enter it in your browser, the backend polls until you
approve. There is no client secret and no callback URL, so the flow behaves the
same on a laptop, inside Docker and behind a port-forward.

Scopes requested: `repo read:org`.

If `GITHUB_CLIENT_ID` is unset, the backend says so in its startup log and the
option is not offered.

### 3. Token from the environment

If `GH_TOKEN` is present, the app offers it as an option — still behind an
approval click. This is how the Docker and Kubernetes targets forward a local
`gh` session into a container, since `gh` is not installed in the image.

`GHDASH_GITHUB_TOKEN` and `GITHUB_TOKEN` are also read, in that order of
preference: `GHDASH_GITHUB_TOKEN`, then `GH_TOKEN`, then `GITHUB_TOKEN`.

### Where the token goes

The token is written to `session.json` in the state directory with `0600`
permissions and is sent only to `api.github.com`. It is never returned to the
browser — there is a test that asserts exactly that.

**Sign out** in the settings dialog deletes the file.

### There is no login

The token lives on the server and the server has no user accounts, so anything
that can reach the port is already authenticated. That is why the app binds to
`127.0.0.1` by default and refuses to answer to a `Host` header it does not
recognise:

```sh
GHDASH_ALLOWED_HOSTS=dash.example.internal make run
```

The reasoning behind that, and the two other guards next to it, is in
[the security model](../tech/architecture/security.md). The short version: do
not put this on a public address without an authenticating proxy in front of it.

## Server mode

Set up by whoever runs the deployment; nothing to install for the people using
it.

**Signing in.** The first screen has one button, *Sign in with GitHub*. GitHub
asks you to authorise the app the first time, then sends you back, signed in as
yourself. Everything you see is fetched with your own token, and your settings
are yours alone.

**Every few hours, a brief pass through GitHub.** With an OAuth App (the
default), the dashboard renews your GitHub token about every 8 hours: the page
goes to GitHub and straight back, without asking anything, and lands where you
were. A background tab waits until you return to it. With a GitHub App the
renewal is invisible.

**Signing out** ends the session on the server and revokes the token at GitHub.

**How long a session lasts.** It ends after 7 days unused or 30 days in all,
whichever comes first; the operator can change both. When it ends, its token is
revoked within about five minutes.

**What the operator configures:** a GitHub app ([OAuth App or GitHub
App](github-app.md)), a [Redis or Valkey](redis.md), and who may sign in. Every
variable is in the
[configuration reference](../tech/low-level-design/configuration-reference.md#server-mode).

**Switching modes does not move settings.** Local mode's `settings.json` is not
read in server mode, and server mode's settings are not written back to it.

## Troubles

Signing in not working? [Authentication troubleshooting](../tech/troubleshooting/authentication.md).
