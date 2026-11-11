# Security policy

## Supported versions

Only the latest minor release gets security fixes. Older minors get none: this
is a one-maintainer project, and backporting is not something it can promise.

## Reporting a vulnerability

Report it privately through GitHub's private vulnerability reporting:
<https://github.com/christiannarbon/yanachan/security/advisories/new>.

Please do not open a public issue for a vulnerability.

You can expect an acknowledgement within 7 days. That is best effort; there is
no SLA.

## The two auth modes

**Local** mode has no login. It is for one person on their own machine, using
their own GitHub token, and the server only answers on loopback. **Server** mode
signs each user in with GitHub and serves every request with that user's own
token. Sessions live in Redis or Valkey (the Helm chart's bundled Valkey by
default, or one the operator provides), and the tokens in them are sealed even
there. Server mode never writes to the app pod's disk. It replaces each token
every `GHDASH_TOKEN_MAX_AGE` (8 hours by default) and revokes it at GitHub on
sign-out and when the session expires.

How all of this works is in [the security model](docs/tech/architecture/security.md).

## In scope

- Token custody in both modes.
- The three request guards: framing, same-origin for mutating requests, and the
  `Host` allowlist.
- Server mode's session cookie.
- The Redis/Valkey session store, and the sealing of tokens inside it.
- The bundled Valkey's defaults: AUTH always on, reachable only from the app,
  its password never on a command line.
- Token rotation and revocation.
- The access policy that decides who may sign in.
- The image and the Helm chart's defaults.

The most serious reports are a request served with another user's token, or a
token reaching a browser or a log line. Next come a usable token readable from
Redis without the session key, and a token that stays valid at GitHub after its
session ended.

## Out of scope

These are documented misconfigurations, not vulnerabilities:

- Exposing a **local-mode** dashboard without an authenticating proxy in front.
- Running server mode with `GHDASH_ALLOW_ANY_USER=true`, and then any GitHub
  user being able to sign in. That is what the setting does.
- Running an **external** Redis/Valkey without AUTH or TLS, or reachable from
  outside the cluster network.
- Backing up the bundled Valkey's volume, or an external Redis, to the same
  place as the Secret that holds the session keys.
- Expecting the bundled Valkey's NetworkPolicy to isolate it on a network plugin
  that does not enforce NetworkPolicy. The chart's `ci/valkey.yaml` values are a
  CI fixture, not a supported deployment.

A vulnerability in the Valkey **image** itself should be reported to Valkey.
This project's part is bumping the pinned digest, which it does in a patch
release.

## If a secret leaks

- **A session key** (`GHDASH_SESSION_KEYS`). Assume every stored token is
  known. Deploy a new key **alone**, dropping the leaked one at once rather than
  after `GHDASH_SESSION_MAX`: every session sealed with it ends, and those users
  sign in again. That is the intended cost. Removing the key only stops the app
  reading the tokens; they stay valid at GitHub, so also revoke each account's
  authorization of the app, as described in
  [the security model](docs/tech/architecture/security.md#threat-model-server-mode).
- **The GitHub client secret.** Generate a new one in the OAuth App or GitHub
  App settings, update the Secret, restart, then delete the old secret at
  GitHub.
- **The Valkey/Redis password.** With the bundled Valkey, change
  `redis-password` in the Secret and restart both the Valkey StatefulSet and the
  app Deployment; the app reads the password at start. With an external one,
  rotate it on the Redis side (for a managed instance, its AUTH string; with
  ACLs, add the new password before removing the old), update `redis-password`
  in the Secret, and restart. Tokens in Redis stay sealed, so a leaked Redis
  password alone exposes none. If the session key may have leaked too, rotate
  that as well.
- **A user's GitHub token.** The user revokes it under GitHub *Settings →
  Applications*. Signing out of the dashboard revokes it too. Even if nobody
  notices, server mode replaces it within `GHDASH_TOKEN_MAX_AGE` and revokes
  the old one; lowering that value shortens the window for everyone.

<!-- P5-02: link verifying-releases.md -->
