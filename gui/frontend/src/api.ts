// Backend bridge. In production (Wails3 webview) calls go to the bound Go
// service via the generated bindings in ../../bindings (wails3 generate
// bindings -ts, using @wailsio/runtime $Call.ByID).
// During `vite dev` (plain browser, no Wails runtime) the probe in init()
// fails and the stores fall back to mock data.
import * as App from '../bindings/nekogo/app.js'
import { ref } from 'vue'
import type { AetherScanResult, AetherScanStatus, ConnectionList, ConnStatus, CountryRefreshResult, GeoIPStatus, IPTestResult, LastNetworkResults, LinkImportResult, LogEntry, Profile, Settings, SNIPairResult, SNIScanStatus, SNIConfig, SpoofStats, SpoofStatus, SpeedTestResult, Stats, Subscription, TestResult, UpdatableComponent, URLTestResult, WgKeyPair } from './types'
import { defaultSettings } from './types'

let useBackend = false
let probed = false

/** Reactive mirror of backend availability (BUG-017): `available()` is a
 *  plain closure templates cannot watch, so views bind to this ref for
 *  offline-disabled states instead. Set once by init(). */
export const backendAvailable = ref(false)

function withTimeout<T>(p: Promise<T>, ms: number): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const t = window.setTimeout(() => reject(new Error('backend probe timeout')), ms)
    Promise.resolve(p).then(
      (v) => {
        window.clearTimeout(t)
        resolve(v)
      },
      (e) => {
        window.clearTimeout(t)
        reject(e)
      },
    )
  })
}

const emptyStats: Stats = { uplink: 0, downlink: 0, running: false, core: '' }

export const backend = {
  available: () => backendAvailable.value,

  /** Probe the Wails runtime once. Must run before any store loads. */
  async init(): Promise<boolean> {
    if (probed) return useBackend
    probed = true
    try {
      await withTimeout(App.AppVersion(), 2000)
      useBackend = true
    } catch {
      useBackend = false
    }
    backendAvailable.value = useBackend
    return useBackend
  },

  listProfiles: () => Promise.resolve(App.ListProfiles()) as Promise<Profile[]>,
  saveProfile: (p: Profile) =>
    Promise.resolve(
      App.SaveProfile(p as unknown as Parameters<typeof App.SaveProfile>[0]),
    ) as Promise<Profile>,
  deleteProfile: (id: string) => Promise.resolve(App.DeleteProfile(id)),
  // BUG-013: fall back to defaults when the binding ever resolves
  // null/undefined, so the shell never deref-crashes on startup.
  getSettings: async (): Promise<Settings> =>
    (await App.GetSettings()) ?? { ...defaultSettings },
  saveSettings: (s: Settings) =>
    Promise.resolve(App.SaveSettings(s as unknown as Parameters<typeof App.SaveSettings>[0])),
  connect: (id: string) => Promise.resolve(App.Connect(id)),
  disconnect: () => Promise.resolve(App.Disconnect()),
  aetherScan: async (p: Profile): Promise<AetherScanResult> => {
    const r = await App.AetherScan(p as unknown as Parameters<typeof App.AetherScan>[0])
    if (!r) throw new Error('scan returned no endpoint')
    return r as unknown as AetherScanResult
  },
  aetherScanCancel: () => Promise.resolve(App.AetherScanCancel()),
  aetherScanEndpoints: async (count = 1, timeoutMs = 0): Promise<AetherScanResult[]> =>
    (await App.AetherScanEndpoints(count, timeoutMs)) ?? [],
  aetherScanProfileEndpoints: async (
    p: Profile,
    count = 1,
    timeoutMs = 0,
  ): Promise<AetherScanResult[]> =>
    (await App.AetherScanProfileEndpoints(
      p as unknown as Parameters<typeof App.AetherScanProfileEndpoints>[0],
      count,
      timeoutMs,
    )) ?? [],
  aetherScanStatus: async (): Promise<AetherScanStatus> =>
    (await App.AetherScanStatus()) ?? { running: false, done: 0, total: 0 },
  aetherCancelScan: () => Promise.resolve(App.AetherCancelScan()),
  aetherListScanResults: async (): Promise<AetherScanResult[]> =>
    (await App.AetherListScanResults()) ?? [],
  aetherClearScanResults: () => Promise.resolve(App.AetherClearScanResults()),
  aetherSaveScanResultAsConfig: async (index: number, name = ''): Promise<Profile> =>
    (await App.AetherSaveScanResultAsConfig(index, name)) as unknown as Profile,
  aetherScanLogs: async (sinceID = 0): Promise<LogEntry[]> =>
    (await App.AetherScanLogs(sinceID)) ?? [],
  status: async (): Promise<ConnStatus> => {
    const m = await App.Status()
    return {
      core: String(m?.['core'] ?? 'sing-box'),
      profileId: String(m?.['profileId'] ?? ''),
      running: Boolean(m?.['running']),
      connectBusy: Boolean(m?.['connectBusy']),
      lastError: String(m?.['lastError'] ?? ''),
    }
  },
  getStats: async (): Promise<Stats> => {
    const s = await App.GetStats()
    if (!s) return { ...emptyStats }
    return {
      uplink: s.uplink ?? 0,
      downlink: s.downlink ?? 0,
      uplinkByTag: s.uplinkByTag as Record<string, number> | undefined,
      downlinkByTag: s.downlinkByTag as Record<string, number> | undefined,
      running: s.running ?? false,
      core: s.core ?? '',
      apiDown: (s as { apiDown?: boolean }).apiDown ?? false,
    }
  },
  lastError: () => Promise.resolve(App.LastError()),
  // BUG-014: the plain listConnections wrapper projected only
  // connections, silently dropping the Truncated flag (capped at 500)
  // — deleted. All callers use listConnectionsFull, which preserves it.
  listConnectionsFull: () =>
    Promise.resolve(App.ListConnections()) as Promise<ConnectionList>,
  testProfile: async (id: string, timeoutMs: number): Promise<TestResult> => {
    const r = await App.TestProfile(id, timeoutMs)
    if (!r) return { outboundTag: '', latencyMs: -1, error: 'no result' }
    return { outboundTag: r.outboundTag ?? '', latencyMs: r.latencyMs ?? -1, error: r.error }
  },
  recentLogs: (n = 300) => Promise.resolve(App.RecentLogs(n)) as Promise<LogEntry[]>,
  appVersion: () => Promise.resolve(App.AppVersion()),
  openURL: (raw: string) => Promise.resolve(App.OpenURL(raw)),
  coreVersion: () => Promise.resolve(App.CoreVersion()),
  xrayVersion: () => Promise.resolve(App.XrayVersion()),
  runtimeInfo: () => Promise.resolve(App.RuntimeInfo()) as Promise<Record<string, string>>,
  openConfigFolder: () => Promise.resolve(App.OpenConfigFolder()),
  detectCountry: async (id: string): Promise<Profile> => {
    const r = await App.DetectCountry(id)
    if (!r) throw new Error('empty country result')
    return r as unknown as Profile
  },
  refreshAllCountries: () =>
    Promise.resolve(App.RefreshAllCountries()) as Promise<CountryRefreshResult>,
  refreshCountriesProgress: () =>
    Promise.resolve(App.RefreshCountriesProgress()) as Promise<{ done: number; total: number; running: boolean }>,
  cancelRefreshCountries: () =>
    Promise.resolve(App.CancelRefreshCountries()),
  clearCountries: () => Promise.resolve(App.ClearCountries()),
  geoIPStatus: async (): Promise<GeoIPStatus> =>
    (await App.GeoIPStatus()) ?? { available: false, path: '', error: 'empty result' },
  /** DEBUG-ONLY: runs the window-close decision branch, returns hide|quit. */
  debugWindowClose: () => Promise.resolve(App.DebugWindowClose()),
  setCloseToTray: (enabled: boolean) =>
    Promise.resolve(App.SetCloseToTray(enabled)),
  getAutoRun: () =>
    Promise.resolve(App.GetAutoRun()),
  setStartOnBoot: (enabled: boolean) =>
    Promise.resolve(App.SetStartOnBoot(enabled)),
  listUpdatables: () =>
    Promise.resolve(App.ListUpdatables()) as Promise<UpdatableComponent[]>,
  checkUpdates: () =>
    Promise.resolve(App.CheckUpdates()) as Promise<UpdatableComponent[]>,
  updateComponent: async (id: string): Promise<UpdatableComponent> =>
    (await App.UpdateComponent(id)) ?? { id, name: id, current: '', latest: '', updateAvail: false, lastError: 'empty result' },
  previewLinks: async (text: string): Promise<LinkImportResult> => {
    const r = await App.PreviewLinks(text)
    if (!r) return { profiles: [], invalid: 0 }
    return { profiles: (r.profiles ?? []) as unknown as Profile[], invalid: r.invalid ?? 0 }
  },
  importLinks: async (text: string): Promise<LinkImportResult> => {
    const r = await App.ImportLinks(text)
    if (!r) return { profiles: [], invalid: 0 }
    return { profiles: (r.profiles ?? []) as unknown as Profile[], invalid: r.invalid ?? 0 }
  },
  updateSubscriptions: async (): Promise<LinkImportResult> => {
    const r = await App.UpdateSubscriptions()
    if (!r) return { profiles: [], invalid: 0 }
    return { profiles: (r.profiles ?? []) as unknown as Profile[], invalid: r.invalid ?? 0 }
  },
  listSubscriptions: async (): Promise<Subscription[]> =>
    (await App.ListSubscriptions()) ?? [],
  /** Removes a feed record; with alsoProfiles its imported profiles go
   *  too. Returns the number of deleted profiles. */
  deleteSubscription: async (url: string, alsoProfiles: boolean): Promise<number> =>
    (await App.DeleteSubscription(url, alsoProfiles)) ?? 0,
  setSubscriptionEnabled: (url: string, enabled: boolean): Promise<void> =>
    Promise.resolve(App.SetSubscriptionEnabled(url, enabled)),
  editSubscription: async (oldUrl: string, newUrl: string, name: string): Promise<LinkImportResult> => {
    const r = await App.EditSubscription(oldUrl, newUrl, name)
    if (!r) return { profiles: [], invalid: 0 }
    return { profiles: (r.profiles ?? []) as unknown as Profile[], invalid: r.invalid ?? 0 }
  },
  buildShareLink: (id: string) =>
    Promise.resolve(App.BuildShareLink(id)),
  // Advanced Mode Phase C: core network tools (Console -> Network).
  // BUG-016: every pointer-returning binding can resolve null — guard
  // like the testProfile/previewLinks siblings instead of casting blind.
  startIPTest: async (): Promise<IPTestResult> =>
    (await App.StartIPTest()) ?? { error: 'empty result' },
  startSpeedTest: async (): Promise<SpeedTestResult> =>
    (await App.StartSpeedTest()) ?? { error: 'empty result' },
  startURLTest: async (url: string, timeoutMs: number): Promise<URLTestResult> =>
    (await App.StartURLTest(url, timeoutMs)) ?? { url, latencyMs: -1, ok: false, error: 'empty result' },
  stopTest: () => Promise.resolve(App.StopTest()),
  lastNetworkResults: async (): Promise<LastNetworkResults> => {
    // HOTFIX: the Go side returns an empty struct when idle, but a null
    // can still arrive (rejected-then-caught shapes, older bindings).
    // BUG-015: normalize to { speedRunning: false } — speedRunning is a
    // required boolean in the contract, and {} leaves it undefined.
    const r = await App.LastNetworkResults()
    return ({ speedRunning: false, ...((r ?? {}) as unknown as Record<string, unknown>) } as unknown) as LastNetworkResults
  },
  // Advanced Mode Phase E: core config section editors
  // (Settings -> Advanced). Get returns pretty section JSON
  // (override or manager default); Set validates, stores and
  // returns the normalized JSON; Reset drops the override.
  getCoreConfigSection: (section: string) =>
    Promise.resolve(App.GetCoreConfigSection(section)),
  setCoreConfigSection: (section: string, jsonText: string) =>
    Promise.resolve(App.SetCoreConfigSection(section, jsonText)),
  resetCoreConfigSection: (section: string) =>
    Promise.resolve(App.ResetCoreConfigSection(section)),
  // Advanced Mode Phase G: raw config editor
  // (Settings -> Advanced -> Raw Config).
  getRawConfig: () => Promise.resolve(App.GetRawConfig()),
  setRawConfig: (jsonText: string) => Promise.resolve(App.SetRawConfig(jsonText)),
  reloadCoreNow: () => Promise.resolve(App.ReloadCoreNow()),
  // Advanced Mode Phase F: backup, restore, reset
  // (Settings -> Advanced -> Backup).
  exportAllData: () => Promise.resolve(App.ExportAllData()),
  importAllData: (blob: string) => Promise.resolve(App.ImportAllData(blob)),
  viewRawConfig: () => Promise.resolve(App.ViewRawConfig()),
  resetToDefaults: (confirm: string) => Promise.resolve(App.ResetToDefaults(confirm)),
  relaunchAsAdmin: () =>
    Promise.resolve(App.RelaunchAsAdmin()),
  // Advanced Mode Phase D: system tools (Console -> System).
  isPrivileged: () => Promise.resolve(App.IsPrivileged()),
  enableSystemProxyGUI: () =>
    Promise.resolve(App.EnableSystemProxyGUI()),
  disableSystemProxyGUI: () =>
    Promise.resolve(App.DisableSystemProxyGUI()),
  getSystemProxyState: () =>
    Promise.resolve(App.GetSystemProxyState()),
  setSystemDNS: (clear: boolean) =>
    Promise.resolve(App.SetSystemDNS(clear)),
  isSupported: (kind: string) =>
    Promise.resolve(App.IsSupported(kind)),
  genWgKeyPair: async (): Promise<WgKeyPair> => {
    const r = await App.GenWgKeyPair()
    if (!r) throw new Error('empty keypair result')
    return r as unknown as WgKeyPair
  },
  cacheHTTP: (url: string) => Promise.resolve(App.CacheHTTP(url)),
  // SNI Spoofing tab (sni-spoof.exe engine, CoreSpoof).
  spoofGetStatus: async (): Promise<SpoofStatus> =>
    (await App.SpoofGetStatus()) ?? { running: false, listenPort: 0, startedAt: '', uptime: 0, lastError: '' },
  spoofGetStats: async (): Promise<SpoofStats | null> =>
    (await App.SpoofGetStats()) as unknown as SpoofStats | null,
  spoofGetConfig: async (): Promise<SNIConfig> =>
    (await App.SpoofGetConfig()) ?? {},
  spoofSetConfig: (cfg: SNIConfig) =>
    Promise.resolve(App.SpoofSetConfig(cfg as unknown as Parameters<typeof App.SpoofSetConfig>[0])),
  spoofStart: (profileId: string) =>
    Promise.resolve(App.SpoofStart(profileId)),
  spoofUseProfile: async (profileId: string): Promise<Profile> => {
    const p = await App.SpoofUseProfile(profileId)
    if (!p) throw new Error('empty profile result')
    return p as unknown as Profile
  },
  spoofStop: () => Promise.resolve(App.SpoofStop()),
  spoofGetLogs: async (sinceID = 0): Promise<LogEntry[]> =>
    (await App.SpoofGetLogs(sinceID)) ?? [],
  scanSNIPairs: async (ips: string[], snis: string[]): Promise<SNIPairResult[]> =>
    (await App.ScanSNIPairs(ips, snis)) ?? [],
  scanSNIStatus: async (): Promise<SNIScanStatus> =>
    (await App.ScanSNIStatus()) ?? { running: false, done: 0, total: 0 },
  cancelSNIScan: () => Promise.resolve(App.CancelSNIScan()),
  resetSNIDefaults: () => Promise.resolve(App.ResetSNIDefaults()),
}
