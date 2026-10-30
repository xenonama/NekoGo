/** Best-effort clipboard write: async Clipboard API with a
 *  textarea + execCommand fallback (WebView2 can deny the async API
 *  without a permission grant). Returns whether the copy landed. */
export async function copyText(s: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(s)
    return true
  } catch {
    try {
      const ta = document.createElement('textarea')
      ta.value = s
      ta.style.position = 'fixed'
      ta.style.opacity = '0'
      document.body.appendChild(ta)
      ta.select()
      const ok = document.execCommand('copy')
      document.body.removeChild(ta)
      return ok
    } catch {
      return false
    }
  }
}
