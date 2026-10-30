<!-- One shell for both modes: the nav, pages and right panel never
     remount on a mode switch. Advanced only reveals extra sections
     inside the same Simple tabs. -->
<template>
  <div v-if="!settings.loaded" class="nb-splash">
    <img class="nb-logo-img" :src="appIcon" alt="NekoGo" />
    <span class="nb-spin" title="Loading…" />
  </div>
  <SimpleShell
    v-if="settings.loaded"
    :version="version"
    :mode="settings.settings.advancedMode ? 'advanced' : 'simple'"
    :startup-error="startupError"
    @dismiss-error="startupError = ''"
  />
  <CustomContextMenu v-if="settings.loaded" />
</template>

<script setup lang="ts">
import { onMounted, onUnmounted, ref, watch } from 'vue'
import { useProfilesStore } from './stores/profiles'
import { useSettingsStore } from './stores/settings'
import { useConnectionStore } from './stores/connection'
import { useSubscriptionsStore } from './stores/subscriptions'
import { backend } from './api'
import { syncThemeClass } from './theme'
import appIcon from './assets/app.png'
import SimpleShell from './components/SimpleShell.vue'
import CustomContextMenu from './components/CustomContextMenu.vue'

const profiles = useProfilesStore()
const settings = useSettingsStore()
const connection = useConnectionStore()
const version = ref('1.0.0')
// Last startup/tray-level backend error (auto-connect etc.), if any.
const startupError = ref('')

async function reconcileSelection() {
  const runningId = connection.status.running ? connection.status.profileId : ''
  if (runningId) {
    // A live core is truth: adopt its profile when we disagree.
    if (settings.settings.activeProfileId !== runningId) {
      try {
        await settings.setActiveProfileId(runningId)
      } catch {
        /* offline/dev: keep memory state */
      }
    }
    return
  }
  const stored = settings.settings.activeProfileId
  if (!profiles.loaded) {
    // The list never loaded (transient IPC failure at startup): an empty
    // array says nothing about the stored id, and clearing it here threw
    // away the user's selection (and the auto-connect target) for good.
    return
  }
  if (stored && !profiles.profiles.some((p) => p.id === stored)) {
    // Dangling target (deleted selection / never-persisted failure):
    // clear so auto-connect cannot aim at a dead id.
    try {
      await settings.setActiveProfileId('')
    } catch {
      settings.settings.activeProfileId = ''
    }
    return
  }
  if (!stored && profiles.profiles.length > 0) {
    try {
      await settings.setActiveProfileId(profiles.profiles[0].id)
    } catch {
      /* offline/dev: keep memory state */
    }
  }
}

const subs = useSubscriptionsStore()

// A mode switch is just a prop flip on the same shell: same stores,
// same scroll, no refetch. Wholesale changes (backup import/reset)
// still reload via stores/sync.ts.

// Throne parity (sub_auto_update): re-fetch every subscription on a
// frontend-owned timer. Backend stays on-demand; the stores refresh
// in place so the shell shows the new profiles. Skips while offline
// or while a manual update runs.
let subTimer = 0

function armSubAutoUpdate() {
  window.clearInterval(subTimer)
  subTimer = 0
  const mins = Number(settings.settings.subAutoUpdateM ?? 0)
  if (!mins || mins <= 0) return
  const ms = Math.max(5, Math.min(24 * 60, mins)) * 60 * 1000
  subTimer = window.setInterval(async () => {
    if (!backend.available() || subs.busy) return
    try {
      await subs.updateAll()
      await profiles.load()
    } catch {
      /* message already stored on the subs store */
    }
  }, ms)
}

watch(
  () => settings.settings.subAutoUpdateM,
  () => armSubAutoUpdate(),
)

onUnmounted(() => {
  window.clearInterval(subTimer)
  connection.release()
})

onMounted(async () => {
  await backend.init()
  await settings.load()
  syncThemeClass(settings.settings.theme)
  await profiles.load()
  try {
    await subs.load()
  } catch { /* dev mode */
  }
  await connection.refresh()
  // BUG-012: reconcile the selection on load. A running core wins (its
  // profile is truth); otherwise a stored id with no matching profile
  // is dangling (failed connect / deleted selection) and must not
  // survive as the auto-connect target. All writes go through the
  // persisting action, never a bare mutation.
  await reconcileSelection()
  // Ref-counted poller: acquires on mount so live connection state stays
  // fresh while the app is loaded. Released on unmount (app teardown).
  connection.acquire()
  armSubAutoUpdate()
  try {
    startupError.value = await backend.lastError()
  } catch {
    startupError.value = ''
  }
  try {
    version.value = await backend.appVersion()
  } catch {
    version.value = '1.0.0'
  }
})
</script>
