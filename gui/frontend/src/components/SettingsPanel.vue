<!-- Settings tab: hero card, pill tabs, grouped rows with auto-persist controls.
     No Save button: every control writes through the settings store. -->
<template>
  <div class="stg">
    <div class="stg-hero">
      <span class="stg-hero-chip"><Icon name="gear" :size="34" /></span>
      <div class="stg-hero-text">
        <h2>Settings</h2>
        <p>How NekoGo starts, listens and connects</p>
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
        v-for="r in visibleRows"
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
            @change="setNum(r.field as NumKey, $event.target as HTMLInputElement)"
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
      <!-- TUN diagnostics + the field group the core reads
           (gui/tun_config.go). Traffic can stall with no core error at
           all — poisoned DNS, oversized packets — so the advice sits
           with the control that fixes it, and the MTU it names is the
           row below rather than a constant in a Go file. -->
      <template v-if="tab === 'connection' && s.tunEnabled">
        <div class="stg-sub" style="padding: 4px 0 10px">
          If heavy pages stall in TUN mode while search still works, the packets are too big
          for the link: try lowering the MTU below and reconnecting. If nothing reaches the
          tunnel at all, check Auto route.
        </div>
        <div class="stg-row">
          <span class="stg-chip"><Icon name="tune" :size="18" /></span>
          <span class="stg-meta">
            <span class="stg-title">MTU</span>
            <span class="stg-sub">Packet size the adapter is given · 1280-65535, 1350 when unset (applies on reconnect)</span>
          </span>
          <span class="stg-ctl">
            <input
              class="stg-num"
              type="number"
              min="1280"
              max="65535"
              step="1"
              aria-label="MTU"
              :value="Number(s.tunMTU || TUN_MTU_DEFAULT)"
              @change="setTunMTU($event.target as HTMLInputElement)"
            />
          </span>
        </div>
        <div class="stg-row">
          <span class="stg-chip"><Icon name="layers" :size="18" /></span>
          <span class="stg-meta">
            <span class="stg-title">Auto route</span>
            <span class="stg-sub">Point the default route at the adapter. Off leaves the tunnel up with nothing in it — for setups that install their own routes (applies on reconnect)</span>
          </span>
          <span class="stg-ctl">
            <button
              class="stg-toggle"
              :class="{ on: s.tunAutoRoute !== false }"
              role="switch"
              :aria-checked="s.tunAutoRoute !== false"
              aria-label="Auto route"
              title="Auto route"
              @click="setTunBool('tunAutoRoute')"
            >
              <span class="knob" />
            </button>
          </span>
        </div>
        <div class="stg-row">
          <span class="stg-chip"><Icon name="shield" :size="18" /></span>
          <span class="stg-meta">
            <span class="stg-title">Strict route</span>
            <span class="stg-sub">Drop traffic that did not come from the adapter — nothing leaks around the tunnel, and local discovery can go with it (applies on reconnect)</span>
          </span>
          <span class="stg-ctl">
            <button
              class="stg-toggle"
              :class="{ on: !!s.tunStrictRoute }"
              role="switch"
              :aria-checked="!!s.tunStrictRoute"
              aria-label="Strict route"
              title="Strict route"
              @click="setTunBool('tunStrictRoute')"
            >
              <span class="knob" />
            </button>
          </span>
        </div>
        <div class="stg-row">
          <span class="stg-chip"><Icon name="globe" :size="18" /></span>
          <span class="stg-meta">
            <span class="stg-title">Adapter network (IPv4)</span>
            <span class="stg-sub">Move it when this range is already routed on your machine (applies on reconnect)</span>
          </span>
          <span class="stg-ctl">
            <PickSelect
              :model-value="s.tunAddress || '172.19.0.1/24'"
              icon="globe"
              title="Adapter network (IPv4)"
              aria-label="Adapter network IPv4"
              quiet
              :options="tunV4Options"
              @update:model-value="setTunStr('tunAddress', $event)"
            />
          </span>
        </div>
        <div class="stg-row">
          <span class="stg-chip"><Icon name="globe" :size="18" /></span>
          <span class="stg-meta">
            <span class="stg-title">IPv6 on the adapter</span>
            <span class="stg-sub">NekoGo asks only when Windows still gives new interfaces IPv6 — or when a global IPv6 address elsewhere could reach the internet around the tunnel (applies on reconnect)</span>
          </span>
          <span class="stg-ctl">
            <button
              class="stg-toggle"
              :class="{ on: !!s.tunEnableIPv6 }"
              role="switch"
              :aria-checked="!!s.tunEnableIPv6"
              aria-label="IPv6 on the adapter"
              title="IPv6 on the adapter"
              @click="setTunBool('tunEnableIPv6')"
            >
              <span class="knob" />
            </button>
          </span>
        </div>
        <div v-if="s.tunEnableIPv6" class="stg-row">
          <span class="stg-chip"><Icon name="globe" :size="18" /></span>
          <span class="stg-meta">
            <span class="stg-title">Adapter network (IPv6)</span>
            <span class="stg-sub">Needs room for a peer address (/126 or shorter) — a single-address prefix is one the core refuses (applies on reconnect)</span>
          </span>
          <span class="stg-ctl">
            <PickSelect
              :model-value="s.tunAddress6 || 'fc00::172:19:0:1/126'"
              icon="globe"
              title="Adapter network (IPv6)"
              aria-label="Adapter network IPv6"
              quiet
              :options="tunV6Options"
              @update:model-value="setTunStr('tunAddress6', $event)"
            />
          </span>
        </div>
        <div class="stg-row">
          <span class="stg-chip"><Icon name="layers" :size="18" /></span>
          <span class="stg-meta">
            <span class="stg-title">Routes to leave alone</span>
            <span class="stg-sub">Comma-separated CIDRs auto route must not take, e.g. 192.168.1.0/24 for a LAN printer (applies on reconnect)</span>
          </span>
          <span class="stg-ctl">
            <input
              class="stg-text"
              type="text"
              aria-label="Routes to leave alone"
              placeholder="192.168.1.0/24, 10.0.0.0/8"
              :value="(s.tunRouteExclude || []).join(', ')"
              @change="setTunExclude(($event.target as HTMLInputElement).value)"
            />
          </span>
        </div>
        <div class="stg-row">
          <span class="stg-chip"><Icon name="bolt" :size="18" /></span>
          <span class="stg-meta">
            <span class="stg-title">Ping (ICMP)</span>
            <span class="stg-sub">What answers a ping while the tunnel is up. “Rule” leaves it to the routing rules, which is how it has always run (applies on reconnect)</span>
          </span>
          <span class="stg-ctl">
            <PickSelect
              :model-value="s.tunIcmp || 'rule'"
              icon="bolt"
              title="Ping (ICMP)"
              aria-label="ICMP policy"
              quiet
              :options="TUN_ICMP_OPTIONS"
              @update:model-value="setTunStr('tunIcmp', $event)"
            />
          </span>
        </div>
      </template>
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
import { parsePortInput } from '../ports'
import { useConfirmStore } from '../stores/confirm'
import type { GeoIPStatus, Settings } from '../types'

type BoolKey = 'startOnBoot' | 'closeToTray' | 'tunEnabled' | 'autoConnect' | 'sidebarLabels' | 'geoIPAutoDetect' | 'fragmentEnabled' | 'fingerprintEnabled' | 'systemProxy' | 'spoofXray' | 'spoofEnabled'
// 'systemProxy' keeps its member and its row: Connect auto-ensures the
// OS proxy only when it is set, so the flag gates the auto path. It is
// the remembered preference, not the live switch — the System card and
// the Quick Actions tile read that from the OS.
// No 'notifications' member: the toggle was dead (nothing reads the
// flag; Wails beta.20 has no desktop-notify API), so the row is gone.
// Only the fields this tab renders. windowStyle is a stored setting no
// control edits, and the log fields belong to the Console tab — a name in
// this union reads like a knob that exists here.
type StrKey = 'language' | 'theme' | 'xrayApiAddress' | 'testUrl' | 'geoIPDBPath' | 'inboundAddress' | 'dnsRemote' | 'dnsDirect' | 'fragmentMode' | 'fragmentLength' | 'fingerprintValue' | 'tunStack' | 'coreEngine'
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
  /** Rows the current settings make irrelevant — a TUN stack with TUN off
   *  configures an inbound that is not rendered. Hidden, not greyed: the
   *  value still applies the moment TUN comes back. */
  show?: (s: Settings) => boolean
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
// Why the last edit did not stick: either the backend rejected it
// (validation of ports, DNS, paths) or the field held text that is not a
// number. The row snaps back either way, so this says what happened.
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
    saveErr.value = ''
    try {
      await backend.setCloseToTray(next)
      store.settings[field] = next
    } catch (e) {
      // A console line is invisible here: the switch just flips back on
      // its own, which reads as the app ignoring the user.
      saveErr.value = String(e)
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

async function setNum(field: NumKey, el: HTMLInputElement) {
  // Nothing is invented out of text that is not a number: the previous
  // port stays in force, the box is rewritten to show it, and the reason
  // goes where the other refused edits surface. mixedPort is the only
  // numeric row, so the label is its own.
  const { value, error } = parsePortInput(
    el.value,
    'Mixed port',
    store.settings[field],
    el.validity.badInput,
  )
  saveErr.value = error
  if (error) {
    el.value = String(store.settings[field])
    return
  }
  if (value === store.settings[field]) return
  const prev = store.settings[field]
  store.settings[field] = value
  if (!(await persist())) {
    store.settings[field] = prev
    el.value = String(prev)
  }
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

// TUN group: the values the sing-box inbound reads (gui/tun_config.go
// holds the same pools, and the backend is what refuses a bad one).
// Kept in one place so the row copy, the option list and the bounds the
// engine enforces cannot each remember something different.
const TUN_MTU_MIN = 1280
const TUN_MTU_DEFAULT = 1350 // tunMTU in gui/singbox_manager.go
const TUN_MTU_MAX = 65535
const TUN_V4 = [
  '172.19.0.1/24',
  '172.18.0.1/30',
  '172.31.0.1/30',
  '172.20.0.1/30',
  '172.16.0.1/30',
  '192.168.100.1/30',
  '10.10.14.1/30',
  '10.1.0.1/30',
]
const TUN_V6 = [
  'fc00::172:19:0:1/126',
  'fc00::172:18:0:1/126',
  'fc00::172:31:0:1/126',
  'fc00::172:20:0:1/126',
  'fc00::172:16:0:1/126',
  'fc00::192:168:100:1/126',
  'fc00::10:10:14:1/126',
  'fc00::10:1:0:1/126',
]
const TUN_ICMP_OPTIONS = [
  { value: 'rule', label: 'Rule (default)' },
  { value: 'direct', label: 'Direct — ping outside the tunnel' },
  { value: 'reply', label: 'Reply — answer from the tunnel' },
  { value: 'unreachable', label: 'Unreachable' },
  { value: 'drop', label: 'Drop — no answer at all' },
]

// An address hand-edited into config.json is still the address in use,
// so it leads the list rather than vanishing from the dropdown.
function tunAddrOptions(pool: string[], current?: string) {
  const list = current && !pool.includes(current) ? [current, ...pool] : pool
  return list.map((v) => ({ value: v, label: v }))
}
const tunV4Options = computed(() => tunAddrOptions(TUN_V4, store.settings.tunAddress))
const tunV6Options = computed(() => tunAddrOptions(TUN_V6, store.settings.tunAddress6))

type TunBoolKey = 'tunAutoRoute' | 'tunStrictRoute' | 'tunEnableIPv6'

async function setTunBool(field: TunBoolKey) {
  // Auto route has three stored states and unset means yes, so the row
  // reads "off only when explicitly false" and writes the opposite of
  // what it showed. Writing the read itself would set true every time it
  // was already on, and the toggle could never be turned off.
  const prev = store.settings[field]
  const effective = field === 'tunAutoRoute' ? prev !== false : prev === true
  store.settings[field] = !effective
  if (!(await persist())) (store.settings as Record<string, unknown>)[field] = prev
}

type TunStrKey = 'tunAddress' | 'tunAddress6' | 'tunIcmp'

async function setTunStr(field: TunStrKey, value: string) {
  const prev = store.settings[field] ?? ''
  store.settings[field] = value
  if (!(await persist())) store.settings[field] = prev
}

// An MTU is not a port: 576 is a legal port and a size sing-box will not
// put on an interface, and 65535 is a legal MTU the port row refuses.
// So the bounds come from the engine's own and the message names MTU.
async function setTunMTU(el: HTMLInputElement) {
  const prev = Number(store.settings.tunMTU || TUN_MTU_DEFAULT)
  const v = Number(el.value)
  if (!Number.isInteger(v) || v < TUN_MTU_MIN || v > TUN_MTU_MAX) {
    saveErr.value = `MTU must be a whole number ${TUN_MTU_MIN}-${TUN_MTU_MAX}. It stays ${prev}.`
    el.value = String(prev)
    return
  }
  saveErr.value = ''
  if (v === Number(store.settings.tunMTU)) return
  store.settings.tunMTU = v
  if (!(await persist())) {
    store.settings.tunMTU = prev
    el.value = String(prev)
  }
}

// The excluded-routes box is one string; the engine wants a list. The
// backend refuses an entry that is not a CIDR, and a refusal must not
// cost the entries that were fine — so the box keeps what it had.
async function setTunExclude(raw: string) {
  const prev = store.settings.tunRouteExclude ?? []
  const next = raw
    .split(/[,\s]+/)
    .map((x) => x.trim())
    .filter((x) => x !== '')
  store.settings.tunRouteExclude = next
  if (!(await persist())) store.settings.tunRouteExclude = prev
}

// Launch-at-login note (e.g. non-Windows backend rejection).
const bootMsg = ref('')

// GeoIP extras (status + manual actions live outside the generic row kinds).
const profiles = useProfilesStore()
const connection = useConnectionStore()
const confirmBox = useConfirmStore()
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
  // The app's own dialog, not window.confirm: in WebView2 the browser box
  // is un-themed chrome, and this is the one button in the tab that
  // destroys data.
  const ok = await confirmBox.ask({
    title: 'Clear all countries?',
    body: 'Every detected flag is removed from all profiles. Re-detect them with Refresh; the profiles themselves are untouched.',
    confirmLabel: 'Clear',
    danger: true,
  })
  if (!ok) return
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
      { key: 'mixed', icon: 'globe', title: 'Mixed port', sub: 'Both HTTP and SOCKS share this one port on the sing-box cores; an Xray profile puts SOCKS on port + 1 · 1-65534 (applies on reconnect)', kind: 'number', field: 'mixedPort' },
      { key: 'bind', icon: 'wifi', title: 'Listen address', sub: 'Empty = loopback only · 0.0.0.0 shares on the LAN (applies on reconnect)', kind: 'text', field: 'inboundAddress' },
      { key: 'dnsremote', icon: 'cloud', title: 'Remote DNS', sub: 'DoH: https://1.1.1.1/dns-query  ·  DoT: tls://8.8.8.8  ·  IP: 8.8.8.8  ·  empty = system default', kind: 'text', field: 'dnsRemote' },
      { key: 'dnsdirect', icon: 'cloud', title: 'Direct DNS', sub: 'Lookups that bypass the proxy — same shapes, empty = system default', kind: 'text', field: 'dnsDirect' },
      { key: 'api', icon: 'bolt', title: 'Xray API', sub: 'StatsService endpoint, e.g. 127.0.0.1:10085', kind: 'text', field: 'xrayApiAddress' },
    ],
  },
  {
    key: 'connection', label: 'Connection', icon: 'wifi',
    head: 'Connection', headSub: 'Core engine, TUN, auto-connect and the probe',
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
      { key: 'tun', icon: 'shield', title: 'TUN mode', sub: 'Route all traffic through a virtual adapter — sing-box profiles only: an Xray or mihomo profile connects without it however this reads (applies on reconnect)', kind: 'toggle', field: 'tunEnabled' },
      // gVisor terminates TCP inside the core, so it carries traffic with
      // no OS involvement. System and Mixed hand TCP to Windows, which
      // sees the tunnel's own handshake as a NEW INBOUND connection to
      // the core binary: without a firewall allow for it, DNS through the
      // tunnel answers and every TCP dial times out. So the cheap stack is
      // opt-in, not the default.
      { key: 'tunstack', icon: 'tune', title: 'TUN stack', sub: 'gVisor (default) carries TCP in the core · System and Mixed hand TCP to Windows and need the core allowed through the firewall, or TCP times out (applies on reconnect)', kind: 'select', field: 'tunStack', show: (v) => !!v.tunEnabled, options: [{ value: 'gvisor', label: 'gVisor (default)' }, { value: 'system', label: 'System' }, { value: 'mixed', label: 'Mixed' }] },
      { key: 'auto', icon: 'power', title: 'Auto-connect', sub: 'Connect last profile on launch', kind: 'toggle', field: 'autoConnect' },
      { key: 'sysproxy', icon: 'globe', title: 'System proxy on connect', sub: 'Point the OS proxy at NekoGo in proxy mode — ignored in TUN mode (including the SNI spoof + Psiphon chain), where TUN carries all traffic', kind: 'toggle', field: 'systemProxy' },
      { key: 'spoof', icon: 'shield', title: 'SNI spoofing', sub: 'Run the spoof engine for SNI Spoofing profiles. Off = dial their own address with the normal transport (e.g. your own 127.0.0.1 tunnel).', kind: 'toggle', field: 'spoofEnabled' },
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
    head: 'Appearance', headSub: 'Theme and sidebar',
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
const visibleRows = computed(() =>
  (activeTab.value?.rows ?? []).filter((r) => !r.show || r.show(store.settings)),
)
</script>
