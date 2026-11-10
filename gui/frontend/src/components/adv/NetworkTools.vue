<!-- Advanced Network workspace content: IP / Speed / URL tests.
     Moved verbatim from ConsolePanel (re-hosted, not rewritten):
     template cards and script logic below are byte-identical to the
     old Console -> Network section, minus the outer tab condition. -->
<template>
  <div class="net-stack">
    <!-- A) IP Test -->
    <div id="adv-tool-ip" class="stg-card">
      <div class="stg-head">
        <span class="stg-head-chip"><Icon name="globe" :size="22" /></span>
        <span class="stg-head-text">
          <span class="stg-head-title">IP Test</span>
          <span class="stg-head-sub">Egress IP through the active core</span>
        </span>
      </div>
      <div class="toolbar">
        <button class="nb-act nb-act--primary" :disabled="!canRun" @click="runIP">
          <Icon name="globe" :size="14" />Run IP Test
        </button>
        <button class="nb-act nb-act--warn" :disabled="!ipRunning" @click="stopTool('ip')">
          <Icon name="x" :size="14" />Stop
        </button>
        <span v-if="ipRunning" class="muted"><span class="nb-spin" /> Probing…</span>
      </div>
      <div v-if="ipResult">
        <div class="stg-row">
          <span class="stg-chip"><Icon name="globe" :size="18" /></span>
          <span class="stg-meta"><span class="stg-title">IP</span><span class="stg-sub nb-mono">{{ ipResult.ip || '—' }}</span></span>
        </div>
        <div class="stg-row">
          <span class="stg-chip"><Icon name="shield" :size="18" /></span>
          <span class="stg-meta"><span class="stg-title">Country</span><span class="stg-sub">{{ ipCountry || '—' }}</span></span>
        </div>
      </div>
      <div v-if="ipError" class="nb-json-error">{{ ipError }}</div>
      <p v-if="!ipResult && !ipRunning && !ipError" class="muted">{{ netHint }}</p>
    </div>
    <!-- B) Speed Test -->
    <div id="adv-tool-speed" class="stg-card">
      <div class="stg-head">
        <span class="stg-head-chip"><Icon name="bolt" :size="22" /></span>
        <span class="stg-head-text">
          <span class="stg-head-title">Speed Test</span>
          <span class="stg-head-sub">Throughput through the active core</span>
        </span>
      </div>
      <div class="toolbar">
        <button class="nb-act nb-act--primary" :disabled="!canRun" @click="runSpeed">
          <Icon name="download" :size="14" />Run Speed Test
        </button>
        <button class="nb-act nb-act--warn" :disabled="!spRunning" @click="stopTool('sp')">
          <Icon name="x" :size="14" />Stop
        </button>
        <span v-if="spRunning" class="muted"><span class="nb-spin" /> Measuring…</span>
      </div>
      <div v-if="spResult">
        <div class="stg-row">
          <span class="stg-chip"><Icon name="download" :size="18" /></span>
          <span class="stg-meta"><span class="stg-title">Download</span><span class="stg-sub nb-mono">{{ fmtMbps(spResult.downloadMbps) }}</span></span>
        </div>
        <div class="stg-row">
          <span class="stg-chip"><Icon name="upload" :size="18" /></span>
          <span class="stg-meta"><span class="stg-title">Upload</span><span class="stg-sub nb-mono">{{ fmtMbps(spResult.uploadMbps) }}</span></span>
        </div>
        <div class="stg-row">
          <span class="stg-chip"><Icon name="bolt" :size="18" /></span>
          <span class="stg-meta"><span class="stg-title">Latency</span><span class="stg-sub nb-mono">{{ spResult.latencyMs ?? '—' }} ms<span v-if="spResult.cancelled" class="muted"> (cancelled)</span></span></span>
        </div>
        <div v-if="spServer" class="stg-row">
          <span class="stg-chip"><Icon name="servers" :size="18" /></span>
          <span class="stg-meta"><span class="stg-title">Server</span><span class="stg-sub">{{ spServer }}</span></span>
        </div>
        <div v-if="countryRows.length > 0" class="stg-row">
          <span class="stg-chip"><Icon name="globe" :size="18" /></span>
          <span class="stg-meta"><span class="stg-title">Country scan</span><span class="stg-sub">{{ countryRows.length }} server(s) seen</span></span>
        </div>
      </div>
      <div v-if="spError" class="nb-json-error">{{ spError }}</div>
      <p v-if="!spResult && !spRunning && !spError" class="muted">{{ netHint }}</p>
    </div>
    <!-- C) URL Test -->
    <div id="adv-tool-url" class="stg-card">
      <div class="stg-head">
        <span class="stg-head-chip"><Icon name="search" :size="22" /></span>
        <span class="stg-head-text">
          <span class="stg-head-title">URL Test</span>
          <span class="stg-head-sub">Probe a custom URL through the active core</span>
        </span>
      </div>
      <div class="toolbar">
        <input
          v-model="urlInput"
          class="nb-mono nb-field"
          style="flex: 1; min-width: 180px"
          placeholder="https://www.gstatic.com/generate_204"
          aria-label="URL to probe"
          @input="urlTouched = true"
        />
        <button class="nb-act nb-act--primary" :disabled="!canRun" @click="runURL">
          <Icon name="search" :size="14" />Run URL Test
        </button>
        <button class="nb-act nb-act--warn" :disabled="!urlRunning" @click="stopTool('url')">
          <Icon name="x" :size="14" />Stop
        </button>
        <span v-if="urlRunning" class="muted"><span class="nb-spin" /> Probing…</span>
      </div>
      <div v-if="urlResult">
        <div class="stg-row">
          <span class="stg-chip"><Icon name="search" :size="18" /></span>
          <span class="stg-meta"><span class="stg-title">{{ urlResult.ok ? 'OK' : 'Failed' }} · {{ urlResult.latencyMs }} ms</span><span class="stg-sub nb-mono">{{ urlResult.url }}</span></span>
        </div>
      </div>
      <div v-if="urlError" class="nb-json-error">{{ urlError }}</div>
      <p v-if="!urlResult && !urlRunning && !urlError" class="muted">{{ netHint }}</p>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import Icon from '../Icon.vue'
import { backend } from '../../api'
import { useSettingsStore } from '../../stores/settings'
import { useConnectionStore } from '../../stores/connection'
import { useProfilesStore } from '../../stores/profiles'
import { flagEmoji } from '../../data/servers'
import type { IPTestResult, SpeedTestResult, URLTestResult } from '../../types'

const settings = useSettingsStore()
const connection = useConnectionStore()
const profiles = useProfilesStore()

// --- Phase C tool cards: one test at a time across all three. ---
const ipRunning = ref(false)
const ipResult = ref<IPTestResult | null>(null)
const ipError = ref('')
const spRunning = ref(false)
const spResult = ref<SpeedTestResult | null>(null)
const spError = ref('')
const urlRunning = ref(false)
const urlResult = ref<URLTestResult | null>(null)
const urlError = ref('')
const urlInput = ref('')
const urlTouched = ref(false)
const countryRows = ref<SpeedTestResult[]>([])

const anyRunning = computed(() => ipRunning.value || spRunning.value || urlRunning.value)
const canRun = computed(() => connection.status.running && !anyRunning.value)
const netHint = computed(() =>
  connection.status.running ? 'No test run yet.' : 'Connect first to run network tools.',
)

const ipCountry = computed(() => {
  if (!ipResult.value) return ''
  const flag = ipResult.value.cc ? flagEmoji(ipResult.value.cc) + ' ' : ''
  return flag + (ipResult.value.country || ipResult.value.cc || '')
})

const spServer = computed(() => {
  if (!spResult.value) return ''
  const parts = [spResult.value.serverName, spResult.value.serverCountry].filter(Boolean)
  return parts.join(' · ')
})

function fmtMbps(v?: number): string {
  if (v === undefined || v === null) return '—'
  return `${v >= 100 ? v.toFixed(0) : v.toFixed(1)} Mbps`
}

// A speed run the CORE owns — one that was already in flight when this card
// mounted, or one a Stop left running server-side — has no local promise to
// await, so nothing would ever clear "Measuring…". Poll the server flag until
// it drops, then adopt the finished result.
let runPoll: ReturnType<typeof setInterval> | null = null

function stopRunPoll() {
  if (runPoll) clearInterval(runPoll)
  runPoll = null
}

function watchServerRun() {
  if (runPoll) return
  runPoll = setInterval(async () => {
    if (!spRunning.value) {
      stopRunPoll()
      return
    }
    try {
      const last = await backend.lastNetworkResults()
      if (last?.speedRunning === true) return
      spRunning.value = false
      if (last?.speed) spResult.value = last.speed
      if (last?.country?.length) countryRows.value = last.country
      stopRunPoll()
    } catch {
      /* transient read failure: the next tick retries */
    }
  }, 1000)
}

// BUG-006: seed from the ACTIVE profile's testUrl first (the backend
// resolves the same chain), then global, so an empty input probes the
// profile target instead of silently bypassing the override.
function ensureURLDefault() {
  if (urlTouched.value || urlInput.value) return
  const active = profiles.profiles.find((p) => p.id === settings.settings.activeProfileId)
  urlInput.value = active?.testUrl || settings.settings.testUrl || ''
}

async function runIP() {
  ipRunning.value = true
  ipError.value = ''
  try {
    ipResult.value = await backend.startIPTest()
  } catch (e) {
    ipError.value = String(e)
  } finally {
    ipRunning.value = false
  }
}

async function runSpeed() {
  spRunning.value = true
  spError.value = ''
  try {
    spResult.value = await backend.startSpeedTest()
    await refreshCountry()
  } catch (e) {
    spError.value = String(e)
  } finally {
    spRunning.value = false
  }
}

async function runURL() {
  urlRunning.value = true
  urlError.value = ''
  try {
    urlResult.value = await backend.startURLTest(urlInput.value.trim(), 10000)
  } catch (e) {
    urlError.value = String(e)
  } finally {
    urlRunning.value = false
  }
}

async function stopTool(which: 'ip' | 'sp' | 'url') {
  try {
    await backend.stopTest()
  } catch (e) {
    const msg = String(e)
    if (which === 'ip') ipError.value = msg
    else if (which === 'sp') spError.value = msg
    else urlError.value = msg
    return
  }
  // Stop landed: resync the spinner from server truth instead of leaving
  // whatever the local flag said (BUG-004). Local-only legs simply clear.
  if (which === 'sp') {
    try {
      const last = await backend.lastNetworkResults()
      spRunning.value = last?.speedRunning === true
    } catch {
      spRunning.value = false
    }
    // The cancel may still be draining server-side: keep watching so the
    // spinner ends when the run really does.
    if (spRunning.value) watchServerRun()
  } else if (which === 'ip') {
    ipRunning.value = false
  } else {
    urlRunning.value = false
  }
}

/** Seed cards with last-known results (reporter reads / xray+mihomo cache).
 *  Never overwrites a displayed result: reporter reads drain, so a
 *  second load must not wipe the cards. Best-effort; offline or
 *  empty reporters simply leave the hints in place. */
async function loadLast() {
  if (!settings.settings.advancedMode || !connection.status.running) return
  try {
    const last = await backend.lastNetworkResults()
    if (!last) return
    if (!ipResult.value && last?.ip) ipResult.value = last.ip
    if (!spResult.value && last?.speed) spResult.value = last.speed
    if (!urlResult.value && last?.url) urlResult.value = last.url
    if (last?.country?.length) countryRows.value = last.country
    // BUG-004: drive the spinner from server truth so re-entering the
    // tab mid-run shows Measuring instead of a dead idle card.
    if (last?.speedRunning === true) {
      spRunning.value = true
      watchServerRun()
    }
  } catch {
    /* best-effort only */
  }
}

async function refreshCountry() {
  try {
    const last = await backend.lastNetworkResults()
    if (last?.country?.length) countryRows.value = last.country
  } catch {
    /* best-effort only */
  }
}

onMounted(() => {
  ensureURLDefault()
  void loadLast()
})
onUnmounted(stopRunPoll)
</script>
