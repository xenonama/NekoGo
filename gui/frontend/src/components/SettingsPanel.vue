<!-- Settings tab: hero card, pill tabs, grouped rows with auto-persist controls.
     No Save button: every control writes through the settings store. -->
<template>
  <div class="stg">
    <div class="stg-hero">
      <span class="stg-hero-chip"><Icon name="gear" :size="34" /></span>
      <div class="stg-hero-text">
        <h2>Settings</h2>
        <p>Customize your NekoBox experience</p>
      </div>
      <Icon name="cloud" :size="28" class="stg-hero-cloud" />
      <img class="stg-hero-cat" :src="appIcon" alt="" />
    </div>

    <div class="stg-tabs" role="tablist">
      <button
        v-for="t in tabs"
        :key="t.key"
        class="stg-tab"
        :class="{ active: tab === t.key }"
        role="tab"
        :aria-selected="tab === t.key"
        @click="tab = t.key"
      >
        <Icon :name="t.icon" :size="18" /><span>{{ t.label }}</span>
      </button>
    </div>

    <!-- Throne parity (PendingRestart): port/TUN/core changes
         render into the core config at connect time, so they apply
         on reconnect — not silently, not immediately. -->
    <div v-if="connection.status.running" class="nb-warn-banner">
      Connected — port, TUN, system-proxy and core-path changes apply after you reconnect.
    </div>

    <div v-if="activeTab && tab !== 'updates'" class="stg-card">
      <div class="stg-head">
        <span class="stg-head-chip"><Icon :name="activeTab.icon" :size="22" /></span>
        <span class="stg-head-text">
          <span class="stg-head-title">{{ activeTab.head }}</span>
          <span class="stg-head-sub">{{ activeTab.headSub }}</span>
        </span>
      </div>
      <div
        v-for="r in activeTab.rows"
        :key="r.key"
        class="stg-row"
      >
        <span class="stg-chip"><Icon :name="r.icon" :size="18" /></span>
        <span class="stg-meta">
          <span class="stg-title">{{ r.title }}</span>
          <span class="stg-sub">{{ r.sub }}</span>
        </span>
        <span class="stg-ctl">
          <button
            v-if="r.kind === 'toggle'"
            class="stg-toggle"
            :class="{ on: !!s[r.field as BoolKey] }"
            role="switch"
            :aria-checked="!!s[r.field as BoolKey]"
            :aria-label="r.title"
            :title="r.title"
            @click="flip(r.field as BoolKey)"
          >
            <span class="knob" />
          </button>
          <PickSelect
            v-else-if="r.kind === 'select'"
            :model-value="String(s[r.field as StrKey])"
            :options="r.options ?? []"
            :icon="r.icon"
            :title="r.title"
            :aria-label="r.title"
            quiet
            @update:model-value="setStr(r.field as StrKey, $event)"
          />
          <input
            v-else-if="r.kind === 'number'"
            class="stg-num"
            type="number"
            min="1"
            max="65534"
            :aria-label="r.title"
            :value="Number(s[r.field as NumKey])"
            @change="setNum(r.field as NumKey, ($event.target as HTMLInputElement).value)"
          />
          <input
            v-else
            class="stg-text"
            type="text"
            :aria-label="r.title"
            :value="(s[r.field as StrKey] ?? '') as string"
            @change="setStr(r.field as StrKey, ($event.target as HTMLInputElement).value)"
          />
        </span>
      </div>
      <div v-if="(tab === 'general' || tab === 'connection') && bootMsg" class="nb-json-error" style="padding: 4px 0 8px">{{ bootMsg }}</div>
      <div v-if="saveErr" class="nb-json-error" style="padding: 4px 0 8px">{{ saveErr }}</div>
      <!-- TUN diagnostics: traffic can stall with no core error at all
           (poisoned DNS, oversized packets), so the TUN group carries
           its own hint. MTU is rendered at 1400 (tunMTU in
           singbox_manager.go) and Advanced -> Config -> Inbound
           overrides it for unusual links. -->
      <div v-if="tab === 'connection'" class="stg-sub" style="padding: 4px 0 10px">
        If YouTube or heavy sites fail in TUN mode, try lowering the MTU to 1400 or 1350 — NekoGo renders 1400; override it under Advanced → Config → Inbound.
      </div>
      <!-- GeoIP extras: status readout + manual actions (auto-persist rows above). -->
      <template v-if="tab === 'geoip'">
        <div class="stg-row">
          <span class="stg-chip"><Icon name="globe" :size="18" /></span>
          <span class="stg-meta">
            <span class="stg-title">Status</span>
            <span class="stg-sub">{{ geoStatusText }}</span>
            <!-- BUG-019: the backend reports a specific reason — a
                 corrupt/unreadable DB must not read as merely absent. -->
            <span v-if="geoStatus?.error" class="stg-sub upd-err">{{ geoStatus.error }}</span>
          </span>
          <span class="stg-ctl">
            <button class="nb-act" :disabled="geoBusy" @click="loadGeoStatus">
              <Icon name="check" :size="14" />Check
            </button>
          </span>
        </div>
        <div class="stg-row">
          <span class="stg-chip"><Icon name="refresh" :size="18" /></span>
          <span class="stg-meta">
            <span class="stg-title">Refresh all countries</span>
            <span class="stg-sub">{{ geoMsg || 'Detect countries for every profile' }}</span>
            <!-- BUG-020: per-profile reasons as an expandable list. -->
            <details v-if="geoErrors.length > 0" class="stg-sub">
              <summary>Refresh failures ({{ geoErrors.length }})</summary>
              <div v-for="(e, i) in geoErrors" :key="i">{{ e }}</div>
            </details>
          </span>
          <span class="stg-ctl">
            <button class="nb-act" :disabled="geoBusy" @click="geoRefresh">
              <Icon name="refresh" :size="14" />Refresh
            </button>
            <button v-if="geoRefreshing" class="nb-act nb-act--warn" @click="geoCancel">
              <Icon name="x" :size="14" />Cancel
            </button>
          </span>
        </div>
        <div class="stg-row">
          <span class="stg-chip"><Icon name="trash" :size="18" /></span>
          <span class="stg-meta">
            <span class="stg-title">Clear all countries</span>
            <span class="stg-sub">Wipe every detected flag</span>
          </span>
          <span class="stg-ctl">
            <button class="nb-act nb-act--danger" :disabled="geoBusy" @click="geoClear">
              <Icon name="trash" :size="14" />Clear
            </button>
          </span>
        </div>
      </template>
      <!-- Transport extras: global fragment + fingerprint overrides.
           Render-time only (stored profiles keep their own values, so
           turning the switches off restores every profile exactly).
           Raw-JSON profiles are exempt — raw bypasses the builders. -->
      <template v-if="tab === 'transport'">
        <div class="stg-row">
          <span class="stg-chip"><Icon name="tune" :size="18" /></span>
          <span class="stg-meta">
            <span class="stg-title">Fragment for all configs</span>
            <span class="stg-sub">When on, every profile fragments TLS hello with the options below (applies on reconnect)</span>
          </span>
          <span class="stg-ctl">
            <button
              class="stg-toggle"
              :class="{ on: !!s.fragmentEnabled }"
              role="switch"
              :aria-checked="!!s.fragmentEnabled"
              aria-label="Fragment for all configs"
              title="Fragment for all configs"
              @click="flipTransport('fragmentEnabled')"
            >
              <span class="knob" />
            </button>
          </span>
        </div>
        <div class="stg-row">
          <span class="stg-chip"><Icon name="tune" :size="18" /></span>
          <span class="stg-meta">
            <span class="stg-title">Fragment mode</span>
            <span class="stg-sub">tlshello fragments the hello · N-M fragments packets N-M</span>
          </span>
          <span class="stg-ctl">
            <PickSelect
              :model-value="s.fragmentMode ?? 'tlshello'"
              icon="tune"
              title="Fragment mode"
              aria-label="Fragment mode"
              quiet
              :options="FRAG_MODES"
              @update:model-value="setTransportStr('fragmentMode', $event)"
            />
          </span>
        </div>
        <div class="stg-row">
          <span class="stg-chip"><Icon name="tune" :size="18" /></span>
          <span class="stg-meta">
            <span class="stg-title">Fragment length</span>
            <span class="stg-sub">Range A-B, e.g. 10-20 (empty = 10-20)</span>
          </span>
          <span class="stg-ctl">
            <input
              class="stg-text"
              type="text"
              aria-label="Fragment length"
              placeholder="10-20"
              :value="s.fragmentLength ?? ''"
              @change="setTransportStr('fragmentLength', ($event.target as HTMLInputElement).value)"
            />
          </span>
        </div>
        <div class="stg-row">
          <span class="stg-chip"><Icon name="tune" :size="18" /></span>
          <span class="stg-meta">
            <span class="stg-title">Fragment interval (ms)</span>
            <span class="stg-sub">Delay upper bound, 0 = none</span>
          </span>
          <span class="stg-ctl">
            <input
              class="stg-num"
              type="number"
              min="0"
              aria-label="Fragment interval ms"
              :value="Number(s.fragmentInterval ?? 0)"
              @change="setTransportInterval(($event.target as HTMLInputElement).value)"
            />
          </span>
        </div>
        <div class="stg-row">
          <span class="stg-chip"><Icon name="shield" :size="18" /></span>
          <span class="stg-meta">
            <span class="stg-title">Fingerprint for all configs</span>
            <span class="stg-sub">When on, every TLS profile uses the fingerprint below (applies on reconnect)</span>
          </span>
          <span class="stg-ctl">
            <button
              class="stg-toggle"
              :class="{ on: !!s.fingerprintEnabled }"
              role="switch"
              :aria-checked="!!s.fingerprintEnabled"
              aria-label="Fingerprint for all configs"
              title="Fingerprint for all configs"
              @click="flipTransport('fingerprintEnabled')"
            >
              <span class="knob" />
            </button>
          </span>
        </div>
        <div class="stg-row">
          <span class="stg-chip"><Icon name="shield" :size="18" /></span>
          <span class="stg-meta">
            <span class="stg-title">Fingerprint value</span>
            <span class="stg-sub">uTLS ClientHello both cores accept</span>
          </span>
          <span class="stg-ctl">
            <PickSelect
              :model-value="s.fingerprintValue ?? 'chrome'"
              icon="shield"
              title="Fingerprint value"
              aria-label="Fingerprint value"
              quiet
              :options="FP_VALUES"
              @update:model-value="setTransportStr('fingerprintValue', $event)"
            />
          </span>
        </div>
        <div v-if="transpErr" class="nb-json-error" style="padding: 4px 0 8px">{{ transpErr }}</div>
        <div v-else class="muted" style="padding: 0 0 8px">Stored profiles are untouched — turning a switch off restores every profile's own values.</div>
      </template>
    </div>
    <div v-else-if="tab === 'updates'" class="stg-card">
      <div class="stg-head">
        <span class="stg-head-chip"><Icon name="download" :size="22" /></span>
        <span class="stg-head-text">
          <span class="stg-head-title">Updates</span>
          <span class="stg-head-sub">Manual component checks and installs</span>
        </span>
      </div>
      <UpdatesPanel />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import Icon, { type IconName } from './Icon.vue'
import appIcon from '../assets/app.png'
import { useSettingsStore } from '../stores/settings'
import { useProfilesStore } from '../stores/profiles'
import { useConnectionStore } from '../stores/connection'
import UpdatesPanel from './UpdatesPanel.vue'
import PickSelect from './PickSelect.vue'
import { backend } from '../api'
import type { GeoIPStatus } from '../types'

type BoolKey = 'startOnBoot' | 'closeToTray' | 'tunEnabled' | 'autoConnect' | 'sidebarLabels' | 'geoIPAutoDetect' | 'fragmentEnabled' | 'fingerprintEnabled' | 'systemProxy' | 'spoofXray'
// No 'systemProxy' member: every Connect ensures the OS proxy now
// (the flag no longer gates anything), so the row is gone — same
// dead-toggle removal as 'notifications' below.
// No 'notifications' member: the toggle was dead (nothing reads the
// flag; Wails beta.20 has no desktop-notify API), so the row is gone.
type StrKey = 'language' | 'theme' | 'windowStyle' | 'xrayApiAddress' | 'testUrl' | 'geoIPDBPath' | 'logLevel' | 'logOutputPath' | 'inboundAddress' | 'dnsRemote' | 'dnsDirect' | 'fragmentMode' | 'fragmentLength' | 'fingerprintValue' | 'tunStack' | 'coreEngine'
type NumKey = 'mixedPort'

interface SelectOpt { value: string; label: string }
interface RowDef {
  key: string
  icon: IconName
  title: string
  sub: string
  kind: 'toggle' | 'select' | 'number' | 'text'
  field: BoolKey | StrKey | NumKey
  options?: SelectOpt[]
}
interface TabDef {
  key: string
  label: string
  icon: IconName
  head: string
  headSub: string
  rows: RowDef[]
}

const store = useSettingsStore()
// Computed, not a captured object: settings.load() replaces the whole
// settings object (backup import, reset to defaults), and a panel that
// held the old one would render and mutate a detached copy while save()
// persisted the live one — every edit silently lost. The template reads
// s.* (auto-unwrapped); the script below goes through store.settings.
const s = computed(() => store.settings)
const tab = ref('general')
// Last backend rejection of a settings save; the row now snaps back, so
// this explains why.
const saveErr = ref('')

// Persist and report success: the generic rows used to keep an
// optimistic value the backend rejected (validation of ports, DNS,
// paths), so the UI showed state disk never kept.
async function persist(): Promise<boolean> {
  try {
    await store.save()
    saveErr.value = ''
    return true
  } catch (e) {
    // Dev mode without a backend keeps the local choice.
    if (!backend.available()) return true
    saveErr.value = String(e)
    return false
  }
}

async function flip(field: BoolKey) {
  // The tray toggle must reach Go: SetCloseToTray persists server-side
  // AND shows/hides (or rebuilds) the icon immediately. On failure the
  // local value is left untouched so the toggle snaps back.
  if (field === 'closeToTray') {
    const next = !store.settings[field]
    try {
      await backend.setCloseToTray(next)
      store.settings[field] = next
    } catch (e) {
      console.error(e)
    }
    return
  }
  // Same for launch-at-login: SetStartOnBoot writes the OS Run key
  // AND persists. Failure (e.g. non-Windows) snaps back with a note.
  if (field === 'startOnBoot') {
    const next = !store.settings[field]
    bootMsg.value = ''
    try {
      await backend.setStartOnBoot(next)
      store.settings[field] = next
    } catch (e) {
      bootMsg.value = String(e)
    }
    return
  }
  const prev = store.settings[field] ?? false
  store.settings[field] = !prev
  if (!(await persist())) store.settings[field] = prev
}

async function setStr(field: StrKey, value: string) {
  // Theme must go through the single shared code path (instant apply + persist).
  if (field === 'theme') {
    await store.setTheme(value as 'light' | 'dark')
    return
  }
  const prev = store.settings[field] ?? ''
  store.settings[field] = value
  if (!(await persist())) store.settings[field] = prev
}

async function setNum(field: NumKey, raw: string) {
  const v = Math.min(65534, Math.max(1, parseInt(raw, 10) || 0))
  const prev = store.settings[field]
  store.settings[field] = v
  if (!(await persist())) store.settings[field] = prev
}

// Transport tab extras: option sources + error-surfacing persist.
// Unlike the generic persist() (which swallows backend rejections),
// these revert the optimistic set on failure so the UI never shows a
// global the disk refused (backend validates mode/length/fingerprint).
const FRAG_MODES = [
  { value: 'tlshello', label: 'tlshello' },
  { value: '1-1', label: '1-1' },
  { value: '1-2', label: '1-2' },
  { value: '1-3', label: '1-3' },
  { value: '1-5', label: '1-5' },
]
const FP_VALUES = [
  { value: 'chrome', label: 'chrome' },
  { value: 'firefox', label: 'firefox' },
  { value: 'safari', label: 'safari' },
  { value: 'ios', label: 'ios' },
  { value: 'android', label: 'android' },
  { value: 'edge', label: 'edge' },
  { value: '360', label: '360' },
  { value: 'qq', label: 'qq' },
  { value: 'random', label: 'random' },
  { value: 'randomized', label: 'randomized' },
]
const transpErr = ref('')

async function flipTransport(field: 'fragmentEnabled' | 'fingerprintEnabled') {
  const prev = !!store.settings[field]
  store.settings[field] = !prev
  transpErr.value = ''
  try {
    await store.save()
  } catch (e) {
    store.settings[field] = prev
    transpErr.value = String(e)
  }
}

async function setTransportStr(field: 'fragmentMode' | 'fragmentLength' | 'fingerprintValue', value: string) {
  const prev = (store.settings[field] ?? '') as string
  ;(store.settings[field] as string) = value
  transpErr.value = ''
  try {
    await store.save()
  } catch (e) {
    ;(store.settings[field] as string) = prev
    transpErr.value = String(e)
  }
}

async function setTransportInterval(raw: string) {
  const prev = Number(store.settings.fragmentInterval ?? 0)
  const v = Math.max(0, parseInt(raw, 10) || 0)
  store.settings.fragmentInterval = v
  transpErr.value = ''
  try {
    await store.save()
  } catch (e) {
    store.settings.fragmentInterval = prev
    transpErr.value = String(e)
  }
}

// Launch-at-login note (e.g. non-Windows backend rejection).
const bootMsg = ref('')

// GeoIP extras (status + manual actions live outside the generic row kinds).
const profiles = useProfilesStore()
const connection = useConnectionStore()
const geoStatus = ref<GeoIPStatus | null>(null)
const geoMsg = ref('')
const geoBusy = ref(false)
const geoErrors = ref<string[]>([])

const geoStatusText = computed(() => {
  if (!geoStatus.value) return 'Unknown — press Check'
  if (geoStatus.value.available) return `Available (${geoStatus.value.path})`
  return 'Missing — place GeoLite2-Country.mmdb in bin\\ (next to the engines) or gui/assets/ in dev'
})

async function loadGeoStatus() {
  try {
    geoStatus.value = await backend.geoIPStatus()
  } catch {
    geoStatus.value = null
  }
}

// BUG-003: bounded backend run (60s total) with live N/M progress and
// Cancel. The in-flight promise resolves with counted failures after a
// cancel, so the button just flips back with partial counts.
const geoRefreshing = ref(false)
const geoCancelled = ref(false)
// The progress poller must not outlive the tab: leaving Settings mid-refresh
// kept a 500ms interval running (and writing into dead refs) until the
// backend refresh finished.
let geoPoll: number | null = null
function stopGeoPoll() {
  if (geoPoll !== null) {
    window.clearInterval(geoPoll)
    geoPoll = null
  }
}
async function geoRefresh() {
  geoBusy.value = true
  geoRefreshing.value = true
  geoCancelled.value = false
  geoMsg.value = 'Refreshing… (may take a while for many profiles)'
  stopGeoPoll()
  geoPoll = window.setInterval(async () => {
    try {
      const p = await backend.refreshCountriesProgress()
      if (p.running && p.total > 0) geoMsg.value = `Refreshing… ${p.done}/${p.total}`
    } catch {
      /* best-effort only */
    }
  }, 500)
  try {
    const r = await backend.refreshAllCountries()
    const tail = geoCancelled.value ? ' (cancelled)' : ''
    geoMsg.value = `Done${tail}: ${r?.updated ?? 0} updated, ${r?.skipped ?? 0} skipped, ${r?.failed ?? 0} failed.`
    geoErrors.value = r?.errors ?? []
    await profiles.load()
    await loadGeoStatus()
  } catch (e) {
    geoMsg.value = String(e)
    geoErrors.value = []
  } finally {
    stopGeoPoll()
    geoRefreshing.value = false
    geoBusy.value = false
  }
}

async function geoCancel() {
  geoCancelled.value = true
  try {
    await backend.cancelRefreshCountries()
  } catch {
    /* the run may have just finished */
  }
}

async function geoClear() {
  if (!confirm('Clear all detected countries?')) return
  geoBusy.value = true
  try {
    await backend.clearCountries()
    geoMsg.value = 'Cleared.'
    await profiles.load()
  } catch (e) {
    geoMsg.value = String(e)
  } finally {
    geoBusy.value = false
  }
}

watch(tab, (t) => {
  if (t === 'geoip') void loadGeoStatus()
})
// The Run key can disappear behind our back (Task Manager, a cleaner tool):
// show what Windows actually has instead of the flag persisted at last write.
async function syncStartOnBoot() {
  try {
    const on = await backend.getAutoRun()
    if (typeof on === 'boolean' && on !== store.settings.startOnBoot) {
      store.settings.startOnBoot = on
      bootMsg.value = on ? '' : 'Windows no longer has the Run key for NekoGo.'
    }
  } catch {
    /* non-Windows or no permission: keep the stored value */
  }
}
onMounted(() => {
  if (tab.value === 'geoip') void loadGeoStatus()
  void syncStartOnBoot()
  connection.acquire()
})
onUnmounted(() => {
  stopGeoPoll()
  connection.release()
})

const tabs: TabDef[] = [
  {
    key: 'general', label: 'General', icon: 'gear',
    head: 'General', headSub: 'Startup, tray and language',
    rows: [
      { key: 'boot', icon: 'power', title: 'Start on system startup', sub: 'Launch NekoGo at login (Windows Run key)', kind: 'toggle', field: 'startOnBoot' },
      { key: 'tray', icon: 'minus', title: 'Minimize to tray', sub: 'Keep running in the tray on close', kind: 'toggle', field: 'closeToTray' },

      // TODO: add fa/zh options once translations land.
      { key: 'lang', icon: 'languages', title: 'Language', sub: 'Interface language', kind: 'select', field: 'language', options: [{ value: 'en', label: 'English' }] },
    ],
  },
  {
    key: 'proxy', label: 'Proxy', icon: 'layers',
    head: 'Proxy', headSub: 'Local inbound ports and DNS',
    rows: [
      { key: 'mixed', icon: 'globe', title: 'Mixed port', sub: 'HTTP proxy on this port · SOCKS proxy on port + 1 (applies on reconnect)', kind: 'number', field: 'mixedPort' },
      { key: 'bind', icon: 'wifi', title: 'Listen address', sub: 'Empty = loopback only · 0.0.0.0 shares on the LAN (applies on reconnect)', kind: 'text', field: 'inboundAddress' },
      { key: 'dnsremote', icon: 'cloud', title: 'Upstream DNS', sub: 'DoH URL, IP or "local" — empty = system default (sing-box)', kind: 'text', field: 'dnsRemote' },
      { key: 'dnsdirect', icon: 'cloud', title: 'Direct DNS', sub: 'Bypass upstream for direct traffic — empty = system default', kind: 'text', field: 'dnsDirect' },
      { key: 'api', icon: 'bolt', title: 'Xray API', sub: 'StatsService endpoint, e.g. 127.0.0.1:10085', kind: 'text', field: 'xrayApiAddress' },
    ],
  },
  {
    key: 'connection', label: 'Connection', icon: 'wifi',
    head: 'Connection', headSub: 'TUN and auto-connect (the OS proxy is set on every connect)',
    rows: [
      // Opt-in backend switch: "custom" is the bundled NekoBox core
      // (Thrift RPC, full protocol set); "upstream" spawns the official
      // sing-box.exe CLI and talks to its Clash API for stats.
      {
        key: 'core', icon: 'gear', title: 'Core engine',
        sub: 'Applies on reconnect. Runs every sing-box transport (plain profiles, SNI spoof, Aether).',
        kind: 'select', field: 'coreEngine',
        options: [
          { value: 'custom', label: 'NekoBox Core' },
          { value: 'upstream', label: 'sing-box' },
        ],
      },
      { key: 'tun', icon: 'shield', title: 'TUN mode', sub: 'Route all traffic (sing-box, applies on reconnect)', kind: 'toggle', field: 'tunEnabled' },
      // CPU: system hands packets to the OS stack; gVisor keeps them in
      // user space and costs ~2-5x the CPU per byte on Windows.
      { key: 'tunstack', icon: 'tune', title: 'TUN stack', sub: 'System is fastest on Windows · gVisor costs more CPU per byte (applies on reconnect)', kind: 'select', field: 'tunStack', options: [{ value: 'system', label: 'System' }, { value: 'gvisor', label: 'gVisor' }, { value: 'mixed', label: 'Mixed' }] },
      { key: 'auto', icon: 'power', title: 'Auto-connect', sub: 'Connect last profile on launch', kind: 'toggle', field: 'autoConnect' },
      { key: 'sysproxy', icon: 'globe', title: 'System proxy on connect', sub: 'Point the OS proxy at NekoGo in proxy mode — ignored in TUN mode (including the SNI spoof + Psiphon chain), where TUN carries all traffic', kind: 'toggle', field: 'systemProxy' },
      { key: 'spoofxray', icon: 'layers', title: 'Xray for SNI spoofing', sub: 'Run SNI spoofing connects on the Xray transport instead of the NekoBox Core (applies on reconnect). The SNI spoof + Psiphon chain stays on sing-box: its TUN inbound exists there only.', kind: 'toggle', field: 'spoofXray' },
      { key: 'testurl', icon: 'bolt', title: 'Test URL', sub: 'Latency probe target (empty = default)', kind: 'text', field: 'testUrl' },
    ],
  },
  {
    key: 'transport', label: 'Transport', icon: 'tune',
    head: 'Transport', headSub: 'Global fragment + fingerprint for every profile (applies on reconnect)',
    rows: [],
  },
  {
    key: 'appearance', label: 'Appearance', icon: 'palette',
    head: 'Appearance', headSub: 'Theme, window and sidebar',
    rows: [
      { key: 'theme', icon: 'sun', title: 'Theme', sub: 'Light or dark interface', kind: 'select', field: 'theme', options: [{ value: 'light', label: 'Light' }, { value: 'dark', label: 'Dark' }] },
      { key: 'labels', icon: 'list', title: 'Show sidebar labels', sub: 'Icons only when off', kind: 'toggle', field: 'sidebarLabels' },
    ],
  },
  {
    key: 'geoip', label: 'GeoIP', icon: 'globe',
    head: 'GeoIP', headSub: 'Country flags from GeoLite2-Country',
    rows: [
      { key: 'autodetect', icon: 'bolt', title: 'Auto-detect country on import/save', sub: 'Look up flags in the background', kind: 'toggle', field: 'geoIPAutoDetect' },
      { key: 'dbpath', icon: 'folder', title: 'Database path', sub: 'Empty = auto (bin\\GeoLite2-Country.mmdb next to the engines, or gui/assets/ in dev)', kind: 'text', field: 'geoIPDBPath' },
    ],
  },
  {
    key: 'updates', label: 'Updates', icon: 'download',
    head: 'Updates', headSub: 'Manual component checks and installs',
    rows: [],
  },
]

const activeTab = computed(() => tabs.find((t) => t.key === tab.value))
</script>
