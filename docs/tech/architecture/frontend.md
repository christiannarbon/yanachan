# Frontend

Vue 3 with `<script setup>`, TypeScript, Vite. No router library, no state
library, no component framework — the whole app is one page with a navigation
rail down its left side, and the state that outlives a component fits in a few
modules.

## Layout

```
frontend/
  src/components/      AuthGate, SideNav, RepoGroup, PrCard, StatsPanel,
                       WeekChart, SettingsPanel, ModalDialog, ThemePicker,
                       LocalePicker, CornerControls
  src/composables/     useTheme, useRepoGroups, useSideNav, useRouting
  src/i18n/            useI18n, Msg.vue, and the en / ja catalogs
  src/styles/          theme tokens, the generated palettes, the calorie meter layer
  src/lib/             API client, types, time helpers
  scripts/             the theme generator and its colour maths
```

## App.vue owns the data

`App.vue` is the only component that talks to the API. It holds `authStatus`,
`board`, `stats` and `settings` as refs, and passes them down. Components emit
back up; nothing fetches on its own.

Bootstrap: ask `/api/auth/status`; if unauthenticated, render `AuthGate` and
stop. If authenticated, load settings and refresh.

`AuthGate` branches on the status's `authMode`. Local mode shows the gh CLI,
environment token and device flow cards; server mode shows one **Sign in with
GitHub** button. That sign-in is a page navigation to `loginUrl`, never a fetch:
GitHub's redirect has to happen in the top-level page for the sign-in cookie to
be set. A failed sign-in comes back as `?authError=<code>`, and `takeAuthError()`
(`lib/authError.ts`) reads and removes it as the first thing `onMounted` does,
because routing rewrites the URL right after and would drop it. Every 401 goes
through `handleAuthFailure()` in `App.vue`. When the body carries
`"reauth": true` (the token is past its maximum age but the session is fine),
`lib/reauth.ts` sends the page through GitHub and back to the same path, waiting
until the tab is visible and going at most once a minute. Any other 401 shows
the gate. A reauth that would loop — a second one within the minute, or one on
a page that has just come back with `?authError=` — shows a recovery screen
instead, with the reason, **Renew sign-in** and **Sign out**. The decision is
`decideAuthFailure()`, a pure function with unit tests in
`lib/reauth.test.ts`. A page restored from the back/forward cache reloads,
so nothing is left showing "Opening GitHub".

A refresh fires both endpoints together — `/api/board` and `/api/stats` — with
their errors kept in **separate** refs. A failing week must not blank the
board's own notice, and the dashboard reports its own trouble in place, through
`ErrorNotice`, which retries only the half that failed.

Optional auto-refresh runs every 5 minutes; a separate 30-second timer advances
the clock that "3 hours ago" is rendered against, so relative stamps do not go
stale on a tab left open.

`FIXED_TABS` (`dashboard`) exists so a reload never navigates away from a view
when the section list changes underneath it. Settings is not in it: it is a
dialog over the board, held open by one flag and no path.

## Paths

Which section is showing is one string, and the path is where it is written
down, so every view has a link that survives a reload:

| Path | Shows |
| --- | --- |
| `/dashboard` | your week, and where `/` and anything unreadable land |
| `/prs/mine`, `/prs/review` | the two built-in queues |
| `/prs/team/<org>/<slug>` | a followed team |
| `/prs/org/<login>` | a followed organization |

Settings has no path of its own — it is a dialog, not a view. `/settings` was
one before, so `isLegacySettingsPath` still opens the dialog for a bookmark to
it and the address corrects itself to `/dashboard`.

`lib/routes.ts` is the mapping and nothing else; `composables/useRouting.ts`
holds the ref and talks to the history API. A click pushes an entry, so back
returns you to the previous section. A correction replaces one instead — an
unreadable path on arrival, or a link to a team that is no longer followed —
because a back button that walks into those again is a trap.

The Go handler already serves `index.html` for any path that is not a file, so
deep links work in the shipped binary as well as under Vite.

## The shell

`App.vue` lays the page out as an app shell rather than a scrolling page: a
header that stays put, and under it a rail and the board, each scrolling on its
own. A rail listing twenty organizations therefore stays reachable from the
bottom of a long queue.

The shell fills the window rather than sitting in a centred column, so the rail
is against the left edge and a wide monitor spends its width on pull requests.
The header's 20px of padding puts the title over the navigation labels below it.

Under 860px the rail has nowhere to sit, so it becomes a drawer over the board
with a scrim behind it and a button in the header.

## The four pieces of global state

All four are module-level refs with a `localStorage` mirror, all readable from
any component, none of them a store library.

| Module | Holds | Key |
| --- | --- | --- |
| `composables/useTheme.ts` | the active theme, written to `[data-art]` on the root | `yana.art` |
| `i18n/index.ts` | the active locale and the catalog it selects | `yana.locale` |
| `composables/useRepoGroups.ts` | which repository groups the viewer has folded away | `yana.collapsedRepos` |
| `composables/useSideNav.ts` | which navigation headings the viewer has folded away | `yana.navGroups` |

`composables/useRouting.ts` is a fifth module-level ref, but its mirror is the
address bar rather than `localStorage`.

The first two read a pre-rename key as a fallback. All of them wrap every
`localStorage` access in `try/catch` — private windows and blocked site data
throw rather than returning null.

The folded set is keyed by repository and not by section, which is the whole
point of it: a repository folded away on one queue is folded on all of them.
`groupByRepo` in the same module does the splitting, and it rearranges without
re-ranking — a group takes the position of its first entry, and entries keep
their order inside it, so the board's sort survives the regrouping.

Theme webfonts are loaded lazily, on first selection, so nine of the ten
palettes cost nothing until someone tries them.

## Components

| Component | Responsibility |
| --- | --- |
| `AuthGate` | the first screen: what sign-in paths exist, and the device-flow polling |
| `SideNav` | the navigation rail: the sections, grouped under foldable headings |
| `StatsPanel` | the week's tiles, the calorie figure, the superlatives |
| `WeekChart` | three strips over one shared scale, plus the visually-hidden table |
| `RepoGroup` | one repository's cards under a heading that folds them away, in as many columns as the window fits |
| `PrCard` | one pull request: indicators, the left border, the actors line |
| `SettingsPanel` | teams, orgs, view settings, session — the dialog's body |
| `ModalDialog` | a titled panel over the page, wrapping the browser's `<dialog>` |
| `ThemePicker` / `LocalePicker` | the two pickers |
| `CornerControls` | the corner cluster: the two pickers, plus the gear that opens settings. Shared with `AuthGate`, which asks for no gear |
| `ErrorNotice` | a refresh that failed, with the one button worth offering |

## Where styling stops being a component's business

Components speak one semantic vocabulary — `--panel`, `--text`, `--fact`
(primary), `--dim` (attention) — and never a colour literal or a theme name.
That is what makes adding a palette a data change.

There is exactly one deliberate exception, `styles/calorie-meter.css`, and it is
documented in [theming system](../low-level-design/theming-system.md#the-one-exception).

## Types

`src/lib/types.ts` mirrors the backend's JSON by hand. There is no code
generation: the surface is a dozen structs, and the drift is caught by
`vue-tsc` the moment a field is read that the type does not have.

```sh
make test    # includes vue-tsc --noEmit
```

## Dev server

Vite on 19090, proxying `/api` to 19080 with `changeOrigin: false` — the
browser's `Host` header survives the proxy, which is what lets the backend's
[host allowlist](security.md) see a real hostname in development. The backend is
told about the dev origin through `GHDASH_DEV_ORIGIN`, which `make dev` sets.
