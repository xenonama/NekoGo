// Shared diagnostics builder: the same output the About card, the
// Overview workspace and the StatusStrip all show. Lives at src/
// (like nav.ts) because all three shells/workspaces import it —
// neither shell imports the other's folder.
import { backend } from './api'

export interface DiagFacts {
  version: string
  coreText: string
}

export async function buildDiagText(f: DiagFacts): Promise<string> {
  let core = 'unknown'
  let xray = 'unknown'
  let mihomo = 'unknown'
  let info: Record<string, string> = {}
  try {
    core = await backend.coreVersion()
  } catch { /* keep unknown */ }
  try {
    xray = await backend.xrayVersion()
  } catch { /* keep unknown */ }
  try {
    mihomo = await backend.mihomoVersion()
  } catch { /* keep unknown */ }
  try {
    info = await backend.runtimeInfo()
  } catch { /* keep unknown */ }
  return [
    `NekoGo ${f.version}`,
    `sing-box ${core || 'unknown'}`,
    `Xray ${xray || 'unknown'}`,
    `mihomo ${mihomo || 'unknown'}`,
    `OS ${info['goos'] || 'unknown'}/${info['goarch'] || 'unknown'}`,
    `Go ${info['go'] || 'unknown'}`,
    `Core ${f.coreText}`,
  ].join('\n')
}
