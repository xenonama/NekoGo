import type { AetherSettings } from './types'

/** The protocols that name two hops instead of one endpoint. */
export function aetherTwoHops(protocol?: string): boolean {
  return ['gool', 'mim', 'wg-over-masque'].includes(protocol || 'wg')
}

/** Whether the engine dials a WARP endpoint of its own: a carrier set to
 *  `only` carries the whole connection and no WARP leg runs. */
export function aetherWarpUsed(a?: AetherSettings | null): boolean {
  return a?.psiphon !== 'only' && a?.tor !== 'only'
}

/** Whether the profile's own address and port reach nothing, so the
 *  endpoint row is hidden and save must not demand a port it never shows.
 *  Mirrors the backend's aetherPortUnused: the two used to disagree, and
 *  the Proxies editor then refused every two-hop profile the Aether tab
 *  had saved. */
export function aetherPortUnused(protocol?: string, a?: AetherSettings | null): boolean {
  return protocol === 'aether' && (aetherTwoHops(a?.protocol) || !aetherWarpUsed(a))
}

/** The endpoint a profile row shows. An Aether profile that dials no fixed
 *  endpoint of its own still carries an address and a port, and printing
 *  them claimed a ":0" (two hops, carrier-only) or a ":443" (blank address,
 *  which the editor's own placeholder calls a scan) that nothing connects
 *  to. A pinned outer endpoint is a real one, so it is shown as written. */
export function profileEndpointLabel(p: {
  protocol: string
  address: string
  port: number
  aether?: AetherSettings | null
}): string {
  if (p.protocol.toLowerCase() !== 'aether') return `${p.address}:${p.port}`
  const a = p.aether
  if (!aetherWarpUsed(a)) return a?.psiphon === 'only' ? 'via psiphon' : 'via tor'
  if (aetherTwoHops(a?.protocol)) return a?.wiwOuter?.trim() || 'scan'
  return p.address.trim() ? `${p.address}:${p.port}` : 'scan'
}