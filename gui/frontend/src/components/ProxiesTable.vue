<!-- Proxies list: v2rayN-style dense grid (Remarks / Protocol / Address /
     Network / Latency columns) with grouped collapsible sections.
     Click selects one config; Ctrl/Cmd+click toggles a config in the
     multi-selection, and Delete/Test/Export Selected act on the whole
     selection. Double-click or Enter connects, arrows/Home/End navigate,
     right-click per-row menu. Toolbar: search, + menu, filters, sort,
     subscription refresh, country refresh. All persistence via backend. -->
<template>
  <div class="nb-card nb-px">
    <div v-if="profiles.profiles.length === 0" class="nb-empty nb-px-empty">
      <Icon name="servers" :size="40" class="nb-empty-icon" />
      <p><strong>No profiles yet</strong></p>
      <p class="muted">Import servers from a link or add one manually.</p>
      <div class="toolbar" style="justify-content: center">
        <button class="nb-act nb-act--primary" @click="$emit('import-link')">
          <Icon name="download" :size="14" />Import from Link
        </button>
        <button class="nb-act" @click="$emit('add-manual')">
          <Icon name="plus" :size="14" />Add Manually
        </button>
      </div>
    </div>
    <template v-else>
      <!-- Main toolbar: higher sticky layer than the filter bar below it,
           so the +Add dropdown (z-index lives inside this toolbar's
           stacking context) is never painted over by the filters row. -->
      <div class="nb-px-toolbar nb-px-toolbar--main">
        <input v-model="search" class="grow nb-field" placeholder="Search name or address..." aria-label="Search proxies" />
        <span class="muted nb-px-count">{{ sortedFiltered.length }} / {{ profiles.profiles.length }}</span>
        <div class="nb-px-menu-wrap">
          <button class="nb-act nb-act--primary" title="Add" @click="menuOpen = !menuOpen"><Icon name="plus" :size="14" />Add</button>
          <div v-if="menuOpen" class="nb-px-menu">
            <button @click="menu('import')">Import from Link</button>
            <button @click="menu('manual')">Add Manually</button>
            <button @click="menu('file')">Import from File</button>
          </div>
        </div>
        <button class="nb-act" title="Create a new remark group" @click="openGroupDialog(null)">
          <Icon name="folder" :size="14" />New Group
        </button>
        <span class="nb-px-sep-v" />
        <button v-if="subs.length > 0" class="nb-act" :disabled="busy" title="Re-fetch all subscriptions" @click="updateSubs">
          <Icon name="refresh" :size="14" />Update All Subscriptions
        </button>
        <button class="nb-act" :disabled="busy" title="Detect countries for all profiles" @click="refreshCountries">
          <Icon name="globe" :size="14" />Refresh Countries
        </button>
        <button v-if="countriesRunning" class="nb-act nb-act--warn" title="Abort the country refresh" @click="cancelCountries">
          <Icon name="x" :size="14" />Cancel
        </button>
        <PickSelect v-model="sortKey" icon="list" title="Sort" aria-label="Sort proxies" neutral="name" :options="sortOptions" />
        <input ref="fileInput" type="file" accept=".txt,.json,.conf,.ini" style="display: none" @change="onFile" />
      </div>
      <div class="nb-px-toolbar nb-px-filters">
        <PickSelect v-model="fGroup" icon="layers" title="Filter by group" aria-label="Filter by group" :options="groupOptions" />
        <PickSelect v-model="fProto" icon="tag" title="Filter by type" aria-label="Filter by type" :options="protoOptions" />
        <PickSelect v-model="fStatus" icon="check" title="Filter by status" aria-label="Filter by status" :options="statusOptions" />
        <button v-if="fGroup || fProto || fStatus" class="nb-act nb-act--ghost" title="Clear filters" @click="clearFilters">
          <Icon name="x" :size="14" />Clear
        </button>
      </div>
      <div v-if="msg" class="muted nb-px-msg">{{ msg }}</div>
      <!-- BUG-020: per-profile refresh reasons (backend caps at 20) as an
           expandable list — counts alone are unactionable. -->
      <details v-if="countryErrors.length > 0" class="muted" style="margin-bottom: 8px">
        <summary>Refresh failures ({{ countryErrors.length }})</summary>
        <div v-for="(e, i) in countryErrors" :key="i">{{ e }}</div>
      </details>

      <div v-if="sortedFiltered.length > 0" class="nb-px-grid-head" aria-hidden="true">
        <span />
        <span>Remarks</span>
        <span>Protocol</span>
        <span>Address</span>
        <span>Network</span>
        <span>Latency</span>
        <span />
      </div>
      <div class="nb-px-grid" role="listbox" aria-multiselectable="true" aria-label="Proxy profiles">
        <template v-for="g in visibleGroups" :key="g.name">
        <div
          v-if="g.total > 1 || g.manual"
          class="nb-px-group"
          :class="{ 'nb-px-group--static': !foldingActive }"
          :role="foldingActive ? 'button' : undefined"
          :tabindex="foldingActive ? 0 : undefined"
          :aria-expanded="foldingActive ? !g.folded : undefined"
          :title="foldingActive ? (g.folded ? 'Expand group' : 'Collapse group') : 'Folding applies when nothing is searched or filtered'"
          @click="foldGroup(g.name)"
          @keydown.enter="foldGroup(g.name)"
          @keydown.space.prevent="foldGroup(g.name)"
        >
          <span class="nb-px-chev" :class="{ 'nb-px-chev--off': !foldingActive }">{{ g.folded ? '▸' : '▾' }}</span>
          <span class="nb-px-group-name">{{ g.name || 'Ungrouped' }}</span>
          <span class="nb-px-pill">{{ g.total }}</span>
          <button
            v-if="g.empty"
            class="nb-icon-btn nb-px-group-x"
            title="Remove this empty group"
            aria-label="Remove this empty group"
            @click.stop="removeGroup(g.name)"
          ><Icon name="x" :size="13" /></button>
        </div>
        <div v-if="g.total > 0 && ((g.total < 2 && !g.manual) || !g.folded)" class="nb-px-rows">
          <div
            v-for="r in g.rows"
            :key="r.id"
            :data-pid="r.id"
            class="nb-px-row"
            :class="{ selected: r.selected, active: r.active, failed: r.failedNoLat, 'is-menu-open': rowMenuFor === r.id }"
            role="option"
            :aria-selected="r.selected"
            :aria-label="r.ariaLabel"
            tabindex="0"
            data-rowmenu
            @click="onRowClick(r.profile, $event)"
            @dblclick="onRowDbl(r.profile, $event)"
            @keydown="onRowKeydown(r.profile, $event)"
            @contextmenu.prevent="openProxyCtx(r.profile, $event)"
          >
            <input type="checkbox" class="nb-px-check" :checked="r.selected" :aria-label="`Select ${r.name}`" @click.stop @change="toggleSelect(r.id)" />
            <span class="nb-px-remarks">
              <span class="nb-px-flag">{{ r.flag }}</span>
              <span class="nb-px-name" :title="r.lastTitle">{{ r.name }}</span>
              <span v-if="r.showGroup" class="nb-px-sub" :title="`Group: ${r.group}`">{{ r.group }}</span>
              <span v-if="r.active" class="nb-active-pill" title="Connected">active</span>
              <span v-if="r.spoof" class="nb-active-pill" title="SNI Spoofing core">spoof</span>
              <span v-if="r.mihomo" class="nb-active-pill" title="mihomo (Clash Meta) core">mihomo</span>
              <span v-if="r.aether" class="nb-active-pill" title="Aether engine">aether</span>
            </span>
            <span class="nb-px-proto">{{ r.proto }}</span>
            <span class="nb-px-addr" :title="r.addr">{{ r.addr }}</span>
            <span class="nb-px-net">{{ r.net }}</span>
            <span class="nb-px-lat">
              <span v-if="testing.has(r.id)" class="nb-spin" title="Testing…" />
              <span v-else-if="r.failedNoLat" class="nb-lat lat-bad" title="Last test failed">Failed</span>
              <span v-else-if="r.failed" class="nb-lat lat-failed-num" :title="r.failTitle">● {{ r.lat }} ms (Failed)</span>
              <span v-else-if="r.lat < 0" class="nb-lat nb-untested" title="Never tested">Untested</span>
              <span v-else class="nb-lat" :class="r.latClass" :title="r.latTitle">● {{ r.lat }} ms</span>
              <span v-if="r.showStaleDot" class="nb-stale-dot" title="Measured over 1 hour ago" />
            </span>
            <span class="nb-px-actions">
              <button
                class="nb-icon-btn"
                :class="{ 'is-on': r.active }"
                :title="r.active ? 'Connected (click to reconnect)' : 'Connect'"
                :disabled="busy || testing.has(r.id) || connecting"
                @click="connect(r.id)"
              ><Icon name="power" :size="15" /></button>
              <button class="nb-icon-btn" title="Test latency" :disabled="busy || testing.has(r.id)" @click="testOne(r.id)"><Icon name="bolt" :size="15" /></button>
              <button class="nb-icon-btn" title="Edit" @click="$emit('edit', r.profile)"><Icon name="pencil" :size="15" /></button>
              <button class="nb-icon-btn nb-danger" title="Delete" :disabled="busy || testing.has(r.id)" @click="removeOne(r.id)"><Icon name="trash" :size="15" /></button>
              <span class="nb-px-menu-wrap">
                <button
                  class="nb-icon-btn"
                  title="More actions"
                  aria-label="More actions"
                  :aria-expanded="rowMenuFor === r.id"
                  :disabled="busy || testing.has(r.id)"
                  @click="toggleRowMenu(r.id)"
                ><Icon name="more" :size="15" /></button>
                <span v-if="rowMenuFor === r.id" class="nb-px-menu nb-px-rowmenu">
                  <!-- Per-row country re-detect — the DetectCountry
                       binding had zero Vue callers before this, forcing
                       a full refresh for one flag. Expert profile tools
                       (speed/IP probes, duplicate, move-to-group,
                       edit-as-JSON) live in the Advanced world's
                       Profile tools panel. -->
                  <button class="nb-mi" :disabled="detecting.has(r.id)" @click="menuAct(r.id, () => redetectCountry(r.id))">
                    <Icon name="refresh" :size="15" /><span>Re-detect country</span>
                  </button>
                  <button class="nb-mi" @click="menuAct(r.id, () => copyShareLink(r.id))">
                    <Icon name="copy" :size="15" /><span>{{ copiedLinkFor === r.id ? 'Copied!' : 'Copy share link' }}</span>
                  </button>
                  <button class="nb-mi" @click="menuAct(r.id, () => openGroupDialog(r.profile))">
                    <Icon name="folder" :size="15" /><span>Move to group…</span>
                  </button>
                </span>
              </span>
            </span>
          </div>
        </div>
        </template>
      </div>

      <!-- Windowing sentinel: rows render in bounded batches, so a
           10k-profile list never puts 10k rows in the DOM at once. -->
      <div v-if="hasMoreRows" ref="moreSentinel" class="nb-px-sentinel" aria-hidden="true" />

      <div v-if="sortedFiltered.length === 0" class="muted nb-px-msg">No proxies match the current search or filters.</div>

      <div class="nb-px-bar">
        <span>Total: {{ profiles.profiles.length }}</span>
        <span v-if="hasMoreRows">Showing {{ renderedRowCount }} of {{ flatRows.length }}</span>
        <span>Selected: {{ selectedIds.size }}</span>
        <span class="spacer" />
        <button class="nb-act nb-act--danger" :disabled="selectedIds.size === 0 || busy" @click="deleteSelected"><Icon name="trash" :size="14" />Delete Selected</button>
        <button class="nb-act nb-act--danger" :disabled="busy || failedCount === 0" :title="`Delete the ${failedCount} profiles whose last test failed`" @click="deleteFailed"><Icon name="trash" :size="14" />Delete Failed ({{ failedCount }})</button>
        <button class="nb-act nb-act--primary" :disabled="selectedIds.size === 0 || busy" @click="testSelected"><Icon name="bolt" :size="14" />Test Selected</button>
        <button v-if="!testAllRunning" class="nb-act nb-act--accent" :disabled="busy || sortedFiltered.length === 0" @click="testAll"><Icon name="bolt" :size="14" />Test All</button>
        <button v-else class="nb-act nb-act--warn" title="Stop the Test All sweep" @click="cancelTestAll"><Icon name="x" :size="14" />Cancel ({{ testAllDone }}/{{ testAllTotal }})</button>
        <button class="nb-act" :disabled="selectedIds.size === 0" @click="exportSelected"><Icon name="upload" :size="14" />Export Selected</button>
      </div>
    </template>
    <div v-if="rowCtx.visible" ref="rowCtxMenu" class="nb-ctxmenu" :style="{ left: rowCtx.x + 'px', top: rowCtx.y + 'px' }">
      <button v-for="(it, i) in rowCtx.items" :key="i" class="nb-ctxmenu-item" @click="runCtxItem(it)">
        <Icon v-if="it.icon" :name="it.icon" :size="15" /><span>{{ it.label }}</span>
      </button>
    </div>
    <!-- Group editor: create a remark group, or move one profile into one.
         Teleported to the body because the card is a size container, which
         would otherwise become the containing block for the fixed overlay. -->
    <Teleport to="body">
      <div v-if="groupDialog.open" class="nb-modal-overlay" @click.self="closeGroupDialog" @keydown.esc.prevent="closeGroupDialog">
        <div class="nb-modal" role="dialog" aria-modal="true" :aria-label="groupDialog.profile ? 'Move to group' : 'New group'">
          <div class="nb-modal-head">
            <span class="nb-section-title">{{ groupDialog.profile ? 'Move to group' : 'New group' }}</span>
            <button class="nb-icon-btn" title="Close" @click="closeGroupDialog"><Icon name="x" :size="18" /></button>
          </div>
          <p class="muted nb-modal-hint">
            {{ groupDialog.profile
              ? `Group remark for "${groupDialog.profile.name}". Leave empty to move it to Ungrouped.`
              : 'Name a remark group. It appears in the list like feed groups, even before profiles are moved into it.' }}
          </p>
          <p class="muted nb-modal-hint">Tip: focus a row and hold Alt + ↑ / ↓ to move it into the group above or below.</p>
          <label class="nb-sub-field">
            <span class="muted">Group remark</span>
            <input
              ref="groupInput"
              v-model="groupName"
              list="nb-group-names"
              placeholder="e.g. US nodes"
              @keydown.enter.prevent="submitGroupDialog"
            />
          </label>
          <datalist id="nb-group-names">
            <option v-for="g in groupNames" :key="g" :value="g" />
          </datalist>
          <div v-if="groupErr" class="nb-conn-err">{{ groupErr }}</div>
          <div class="nb-modal-foot">
            <button class="nb-act nb-act--ghost" @click="closeGroupDialog"><Icon name="x" :size="14" />Cancel</button>
            <button
              class="nb-act nb-act--primary"
              :disabled="groupSaving || (!groupDialog.profile && !groupName.trim())"
              @click="submitGroupDialog"
            >
              <Icon name="check" :size="14" />{{ groupSaving ? 'Saving…' : groupDialog.profile ? 'Move' : 'Create' }}
            </button>
          </div>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import Icon from './Icon.vue'
import PickSelect, { type PickOption } from './PickSelect.vue'
import { useProfilesStore } from '../stores/profiles'
import { useConnectionStore } from '../stores/connection'
import { useSettingsStore } from '../stores/settings'
import { useSubscriptionsStore } from '../stores/subscriptions'
import { reloadProfilesAndSubs } from '../stores/sync'
import { backend } from '../api'
import { copyText } from '../clipboard'
import { flagEmoji, isStale, latencyClass } from '../data/servers'
import type { Profile } from '../types'

const emit = defineEmits<{
  (e: 'add-manual'): void
  (e: 'edit', p: Profile): void
  (e: 'import-link'): void
}>()

const profiles = useProfilesStore()
const connection = useConnectionStore()
const settings = useSettingsStore()

const search = ref('')
const sortKey = ref('name')
const sortOptions: PickOption[] = [
  { value: 'name', label: 'Name' },
  { value: 'latency', label: 'Latency' },
  { value: 'last', label: 'Last used' },
]
// Filter bar: group / protocol / status predicates over the table.
const fGroup = ref('')
const fProto = ref('')
const fStatus = ref('')

const protoNames = computed(() => {
  const m = new Map<string, string>()
  for (const p of profiles.profiles) m.set(p.protocol.toLowerCase(), p.protocol.toUpperCase())
  return [...m.entries()]
    .sort((a, b) => a[1].localeCompare(b[1]))
    .map(([value, label]) => ({ value, label }))
})

const protoOptions = computed<PickOption[]>(() => [
  { value: '', label: 'All types' },
  ...protoNames.value,
])

const statusOptions: PickOption[] = [
  { value: '', label: 'All statuses' },
  { value: 'active', label: 'Active' },
  { value: 'failed', label: 'Failed' },
  { value: 'untested', label: 'Untested' },
  { value: 'stale', label: 'Stale' },
]

function clearFilters() {
  fGroup.value = ''
  fProto.value = ''
  fStatus.value = ''
}
// Multi-select: a plain click selects one row, Ctrl/Cmd+click toggles a
// row, and the checkbox column toggles without clearing the rest. This
// set is what Delete/Test/Export Selected act on.
const selectedIds = ref<Set<string>>(new Set())
// Collapsed remark groups (subscription feeds) persist in Settings so
// both a tab switch (this component unmounts) and a restart keep the
// list folded the way it was left.
const collapsed = ref<Set<string>>(new Set(settings.settings.collapsedProxyGroups ?? []))
const menuOpen = ref(false)
const busy = ref(false)
// Connect state belongs to the connection store: a row must not offer a
// second Connect while one is already in flight here or from Home.
// pendingConnect covers the selection IPC that runs BEFORE the store's
// busy flag is set — without it the row stayed clickable for that whole
// window and a second click re-persisted a different selection.
const pendingConnect = ref(false)
const connecting = computed(() => pendingConnect.value || connection.busy || connection.connectBusy)
const msg = ref('')
// Throne-style Test All sweep state: bounded-concurrency workers with
// live N/M progress + Cancel (see testAll/cancelTestAll).
const testAllCancelled = ref(false)
const testAllRunning = ref(false)
const testAllDone = ref(0)
const testAllTotal = ref(0)
const subsStore = useSubscriptionsStore()
// Shared with SimpleShell subscription view (sync fix).
const subs = computed(() => subsStore.feeds)
const fileInput = ref<HTMLInputElement | null>(null)
const testing = ref<Set<string>>(new Set())
const detecting = ref<Set<string>>(new Set())
const countryErrors = ref<string[]>([])
// Overflow menu: one open at a time (rowMenuFor).
const rowMenuFor = ref('')

// BUG: both dropdowns were click-toggle only, so once opened they
// floated over the list until toggled again (the row menu sits at
// z-index 50 above the cards). Any click outside a menu wrap closes
// both; items inside close themselves as before.
function onDocClick(e: MouseEvent) {
  if (rowCtx.visible) {
    const t = e.target as HTMLElement | null
    if (!t || !t.closest('.nb-ctxmenu')) rowCtx.visible = false
  }
  if (!menuOpen.value && !rowMenuFor.value) return
  const t = e.target as HTMLElement | null
  if (t && t.closest('.nb-px-menu-wrap')) return
  menuOpen.value = false
  rowMenuFor.value = ''
}
onMounted(() => document.addEventListener('click', onDocClick))
onUnmounted(() => document.removeEventListener('click', onDocClick))
// Keep the active-core pill fresh while the table is mounted.
onMounted(() => connection.acquire())
onUnmounted(() => connection.release())

/** The row carrying the live connection, if any. */
function isActive(id: string): boolean {
  return connection.status.running && connection.status.profileId === id
}

function toggleRowMenu(id: string) {
  rowMenuFor.value = rowMenuFor.value === id ? '' : id
}

/** Close the overflow menu, then run the picked action. */
async function menuAct(id: string, fn: () => void | Promise<void>) {
  if (rowMenuFor.value === id) rowMenuFor.value = ''
  await fn()
}

// User-created remark groups live in Settings: unlike feed groups they
// have no Profile.Group to be derived from until a row is moved in.
const manualGroups = computed(() => settings.settings.proxyGroups ?? [])

const groupNames = computed(() =>
  [...new Set([
    ...profiles.profiles.map((p) => p.group).filter((g): g is string => !!g),
    ...manualGroups.value,
  ])].sort(),
)

const groupOptions = computed<PickOption[]>(() => [
  { value: '', label: 'All groups' },
  { value: '__none__', label: 'No group' },
  ...groupNames.value.map((g) => ({ value: g, label: g })),
])

onMounted(async () => {
  await subsStore.load()
})

function latOf(id: string): number {
  return profiles.latencies[id] ?? -1
}

function staleOf(id: string): boolean {
  return isStale(profiles.testedAt[id])
}

function markTesting(id: string, on: boolean) {
  const s = new Set(testing.value)
  if (on) s.add(id)
  else s.delete(id)
  testing.value = s
}

/** Tooltip for a failed row: error message + relative failure time. */
function failTitle(id: string): string {
  const msg = profiles.lastError[id]
  if (!msg) return ''
  const at = profiles.lastErrorAt[id]
  return at ? `${msg} (failed ${relTime(at)})` : msg
}

function relTime(iso?: string): string {  if (!iso) return 'never'
  const t = new Date(iso).getTime()
  if (Number.isNaN(t)) return 'never'
  const s = Math.floor((Date.now() - t) / 1000)
  if (s < 60) return 'just now'
  if (s < 3600) return `${Math.floor(s / 60)}m ago`
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`
  if (s < 30 * 86400) return `${Math.floor(s / 86400)}d ago`
  return new Date(t).toLocaleDateString()
}

// ---- Windowed rendering ----
const BATCH_SIZE = 200
const batchCount = ref(1)
const moreSentinel = ref<HTMLElement | null>(null)

// The sentinel is v-if, so it is destroyed and re-created every time the
// list crosses the batch limit again (search cleared, group expanded, a big
// subscription imported while this tab is open). Observing it once at mount
// left every later element unwatched, so the rows past the first batch
// could never load — re-observe each new element instead.
let sentinelIO: IntersectionObserver | null = null
watch(moreSentinel, (el) => {
  if (!sentinelIO) return
  sentinelIO.disconnect()
  if (el) sentinelIO.observe(el)
})

// A subscription refresh or an import can remove profiles behind this
// view's back; drop the ids that no longer exist so the footer count and
// the selection actions never act on ghosts.
watch(
  () => profiles.profiles.map((p) => p.id).join('\n'),
  () => {
    if (selectedIds.value.size === 0) return
    const live = new Set(profiles.profiles.map((p) => p.id))
    const next = new Set([...selectedIds.value].filter((id) => live.has(id)))
    if (next.size !== selectedIds.value.size) selectedIds.value = next
  },
)
onMounted(() => {
  sentinelIO = new IntersectionObserver(
    (entries) => {
      for (const e of entries) {
        if (e.isIntersecting && hasMoreRows.value) {
          batchCount.value += 1
        }
      }
    },
    { threshold: 0.1, rootMargin: '200px' },
  )
  if (moreSentinel.value) sentinelIO.observe(moreSentinel.value)
})
onUnmounted(() => {
  sentinelIO?.disconnect()
  sentinelIO = null
})

interface PxRow {
  id: string
  profile: Profile
  selected: boolean
  active: boolean
  failedNoLat: boolean
  failed: boolean
  showStaleDot: boolean
  showGroup: boolean
  spoof: boolean
  /** mihomo (Clash Meta) is a whole core, not a transport: the profile
   *  runs mihomo.exe alone and its own controller serves the stats. */
  mihomo: boolean
  /** Aether is a protocol, not a core: the profile runs aether.exe next to
   *  sing-box and dials its internal SOCKS listener. */
  aether: boolean
  proto: string
  addr: string
  net: string
  lat: number
  latClass: string
  latTitle: string
  failTitle: string
  ariaLabel: string
  flag: string
  name: string
  lastTitle: string
  group: string
}

/** Precompute all display-derived properties for one row.
 *  Flag emoji, latency class, stale state, group visibility, etc. are
 *  computed once per row instead of on every render pass. */
function makeRow(p: Profile, isSingleInGroup: boolean): PxRow {
  const lat = latOf(p.id)
  const failed = !!profiles.lastError[p.id]
  const failedNoLat = failed && lat < 0
  const stale = staleOf(p.id)
  const selected = selectedIds.value.has(p.id)
  const active = isActive(p.id)
  const spoof = p.core === 2
  const mihomo = p.core === 3
  const aether = p.protocol.toLowerCase() === 'aether'
  const proto = p.protocol.toUpperCase()
  const addr = `${p.address}:${p.port}`
  const net = p.network ? (p.tls ? `${p.network} · tls` : p.network) : 'tcp'
  const latClass = stale ? 'lat-stale' : latencyClass(lat)
  const latTitle = stale ? 'Measured over 1 hour ago' : 'Measured latency'
  const flag = flagEmoji(p.countryCode ?? '')
  const lastTitle = `Last connected: ${relTime(p.lastConnectedAt)}`
  const ariaLabel = `${p.name}, ${p.address}:${p.port}`
  const failTitle = failed && profiles.lastErrorAt[p.id]
    ? `${profiles.lastError[p.id]} (failed ${relTime(profiles.lastErrorAt[p.id])})`
    : failed ? profiles.lastError[p.id] : ''
  const showStaleDot = !testing.value.has(p.id) && stale && lat >= 0
  return {
    id: p.id,
    profile: p,
    selected,
    active,
    failedNoLat,
    failed,
    showStaleDot,
    showGroup: isSingleInGroup,
    spoof,
    mihomo,
    aether,
    proto,
    addr,
    net,
    lat,
    latClass,
    latTitle,
    failTitle,
    ariaLabel,
    flag,
    name: p.name,
    lastTitle,
    group: p.group ?? '',
  }
}

// Raw filtered+sorted list (same search/filter/sort as before).
const sortedFiltered = computed(() => {
  const q = search.value.trim().toLowerCase()
  let rows = profiles.profiles.filter((p) => {
    if (q && !(p.name.toLowerCase().includes(q) || p.address.toLowerCase().includes(q))) return false
    if (fGroup.value) {
      if (fGroup.value === '__none__') {
        if (p.group) return false
      } else if ((p.group ?? '') !== fGroup.value) return false
    }
    if (fProto.value && p.protocol.toLowerCase() !== fProto.value) return false
    if (fStatus.value === 'active' && !isActive(p.id)) return false
    if (fStatus.value === 'failed' && !profiles.lastError[p.id]) return false
    if (fStatus.value === 'untested' && !(latOf(p.id) < 0 && !profiles.lastError[p.id])) return false
    if (fStatus.value === 'stale' && !staleOf(p.id)) return false
    return true
  })
  rows = [...rows].sort((a, b) => {
    if (sortKey.value === 'latency') return (profiles.latencies[a.id] ?? 1e9) - (profiles.latencies[b.id] ?? 1e9)
    if (sortKey.value === 'last')
      return (b.lastConnectedAt ?? '').localeCompare(a.lastConnectedAt ?? '')
    return a.name.localeCompare(b.name)
  })
  return rows
})

// Groups in display order: the single source for both windowed rows and
// keyboard navigation. Collapse is applied to rows, never to the group
// itself, so a collapsed feed keeps its header (it must not look deleted).
const groupEntries = computed<[string, Profile[]][]>(() => {
  const map = new Map<string, Profile[]>()
  for (const p of sortedFiltered.value) {
    const g = p.group ?? ''
    if (!map.has(g)) map.set(g, [])
    map.get(g)!.push(p)
  }
  // Keep manually created groups visible even before a profile lands in
  // them, unless a search/type/status filter is narrowing the list.
  if (!search.value.trim() && !fProto.value && !fStatus.value) {
    for (const g of manualGroups.value) {
      if (fGroup.value && fGroup.value !== g) continue
      if (!map.has(g)) map.set(g, [])
    }
  }
  return [...map.entries()]
    .sort(([a], [b]) => (a === '' ? 1 : b === '' ? -1 : a.localeCompare(b)))
})

// Folding is a preference for the full list. While a search or a filter
// narrows it, folded groups would hide the very matches the user asked for
// — pills would count rows that never render, and the list would look empty
// — so collapse is bypassed until the narrowing clears.
const foldingActive = computed(
  () => !search.value.trim() && !fGroup.value && !fProto.value && !fStatus.value,
)
const noFold = new Set<string>()
const foldedNow = computed(() => (foldingActive.value ? collapsed.value : noFold))

// All currently expanded rows in display order (keyboard navigation).
const flatRows = computed<PxRow[]>(() => {
  const out: PxRow[] = []
  for (const [name, rows] of groupEntries.value) {
    // Manual groups always carry a header (even for one row), so their
    // collapse state applies at any count; feed groups only fold at 2+.
    const manual = manualGroups.value.includes(name)
    if ((rows.length >= 2 || manual) && foldedNow.value.has(name)) continue
    for (const p of rows) out.push(makeRow(p, rows.length < 2 && !manual))
  }
  return out
})

// Windowed groups for the template: a folded group contributes its
// header with zero rows; expanded groups fill the row budget in order.
const visibleGroups = computed(() => {
  const limit = batchCount.value * BATCH_SIZE
  let used = 0
  const out: {
    name: string
    total: number
    rows: PxRow[]
    empty: boolean
    manual: boolean
    folded: boolean
  }[] = []
  for (const [name, rows] of groupEntries.value) {
    const manual = manualGroups.value.includes(name)
    const empty = rows.length === 0 && manual
    const folded = (rows.length >= 2 || manual) && foldedNow.value.has(name)
    if (folded) {
      out.push({ name, total: rows.length, rows: [], empty, manual, folded })
      continue
    }
    if (used >= limit) break
    const take = rows.slice(0, limit - used)
    out.push({
      name,
      total: rows.length,
      rows: take.map((p) => makeRow(p, rows.length < 2 && !manual)),
      empty,
      manual,
      folded,
    })
    used += take.length
    if (take.length < rows.length) break
  }
  return out
})

const renderedRowCount = computed(() =>
  visibleGroups.value.reduce((n, g) => n + g.rows.length, 0),
)
const totalShownRows = computed(() =>
  groupEntries.value.reduce((n, [name, rows]) => {
    const manual = manualGroups.value.includes(name)
    const folded = (rows.length >= 2 || manual) && foldedNow.value.has(name)
    return n + (folded ? 0 : rows.length)
  }, 0),
)
const hasMoreRows = computed(() => renderedRowCount.value < totalShownRows.value)

function foldGroup(name: string) {
  if (!foldingActive.value) return
  toggleGroup(name)
}

function toggleGroup(name: string) {
  const c = new Set(collapsed.value)
  if (c.has(name)) c.delete(name)
  else c.add(name)
  collapsed.value = c
  // Write through to Settings (a failed save keeps the in-memory state;
  // this is view chrome, nothing depends on the write).
  settings.settings.collapsedProxyGroups = [...c]
  void settings.save().catch(() => {})
  // Reset batching when collapse changes (row count changed).
  batchCount.value = 1
}

// Group editor: one modal both creates a remark group (profile = null)
// and moves a single profile into a group. window.prompt is avoided so
// the flow looks like the rest of the app and works in the WebView.
const groupDialog = reactive<{ open: boolean; profile: Profile | null }>({ open: false, profile: null })
const groupName = ref('')
const groupErr = ref('')
const groupSaving = ref(false)
const groupInput = ref<HTMLInputElement | null>(null)

function openGroupDialog(profile: Profile | null) {
  groupDialog.profile = profile
  groupName.value = profile?.group ?? ''
  groupErr.value = ''
  groupDialog.open = true
  void nextTick(() => {
    groupInput.value?.focus()
    groupInput.value?.select()
  })
}

function closeGroupDialog() {
  groupDialog.open = false
  groupDialog.profile = null
  groupErr.value = ''
}

/** Persist a newly named group; it shows up like feed groups even while
 *  empty because the name lives in Settings, not on a profile. */
async function createGroup(name: string) {
  const prev = manualGroups.value
  settings.settings.proxyGroups = [...prev, name]
  // A stale collapse entry with this name would hide rows moved in later.
  uncollapse(name)
  if (backend.available()) {
    try {
      await settings.save()
    } catch (e) {
      settings.settings.proxyGroups = prev
      throw e
    }
  }
  msg.value = `Group "${name}" added. Move profiles in from a row's ⋯ menu.`
}

async function submitGroupDialog() {
  if (groupSaving.value) return
  const profile = groupDialog.profile
  const typed = groupName.value.trim()
  // "Ungrouped" is the display label for an empty group value, so typing
  // it is the same as leaving the box empty.
  const name = typed === 'Ungrouped' ? '' : typed
  groupErr.value = ''
  if (!profile) {
    if (!name) {
      groupErr.value = 'Enter a group name.'
      return
    }
    if (groupNames.value.includes(name)) {
      groupErr.value = `A group named "${name}" already exists.`
      return
    }
  } else if (name === (profile.group ?? '')) {
    closeGroupDialog()
    return
  }
  groupSaving.value = true
  try {
    if (!profile) {
      await createGroup(name)
    } else {
      // Re-file the profile via saveProfile; a name typed here becomes a
      // persistent group so it survives the profile moving out later.
      // Capture "new" before the save: afterwards the profile itself
      // already makes the name part of groupNames.
      const isNewGroup = !!name && !groupNames.value.includes(name)
      // A folded destination — or a stale fold entry under a name that is
      // only now coming back — would swallow the moved row, so reveal it.
      const revealed = uncollapse(name)
      await profiles.save({ ...profile, group: name || undefined })
      if (revealed) batchCount.value = 1
      if (isNewGroup) {
        const prev = manualGroups.value
        settings.settings.proxyGroups = [...prev, name]
        if (backend.available()) {
          try {
            await settings.save()
          } catch {
            settings.settings.proxyGroups = prev
          }
        }
      } else if (revealed && backend.available()) {
        void settings.save().catch(() => {})
      }
      msg.value = name ? `Moved "${profile.name}" to "${name}".` : `Moved "${profile.name}" to Ungrouped.`
    }
    closeGroupDialog()
  } catch (e) {
    groupErr.value = String(e)
  } finally {
    groupSaving.value = false
  }
}

/** Remove a manual group; the button is only offered for empty ones. */
async function removeGroup(name: string) {
  if (!confirm(`Remove the empty group "${name}"?`)) return
  const prev = manualGroups.value
  settings.settings.proxyGroups = prev.filter((g) => g !== name)
  uncollapse(name)
  if (!backend.available()) return
  try {
    await settings.save()
  } catch (e) {
    settings.settings.proxyGroups = prev
    msg.value = String(e)
  }
}

/** Un-hide a group and mirror the change into the persisted settings
 *  field; the caller's own save() carries it to disk. Returns whether the
 *  collapse set actually changed. */
function uncollapse(name: string): boolean {
  if (!collapsed.value.has(name)) return false
  const c = new Set(collapsed.value)
  c.delete(name)
  collapsed.value = c
  settings.settings.collapsedProxyGroups = [...c]
  return true
}

// Alt+ArrowUp/Down walks a config through the group list instead of
// through the rows, so a keyboard-only re-filing never needs the ⋯ menu.
const groupMoving = ref(false)

async function moveAcrossGroups(p: Profile, delta: number) {
  if (groupMoving.value) return
  const names = groupEntries.value.map(([name]) => name)
  const idx = names.indexOf(p.group ?? '')
  if (idx < 0) return
  const to = idx + delta
  if (to < 0 || to >= names.length) {
    msg.value =
      names.length < 2
        ? 'No other group to move between (a filter may be hiding groups).'
        : `"${p.name}" is already in the ${to < 0 ? 'first' : 'last'} group.`
    return
  }
  const target = names[to]
  groupMoving.value = true
  try {
    // A collapsed destination would swallow the moved row, so reveal it.
    const revealed = uncollapse(target)
    await profiles.save({ ...p, group: target || undefined })
    if (revealed) {
      // Revealing a group adds rows to the windowed budget, as in toggleGroup.
      batchCount.value = 1
      if (backend.available()) void settings.save().catch(() => {})
    }
    msg.value = target
      ? `Moved "${p.name}" to "${target}".`
      : `Moved "${p.name}" to Ungrouped.`
    selectOne(p.id)
    await nextTick()
    // Rows live inside their group section, so the DOM node is recreated
    // by the move; without this the keyboard focus drops to the body.
    rowEl(p.id)?.focus()
  } catch (e) {
    msg.value = String(e)
  } finally {
    groupMoving.value = false
  }
}

function selectOne(id: string) {
  selectedIds.value = new Set([id])
  cursor.value = id
}

function toggleSelect(id: string) {
  const next = new Set(selectedIds.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  selectedIds.value = next
  cursor.value = id
}

function clearSelection() {
  selectedIds.value = new Set()
}

// --- Selection: a plain click (or a keyboard move) selects exactly one
// config; Ctrl/Cmd+click toggles a config in the selection, so several
// rows can be tested/deleted/exported together. Arrows/Home/End navigate,
// Alt+Arrow re-files the focused row into the previous/next group, Enter
// (or double-click) connects.
const cursor = ref('')

function rowEl(id: string): HTMLElement | null {
  return document.querySelector<HTMLElement>(`.nb-px-row[data-pid="${CSS.escape(id)}"]`)
}

function focusRowAt(idx: number) {
  const rows = flatRows.value
  if (idx < 0 || idx >= rows.length) return
  const id = rows[idx].id
  selectOne(id)
  void nextTick(() => rowEl(id)?.focus())
}

function moveCursor(fromId: string, delta: number) {
  const ids = flatRows.value.map((r) => r.id)
  const idx = ids.indexOf(fromId)
  if (idx < 0) return
  focusRowAt(Math.min(Math.max(idx + delta, 0), ids.length - 1))
}

function onRowClick(p: Profile, e: MouseEvent) {
  const t = e.target as HTMLElement | null
  if (t?.closest('button,input,select,a,textarea,.nb-px-menu-wrap')) return
  // Ctrl/Cmd+click adds or removes this row; a plain click selects just
  // this row (keyboard navigation behaves the same way).
  if (e.ctrlKey || e.metaKey) toggleSelect(p.id)
  else selectOne(p.id)
}

function onRowDbl(p: Profile, e: MouseEvent) {
  const t = e.target as HTMLElement | null
  if (t?.closest('button,input,select,a,textarea,.nb-px-menu-wrap')) return
  void connect(p.id)
}

function onRowKeydown(p: Profile, e: KeyboardEvent) {
  const t = e.target as HTMLElement | null
  if (t?.closest('button,a,select,textarea')) return
  if (e.altKey && (e.key === 'ArrowDown' || e.key === 'ArrowUp')) {
    e.preventDefault()
    void moveAcrossGroups(p, e.key === 'ArrowDown' ? 1 : -1)
  } else if (e.key === 'ArrowDown') {
    e.preventDefault()
    moveCursor(p.id, 1)
  } else if (e.key === 'ArrowUp') {
    e.preventDefault()
    moveCursor(p.id, -1)
  } else if (e.key === 'Home') {
    e.preventDefault()
    focusRowAt(0)
  } else if (e.key === 'End') {
    e.preventDefault()
    focusRowAt(flatRows.value.length - 1)
  } else if (e.key === 'Enter' && t?.tagName !== 'INPUT') {
    e.preventDefault()
    void connect(p.id)
  }
}

function menu(which: 'import' | 'manual' | 'file') {
  menuOpen.value = false
  if (which === 'import') emit('import-link')
  else if (which === 'manual') emit('add-manual')
  else fileInput.value?.click()
}

async function connect(id: string) {
  // A second click while a connect is in flight used to start a second
  // backend Connect: this row button only knew the table's local busy flag,
  // not the connection store's (ConnectionCard and Sidebar already gate on
  // both). Last-write-wins left an uncertain connected state.
  if (connecting.value) return
  pendingConnect.value = true
  msg.value = ''
  try {
    // Same BUG-004 pattern as ServersPanel: persist selection first, and
    // do not connect to a profile the store could not record.
    try {
      await settings.setActiveProfileId(id)
    } catch (e) {
      msg.value = String(e)
      return
    }
    if (!(await connection.connect(id))) {
      // The store reports the backend failure through connection.error.
      // Ignoring the result left the row silent, so a refused connect
      // looked exactly like a successful one.
      msg.value = connection.error || 'Another connect is in progress.'
    }
    await profiles.load()
  } finally {
    pendingConnect.value = false
  }
}

async function testOne(id: string) {
  markTesting(id, true)
  msg.value = ''
  try {
    await profiles.test(id)
  } catch (e) {
    msg.value = String(e)
  } finally {
    markTesting(id, false)
  }
}

// Throne parity: copy one profile's share link (Throne's "Copy
// links"). Raw-JSON or exotic protocols report why, not a lossy
// link. Feedback is the row label itself (2s "Copied!").
const copiedLinkFor = ref('')
let copiedLinkTimer = 0
async function copyShareLink(id: string) {
  try {
    const link = await backend.buildShareLink(id)
    if (!(await copyText(link))) {
      msg.value = 'Copy failed — clipboard unavailable.'
      return
    }
    copiedLinkFor.value = id
    window.clearTimeout(copiedLinkTimer)
    copiedLinkTimer = window.setTimeout(() => {
      if (copiedLinkFor.value === id) copiedLinkFor.value = ''
    }, 2000)
  } catch (e) {
    msg.value = String(e)
  }
}

// BUG-013: single-profile country re-detect wired to the
// backend.detectCountry binding (previously dead surface).
async function redetectCountry(id: string) {
  if (detecting.value.has(id)) return
  detecting.value = new Set(detecting.value).add(id)
  try {
    await backend.detectCountry(id)
    await profiles.load()
  } catch (e) {
    msg.value = String(e)
  } finally {
    const s = new Set(detecting.value)
    s.delete(id)
    detecting.value = s
  }
}

async function removeOne(id: string) {
  if (!confirm('Delete this profile?')) return
  try {
    await profiles.remove(id)
    if (selectedIds.value.has(id)) {
      const next = new Set(selectedIds.value)
      next.delete(id)
      selectedIds.value = next
    }
  } catch (e) {
    msg.value = String(e)
  }
}

// --- Right-click row menu (the ⋯ button content at the cursor).
// The global edit menu yields data-rowmenu zones to these handlers.
interface RowCtxItem {
  label: string
  icon?: 'refresh' | 'copy' | 'power' | 'trash' | 'folder'
  run: () => void | Promise<void>
}
const rowCtx = reactive({ visible: false, x: 0, y: 0, items: [] as RowCtxItem[] })
const rowCtxMenu = ref<HTMLElement | null>(null)
function openCtx(e: MouseEvent, items: RowCtxItem[]): void {
  rowMenuFor.value = ''
  rowCtx.items = items
  rowCtx.visible = true
  const px = e.clientX
  const py = e.clientY
  void nextTick(() => {
    const w = rowCtxMenu.value?.offsetWidth ?? 200
    const h = rowCtxMenu.value?.offsetHeight ?? 120
    rowCtx.x = Math.max(4, Math.min(px, window.innerWidth - w - 4))
    rowCtx.y = Math.max(4, Math.min(py, window.innerHeight - h - 4))
  })
}
function openProxyCtx(p: Profile, e: MouseEvent): void {
  openCtx(e, [
    { label: 'Connect', icon: 'power', run: () => connect(p.id) },
    { label: 'Move to group…', icon: 'folder', run: () => openGroupDialog(p) },
    { label: 'Re-detect country', icon: 'refresh', run: () => redetectCountry(p.id) },
    { label: 'Copy share link', icon: 'copy', run: () => copyShareLink(p.id) },
  ])
}
async function runCtxItem(it: RowCtxItem): Promise<void> {
  rowCtx.visible = false
  await it.run()
}

async function deleteSelected() {
  const ids = [...selectedIds.value]
  if (ids.length === 0) return
  if (!confirm(ids.length === 1 ? 'Delete the selected profile?' : `Delete ${ids.length} selected profiles?`)) return
  busy.value = true
  const failures: string[] = []
  let deleted = 0
  try {
    for (const id of ids) {
      try {
        await profiles.remove(id)
        deleted++
      } catch (e) {
        failures.push(`${id}: ${e}`)
      }
    }
    clearSelection()
    msg.value =
      failures.length === 0
        ? `Deleted ${deleted} profile${deleted === 1 ? '' : 's'}.`
        : `Deleted ${deleted}, failed ${failures.length}: ${failures.join('; ')}`
  } finally {
    busy.value = false
  }
}

// Throne parity ("Remove Unavailable"): delete every profile whose
// last test failed (the connected profile refuses and is reported,
// not fatal).
const failedCount = computed(
  () => profiles.profiles.filter((p) => profiles.lastError[p.id]).length,
)

async function deleteFailed() {
  const ids = profiles.profiles.filter((p) => profiles.lastError[p.id]).map((p) => p.id)
  if (ids.length === 0) return
  if (!confirm(`Delete ${ids.length} failed profiles?`)) return
  busy.value = true
  const failures: string[] = []
  let deleted = 0
  try {
    for (const id of ids) {
      try {
        await profiles.remove(id)
        deleted++
        if (selectedIds.value.has(id)) {
          const next = new Set(selectedIds.value)
          next.delete(id)
          selectedIds.value = next
        }
      } catch (e) {
        failures.push(`${id}: ${e}`)
      }
    }
    msg.value =
      failures.length === 0
        ? `Deleted ${deleted} failed profiles.`
        : `Deleted ${deleted}, failed ${failures.length}: ${failures.join('; ')}`
  } finally {
    busy.value = false
  }
}

async function testSelected() {
  const ids = [...selectedIds.value]
  if (ids.length === 0) return
  busy.value = true
  msg.value = ids.length === 1 ? '' : `Testing 0/${ids.length}…`
  let done = 0
  let idx = 0
  // Same bounded sweep as Test All, over the selection instead of the
  // filtered list: 5 concurrent probes, per-row spinner, live progress.
  async function worker() {
    while (true) {
      const i = idx++
      if (i >= ids.length) return
      const id = ids[i]
      markTesting(id, true)
      try {
        await profiles.test(id)
      } catch {
        /* per-row failure is non-fatal, shown via Failed badge */
      } finally {
        markTesting(id, false)
      }
      done++
      if (ids.length > 1) msg.value = `Testing ${done}/${ids.length}…`
    }
  }
  try {
    await Promise.all(Array.from({ length: Math.min(5, ids.length) }, () => worker()))
    msg.value = ids.length === 1 ? 'test done' : `test done (${ids.length})`
  } finally {
    busy.value = false
  }
}

async function testAll() {
  busy.value = true
  msg.value = ''
  testAllCancelled.value = false
  testAllRunning.value = true
  // Throne-style batched sweep (probe/runBatch): bounded concurrency
  // (default 5, like Throne's MaxConcurrentTests scale for ephemeral
  // cores) with live N/M progress and Cancel. Each profile still
  // probes through its own ephemeral core (NekoGo has no shared live
  // box to batch inside); the concurrency is what makes Test All
  // feel like Throne instead of one-by-one.
  const list = sortedFiltered.value.slice()
  const total = list.length
  testAllTotal.value = total
  testAllDone.value = 0
  let done = 0
  let idx = 0
  msg.value = total > 0 ? `Testing 0/${total}…` : ''
  async function worker() {
    while (true) {
      if (testAllCancelled.value) return
      const i = idx++
      if (i >= list.length) return
      const p = list[i]
      markTesting(p.id, true)
      try {
        await profiles.test(p.id, 3000)
      } catch {
        /* per-row failure is non-fatal, shown via Failed badge */
      } finally {
        markTesting(p.id, false)
      }
      done++
      testAllDone.value = done
      if (!testAllCancelled.value) msg.value = `Testing ${done}/${total}…`
    }
  }
  try {
    const concurrency = Math.min(5, total)
    await Promise.all(Array.from({ length: concurrency }, () => worker()))
    msg.value = testAllCancelled.value ? `test cancelled (${done}/${total})` : 'test all done'
  } finally {
    busy.value = false
    testAllRunning.value = false
  }
}

function cancelTestAll() {
  testAllCancelled.value = true
}

function exportSelected() {
  const rows = profiles.profiles.filter((p) => selectedIds.value.has(p.id))
  if (rows.length === 0) return
  const blob = new Blob([JSON.stringify(rows, null, 2)], { type: 'application/json' })
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = 'nekobox-profiles.json'
  a.click()
  URL.revokeObjectURL(a.href)
}

async function updateSubs() {
  busy.value = true
  msg.value = ''
  try {
    const r = await subsStore.updateAll()
    msg.value = subsStore.message || `Updated subscriptions: ${r.profiles.length} new profiles.`
    await profiles.load()
  } catch (e) {
    msg.value = String(e)
  } finally {
    busy.value = false
  }
}

// BUG-003: same bounded run as Settings (60s total) with live N/M
// progress and Cancel; see SettingsPanel.geoRefresh.
const countriesRunning = ref(false)
const countriesCancelled = ref(false)
// The progress poller must not outlive the tab: leaving Proxies mid-refresh
// used to keep a 500ms interval writing into dead refs until the backend
// refresh finished (minutes for a large list).
let countryPoll: number | null = null
function stopCountryPoll() {
  if (countryPoll !== null) {
    window.clearInterval(countryPoll)
    countryPoll = null
  }
}
onUnmounted(stopCountryPoll)

async function refreshCountries() {
  busy.value = true
  countriesRunning.value = true
  countriesCancelled.value = false
  msg.value = 'Refreshing countries… (may take a while for many profiles)'
  stopCountryPoll()
  countryPoll = window.setInterval(async () => {
    try {
      const p = await backend.refreshCountriesProgress()
      if (p.running && p.total > 0) msg.value = `Refreshing countries… ${p.done}/${p.total}`
    } catch {
      /* best-effort only */
    }
  }, 500)
  try {
    const r = await backend.refreshAllCountries()
    const tail = countriesCancelled.value ? ' (cancelled)' : ''
    msg.value = `Updated${tail} ${r?.updated ?? 0}, skipped ${r?.skipped ?? 0}, failed ${r?.failed ?? 0}.`
    countryErrors.value = r?.errors ?? []
    await profiles.load()
  } catch (e) {
    msg.value = String(e)
    countryErrors.value = []
  } finally {
    stopCountryPoll()
    countriesRunning.value = false
    busy.value = false
  }
}

async function cancelCountries() {
  countriesCancelled.value = true
  try {
    await backend.cancelRefreshCountries()
  } catch {
    /* the run may have just finished */
  }
}

function onFile() {
  const f = fileInput.value?.files?.[0]
  if (!f) return
  const rd = new FileReader()
  rd.onload = async () => {
    msg.value = ''
    try {
      const r = await backend.importLinks(String(rd.result ?? ''))
      msg.value = `Imported ${r.profiles.length} profiles, skipped ${r.invalid} invalid links.`
      await reloadProfilesAndSubs()
    } catch (e) {
      msg.value = String(e)
    }
    if (fileInput.value) fileInput.value.value = ''
  }
  rd.readAsText(f)
}
</script>
