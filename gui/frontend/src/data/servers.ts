// Shared display helpers for server rows.
//
// NOTE: there is intentionally NO mock server catalog in this file. Latency
// values shown in the UI must only come from real backend measurements
// (Profile.lastLatency via the profiles store). Never hardcode latencies for
// real profiles and never mix demo data into the store.
export interface ServerRow {
  id: string
  name: string
  city: string
  countryCode: string // ISO-3166 alpha-2, used for the flag emoji
  latencyMs: number // measured ms, or -1 when never tested
  testedAt?: string // RFC3339 timestamp of the measurement
}

/** ISO alpha-2 country code -> flag emoji (fallback: globe). */
export function flagEmoji(code: string): string {
  if (!/^[A-Za-z]{2}$/.test(code)) return '🌐'
  const base = 0x1f1e6
  return [...code.toUpperCase()]
    .map((c) => String.fromCodePoint(base + c.charCodeAt(0) - 65))
    .join('')
}

/** Latency badge color class: green <100ms, amber <300ms, red otherwise.
 *  BUG-016: ms < 0 means "never tested" and must never report healthy
 *  green — it gets its own untested class instead. */
export function latencyClass(ms: number): string {
  if (ms < 0) return 'lat-untested'
  if (ms < 100) return 'lat-ok'
  if (ms < 300) return 'lat-warn'
  return 'lat-bad'
}

/** A measurement older than 1 hour is flagged stale (muted badge + dot). */
export function isStale(iso?: string): boolean {
  if (!iso) return false
  const t = new Date(iso).getTime()
  if (Number.isNaN(t)) return false
  return Date.now() - t > 3600_000
}

/** Format a byte-per-second rate nicely, e.g. "12.4 MB/s". */
export function formatRate(bps: number): string {
  if (!bps || bps <= 0) return '0 B/s'
  const units = ['B/s', 'KB/s', 'MB/s', 'GB/s']
  let v = bps
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(1)} ${units[i]}`
}
