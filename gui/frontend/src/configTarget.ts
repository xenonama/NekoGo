import type { Profile } from './types'

/** Which profile the core-config editors render and write for.
 *
 *  Mirrors app.go resolveConfigTarget: the live connection when running,
 *  else the stored active profile, else the first profile. Each step
 *  falls through when the id no longer names a stored profile, exactly
 *  as the Go lookup does — a stale id must not resolve to nothing while
 *  the backend has already moved on to the next candidate. */
export function configTargetProfile(
  status: { running: boolean; profileId: string },
  activeProfileId: string | undefined,
  profiles: readonly Profile[],
): Profile | null {
  if (status.running && status.profileId) {
    const live = profiles.find((p) => p.id === status.profileId)
    if (live) return live
  }
  if (activeProfileId) {
    const active = profiles.find((p) => p.id === activeProfileId)
    if (active) return active
  }
  return profiles[0] ?? null
}