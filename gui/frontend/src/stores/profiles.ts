import { defineStore } from 'pinia'
import { ref } from 'vue'
import { backend } from '../api'
import { useSettingsStore } from './settings'
import type { Profile } from '../types'

export const useProfilesStore = defineStore('profiles', () => {
  const profiles = ref<Profile[]>([])
  const loading = ref(false)
  const error = ref('')
  /** True after a successful load(): an empty array before that says
   *  nothing about the stored selection (a transient IPC failure must not
   *  look like "the selected profile was deleted"). */
  const loaded = ref(false)
  /** Real measured latencies in ms, keyed by profile id. No fake values ever. */
  const latencies = ref<Record<string, number>>({})
  /** RFC3339 measurement timestamps, keyed by profile id. */
  const testedAt = ref<Record<string, string>>({})
  /** Last test failure message, keyed by profile id. */
  const lastError = ref<Record<string, string>>({})
  /** RFC3339 timestamp of the last failure, keyed by profile id. */
  const lastErrorAt = ref<Record<string, string>>({})

  async function load() {
    loading.value = true
    error.value = ''
    try {
      if (!backend.available()) throw new Error('backend unavailable (dev mode)')
      profiles.value = await backend.listProfiles()
      // Seed the maps from persisted measurements only. Never tested
      // profiles stay absent so the UI renders "Untested".
      const lat: Record<string, number> = {}
      const tst: Record<string, string> = {}
      for (const p of profiles.value) {
        if (p.lastLatency && p.lastLatency > 0) {
          lat[p.id] = p.lastLatency
          if (p.lastTestedAt) tst[p.id] = p.lastTestedAt
        }
      }
      latencies.value = lat
      testedAt.value = tst
      lastError.value = {}
      lastErrorAt.value = {}
      loaded.value = true
    } catch (e) {
      error.value = String(e)
      // No mock data: without a backend the store stays empty and the UI
      // shows its empty states instead of fake servers/latencies.
      if (!backend.available()) {
        profiles.value = []
        latencies.value = {}
        testedAt.value = {}
        lastError.value = {}
        lastErrorAt.value = {}
      }
    } finally {
      loading.value = false
    }
  }

  /** Run a real backend latency test; updates maps + persisted profile.
   *  A failed retest NEVER discards the last-good latency — it only
   *  records lastError/lastErrorAt so the UI can show both.
   *  BUG-025: map updates merge per-key by direct mutation, never by
   *  snapshot-replacing the whole object — two overlapping tests would
   *  otherwise interleave read-modify-write and the later resolver can
   *  erase the earlier row's update. */
  async function test(id: string, timeoutMs = 8000): Promise<number> {
    const r = await backend.testProfile(id, timeoutMs)
    const now = new Date().toISOString()
    if (r.error || r.latencyMs < 0) {
      const msg = r.error || 'failed'
      lastError.value[id] = msg
      lastErrorAt.value[id] = now
      throw new Error(msg)
    }
    latencies.value[id] = r.latencyMs
    testedAt.value[id] = now
    delete lastError.value[id]
    delete lastErrorAt.value[id]
    // Refresh persisted lastLatency/lastTestedAt on the cached profile.
    const p = profiles.value.find((x) => x.id === id)
    if (p) {
      p.lastLatency = r.latencyMs
      p.lastTestedAt = now
    }
    return r.latencyMs
  }

  async function save(p: Profile) {
    if (!backend.available()) {
      // Local-only fallback for vite dev: upsert in memory.
      const cp = { ...p, id: p.id || `local-${Date.now()}` }
      const i = profiles.value.findIndex((x) => x.id === cp.id)
      if (i >= 0) profiles.value[i] = cp
      else profiles.value.push(cp)
      return cp
    }
    const saved = await backend.saveProfile(p)
    const i = profiles.value.findIndex((x) => x.id === saved.id)
    if (i >= 0) profiles.value[i] = saved
    else profiles.value.push(saved)
    return saved
  }

  async function remove(id: string) {
    if (backend.available()) {
      await backend.deleteProfile(id)
    }
    profiles.value = profiles.value.filter((x) => x.id !== id)
    // BUG-012: the backend clears the persisted ActiveProfileID on
    // match; mirror it locally so the UI never points at a dead id.
    // (No import cycle: settings.ts never imports this store.)
    const settings = useSettingsStore()
    if (settings.settings.activeProfileId === id) {
      try {
        await settings.setActiveProfileId('')
      } catch {
        settings.settings.activeProfileId = ''
      }
    }
  }

  return { profiles, loading, error, loaded, latencies, testedAt, lastError, lastErrorAt, load, test, save, remove }
})
