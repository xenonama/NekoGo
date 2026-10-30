import { useProfilesStore } from './profiles'
import { useSettingsStore } from './settings'
import { useConnectionStore } from './connection'
import { useSubscriptionsStore } from './subscriptions'
import { syncThemeClass } from '../theme'

/**
 * Unified sync hub — the fix for Simple/Advanced desync.
 *
 * Every wholesale change (mode switch, backup import/reset, subscription
 * update, profile save from either shell) funnels through reloadAll()
 * so both shells always read the same stores.
 *
 * Rules:
 * - Stores are the truth; components never keep local copies of
 *   profiles/settings/subscriptions.
 * - reloadAll() is idempotent and safe to call on every shell mount
 *   and on every mode toggle.
 */
export async function reloadAll(): Promise<void> {
  const settings = useSettingsStore()
  const profiles = useProfilesStore()
  const connection = useConnectionStore()
  const subs = useSubscriptionsStore()

  // Settings first (theme depends on it), then data in parallel.
  try {
    await settings.load()
  } catch { /* keep in-memory on backend failure */ }
  try {
    syncThemeClass(settings.settings.theme)
  } catch { /* non-fatal */ }

  await Promise.allSettled([
    (async () => { try { await profiles.load() } catch { /* keep */ } })(),
    (async () => { try { await subs.load() } catch { /* keep */ } })(),
    (async () => { try { await connection.refresh() } catch { /* keep */ } })(),
  ])
}

/** Lightweight refresh after a single-profile mutation. */
export async function reloadProfilesAndSubs(): Promise<void> {
  const profiles = useProfilesStore()
  const subs = useSubscriptionsStore()
  const settings = useSettingsStore()
  // Settings too: the backend owns fields like startOnBoot (Run key) and
  // can rewrite them; a stale in-memory copy would be saved back later.
  await Promise.allSettled([profiles.load(), subs.load(), settings.load()])
}
