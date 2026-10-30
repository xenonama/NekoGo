<!-- SNI Spoofing tab: status + engine options + Cloudflare IP list +
     decoy-SNI list + live stats + engine logs for the sni-spoof core.
     All state comes from the backend (SpoofGetStatus/Stats/Config):
     Stop never flips an optimistic flag — every click round-trips and the
     card re-renders from the next poll. Starting happens through Connect
     (Home / Proxies) for the selected profile; the Psiphon chain button
     below is the only start path this tab owns. -->
<template>
  <div class="nb-card nb-page">
    <h3>SNI Spoofing</h3>
    <p class="muted">Routes trojan/vless through the built-in sni-spoof core (WinDivert fake-ClientHello injection). Requires admin + WinDivert (Windows only).</p>
    <div v-if="msg" class="muted">{{ msg }}</div>
    <div v-if="err" class="latency-bad">{{ err }}</div>
  </div>

  <!-- 1. Status -->
  <div class="nb-card nb-page">
    <h3>Status</h3>
    <div v-if="spoofable.length === 0 && convertible.length === 0" class="muted">
      No eligible profile. Import a trojan/vless link over TLS+WS in Proxies (direct domain or chained 127.0.0.1 address) — it will be offered here for the SNI spoof core.
    </div>
    <!-- Status row: stg-meta holds the state pill (+ subs), stg-ctl holds
         the Stop control. Starting happens through Connect (Home / Proxies)
         for the selected profile — there is no separate engine-only start
         and no profile picker here. The shared .stg-row grid is a 40px
         chip slot — wrong for this card, so it gets its own template. -->
    <div class="stg-row stg-row--status">
      <span class="stg-meta">
        <span class="stg-title">
          <span class="nb-pill" :class="status.running ? 'is-on' : ''">{{ status.running ? 'Running' : 'Stopped' }}</span>
        </span>
        <span v-if="status.running" class="stg-sub">Listening on 127.0.0.1:{{ status.listenPort }} · uptime {{ uptimeText }}</span>
        <span v-if="status.lastError" class="stg-sub latency-bad">{{ status.lastError }}</span>
        <span v-if="!status.running" class="stg-sub">Connect a spoof profile from Home or Proxies to run it.</span>
      </span>
      <span v-if="status.running" class="stg-ctl">
        <button class="nb-act nb-act--warn" :disabled="busy" @click="stop">
          <Icon name="minus" :size="14" />Stop
        </button>
      </span>
    </div>
    <div class="sni-status-grid">
      <div class="sni-status-k">Pinned IP</div>
      <div class="sni-status-v">
        <span class="mono">{{ selectedIp || '—' }}</span>
        <span class="sni-status-sub" :class="pinIp.cls">{{ pinIp.text }}</span>
      </div>
      <div class="sni-status-k">Pinned SNI</div>
      <div class="sni-status-v">
        <span class="mono">{{ selectedSni || '—' }}</span>
        <span class="sni-status-sub" :class="pinSni.cls">{{ pinSni.text }}</span>
      </div>
      <div class="sni-status-k">Elevation</div>
      <div class="sni-status-v">
        <span :class="privileged === false ? 'lat-warn' : privileged === true ? 'lat-ok' : ''">{{ elevationText }}</span>
        <button
          v-if="privileged === false"
          class="nb-act nb-act--sm nb-act--warn sni-env-btn"
          :disabled="relaunching"
          title="Restart NekoGo elevated — the engine needs admin rights (WinDivert)"
          @click="relaunchElevated"
        >
          <Icon name="shield" :size="12" />{{ relaunching ? 'Relaunching…' : 'Relaunch as admin' }}
        </button>
      </div>
    </div>
  </div>

  <!-- 2. Psiphon chain -->
  <div class="nb-card nb-page">
    <h3>Psiphon chain</h3>
    <label class="sni-chain-row">
      <input v-model="chainOn" type="checkbox" :disabled="busy" @change="saveChain" />
      <span>Run Psiphon after SNI spoofing</span>
    </label>
    <p class="muted">
      On: connecting a spoof profile builds <span class="mono">TUN → Psiphon → SNI-spoofed tunnel → your VPN server</span>.
      Psiphon exits from its own network while every connection it makes rides the spoofed tunnel.
      The chain always runs in TUN mode, so NekoGo leaves the OS proxy off for these connects;
      a plain spoof connection keeps using the OS proxy (Settings → Connection → System proxy on connect).
      “Xray for SNI spoofing” in Settings applies to plain spoof connects only — the chain always runs on
      sing-box, whose TUN inbound is what feeds it. This is not the Aether tab's Psiphon chain, which rides
      the Aether tunnel instead.
      This applies to <strong>Connect</strong> (Home / Proxies) — the SNI tab no longer starts the engine on its own.
    </p>
    <div v-if="status.chain" class="muted">
      Chain active — Psiphon listening on <span class="mono">127.0.0.1:{{ status.chainPort }}</span>.
    </div>
    <div v-if="chainOn && !status.running" class="toolbar" style="padding: 12px 0 0">
      <button class="nb-act nb-act--primary" :disabled="busy || !chainReady" @click="startChain">
        <Icon name="bolt" :size="14" />Connect {{ chainProfile ? `"${chainLabel}" ` : '' }}with Psiphon
      </button>
      <span v-if="chainMsg" class="muted">{{ chainMsg }}</span>
      <span v-else-if="!chainProfile" class="muted">Select a profile on Home or in Proxies first.</span>
      <span v-else-if="!chainReady" class="muted">The active profile is not a trojan/vless + TLS + WS profile, so it cannot run through the SNI spoof core.</span>
    </div>
  </div>

  <!-- 3. Engine options -->
  <div class="nb-card nb-page">
    <h3>Engine options</h3>
    <p class="muted">Empty = engine default. Method migrates to "auto" on save.</p>
    <div class="form-grid">
      <label>Method</label>
      <PickSelect v-model="draft.method" icon="wrench" title="Bypass method" aria-label="Bypass method" quiet neutral="" :options="methodOptions" />
      <label>Fingerprint</label>
      <PickSelect v-model="draft.fingerprint" icon="shield" title="TLS fingerprint" aria-label="TLS fingerprint" quiet neutral="" :options="fpOptions" />
      <label>QUIC mode</label>
      <PickSelect v-model="draft.quicMode" icon="globe" title="QUIC mode" aria-label="QUIC mode" quiet neutral="" :options="quicOptions" />
      <label>IP mode</label>
      <PickSelect v-model="draft.ipMode" icon="layers" title="IP mode" aria-label="IP mode" quiet neutral="" :options="ipModeOptions" />
      <label>Handshake timeout (0.5–10.0)</label><input v-model="draft.handshakeTimeout" type="number" min="0" max="10" step="0.1" placeholder="engine default" />
      <label>Max connections (10–2000)</label><input v-model="draft.maxConnections" type="number" min="0" max="2000" placeholder="engine default" />
      <label>Fake delay (0.0–5.0)</label><input v-model="draft.fakeDelay" type="number" min="0" max="5" step="0.01" placeholder="engine default" />
      <label>Seq overlap (0–16)</label><input v-model="draft.seqOverlap" type="number" min="0" max="16" placeholder="engine default" />
      <label>Padding size (0–128)</label><input v-model="draft.paddingSize" type="number" min="0" max="128" placeholder="engine default" />
    </div>
    <div class="toolbar" style="padding: 12px 0 0">
      <button class="nb-act nb-act--primary" :disabled="busy" @click="saveConfig">
        <Icon name="check" :size="14" />Save
      </button>
      <span v-if="cfgMsg" class="muted">{{ cfgMsg }}</span>
    </div>
  </div>

  <!-- 4. Cloudflare IPs -->
  <SniListCard
    title="Cloudflare IPs"
    :hint="ipHint"
    :rows="ipRows"
    :primary-key="selectedIp"
    check-title="Check to hand this IP to the engine (star = primary)"
    :scan-title="ipScanTitle"
    :can-scan="!!selectedSni"
    :scanning="scanning"
    :scanning-key="scanningKey"
    placeholder="1.2.3.4 or a batch"
    search-placeholder="Filter IPs"
    add-label="+ Add IP(s)"
    empty-text="No IPs yet — add a Cloudflare IP above."
    remove-failed-title="Remove scanned-failing IPs (the primary entry is kept)"
    :busy="busy"
    @toggle="toggleIp"
    @set-checked="setIpsChecked"
    @make-primary="makeIpPrimary"
    @scan="scanIp"
    @remove="removeIp"
    @remove-failed="removeFailedIps"
    @add-bulk="addIps"
  >
    <template #toolbar>
      <button class="nb-act nb-act--accent" :disabled="busy || scanning || !selectedSni || checkedIpCount === 0" :title="scanCheckedIpsTitle" @click="scanCheckedIps">
        <Icon name="search" :size="14" />Scan checked ({{ checkedIpCount }})
      </button>
      <button v-if="scanning" class="nb-act nb-act--warn" title="Abort the running probe" @click="cancelScan">
        <Icon name="x" :size="14" />Cancel scan
      </button>
      <span v-if="scanning && scanKind === 'ip'" class="muted">probe {{ scanDone }}/{{ scanTotal }}</span>
      <button class="nb-act nb-act--ghost" :disabled="busy || scanning" title="Restore the curated Cloudflare IPs + decoy SNIs (keeps scan results for kept entries)" @click="resetLists">
        <Icon name="refresh" :size="14" />Reset to defaults
      </button>
    </template>
  </SniListCard>

  <!-- 5. Decoy SNIs -->
  <SniListCard
    title="Decoy SNIs"
    :hint="sniHint"
    :rows="sniRows"
    :primary-key="selectedSni"
    check-title="Check to hand this SNI to the engine (star = primary)"
    :scan-title="sniScanTitle"
    :can-scan="!!selectedIp"
    :scanning="scanning"
    :scanning-key="scanningKey"
    placeholder="example.com or a batch"
    search-placeholder="Filter SNIs"
    add-label="+ Add SNI(s)"
    empty-text="No SNIs yet — add a decoy host above."
    remove-failed-title="Remove scanned-failing SNIs (the primary entry is kept)"
    :busy="busy"
    @toggle="toggleSni"
    @set-checked="setSnisChecked"
    @make-primary="makeSniPrimary"
    @scan="scanSni"
    @remove="removeSni"
    @remove-failed="removeFailedSnis"
    @add-bulk="addSnis"
  >
    <template #toolbar>
      <button class="nb-act nb-act--accent" :disabled="busy || scanning || !selectedIp || checkedSniCount === 0" :title="scanCheckedSnisTitle" @click="scanCheckedSnis">
        <Icon name="search" :size="14" />Scan checked ({{ checkedSniCount }})
      </button>
      <button v-if="scanning" class="nb-act nb-act--warn" title="Abort the running probe" @click="cancelScan">
        <Icon name="x" :size="14" />Cancel scan
      </button>
      <span v-if="scanning && scanKind === 'sni'" class="muted">probe {{ scanDone }}/{{ scanTotal }}</span>
    </template>
  </SniListCard>

  <!-- 6. Stats -->
  <div class="nb-card nb-page">
    <h3>Stats</h3>
    <div v-if="!stats" class="muted">No stats yet — start the engine to collect them.</div>
    <template v-else>
      <div class="sni-stat-grid">
        <div class="sni-stat"><div class="k">Active</div><div class="v">{{ stats.active }}</div></div>
        <div class="sni-stat"><div class="k">Connections</div><div class="v">{{ stats.total }}</div></div>
        <div class="sni-stat"><div class="k">Success rate</div><div class="v" :class="rateClass">{{ rateText }}</div></div>
        <div class="sni-stat"><div class="k">Failed</div><div class="v" :class="stats.failed > 0 ? 'lat-bad' : ''">{{ stats.failed }}</div></div>
        <div class="sni-stat"><div class="k">Up</div><div class="v">{{ fmtBytes(stats.up_bytes) }}</div></div>
        <div class="sni-stat"><div class="k">Down</div><div class="v">{{ fmtBytes(stats.down_bytes) }}</div></div>
        <div class="sni-stat"><div class="k">Best endpoint</div><div class="v mono">{{ stats.best_endpoint || '—' }}</div></div>
        <div class="sni-stat"><div class="k">Best method</div><div class="v mono">{{ stats.best_method || '—' }}</div></div>
      </div>
      <div v-if="(stats.methods ?? []).length > 0" class="sni-methods">
        <div class="sni-methods-title">Bypass methods</div>
        <div v-for="m in (stats.methods ?? []).slice(0, 5)" :key="m.key" class="sni-method">
          <span class="sni-method-name mono">{{ m.key }}</span>
          <span class="nb-quota"><span class="nb-quota-fill" :style="{ width: barWidth(m.rate) }" /></span>
          <span class="sni-method-rate">{{ (m.rate * 100).toFixed(1) }}%</span>
          <span class="sni-method-counts">{{ m.ok }} ok · {{ m.fail }} fail</span>
        </div>
      </div>
    </template>
  </div>

  <!-- 7. Logs -->
  <div class="nb-card nb-page">
    <h3>Logs</h3>
    <div class="toolbar">
      <PickSelect
        v-model="logFilter"
        icon="terminal"
        title="Log level filter"
        aria-label="Log level filter"
        neutral=""
        :options="logLevelOptions"
      />
      <span class="spacer" />
      <button class="nb-act nb-act--ghost" @click="clearLogs">
        <Icon name="trash" />
        Clear
      </button>
    </div>
    <div v-if="filteredLogs.length === 0" class="muted">No engine logs yet.</div>
    <div v-else class="sni-log">
      <div v-for="(l, i) in filteredLogs.slice(-200)" :key="i" class="sni-log-line">
        <span class="sni-log-time">{{ fmtTime(l.time) }}</span>
        <span class="sni-chip" :class="chipClass(l.level)">{{ l.level }}</span>
        <span class="sni-log-msg">{{ l.message }}</span>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import { backend } from '../api'
import { useConnectionStore } from '../stores/connection'
import { useProfilesStore } from '../stores/profiles'
import { useSettingsStore } from '../stores/settings'
import SniListCard from './SniListCard.vue'
import Icon from './Icon.vue'
import PickSelect from './PickSelect.vue'
import type { LogEntry, Profile, SNIIP, SNISNI, SniListRow, SpoofStats, SpoofStatus } from '../types'

const profiles = useProfilesStore()
const settings = useSettingsStore()
const connection = useConnectionStore()
const msg = ref('')
const err = ref('')
const busy = ref(false)
const status = ref<SpoofStatus>({ running: false, listenPort: 0, startedAt: '', uptime: 0, lastError: '', chain: false, chainPort: 0 })
const stats = ref<SpoofStats | null>(null)
const logs = ref<LogEntry[]>([])
let logCursor = 0 // newest spoof/psiphon log ID seen (0 = from the start)
const logFilter = ref('')
const logLevelOptions = [
  { value: '', label: 'All levels' },
  { value: 'info', label: 'INFO' },
  { value: 'warn', label: 'WARN' },
  { value: 'error', label: 'ERROR' },
]

const methods = ['auto', 'wrong_seq', 'wrong_seq_ttl', 'split_seq', 'fragmented', 'padding', 'delayed_retry', 'double_sni', 'hostfakesplit', 'fakedsplit']
const fps = ['legacy', 'chrome_120', 'chrome_124', 'firefox_122', 'firefox_124']
const quics = ['block', 'spoof', 'passthrough']
const ipmodes = ['ipv4', 'ipv6', 'both']

function withDefault(values: string[]) {
  return [{ value: '', label: 'engine default' }, ...values.map((v) => ({ value: v, label: v }))]
}
const methodOptions = withDefault(methods)
const fpOptions = withDefault(fps)
const quicOptions = withDefault(quics)
const ipModeOptions = withDefault(ipmodes)

const draft = reactive({
  method: 'auto', fingerprint: '', quicMode: '', ipMode: '',
  handshakeTimeout: '', maxConnections: '', fakeDelay: '', seqOverlap: '', paddingSize: '',
})
const cfgMsg = ref('')

const ipList = computed(() => settings.settings.sniIps ?? [])
const sniList = computed(() => settings.settings.sniSnis ?? [])

// SNI hosts are compared/spread normalized (same rule as addSnis).
function normSni(v: string): string {
  return String(v ?? '').trim().toLowerCase().replace(/^\.+|\.+$/g, '')
}

// Checked = handed to the engine (absent/null flag = legacy = checked).
function isChecked(e: { selected?: boolean | null }): boolean {
  return e.selected !== false
}

// Mirror of the backend pin policy (spoofPinIP/spoofPinSNI): the
// stored pin wins while its entry stays checked (or while it is not
// in the list at all — loose legacy pins); an unchecked pin yields to
// the first checked entry. Empty = nothing checked (the engine falls
// back to the profile's own target).
const selectedIp = computed(() => {
  const pin = String(settings.settings.sniSelectedIp ?? '').trim()
  if (pin) {
    const hit = ipList.value.find((e) => String(e.ip ?? '').trim() === pin)
    if (!hit || isChecked(hit)) return pin
  }
  const first = ipList.value.find(isChecked)
  return first ? String(first.ip ?? '').trim() : ''
})
const selectedSni = computed(() => {
  const pin = normSni(settings.settings.sniSelectedSni ?? '')
  if (pin) {
    const hit = sniList.value.find((e) => normSni(e.host) === pin)
    if (!hit || isChecked(hit)) return pin
  }
  const first = sniList.value.find(isChecked)
  return first ? normSni(first.host) : ''
})

// Store entries -> list-card rows (scan line + health stamps for the
// card's sort/filter; `index` stays the store position for removal).
const ipRows = computed<SniListRow[]>(() =>
  ipList.value.map((e, index) => ({
    key: e.ip, index, label: e.ip,
    sub: scanText(e.lastOk, e.lastMs, e.lastAt, e.lastTls),
    cls: scanClass(e.lastOk, e.lastAt),
    ok: !!e.lastOk, ms: e.lastMs ?? 0, at: e.lastAt ?? '',
    checked: isChecked(e),
    primary: isChecked(e) && String(e.ip ?? '').trim() === selectedIp.value,
  })),
)
const sniRows = computed<SniListRow[]>(() =>
  sniList.value.map((e, index) => ({
    key: e.host, index, label: e.host,
    sub: scanText(e.lastOk, e.lastMs, e.lastAt, e.lastTls),
    cls: scanClass(e.lastOk, e.lastAt),
    ok: !!e.lastOk, ms: e.lastMs ?? 0, at: e.lastAt ?? '',
    checked: isChecked(e),
    primary: isChecked(e) && normSni(e.host) === selectedSni.value,
  })),
)

const spoofable = computed(() => profiles.profiles.filter((p) => p.core === 2))

// Mirror of the backend's spoofEligibilityIssue (spoof_manager.go):
// trojan/vless over TLS+WS, no REALITY, no xtls flow, with an
// address. Profiles matching it but still on another core (e.g. IP
// literals, which never auto-tag) are offered in the selector and
// converted by SpoofUseProfile on Start.
function isConvertible(p: Profile): boolean {
  return p.core !== 2
    && (p.protocol === 'trojan' || p.protocol === 'vless')
    && !!p.tls && !p.realityEnabled && !p.flow
    && p.network === 'ws'
    && String(p.address ?? '').trim() !== ''
}
const convertible = computed(() => profiles.profiles.filter(isConvertible))

// The chain button targets whatever profile Connect would use — the app has
// exactly one "current" profile and the SNI tab must not keep a second,
// hidden selection of its own.
const chainProfile = computed<Profile | null>(() => {
  const id = String(settings.settings.activeProfileId ?? '')
  return profiles.profiles.find((p) => p.id === id) ?? null
})
const chainLabel = computed(() => (chainProfile.value ? optionLabel(chainProfile.value) : ''))
/** Spoof core already, or convertible by the same eligibility rule the importer uses. */
const chainReady = computed(() => {
  const p = chainProfile.value
  return !!p && (p.core === 2 || isConvertible(p))
})

// Same-named profiles are common (an old import plus a re-import):
// label with the server name (or address) so the target is readable.
function optionLabel(p: Profile): string {
  return `${p.name} — ${p.serverName || p.address || p.protocol}`
}

const uptimeText = computed(() => {
  const u = Math.floor(status.value.uptime)
  if (u < 60) return `${u}s`
  if (u < 3600) return `${Math.floor(u / 60)}m ${u % 60}s`
  return `${Math.floor(u / 3600)}h ${Math.floor((u % 3600) / 60)}m`
})

// --- Status-card environment readouts ---

// Elevation: the engine needs admin rights (WinDivert). null = probe
// unavailable (dev browser); never fabricate a state.
const privileged = ref<boolean | null>(null)
const relaunching = ref(false)

async function loadPrivileged() {
  try {
    privileged.value = await backend.isPrivileged()
  } catch {
    privileged.value = null
  }
}

const elevationText = computed(() => {
  if (privileged.value === null) return 'Unknown'
  return privileged.value ? 'Windows administrator' : 'Standard user'
})

async function relaunchElevated() {
  if (relaunching.value) return
  if (!confirm('Restart NekoGo as administrator? The app will relaunch and resume the connection.')) return
  relaunching.value = true
  err.value = ''
  try {
    await backend.relaunchAsAdmin()
    // On success the backend shuts this app down; keep the button
    // disabled until the process is gone.
  } catch (e) {
    err.value = String(e)
    relaunching.value = false
    await loadPrivileged()
  }
}

// Pinned-entry scan health from the stored list stamps.
function pinHealth(entry?: { lastOk?: boolean; lastMs?: number; lastAt?: string }): { text: string; cls: string } {
  if (!entry || !entry.lastAt) return { text: 'never scanned', cls: '' }
  return entry.lastOk
    ? { text: `scan ok${entry.lastMs ? ` · ${entry.lastMs} ms` : ''}`, cls: 'lat-ok' }
    : { text: 'scan failed', cls: 'lat-bad' }
}
const pinnedIpEntry = computed(() => ipList.value.find((e) => e.ip === selectedIp.value))
const pinnedSniEntry = computed(() => sniList.value.find((e) => e.host === selectedSni.value))
const pinIp = computed(() => pinHealth(pinnedIpEntry.value))
const pinSni = computed(() => pinHealth(pinnedSniEntry.value))

const rateText = computed(() => {
  if (!stats.value) return '—'
  return `${(stats.value.success_rate * 100).toFixed(1)}%`
})
const rateClass = computed(() => {
  if (!stats.value) return ''
  const r = stats.value.success_rate
  if (r >= 0.9) return 'lat-ok'
  if (r >= 0.5) return 'lat-warn'
  return 'lat-bad'
})
const filteredLogs = computed(() =>
  logFilter.value ? logs.value.filter((l) => l.level === logFilter.value) : logs.value,
)

function barWidth(rate: number): string {
  const pct = Math.min(100, Math.max(0, (Number(rate) || 0) * 100))
  return `${pct.toFixed(1)}%`
}

function fmtBytes(n: number): string {
  const v = Number(n) || 0
  if (v < 1024) return `${v} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let x = v / 1024
  let i = 0
  while (x >= 1024 && i < units.length - 1) { x /= 1024; i++ }
  return `${x.toFixed(x >= 100 ? 0 : 1)} ${units[i]}`
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

function scanText(ok?: boolean, ms?: number, at?: string, tls?: string): string {
  if (!at) return 'never scanned'
  return `${ok ? 'ok' : 'failed'}${ms ? ` · ${ms} ms` : ''}${tls ? ` · TLS ${tls}` : ''} · ${at}`
}

function scanClass(ok?: boolean, at?: string): string {
  if (!at) return ''
  return ok ? 'lat-ok' : 'lat-bad'
}

function strOrUndef(v: unknown): string | undefined {
  // Defensive: v-model on number inputs may hand us a number at
  // runtime despite the string type — never call .trim() blind.
  const t = String(v ?? '').trim()
  return t === '' ? undefined : t
}

function numOrUndef(v: unknown): number | undefined {
  const t = String(v ?? '').trim()
  if (t === '') return undefined
  const n = Number(t)
  return Number.isFinite(n) ? n : undefined
}

// The 2s poll can outlive a slow refresh (three awaited IPC calls): two
// overlapping refreshes read the same log cursor and append the same chunk
// twice. The connection and aether stores guard the same way.
let refreshInFlight = false

async function refresh() {
  if (refreshInFlight) return
  refreshInFlight = true
  try {
    try {
      status.value = await backend.spoofGetStatus()
    } catch (e) { err.value = String(e) }
    try {
      stats.value = await backend.spoofGetStats()
    } catch { stats.value = null }
    try {
      const chunk = await backend.spoofGetLogs(logCursor)
      if (chunk.length > 0) {
        // Cursor is the newest log ID, not a count: the backend buffer is a
        // 2000-entry ring, so an offset drifts and then stops advancing.
        logCursor = chunk.reduce((max, e) => Math.max(max, Number(e.id ?? 0)), logCursor)
        logs.value = [...logs.value, ...chunk].slice(-1000)
      }
    } catch { /* logs are best-effort */ }
  } finally {
    refreshInFlight = false
  }
}

async function stop() {
  busy.value = true
  err.value = ''
  try {
    await backend.spoofStop()
    await refresh()
  } catch (e) { err.value = String(e) } finally { busy.value = false }
}

// Psiphon chain: a persisted Settings flag the backend reads on every
// CoreSpoof Connect. Saves go through immediately (the lists' contract).
const chainMsg = ref('')
const chainOn = computed({
  get: () => !!settings.settings.spoofPsiphon,
  set: (v: boolean) => { settings.settings.spoofPsiphon = v },
})

async function saveChain() {
  busy.value = true
  err.value = ''
  chainMsg.value = ''
  try {
    await settings.save()
    chainMsg.value = chainOn.value
      ? 'Psiphon chain enabled — Connect a spoof profile to run it.'
      : 'Psiphon chain disabled.'
  } catch (e) { err.value = String(e) } finally { busy.value = false }
}

// The chain is a CoreManager group (spoof engine + transport + Psiphon),
// so it only runs through Connect — the active profile is the target.
async function startChain() {
  const target = chainProfile.value
  if (!target) return
  busy.value = true
  err.value = ''
  msg.value = ''
  chainMsg.value = ''
  try {
    // A profile still on another core (e.g. loopback ws imported as
    // sing-box) would dial a dead local port; switch it first via the
    // same eligibility rule the importer uses, then connect.
    if (target.core !== 2) {
      const saved = await backend.spoofUseProfile(target.id)
      chainMsg.value = `Switched "${saved.name || target.name}" to the SNI spoof core.`
      await profiles.load()
    }
    await connection.connect(target.id)
    if (connection.error) throw new Error(connection.error)
    chainMsg.value = 'Connected through the SNI spoof → Psiphon chain.'
    await refresh()
  } catch (e) { err.value = String(e) } finally { busy.value = false }
}

async function saveConfig() {
  busy.value = true
  cfgMsg.value = ''
  err.value = ''
  try {
    await backend.spoofSetConfig({
      method: strOrUndef(draft.method),
      fingerprint: strOrUndef(draft.fingerprint),
      quicMode: strOrUndef(draft.quicMode),
      ipMode: strOrUndef(draft.ipMode),
      handshakeTimeout: numOrUndef(draft.handshakeTimeout),
      maxConnections: numOrUndef(draft.maxConnections),
      fakeDelay: numOrUndef(draft.fakeDelay),
      seqOverlap: numOrUndef(draft.seqOverlap),
      paddingSize: numOrUndef(draft.paddingSize),
    })
    await settings.load()
    cfgMsg.value = 'Saved.'
  } catch (e) { err.value = String(e) } finally { busy.value = false }
}

async function persistLists() {
  busy.value = true
  err.value = ''
  try {
    await settings.save()
  } catch (e) { err.value = String(e) } finally { busy.value = false }
}

// Bulk paste: dedupe against the store (and within the batch), then a
// single persist — pasting hundreds of IPs costs one settings save.
// Manual entries arrive checked (the common case: the user pasted
// endpoints to use).
async function addIps(values: string[]) {
  const existing = new Set(ipList.value.map((e) => e.ip))
  const fresh: SNIIP[] = []
  for (const v of values) {
    const ip = v.trim()
    if (!ip || existing.has(ip)) continue
    existing.add(ip)
    fresh.push({ ip, source: 'manual', selected: true })
  }
  if (fresh.length === 0) { msg.value = 'Nothing added (all duplicates).'; return }
  settings.settings.sniIps = [...(settings.settings.sniIps ?? []), ...fresh]
  settings.settings.sniSelectedIp = selectedIp.value
  await persistLists()
  msg.value = `Added ${fresh.length} IP(s).`
}

async function removeIp(i: number) {
  settings.settings.sniIps = (settings.settings.sniIps ?? []).filter((_, j) => j !== i)
  settings.settings.sniSelectedIp = selectedIp.value
  await persistLists()
}

// Checkbox toggle: store the explicit flag, then re-derive the pin
// (unchecking the pin yields to the first checked entry — the backend
// applies the same rule via spoofPinIP).
async function toggleIp(ip: string) {
  const e = ipList.value.find((x) => x.ip === ip)
  if (!e) return
  e.selected = e.selected === false
  settings.settings.sniSelectedIp = selectedIp.value
  await persistLists()
}

// "Remove N failed" bulk action: only entries with a failed scan stamp
// (never-scanned ones are kept), and never the primary entry.
async function removeFailedIps() {
  const pinned = selectedIp.value
  const list = settings.settings.sniIps ?? []
  const kept = list.filter((e) => !(e.lastAt && !e.lastOk) || e.ip === pinned)
  const removed = list.length - kept.length
  if (removed === 0) return
  settings.settings.sniIps = kept
  settings.settings.sniSelectedIp = selectedIp.value
  await persistLists()
  msg.value = `Removed ${removed} failing IP(s)${pinned ? ' (primary entry kept)' : ''}.`
}

async function addSnis(values: string[]) {
  const existing = new Set(sniList.value.map((e) => e.host))
  const fresh: SNISNI[] = []
  for (const v of values) {
    const host = normSni(v)
    if (!host || existing.has(host)) continue
    existing.add(host)
    fresh.push({ host, source: 'manual', selected: true })
  }
  if (fresh.length === 0) { msg.value = 'Nothing added (all duplicates).'; return }
  settings.settings.sniSnis = [...(settings.settings.sniSnis ?? []), ...fresh]
  settings.settings.sniSelectedSni = selectedSni.value
  await persistLists()
  msg.value = `Added ${fresh.length} SNI(s).`
}

async function removeSni(i: number) {
  settings.settings.sniSnis = (settings.settings.sniSnis ?? []).filter((_, j) => j !== i)
  settings.settings.sniSelectedSni = selectedSni.value
  await persistLists()
}

// Checkbox toggle (same contract as toggleIp, for the decoy-SNI list).
async function toggleSni(host: string) {
  const e = sniList.value.find((x) => x.host === host)
  if (!e) return
  e.selected = e.selected === false
  settings.settings.sniSelectedSni = selectedSni.value
  await persistLists()
}

// Bulk check/uncheck ("Check all"/"Uncheck all", Shift-click ranges):
// one store write, then re-derive the pin and persist once (same
// contract as toggleIp — an unchecked pin yields to the first checked).
async function setIpsChecked(keys: string[], checked: boolean) {
  const set = new Set(keys)
  for (const e of ipList.value) if (set.has(e.ip)) e.selected = checked
  settings.settings.sniSelectedIp = selectedIp.value
  await persistLists()
}
async function setSnisChecked(keys: string[], checked: boolean) {
  const set = new Set(keys)
  for (const e of sniList.value) if (set.has(e.host)) e.selected = checked
  settings.settings.sniSelectedSni = selectedSni.value
  await persistLists()
}

// Star action: pin any entry as the primary (checks it first). The
// backend pin policy keeps the stored pin while its entry is checked,
// so the starred row becomes the engine's dial target.
async function makeIpPrimary(ip: string) {
  const e = ipList.value.find((x) => x.ip === ip)
  if (!e) return
  e.selected = true
  settings.settings.sniSelectedIp = String(e.ip ?? '').trim()
  await persistLists()
}
async function makeSniPrimary(host: string) {
  const e = sniList.value.find((x) => x.host === host)
  if (!e) return
  e.selected = true
  settings.settings.sniSelectedSni = normSni(e.host)
  await persistLists()
}

async function removeFailedSnis() {
  const pinned = selectedSni.value
  const list = settings.settings.sniSnis ?? []
  const kept = list.filter((e) => !(e.lastAt && !e.lastOk) || normSni(e.host) === pinned)
  const removed = list.length - kept.length
  if (removed === 0) return
  settings.settings.sniSnis = kept
  settings.settings.sniSelectedSni = selectedSni.value
  await persistLists()
  msg.value = `Removed ${removed} failing SNI(s)${pinned ? ' (primary entry kept)' : ''}.`
}

const scanning = ref(false)
// Row key ("ip:1.2.3.4" / "sni:example.com") of the probe in flight.
const scanningKey = ref('')

// Live progress of the in-flight probe ("probe 3/12"), polled from the
// backend status so it is always clear exactly which pairs run — the
// engine fingerprint of the old "scan all" impression.
const scanKind = ref<'ip' | 'sni' | ''>('')
const scanDone = ref(0)
const scanTotal = ref(0)
const checkedIpCount = computed(() => ipList.value.filter((e) => isChecked(e)).length)
const checkedSniCount = computed(() => sniList.value.filter((e) => isChecked(e)).length)
let scanPoll = 0
function startScanPoll(kind: 'ip' | 'sni') {
  scanKind.value = kind
  scanDone.value = 0
  scanTotal.value = 0
  window.clearInterval(scanPoll)
  scanPoll = window.setInterval(async () => {
    if (!scanning.value) return
    try {
      const s = await backend.scanSNIStatus()
      scanDone.value = s.done
      scanTotal.value = s.total
      const [ip, sni] = String(s.last ?? '').split(' / ')
      if (kind === 'ip' && ip) {
        const row = ipList.value.find((e) => String(e.ip ?? '').trim() === ip)
        if (row) scanningKey.value = `ip:${row.ip}`
      } else if (kind === 'sni' && sni) {
        const row = sniList.value.find((e) => normSni(e.host) === sni)
        if (row) scanningKey.value = `sni:${row.host}`
      }
    } catch { /* the run may have just finished */ }
  }, 250)
}
function stopScanPoll() {
  window.clearInterval(scanPoll)
  scanPoll = 0
  scanKind.value = ''
}

const ipHint = computed(() => {
  const base = 'Checked IPs are handed to the engine (best-scanned first); click rows to check/uncheck, star one to make it the primary endpoint. Unchecked entries stay in the list but the engine never uses them.'
  if (!ipList.value.some(isChecked)) {
    return `${base} None checked — the engine falls back to the profile's own endpoint only.`
  }
  return base
})
const sniHint = computed(() => {
  const base = 'Checked SNIs rotate as per-connection decoys; click rows to check/uncheck, star one to make it the primary. Unchecked entries stay in the list but the engine never uses them.'
  if (!sniList.value.some(isChecked)) {
    return `${base} None checked — the engine uses the profile's own SNI only.`
  }
  return base
})

// Per-row probes: an IP row scans against the pinned SNI, an SNI row
// against the pinned IP — one real TLS handshake, one row. "Scan
// checked (N)" probes the checked subset the same way (still no
// IP x SNI matrix: the old "Scan all" did that, and it is gone).
const ipScanTitle = computed(() =>
  selectedSni.value
    ? `Probe this IP against the pinned SNI (${selectedSni.value}) with a real TLS handshake`
    : 'Check a decoy SNI first — an IP probe needs a ServerName to dial',
)
const sniScanTitle = computed(() =>
  selectedIp.value
    ? `Probe this SNI against the pinned IP (${selectedIp.value}) with a real TLS handshake`
    : 'Check a Cloudflare IP first — an SNI probe needs an endpoint to dial',
)
const scanCheckedIpsTitle = computed(() =>
  selectedSni.value
    ? `Probe every checked IP against the pinned SNI (${selectedSni.value}) — one TLS handshake per IP`
    : 'Check a decoy SNI first — IP probes need a ServerName to dial',
)
const scanCheckedSnisTitle = computed(() =>
  selectedIp.value
    ? `Probe the pinned IP (${selectedIp.value}) against every checked SNI — one TLS handshake per SNI`
    : 'Check a Cloudflare IP first — SNI probes need an endpoint to dial',
)

async function scanIp(ip: string) {
  const sni = selectedSni.value
  if (!sni) return
  await scanPair([ip], [sni], `ip:${ip}`, 'ip')
}

async function scanSni(host: string) {
  const ip = selectedIp.value
  if (!ip) return
  await scanPair([ip], [host], `sni:${host}`, 'sni')
}

// "Scan checked (N)": the checked subset only, against the pinned
// counterpart — N probes, never the full matrix.
async function scanCheckedIps() {
  const sni = selectedSni.value
  const ips = ipList.value.filter((e) => isChecked(e)).map((e) => String(e.ip ?? '').trim()).filter(Boolean)
  if (!sni || ips.length === 0) return
  await scanPair(ips, [sni], '', 'ip')
}

async function scanCheckedSnis() {
  const ip = selectedIp.value
  const snis = sniList.value.filter((e) => isChecked(e)).map((e) => normSni(e.host)).filter(Boolean)
  if (!ip || snis.length === 0) return
  await scanPair([ip], snis, '', 'sni')
}

// One probe at a time (the backend rejects concurrent runs): flag the
// row(s), poll live progress, round-trip, then reload the scanner
// stamps.
async function scanPair(ips: string[], snis: string[], key: string, kind: 'ip' | 'sni') {
  scanning.value = true
  scanningKey.value = key
  err.value = ''
  msg.value = ''
  startScanPoll(kind)
  try {
    const r = await backend.scanSNIPairs(ips, snis)
    if (r.length === 1) {
      const hit = r[0]
      const target = `${hit.sni} @ ${hit.ip}`
      msg.value = hit.ok
        ? `Scan ${target}: OK${hit.latencyMs ? ` · ${hit.latencyMs} ms` : ''}.`
        : `Scan ${target}: failed${hit.error ? ` — ${hit.error}` : ''}.`
    } else {
      const ok = r.filter((x) => x.ok).length
      msg.value = `Scanned ${r.length} pair(s): ${ok} OK, ${r.length - ok} failed.`
    }
    await settings.load()
  } catch (e) {
    // A user Cancel surfaces as context canceled — not an error.
    const m = String(e)
    if (/canceled|cancelled/i.test(m)) msg.value = 'Scan cancelled.'
    else err.value = m
  } finally {
    stopScanPoll()
    scanning.value = false
    scanningKey.value = ''
  }
}

async function cancelScan() {
  try { await backend.cancelSNIScan() } catch { /* run may have finished */ }
}

async function resetLists() {
  busy.value = true
  err.value = ''
  try {
    await backend.resetSNIDefaults()
    await settings.load()
    msg.value = 'Lists reset to curated defaults.'
  } catch (e) { err.value = String(e) } finally { busy.value = false }
}

function clearLogs() {
  logs.value = []
}

let timer = 0
// Set on unmount: the mount repair below awaits several RPCs, and an
// interval installed after the tab is gone was never cleared (every quick
// visit leaked another 2 s poller).
let disposed = false

onMounted(async () => {
  try {
    const cfg = await backend.spoofGetConfig()
    draft.method = String(cfg.method ?? 'auto')
    draft.fingerprint = String(cfg.fingerprint ?? '')
    draft.quicMode = String(cfg.quicMode ?? '')
    draft.ipMode = String(cfg.ipMode ?? '')
    draft.handshakeTimeout = cfg.handshakeTimeout?.toString() ?? ''
    draft.maxConnections = cfg.maxConnections?.toString() ?? ''
    draft.fakeDelay = cfg.fakeDelay?.toString() ?? ''
    draft.seqOverlap = cfg.seqOverlap?.toString() ?? ''
    draft.paddingSize = cfg.paddingSize?.toString() ?? ''
  } catch (e) { err.value = String(e) }
  // Fresh profiles: the store may predate an import done from another
  // shell, which is exactly how a convertible profile shows up here.
  try { await profiles.load() } catch { /* keep store */ }
  await loadPrivileged()
  await refresh()
  if (disposed) return
  timer = window.setInterval(refresh, 2000)
})

onUnmounted(() => {
  disposed = true
  window.clearInterval(timer)
  window.clearInterval(scanPoll)
})
</script>
