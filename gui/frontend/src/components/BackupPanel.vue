<!-- Advanced Mode Phase F: Backup & Reset cards (export, import,
     raw config viewer, two-step reset). Zero new deps; stg-* styles
     and var() tokens only. One shared busy flag covers all four
     cards, and each card reports its own result: a single message
     after the last card sat off-screen from the button that caused it. -->
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
      <div v-if="toast && toastCard === 'export'" class="muted">{{ toast }}</div>
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
      <div v-if="toast && toastCard === 'import'" class="muted">{{ toast }}</div>
    </div>
    <!-- C) View raw config -->
    <div v-if="show('raw')" class="stg-card">
      <div class="stg-head">
        <span class="stg-head-chip"><Icon name="code" :size="22" /></span>
        <span class="stg-head-text">
          <span class="stg-head-title">View raw config</span>
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
      <div v-if="toast && toastCard === 'raw'" class="muted">{{ toast }}</div>
    </div>
    <!-- D) Reset to defaults -->
    <div v-if="show('reset')" class="stg-card stg-card-danger">
      <div class="stg-head">
        <span class="stg-head-chip"><Icon name="warn" :size="22" /></span>
        <span class="stg-head-text">
          <span class="stg-head-title">Reset to defaults</span>
          <span class="stg-head-sub">Wipes settings, profiles, subscriptions and overrides. This cannot be undone.</span>
        </span>
        <button class="nb-act nb-act--danger" :disabled="busy" @click="toggleReset">
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
      <div v-if="toast && toastCard === 'reset'" class="muted">{{ toast }}</div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import Icon from './Icon.vue'
import { backend } from '../api'
import { useProfilesStore } from '../stores/profiles'
import { useSettingsStore } from '../stores/settings'
import { useConnectionStore } from '../stores/connection'
import { useSubscriptionsStore } from '../stores/subscriptions'
import { useConfirmStore } from '../stores/confirm'
import { configTargetProfile } from '../configTarget'
import { syncThemeClass } from '../theme'
import type { BackupBlob } from '../types'

const profiles = useProfilesStore()
const settings = useSettingsStore()
const connection = useConnectionStore()
const confirmBox = useConfirmStore()

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
/** Which card owns the current message, so it renders beside the button
 *  that produced it instead of after the last card. */
const toastCard = ref('')

function say(card: string, message: string) {
  toastCard.value = card
  toast.value = message
}

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
    say('export', `Exported to ${name}.`)
  } catch (e) {
    say('export', String(e))
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
  // restoreAll replaces settings and profiles wholesale, so this wipes
  // as much as the Reset card beside it — which asks for a typed token.
  if (
    !(await confirmBox.ask({
      title: 'Replace all current data?',
      body: 'The backup restores over your current settings, profiles, subscriptions and overrides. What is here now is not kept.',
      confirmLabel: 'Replace data',
      danger: true,
    }))
  )
    return
  busy.value = true
  importError.value = ''
  toast.value = ''
  try {
    await backend.importAllData(fileText.value)
    fileText.value = ''
    importSummary.value = ''
    await reloadStores()
    say('import', 'Import complete.')
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
  await refreshRaw()
  busy.value = false
}

/** Quiet re-read: the auto refresh must not disable the other three
 *  cards, so it stays off the shared busy flag. */
async function refreshRaw() {
  try {
    rawConfig.value = await backend.getRawConfig()
  } catch (e) {
    rawConfig.value = ''
    say('raw', String(e))
  }
}

// The card promises the exact document the core receives. Both the
// target profile and the stored overrides change it — the Core config
// cards below edit the overrides on this same screen.
watch(
  [
    () =>
      configTargetProfile(connection.status, settings.settings.activeProfileId, profiles.profiles)
        ?.id ?? '',
    () => JSON.stringify(settings.settings.coreOverrides ?? {}),
  ],
  () => {
    if (rawConfig.value) void refreshRaw()
  },
)

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

/** Disarming drops the typed token: keeping it would leave "Confirm
 *  reset" enabled the next time the card is armed, turning the two-step
 *  guard into one click. */
function toggleReset() {
  resetArmed.value = !resetArmed.value
  if (!resetArmed.value) resetToken.value = ''
}

async function doReset() {
  busy.value = true
  toast.value = ''
  try {
    await backend.resetToDefaults(resetToken.value)
    resetArmed.value = false
    resetToken.value = ''
    rawConfig.value = ''
    await reloadStores()
    say('reset', 'All data reset.')
  } catch (e) {
    say('reset', String(e))
  } finally {
    busy.value = false
  }
}
</script>
