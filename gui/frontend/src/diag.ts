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
  aether: string
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
 *  3s kill, so six sequential calls could stall the card for 18s.
 *
 *  singBox is the official sing-box.exe and nekoBox the NekoBox Thrift
 *  fork. They are not interchangeable: build.bat stamps the fork's
 *  sing-box version constant with the NekoGo build version so the
 *  updater can compare it against NekoGo release tags, so the fork's
 *  version line is NekoGo's version, not sing-box's.
 *
 *  aether is the engine that carries Tor (arti is linked into it) and
 *  Psiphon; an empty answer means the folder has no aether.exe, so no
 *  Aether profile and neither exit chain can run. */
export async function collectVersions(f: DiagFacts): Promise<DiagVersions> {
  const [singBox, nekoBox, xray, mihomo, aether, info] = await Promise.all([
    orEmpty(() => backend.upstreamSingBoxVersion(), ''),
    orEmpty(() => backend.coreVersion(), ''),
    orEmpty(() => backend.xrayVersion(), ''),
    orEmpty(() => backend.mihomoVersion(), ''),
    orEmpty(() => backend.aetherVersion(), ''),
    orEmpty(() => backend.runtimeInfo(), {} as Record<string, string>),
  ])
  return {
    app: f.version || unknown,
    singBox: singBox || unknown,
    nekoBox: nekoBox || unknown,
    xray: xray || unknown,
    mihomo: mihomo || unknown,
    aether: aether || unknown,
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
    `aether ${v.aether}`,
    `OS ${v.os}`,
    `Core ${v.core}`,
  ].join('\n')
}

export interface StatRow {
  label: string
  value: string
}

/** The version token out of a core's `--version` banner, normalised to
 *  `vN.N.N[-pre]`. Each core query returns the whole first stdout line —
 *  `Xray 25.9.11 (v2fly.org) go1.25.0 windows amd64` — and the stats card
 *  wants only `v25.9.11`. A line with no version in it is returned
 *  unchanged, so the collector's `unknown` still reads as the failure it
 *  is. `go1.25.0` cannot be mistaken for the version: the leading `\b`
 *  needs a word boundary, and `o1` is one run of word characters. */
export function bareVersion(text: string): string {
  const m = /\bv?\d+(?:\.\d+)+(?:[-+][0-9A-Za-z.-]+)?/.exec(text)
  return m ? 'v' + m[0].replace(/^v/, '') : text
}

/** The rows of the About → Version stats card.
 *
 *  The core versions show in both modes: "which core version do you
 *  have?" is the question a bug report asks, and a Simple-mode user can
 *  answer it from this card. What stays advanced-only is "Engine", the
 *  core path read-out — Simple mode chooses no engine and shows no core
 *  files, so a path describes a screen its user cannot reach. The card
 *  trims each version to the number; formatDiagText above still carries
 *  the whole banner line, because the person reading a pasted report
 *  wants the build the binary reports. */
export function versionStatRows(v: DiagVersions, advanced: boolean): StatRow[] {
  const rows: StatRow[] = [
    { label: 'NekoGo', value: v.app },
    { label: 'sing-box core', value: bareVersion(v.singBox) },
    { label: 'NekoBox version', value: bareVersion(v.nekoBox) },
    { label: 'Xray core', value: bareVersion(v.xray) },
    { label: 'mihomo core', value: bareVersion(v.mihomo) },
    { label: 'Aether core', value: bareVersion(v.aether) },
  ]
  if (advanced) {
    rows.push({ label: 'Engine', value: v.core })
  }
  rows.push({ label: 'OS', value: v.os })
  return rows
}