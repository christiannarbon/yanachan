<h1 align="center">Yana-chan 4K</h1>

<p align="center">A pull request dashboard.</p>

<p align="center">
  <img alt="Go 1.24" src="https://img.shields.io/badge/Go-1.24-00ADD8?style=flat-square&logo=go&logoColor=white">
  <img alt="Vue 3" src="https://img.shields.io/badge/Vue-3-4FC08D?style=flat-square&logo=vuedotjs&logoColor=white">
  <img alt="TypeScript 5" src="https://img.shields.io/badge/TypeScript-5-3178C6?style=flat-square&logo=typescript&logoColor=white">
  <img alt="Vite 8" src="https://img.shields.io/badge/Vite-8-646CFF?style=flat-square&logo=vite&logoColor=white">
  <img alt="Docker" src="https://img.shields.io/badge/Docker-multi--stage-2496ED?style=flat-square&logo=docker&logoColor=white">
  <img alt="Kubernetes" src="https://img.shields.io/badge/Kubernetes-kustomize-326CE5?style=flat-square&logo=kubernetes&logoColor=white">
</p>

<p align="center">
  <img alt="GitHub API: GraphQL" src="https://img.shields.io/badge/GitHub_API-GraphQL-181717?style=flat-square&logo=github&logoColor=white">
  <img alt="Themes: 10" src="https://img.shields.io/badge/themes-10-070a7d?style=flat-square">
  <img alt="Languages: EN and JA" src="https://img.shields.io/badge/i18n-EN_%2F_JA-ff7031?style=flat-square">
  <img alt="Light theme only" src="https://img.shields.io/badge/theme-light_only-fff100?style=flat-square&labelColor=555555">
  <img alt="No LLM anywhere" src="https://img.shields.io/badge/LLM_calls-0-success?style=flat-square">
  <img alt="One binary" src="https://img.shields.io/badge/ships_as-one_binary-informational?style=flat-square">
</p>

---

The dashboard: a tabbed web version of the shell script this grew out of, which
answered three questions with a `gh` search and a jq program. Same three
questions, now one tab each, plus a tab for every team and organization you
choose to follow — and a landing tab the script never had, which answers the one
question a queue cannot: what did you actually get done this week.

This is a passion project, built as an ode to **八奈見杏菜 —
Yanami Anna**, of *負けヒロインが多すぎる！* (*Too Many Losing Heroines!*). She is
the patron saint of eating your feelings and losing with style, and she is why
this repository is named the way it is. The `4K` came from 温水くん.

## The sections

Everything is reachable from the rail down the left, grouped so a long list of
teams and organizations folds away.

- **Your week** — where you land: what you opened, merged, closed and
  reviewed over the last seven days.
- **Your open PRs** — did anyone comment inside the activity window?
- **Review requested from you** — requested from you, or already reviewed by you.
- **One per team** — pull requests where that team's review was requested.
- **One per organization** — open PRs in that org that involve you and are
  not already in another section.

Go backend, Vue 3 frontend, one binary, two languages, ten light themes.

## Documentation

Everything lives in [`docs/`](docs/).

### [Usage](docs/usage/) — running it

| | |
| --- | --- |
| [Getting started](docs/usage/getting-started.md) | prerequisites, build, the first screen |
| [Authentication](docs/usage/authentication.md) | gh CLI, OAuth device flow, or a token from the environment |
| [Your week](docs/usage/weekly-dashboard.md) | the landing tab, and the calorie figure |
| [Review queues](docs/usage/review-queues.md) | the queues, the indicators, the activity window |
| [Settings](docs/usage/settings.md) | teams, organizations, and where state lives |
| [Themes](docs/usage/themes.md) · [Language](docs/usage/language.md) | ten palettes; English and Japanese |
| [Docker](docs/usage/docker.md) · [Kubernetes](docs/usage/kubernetes.md) | running it somewhere else |

### [Tech](docs/tech/) — changing it

| | |
| --- | --- |
| [Architecture](docs/tech/architecture/) | components, request flow, backend and frontend structure, and the [security model](docs/tech/architecture/security.md) |
| [Low level design](docs/tech/low-level-design/) | the [API contract](docs/tech/low-level-design/api-reference.md), the classification branches, the weekly reduction, GraphQL batching, state, theming, i18n, configuration |
| [Troubleshooting](docs/tech/troubleshooting/) | authentication, networking, build and tests, Kubernetes |

## A few things worth knowing up front

- **In local mode there is no login.** The GitHub token lives on the server, so anything that
  can reach the port is already authenticated. The app binds to loopback and
  refuses unrecognised `Host` headers to keep that honest — read
  [the security model](docs/tech/architecture/security.md) before exposing it.
- **One GraphQL request per refresh, per endpoint.** Every tab's search is
  batched into a single aliased query, so following ten more teams does not mean
  ten more round trips.
- **No LLM is called anywhere in this project.** Not in the build, not at
  runtime.
- **Light theme only, by design.** No dark mode toggle.
- **The state directory survives the rename.** If a `github-dashboarder`
  directory is still there from before and the new one is not, that one keeps
  being used, so upgrading does not quietly orphan a stored session.

  ## Quick start

```sh
make deps      # install Go modules and npm packages
make run       # build everything, serve on http://127.0.0.1:19080
```

Open <http://127.0.0.1:19080>. The first screen asks how you want to
authenticate. Nothing is followed and no token is read until you say so.

```sh
make dev       # hot reloading: backend on 19080, vite on 19090
make test      # go test ./... plus vue-tsc --noEmit
make down      # stop everything
make help      # every target
```

`make doctor` checks the toolchain.

## Running it for a team

Server mode serves a whole team from one shared hostname: each person signs in
with their own GitHub account and sees only what their own token can see. It
needs a GitHub OAuth App (or GitHub App) and a Redis or Valkey. Start with
[OAuth App or GitHub App](docs/usage/github-app.md), then
[Redis or Valkey](docs/usage/redis.md); what users see is in
[authentication](docs/usage/authentication.md#server-mode).

## THIS IS ME JUST DOING SOME STORY TELLING

There is no practical reason a tool that answers "did anyone reply to my pull
requests" should be named after a fictional high schooler who cannot stop
eating, and there is no practical reason its default palette should be reverse
engineered out of an anime tie-in page's stylesheet. Both are true anyway.

八奈見杏菜 loses. That is the premise she is introduced with and the one she is
still living in by the end. She loses and then orders more, and the show's own
site keeps a straight-faced running tally of it, episode by episode, scene by
scene, in kcal and carbohydrates, as though the committee commissioned a
nutritional survey. Somebody built that page. Somebody picked Quantico for the
digits and gave every one of them its own yellow tile.

The default theme is hers, taken from the official site's
[calorie meter](https://makeine-anime.com/special/calorie_meter/) — the navy,
the vermillion, the yellow kcal tiles and the two typefaces are all its own. How
it is put together, and the one file in the project allowed to know a theme's
name, is in [themes](docs/usage/themes.md) and
[the theming system](docs/tech/low-level-design/theming-system.md).

---

負けヒロインが多すぎる！ is © Takibi Amamori / Shogakukan / "Makeine" Production
Committee. Nothing here is affiliated with or endorsed by them; the colours and
type choices are cited the way you would cite a source, and the artwork is not
redistributed. Go watch it.
