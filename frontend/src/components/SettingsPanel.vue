<script setup lang="ts">
/**
 * Everything you can configure, as the body of the settings dialog.
 *
 * It is one column of sections rather than a page of cards: the dialog is
 * already a raised panel, and stacking cards inside it only draws boxes within
 * a box. A hairline between sections says the same thing more quietly.
 */
import { computed, onMounted, ref } from 'vue'
import { useI18n } from '../i18n'
import Msg from '../i18n/Msg.vue'
import { ApiError, api } from '../lib/api'
import type { Settings, Suggestions } from '../lib/types'

const props = defineProps<{ settings: Settings; sessionMode: string; login: string }>()
const emit = defineEmits<{
  (e: 'saved', s: Settings): void
  (e: 'signout'): void
  (e: 'authFailure', err: ApiError): void
}>()

const { t } = useI18n()

const draft = ref<Settings>(JSON.parse(JSON.stringify(props.settings)) as Settings)
const teamInput = ref('')
const orgInput = ref('')
/** Held as a key so the note follows a language switch. */
const message = ref<'' | 'saved'>('')
const error = ref('')
const saving = ref(false)
const suggestions = ref<Suggestions | null>(null)
const loadingSuggestions = ref(false)

onMounted(loadSuggestions)

async function loadSuggestions() {
  loadingSuggestions.value = true
  try {
    suggestions.value = await api.suggestions()
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) {
      emit('authFailure', e)
      return
    }
    suggestions.value = { orgs: [], teams: [], warning: (e as Error).message }
  } finally {
    loadingSuggestions.value = false
  }
}

function addTeam(value?: string) {
  const raw = (value ?? teamInput.value).trim().replace(/^https?:\/\/github\.com\/orgs\//, '').replace(/\/teams\//, '/')
  if (!raw) return
  if (!raw.includes('/')) {
    error.value = t.value.settings.teamFormat
    return
  }
  if (!draft.value.teams.some((existing) => existing.toLowerCase() === raw.toLowerCase())) {
    draft.value.teams.push(raw)
  }
  teamInput.value = ''
  error.value = ''
}

function addOrg(value?: string) {
  const raw = (value ?? orgInput.value).trim().replace(/^https?:\/\/github\.com\//, '').replace(/\/$/, '')
  if (!raw) return
  if (raw.includes('/')) {
    error.value = t.value.settings.orgFormat
    return
  }
  if (!draft.value.orgs.some((o) => o.toLowerCase() === raw.toLowerCase())) {
    draft.value.orgs.push(raw)
  }
  orgInput.value = ''
  error.value = ''
}

function removeTeam(index: number) {
  draft.value.teams.splice(index, 1)
}

function removeOrg(index: number) {
  draft.value.orgs.splice(index, 1)
}

async function save() {
  saving.value = true
  error.value = ''
  message.value = ''
  try {
    const saved = await api.saveSettings(draft.value)
    draft.value = JSON.parse(JSON.stringify(saved)) as Settings
    message.value = 'saved'
    emit('saved', saved)
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    saving.value = false
  }
}

const modeLabel = computed<string>(() => {
  switch (props.sessionMode) {
    case 'gh-cli':
      return t.value.settings.modeGhCli
    case 'oauth':
      return t.value.settings.modeOauth
    case 'env-token':
      return t.value.settings.modeEnvToken
    case 'github':
      return t.value.settings.modeGithub
    default:
      return props.sessionMode
  }
})
</script>

<template>
  <div class="settings">
    <section class="block">
      <h2>{{ t.settings.teamsTitle }}</h2>
      <p class="soft">
        <Msg :text="t.settings.teamsExplain">
          <template #format><code>org/team-slug</code></template>
        </Msg>
      </p>
      <ul class="chips" v-if="draft.teams.length">
        <li v-for="(team, i) in draft.teams" :key="team" class="chip">
          <span class="mono">{{ team }}</span>
          <button class="chip-x" :title="t.common.remove" @click="removeTeam(i)">
            {{ t.common.remove }}
          </button>
        </li>
      </ul>
      <p v-else class="empty">{{ t.settings.teamsEmpty }}</p>
      <div class="row">
        <input type="text" v-model="teamInput" placeholder="org/team-slug" @keyup.enter="addTeam()" />
        <button @click="addTeam()">{{ t.settings.teamAdd }}</button>
      </div>
      <div v-if="suggestions && suggestions.teams.length" class="suggest">
        <span class="soft">{{ t.settings.teamsYours }}</span>
        <button
          v-for="team in suggestions.teams"
          :key="team"
          class="ghost suggest-item mono"
          @click="addTeam(team)"
        >{{ team }}</button>
      </div>
    </section>

    <section class="block">
      <h2>{{ t.settings.orgsTitle }}</h2>
      <p class="soft">{{ t.settings.orgsExplain }}</p>
      <ul class="chips" v-if="draft.orgs.length">
        <li v-for="(org, i) in draft.orgs" :key="org" class="chip">
          <span class="mono">{{ org }}</span>
          <button class="chip-x" :title="t.common.remove" @click="removeOrg(i)">
            {{ t.common.remove }}
          </button>
        </li>
      </ul>
      <p v-else class="empty">{{ t.settings.orgsEmpty }}</p>
      <div class="row">
        <input type="text" v-model="orgInput" placeholder="organization-login" @keyup.enter="addOrg()" />
        <button @click="addOrg()">{{ t.settings.orgAdd }}</button>
      </div>
      <div v-if="suggestions && suggestions.orgs.length" class="suggest">
        <span class="soft">{{ t.settings.orgsYours }}</span>
        <button
          v-for="org in suggestions.orgs"
          :key="org"
          class="ghost suggest-item mono"
          @click="addOrg(org)"
        >{{ org }}</button>
      </div>
      <p v-if="loadingSuggestions" class="soft">{{ t.settings.membershipsLoading }}</p>
      <p v-else-if="suggestions?.warning" class="soft">
        {{ t.settings.membershipsFailed(suggestions.warning) }}
      </p>
    </section>

    <section class="block">
      <h2>{{ t.settings.viewTitle }}</h2>
      <div class="field">
        <label for="limit">{{ t.settings.limitLabel }}</label>
        <input id="limit" type="number" min="1" max="100" v-model.number="draft.limit" />
        <span class="soft">
          <Msg :text="t.settings.limitHint">
            <template #flag><code>--limit</code></template>
          </Msg>
        </span>
      </div>
      <div class="field">
        <label for="hours">{{ t.settings.windowLabel }}</label>
        <input id="hours" type="number" min="0" max="720" v-model.number="draft.windowHours" />
        <span class="soft">{{ t.settings.windowHint }}</span>
      </div>
      <div class="field">
        <label class="check">
          <input type="checkbox" v-model="draft.onlyActive" />
          {{ t.settings.onlyActive }}
        </label>
      </div>
      <div class="field">
        <label class="check">
          <input type="checkbox" v-model="draft.showUrls" />
          {{ t.settings.showUrls }}
        </label>
      </div>
    </section>

    <section class="block">
      <h2>{{ t.settings.sessionTitle }}</h2>
      <p class="soft">
        <Msg :text="t.settings.sessionLine">
          <template #login><strong>{{ login }}</strong></template>
          <template #mode>{{ modeLabel }}</template>
        </Msg>
      </p>
      <button class="accent" @click="emit('signout')">
        {{ sessionMode === 'github' ? t.settings.signOutServer : t.settings.signOut }}
      </button>
    </section>

    <div class="actions">
      <p v-if="error" class="notice">{{ error }}</p>
      <p v-else-if="message" class="ok-note">{{ t.settings.saved }}</p>
      <button class="primary" :disabled="saving" @click="save">
        {{ saving ? t.settings.saving : t.settings.save }}
      </button>
    </div>
  </div>
</template>

<style scoped>
.settings { display: flex; flex-direction: column; }
.block { padding: var(--space-5); }
.block + .block { border-top: 1px solid var(--border); }
.block h2 { margin: 0 0 8px; font-size: 14px; color: var(--heading); }
.block > p { margin: 0 0 12px; }

/* Saving is the panel's one action, so it leaves the View section -- where it
   looked like it saved only that -- for a strip along the foot of the dialog.
   Sticky, so a long panel never asks you to scroll to reach it. */
.actions {
  position: sticky;
  bottom: 0;
  z-index: 1;
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: var(--space-3);
  padding: var(--space-4) var(--space-5);
  border-top: 1px solid var(--border);
  background: var(--panel-raised);
}

/* Whatever the last save had to say, beside the button that caused it. Both
   are lines of text here rather than blocks: the strip is already set apart,
   and a box inside it would only crowd the button. */
.actions > p {
  flex: 1 1 auto;
  min-width: 0;
  margin: 0;
  font-size: 12.5px;
}
.actions .notice { padding: 0; border: 0; background: none; }
.actions .ok-note { color: var(--on-fact-soft); }
/* The note takes the width that is left and wraps; the button keeps its own
   label on one line. */
.actions button { white-space: nowrap; }

.chips { list-style: none; display: flex; flex-wrap: wrap; gap: 8px; padding: 0; margin: 0 0 12px; }
.chip {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  padding: 4px 6px 4px 10px;
  border: 1px solid var(--fact-line);
  background: var(--fact-soft);
  border-radius: 999px;
  font-size: 12.5px;
  color: var(--heading);
}
.chip-x {
  border: none;
  background: transparent;
  color: var(--on-dim-soft);
  padding: 0 6px;
  font-size: 11px;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  font-weight: 600;
}
.chip-x:hover { background: var(--dim-soft); }

.empty { color: var(--text-faint); margin: 0 0 12px; }

.suggest { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; margin-top: 12px; font-size: 12.5px; }
.suggest-item { padding: 3px 8px; font-size: 12px; border: 1px dashed var(--border-strong); }

.field { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; margin-bottom: 12px; }
.field label { min-width: 190px; color: var(--text-muted); }
.field input[type='number'] { width: 90px; }
.check { display: flex; align-items: center; gap: 8px; min-width: 0; }
</style>
