<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import AuthGate from './components/AuthGate.vue'
import CornerControls from './components/CornerControls.vue'
import ErrorNotice from './components/ErrorNotice.vue'
import ModalDialog from './components/ModalDialog.vue'
import RepoGroup from './components/RepoGroup.vue'
import SettingsPanel from './components/SettingsPanel.vue'
import SideNav from './components/SideNav.vue'
import StatsPanel from './components/StatsPanel.vue'
import { groupByRepo, useRepoGroups } from './composables/useRepoGroups'
import { useRouting } from './composables/useRouting'
import { useSideNav } from './composables/useSideNav'
import Msg from './i18n/Msg.vue'
import { useI18n } from './i18n'
import { ApiError, api } from './lib/api'
import { takeAuthError, type AuthErrorCode } from './lib/authError'
import { reauthenticate } from './lib/reauth'
import { isLegacySettingsPath } from './lib/routes'
import type { AuthStatus, Board, Settings, Stats } from './lib/types'

const { t, ago, stamp, sectionTitle, windowLabel } = useI18n()
const { isCollapsed, toggle: toggleRepo, setAll: setAllRepos } = useRepoGroups()
const { drawerOpen } = useSideNav()
const { activeSection: activeTab, go, startRouting } = useRouting()

/** A view that is not a board section, so a reload never navigates away from
 *  it even when the section list changes underneath. */
const FIXED_TABS = ['dashboard']

const authStatus = ref<AuthStatus | null>(null)
const board = ref<Board | null>(null)
const stats = ref<Stats | null>(null)
const settings = ref<Settings | null>(null)
const loading = ref(true)
const refreshing = ref(false)
const error = ref('')
/** Kept apart from `error` so a failing week does not blank the board's own
 *  notice, and so the dashboard can report its trouble in its own tab. */
const statsError = ref('')
const autoRefresh = ref(false)
const now = ref(Date.now())
/** Settings is a dialog over the board rather than a page, so nothing outside
 *  this flag has to know it is open. */
const settingsOpen = ref(false)
/** A failed sign-in's reason, read off the address bar before routing drops it. */
const authError = ref<AuthErrorCode | ''>('')
/** The page is leaving for GitHub to renew its token, now or once the tab is
 *  visible; nothing else should be fetched meanwhile. */
const reauthPending = ref(false)

let clockTimer: number | undefined
let refreshTimer: number | undefined
let stopRouting: (() => void) | undefined
const REFRESH_MS = 5 * 60 * 1000

onMounted(async () => {
  authError.value = takeAuthError()
  clockTimer = window.setInterval(() => (now.value = Date.now()), 30_000)
  stopRouting = startRouting()
  window.addEventListener('keydown', onKeydown)
  // Settings used to be a page; a bookmark to it opens the dialog instead.
  settingsOpen.value = isLegacySettingsPath(location.pathname)
  // `/`, and anything the app cannot read, become the landing path rather than
  // sitting in the address bar naming a page nobody is looking at.
  go(activeTab.value, true)
  await bootstrap()
})

onUnmounted(() => {
  if (clockTimer !== undefined) window.clearInterval(clockTimer)
  if (stopRouting) stopRouting()
  window.removeEventListener('keydown', onKeydown)
  stopAutoRefresh()
})

watch(autoRefresh, (on) => (on ? startAutoRefresh() : stopAutoRefresh()))

function startAutoRefresh() {
  stopAutoRefresh()
  refreshTimer = window.setInterval(() => void refreshAll(), REFRESH_MS)
}
function stopAutoRefresh() {
  if (refreshTimer !== undefined) window.clearInterval(refreshTimer)
  refreshTimer = undefined
}

async function bootstrap() {
  loading.value = true
  error.value = ''
  try {
    authStatus.value = await api.authStatus()
    if (authStatus.value.authenticated) {
      settings.value = await api.settings()
      await refreshAll()
    }
  } catch (e) {
    // A session can end between the status call and the settings call.
    if (!(await handleAuthFailure(e))) error.value = (e as Error).message
  } finally {
    loading.value = false
  }
}

/** The one answer to a 401: renew the token through GitHub when the server
 *  asks for it, otherwise go back to the gate. False when e is not a 401. */
async function handleAuthFailure(e: unknown): Promise<boolean> {
  if (!(e instanceof ApiError && e.status === 401)) return false
  if (reauthPending.value) return true
  const loginUrl = authStatus.value?.loginUrl
  if (e.reauth && loginUrl && reauthenticate(loginUrl)) {
    reauthPending.value = true
    return true
  }
  board.value = null
  authStatus.value = await api.authStatus()
  return true
}

/** Both halves of a refresh go out together: they are separate endpoints, and
 *  the landing tab should not wait on the queues it is not showing. */
async function refreshAll() {
  if (reauthPending.value) return
  refreshing.value = true
  try {
    await Promise.all([loadBoard(), loadStats()])
  } finally {
    refreshing.value = false
  }
}

async function loadBoard() {
  error.value = ''
  try {
    const next = await api.board()
    board.value = next
    now.value = Date.now()
    if (!FIXED_TABS.includes(activeTab.value) && !next.sections.some((s) => s.id === activeTab.value)) {
      // A link to a team you no longer follow, or a section that has gone.
      go(next.sections[0]?.id ?? 'mine', true)
    }
  } catch (e) {
    if (!(await handleAuthFailure(e))) error.value = (e as Error).message
  }
}

async function loadStats() {
  statsError.value = ''
  try {
    stats.value = await api.stats()
  } catch (e) {
    // A plain 401 is the board's to handle: it reloads the auth status, and
    // this half would only race it to the same conclusion.
    if (e instanceof ApiError && e.status === 401) {
      if (e.reauth) await handleAuthFailure(e)
    } else {
      statsError.value = (e as Error).message
    }
  }
}

/** A retry from a notice. It reloads only the half that failed, and borrows the
 *  refresh flag so the button says it is going. */
async function retry(half: 'board' | 'stats') {
  refreshing.value = true
  try {
    await (half === 'board' ? loadBoard() : loadStats())
  } finally {
    refreshing.value = false
  }
}

async function onAuthenticated() {
  authError.value = ''
  await bootstrap()
}

async function onSignOut() {
  await api.logout()
  board.value = null
  stats.value = null
  settings.value = null
  go('dashboard')
  settingsOpen.value = false
  drawerOpen.value = false
  authStatus.value = await api.authStatus()
}

async function onSettingsSaved(saved: Settings) {
  settings.value = saved
  // The limit and the followed refs feed both endpoints, so both reload.
  await refreshAll()
}

/** The navigation's one job. On a narrow window it also shuts the drawer,
 *  which is covering the thing you just asked to see. */
function onSelect(id: string) {
  go(id)
  drawerOpen.value = false
}

function onKeydown(e: KeyboardEvent) {
  // The dialog answers Escape itself, and the drawer it covers should not
  // close behind it on the same press.
  if (e.key === 'Escape' && !settingsOpen.value) drawerOpen.value = false
}

const currentSection = computed(() => board.value?.sections.find((s) => s.id === activeTab.value) ?? null)
const showUrls = computed(() => settings.value?.showUrls ?? true)
const totalHot = computed(() => board.value?.sections.reduce((sum, s) => sum + s.hot, 0) ?? 0)

/** The name of the page, for the browser tab and for the history entry the
 *  path just made. */
watch(
  [activeTab, currentSection, t],
  () => {
    const name =
      activeTab.value === 'dashboard'
        ? t.value.sections.dashboard
        : currentSection.value
          ? sectionTitle(currentSection.value)
          : ''
    document.title = name ? `${name} · ${t.value.appName}` : t.value.appName
  },
  { immediate: true },
)

/** The visible queue, split by repository. Empty on the two fixed tabs. */
const repoGroups = computed(() => groupByRepo(currentSection.value?.entries ?? []))
/** Drives the one button: it offers whichever move is still available. */
const allCollapsed = computed(
  () => repoGroups.value.length > 0 && repoGroups.value.every((g) => isCollapsed(g.repo)),
)

function toggleAllGroups() {
  setAllRepos(
    repoGroups.value.map((g) => g.repo),
    !allCollapsed.value,
  )
}

async function toggleOnlyActive() {
  if (!settings.value) return
  const next = { ...settings.value, onlyActive: !settings.value.onlyActive }
  settings.value = await api.saveSettings(next)
  await loadBoard()
}

</script>

<template>
  <div class="shell">
    <p v-if="loading" class="centered soft">{{ t.common.loading }}</p>

    <AuthGate
      v-else-if="authStatus && !authStatus.authenticated"
      :status="authStatus"
      :auth-error="authError"
      @authenticated="onAuthenticated"
    />

    <p v-else-if="reauthPending && !board" class="centered notice">{{ t.auth.reauthPending }}</p>

    <template v-else-if="board && settings">
      <header class="topbar">
        <div class="topbar-inner">
          <button
            class="ghost drawer-toggle"
            aria-controls="rail"
            :aria-expanded="drawerOpen"
            @click="drawerOpen = !drawerOpen"
          >
            {{ drawerOpen ? t.nav.close : t.nav.open }}
          </button>
          <div class="brand">
            <h1>{{ t.appName }}</h1>
            <p class="brand-sub soft">
              <Msg :text="t.board.summary">
                <template #login><span class="mono">@{{ board.login }}</span></template>
                <template #label>{{ windowLabel(board.window) }}</template>
                <template #stamp>{{ stamp(board.window.cutoff) }}</template>
              </Msg>
              <template v-if="totalHot > 0">
                ·
                <span class="attention">{{ t.board.needingAttention(totalHot) }}</span>
              </template>
            </p>
          </div>
          <div class="row controls">
            <button
              v-if="activeTab !== 'dashboard'"
              class="ghost"
              :class="{ toggled: settings.onlyActive }"
              @click="toggleOnlyActive"
            >
              {{ settings.onlyActive ? t.board.showingActiveOnly : t.board.showingEverything }}
            </button>
            <label class="auto soft">
              <input type="checkbox" v-model="autoRefresh" />
              {{ t.board.autoRefresh }}
            </label>
            <button class="primary" :disabled="refreshing" @click="refreshAll">
              {{ refreshing ? t.board.refreshing : t.board.refresh }}
            </button>
            <CornerControls with-settings @settings="settingsOpen = true" />
          </div>
        </div>
      </header>

      <div class="body">
        <!-- The backdrop only exists while the rail is a drawer, and its only
             job is to give the tap outside somewhere to land. -->
        <div v-if="drawerOpen" class="scrim" @click="drawerOpen = false"></div>

        <aside id="rail" class="rail" :class="{ 'rail-open': drawerOpen }">
          <SideNav :sections="board.sections" :active-id="activeTab" @select="onSelect" />
        </aside>

        <main class="content">
          <p v-if="reauthPending" class="notice">{{ t.auth.reauthPending }}</p>
          <ErrorNotice v-if="error" :message="error" :busy="refreshing" @retry="retry('board')" />
          <p v-if="board.warning" class="notice">{{ t.board.githubReported(board.warning) }}</p>

          <template v-if="activeTab === 'dashboard'">
            <ErrorNotice
              v-if="statsError"
              :message="statsError"
              :busy="refreshing"
              @retry="retry('stats')"
            />
            <StatsPanel v-if="stats" :stats="stats" />
            <p v-else-if="!statsError" class="centered soft">{{ t.common.loading }}</p>
            <p v-if="stats?.warning" class="notice">{{ t.board.githubReported(stats.warning) }}</p>
          </template>

          <template v-else-if="currentSection">
            <div class="section-head">
              <div>
                <h2>{{ sectionTitle(currentSection) }}</h2>
                <p class="soft">
                  {{ t.board.prCount(currentSection.total) }}
                  <template v-if="repoGroups.length > 1">
                    · {{ t.board.repoCount(repoGroups.length) }}
                  </template>
                  <template v-if="currentSection.hot > 0">
                    · {{ t.board.needingAttention(currentSection.hot) }}
                  </template>
                  <template v-if="settings.onlyActive && currentSection.total !== currentSection.entries.length">
                    · {{ t.board.hiddenAsQuiet(currentSection.total - currentSection.entries.length) }}
                  </template>
                </p>
              </div>
              <button v-if="repoGroups.length > 1" class="ghost fold-all" @click="toggleAllGroups">
                {{ allCollapsed ? t.board.expandAll : t.board.collapseAll }}
              </button>
            </div>

            <p v-if="currentSection.error" class="notice">{{ currentSection.error }}</p>

            <div v-if="repoGroups.length" class="groups">
              <RepoGroup
                v-for="group in repoGroups"
                :key="group.repo"
                :repo="group.repo"
                :entries="group.entries"
                :hot="group.hot"
                :collapsed="isCollapsed(group.repo)"
                :kind="currentSection.kind"
                :show-url="showUrls"
                :now="now"
                @toggle="toggleRepo(group.repo)"
              />
            </div>
            <p v-else class="card empty-state soft">
              {{ t.board.empty }}
              <Msg
                v-if="currentSection.kind === 'team' || currentSection.kind === 'org'"
                :text="t.board.emptyScope"
              >
                <template #ref><span class="mono">{{ currentSection.ref }}</span></template>
              </Msg>
            </p>
          </template>

          <footer class="foot soft">
            {{ t.board.footer(ago(board.generatedAt, now), board.limit) }}
          </footer>
        </main>
      </div>

      <ModalDialog
        v-if="settingsOpen"
        :title="t.settings.title"
        @close="settingsOpen = false"
      >
        <SettingsPanel
          :settings="settings"
          :session-mode="board.authMode"
          :login="board.login"
          @saved="onSettingsSaved"
          @signout="onSignOut"
          @auth-failure="handleAuthFailure"
        />
      </ModalDialog>
    </template>

    <p v-else class="centered notice">{{ error || t.common.somethingWrong }}</p>
  </div>
</template>

<style scoped>
/* An app shell rather than a scrolling page: the header stays put, and the
   navigation and the board scroll independently underneath it. That is what
   lets a rail of twenty organizations stay reachable from the bottom of a long
   queue without either one dragging the other around.

   The shell fills the window rather than sitting in a centred column: the rail
   belongs against the left edge, and a wide monitor should spend its width on
   pull requests instead of two grey gutters. */
.shell {
  --rail: 256px;
  height: 100%;
  /* The board keeps its own scrolling inside .body; this is here for the sign-in
     screen, which is a single tall block and has to be able to scroll. */
  overflow-y: auto;
  display: flex;
  flex-direction: column;
}
.centered { padding: 60px 20px; text-align: center; }

.topbar {
  flex: none;
  background: var(--panel);
  border-bottom: 1px solid var(--border);
  z-index: 10;
}
.topbar-inner {
  /* 20px puts the title over the navigation labels below it, which sit 10px
     into the rail's own 10px of padding. */
  padding: 14px 20px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  flex-wrap: wrap;
}

.body {
  flex: 1 1 auto;
  min-height: 0;
  width: 100%;
  display: flex;
  align-items: stretch;
}
.rail {
  flex: none;
  width: var(--rail);
  border-right: 1px solid var(--border);
  background: var(--panel);
  overflow-y: auto;
  overscroll-behavior: contain;
}
.scrim { display: none; }
/* The rail is the page's navigation on a wide window; the button that would
   hide it only appears once there is no room to keep it open. */
.drawer-toggle { display: none; }

.brand h1 {
  margin: 0;
  font-size: 17px;
  letter-spacing: -0.01em;
  color: var(--heading);
}
.brand-sub { margin: 2px 0 0; font-size: 12.5px; }
.attention { color: var(--on-dim-soft); font-weight: 600; }

.controls { gap: 8px; }
.controls .toggled { color: var(--on-fact-soft); background: var(--fact-soft); }
.auto { display: inline-flex; align-items: center; gap: 6px; font-size: 13px; }

.content {
  flex: 1 1 auto;
  min-width: 0;
  padding: 20px;
  overflow-y: auto;
}
.section-head {
  margin-bottom: 14px;
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
}
.section-head h2 { margin: 0; font-size: 15px; color: var(--heading); }
.section-head p { margin: 2px 0 0; font-size: 12.5px; }
.fold-all { font-size: 12.5px; white-space: nowrap; }

/* Each group spaces itself from the one above, so this only stacks them. */
.groups { display: flex; flex-direction: column; }
.empty-state { padding: 28px; text-align: center; }
.foot { margin-top: 28px; font-size: 12px; text-align: center; }

/* Below this the rail has nowhere to sit, so it becomes a drawer over the
   board and the button in the header is the way in and out of it. */
@media (max-width: 860px) {
  .drawer-toggle { display: inline-flex; order: -1; }
  /* The button and the title share the first line; the summary under the title
     wraps within the brand rather than pushing the button onto a line alone. */
  .brand { flex: 1 1 200px; min-width: 0; }
  .rail {
    position: fixed;
    top: 0;
    bottom: 0;
    left: 0;
    z-index: 30;
    box-shadow: var(--shadow-lg);
    transform: translateX(-100%);
    transition: transform var(--dur) var(--ease);
  }
  .rail-open { transform: none; }
  .scrim {
    display: block;
    position: fixed;
    inset: 0;
    z-index: 20;
    background: var(--overlay);
  }
}
</style>
