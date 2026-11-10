<!-- Advanced "Profile tools": the per-profile expert actions (speed/IP
     probes, duplicate, move-to-group, edit-as-JSON) that the Proxies
     table leaves out. Proxies itself keeps Connect / Test / Edit /
     Delete / Re-detect. One card, one head, five rows. -->
<template>
  <div class="stg-card">
    <div class="stg-head">
      <span class="stg-head-chip"><Icon name="wrench" :size="22" /></span>
      <span class="stg-head-text">
        <span class="stg-head-title">Profile tools</span>
        <span class="stg-head-sub">Speed/IP probes, duplicate, move, raw JSON for one profile</span>
      </span>
    </div>
    <div v-if="profiles.profiles.length === 0" class="muted">No profiles yet — add one in Proxies.</div>
    <template v-else>
      <div class="toolbar">
        <PickSelect
          v-model="targetId"
          icon="servers"
          title="Profile under test"
          aria-label="Profile under test"
          quiet
          :disabled="busy"
          :options="targetOptions"
        />
        <span v-if="note" class="muted">{{ note }}</span>
      </div>
      <div class="stg-row">
        <span class="stg-chip"><Icon name="download" :size="18" /></span>
        <span class="stg-meta">
          <span class="stg-title">Speed test</span>
          <span class="stg-sub">Switches to this profile, measures, restores</span>
        </span>
        <span class="stg-ctl">
          <button class="nb-act nb-act--primary" :disabled="probeBlocked || !target" @click="probeSpeed">
            <Icon name="download" :size="14" />{{ busy && busyWhat === 'speed' ? 'Measuring…' : 'Run speed test' }}
          </button>
        </span>
      </div>
      <div class="stg-row">
        <span class="stg-chip"><Icon name="globe" :size="18" /></span>
        <span class="stg-meta">
          <span class="stg-title">IP test</span>
          <span class="stg-sub">Switches to this profile, probes egress, restores</span>
        </span>
        <span class="stg-ctl">
          <button class="nb-act" :disabled="probeBlocked || !target" @click="probeIP">
            <Icon name="globe" :size="14" />{{ busy && busyWhat === 'ip' ? 'Probing…' : 'Run IP test' }}
          </button>
        </span>
      </div>
      <div class="stg-row">
        <span class="stg-chip"><Icon name="copy" :size="18" /></span>
        <span class="stg-meta">
          <span class="stg-title">Duplicate profile</span>
          <span class="stg-sub">Clone via saveProfile (new id + suffix)</span>
        </span>
        <span class="stg-ctl">
          <button class="nb-act" :disabled="busy || !target" @click="duplicate">
            <Icon name="copy" :size="14" />Duplicate
          </button>
        </span>
      </div>
      <div class="stg-row">
        <span class="stg-chip"><Icon name="folder" :size="18" /></span>
        <span class="stg-meta">
          <span class="stg-title">Move to group</span>
          <span class="stg-sub">Re-file this profile via saveProfile</span>
        </span>
        <span class="stg-ctl stg-ctl--pair">
          <PickSelect
            v-model="moveGroup"
            icon="folder"
            title="Target group"
            aria-label="Target group"
            quiet
            :disabled="busy || !target"
            :options="groupOptions"
          />
          <button class="nb-act" :disabled="busy || !target" @click="moveToGroup">
            <Icon name="folder" :size="14" />Move
          </button>
        </span>
      </div>
      <div class="stg-row">
        <span class="stg-chip"><Icon name="code" :size="18" /></span>
        <span class="stg-meta">
          <span class="stg-title">Edit as JSON</span>
          <span class="stg-sub">Raw profile editor with validation</span>
        </span>
        <span class="stg-ctl">
          <button class="nb-act" :disabled="busy || !target" @click="openJson">
            <Icon name="code" :size="14" />Open editor
          </button>
        </span>
      </div>
      <div v-if="msg" class="muted">{{ msg }}</div>
    </template>
    <JsonEditorModal
      v-model="showJson"
      :profile="editingProfile"
      @saved="onProfileSaved"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import Icon from '../Icon.vue'
import PickSelect from '../PickSelect.vue'
import JsonEditorModal from '../JsonEditorModal.vue'
import { useProfilesStore } from '../../stores/profiles'
import { useConnectionStore } from '../../stores/connection'
import { useSettingsStore } from '../../stores/settings'
import { backend } from '../../api'
import type { Profile } from '../../types'

const profiles = useProfilesStore()
const connection = useConnectionStore()
const settings = useSettingsStore()

const targetId = ref(settings.settings.activeProfileId || profiles.profiles[0]?.id || '')
const busy = ref(false)
const busyWhat = ref('')
const note = ref('')
const msg = ref('')
const moveGroup = ref('')
const showJson = ref(false)
const editingProfile = ref<Profile | null>(null)

const target = computed(() => profiles.profiles.find((p) => p.id === targetId.value) ?? null)

// A row probe switches the active profile and reconnects, so it must not
// start while another connect owns the core — the switch would be refused
// and the measurement would ride whatever tunnel is already up.
const probeBlocked = computed(() => busy.value || connection.busy || connection.connectBusy)

const groupNames = computed(() =>
  [...new Set(profiles.profiles.map((p) => p.group).filter((g): g is string => !!g))].sort(),
)

const targetOptions = computed(() =>
  profiles.profiles.map((p) => ({ value: p.id, label: p.name })),
)
const groupOptions = computed(() => [
  { value: '', label: '(no group)' },
  ...groupNames.value.map((g) => ({ value: g, label: g })),
  { value: '__new__', label: 'New group…' },
])

// Keep the selector on a live profile when the list changes.
watch(() => profiles.profiles.length, () => {
  if (!profiles.profiles.some((p) => p.id === targetId.value)) {
    targetId.value = profiles.profiles[0]?.id || ''
  }
})

/** Temporarily switch the active profile to id, run fn (a network
 *  tool against the now-active core), then restore the previous
 *  selection and connection. Moved with the row probes. */
async function withSwitchedProfile(id: string, fn: () => Promise<void>) {
  const prevActive = settings.settings.activeProfileId
  const wasRunning = connection.status.running
  const wasProfile = connection.status.profileId
  if (wasRunning && wasProfile === id) {
    await fn()
    return
  }
  await settings.setActiveProfileId(id)
  try {
    if (!(await connection.connect(id))) {
      // Measuring now would ride the PREVIOUS tunnel while the note labels
      // the result with this profile's name — a wrong answer presented as
      // a right one. The finally below still restores the old selection.
      throw new Error(connection.error || 'another connect is in progress')
    }
    await fn()
  } finally {
    try {
      if (prevActive !== id) await settings.setActiveProfileId(prevActive)
    } catch {
      /* keep the probed selection when restore fails */
    }
    if (wasRunning && wasProfile && wasProfile !== id) {
      await connection.connect(wasProfile)
    } else if (!wasRunning) {
      await connection.disconnect()
    }
    await profiles.load()
  }
}

async function probeSpeed() {
  if (probeBlocked.value || !target.value) return
  busy.value = true
  busyWhat.value = 'speed'
  note.value = 'Switching…'
  msg.value = ''
  try {
    await withSwitchedProfile(target.value.id, async () => {
      note.value = 'Measuring…'
      const r = await backend.startSpeedTest()
      if (r.error) note.value = `Speed failed: ${r.error}`
      else note.value = `↓ ${r.downloadMbps ?? '—'} Mbps · ↑ ${r.uploadMbps ?? '—'} Mbps · ${r.latencyMs ?? '—'} ms`
    })
  } catch (e) {
    note.value = String(e)
  } finally {
    busy.value = false
    busyWhat.value = ''
  }
}

async function probeIP() {
  if (probeBlocked.value || !target.value) return
  busy.value = true
  busyWhat.value = 'ip'
  note.value = 'Switching…'
  msg.value = ''
  try {
    await withSwitchedProfile(target.value.id, async () => {
      note.value = 'Probing…'
      const r = await backend.startIPTest()
      if (r.error) note.value = `IP failed: ${r.error}`
      else note.value = `IP ${r.ip ?? '—'}${r.country || r.cc ? ` (${r.country || r.cc})` : ''}`
    })
  } catch (e) {
    note.value = String(e)
  } finally {
    busy.value = false
    busyWhat.value = ''
  }
}

/** Clone the profile via saveProfile (new id + suffix). */
async function duplicate() {
  if (!target.value) return
  busy.value = true
  msg.value = ''
  try {
    const p = target.value
    const copy: Profile = {
      ...p, id: '', name: `${p.name} (copy)`,
      lastLatency: undefined, lastTestedAt: undefined, lastConnectedAt: undefined,
    }
    const saved = await profiles.save(copy)
    msg.value = `Duplicated as ${saved.name}.`
    targetId.value = saved.id
  } catch (e) {
    msg.value = String(e)
  } finally {
    busy.value = false
  }
}

/** Move the profile to a group via saveProfile. */
async function moveToGroup() {
  if (!target.value) return
  let g = moveGroup.value
  if (g === '__new__') {
    const name = prompt('New group name:')
    if (name === null) return
    g = name.trim()
    if (!g) return
  }
  busy.value = true
  msg.value = ''
  try {
    await profiles.save({ ...target.value, group: g || undefined })
    msg.value = g ? `Moved to ${g}.` : 'Removed from group.'
    moveGroup.value = ''
  } catch (e) {
    msg.value = String(e)
  } finally {
    busy.value = false
  }
}

function openJson() {
  editingProfile.value = target.value
  showJson.value = true
}

async function onProfileSaved() {
  await profiles.load()
}
</script>
