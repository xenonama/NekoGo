<!-- Updates panel: manual per-component update checks + installs.
      Hosted as the "Updates" tab of SettingsPanel. No auto-check. -->
<template>
  <div class="upd">
    <div class="upd-top">
      <button
        class="nb-act nb-act--primary"
        :disabled="checking || batchUpdating || savingPrefs || selectedIds.length === 0 || busy.size > 0"
        @click="checkSelected"
      >
        <Icon name="refresh" :size="14" />{{ checking ? 'Checking…' : `Check (${selectedIds.length})` }}
      </button>
      <button
        class="nb-act nb-act--primary"
        :disabled="checking || batchUpdating || savingPrefs || selectedIds.length === 0 || busy.size > 0"
        @click="checkAndUpdate"
      >
        <Icon name="download" :size="14" />Check and Update
      </button>
      <span class="muted">Last check: {{ lastGlobalCheck || 'never' }}</span>
    </div>
    <div class="stg-card upd-proxy">
      <div class="upd-proxy-copy">
        <span class="stg-title">Check and download via proxy</span>
        <span class="stg-sub">Use the running NekoGo proxy or the enabled Windows proxy.</span>
      </div>
      <button
        class="stg-toggle"
        :class="{ on: downloadViaProxy }"
        role="switch"
        :aria-checked="downloadViaProxy"
        aria-label="Check and download updates via proxy"
        :disabled="savingPrefs"
        @click="toggleProxy"
      >
        <span class="knob" />
      </button>
    </div>
    <div class="muted upd-selection-hint">Only enabled components are checked.</div>
    <div v-if="cardMsg._all" class="stg-card upd-card upd-err">{{ cardMsg._all }}</div>
    <div v-for="c in items" :key="c.id" class="stg-card upd-card">
      <div class="upd-head">
        <span class="upd-name">{{ c.name }}</span>
        <span class="upd-pill" :title="c.current || 'Not installed'">{{
          c.current || 'not installed'
        }}</span>
        <span class="upd-check-control">
          <span class="muted">Check</span>
          <button
            class="stg-toggle upd-check-toggle"
            :class="{ on: c.checkEnabled }"
            role="switch"
            :aria-checked="c.checkEnabled"
            :aria-label="`Check ${c.name} for updates`"
            :disabled="savingPrefs || checking"
            @click="toggleCheck(c.id, c.checkEnabled)"
          >
            <span class="knob" />
          </button>
        </span>
        <span v-if="c.updateFoundAt" class="upd-found">Update found</span>
      </div>
      <div class="muted upd-sub">
        <span v-if="c.latest">Latest: {{ c.latest }}</span>
        <span v-else>Latest: unknown</span>
        <span v-if="c.updateAvail" class="upd-avail">(update available)</span>
        <span v-if="c.lastCheck"> · checked {{ relTime(c.lastCheck) }}</span>
      </div>
      <div v-if="c.updateFoundAt" class="upd-sub upd-reminder">
        <Icon name="info" :size="14" />
        {{ c.latest }} was found on {{ foundDate(c.updateFoundAt) }} and is waiting to be installed.
      </div>
      <div v-if="c.downloading" class="upd-sub upd-progress-copy">
        <span>{{ downloadSize(c) }}</span>
        <span v-if="progressPercent(c) !== null">{{ progressPercent(c) }}%</span>
      </div>
      <progress
        v-if="c.downloading"
        class="upd-progress"
        :value="progressPercent(c) ?? undefined"
        :max="100"
        :aria-label="`Downloading ${c.name}`"
      />
      <div v-if="c.notes" class="muted upd-sub">
        <Icon v-if="isManual(c)" name="info" :size="14" />
        {{ c.notes }}
      </div>
      <div v-if="cardMsg[c.id]" class="upd-sub" :class="cardOk[c.id] ? 'upd-ok' : 'upd-err'">
        {{ cardMsg[c.id] }}
      </div>
      <div v-if="c.lastError" class="upd-sub upd-err">{{ errorText(c.lastError) }}</div>
      <div class="upd-actions">
        <span v-if="busy.has(c.id)" class="nb-spin" title="Working…" />
        <button
          v-if="c.downloading"
          class="nb-act"
          @click="cancelDownload(c.id)"
        >
          <Icon name="x" :size="14" />Cancel
        </button>
        <!-- Manual-only components (cores without a published asset): no
             Update button — the note above explains how to update instead. -->
        <button
          v-if="!isManual(c)"
          class="nb-act nb-act--primary"
          :disabled="!c.updateAvail || busy.has(c.id) || c.downloading || checking || batchUpdating"
          :title="c.updateAvail ? 'Download and install' : 'Already up to date'"
          @click="updateOne(c.id)"
        >
          <Icon name="download" :size="14" />Update
        </button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { backend } from '../api'
import Icon from './Icon.vue'
import type { UpdatableComponent } from '../types'

// Manual-only components (e.g. a core with no published asset):
// show the note with an info icon and hide the Update button instead
// of offering a click that must fail.
function isManual(c: UpdatableComponent): boolean {
  return /manual/i.test(c.notes ?? '')
}

function errorText(error: unknown): string {
  const text = String(error instanceof Error ? error.message : error)
    .replace(/^\s*RuntimeError:\s*/i, '')
    .trim()
  if (/TLS handshake timeout/i.test(text)) {
    return `${text} — check Check and download via proxy, the active proxy, and the network.`
  }
  return text
}

const items = ref<UpdatableComponent[]>([])
const busy = ref<Set<string>>(new Set())
const checking = ref(false)
const batchUpdating = ref(false)
const savingPrefs = ref(false)
const downloadViaProxy = ref(true)
const selectedIds = computed(() => items.value.filter((c) => c.checkEnabled).map((c) => c.id))
const lastGlobalCheck = ref('')
const cardMsg = ref<Record<string, string>>({})
const cardOk = ref<Record<string, boolean>>({})
let progressTimer: ReturnType<typeof setInterval> | undefined

function syncProgressPolling() {
  if (progressTimer) clearInterval(progressTimer)
  progressTimer = undefined
  if (!items.value.some((c) => c.downloading)) return
  progressTimer = setInterval(() => void load(), 750)
}

function formatBytes(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return '0 B'
  if (n < 1000) return `${Math.round(n)} B`
  const units = ['KB', 'MB', 'GB']
  let value = n / 1000
  let unit = 0
  while (value >= 1000 && unit < units.length - 1) {
    value /= 1000
    unit++
  }
  return `${value >= 10 ? value.toFixed(0) : value.toFixed(1)} ${units[unit]}`
}

function downloadSize(c: UpdatableComponent): string {
  const downloaded = c.downloadedBytes ?? 0
  const total = c.totalBytes ?? -1
  if (downloaded <= 0 && total <= 0) return 'Starting…'
  if (total >= 0) return `${formatBytes(downloaded)}/${formatBytes(total)}`
  return `${formatBytes(downloaded)} downloaded`
}

function progressPercent(c: UpdatableComponent): number | null {
  const total = c.totalBytes ?? -1
  if (total <= 0) return null
  return Math.min(100, Math.round(((c.downloadedBytes ?? 0) / total) * 100))
}

function foundDate(iso: string): string {
  const date = new Date(iso)
  return Number.isNaN(date.getTime()) ? iso : date.toLocaleString()
}

function relTime(iso?: string): string {
  if (!iso) return 'never'
  const t = new Date(iso).getTime()
  if (Number.isNaN(t)) return iso
  const s = Math.floor((Date.now() - t) / 1000)
  if (s < 60) return 'just now'
  if (s < 3600) return `${Math.floor(s / 60)}m ago`
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`
  return new Date(t).toLocaleDateString()
}

function markBusy(id: string, on: boolean) {
  const s = new Set(busy.value)
  if (on) s.add(id)
  else s.delete(id)
  busy.value = s
}

function recordLastCheck(rows: UpdatableComponent[], ids?: string[]) {
  const selected = ids ? new Set(ids) : undefined
  const checks = rows
    .filter((c) => !selected || selected.has(c.id))
    .map((c) => c.lastCheck)
    .filter((v): v is string => Boolean(v))
    .sort()
  if (checks.length) lastGlobalCheck.value = checks[checks.length - 1]
}

async function load() {
  try {
    items.value = (await backend.listUpdatables()) ?? []
  } catch {
    return
  }
  recordLastCheck(items.value)
  syncProgressPolling()
}

async function loadProxyPreference() {
  savingPrefs.value = true
  try {
    downloadViaProxy.value = await backend.updaterProxyEnabled()
  } catch (error) {
    cardMsg.value = { ...cardMsg.value, _all: errorText(error) }
  } finally {
    savingPrefs.value = false
  }
}

async function checkSelected() {
  const ids = [...selectedIds.value]
  if (checking.value || batchUpdating.value || ids.length === 0) return
  checking.value = true
  cardMsg.value = { ...cardMsg.value, _all: '' }
  try {
    items.value = (await backend.checkUpdates(ids)) ?? items.value
    recordLastCheck(items.value, ids)
  } catch (error) {
    cardMsg.value = { ...cardMsg.value, _all: errorText(error) }
  } finally {
    checking.value = false
    syncProgressPolling()
  }
}

async function checkAndUpdate() {
  const ids = [...selectedIds.value]
  if (checking.value || batchUpdating.value || ids.length === 0) return
  checking.value = true
  cardMsg.value = { ...cardMsg.value, _all: '' }
  let rows: UpdatableComponent[]
  try {
    rows = (await backend.checkUpdates(ids)) ?? []
  } catch (error) {
    cardMsg.value = { ...cardMsg.value, _all: errorText(error) }
    return
  } finally {
    checking.value = false
    syncProgressPolling()
  }

  items.value = rows
  recordLastCheck(rows, ids)
  const selected = new Set(ids)
  const candidates = rows.filter((c) => selected.has(c.id) && c.checkEnabled && c.updateAvail && !isManual(c))
  batchUpdating.value = true
  try {
    for (const c of candidates) {
      if (!(await updateOne(c.id))) break
    }
  } finally {
    batchUpdating.value = false
    syncProgressPolling()
  }
}

async function toggleCheck(id: string, enabled: boolean) {
  if (savingPrefs.value) return
  const next = !enabled
  items.value = items.value.map((c) => c.id === id ? { ...c, checkEnabled: next } : c)
  savingPrefs.value = true
  try {
    await backend.setUpdatableEnabled(id, next)
  } catch (error) {
    items.value = items.value.map((c) => c.id === id ? { ...c, checkEnabled: enabled } : c)
    cardMsg.value = { ...cardMsg.value, _all: errorText(error) }
  } finally {
    savingPrefs.value = false
  }
}

async function toggleProxy() {
  if (savingPrefs.value) return
  const previous = downloadViaProxy.value
  downloadViaProxy.value = !previous
  savingPrefs.value = true
  try {
    await backend.setUpdaterProxyEnabled(downloadViaProxy.value)
  } catch (error) {
    downloadViaProxy.value = previous
    cardMsg.value = { ...cardMsg.value, _all: errorText(error) }
  } finally {
    savingPrefs.value = false
  }
}

async function updateOne(id: string): Promise<boolean> {
  markBusy(id, true)
  cardMsg.value = { ...cardMsg.value, [id]: '' }
  items.value = items.value.map((c) => (c.id === id ? { ...c, downloading: true, downloadedBytes: 0, totalBytes: -1 } : c))
  syncProgressPolling()
  try {
    const row = await backend.updateComponent(id)
    cardMsg.value = {
      ...cardMsg.value,
      [id]: id === 'nekogo'
        ? 'NekoGo will restart to install the update.'
        : `Updated to ${row?.current || row?.latest || 'latest'}`,
    }
    cardOk.value = { ...cardOk.value, [id]: true }
    return true
  } catch (error) {
    const cancelled = /download cancelled/i.test(errorText(error))
    cardMsg.value = {
      ...cardMsg.value,
      [id]: cancelled ? 'Download cancelled.' : errorText(error),
    }
    cardOk.value = { ...cardOk.value, [id]: cancelled }
    return !cancelled
  } finally {
    await load()
    markBusy(id, false)
    syncProgressPolling()
  }
}

async function cancelDownload(value: string | UpdatableComponent) {
  const c = typeof value === 'string' ? { id: value } : value
  const id = c.id
  try {
    const cancelled = await backend.cancelUpdate(c.id)
    cardMsg.value = {
      ...cardMsg.value,
      [id]: cancelled ? 'Download cancelled.' : 'Download is no longer running.',
    }
    cardOk.value = { ...cardOk.value, [id]: true }
  } catch (error) {
    cardMsg.value = { ...cardMsg.value, [id]: errorText(error) }
    cardOk.value = { ...cardOk.value, [id]: false }
  } finally {
    await load()
  }
}

onMounted(() => {
  void load()
  void loadProxyPreference()
})
onUnmounted(() => {
  if (progressTimer) clearInterval(progressTimer)
})
</script>
