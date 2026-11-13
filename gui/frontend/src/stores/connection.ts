import { defineStore } from 'pinia'
import { ref, shallowRef } from 'vue'
import { backend } from '../api'
import { useElevationStore } from './elevation'
import type { ConnStatus, LogEntry, Stats } from '../types'

export const useConnectionStore = defineStore('connection', () => {
  const status = ref<ConnStatus>({ core: 'sing-box', profileId: '', running: false })
  const stats = ref<Stats>({ uplink: 0, downlink: 0, running: false, core: '' })
  // shallowRef: LogViewer only iterates logs, never mutates per-entry.
  // The poller replaces the whole array on each fetch, which shallowRef
  // tracks — avoiding deep Proxy traversal of 300 entries every poll.
  const logs = shallowRef<LogEntry[]>([])
  const busy = ref(false)
  // connectBusy mirrors the backend in-flight Connect state (auto-connect
  // included): while true, Connect/Disconnect buttons stay disabled.
  const connectBusy = ref(false)
  const error = ref('')
  // fault mirrors the backend's last engine-death report. Kept apart
  // from error (a Connect failure) so a successful reconnect does not
  // erase the crash story, and vice versa.
  const fault = ref('')
  let timer: number | undefined
  let logsTimer: number | undefined
  // Ref count of mounted consumers for the STATUS poller (status() +
  // getStats() every 1s). Starts on 0->1, stops on 1->0 so these two
  // calls are not fetched when no tab needs live data.
  let subs = 0
  // Ref count of mounted consumers for the LOGS poller (recentLogs(300)
  // every 2s). Independent of subs: the logs timer only exists while
  // logsSubs > 0, so ~68 KB/min of bridge traffic disappears when the
  // Logs tab is closed.
  let logsSubs = 0
  // inFlight is the running poll leg rather than a boolean: an overlapping
  // timer tick still drops itself instead of piling concurrent Thrift calls
  // onto the non-concurrent client (frontend BUG-001), but a forced sample
  // queues behind it and then runs, so the refresh after connect/disconnect
  // is no longer silently skipped for up to a second.
  let inFlight: Promise<void> | null = null
  // The log buffer is 300 entries (~68 KB of bridge payload per
  // fetch, measured). Refetching it every second spent ~4 MB/min on
  // the bridge for a list the user reads at 1s; the log view refreshes
  // every 2s instead — half the traffic, same window.
  const logPollMs = 2000
  let lastLogFetch = 0
  // The elevation store owns the relaunch dialog's state; this store only
  // asks, so the dialog can live in the shell rather than in a caller.
  const elevation = useElevationStore()

  function noteError(e: unknown) {
    if (backend.available()) error.value = String(e)
  }

  async function sampleStatus() {
    try {
      status.value = await backend.status()
      connectBusy.value = status.value.connectBusy === true
      fault.value = status.value.lastError ?? ''
    } catch (e) {
      noteError(e)
    }
    try {
      stats.value = await backend.getStats()
    } catch (e) {
      noteError(e)
    }
  }

  async function sampleLogs(force: boolean) {
    const now = Date.now()
    if (!force && now - lastLogFetch < logPollMs) return
    lastLogFetch = now
    try {
      logs.value = await backend.recentLogs(300)
    } catch (e) {
      noteError(e)
    }
  }

  /** Run one poll leg, serialized against any leg already running. The
   *  samples swallow their own errors, so the chain never rejects. */
  async function poll(sample: () => Promise<void>, force: boolean) {
    const prev = inFlight
    if (prev && !force) return
    const run = prev ? prev.then(sample) : sample()
    inFlight = run
    try {
      await run
    } finally {
      if (inFlight === run) inFlight = null
    }
  }

  /** Fetch status() + getStats() (the 1s status poller leg). */
  async function refreshStatus(force = false) {
    await poll(sampleStatus, force)
  }

  /** Fetch recentLogs(300) (the 2s logs poller leg). Gated by logPollMs
   *  so it only actually crosses the bridge every 2s even though the
   *  timer fires every 1s; force skips that cadence gate. */
  async function refreshLogs(force = false) {
    await poll(() => sampleLogs(force), force)
  }

  /** Fetch everything: status + stats + logs. Used one-shot on mount
   *  and after connect/disconnect; the timers call the individual legs. */
  async function refresh() {
    await refreshStatus()
    await refreshLogs()
  }

  /** Starts a profile. Resolves true only when the backend accepted the
   *  connect — callers that act on the new tunnel (speed/IP probes, row
   *  feedback) must check it instead of reading `error`, which the poller
   *  also writes. The guard is synchronous: two components clicking in the
   *  same tick must not both reach backend.connect. */
  async function connect(id: string): Promise<boolean> {
    if (busy.value) return false
    busy.value = true
    error.value = ''
    try {
      await backend.connect(id)
      await refreshStatus(true)
      await refreshLogs(true)
      return true
    } catch (e) {
      const msg = String(e)
      error.value = msg
      // TUN needs an elevated process for the Wintun adapter, and the SNI
      // spoof engine needs one for WinDivert. The backend refuses the
      // connect with an actionable error that names which, and persists the
      // requested profile, so the existing RelaunchAsAdmin handoff resumes
      // it via --connect. Both messages carry this phrase — matching only
      // the TUN one left a spoof connect failing on a driver error with no
      // way to fix it from the UI.
      if (msg.includes('requires administrator rights') && backend.available()) {
        // The app's own dialog: window.confirm renders as a browser box
        // in WebView2, and this is the ask that restarts the process.
        if (await elevation.ask(msg)) {
          try {
            await backend.relaunchAsAdmin()
          } catch (re) {
            error.value = String(re)
          }
        }
      }
      return false
    } finally {
      busy.value = false
    }
  }

  /** Stops the running core. Same contract as connect: true only when the
   *  backend accepted it. */
  async function disconnect(): Promise<boolean> {
    if (busy.value) return false
    busy.value = true
    try {
      await backend.disconnect()
      await refreshStatus(true)
      await refreshLogs(true)
      return true
    } catch (e) {
      error.value = String(e)
      return false
    } finally {
      busy.value = false
    }
  }

  /** Mounted-consumer ref count for the STATUS poller. Starts the 1s
   *  timer on 0->1, stops it on 1->0. Called by Sidebar, ConnectionCard,
   *  TrafficStats, TrafficChart, SettingsPanel, ProxiesTable, ServerList,
   *  About, AetherPanel, CoreConfigEditor, SimpleShell, QuickActions,
   *  and App.vue. */
  function acquire() {
    subs += 1
    if (subs === 1) {
      void refreshStatus()
      // 1s cadence so the connection-card up/down speeds feel real-time.
      timer = window.setInterval(refreshStatus, 1000)
    }
  }

  function release() {
    subs = Math.max(0, subs - 1)
    if (subs === 0 && timer !== undefined) {
      window.clearInterval(timer)
      timer = undefined
    }
  }

  /** Mounted-consumer ref count for the LOGS poller. Starts the 2s
   *  timer on 0->1, stops it on 1->0. Called only by LogViewer (mounted
   *  conditionally by ConsolePanel when the Logs tab is active). This is
   *  where the ~2 MB/min idle bridge win comes from: when no consumer
   *  needs logs, recentLogs(300) stops firing entirely. */
  function acquireLogs() {
    logsSubs += 1
    if (logsSubs === 1) {
      void refreshLogs()
      // 2s cadence matches the log view's read rate.
      logsTimer = window.setInterval(refreshLogs, 2000)
    }
  }

  function releaseLogs() {
    logsSubs = Math.max(0, logsSubs - 1)
    if (logsSubs === 0 && logsTimer !== undefined) {
      window.clearInterval(logsTimer)
      logsTimer = undefined
    }
  }

  return {
    status,
    stats,
    logs,
    busy,
    connectBusy,
    error,
    fault,
    refresh,
    refreshStatus,
    refreshLogs,
    connect,
    disconnect,
    acquire,
    release,
    acquireLogs,
    releaseLogs,
  }
})
