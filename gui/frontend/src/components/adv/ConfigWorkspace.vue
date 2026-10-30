<!-- Advanced Config: simple card stack. Section editors + raw editor. -->
<template>
  <div class="nb-card nb-page">
    <h3>Active overrides</h3>
    <p class="muted">What's stored in Settings (raw wins over sections).</p>
    <div v-if="overrideKeys.length === 0" class="nb-card nb-empty">
      <p class="muted">No overrides stored — editors render manager defaults.</p>
      <button class="nb-act nb-act--primary" @click="focusEditors">
        <Icon name="tune" :size="14" />Edit a section
      </button>
    </div>
    <template v-else>
      <div v-for="k in overrideKeys" :key="k" class="stg-row">
        <span class="stg-chip"><Icon name="tune" :size="18" /></span>
        <span class="stg-meta">
          <span class="stg-title">{{ k === '*' ? 'raw (whole config)' : k }}</span>
          <span class="stg-sub">{{ overrideSize(k) }}</span>
        </span>
      </div>
    </template>
  </div>

  <div class="nb-card nb-page">
    <h3>Section editors</h3>
    <p class="muted">{{ cfgDesc }}</p>
    <div v-if="!activeProfile" class="nb-card nb-empty">
      <p class="muted">No active profile — pick one in Proxies.</p>
      <button class="nb-act nb-act--primary" @click="$emit('goto', 'proxies')">
        <Icon name="servers" :size="14" />Open Proxies
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
      <p v-if="cfgTab === 'raw'" class="muted">Raw config lives in the panel below — section edits are disabled while it is open.</p>
      <CoreConfigEditor
        v-else
        ref="cfgEditor"
        :section="cfgTab"
        mode="section"
        :notice="rawNotice"
        :description="cfgDesc"
        :disabled="cfgDisabledNote !== ''"
        :disabled-note="cfgDisabledNote"
      />
    </template>
  </div>

  <div class="nb-card nb-page">
    <h3>Raw config</h3>
    <p class="muted">The entire rendered config in one editor.</p>
    <div v-if="!activeProfile" class="nb-card nb-empty">
      <p class="muted">No active profile — pick one in Proxies.</p>
      <button class="nb-act nb-act--primary" @click="$emit('goto', 'proxies')">
        <Icon name="servers" :size="14" />Open Proxies
      </button>
    </div>
    <CoreConfigEditor
      v-else
      ref="rawEditor"
      section="raw"
      mode="raw"
      :notice="rawNotice"
      description="The entire rendered config for the active profile in one editor. Saving replaces the per-section overrides above."
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

defineEmits<{
  (e: 'goto', view: 'proxies'): void
}>()

const settings = useSettingsStore()
const profiles = useProfilesStore()
const connection = useConnectionStore()

const cfgSections = [
  { key: 'route', label: 'Route Rules', desc: 'Route rules decide which outbound each connection uses. Edits sing-box "route" (xray "routing").' },
  { key: 'dns', label: 'DNS Rules', desc: 'DNS servers and rules for the core resolver. Edits "dns" on either core.' },
  { key: 'inbound', label: 'Inbound', desc: 'Local listener set (a JSON array). Edits "inbounds" on either core.' },
  { key: 'experimental', label: 'Experimental', desc: 'Sing-box experimental block (cache, clash API, ...). Sing-box only.' },
  { key: 'raw', label: 'Raw Config', desc: 'The entire rendered config for the active profile in one editor. Saving replaces the per-section overrides above.' },
]
const rawNotice = 'Saving this raw config will replace your per-section overrides.'
const cfgTab = ref('route')
const cfgEditor = ref<{ isDirty: () => boolean } | null>(null)
const rawEditor = ref<{ isDirty: () => boolean } | null>(null)
const cfgDesc = computed(() => cfgSections.find((st) => st.key === cfgTab.value)?.desc ?? '')

function isDirty(): boolean {
  return cfgEditor.value?.isDirty() === true || rawEditor.value?.isDirty() === true
}

function switchCfgTab(key: string) {
  if (key === cfgTab.value) return
  if (isDirty() && !confirm('Discard changes?')) return
  cfgTab.value = key
}

function switchTo(sub: string) {
  if (cfgSections.some((st) => st.key === sub)) {
    if (isDirty() && sub !== cfgTab.value && !confirm('Discard changes?')) return
    cfgTab.value = sub
  }
}
defineExpose({ switchTo, isDirty })

function focusEditors() {
  if (cfgTab.value === 'raw') switchCfgTab('route')
}

const activeProfile = computed(() => {
  const id = settings.settings.activeProfileId
  return profiles.profiles.find((p) => p.id === id) ?? profiles.profiles[0] ?? null
})

const cfgDisabledNote = computed(() => {
  if (!connection.status.running)
    return 'Connect first - the editor follows the active core.'
  if (cfgTab.value === 'experimental' && connection.status.core === 'xray')
    return 'Experimental is only available with the sing-box core.'
  return ''
})

const rawDisabledNote = computed(() => {
  if (!connection.status.running) return 'Connect a profile to edit its raw config.'
  return ''
})

const overrideKeys = computed(() => Object.keys(settings.settings.coreOverrides ?? {}))

function overrideSize(k: string): string {
  const v = (settings.settings.coreOverrides ?? {})[k]
  const n = v === undefined || v === null ? 0 : String(v).length
  return `${n} chars stored`
}
</script>
