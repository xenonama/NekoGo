// Shared version facts. The About "Version stats" card renders
// versionStatRows of these values and its Copy diagnostics button emits
// formatDiagText of the same ones, so a number the card shows can never
// disagree with the number a bug report carries. The card drops the
// advanced-only rows in Simple mode; the copy never does.
// Lives at src/ (like nav.ts) because any shell or workspace can import it.
import { backend } from './api'

export interface DiagFacts {
  version: string
  coreText: string
}

export interface DiagVersions {
  app: string
  singBox: string
  nekoBox: string
  xray: string
  mihomo: string
  os: string
  core: string
}

const unknown = 'unknown'

// A binding can reject (no Wails runtime in `vite dev`) or throw before it
// ever returns a promise, so the call itself has to sit inside the try.
async function orEmpty<T>(call: () => Promise<T>, fallback: T): Promise<T> {
  try {
    return (await call()) ?? fallback
  } catch {
    return fallback
  }
}

/** Reads every version from the backend. The core queries are cached on
 *  the Go side and run in parallel here: each one spawns a binary with a
 *  3s kill, so five sequential calls could stall the card for 15s.
 *
 *  singBox is the official sing-box.exe and nekoBox the NekoBox Thrift
 *  fork. They are not interchangeable: build.bat stamps the fork's
 *  sing-box version constant with the NekoGo build version so the
 *  updater can compare it against NekoGo release tags, so the fork's
 *  version line is NekoGo's version, not sing-box's. */
export async function collectVersions(f: DiagFacts): Promise<DiagVersions> {
  const [singBox, nekoBox, xray, mihomo, info] = await Promise.all([
    orEmpty(() => backend.upstreamSingBoxVersion(), ''),
    orEmpty(() => backend.coreVersion(), ''),
    orEmpty(() => backend.xrayVersion(), ''),
    orEmpty(() => backend.mihomoVersion(), ''),
    orEmpty(() => backend.runtimeInfo(), {} as Record<string, string>),
  ])
  return {
    app: f.version || unknown,
    singBox: singBox || unknown,
    nekoBox: nekoBox || unknown,
    xray: xray || unknown,
    mihomo: mihomo || unknown,
    os: info['goos'] && info['goarch'] ? `${info['goos']}/${info['goarch']}` : unknown,
    core: f.coreText || unknown,
  }
}

export function formatDiagText(v: DiagVersions): string {
  return [
    `NekoGo ${v.app}`,
    `sing-box ${v.singBox}`,
    `NekoBox version ${v.nekoBox}`,
    `Xray ${v.xray}`,
    `mihomo ${v.mihomo}`,
    `OS ${v.os}`,
    `Core ${v.core}`,
  ].join('\n')
}

export interface StatRow {
  label: string
  value: string
}

/** The rows of the About → Version stats card.
 *
 *  Simple mode chooses no engine, installs no second core and shows no
 *  core path, so the four binary versions and the idle "Engine" read-out
 *  describe a screen that mode cannot reach — they are the advanced
 *  statistics the Advanced card already treats as advanced. The card
 *  hides them; formatDiagText above still carries all of them, because a
 *  bug report needs the engine facts whether or not the screen shows
 *  them. */
export function versionStatRows(v: DiagVersions, advanced: boolean): StatRow[] {
  const rows: StatRow[] = [{ label: 'NekoGo', value: v.app }]
  if (advanced) {
    rows.push(
      { label: 'sing-box core', value: v.singBox },
      { label: 'NekoBox version', value: v.nekoBox },
      { label: 'Xray core', value: v.xray },
      { label: 'mihomo core', value: v.mihomo },
      { label: 'Engine', value: v.core },
    )
  }
  rows.push({ label: 'OS', value: v.os })
  return rows
}