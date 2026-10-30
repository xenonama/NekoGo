// One-shot cross-shell view request: the Advanced shell can ask the
// Simple shell to open on a specific view (StatusStrip "Open Console",
// the "Open Proxies" empty-state CTAs) without prop-drilling through
// App.vue. Written right before peeking; consumed and cleared on
// SimpleShell mount, so a stale request can never hijack a later
// visit. Module scope on purpose (same pattern as collapsedByKey /
// recentKeys): the value must outlive the component that wrote it.
import type { NavKey } from './components/Sidebar.vue'

let pending: NavKey | null = null

/** Queue `view` as the Simple shell's landing target on next mount. */
export function requestSimpleView(view: NavKey): void {
  pending = view
}

/** Read-and-clear the pending request (SimpleShell only). */
export function takePendingSimpleView(): NavKey | null {
  const v = pending
  pending = null
  return v
}
