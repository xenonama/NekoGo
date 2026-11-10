<!-- One shell for both modes: the everyday nav/pages stay put and
     Advanced only adds expert sections inside those same pages. The
     footer switch flips modes in place; nothing is remounted. -->
<template>
  <div class="nb-simple">
    <div class="nb-shell">
      <Sidebar :mode="mode" :model-value="view" @update:model-value="onNav" @exit="onExitRequest" />
      <!-- visible gates the workspace timers; the shell itself stays
           mounted so tab/scroll state survives a mode switch. -->
      <main v-if="visible" class="nb-center">
        <div v-if="startupError" class="nb-warn-banner nb-dismissible">
          <span>{{ startupError }}</span>
          <button class="nb-banner-x" title="Dismiss" @click="$emit('dismiss-error')">✕</button>
        </div>
        <!-- Home -->
        <template v-if="view === 'home'">
          <ConnectionCard />
          <QuickActions />
        </template>
        <!-- Proxies: full-page table -->
        <template v-else-if="view === 'proxies'">
          <ProxiesTable
            v-if="!editing"
            @add-manual="add"
            @edit="edit"
            @import-link="showImport = true"
          />
          <ProfileForm v-else :profile="editingProfile" @saved="saved" @cancel="editing = false" />
          <ProfileToolsPanel v-if="advanced && !editing" />
        </template>
        <!-- Subscription: add bar + v2rayN-style feed table. -->
        <template v-else-if="view === 'subscription'">
          <div class="nb-card nb-page">
            <h3>Subscription</h3>
            <p class="muted">Import servers from a subscription link. Paste your URL below and press Add.</p>
            <div class="nb-sub-add">
              <input
                v-model="subUrl"
                class="grow"
                placeholder="https://example.com/sub.txt"
                aria-label="Subscription URL"
                @keydown.enter="importSub"
              />
              <button class="nb-act nb-act--primary" :disabled="!subUrl.trim() || subBusy || !backendAvailable" @click="importSub">
                <Icon name="plus" :size="14" />Add / Update
              </button>
              <button class="nb-act" :disabled="subBusy || subs.length === 0 || !backendAvailable" @click="updateAllSubs">
                <Icon name="refresh" :size="14" />Update All
              </button>
            </div>
            <div class="nb-sub-add">
              <span class="muted">Auto-update every</span>
              <input
                v-model.number="subAutoMins"
                class="stg-num"
                type="number"
                min="0"
                max="1440"
                aria-label="Auto-update interval in minutes (0 = off)"
                title="Auto-update interval in minutes (0 = off)"
                :disabled="!backendAvailable"
                @change="saveSubAuto"
              />
              <span class="muted">minutes (0 = off)</span>
            </div>
            <div v-if="subMsg" class="muted nb-sub-msg">{{ subMsg }}</div>
            <div v-if="!backendAvailable" class="muted">Backend offline</div>
          </div>
          <div v-if="subs.length > 0" class="nb-card nb-sub-list">
            <div class="nb-section-head">
              <span class="nb-section-title">Feeds ({{ subs.length }})</span>
              <span class="muted nb-sub-lead">Update imports new server links; Edit renames or repoints a feed.</span>
            </div>
            <div class="nb-sub-grid-head" aria-hidden="true">
              <span />
              <span>Name</span>
              <span>URL</span>
              <span>Updated</span>
              <span>Configs</span>
              <span>Quota</span>
              <span />
            </div>
            <div class="nb-sub-rows">
              <div v-for="s in subs" :key="s.url" class="nb-sub-row" :class="{ off: s.enabled === false }">
                <input
                  type="checkbox"
                  :checked="s.enabled !== false"
                  :disabled="subBusy"
                  :title="s.enabled === false ? 'Disabled: Update All and auto-update skip this feed (manual Update still works)' : 'Enabled: included in Update All and auto-update'"
                  :aria-label="`Enable ${s.name || s.url}`"
                  @change="toggleSubEnabled(s, ($event.target as HTMLInputElement).checked)"
                />
                <span class="nb-sub-name" :title="s.name || s.url">{{ s.name || s.url }}</span>
                <span class="nb-sub-cell">
                  <span class="nb-sub-url" :title="s.url">{{ s.url }}</span>
                  <span v-if="subNote[s.url]" class="nb-sub-note" :title="subNote[s.url]">{{ subNote[s.url] }}</span>
                </span>
                <span class="nb-sub-when" :title="subUpdatedText(s)">{{ subWhenText(s) }}</span>
                <span class="nb-sub-counts" :title="`${subConfigCount(s)} configs in Proxies came from this feed`">{{ subConfigCount(s) }}</span>
                <span class="nb-sub-cell">
                  <span v-if="subQuotaText(s)" class="nb-sub-quota" :title="subQuotaText(s)">{{ subQuotaText(s) }}</span>
                  <span v-else class="muted">—</span>
                  <span v-if="subQuotaPct(s) !== null" class="nb-quota" :title="subQuotaText(s)">
                    <span class="nb-quota-fill" :style="{ width: subQuotaPct(s) + '%' }" />
                  </span>
                </span>
                <span class="nb-sub-actions">
                  <button
                    class="nb-icon-btn"
                    :title="`Re-fetch ${s.name || s.url} and import new servers`"
                    :disabled="subBusy || refreshing.has(s.url) || !backendAvailable"
                    @click="refreshOne(s.url)"
                  >
                    <span v-if="refreshing.has(s.url)" class="nb-spin" />
                    <Icon v-else name="refresh" :size="15" />
                  </button>
                  <button class="nb-icon-btn" :title="copiedSub === s.url ? 'Copied!' : 'Copy subscription URL'" @click="copySubUrl(s.url)">
                    <Icon :name="copiedSub === s.url ? 'check' : 'copy'" :size="15" />
                  </button>
                  <button class="nb-icon-btn" title="Edit name or URL" @click="openSubEdit(s)"><Icon name="pencil" :size="15" /></button>
                  <button class="nb-icon-btn nb-danger" title="Delete feed…" :disabled="subBusy" @click="askRemoveSub(s)"><Icon name="trash" :size="15" /></button>
                </span>
              </div>
            </div>
          </div>
          <div v-else class="nb-card nb-empty">
            <p class="muted">No subscription feeds yet — add one above.</p>
          </div>
        </template>
        <!-- Settings -->
        <template v-else-if="view === 'settings'">
          <div class="nb-card nb-page">
            <h3>Settings</h3>
          </div>
          <SettingsPanel />
          <RoutingRules />
          <template v-if="advanced">
            <h4 class="nb-adv-div">Backup &amp; reset</h4>
            <BackupWorkspace />
            <h4 class="nb-adv-div">Core config</h4>
            <ConfigWorkspace ref="configPanel" @goto="onGoto" />
          </template>
        </template>
        <!-- Console: live traffic + logs -->
        <template v-else-if="view === 'console'">
          <div class="nb-card nb-page">
            <h3>Console</h3>
            <p class="muted">Live traffic and application logs. Use the
              tabs below to switch views.</p>
          </div>
          <ConsolePanel :only="['traffic', 'logs', 'connection']" />
          <template v-if="advanced">
            <h4 class="nb-adv-div">Network tools</h4>
            <NetworkTools v-if="connection.status.running" />
            <p v-else class="nb-adv-note">Connect a profile to run IP, speed and URL tests.</p>
            <h4 class="nb-adv-div">System tools</h4>
            <SystemTools />
          </template>
        </template>
        <template v-else-if="view === 'sni'">
          <SNIPanel />
        </template>
        <!-- Aether: dedicated editor + endpoint scan (PattNG parity) -->
        <template v-else-if="view === 'aether'">
          <AetherPanel />
        </template>
        <!-- Chain (Advanced only): Psiphon hub + transport mode. -->
        <template v-else-if="view === 'chain'">
          <ChainPanel />
        </template>
        <!-- About -->
        <template v-else-if="view === 'about'">
          <About :version="version" />
        </template>
      </main>
      <ServersPanel
        :selected-id="settings.settings.activeProfileId"
        :version="version"
        @select="onSelectServer"
        @open-about="onNav('about')"
        @import="showImport = true"
      />
      <ImportModal :show="showImport" @close="showImport = false" @imported="onImported" />
      <!-- Subscription edit modal: rename / repoint one feed. -->
      <div v-if="subEdit" class="nb-modal-overlay" @click.self="closeSubEdit">
        <div class="nb-modal" role="dialog" aria-modal="true" aria-label="Edit subscription">
          <div class="nb-modal-head">
            <span class="nb-section-title">Edit subscription</span>
            <button class="nb-icon-btn" title="Close" @click="closeSubEdit"><Icon name="x" :size="18" /></button>
          </div>
          <label class="nb-sub-field">
            <span class="muted">Name</span>
            <input v-model="subEditName" placeholder="My feed" @keydown.enter="saveSubEdit" />
          </label>
          <label class="nb-sub-field">
            <span class="muted">URL</span>
            <input v-model="subEditUrl" placeholder="https://example.com/sub.txt" @keydown.enter="saveSubEdit" />
          </label>
          <p class="muted nb-modal-hint">
            Changing the URL re-imports the new feed before the old record drops.
            Renaming also moves the group of profiles imported from this feed.
          </p>
          <div v-if="subEditMsg" class="nb-conn-err">{{ subEditMsg }}</div>
          <div class="nb-modal-foot">
            <button class="nb-act nb-act--ghost" @click="closeSubEdit">
              <Icon name="x" :size="14" />Cancel
            </button>
            <button class="nb-act nb-act--primary" :disabled="subBusy || !subEditUrl.trim()" @click="saveSubEdit">
              <Icon name="check" :size="14" />{{ subBusy ? 'Saving…' : 'Save' }}
            </button>
          </div>
        </div>
      </div>
      <!-- Subscription delete modal: feed only, or feed + its configs. -->
      <div v-if="subDel" class="nb-modal-overlay" @click.self="closeRemoveSub">
        <div class="nb-modal" role="dialog" aria-modal="true" aria-label="Delete subscription">
          <div class="nb-modal-head">
            <span class="nb-section-title">Delete subscription</span>
            <button class="nb-icon-btn" title="Close" @click="closeRemoveSub"><Icon name="x" :size="18" /></button>
          </div>
          <p class="muted nb-modal-hint">
            "{{ subDel.name || subDel.url }}" has {{ subDelCount }} config{{ subDelCount === 1 ? '' : 's' }} in Proxies.
          </p>
          <p class="muted">
            Removing only the feed keeps the configs working; they stay as normal rows in the
            Proxies tab. Deleting both removes the feed and those configs.
          </p>
          <div class="nb-modal-foot">
            <button class="nb-act nb-act--ghost" @click="closeRemoveSub">
              <Icon name="x" :size="14" />Cancel
            </button>
            <button class="nb-act nb-act--danger" :disabled="subBusy" @click="doRemoveSub(false)">
              <Icon name="trash" :size="14" />Remove feed only
            </button>
            <button v-if="subDelCount > 0" class="nb-act nb-act--danger" :disabled="subBusy" @click="doRemoveSub(true)">
              <Icon name="trash" :size="14" />Remove feed + {{ subDelCount }} config{{ subDelCount === 1 ? '' : 's' }}
            </button>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useProfilesStore } from '../stores/profiles'
import { useSettingsStore } from '../stores/settings'
import { useConnectionStore } from '../stores/connection'
import { useSubscriptionsStore } from '../stores/subscriptions'
import { reloadProfilesAndSubs } from '../stores/sync'
import { backendAvailable } from '../api'
import { copyText } from '../clipboard'
import type { Profile, Subscription } from '../types'
import Sidebar, { type NavKey } from './Sidebar.vue'
import { takePendingSimpleView } from '../nav'
import ConnectionCard from './ConnectionCard.vue'
import QuickActions from './QuickActions.vue'
import ServersPanel from './ServersPanel.vue'
import ProxiesTable from './ProxiesTable.vue'
import ImportModal from './ImportModal.vue'
import ProfileForm from './ProfileForm.vue'
import RoutingRules from './RoutingRules.vue'
import SettingsPanel from './SettingsPanel.vue'
import ConsolePanel from './ConsolePanel.vue'
import SNIPanel from './SNIPanel.vue'
import AetherPanel from './AetherPanel.vue'
import ChainPanel from './ChainPanel.vue'
import About from './About.vue'
import Icon from './Icon.vue'
import BackupWorkspace from './adv/BackupWorkspace.vue'
import ConfigWorkspace from './adv/ConfigWorkspace.vue'
import NetworkTools from './adv/NetworkTools.vue'
import ProfileToolsPanel from './adv/ProfileToolsPanel.vue'
import SystemTools from './adv/SystemTools.vue'

const props = withDefaults(
  defineProps<{
    version: string
    startupError: string
    /** Same shell in both modes; Advanced only reveals extra sections. */
    mode?: 'simple' | 'advanced'
    /** False while App hides the center: pollers stop, chrome stays. */
    visible?: boolean
  }>(),
  { mode: 'simple', visible: true },
)

defineEmits<{
  (e: 'dismiss-error'): void
}>()

const view = ref<NavKey>('home')
const advanced = computed(() => props.mode === 'advanced')
const configPanel = ref<{ isDirty: () => boolean } | null>(null)
const profiles = useProfilesStore()
const settings = useSettingsStore()
const connection = useConnectionStore()
const editing = ref(false)
const editingProfile = ref<Profile | null>(null)
const subUrl = ref('')
// Subscriptions live in the shared store so mode switches keep the
// same feeds and in-flight state.
const subsStore = useSubscriptionsStore()
const subs = computed(() => subsStore.feeds)
const subMsg = computed(() => subsStore.message)
const subBusy = computed(() => subsStore.busy)
const refreshing = computed(() => subsStore.refreshing)
const subNote = computed(() => subsStore.notes)

function relTimeSub(iso?: string): string {
  if (!iso) return 'never'
  const t = new Date(iso).getTime()
  if (Number.isNaN(t)) return 'never'
  const s = Math.floor((Date.now() - t) / 1000)
  if (s < 60) return 'just now'
  if (s < 3600) return `${Math.floor(s / 60)}m ago`
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`
  if (s < 30 * 86400) return `${Math.floor(s / 86400)}d ago`
  return new Date(t).toLocaleDateString()
}

function subUpdatedText(s: Subscription): string {
  return s.updatedAt ? `updated ${relTimeSub(s.updatedAt)}` : 'never updated'
}

// Grid cell variant: the column header already says "Updated".
function subWhenText(s: Subscription): string {
  return s.updatedAt ? relTimeSub(s.updatedAt) : 'never'
}

// Throne parity: per-feed quota/expiry from the subscription-userinfo
// header (empty when the provider sends none).
function formatQuotaBytes(n: number): string {
  if (!n || n <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let v = n
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v >= 100 ? v.toFixed(0) : v.toFixed(1)} ${units[i]}`
}

function expireText(v: string): string {
  if (/^\d+$/.test(v)) {
    const ms = v.length > 10 ? Number(v) : Number(v) * 1000
    const d = new Date(ms)
    if (!Number.isNaN(d.getTime()) && d.getFullYear() >= 2000 && d.getFullYear() <= 2100) {
      return `expires ${d.toLocaleDateString()} (${relTimeSub(d.toISOString())})`
    }
  }
  return `expires ${v}`
}

function subQuotaText(s: Subscription): string {
  const used = (s.usageUpload ?? 0) + (s.usageDownload ?? 0)
  const total = s.usageTotal ?? 0
  const parts: string[] = []
  if (total > 0) parts.push(`${formatQuotaBytes(used)} / ${formatQuotaBytes(total)}`)
  else if (used > 0) parts.push(`${formatQuotaBytes(used)} used`)
  if (s.usageExpire) parts.push(expireText(s.usageExpire))
  return parts.join(' · ')
}

function subQuotaPct(s: Subscription): number | null {
  const total = s.usageTotal ?? 0
  if (total <= 0) return null
  const used = (s.usageUpload ?? 0) + (s.usageDownload ?? 0)
  return Math.max(0, Math.min(100, Math.round((used / total) * 100)))
}

// Auto-update interval (Throne parity: sub_auto_update). The App
// watcher re-arms the frontend timer on every persisted change.
const subAutoMins = computed({
  get: () => settings.settings.subAutoUpdateM ?? 0,
  set: (v: number) => {
    settings.settings.subAutoUpdateM = Math.max(0, Math.min(1440, Math.round(v) || 0))
  },
})

async function saveSubAuto() {
  try {
    await settings.save()
  } catch {
    /* dev mode: keep local choice */
  }
}

async function refreshOne(url: string) {
  try {
    await subsStore.refreshOne(url)
    await profiles.load()
  } catch { /* note already stored */ }
}

// Per-feed actions: copy URL, edit (rename/repoint), delete.
const copiedSub = ref('')
let copiedSubTimer = 0
async function copySubUrl(url: string) {
  if (!(await copyText(url))) {
    subsStore.setNote(url, 'Copy failed — clipboard unavailable.')
    return
  }
  copiedSub.value = url
  window.clearTimeout(copiedSubTimer)
  copiedSubTimer = window.setTimeout(() => {
    if (copiedSub.value === url) copiedSub.value = ''
  }, 1500)
}

const subEdit = ref<Subscription | null>(null)
const subEditName = ref('')
const subEditUrl = ref('')
const subEditMsg = ref('')

function openSubEdit(s: Subscription) {
  subEdit.value = s
  subEditName.value = s.name || ''
  subEditUrl.value = s.url
  subEditMsg.value = ''
}

function closeSubEdit() {
  subEdit.value = null
  subEditMsg.value = ''
}

async function saveSubEdit() {
  const s = subEdit.value
  if (!s) return
  const url = subEditUrl.value.trim()
  const name = subEditName.value.trim()
  if (!url) return
  if (url === s.url && name === (s.name || '')) {
    closeSubEdit()
    return
  }
  subEditMsg.value = ''
  try {
    await subsStore.edit(s.url, url, name)
    await profiles.load()
    closeSubEdit()
  } catch (e) {
    subEditMsg.value = String(e)
  }
}

// Config count per feed: precise on subUrl, group-name fallback for
// profiles imported before subUrl existed (rename keeps them in sync).
function subConfigCount(s: Subscription): number {
  return profiles.profiles.filter((p) =>
    p.subUrl ? p.subUrl === s.url : !!s.name && p.group === s.name,
  ).length
}

async function toggleSubEnabled(s: Subscription, on: boolean) {
  try {
    await subsStore.setEnabled(s.url, on)
  } catch { /* message set, checkbox reverted by the store */ }
}

// Delete flow: feed-only vs feed + its configs (count shown up front).
const subDel = ref<Subscription | null>(null)
const subDelCount = computed(() => (subDel.value ? subConfigCount(subDel.value) : 0))

function askRemoveSub(s: Subscription) {
  subDel.value = s
}
function closeRemoveSub() {
  subDel.value = null
}
// Escape has to work even while focus is still on the row that opened the
// dialog: a keydown binding on the overlay never fires because the overlay
// is never focused (ImportModal/JsonEditorModal already listen on window).
function onSubModalKey(e: KeyboardEvent) {
  if (e.key !== 'Escape') return
  if (subEdit.value) closeSubEdit()
  else if (subDel.value) closeRemoveSub()
}
watch([subEdit, subDel], ([edit, del]) => {
  if (edit || del) window.addEventListener('keydown', onSubModalKey)
  else window.removeEventListener('keydown', onSubModalKey)
})
onUnmounted(() => window.removeEventListener('keydown', onSubModalKey))
async function doRemoveSub(alsoProfiles: boolean) {
  const s = subDel.value
  if (!s) return
  try {
    await subsStore.remove(s.url, alsoProfiles)
    await profiles.load()
    closeRemoveSub()
  } catch { /* message stored; keep the modal open */ }
}
const showImport = ref(false)

function confirmLeaveConfig(next: NavKey | null): boolean {
  if (next === 'settings') return true
  if (view.value !== 'settings' || configPanel.value?.isDirty() !== true) return true
  return window.confirm('Discard unsaved config changes?')
}

function onNav(key: NavKey) {
  if (!confirmLeaveConfig(key)) return
  view.value = key
}

function onGoto(key: 'proxies') {
  onNav(key)
}

function onExitRequest() {
  if (!confirmLeaveConfig(null)) return
  void settings.setAdvancedMode(false)
}

// The Chain tab only exists in Advanced; leaving the mode while it is
// open must not strand the shell on a tab its sidebar no longer offers.
watch(advanced, (on) => {
  if (!on && view.value === 'chain') view.value = 'home'
})

async function importSub() {
  const url = subUrl.value
  if (!url.trim()) return
  try {
    await subsStore.addOrUpdate(url)
    subUrl.value = ''
    await profiles.load()
  } catch { /* message already stored */ }
}
async function updateAllSubs() {
  try {
    await subsStore.updateAll()
    await profiles.load()
  } catch { /* message already stored */ }
}
async function onImported() {
  await reloadProfilesAndSubs()
}

function add() {
  editingProfile.value = null
  editing.value = true
}
function edit(p: Profile) {
  editingProfile.value = p
  editing.value = true
}
async function saved() {
  editing.value = false
  editingProfile.value = null
  // ProfileForm persisted via the store (profiles.save() patched the
  // Pinia store in place). A single profile save does NOT need a full
  // listProfiles() reload — that would re-send the ~123 KB bridge
  // payload and re-render all ProxiesTable rows. Batch flows (file
  // import via onImported, subscription update, country refresh) still
  // reload through their own call sites of reloadProfilesAndSubs().
}
async function onSelectServer(id: string) {
  try {
    await settings.setActiveProfileId(id)
  } catch {
    // Persistence failed and the action reverted the set; stay put.
    return
  }
  if (view.value === 'proxies') view.value = 'home'
}

// Single mount: boot already loaded every store, and mutations reload
// via stores/sync.ts — so no refetch here.
onMounted(() => {
  const requested = takePendingSimpleView()
  if (requested) view.value = requested
  connection.acquire()
})
onUnmounted(() => connection.release())
</script>
