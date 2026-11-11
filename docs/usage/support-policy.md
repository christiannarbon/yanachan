# Support policy

What is supported, and for how long. Security fixes follow
[SECURITY.md](../../SECURITY.md): the latest minor release only.

## Versions

Releases follow [Semantic Versioning](https://semver.org/). The image and the
Helm chart share one version and are released together.

A change is **breaking**, and needs a new major version (or, before 1.0, a new
minor), when it:

- renames or removes a chart value;
- changes a default in a way that alters exposure, storage or the Redis key
  layout;
- removes an environment variable;
- changes the default auth mode.

Upgrading one minor version at a time is supported. Upgrade notes live in the
[changelog](../../CHANGELOG.md).

## Auth modes

Both `local` and `server` modes are supported. Server mode supports both an
OAuth App and a GitHub App; the OAuth App is the default. See
[OAuth App or GitHub App](github-app.md).

## Redis and Valkey

Server mode needs Redis or Valkey. The Helm chart bundles a pinned Valkey by
default, and that version is supported by definition.

For one you provide, the tested version is **Valkey 9.1**: the store's
conformance suite and the server-mode end-to-end tests run against it in CI.
Managed offerings that speak the Redis protocol are expected to work. See
[Redis or Valkey](redis.md) for what the app needs from it.

## Kubernetes and Helm

- **Kubernetes** 1.30 and later. Tested against the oldest and newest minors
  in that range that upstream still supports: **1.35** and **1.37**, checked on
  2026-11-11 against <https://kubernetes.io/releases/>.
- **Helm** 3.8 or later, and Helm 4.

## Architectures

`linux/amd64` and `linux/arm64`.

## GitHub Enterprise Server

Configurable, but **untested**: nobody has run the app against a real GitHub
Enterprise Server yet.
