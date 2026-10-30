import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { backend } from '../api'
import type { Subscription } from '../types'

/**
 * Single source of truth for subscription feeds.
 * Previously SimpleShell and ProxiesTable each kept a local `subs` ref
 * loaded once on mount — edits in one shell never appeared in the other
 * until a remount. Every reader/writer must go through this store.
 */
export const useSubscriptionsStore = defineStore('subscriptions', () => {
  const feeds = ref<Subscription[]>([])
  const busy = ref(false)
  const message = ref('')
  /** Per-feed refresh state, keyed by URL. Shared so both shells show it. */
  const refreshing = ref<Set<string>>(new Set())
  const notes = ref<Record<string, string>>({})

  const count = computed(() => feeds.value.length)

  async function load() {
    try {
      feeds.value = (await backend.listSubscriptions()) ?? []
    } catch {
      if (!backend.available()) feeds.value = []
    }
  }

  function markRefreshing(url: string, on: boolean) {
    const s = new Set(refreshing.value)
    if (on) s.add(url)
    else s.delete(url)
    refreshing.value = s
  }

  async function addOrUpdate(url: string) {
    busy.value = true
    message.value = ''
    try {
      const r = await backend.importLinks(url)
      message.value = `Imported ${r.profiles.length} profiles, skipped ${r.invalid} invalid links.`
      await load()
      return r
    } catch (e) {
      message.value = String(e)
      throw e
    } finally {
      busy.value = false
    }
  }

  async function refreshOne(url: string) {
    if (busy.value || refreshing.value.has(url)) return
    markRefreshing(url, true)
    try {
      const r = await backend.importLinks(url)
      notes.value = { ...notes.value, [url]: `${r.profiles.length} new, ${r.invalid} invalid` }
      await load()
      return r
    } catch (e) {
      notes.value = { ...notes.value, [url]: String(e) }
      throw e
    } finally {
      markRefreshing(url, false)
    }
  }

  async function updateAll() {
    busy.value = true
    message.value = ''
    try {
      const r = await backend.updateSubscriptions()
      message.value = `Updated subscriptions: ${r.profiles.length} new profiles.`
      await load()
      return r
    } catch (e) {
      message.value = String(e)
      throw e
    } finally {
      busy.value = false
    }
  }

  /** Rename a feed and/or repoint it at a new URL (URL changes
   *  re-import the feed backend-side before the old record drops). */
  async function edit(oldUrl: string, newUrl: string, name: string) {
    busy.value = true
    message.value = ''
    try {
      const r = await backend.editSubscription(oldUrl, newUrl, name)
      message.value = `Subscription saved — ${r.profiles.length} new profiles imported, ${r.invalid} invalid.`
      await load()
      return r
    } catch (e) {
      message.value = String(e)
      throw e
    } finally {
      busy.value = false
    }
  }

  /** Remove a feed record. With alsoProfiles the profiles imported
   *  from it are deleted too; returns the deleted-profile count. */
  async function remove(url: string, alsoProfiles = false) {
    busy.value = true
    message.value = ''
    try {
      const n = await backend.deleteSubscription(url, alsoProfiles)
      message.value = n > 0
        ? `Subscription removed with ${n} configs.`
        : 'Subscription removed. Its imported profiles stay in Proxies.'
      await load()
      return n
    } catch (e) {
      message.value = String(e)
      throw e
    } finally {
      busy.value = false
    }
  }

  /** Pause/resume a feed (optimistic: the checkbox flips instantly and
   *  reverts if the backend rejects the change). */
  async function setEnabled(url: string, enabled: boolean) {
    const f = feeds.value.find((x) => x.url === url)
    const prev = f?.enabled
    if (f) f.enabled = enabled
    try {
      await backend.setSubscriptionEnabled(url, enabled)
    } catch (e) {
      if (f) f.enabled = prev
      message.value = String(e)
      throw e
    }
  }

  function setNote(url: string, text: string) {
    notes.value = { ...notes.value, [url]: text }
  }

  return { feeds, busy, message, refreshing, notes, count, load, addOrUpdate, refreshOne, updateAll, edit, remove, setEnabled, setNote }
})
