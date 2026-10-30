import { defineStore } from 'pinia'
import { ref, shallowRef } from 'vue'
import { backend } from '../api'
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
  // inFlight self-throttles both polls: a tick overlapping a slow
  // previous refresh returns immediately instead of piling concurrent
  // Thrift calls onto the non-concurrent client (frontend BUG-001).
  let inFlight = false
  // The log buffer is 300 entries (~68 KB of bridge payload per
  // fetch, measured). Refetching it every second spent ~4 MB/min on
  // the bridge for a list the user reads at 1s; the log view refreshes
  // every 2s instead — half the traffic, same window.
  const logPollMs = 2000
  let lastLogFetch = 0

  function noteError(e: unknown) {
    if (backend.available()) error.value = String(e)
  }

  /** Fetch status() + getStats() (the 1s status poller leg). */
  async function refreshStatus() {
    if (inFlight) return
    inFlight = true
    try {
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
    } finally {
      inFlight = false
    }
  }

  /** Fetch recentLogs(300) (the 2s logs poller leg). Gated by logPollMs
   *  so it only actually crosses the bridge every 2s even though the
   *  timer fires every 1s. */
  async function refreshLogs() {
    if (inFlight) return
    inFlight = true
    try {
      const now = Date.now()
      if (now - lastLogFetch >= logPollMs) {
        lastLogFetch = now
        try {
          logs.value = await backend.recentLogs(300)
        } catch (e) {
          noteError(e)
        }
      }
    } finally {
      inFlight = false
    }
  }

  /** Fetch everything: status + stats + logs. Used one-shot on mount
   *  and after connect/disconnect; the timers call the individual legs. */
  async function refresh() {
    await refreshStatus()
    await refreshLogs()
  }

  async function connect(id: string) {
    busy.value = true
    error.value = ''
    try {
      await backend.connect(id)
      await refreshStatus()
      await refreshLogs()
    } catch (e) {
      const msg = String(e)
      error.value = msg
      // TUN needs an elevated process for the Wintun adapter. The
      // backend refuses the connect with this actionable error and
      // persists the requested profile, so the existing
      // RelaunchAsAdmin handoff resumes it via --connect.
      if (msg.includes('TUN mode requires administrator rights') && backend.available()) {
        const relaunch = confirm('TUN mode requires administrator rights.\n\nRelaunch NekoGo as administrator? The app will restart and resume the connection.')
        if (relaunch) {
          try {
            await backend.relaunchAsAdmin()
          } catch (re) {
            error.value = String(re)
          }
        }
      }
    } finally {
      busy.value = false
    }
  }

  async function disconnect() {
    busy.value = true
    try {
      await backend.disconnect()
      await refreshStatus()
      await refreshLogs()
    } catch (e) {
      error.value = String(e)
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
