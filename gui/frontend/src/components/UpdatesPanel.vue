<!-- Updates panel: manual per-component update checks + installs.
      Hosted as the "Updates" tab of SettingsPanel. No auto-check. -->
<template>
  <div class="upd">
    <div class="upd-top">
      <button class="nb-act nb-act--primary" :disabled="checkingAll" @click="checkAll">
        <Icon name="refresh" :size="14" />{{ checkingAll ? 'Checking…' : 'Check all' }}
      </button>
      <span class="muted">Last global check: {{ lastGlobalCheck || 'never' }}</span>
    </div>
    <!-- BUG-024: global "Check all" errors were stored under cardMsg._all,
         which no row ever renders — surface them as a banner instead. -->
    <div v-if="cardMsg._all" class="stg-card upd-card upd-err">{{ cardMsg._all }}</div>
    <div v-for="c in items" :key="c.id" class="stg-card upd-card">
      <div class="upd-head">
        <span class="upd-name">{{ c.name }}</span>
        <span class="upd-pill" :title="c.current || 'Not installed'">{{
          c.current || 'not installed'
        }}</span>
      </div>
      <div class="muted upd-sub">
        <span v-if="c.latest">Latest: {{ c.latest }}</span>
        <span v-else>Latest: unknown</span>
        <span v-if="c.updateAvail" class="upd-avail">(update available)</span>
        <span v-if="c.lastCheck"> · checked {{ relTime(c.lastCheck) }}</span>
      </div>
      <div v-if="c.notes" class="muted upd-sub">
        <Icon v-if="isManual(c)" name="info" :size="14" />
        {{ c.notes }}
      </div>
      <div v-if="cardMsg[c.id]" class="upd-sub" :class="cardOk[c.id] ? 'upd-ok' : 'upd-err'">
        {{ cardMsg[c.id] }}
      </div>
      <div v-if="c.lastError" class="upd-sub upd-err">{{ c.lastError }}</div>
      <div class="upd-actions">
        <span v-if="busy.has(c.id)" class="nb-spin" title="Working…" />
        <button class="nb-act nb-act--ghost" :disabled="busy.has(c.id) || checkingAll" @click="checkOne(c.id)">
          <Icon name="refresh" :size="14" />Check
        </button>
        <!-- Manual-only components (cores without a published asset): no
             Update button — the note above explains how to update instead. -->
        <button
          v-if="!isManual(c)"
          class="nb-act nb-act--primary"
          :disabled="!c.updateAvail || busy.has(c.id) || checkingAll"
          :title="c.updateAvail ? 'Download and install' : 'Already up to date'"
          @click="updateOne(c.id)"
        >
          <Icon name="download" :size="14" />Update
        </button>
        <!-- Downloaded GUI installer: explicit user-initiated open only.
             The backend never auto-opens downloads (no signature check). -->
        <button
          v-if="c.installerPath"
          class="nb-act"
          :disabled="busy.has(c.id)"
          :title="'Open ' + c.installerPath"
          @click="openInstaller(c.id)"
        >
          <Icon name="folder" :size="14" />Open installer
        </button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { backend } from '../api'
import Icon from './Icon.vue'
import type { UpdatableComponent } from '../types'

// Manual-only components (e.g. a core with no published asset):
// show the note with an info icon and hide the Update button instead
// of offering a click that must fail.
function isManual(c: UpdatableComponent): boolean {
  return /manual/i.test(c.notes ?? '')
}

const items = ref<UpdatableComponent[]>([])
const busy = ref<Set<string>>(new Set())
const checkingAll = ref(false)
const lastGlobalCheck = ref('')
const cardMsg = ref<Record<string, string>>({})
const cardOk = ref<Record<string, boolean>>({})

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

async function load() {
  try {
    items.value = (await backend.listUpdatables()) ?? []
  } catch {
    items.value = []
  }
}

async function checkAll() {
  checkingAll.value = true
  try {
    items.value = (await backend.checkUpdates()) ?? items.value
    lastGlobalCheck.value = new Date().toISOString()
  } catch (e) {
    cardMsg.value = { ...cardMsg.value, _all: String(e) }
  } finally {
    checkingAll.value = false
  }
}

// BUG-023: checkOne wraps its body in try/catch like updateOne —
// without it a failed per-row Check rejects the click promise and
// the spinner just stops with zero feedback.
async function checkOne(id: string) {
  markBusy(id, true)
  cardMsg.value = { ...cardMsg.value, [id]: '' }
  try {
    const rows = (await backend.checkUpdates()) ?? []
    const row = rows.find((r) => r.id === id)
    if (row) {
      items.value = items.value.map((c) => (c.id === id ? row : c))
    }
  } catch (e) {
    cardMsg.value = { ...cardMsg.value, [id]: String(e) }
    cardOk.value = { ...cardOk.value, [id]: false }
  } finally {
    markBusy(id, false)
  }
}

async function updateOne(id: string) {
  markBusy(id, true)
  cardMsg.value = { ...cardMsg.value, [id]: '' }
  try {
    const row = await backend.updateComponent(id)
    cardMsg.value = { ...cardMsg.value, [id]: `Updated to ${row?.current || row?.latest || 'latest'}` }
    cardOk.value = { ...cardOk.value, [id]: true }
    await load()
  } catch (e) {
    cardMsg.value = { ...cardMsg.value, [id]: String(e) }
    cardOk.value = { ...cardOk.value, [id]: false }
  } finally {
    markBusy(id, false)
  }
}

async function openInstaller(id: string) {
  const row = items.value.find((r) => r.id === id)
  const path = row?.installerPath
  if (!path) return
  markBusy(id, true)
  try {
    await backend.openURL(path)
    cardMsg.value = { ...cardMsg.value, [id]: `Opened ${path}` }
    cardOk.value = { ...cardOk.value, [id]: true }
  } catch (e) {
    cardMsg.value = { ...cardMsg.value, [id]: String(e) }
    cardOk.value = { ...cardOk.value, [id]: false }
  } finally {
    markBusy(id, false)
  }
}

onMounted(load)
</script>
