import { defineStore } from 'pinia'
import { computed, ref, shallowRef } from 'vue'
import { backend } from '../api'
import { useElevationStore } from './elevation'
import { useSettingsStore } from './settings'
import type { ConnStatus, LogEntry, Stats } from '../types'

export const useConnectionStore = defineStore('connection', () => {
  const status = ref<ConnStatus>({ core: 'sing-box', profileId: '', running: false })
  const stats = ref<Stats>({ uplink: 0, downlink: 0, running: false, core: '' })
  // shallowRef: LogViewer only iterates logs, never mutates per-entry.
  // The poller replaces the whole array on each fetch, which shallowRef
  // tracks — avoiding deep Proxy traversal of the fetched window every poll.
  const logs = shallowRef<LogEntry[]>([])
  // What Clear hides, as an id watermark: the backend stamps every entry
  // from one monotonic counter, so "hidden up to N" needs no growing set of
  // ids. It lives here, not in LogViewer, because the Logs tab is mounted
  // with `v-else-if` — leaving it destroys the component, and a dismissal
  // list owned by that instance died with it, so every cleared line was
  // back the next time the tab opened.
  const logsClearedThrough = ref(0)
  const sniLogsClearedThrough = ref(0)
  const busy = ref(false)
  // connectBusy mirrors the backend in-flight Connect state (auto-connect
  // included): while true, Connect/Disconnect buttons stay disabled.
  const connectBusy = ref(false)
  // connectRequested covers the window the poller has not sampled yet:
  // between the click and the next status() tick connectBusy is still
  // false, and a button reading only the backend would say "Connect"
  // while one is running.
  const connectRequested = ref(false)
  // Set by a press of Cancel and read by connect()'s catch. The abort comes
  // back as the core manager's own error, and the plan the user just
  // executed is not a failure to report.
  const cancelRequested = ref(false)
  // True while the abort's Disconnect round-trip is in flight, so the
  // button can read "Cancelling…" and a second press does nothing.
  const cancelling = ref(false)
  // What the connect button asks: is a connect still cancellable? `busy`
  // is not the answer — it also covers Disconnect, whose press must not
  // turn the same button into a Cancel.
  const connecting = computed(() => connectRequested.value || connectBusy.value)
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
  // Ref count of mounted consumers for the LOGS poller (the configured
  // recent-logs window every 2s). Independent of subs: the logs timer only
  // exists while logsSubs > 0, so polling traffic disappears when the Logs
  // tab is closed.
  let logsSubs = 0
  // inFlight is the running poll leg rather than a boolean: an overlapping
  // timer tick still drops itself instead of piling concurrent Thrift calls
  // onto the non-concurrent client (frontend BUG-001), but a forced sample
  // queues behind it and then runs, so the refresh after connect/disconnect
  // is no longer silently skipped for up to a second.
  let inFlight: Promise<void> | null = null
  // A 2s read cadence keeps live logs responsive without polling the bridge
  // every second.
  const logPollMs = 2000
  let lastLogFetch = 0
  // The elevation store owns the relaunch dialog's state; this store only
  // asks, so the dialog can live in the shell rather than in a caller.
  const elevation = useElevationStore()
  const settings = useSettingsStore()

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
      const limit = settings.settings.consoleLogLimit ?? 300
      logs.value = await backend.recentLogs(limit)
    } catch (e) {
      noteError(e)
    }
  }

  /** Hide every log line fetched so far. A view action only: the backend
   *  buffer and the log file keep the history, and lines written after the
   *  clear carry larger ids, so they still show. */
  function clearLogs() {
    let max = 0
    for (const l of logs.value) if (l.id > max) max = l.id
    logsClearedThrough.value = max
  }

  function clearSniLogs(throughID: number) {
    sniLogsClearedThrough.value = Math.max(sniLogsClearedThrough.value, throughID)
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

  /** Fetch the configured recent-logs window (the 2s logs poller leg).
   *  Gated by logPollMs so it only actually crosses the bridge every 2s even
   *  though the timer fires every 1s; force skips that cadence gate. */
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
    return await runConnect(id, backend.connect)
  }

  // The one raise-the-tunnel path, used for a plain Connect and for a
  // Reconnect alike: the busy bookkeeping, the Cancel story and the
  // elevation handoff belong to the connect, not to whichever RPC got us
  // here.
  async function runConnect(
    id: string,
    call: (id: string) => Promise<unknown>,
  ): Promise<boolean> {
    if (busy.value) return false
    busy.value = true
    connectRequested.value = true
    cancelRequested.value = false
    error.value = ''
    try {
      await call(id)
      await refreshStatus(true)
      await refreshLogs(true)
      return true
    } catch (e) {
      const msg = String(e)
      // The button turning into Cancel is the user's answer to a connect
      // that is taking too long, and pulling it makes the backend stop the
      // engine it was still raising — which App.Connect reports as the
      // manager's "connect aborted (concurrent stop)". Expected, so it is
      // neither a failure to publish below nor a reason to offer
      // elevation, and the status refresh is what flips the button back.
      if (cancelRequested.value && msg.includes('connect aborted (concurrent stop)')) {
        await refreshStatus(true)
        await refreshLogs(true)
        return false
      }
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
      connectRequested.value = false
      cancelRequested.value = false
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

  /** Gives up on the Connect in flight. backend.disconnect() is the whole
   *  mechanism: stopping the core manager bumps its epoch, which is how a
   *  Start that is still raising the engine learns it has been superseded —
   *  the same lever a mid-connect Disconnect has always pulled, and the one
   *  that reaps a half-built Aether or Psiphon leg. It goes straight to the
   *  backend rather than through disconnect() above because that refuses
   *  while busy, and busy is exactly the state the connect being cancelled
   *  leaves the store in. An engine that was already up is taken down too,
   *  which is what the user asked for by pressing Cancel. */
  async function cancelConnect(): Promise<boolean> {
    if (!connecting.value || cancelling.value || cancelRequested.value) return false
    cancelRequested.value = true
    cancelling.value = true
    try {
      await backend.disconnect()
      await refreshStatus(true)
      return true
    } catch (e) {
      error.value = String(e)
      return false
    } finally {
      cancelling.value = false
    }
  }

  /** The one explicit "restart the tunnel" control: one backend call that
   *  stops and starts the same profile. Going through the backend rather
   *  than disconnect()+connect() is what makes the Reconnect mode mean
   *  anything — the chain leg a sticky reconnect hands over has to be
   *  spared inside the teardown, and a client-side Disconnect would
   *  already have reaped it by the time connect() ran. Beyond the profile
   *  id nothing is carried over: the chain, its Psiphon/Tor options and an
   *  Aether front are re-read from the saved profile on the way back up.
   *  There is no reuse of last time's endpoint and no retry on a dropped
   *  connection; this button is pressed by a person, not by a timer. */
  async function reconnect(id: string): Promise<boolean> {
    if (!id || busy.value || connectBusy.value) return false
    return await runConnect(id, backend.reconnect)
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
   *  conditionally by ConsolePanel when the Logs tab is active), so log
   *  polling stops entirely when no consumer needs it. */
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
    logsClearedThrough,
    sniLogsClearedThrough,
    busy,
    connectBusy,
    connecting,
    cancelling,
    error,
    fault,
    refresh,
    refreshStatus,
    refreshLogs,
    clearLogs,
    clearSniLogs,
    connect,
    disconnect,
    cancelConnect,
    reconnect,
    acquire,
    release,
    acquireLogs,
    releaseLogs,
  }
})
