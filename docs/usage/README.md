# Usage

How to run Yana-chan 4K and what every screen is telling you. No Go or Vue
knowledge is assumed; if you are here to change the code, start at
[tech](../tech/) instead.

## Start here

1. **[Getting started](getting-started.md)** — install the dependencies, build,
   and open the dashboard.
2. **[Authentication](authentication.md)** — pick one of the three sign-in paths, or read how server mode signs a team in.
3. **[Your week](weekly-dashboard.md)** — the tab you land on.

## Reading the dashboard

- [Your week](weekly-dashboard.md) — the look back: opened, merged, closed,
  reviewed, and the calorie total.
- [Review queues](review-queues.md) — the sections that are queues, the `Reply` and
  `New` badges, and the activity window that decides what counts as recent.

## Making it yours

- [Settings](settings.md) — follow teams and organizations, override the
  window, cap the query size.
- [Themes](themes.md) — ten light palettes, the default being Yanami's own.
- [Language](language.md) — English and Japanese, switched in the top corner.

## Running it somewhere else

- [Docker](docker.md) — one container, state on a volume, published on loopback.
- [Kubernetes](kubernetes.md) — a local cluster, one command up, one command down.

## For organisations

- [OAuth App or GitHub App](github-app.md) — register the app server mode signs
  everyone in with, and choose between the two.
- [Redis or Valkey](redis.md) — where server mode keeps sessions and settings,
  and how to harden it.
- [Support policy](support-policy.md) — supported versions, platforms and
  upgrade paths.

Something not behaving? See [troubleshooting](../tech/troubleshooting/).
