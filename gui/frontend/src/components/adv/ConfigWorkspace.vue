<!-- Advanced Config: three cards, one head each. The "Core config"
     group label comes from the shell divider and each card states its
     subject once, so no line here repeats another's text. -->
<template>
  <div class="stg-card">
    <div class="stg-head">
      <span class="stg-head-chip"><Icon name="layers" :size="22" /></span>
      <span class="stg-head-text">
        <span class="stg-head-title">Active overrides</span>
        <span class="stg-head-sub">What Settings stores — a raw override wins over the sections</span>
      </span>
    </div>
    <p v-if="overrideKeys.length === 0" class="muted">No overrides stored — the editors render manager defaults.</p>
    <div v-for="k in overrideKeys" :key="k" class="stg-row">
      <span class="stg-chip"><Icon name="tune" :size="18" /></span>
      <span class="stg-meta">
        <span class="stg-title">{{ k === '*' ? 'raw (whole config)' : k }}</span>
        <span class="stg-sub">{{ overrideSize(k) }}</span>
      </span>
    </div>
  </div>

  <div class="stg-card">
    <div class="stg-head">
      <span class="stg-head-chip"><Icon name="tune" :size="22" /></span>
      <span class="stg-head-text">
        <span class="stg-head-title">Section editors</span>
        <span class="stg-head-sub">{{ cfgDesc }}</span>
        <span v-if="targetLabel" class="stg-head-sub">Editing {{ targetLabel }}</span>
      </span>
    </div>
    <div v-if="!activeProfile" class="nb-adv-blank">
      <span class="muted">No active profile — pick one in Proxies.</span>
      <button class="nb-act nb-act--sm nb-act--ghost" @click="$emit('goto', 'proxies')">
        <Icon name="servers" :size="13" />Open Proxies
      </button>
    </div>
    <template v-else>
      <div class="console-tabs" role="tablist">
        <button
          v-for="st in cfgSections"
          :key="st.key"
          class="console-tab"
          :class="{ active: cfgTab === st.key }"
          role="tab"
          :aria-selected="cfgTab === st.key"
          @click="switchCfgTab(st.key)"
        >
          {{ st.label }}
        </button>
      </div>
      <CoreConfigEditor
        ref="cfgEditor"
        :section="cfgTab"
        mode="section"
        :disabled="cfgDisabledNote !== ''"
        :disabled-note="cfgDisabledNote"
      />
    </template>
  </div>

  <div class="stg-card">
    <div class="stg-head">
      <span class="stg-head-chip"><Icon name="code" :size="22" /></span>
      <span class="stg-head-text">
        <span class="stg-head-title">Raw config</span>
        <span class="stg-head-sub">The entire rendered config for {{ targetLabel || 'the active profile' }}</span>
      </span>
    </div>
    <div v-if="!activeProfile" class="nb-adv-blank">
      <span class="muted">No active profile — pick one in Proxies.</span>
      <button class="nb-act nb-act--sm nb-act--ghost" @click="$emit('goto', 'proxies')">
        <Icon name="servers" :size="13" />Open Proxies
      </button>
    </div>
    <CoreConfigEditor
      v-else
      ref="rawEditor"
      section="raw"
      mode="raw"
      :notice="rawNotice"
      :disabled="rawDisabledNote !== ''"
      :disabled-note="rawDisabledNote"
    />
  </div>
</template>
<script setup lang="ts">
import { computed, ref } from 'vue'
import Icon from '../Icon.vue'
import CoreConfigEditor from '../CoreConfigEditor.vue'
import { useSettingsStore } from '../../stores/settings'
import { useProfilesStore } from '../../stores/profiles'
import { useConnectionStore } from '../../stores/connection'
import { useConfirmStore } from '../../stores/confirm'
import { configTargetProfile } from '../../configTarget'

defineEmits<{
  (e: 'goto', view: 'proxies'): void
}>()

const settings = useSettingsStore()
const profiles = useProfilesStore()
const connection = useConnectionStore()
const confirmBox = useConfirmStore()

const cfgSections = [
  { key: 'route', label: 'Route Rules', desc: 'Route rules decide which outbound each connection uses. Edits sing-box "route" (xray "routing").' },
  { key: 'dns', label: 'DNS Rules', desc: 'DNS servers and rules for the core resolver. Edits "dns" on sing-box and xray.' },
  { key: 'inbound', label: 'Inbound', desc: 'Local listener set (a JSON array). Edits "inbounds" on sing-box and xray.' },
  { key: 'experimental', label: 'Experimental', desc: 'Sing-box experimental block (cache, clash API, ...). Sing-box only.' },
]
const rawNotice = 'Saving this raw config will replace your per-section overrides.'
const cfgTab = ref('route')
const cfgEditor = ref<{ isDirty: () => boolean } | null>(null)
const rawEditor = ref<{ isDirty: () => boolean } | null>(null)
const cfgDesc = computed(() => cfgSections.find((st) => st.key === cfgTab.value)?.desc ?? '')

function isDirty(): boolean {
  return cfgEditor.value?.isDirty() === true || rawEditor.value?.isDirty() === true
}

async function switchCfgTab(key: string) {
  if (key === cfgTab.value) return
  if (
    isDirty() &&
    !(await confirmBox.ask({
      title: 'Discard changes?',
      body: 'The editor holds unsaved JSON. Switching sections throws it away.',
      confirmLabel: 'Discard',
      danger: true,
    }))
  )
    return
  cfgTab.value = key
}

defineExpose({ isDirty })

const activeProfile = computed(() =>
  configTargetProfile(connection.status, settings.settings.activeProfileId, profiles.profiles),
)

/** Names the profile the backend actually renders, so a running core
 *  that differs from the stored selection is visible instead of silent. */
const targetLabel = computed(() => {
  const p = activeProfile.value
  if (!p) return ''
  const core = p.core === 1 ? 'Xray' : p.core === 3 ? 'Mihomo' : 'sing-box'
  return `${p.name} (${core})`
})

// The backend refuses both editors for a mihomo profile: it renders its
// own Clash Meta document and never merges these overrides, so offering
// an edit here would be an edit that cannot take effect. Gate on the
// profile (as Go does), not the running core — it holds while idle too.
const mihomoProfile = computed(() => activeProfile.value?.core === 3)
const mihomoNote =
  'mihomo renders its own Clash Meta document — these editors do not apply. Use Backup → View raw config to read what mihomo was given.'

const cfgDisabledNote = computed(() => {
  if (mihomoProfile.value) return mihomoNote
  if (!connection.status.running)
    return 'Connect first - the editor follows the active core.'
  if (cfgTab.value === 'experimental' && connection.status.core === 'xray')
    return 'Experimental is only available with the sing-box core.'
  return ''
})

const rawDisabledNote = computed(() => {
  if (mihomoProfile.value) return mihomoNote
  if (!connection.status.running) return 'Connect a profile to edit its raw config.'
  return ''
})

const overrideKeys = computed(() => Object.keys(settings.settings.coreOverrides ?? {}))

function overrideSize(k: string): string {
  const v = (settings.settings.coreOverrides ?? {})[k]
  // The wire value is parsed JSON (Go json.RawMessage), so String(v) on
  // an object is "[object Object]" — 15 chars for every section. Measure
  // the JSON text: that is what the editor loads and the core is given.
  const n = v === undefined || v === null ? 0 : JSON.stringify(v).length
  return `${n} chars stored`
}
</script>
