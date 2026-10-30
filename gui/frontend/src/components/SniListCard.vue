<!-- Scalable candidate-list card for the SNI tab (Cloudflare IPs and
     decoy SNIs). Hundreds of entries used to render as one endless
     row list; this card bounds the DOM (capped, scrollable window),
     makes entries findable (live filter + health sort: scanned-OK
     fastest first, then unscanned, then failed — the same rank the
     backend applies to the engine pools), supports bulk paste
     ("a, b, c" / one per line), and offers one-click removal of
     scanned-failing entries (the primary entry is never removed).
     Checkboxes are the on/off switch: only checked entries feed the
     engine and checked rows carry an accent highlight; clicking
     anywhere on a row toggles it, Shift-click applies the range since
     the last click, and the toolbar's "Check all / Uncheck all"
     covers everything matching the filter. The primary (star button,
     badge on the row) is the pinned entry the engine dials — any
     checked entry can be starred, not just the first. Each row scans
     alone (an IP against the pinned SNI, an SNI against the pinned
     IP). The toolbar's "Scan checked (N)" probes only the checked
     subset the same way — there is no IP x SNI matrix scan. -->
<template>
  <div class="nb-card nb-page">
    <h3>{{ title }} <span class="sni-count">{{ countText }} · {{ activeCount }} active</span></h3>
    <p class="muted">{{ hint }}<template v-if="primaryKey"> Primary: <strong>{{ primaryKey }}</strong>.</template></p>
    <div class="toolbar sni-toolbar">
      <slot name="toolbar" />
      <span class="sni-list-search">
        <SearchBar v-model="query" :placeholder="searchPlaceholder" />
      </span>
      <PickSelect v-model="sortMode" icon="list" title="Sort order" aria-label="Sort order" neutral="health" :options="sortOptions" />
      <button class="nb-act" :disabled="busy || scanning || sorted.length === 0" :title="checkAllTitle" @click="toggleCheckAll">
        <Icon name="check" :size="14" />{{ allFilteredChecked ? 'Uncheck all' : 'Check all' }}
      </button>
      <button v-if="failedCount > 0" class="nb-act nb-act--danger" :disabled="busy" :title="removeFailedTitle" @click="$emit('remove-failed')">
        <Icon name="trash" :size="14" />Remove {{ failedCount }} failed
      </button>
      <span class="spacer" />
      <input v-model="newValue" class="nb-field" :placeholder="placeholder" :aria-label="addLabel" @keydown.enter.prevent="add" />
      <button class="nb-act nb-act--primary" :disabled="busy || !newValue.trim()" @click="add">{{ addLabel }}</button>
    </div>
    <div v-if="rows.length === 0" class="muted">{{ emptyText }}</div>
    <div v-else-if="sorted.length === 0" class="muted">No matches for "{{ query }}".</div>
    <div v-else class="nb-list sni-list">
      <div v-for="r in visibleRows" :key="r.key" class="stg-row" :class="{ checked: r.checked, primary: r.primary }" @click="onRowClick($event, r)">
        <input type="checkbox" :checked="r.checked" :disabled="busy || scanning" :title="checkTitle" @click.stop @change="$emit('toggle', r.key)" />
        <span class="stg-meta">
          <span class="stg-title">{{ r.label }}<span v-if="r.primary" class="sni-primary-badge" title="Primary: the pinned entry the engine dials">primary</span></span>
          <span class="stg-sub" :class="r.cls">{{ r.sub }}</span>
        </span>
        <span class="stg-ctl">
          <button
            v-if="r.checked && !r.primary"
            class="nb-act nb-act--sm sni-star"
            :disabled="busy || scanning"
            title="Make this the primary — the pinned entry the engine dials"
            @click="$emit('make-primary', r.key)"
          ><Icon name="star" :size="13" /><span>Primary</span></button>
          <button class="nb-act nb-act--sm" :disabled="busy || scanning || !canScan" :title="scanTitle" @click="$emit('scan', r.key)">
            <Icon :name="scanningKey === r.key ? 'refresh' : 'search'" :size="13" />{{ scanningKey === r.key ? 'Scanning…' : 'Scan' }}
          </button>
          <button class="nb-act nb-act--sm nb-act--ghost" :disabled="busy || scanning" @click="$emit('remove', r.index)">
            <Icon name="trash" :size="13" />Remove
          </button>
        </span>
      </div>
    </div>
    <div v-if="hiddenCount > 0" class="sni-more">
      <button class="nb-act nb-act--ghost" :disabled="busy" @click="visible += pageSize">
        Show {{ Math.min(pageSize, hiddenCount) }} more <span class="muted">({{ hiddenCount }} hidden)</span>
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import SearchBar from './SearchBar.vue'
import Icon from './Icon.vue'
import PickSelect from './PickSelect.vue'
import type { SniListRow } from '../types'

const props = defineProps<{
  title: string
  hint: string
  rows: SniListRow[]
  primaryKey: string
  checkTitle: string
  scanTitle: string
  canScan: boolean
  scanning: boolean
  scanningKey: string
  placeholder: string
  searchPlaceholder: string
  addLabel: string
  emptyText: string
  removeFailedTitle: string
  busy: boolean
}>()
const emit = defineEmits<{
  (e: 'toggle', key: string): void
  (e: 'set-checked', keys: string[], checked: boolean): void
  (e: 'make-primary', key: string): void
  (e: 'scan', key: string): void
  (e: 'remove', index: number): void
  (e: 'remove-failed'): void
  (e: 'add-bulk', values: string[]): void
}>()

// Render window: at most this many rows in the DOM at once; the
// "show more" footer extends it page by page. Keeps even a
// thousands-entry list off the critical path.
const pageSize = 60
const query = ref('')
const sortMode = ref('health')
const sortOptions = [
  { value: 'health', label: 'Best first' },
  { value: 'added', label: 'List order' },
  { value: 'az', label: 'A–Z' },
]
const visible = ref(pageSize)
const newValue = ref('')

watch([query, sortMode], () => { visible.value = pageSize })

function rank(r: SniListRow): [number, number] {
  if (r.ok) return [0, r.ms]
  return r.at ? [2, 0] : [1, 0]
}

const sorted = computed<SniListRow[]>(() => {
  const q = query.value.trim().toLowerCase()
  const filtered = q ? props.rows.filter((r) => r.label.toLowerCase().includes(q)) : props.rows.slice()
  if (sortMode.value === 'az') return filtered.sort((a, b) => a.label.localeCompare(b.label))
  if (sortMode.value === 'added') return filtered
  return filtered.sort((a, b) => {
    const [ra, ma] = rank(a)
    const [rb, mb] = rank(b)
    return ra - rb || ma - mb
  })
})

const visibleRows = computed(() => sorted.value.slice(0, visible.value))
const hiddenCount = computed(() => Math.max(0, sorted.value.length - visible.value))
const failedCount = computed(() =>
  props.rows.filter((r) => r.at !== '' && !r.ok && !r.primary).length,
)
const activeCount = computed(() => props.rows.filter((r) => r.checked).length)
const countText = computed(() =>
  query.value.trim() ? `${sorted.value.length} / ${props.rows.length}` : `${props.rows.length}`,
)

// "Check all / Uncheck all" applies to every entry matching the current
// filter (a transient search never hides the scope: the title says it).
const allFilteredChecked = computed(() => sorted.value.length > 0 && sorted.value.every((r) => r.checked))
const checkAllTitle = computed(() =>
  allFilteredChecked.value
    ? 'Uncheck every entry matching the current filter'
    : 'Check every entry matching the current filter',
)
function toggleCheckAll() {
  if (sorted.value.length === 0) return
  emit('set-checked', sorted.value.map((r) => r.key), !allFilteredChecked.value)
}

// Row click toggles the entry (the checkbox is just the visual state);
// Shift-click applies the clicked row's new state across the range
// since the last plain click (anchored in the displayed sort order).
let lastKey = ''
function onRowClick(e: MouseEvent, r: SniListRow) {
  if (props.busy || props.scanning) return
  const t = e.target as HTMLElement | null
  if (t?.closest('button,input,select,a,textarea')) return
  if (e.shiftKey && lastKey) {
    const seq = sorted.value
    const a = seq.findIndex((x) => x.key === lastKey)
    const b = seq.findIndex((x) => x.key === r.key)
    if (a >= 0 && b >= 0) {
      const [lo, hi] = a < b ? [a, b] : [b, a]
      emit('set-checked', seq.slice(lo, hi + 1).map((x) => x.key), !r.checked)
      return
    }
  }
  emit('toggle', r.key)
  lastKey = r.key
}

// Bulk paste: split on commas / whitespace / semicolons, one emit.
function add() {
  const values = newValue.value.split(/[\s,;]+/).map((v) => v.trim()).filter(Boolean)
  if (values.length === 0) return
  newValue.value = ''
  emit('add-bulk', values)
}
</script>
