import { defineStore } from 'pinia'
import { ref } from 'vue'
import { backend } from '../api'
import { syncThemeClass } from '../theme'
import type { Settings } from '../types'
import { defaultSettings as defaults } from '../types'

export const useSettingsStore = defineStore('settings', () => {
  const settings = ref<Settings>({ ...defaults })
  /** True after the first load() resolves (backend or defaults).
   *  App.vue renders a Splash until then so first paint never
   *  flashes the wrong shell. Frontend-only, never persisted. */
  const loaded = ref(false)

  async function load() {
    try {
      settings.value = await backend.getSettings()
    } catch {
      if (!backend.available()) settings.value = { ...defaults }
    } finally {
      loaded.value = true
    }
  }

  async function save() {
    await backend.saveSettings(settings.value)
  }

  /** Single shared code path for every theme switch: apply instantly, persist. */
  async function setTheme(theme: 'light' | 'dark') {
    settings.value.theme = theme
    syncThemeClass(theme) // instant, no reload
    try {
      await save()
    } catch {
      /* dev mode: keep local choice */
    }
  }

  /** Single shared code path for server selection (frontend BUG-004):
   *  set + await save with error propagation. On persistence failure
   *  the optimistic set is reverted so the UI never shows a selection
   *  that disk did not keep (restart/auto-connect would use the old one).
   *  Callers (ServersPanel, App.onSelectServer, ProxiesTable.connect)
   *  must all go through this action, never mutate + fire-and-forget. */
  async function setActiveProfileId(id: string) {
    const prev = settings.value.activeProfileId
    settings.value.activeProfileId = id
    try {
      await save()
    } catch (e) {
      settings.value.activeProfileId = prev
      throw e
    }
  }

  /** Legacy last-tab persistence kept for settings compatibility; the
   *  unified shell now keeps its current tab in place across modes. */
  async function setAdvancedLastTab(tab: string) {
    settings.value.advancedLastTab = tab
    try {
      await save()
    } catch {
      /* dev mode: keep local choice */
    }
  }

  /** The single Simple/Advanced switch for the unified shell. Disabling
   *  also clears peek state. */
  async function setAdvancedMode(on: boolean) {
    settings.value.advancedMode = on
    // Any mode switch exits peek: enabling lands in the hub, and
    // disabling must not leave a stale peek flag behind.
    settings.value.advancedPeekSimple = false
    try {
      await save()
    } catch {
      /* dev mode: keep local choice */
    }
  }

  /** Peek into the Simple shell without disabling Advanced Mode
   *  (advancedPeekSimple). The running core and profile are untouched:
   *  both shells read the same shared stores. */
  async function setAdvancedPeekSimple(on: boolean) {
    settings.value.advancedPeekSimple = on
    try {
      await save()
    } catch {
      /* dev mode: keep local choice */
    }
  }

  return { settings, loaded, load, save, setTheme, setActiveProfileId, setAdvancedLastTab, setAdvancedMode, setAdvancedPeekSimple }
})
