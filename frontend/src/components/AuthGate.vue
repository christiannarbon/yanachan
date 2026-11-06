<script setup lang="ts">
import { computed, onUnmounted, ref } from 'vue'
import { useI18n } from '../i18n'
import Msg from '../i18n/Msg.vue'
import { api } from '../lib/api'
import type { AuthErrorCode } from '../lib/authError'
import type { AuthStatus, DeviceStart } from '../lib/types'
import CornerControls from './CornerControls.vue'

const props = defineProps<{ status: AuthStatus; authError?: AuthErrorCode | '' }>()
const emit = defineEmits<{ (e: 'authenticated'): void }>()

const { t } = useI18n()

const busy = ref<'' | 'gh-cli' | 'env-token' | 'device'>('')
const error = ref('')
const device = ref<DeviceStart | null>(null)
/** Held as a key, not a sentence, so it follows a language switch mid-flow. */
const devicePhase = ref<'' | 'waiting' | 'approved' | 'copied' | 'copyManually'>('')
let timer: number | undefined

const devicePhaseText = computed(() => {
  switch (devicePhase.value) {
    case 'waiting':
      return t.value.auth.oauthWaiting
    case 'approved':
      return t.value.auth.oauthApproved
    case 'copied':
      return t.value.auth.oauthCopied
    case 'copyManually':
      return t.value.auth.oauthCopyManually
    default:
      return ''
  }
})

function stopPolling() {
  if (timer !== undefined) {
    window.clearTimeout(timer)
    timer = undefined
  }
}
onUnmounted(stopPolling)

async function useGhCli() {
  busy.value = 'gh-cli'
  error.value = ''
  try {
    await api.approveGhCli()
    emit('authenticated')
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    busy.value = ''
  }
}

async function useEnvToken() {
  busy.value = 'env-token'
  error.value = ''
  try {
    await api.approveEnvToken()
    emit('authenticated')
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    busy.value = ''
  }
}

async function startDevice() {
  busy.value = 'device'
  error.value = ''
  try {
    device.value = await api.deviceStart()
    devicePhase.value = 'waiting'
    schedulePoll(device.value.interval * 1000)
  } catch (e) {
    error.value = (e as Error).message
    busy.value = ''
  }
}

function schedulePoll(delayMs: number) {
  stopPolling()
  timer = window.setTimeout(poll, delayMs)
}

async function poll() {
  if (!device.value) return
  try {
    const res = await api.devicePoll()
    switch (res.state) {
      case 'complete':
        devicePhase.value = 'approved'
        stopPolling()
        emit('authenticated')
        return
      case 'pending':
        schedulePoll(device.value.interval * 1000)
        return
      case 'slow_down':
        schedulePoll((device.value.interval + 5) * 1000)
        return
      case 'expired':
        error.value = t.value.auth.oauthExpired
        break
      case 'denied':
        error.value = t.value.auth.oauthDenied
        break
    }
  } catch (e) {
    error.value = (e as Error).message
  }
  device.value = null
  busy.value = ''
  stopPolling()
}

async function copyCode() {
  if (!device.value) return
  try {
    await navigator.clipboard.writeText(device.value.userCode)
    devicePhase.value = 'copied'
  } catch {
    devicePhase.value = 'copyManually'
  }
}

const cliStatus = props.status.ghCli

const leaving = ref(false)

/** Held as a code, not a sentence, so it follows a language switch. */
const authErrorText = computed(() => {
  switch (props.authError) {
    case 'denied':
      return t.value.auth.errDenied
    case 'state':
      return t.value.auth.errState
    case 'exchange':
      return t.value.auth.errExchange
    case 'not_allowed':
      return t.value.auth.errNotAllowed
    case 'server':
      return t.value.auth.errServer
    default:
      return ''
  }
})

/** A navigation, not a fetch: GitHub's redirect must happen in the top-level
 *  page so the sign-in cookie is set. */
function signIn() {
  leaving.value = true
  window.location.assign(props.status.loginUrl ?? '/api/auth/login')
}
</script>

<template>
  <div class="gate">
    <CornerControls class="gate-corner" />

    <header class="gate-head">
      <h1>{{ t.appName }}</h1>
      <p class="soft">{{ status.authMode === 'server' ? t.auth.serverIntro : t.auth.intro }}</p>
    </header>

    <section v-if="status.authMode === 'server'" class="card option">
      <h2>{{ t.auth.serverTitle }}</h2>
      <p class="soft">{{ t.auth.serverExplain }}</p>
      <p v-if="authErrorText" class="notice" role="alert">{{ authErrorText }}</p>
      <button class="accent" :disabled="leaving" @click="signIn">
        {{ leaving ? t.auth.serverLeaving : t.auth.serverSignIn }}
      </button>
    </section>

    <template v-else>
      <p v-if="error" class="notice">{{ error }}</p>

      <section class="card option" v-if="status.ghCliAllowed">
        <div class="option-head">
          <h2>{{ t.auth.cliTitle }}</h2>
          <span class="badge" :class="cliStatus.authenticated ? 'badge-green' : 'badge-neutral'">
            {{
              cliStatus.authenticated
                ? t.auth.cliDetected
                : cliStatus.available
                  ? t.auth.cliNotLoggedIn
                  : t.auth.cliNotFound
            }}
          </span>
        </div>

        <template v-if="cliStatus.authenticated">
          <p>
            <Msg :text="cliStatus.login ? t.auth.cliSession : t.auth.cliSessionNoLogin">
              <template #path><code>{{ cliStatus.path }}</code></template>
              <template #host><strong>{{ cliStatus.host }}</strong></template>
              <template #login><strong>{{ cliStatus.login }}</strong></template>
            </Msg>
          </p>
          <p class="soft">
            <Msg :text="t.auth.cliExplain">
              <template #command><code>gh auth token</code></template>
            </Msg>
          </p>
          <button class="primary" :disabled="busy !== ''" @click="useGhCli">
            {{ busy === 'gh-cli' ? t.auth.cliChecking : t.auth.cliApprove }}
          </button>
        </template>

        <template v-else-if="cliStatus.available">
          <p>{{ t.auth.cliNoAccount }}</p>
          <p class="soft">
            <Msg :text="t.auth.cliRunLogin">
              <template #command><code>gh auth login</code></template>
            </Msg>
          </p>
        </template>

        <template v-else>
          <p class="soft">{{ cliStatus.detail || t.auth.cliMissing }}</p>
          <p class="soft">
            <Msg :text="t.auth.cliContainerHint">
              <template #variable><code>GH_TOKEN</code></template>
            </Msg>
          </p>
        </template>
      </section>

      <section class="card option" v-if="status.envTokenAvailable">
        <div class="option-head">
          <h2>{{ t.auth.envTitle }}</h2>
          <span class="badge badge-green">{{ t.auth.envAvailable }}</span>
        </div>
        <p class="soft">
          <Msg :text="t.auth.envExplain">
            <template #variable><code>GH_TOKEN</code></template>
            <template #command><code>make docker-up</code></template>
          </Msg>
        </p>
        <button class="primary" :disabled="busy !== ''" @click="useEnvToken">
          {{ busy === 'env-token' ? t.auth.cliChecking : t.auth.envApprove }}
        </button>
      </section>

      <section class="card option">
        <div class="option-head">
          <h2>{{ t.auth.oauthTitle }}</h2>
          <span class="badge" :class="status.oauthEnabled ? 'badge-green' : 'badge-neutral'">
            {{ status.oauthEnabled ? t.auth.oauthEnabled : t.auth.oauthNotConfigured }}
          </span>
        </div>

        <template v-if="!status.oauthEnabled">
          <p class="soft">
            <Msg :text="t.auth.oauthSetup">
              <template #variable><code>GITHUB_CLIENT_ID</code></template>
            </Msg>
          </p>
        </template>

        <template v-else-if="!device">
          <p class="soft">
            <Msg :text="t.auth.oauthScopes">
              <template #scopes><code>{{ status.oauthScopes }}</code></template>
            </Msg>
          </p>
          <button class="primary" :disabled="busy !== ''" @click="startDevice">
            {{ busy === 'device' ? t.auth.oauthContacting : t.auth.oauthStart }}
          </button>
        </template>

        <template v-else>
          <p>
            <Msg :text="t.auth.oauthOpen">
              <template #link>
                <a :href="device.verificationUri" target="_blank" rel="noopener noreferrer">
                  {{ device.verificationUri }}
                </a>
              </template>
            </Msg>
          </p>
          <div class="code-row">
            <span class="device-code mono">{{ device.userCode }}</span>
            <button @click="copyCode">{{ t.common.copy }}</button>
          </div>
          <p class="soft">{{ devicePhaseText }}</p>
        </template>
      </section>
    </template>
  </div>
</template>

<style scoped>
.gate {
  max-width: 720px;
  margin: 0 auto;
  padding: 48px 20px 64px;
  display: flex;
  flex-direction: column;
  gap: 16px;
}
/* The gate has no topbar to sit in, so the cluster takes the window's own
   corner -- the same place it occupies once the board is up. */
.gate-corner {
  position: fixed;
  top: 16px;
  right: 20px;
  z-index: 20;
}

.gate-head h1 {
  margin: 0;
  font-size: 22px;
  color: var(--heading);
  letter-spacing: -0.01em;
}
.gate-head p { margin: 6px 0 0; }

.option { padding: 18px 20px; }
.option h2 { margin: 0; font-size: 15px; color: var(--heading); }
.option p { margin: 0 0 10px; }
.option-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 10px;
}

.code-row { display: flex; align-items: center; gap: 12px; margin-bottom: 10px; }
.device-code {
  font-size: 24px;
  font-weight: 600;
  letter-spacing: 0.14em;
  color: var(--on-dim-soft);
  background: var(--dim-soft);
  border: 1px solid var(--dim-line);
  border-radius: var(--radius-sm);
  padding: 6px 14px;
}
</style>
