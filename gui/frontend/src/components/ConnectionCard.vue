<!-- Connection hero: status pill + power ring + current server +
     one clear Connect/Disconnect button. The old giant gray circle
     had near-zero contrast and no explicit action; the state now
     reads at a glance in both themes. -->
<template>
  <div class="nb-card nb-conn" :class="running ? 'is-on' : 'is-off'">
    <div class="nb-conn-top">
      <button
        class="nb-power-btn"
        :class="{ on: running }"
        :disabled="btnDisabled"
        :title="connectTitle"
        @click="toggle"
      >
        <Icon name="power" :size="44" />
      </button>
      <div class="nb-conn-meta">
        <span class="nb-pill" :class="pillClass">{{ pillText }}</span>
        <span v-if="current" class="nb-conn-server">
          <span class="nb-flag">{{ flagEmoji(current.countryCode) }}</span>
          <span class="meta">
            <span class="name">{{ current.name }}</span>
            <span class="city">{{ current.city }}</span>
          </span>
        </span>
        <span v-else class="muted">No server selected — add one in Proxies.</span>
      </div>
      <div class="nb-conn-rates">
        <template v-if="current && !apiDown">
          <span class="nb-speed down">↓ {{ downText }}</span>
          <span class="nb-speed up">↑ {{ upText }}</span>
        </template>
        <span v-else-if="apiDown" class="nb-speed api-down">API down</span>
      </div>
    </div>
    <button
      class="nb-connect-btn"
      :class="{ 'is-on': running, 'is-cancel': connecting }"
      :disabled="btnDisabled"
      @click="toggle"
    >
      <span class="nb-connect-ico">
        <Icon :name="connecting ? 'x' : running ? 'power' : 'bolt'" :size="18" />
      </span>
      <span class="nb-connect-label">{{ connectLabel }}</span>
      <span v-if="running && !connecting" class="nb-connect-dot" aria-hidden="true" />
    </button>
    <button
      v-if="reconnectVisible()"
      class="nb-act nb-act--ghost nb-conn-reconnect"
      :disabled="btnDisabled"
      :title="reconnectTitle"
      @click="reconnect"
    >
      <Icon name="refresh" :size="14" />
      Reconnect
    </button>
    <div v-if="connection.fault" class="nb-conn-err">{{ connection.fault }}</div>
    <div v-else-if="connection.error" class="nb-conn-err">{{ connection.error }}</div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import Icon from './Icon.vue'
import { useConnectionStore } from '../stores/connection'
import { useProfilesStore } from '../stores/profiles'
import { useSettingsStore } from '../stores/settings'
import { flagEmoji, formatRate } from '../data/servers'
import { profileEndpointLabel } from '../aether'

const connection = useConnectionStore()
const profiles = useProfilesStore()
const settings = useSettingsStore()

const running = computed(() => connection.status.running)
// switching covers the selection IPC that runs BEFORE the store sets its
// busy flag: without it the button stayed enabled for that whole window
// and a second click re-entered toggle and raced the first connect.
const switching = ref(false)
const connectDisabled = computed(() => switching.value || connection.busy || connection.connectBusy)
// Core process alive but stats API unreachable: counters are stale, so
// show "API down" instead of misleading rates.
const apiDown = computed(() => connection.stats.apiDown === true)

const pillText = computed(() =>
  connection.connectBusy ? 'Connecting' : running.value ? 'Connected' : 'Disconnected',
)
const pillClass = computed(() =>
  connection.connectBusy ? 'is-busy' : running.value ? 'is-on' : 'is-off',
)

// Resolve the selected server from real backend profiles only. While a
// core runs, the RUNNING profile is shown: the rail lets the selection
// move without reconnecting, and the card used to label that selection
// "Connected" while the rates and the disconnect action belonged to the
// live profile.
const current = computed(() => {
  const runningID = running.value ? connection.status.profileId : ''
  const id = runningID || settings.settings.activeProfileId || profiles.profiles[0]?.id
  const p = profiles.profiles.find((x) => x.id === id)
  if (!p) return null
  return { id: p.id, name: p.name, city: profileEndpointLabel(p), countryCode: p.countryCode ?? '' }
})

// The hero button and the power ring are one action, and while a connect is
// in flight that action is Cancel. Only the abort's own round-trip disables
// them: a connect can take half a minute (Aether waits 30s for a usable
// port, a Psiphon-fronted chain 120s for its tunnel) and the button that
// used to grey out for all of it is the one the user reaches for.
const connecting = computed(() => connection.connecting)
const btnDisabled = computed(() =>
  connecting.value
    ? connection.cancelling
    : connectDisabled.value || (!running.value && !current.value),
)
const connectLabel = computed(() =>
  connecting.value
    ? connection.cancelling ? 'Cancelling…' : 'Cancel'
    : running.value ? 'Disconnect' : 'Connect',
)
const connectTitle = computed(() =>
  connecting.value
    ? connection.cancelling ? 'Cancelling…' : 'Cancel the connect in flight'
    : running.value ? 'Disconnect' : 'Connect',
)

// The Reconnect mode (Chain tab, in advanced mode) decides both whether
// this button exists and when it shows. "off" removes it. "restart" is
// the tunnel control it has always been, so it needs a tunnel. "sticky"
// sits below Connect whatever the state, because what it promises is
// about the NEXT connect (reuse the chain's last exit hop), not the
// current one — pressing it while disconnected is a plain connect
// through the same call, which is exactly what the button was for.
const reconnectMode = computed(() => settings.settings.reconnectMode || 'restart')

function reconnectVisible(): boolean {
  if (reconnectMode.value === 'off') return false
  if (reconnectMode.value === 'sticky') return !connecting.value
  return running.value && !connecting.value
}

const reconnectTitle = computed(() =>
  reconnectMode.value === 'sticky'
    ? 'Restart the tunnel on the same server, keeping the chain’s last exit hop'
    : 'Restart the tunnel on the same server',
)

// Per-second rates derived from cumulative backend counters.
const downRate = ref(0)
const upRate = ref(0)
// Consecutive ticks with no movement while running; after two, the card
// shows the cumulative total instead of "0 B/s".
const stallTicks = ref(0)
// -1 means "no baseline yet": the next sampleRates() tick only records it.
let prevDown = -1
let prevUp = -1

function sampleRates() {
  const d = connection.stats.downlink ?? 0
  const u = connection.stats.uplink ?? 0
  if (running.value && prevDown >= 0) {
    const dd = Math.max(0, d - prevDown)
    const du = Math.max(0, u - prevUp)
    stallTicks.value = dd === 0 && du === 0 ? stallTicks.value + 1 : 0
    downRate.value = dd
    upRate.value = du
  } else {
    downRate.value = 0
    upRate.value = 0
    stallTicks.value = 0
  }
  prevDown = d
  prevUp = u
}

const stalled = computed(() => running.value && stallTicks.value >= 2)
const downText = computed(() =>
  stalled.value
    ? formatBytes(connection.stats.downlink ?? 0)
    : `${formatRate(downRate.value)} (${formatBytes(connection.stats.downlink ?? 0)})`,
)
const upText = computed(() =>
  stalled.value
    ? formatBytes(connection.stats.uplink ?? 0)
    : `${formatRate(upRate.value)} (${formatBytes(connection.stats.uplink ?? 0)})`,
)

/** Cumulative bytes without "/s": "0 B" / "1.2 KB" / "3.4 MB" / "5.6 GB". */
function formatBytes(n: number): string {
  if (!n || n <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let v = n
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(1)} ${units[i]}`
}

watch(
  running,
  (on) => {
    if (on) {
      // Do NOT snapshot here: the store may not have refreshed yet, and an
      // early snapshot would zero out the first real delta. -1 marks
      // "no baseline", so the first sampleRates() tick only records it.
      prevDown = -1
      prevUp = -1
    } else {
      downRate.value = 0
      upRate.value = 0
      stallTicks.value = 0
    }
  },
  // App keeps both shells mounted (v-show), but this card unmounts
  // when the Home view switches away — without immediate a mount made
  // mid-connection would never sample.
  { immediate: true },
)
// Sample on the connection store's own 1s tick (it replaces `stats`
// on every refresh) instead of running a second timer of our own.
watch(
  () => connection.stats,
  () => {
    if (running.value) sampleRates()
  },
)

// Keep connection state fresh while the card is mounted.
onMounted(() => connection.acquire())
onUnmounted(() => connection.release())

async function toggle() {
  // Cancel comes first: `connectDisabled` is true for the whole connect, so
  // guarding on it here is what used to swallow the press and leave the
  // user waiting out a handshake they had already given up on.
  if (connection.connecting) {
    await connection.cancelConnect()
    return
  }
  if (connectDisabled.value) return
  switching.value = true
  try {
    if (running.value) {
      await connection.disconnect()
    } else {
      const id = settings.settings.activeProfileId || profiles.profiles[0]?.id
      if (!id) return
      // Persist selection so the right panel and reconnect agree; abort
      // the connect when persistence fails (same BUG-004 discipline as
      // the other selection paths).
      try {
        await settings.setActiveProfileId(id)
      } catch {
        return
      }
      await connection.connect(id)
    }
  } finally {
    switching.value = false
  }
}

/** Stop the running tunnel and raise it again on the same profile. */
function reconnect() {
  // The RUNNING profile, not the selected one: the rail lets the selection
  // move without reconnecting, and this button restarts what is up.
  const id = connection.status.profileId || settings.settings.activeProfileId || profiles.profiles[0]?.id
  if (id) void connection.reconnect(id)
}
</script>
