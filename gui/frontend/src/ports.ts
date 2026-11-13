/** The Settings numeric fields. They used to parse with
 *  `parseInt(raw, 10) || 0` and then clamp, so clearing the box or typing
 *  "208oo" produced 0 → 1, which the backend's own check accepts: the app
 *  saved a listener port nobody asked for and the next connect failed on
 *  it. Refusing the edit and keeping the previous port is the honest
 *  answer, with a message that says which number is still in force. */

// One below the largest port because an Xray profile puts its SOCKS
// listener on port + 1; 65535 would leave that listener nowhere to go.
export const minPort = 1
export const maxPort = 65534

export interface PortInput {
  value: number
  error: string
}

export function parsePortInput(raw: string, label: string, prev: number, badInput = false): PortInput {
  const text = raw.trim()
  const keep = (why: string): PortInput => ({
    value: prev,
    error: `${label}: ${why}. It stays ${prev}.`,
  })
  // An <input type=number> reports "" for "208oo" — the keystrokes are
  // gone by the time anything reads the field, so badInput is the only
  // thing that distinguishes a mistyped value from a cleared one.
  if (text === '' && !badInput) return keep('empty')
  if (!/^\d+$/.test(text)) return keep('not a whole number')
  const n = Number(text)
  if (n < minPort || n > maxPort) return keep(`must be ${minPort}-${maxPort}`)
  return { value: n, error: '' }
}
