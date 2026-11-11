// Shared version facts. The About "Version stats" card renders these and
// its Copy diagnostics button emits formatDiagText of the same values, so
// what the user reads and what they paste into a bug report cannot drift.
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
    `NekoBox core ${v.nekoBox}`,
    `Xray ${v.xray}`,
    `mihomo ${v.mihomo}`,
    `OS ${v.os}`,
    `Core ${v.core}`,
  ].join('\n')
}