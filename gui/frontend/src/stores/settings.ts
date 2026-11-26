import { defineStore } from 'pinia'
import { ref } from 'vue'
import { backend } from '../api'
import { syncThemeClass } from '../theme'
import type { Settings } from '../types'
import { defaultSettings as defaults } from '../types'

export const useSettingsStore = defineStore('settings', () => {
  const settings = ref<Settings>({ ...defaults })
  let persistedSettings = snapshot(defaults)
  /** True after the first load() resolves (backend or defaults).
   *  App.vue renders a Splash until then so first paint never
   *  flashes the wrong shell. Frontend-only, never persisted. */
  const loaded = ref(false)

  async function load() {
    try {
      const loadedSettings = await backend.getSettings()
      settings.value = loadedSettings
      persistedSettings = snapshot(loadedSettings)
    } catch {
      if (!backend.available()) {
        settings.value = { ...defaults }
        persistedSettings = snapshot(defaults)
      }
    } finally {
      loaded.value = true
    }
  }

  type SettingField = keyof Settings

  function snapshot(value: Settings): Settings {
    return JSON.parse(JSON.stringify(value)) as Settings
  }

  function restoreField<K extends SettingField>(field: K) {
    const value = persistedSettings[field]
    settings.value[field] = (value === undefined ? value : JSON.parse(JSON.stringify(value))) as Settings[K]
  }

  let fieldRevision = 0
  const fieldRevisions: Partial<Record<SettingField, number>> = {}
  let saveQueue: Promise<void> = Promise.resolve()

  function claimFields(...fields: SettingField[]) {
    const revision = ++fieldRevision
    for (const field of fields) fieldRevisions[field] = revision
    return revision
  }

  function fieldsUnchanged(revision: number, ...fields: SettingField[]) {
    return fields.every((field) => fieldRevisions[field] === revision)
  }

  function save(): Promise<void> {
    const pending = saveQueue.then(async () => {
      const next = snapshot(settings.value)
      await backend.saveSettings(next)
      persistedSettings = next
    })
    saveQueue = pending.then(() => undefined, () => undefined)
    return pending
  }

  async function setConsoleLogLimit(limit: number) {
    const revision = claimFields('consoleLogLimit')
    settings.value.consoleLogLimit = limit
    try {
      await save()
    } catch (e) {
      if (backend.available()) {
        if (fieldsUnchanged(revision, 'consoleLogLimit')) restoreField('consoleLogLimit')
        throw e
      }
    }
  }

  /** Single shared code path for every theme switch: apply instantly, persist. */
  async function setTheme(theme: 'light' | 'dark') {
    const revision = claimFields('theme')
    settings.value.theme = theme
    syncThemeClass(theme) // instant, no reload
    try {
      await save()
    } catch (e) {
      if (backend.available()) {
        if (fieldsUnchanged(revision, 'theme')) {
          restoreField('theme')
          syncThemeClass(settings.value.theme)
        }
        throw e
      }
    }
  }

  /** Single shared code path for server selection (frontend BUG-004):
   *  set + await save with error propagation. On persistence failure
   *  the optimistic set is reverted so the UI never shows a selection
   *  that disk did not keep (restart/auto-connect would use the old one).
   *  Callers (ServersPanel, App.onSelectServer, ProxiesTable.connect)
   *  must all go through this action, never mutate + fire-and-forget. */
  async function setActiveProfileId(id: string) {
    const revision = claimFields('activeProfileId')
    settings.value.activeProfileId = id
    if (!backend.available()) return
    try {
      await save()
    } catch (e) {
      if (fieldsUnchanged(revision, 'activeProfileId')) restoreField('activeProfileId')
      throw e
    }
  }

  /** Legacy last-tab persistence kept for settings compatibility; the
   *  unified shell now keeps its current tab in place across modes. */
  async function setAdvancedLastTab(tab: string) {
    const revision = claimFields('advancedLastTab')
    settings.value.advancedLastTab = tab
    try {
      await save()
    } catch (e) {
      if (backend.available()) {
        if (fieldsUnchanged(revision, 'advancedLastTab')) restoreField('advancedLastTab')
        throw e
      }
    }
  }

  /** The single Simple/Advanced switch for the unified shell. Disabling
   *  also clears peek state. */
  async function setAdvancedMode(on: boolean) {
    const revision = claimFields('advancedMode', 'advancedPeekSimple')
    settings.value.advancedMode = on
    // Any mode switch exits peek: enabling lands in the hub, and
    // disabling must not leave a stale peek flag behind.
    settings.value.advancedPeekSimple = false
    try {
      await save()
    } catch (e) {
      if (backend.available()) {
        if (fieldsUnchanged(revision, 'advancedMode')) restoreField('advancedMode')
        if (fieldsUnchanged(revision, 'advancedPeekSimple')) restoreField('advancedPeekSimple')
        throw e
      }
    }
  }

  /** Peek into the Simple shell without disabling Advanced Mode
   *  (advancedPeekSimple). The running core and profile are untouched:
   *  both shells read the same shared stores. */
  async function setAdvancedPeekSimple(on: boolean) {
    const revision = claimFields('advancedPeekSimple')
    settings.value.advancedPeekSimple = on
    try {
      await save()
    } catch (e) {
      if (backend.available()) {
        if (fieldsUnchanged(revision, 'advancedPeekSimple')) restoreField('advancedPeekSimple')
        throw e
      }
    }
  }

  return { settings, loaded, load, save, setConsoleLogLimit, setTheme, setActiveProfileId, setAdvancedLastTab, setAdvancedMode, setAdvancedPeekSimple }
})
