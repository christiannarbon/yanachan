# Documentation

Everything that used to live in one very long README, split by who is reading it.

## [Usage](usage/) — running the dashboard

You want the thing on screen, authenticated, showing your pull requests.

| Guide | What it covers |
| --- | --- |
| [Getting started](usage/getting-started.md) | prerequisites, `make deps`, `make run`, the first screen |
| [Authentication](usage/authentication.md) | local and server mode, the sign-in paths, where the token lives, signing out |
| [OAuth App or GitHub App](usage/github-app.md) | for organisations: registering the app server mode signs people in with |
| [Redis or Valkey](usage/redis.md) | for organisations: what server mode stores, requirements, hardening |
| [Your week](usage/weekly-dashboard.md) | the landing tab: the four counts, the calorie figure, reading the chart |
| [Review queues](usage/review-queues.md) | the queues, the indicators, the activity window |
| [Settings](usage/settings.md) | teams, organizations, the window override, the state files |
| [Themes](usage/themes.md) | the ten palettes and how to pick one |
| [Language](usage/language.md) | English and Japanese |
| [Docker](usage/docker.md) | building and running the container |
| [Kubernetes](usage/kubernetes.md) | `make k8s-up`, the tunnel, the secret, the overlays |

## [Tech](tech/) — how it is built

You are changing the code, reviewing it, or trying to work out why it did that.

- **[Architecture](tech/architecture/)** — the shape of the system: components,
  request flow, and the security model that shapes both.
- **[Low level design](tech/low-level-design/)** — the rules inside a package:
  the classification branches, the window arithmetic, the batched GraphQL
  document, the theme generator, the API contract.
- **[Troubleshooting](tech/troubleshooting/)** — symptoms, causes, fixes.

## Conventions used here

- Commands are run from the repository root unless a snippet says otherwise.
- `make help` lists every target; `make doctor` checks the toolchain.
- Paths like `backend/internal/board/build.go` point at the file that actually
  implements the paragraph you are reading. When the two disagree, the file wins.
