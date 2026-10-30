<!-- Aether tab: dedicated editor and endpoint scan, mirroring PattNG's
     ServerAetherActivity. The scan runs aether.exe itself (the ranges
     live inside the core); scan state, results, and logs live in the
     shared aether store so switching tabs never loses them. -->
<template>
  <div class="nb-card nb-page">
    <h3>Aether</h3>
    <p class="muted">
      Runs the Aether engine next to sing-box (like PattNG): the profile dials its internal SOCKS
      listener. Scan asks aether.exe for a WARP endpoint; the result stays editable and is saved
      with the profile. A missing WARP key is registered on the first scan or connection.
    </p>
    <div class="form-grid">
      <label>Profile</label>
      <PickSelect
        v-model="selectedId"
        icon="cloud"
        title="Aether profile"
        aria-label="Aether profile"
        neutral=""
        :options="profileOptions"
        @update:model-value="onSelect"
      />
      <label>Name</label><input v-model="draft.name" placeholder="Aether" />
      <AetherFields :model="draft" />
      <template v-if="!twoHops">
        <label>Address</label><input v-model="draft.address" placeholder="host (scan or type it)" />
        <label>Port</label><input v-model.number="draft.port" type="number" min="1" max="65535" />
      </template>
      <template v-if="warpUsed">
        <label>Scan</label>
        <div class="toolbar" style="padding: 0">
          <button class="nb-act nb-act--primary" :disabled="scanDisabled" @click="scan">
            <Icon :name="scanning ? 'refresh' : 'search'" :size="14" />
            {{ scanning ? 'Scanning…' : 'Scan for endpoint' }}
          </button>
          <button v-if="scanning" class="nb-act nb-act--warn" @click="cancelScan">
            <Icon name="x" :size="14" />Cancel
          </button>
          <span v-if="scanning" class="muted">{{ scanStatusText }}</span>
        </div>
      </template>
      <p v-else class="muted">
        Scan runs a WARP tunnel; it is off while Psiphon or Tor is set to "only".
      </p>
      <p v-if="scanning" class="muted">The scan can take a few minutes; live logs are below.</p>
      <p v-if="aetherRunning && !scanning" class="muted">Disconnect the running Aether tunnel before scanning.</p>
    </div>
    <div class="toolbar" style="padding: 12px 0 0">
      <button class="nb-act nb-act--primary" :disabled="saving || scanning" @click="save(false)">
        <Icon name="check" :size="14" />{{ saving ? 'Saving…' : 'Save' }}
      </button>
      <button
        class="nb-act nb-act--accent"
        :disabled="saving || scanning || connection.busy || connection.connectBusy"
        @click="save(true)"
      >
        <Icon name="bolt" :size="14" />Save &amp; connect
      </button>
      <span v-if="err || aether.error" class="latency-bad">{{ err || aether.error }}</span>
    </div>

    <div class="nb-card" style="margin-top: 14px">
      <div class="toolbar" style="padding: 0">
        <strong>Psiphon chain</strong>
        <label style="display: flex; align-items: center; gap: 8px; cursor: pointer">
          <input v-model="aetherChainOn" type="checkbox" :disabled="saving || scanning" @change="saveAetherChain" />
          <span>Run Psiphon after the Aether tunnel</span>
        </label>
      </div>
      <p class="muted">
        Aether → Psiphon, not the SNI spoofing → Psiphon chain from that tab: this one uses the Aether
        core's own chain mode, so <span class="mono">Aether tunnel → Psiphon → internet</span> — Psiphon's
        servers are dialled through the tunnel and NekoGo keeps dialling Psiphon's local listener.
        Connect waits until the chain carries real traffic before the transport starts.
        A profile that sets its own Psiphon mode above keeps it. Applies on the next Connect.
      </p>
    </div>

    <div class="nb-card" style="margin-top: 14px">
      <div class="toolbar" style="padding: 0">
        <strong>Scan results</strong>
        <span v-if="scanning" class="muted">{{ scanStatusText }}</span>
        <span v-else-if="aether.scanStatus.lastError" class="latency-bad">{{ aether.scanStatus.lastError }}</span>
        <button
          class="nb-act nb-act--ghost"
          :disabled="scanning || aether.scanResults.length === 0"
          @click="clearResults"
        >
          <Icon name="trash" :size="14" />Clear
        </button>
      </div>
      <div v-if="aether.scanResults.length === 0" class="muted">No endpoints scanned yet.</div>
      <div v-else>
        <div
          v-for="(r, i) in aether.scanResults"
          :key="`${i}-${r.endpoint || r.outer || r.host || ''}`"
          class="toolbar"
          style="padding: 6px 0; border-top: 1px solid var(--border)"
        >
          <span class="mono">{{ resultLabel(r) }}</span>
          <span class="muted">{{ resultMeta(r) }}</span>
          <button class="nb-act nb-act--sm" :disabled="saving || scanning" @click="saveResult(i)">
            <Icon name="plus" :size="13" />Add as config
          </button>
        </div>
      </div>
    </div>

    <div class="nb-card" style="margin-top: 14px">
      <div class="toolbar" style="padding: 0">
        <strong>Scan log</strong>
        <span class="muted">{{ aether.scanLogs.length }} lines</span>
        <button class="nb-act nb-act--ghost" :disabled="aether.scanLogs.length === 0" @click="aether.clearLogs()">
          <Icon name="trash" :size="14" />Clear log
        </button>
      </div>
      <div v-if="aether.scanLogs.length === 0" class="muted">No Aether scan logs yet.</div>
      <div v-else ref="logBox" class="sni-log">
        <div v-for="l in aether.scanLogs" :key="l.id" class="sni-log-line">
          <span class="sni-log-time">{{ fmtTime(l.time) }}</span>
          <span class="sni-chip" :class="chipClass(l.level)">{{ l.level }}</span>
          <span class="sni-log-msg">{{ l.message }}</span>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { backendAvailable } from '../api'
import { useAetherStore } from '../stores/aether'
import { useProfilesStore } from '../stores/profiles'
import { useSettingsStore } from '../stores/settings'
import { useConnectionStore } from '../stores/connection'
import AetherFields from './AetherFields.vue'
import Icon from './Icon.vue'
import PickSelect from './PickSelect.vue'
import type { AetherScanResult, AetherSettings, Profile } from '../types'

const profiles = useProfilesStore()
const settings = useSettingsStore()
const connection = useConnectionStore()
const aether = useAetherStore()

function blankAether(): AetherSettings {
  return {
    protocol: 'wg', transport: 'h3', scanMode: 'balanced', obfuscation: 'auto', ipVersion: 'v4',
    masqueSni: '', fingerprint: '', dns: '', exitLoc: '', wiwOuter: '', wiwInner: '',
    fragment: false, fragmentSize: '', fragmentDelay: '', ech: false, echDns: '', echDomain: '',
    command: '', psiphon: 'off', psiphonMode: 'auto', psiphonCdnIps: '', psiphonCdnSni: '',
    psiphonCdnSets: '', psiphonRegion: '', psiphonBundledList: undefined,
    tor: 'off', torBridges: 'auto', torBridgeLines: '', torRelays: 'auto',
  }
}

function blankProfile(): Profile {
  return {
    id: '', name: '', core: 0, protocol: 'aether', address: '', port: 443,
    aether: blankAether(),
  }
}

const draft = reactive<Profile>(blankProfile())
const selectedId = ref('')
const saving = ref(false)
const err = ref('')
const logBox = ref<HTMLElement | null>(null)

const scanning = computed(() => aether.scanStatus.running)
const aetherProfiles = computed(() => profiles.profiles.filter((p) => p.protocol === 'aether'))
const profileOptions = computed(() => [
  { value: '', label: 'New Aether profile' },
  ...aetherProfiles.value.map((p) => ({ value: p.id, label: p.name || p.address || 'Aether' })),
])
const twoHops = computed(() => ['gool', 'mim', 'wg-over-masque'].includes(draft.aether?.protocol || 'wg'))
const warpUsed = computed(() => draft.aether?.psiphon !== 'only' && draft.aether?.tor !== 'only')
// The RUNNING profile decides whether an Aether tunnel is up: the rail
// lets the selection move to a non-Aether profile while the tunnel keeps
// running, which used to re-enable Scan behind a live engine (the backend
// then refuses, but the warning vanished too).
const runningProfile = computed(() => profiles.profiles.find((p) => p.id === connection.status.profileId))
const aetherRunning = computed(() => connection.status.running && runningProfile.value?.protocol === 'aether')
const scanDisabled = computed(() =>
  scanning.value || saving.value || !backendAvailable.value || aetherRunning.value ||
  connection.busy || connection.connectBusy,
)
const scanStatusText = computed(() => {
  const s = aether.scanStatus
  return s.total > 0 ? `Scanning ${s.done}/${s.total}…` : 'Scanning…'
})

// Aether → Psiphon: a persisted Settings flag the backend turns into the
// engine's own chain mode on every Aether connect. Separate from the SNI
// spoof chain (SNI tab): this one rides the Aether tunnel.
const aetherChainOn = computed({
  get: () => !!settings.settings.aetherPsiphon,
  set: (v: boolean) => { settings.settings.aetherPsiphon = v },
})

async function saveAetherChain() {
  err.value = ''
  try {
    await settings.save()
  } catch (e) {
    err.value = String(e)
  }
}

function clone(p: Profile): Profile {
  return JSON.parse(JSON.stringify(p)) as Profile
}

function useProfile(p: Profile) {
  const copy = clone(p)
  copy.aether = { ...blankAether(), ...(copy.aether ?? {}) }
  Object.assign(draft, blankProfile(), copy)
  selectedId.value = p.id || ''
  err.value = ''
}

function onSelect() {
  const p = profiles.profiles.find((x) => x.id === selectedId.value)
  if (p) useProfile(p)
  else useProfile(blankProfile())
}

onMounted(() => {
  if (profiles.profiles.length === 0) void profiles.load()
  aether.acquire()
  connection.acquire()
})

onUnmounted(() => {
  aether.release()
  connection.release()
})

watch(
  () => aether.scanLogs.length,
  async () => {
    await nextTick()
    if (logBox.value) logBox.value.scrollTop = logBox.value.scrollHeight
  },
)

function endpointText(host?: string, port?: number): string {
  if (!host) return ''
  const h = host.includes(':') ? `[${host}]` : host
  return port ? `${h}:${port}` : h
}

function resultLabel(r: AetherScanResult): string {
  if (r.outer) return r.inner ? `${r.outer} → ${r.inner}` : r.outer
  return r.endpoint || endpointText(r.host, r.port)
}

function resultMeta(r: AetherScanResult): string {
  return [r.protocol, r.transport, r.latencyMs ? `${r.latencyMs} ms` : '', r.lastScannedAt]
    .filter(Boolean)
    .join(' · ')
}

function fmtTime(t: string): string {
  const d = new Date(t)
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleTimeString(undefined, { hour12: false })
}

function chipClass(level: string): string {
  const l = String(level ?? '').toLowerCase()
  if (l === 'warn' || l === 'warning') return 'warn'
  if (l === 'error') return 'error'
  if (l === 'debug') return 'debug'
  return 'info'
}

async function scan() {
  if (scanDisabled.value) return
  err.value = ''
  await aether.startScan(1, 480000, clone(draft))
}

async function cancelScan() {
  await aether.cancelScan()
}

async function clearResults() {
  await aether.clearResults()
}

async function saveResult(index: number) {
  if (saving.value || scanning.value) return
  err.value = ''
  saving.value = true
  try {
    const saved = await aether.saveAsConfig(index, draft.name.trim())
    await profiles.load()
    useProfile(saved)
  } catch (e) {
    err.value = String(e)
  } finally {
    saving.value = false
  }
}

async function save(connect: boolean) {
  if (saving.value || scanning.value) return
  err.value = ''
  if (!twoHops.value) {
    if (!draft.address.trim()) {
      err.value = 'Address is required (scan or type it).'
      return
    }
    const port = Number(draft.port)
    if (!Number.isInteger(port) || port < 1 || port > 65535) {
      err.value = `Port must be 1..65535 (got ${draft.port})`
      return
    }
  }
  saving.value = true
  try {
    const saved = await profiles.save(clone(draft))
    useProfile(saved)
    if (connect) {
      try {
        await settings.setActiveProfileId(saved.id)
      } catch {
        /* selection is best effort; connect still takes the id */
      }
      await connection.connect(saved.id)
      if (connection.error) err.value = connection.error
    }
  } catch (e) {
    err.value = String(e)
  } finally {
    saving.value = false
  }
}
</script>
