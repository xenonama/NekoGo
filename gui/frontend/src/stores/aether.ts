import { defineStore } from 'pinia'
import { ref, shallowRef } from 'vue'
import { backend } from '../api'
import type { AetherKeyInfo, AetherKeysSettings, AetherKeysStatus, AetherScanResult, AetherScanStatus, LogEntry, Profile } from '../types'

const MAX_LOGS = 500

/**
 * Aether scan state lives outside the tab component so switching tabs
 * cannot lose progress or results. One ref-counted 2s poller refreshes
 * the live status, the persisted results, and the aether log cursor.
 * acquire() on mount, release() on unmount: the interval starts on the
 * 0->1 edge and stops at 0, while the backend scan keeps running.
 * The WARP key renewal rides the same poller for the same reason.
 */
export const useAetherStore = defineStore('aether', () => {
  const scanStatus = ref<AetherScanStatus>({ running: false, done: 0, total: 0 })
  const scanResults = ref<AetherScanResult[]>([])
  // shallowRef: scanLogs is append-only (replaced wholesale on each poll),
  // the UI only iterates it — never mutates per-entry.
  const scanLogs = shallowRef<LogEntry[]>([])
  const error = ref('')
  const keysStatus = ref<AetherKeysStatus>({ running: false })
  const keysList = ref<AetherKeyInfo[]>([])
  const keysError = ref('')

  let timer: number | undefined
  let subs = 0
  let inFlight = false
  let lastLogID = 0
  // A user Cancel makes the blocking scan call reject with "aether scan
  // cancelled"; that is a normal outcome, not a failure to show in red.
  let cancelRequested = false
  let keysCancelRequested = false
  // A renewal this store owns: the poller must not read the backend's
  // status over the optimistic one until the run has actually begun, or
  // the button re-enables and a second run is offered.
  let keysBusy = false

  async function refresh() {
    if (inFlight) return
    inFlight = true
    try {
      if (!backend.available()) return
      scanStatus.value = await backend.aetherScanStatus()
      scanResults.value = await backend.aetherListScanResults()
      const chunk = await backend.aetherScanLogs(lastLogID)
      if (chunk.length > 0) {
        lastLogID = chunk.reduce((max, e) => Math.max(max, Number(e.id ?? 0)), lastLogID)
        scanLogs.value = [...scanLogs.value, ...chunk].slice(-MAX_LOGS)
      }
      await refreshKeys()
    } catch {
      /* dev mode or a transient IPC failure: keep the last state */
    } finally {
      inFlight = false
    }
  }

  // Reading the identities settles a renewal that finished but could not
  // move every file, so it is left alone while one is still writing.
  async function refreshKeys() {
    if (keysBusy) return
    const was = keysStatus.value.running
    const st = await backend.aetherKeysProgress()
    keysStatus.value = st
    if (!st.running || !was) keysList.value = await backend.aetherKeysList()
  }

  function acquire() {
    subs += 1
    if (subs === 1) {
      void refresh()
      timer = window.setInterval(() => void refresh(), 2000)
    }
  }

  function release() {
    subs = Math.max(0, subs - 1)
    if (subs === 0 && timer !== undefined) {
      window.clearInterval(timer)
      timer = undefined
    }
  }

  async function startScan(count = 1, timeoutMs = 480000, profile?: Profile): Promise<void> {
    error.value = ''
    cancelRequested = false
    scanStatus.value = { running: true, done: 0, total: count }
    try {
      if (profile) await backend.aetherScanProfileEndpoints(profile, count, timeoutMs)
      else await backend.aetherScanEndpoints(count, timeoutMs)
    } catch (e) {
      if (!cancelRequested) error.value = String(e)
    } finally {
      cancelRequested = false
      await refresh()
    }
  }

  async function cancelScan() {
    error.value = ''
    cancelRequested = true
    try {
      await backend.aetherCancelScan()
    } catch (e) {
      error.value = String(e)
      cancelRequested = false
    } finally {
      await refresh()
    }
  }

  async function clearResults() {
    error.value = ''
    try {
      await backend.aetherClearScanResults()
    } catch (e) {
      error.value = String(e)
    } finally {
      await refresh()
    }
  }

  function clearLogs() {
    scanLogs.value = []
  }

  async function saveAsConfig(index: number, name = ''): Promise<Profile> {
    error.value = ''
    try {
      const saved = await backend.aetherSaveScanResultAsConfig(index, name)
      await refresh()
      return saved
    } catch (e) {
      error.value = String(e)
      throw e
    }
  }

  /** Registers new WARP keys. Blocks for the whole run (minutes with a
   *  carrier); the outcome is reported through keysStatus either way. */
  async function renewKeys(s: AetherKeysSettings): Promise<void> {
    keysError.value = ''
    keysCancelRequested = false
    keysBusy = true
    keysStatus.value = { running: true, kind: s.kind || 'all' }
    try {
      keysList.value = await backend.aetherRenewKeys(s)
    } catch (e) {
      if (!keysCancelRequested) keysError.value = String(e)
    } finally {
      keysCancelRequested = false
      keysBusy = false
      await refreshKeys()
    }
  }

  async function cancelKeys() {
    keysCancelRequested = true
    try {
      await backend.aetherCancelKeys()
    } catch (e) {
      keysError.value = String(e)
      keysCancelRequested = false
    }
  }

  return {
    scanStatus,
    scanResults,
    scanLogs,
    error,
    keysStatus,
    keysList,
    keysError,
    refresh,
    refreshKeys,
    acquire,
    release,
    startScan,
    cancelScan,
    clearResults,
    clearLogs,
    saveAsConfig,
    renewKeys,
    cancelKeys,
  }
})
