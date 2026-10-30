<!-- Application logs: level filter, search, clear/copy, smart auto-scroll. -->
<template>
  <div class="panel lv">
    <div class="toolbar lv-toolbar">
      <PickSelect
        v-model="levelFilter"
        icon="terminal"
        title="Level filter"
        aria-label="Level filter"
        neutral="all"
        :options="levelOptions"
      />
      <div class="lv-field">
        <Icon name="search" :size="14" />
        <input v-model="query" placeholder="Search logs…" aria-label="Search logs" />
        <button v-if="query" class="lv-clear" title="Clear search" aria-label="Clear search" @click="query = ''">
          <Icon name="x" :size="12" />
        </button>
      </div>
      <div class="lv-field" :class="{ 'is-bad': incBad }" :title="incBad ? reError : 'Only show lines matching this regex'">
        <Icon name="code" :size="14" />
        <input v-model="includeRe" placeholder="Include regex…" aria-label="Include regex" />
        <button v-if="includeRe" class="lv-clear" title="Clear include regex" aria-label="Clear include regex" @click="includeRe = ''">
          <Icon name="x" :size="12" />
        </button>
      </div>
      <div class="lv-field" :class="{ 'is-bad': excBad }" :title="excBad ? reError : 'Hide lines matching this regex'">
        <Icon name="code" :size="14" />
        <input v-model="excludeRe" placeholder="Exclude regex…" aria-label="Exclude regex" />
        <button v-if="excludeRe" class="lv-clear" title="Clear exclude regex" aria-label="Clear exclude regex" @click="excludeRe = ''">
          <Icon name="x" :size="12" />
        </button>
      </div>
      <button class="nb-act nb-act--ghost" @click="clearView">
        <Icon name="trash" />
        Clear
      </button>
      <button class="nb-act nb-act--ghost" @click="copyVisible">
        <Icon name="copy" />
        Copy
      </button>
      <span class="muted lv-count">{{ visible.length }} / {{ logs.length }} lines</span>
      <span v-if="reError" class="nb-json-error" :title="reError">Bad regex</span>
      <span v-if="copied" class="muted">Copied</span>
    </div>
    <div ref="box" class="logbox lv-box" @scroll="onScroll">
      <div v-for="l in visible" :key="keyOf(l)">
        <span class="lv-time">[{{ hhmmss(l.time) }}]</span>
        <span class="lv-src" :class="lvlClass(l.level)">[{{ l.source }}]</span>
        <span class="lv-msg">{{ l.message }}</span>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { useConnectionStore } from '../stores/connection'
import type { LogEntry } from '../types'
import Icon from './Icon.vue'
import PickSelect from './PickSelect.vue'

const connection = useConnectionStore()
const logs = computed(() => connection.logs)

const levelFilter = ref('all')
const levelOptions = [
  { value: 'all', label: 'All levels' },
  { value: 'debug', label: 'Debug' },
  { value: 'info', label: 'Info' },
  { value: 'warn', label: 'Warn' },
  { value: 'error', label: 'Error' },
]
const query = ref('')
// Throne parity: include/exclude regex filters (Throne's
// log_enable_include/exclude). Invalid patterns show "Bad regex"
// and are ignored, never crash the list.
const includeRe = ref('')
const excludeRe = ref('')
const incBad = computed(() => includeRe.value.trim() !== '' && !tryRegex(includeRe.value))
const excBad = computed(() => excludeRe.value.trim() !== '' && !tryRegex(excludeRe.value))
const reError = computed(() =>
  incBad.value || excBad.value ? 'One of the regex filters is invalid and is being ignored.' : '',
)
const copied = ref(false)
// Keys of lines the user dismissed via Clear (local view only; the backend
// buffer is untouched, so brand-new lines keep appearing).
const cleared = ref(new Set<string>())
const box = ref<HTMLElement | null>(null)
// Stick to the bottom on new lines unless the user scrolled up.
let stick = true

function normLevel(level: string): string {
  const l = (level || '').toLowerCase()
  return l === 'warning' ? 'warn' : l
}

// Identity of a log line for the Clear set. The backend assigns a monotonic
// id, so use it: the previous key embedded the array index, and once the
// 300-line window slid every index changed — dismissed lines came back on
// the next poll.
function keyOf(l: LogEntry): string {
  return String(l.id)
}

function tryRegex(src: string): RegExp | null {
  const t = src.trim()
  if (!t) return null
  try {
    return new RegExp(t, 'i')
  } catch {
    return null
  }
}

const visible = computed(() => {
  const q = query.value.trim().toLowerCase()
  const inc = tryRegex(includeRe.value)
  const exc = tryRegex(excludeRe.value)
  return logs.value.filter((l) => {
    if (cleared.value.has(keyOf(l))) return false
    if (levelFilter.value !== 'all' && normLevel(l.level) !== levelFilter.value) return false
    if (q && !`${l.message} ${l.source}`.toLowerCase().includes(q)) return false
    const hay = `${l.message} ${l.source}`
    if (inc && !inc.test(hay)) return false
    if (exc && exc.test(hay)) return false
    return true
  })
})

function lvlClass(level: string): string {
  switch (normLevel(level)) {
    case 'error': return 'lv-error'
    case 'warn': return 'lv-warn'
    case 'debug': return 'lv-debug'
    default: return 'lv-info'
  }
}

/** Trim an RFC3339 timestamp down to HH:MM:SS. */
function hhmmss(t: string): string {
  const m = /T(\d{2}:\d{2}:\d{2})/.exec(t || '')
  return m ? m[1] : t
}

function scrollBottom() {
  const el = box.value
  if (el) el.scrollTop = el.scrollHeight
}

function onScroll() {
  const el = box.value
  if (!el) return
  stick = el.scrollHeight - el.scrollTop - el.clientHeight < 24
}

function clearView() {
  // Drop ids that already left the buffer so the set cannot grow without
  // bound over a long session of repeated Clears.
  const live = new Set(logs.value.map((l) => keyOf(l)))
  const next = new Set<string>()
  cleared.value.forEach((k) => {
    if (live.has(k)) next.add(k)
  })
  visible.value.forEach((l) => next.add(keyOf(l)))
  cleared.value = next
}

async function copyVisible() {
  const text = visible.value.map((l) => `[${hhmmss(l.time)}] [${l.source}] ${l.message}`).join('\n')
  try {
    await navigator.clipboard.writeText(text)
  } catch {
    // Clipboard API unavailable (permissions): textarea + execCommand fallback.
    const ta = document.createElement('textarea')
    ta.value = text
    document.body.appendChild(ta)
    ta.select()
    try {
      document.execCommand('copy')
    } catch {
      /* best effort */
    }
    document.body.removeChild(ta)
  }
  copied.value = true
  window.setTimeout(() => { copied.value = false }, 1500)
}

watch(visible, () => {
  if (stick) void nextTick(scrollBottom)
})
onMounted(() => {
  // New lines may already be present; settle at the bottom on first paint.
  void nextTick(scrollBottom)
  connection.acquireLogs()
})
onUnmounted(() => connection.releaseLogs())
</script>
