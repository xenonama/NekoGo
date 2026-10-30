<!-- Advanced Mode Phase F: Backup & Reset cards (export, import,
     raw config viewer, two-step reset). Zero new deps; stg-* styles
     and var() tokens only. One shared busy flag covers all four
     cards; the parent sub-tab guard reads isBusy(). -->
<template>
  <div class="nb-backup">
    <!-- A) Export all data -->
    <div v-if="show('export')" class="stg-card">
      <div class="stg-head">
        <span class="stg-head-chip"><Icon name="upload" :size="22" /></span>
        <span class="stg-head-text">
          <span class="stg-head-title">Export all data</span>
          <span class="stg-head-sub">Includes settings, profiles, subscriptions and config overrides. Keep it safe.</span>
        </span>
        <button class="nb-act nb-act--primary" :disabled="busy" @click="doExport">
          <Icon name="download" :size="14" />Export to file
        </button>
      </div>
    </div>
    <!-- B) Import data -->
    <div v-if="show('import')" class="stg-card">
      <div class="stg-head">
        <span class="stg-head-chip"><Icon name="download" :size="22" /></span>
        <span class="stg-head-text">
          <span class="stg-head-title">Import data</span>
          <span class="stg-head-sub">{{ importSummary || 'Restore from a backup file (replaces current data).' }}</span>
        </span>
      </div>
      <div class="nb-px-bar">
        <input ref="fileRef" type="file" accept=".json,application/json" style="display: none" @change="onFile" />
        <button class="nb-act" :disabled="busy" @click="fileRef?.click()">
          <Icon name="folder" :size="14" />Choose file…
        </button>
        <button class="nb-act nb-act--primary" :disabled="busy || !fileText" @click="doImport">
          <Icon name="upload" :size="14" />Replace current data
        </button>
      </div>
      <div v-if="importError" class="nb-json-error">{{ importError }}</div>
    </div>
    <!-- C) View raw config -->
    <div v-if="show('raw')" class="stg-card">
      <div class="stg-head">
        <span class="stg-head-chip"><Icon name="code" :size="22" /></span>
        <span class="stg-head-text">
          <span class="stg-head-title">Raw config</span>
          <span class="stg-head-sub">Exact config the active core receives, overrides merged in.</span>
        </span>
        <button class="nb-act" :disabled="busy" @click="showRaw">
          <Icon name="code" :size="14" />Show raw config
        </button>
      </div>
      <pre v-if="rawConfig" class="nb-json-text selectable" style="white-space: pre-wrap">{{ rawConfig }}</pre>
      <div v-else class="muted">Connect a profile to see its config.</div>
      <div class="nb-px-bar">
        <button class="nb-act nb-act--sm nb-act--ghost" :disabled="busy || !rawConfig" @click="copyRaw">
          <Icon name="copy" :size="13" />{{ copied ? 'Copied' : 'Copy' }}
        </button>
      </div>
    </div>
    <!-- D) Reset to defaults -->
    <div v-if="show('reset')" class="stg-card stg-card-danger">
      <div class="stg-head">
        <span class="stg-head-chip"><Icon name="warn" :size="22" /></span>
        <span class="stg-head-text">
          <span class="stg-head-title">Reset to defaults</span>
          <span class="stg-head-sub">Wipes settings, profiles, subscriptions and overrides. This cannot be undone.</span>
        </span>
        <button class="nb-act nb-act--danger" :disabled="busy" @click="resetArmed = !resetArmed">
          <Icon name="trash" :size="14" />Reset to defaults
        </button>
      </div>
      <div v-if="resetArmed" class="nb-px-bar">
        <span class="muted">Type RESET-ALL to confirm:</span>
        <input v-model="resetToken" class="nb-field" placeholder="RESET-ALL" style="max-width: 160px" />
        <button class="nb-act nb-act--danger" :disabled="busy || resetToken !== 'RESET-ALL'" @click="doReset">
          <Icon name="warn" :size="14" />Confirm reset
        </button>
      </div>
    </div>
    <div v-if="toast" class="muted">{{ toast }}</div>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import Icon from './Icon.vue'
import { backend } from '../api'
import { useProfilesStore } from '../stores/profiles'
import { useSettingsStore } from '../stores/settings'
import { useConnectionStore } from '../stores/connection'
import { useSubscriptionsStore } from '../stores/subscriptions'
import { syncThemeClass } from '../theme'
import type { BackupBlob } from '../types'

const profiles = useProfilesStore()
const settings = useSettingsStore()
const connection = useConnectionStore()

// Section filter for the Advanced Backup workspace: each Panel
// mounts one BackupPanel with a single card (export/import/raw/
// reset). Null renders everything (legacy full list).
const props = withDefaults(
  defineProps<{ only?: string[] | null }>(),
  { only: null },
)

function show(...keys: string[]): boolean {
  return !props.only || keys.some((k) => props.only!.includes(k))
}

/** Single shared busy across all four cards (Phase F concurrency). */
const busy = ref(false)
const toast = ref('')

/** Parent sub-tab guard: an in-flight action prompts before switching. */
function isBusy(): boolean {
  return busy.value
}
defineExpose({ isBusy })

function pad(n: number): string {
  return String(n).padStart(2, '0')
}

/** Reload every store after a wholesale data change (import/reset).
 *  Includes subscriptions so Simple + Advanced stay identical. */
async function reloadStores() {
  await settings.load()
  syncThemeClass(settings.settings.theme)
  await profiles.load()
  try {
    await useSubscriptionsStore().load()
  } catch { /* best effort */ }
  await connection.refresh()
}

// --- A) Export ---
async function doExport() {
  busy.value = true
  toast.value = ''
  try {
    const text = await backend.exportAllData()
    const d = new Date()
    const name = `NekoGo-backup-${d.getFullYear()}${pad(d.getMonth() + 1)}${pad(d.getDate())}-${pad(d.getHours())}${pad(d.getMinutes())}${pad(d.getSeconds())}.json`
    const url = URL.createObjectURL(new Blob([text], { type: 'application/json' }))
    const a = document.createElement('a')
    a.href = url
    a.download = name
    document.body.appendChild(a)
    a.click()
    document.body.removeChild(a)
    URL.revokeObjectURL(url)
    toast.value = `Exported to ${name}.`
  } catch (e) {
    toast.value = String(e)
  } finally {
    busy.value = false
  }
}

// --- B) Import ---
const fileRef = ref<HTMLInputElement | null>(null)
const fileText = ref('')
const importSummary = ref('')
const importError = ref('')

function onFile() {
  const f = fileRef.value?.files?.[0]
  if (!f) return
  const rd = new FileReader()
  rd.onload = () => {
    importError.value = ''
    try {
      const blob = JSON.parse(String(rd.result ?? '')) as BackupBlob
      const keys = blob.settings ? Object.keys(blob.settings).length : 0
      importSummary.value =
        `${blob.profiles?.length ?? 0} profiles, ${blob.subscriptions?.length ?? 0} subscriptions, ` +
        `${keys} settings keys, ${blob.coreOverrides ? Object.keys(blob.coreOverrides).length : 0} overrides.`
      fileText.value = String(rd.result ?? '')
    } catch (e) {
      importSummary.value = ''
      fileText.value = ''
      importError.value = 'Invalid backup file: ' + (e instanceof Error ? e.message : String(e))
    }
    if (fileRef.value) fileRef.value.value = ''
  }
  rd.readAsText(f)
}

async function doImport() {
  busy.value = true
  importError.value = ''
  toast.value = ''
  try {
    await backend.importAllData(fileText.value)
    fileText.value = ''
    importSummary.value = ''
    await reloadStores()
    toast.value = 'Import complete.'
  } catch (e) {
    importError.value = String(e)
  } finally {
    busy.value = false
  }
}

// --- C) Raw config ---
const rawConfig = ref('')
const copied = ref(false)

// Phase G agreement: the viewer reads the same getRawConfig the Raw
// Config editor saves through, so both always show the same text
// (the older viewRawConfig binding stays for compatibility).
async function showRaw() {
  busy.value = true
  toast.value = ''
  try {
    rawConfig.value = await backend.getRawConfig()
  } catch (e) {
    toast.value = String(e)
  } finally {
    busy.value = false
  }
}

async function copyRaw() {
  if (!rawConfig.value) return
  try {
    await navigator.clipboard.writeText(rawConfig.value)
  } catch {
    const ta = document.createElement('textarea')
    ta.value = rawConfig.value
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

// --- D) Reset ---
const resetArmed = ref(false)
const resetToken = ref('')

async function doReset() {
  busy.value = true
  toast.value = ''
  try {
    await backend.resetToDefaults(resetToken.value)
    resetArmed.value = false
    resetToken.value = ''
    rawConfig.value = ''
    await reloadStores()
    toast.value = 'All data reset.'
  } catch (e) {
    toast.value = String(e)
  } finally {
    busy.value = false
  }
}
</script>
