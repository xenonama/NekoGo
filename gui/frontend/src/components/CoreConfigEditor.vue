<!-- Advanced Mode Phase E: one core config section editor.
     Plain <textarea> only (zero new deps). The parent (SettingsPanel
     Advanced tab) renders one of these per sub-tab and guards sub-tab
     switches through the exposed isDirty() (JsonEditorModal pattern). -->
<template>
  <div class="nb-ccfg">
    <p class="muted">{{ description }}</p>
    <!-- Phase G: one-time warning when per-section overrides exist —
         saving raw replaces them. The checkbox persists the ack in
         Settings (not CoreOverrides). -->
    <div v-if="showOverrideWarning" class="muted" style="margin-bottom: 8px">
      <span>{{ notice }}</span>
      <label style="margin-left: 12px">
        <input type="checkbox" :checked="ack" @change="setAck(($event.target as HTMLInputElement).checked)" />
        Don't show again
      </label>
    </div>
    <div v-if="disabled" class="muted">{{ disabledNote }}</div>
    <template v-else>
      <div v-if="loading" class="muted">Loading section…</div>
      <template v-else>
        <textarea
          ref="areaRef"
          v-model="draft"
          class="nb-json-text selectable"
          rows="14"
          spellcheck="false"
          :aria-label="section + ' JSON'"
          :disabled="saving"
        />
        <div class="nb-json-bar">
          <span v-if="error" class="nb-json-error">{{ error }}</span>
          <span v-else-if="staleTarget" class="nb-json-error">Profile changed — Reload before saving.</span>
          <span v-else-if="dirty" class="muted">Unsaved changes</span>
          <span v-else class="muted">Loaded</span>
          <span class="spacer" />
          <button class="nb-act nb-act--sm nb-act--ghost" :disabled="saving || loading" title="Discard changes and load the current target again" @click="load">
            <Icon name="refresh" :size="13" />Reload
          </button>
          <button class="nb-act nb-act--sm nb-act--ghost" :disabled="saving" title="Pretty-print valid JSON" @click="format">
            <Icon name="code" :size="13" />Format
          </button>
          <button class="nb-act nb-act--sm nb-act--ghost" :disabled="saving" @click="validate">
            <Icon name="check" :size="13" />Validate
          </button>
        </div>
        <div class="nb-modal-foot">
          <button class="nb-act nb-act--ghost" :disabled="saving" @click="reset">
            <Icon name="refresh" :size="14" />Reset to defaults
          </button>
          <button class="nb-act nb-act--primary" :disabled="saving || !draft.trim()" @click="save">
            <Icon name="check" :size="14" />Save
          </button>
        </div>
        <div v-if="toast" class="muted">{{ toast }}</div>
      </template>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import Icon from './Icon.vue'
import { backend } from '../api'
import { useConnectionStore } from '../stores/connection'
import { useSettingsStore } from '../stores/settings'

const props = withDefaults(
  defineProps<{
    section: string
    description: string
    disabled: boolean
    disabledNote: string
    /** Phase G full-config variant: load/save go through the raw
     *  bindings and validation covers the whole config object. */
    mode?: 'section' | 'raw'
    /** One-time Phase G warning shown above the editor when set. */
    notice?: string
  }>(),
  { mode: 'section', notice: '' },
)

const connection = useConnectionStore()
const settings = useSettingsStore()

const ack = computed(() => settings.settings.rawOverrideAck === true)
const hasSectionOverrides = computed(() => {
  const ov = settings.settings.coreOverrides ?? {}
  return ['route', 'dns', 'inbound', 'experimental'].some((k) => {
    const v = ov[k]
    return v !== undefined && v !== null && String(v).trim() !== ''
  })
})
const showOverrideWarning = computed(
  () => props.mode === 'raw' && hasSectionOverrides.value && !ack.value && !props.disabled,
)

async function setAck(on: boolean) {
  settings.settings.rawOverrideAck = on
  try {
    await settings.save()
  } catch {
    /* dev mode without backend: keep local choice */
  }
}

const draft = ref('')
const loaded = ref('')
const loading = ref(true)
const saving = ref(false)
const error = ref('')
const toast = ref('')
const areaRef = ref<HTMLTextAreaElement | null>(null)
// The config target moved (a different profile is selected or running)
// while this draft was loaded. Saving then would store profile A's
// config as the app-wide override for B.
const staleTarget = ref(false)

const dirty = computed(() => draft.value !== loaded.value)

/** The profile the backend would render for this editor. */
const targetID = computed(() => connection.status.profileId || settings.settings.activeProfileId)

/** Reload the shell's settings snapshot after a core-config write: the
 *  backend owns CoreOverrides and the store otherwise keeps the startup
 *  value, which a later save would post back and use to drop the edit. */
async function refreshSettings() {
  try {
    await settings.load()
  } catch {
    /* dev mode */
  }
}

/** Parent sub-tab guard (JsonEditorModal discard pattern). */
function isDirty(): boolean {
  return dirty.value
}
defineExpose({ isDirty })

/** Client-side Validate: JSON.parse plus the light schema check the
 *  backend enforces. Section mode: inbound is an array, every other
 *  section is an object. Raw mode: the whole config is an object with
 *  the per-section rules applied to whichever keys are present. */
function clientValidate(text: string): string {
  let v: unknown
  try {
    v = JSON.parse(text)
  } catch (e) {
    return 'Invalid JSON: ' + (e instanceof Error ? e.message : String(e))
  }
  if (props.mode === 'raw') {
    if (typeof v !== 'object' || v === null || Array.isArray(v)) return 'Config must be a JSON object'
    const o = v as Record<string, unknown>
    if ('inbounds' in o && !Array.isArray(o['inbounds'])) return '"inbounds" must be a JSON array'
    for (const k of ['route', 'routing', 'dns', 'experimental']) {
      if (k in o && (typeof o[k] !== 'object' || o[k] === null || Array.isArray(o[k])))
        return `"${k}" must be a JSON object`
    }
    return ''
  }
  if (props.section === 'inbound') {
    if (!Array.isArray(v)) return 'inbound must be a JSON array'
  } else if (typeof v !== 'object' || v === null || Array.isArray(v)) {
    return `${props.section} must be a JSON object`
  }
  return ''
}

async function load() {
  loading.value = true
  error.value = ''
  toast.value = ''
  staleTarget.value = false
  try {
    const text =
      props.mode === 'raw'
        ? await backend.getRawConfig()
        : await backend.getCoreConfigSection(props.section)
    loaded.value = text
    draft.value = text
    void nextTick(() => areaRef.value?.focus())
  } catch (e) {
    error.value = String(e)
  } finally {
    loading.value = false
  }
}

function validate() {
  error.value = clientValidate(draft.value)
  if (!error.value) toast.value = 'Valid JSON.'
}

function format() {
  try {
    draft.value = JSON.stringify(JSON.parse(draft.value), null, 2)
    error.value = ''
  } catch (e) {
    error.value = 'Invalid JSON: ' + (e instanceof Error ? e.message : String(e))
  }
}

async function save() {
  if (staleTarget.value) {
    error.value = 'The active profile changed while this draft was open — press Reload before saving.'
    return
  }
  const v = clientValidate(draft.value)
  if (v) {
    error.value = v
    return
  }
  saving.value = true
  error.value = ''
  toast.value = ''
  try {
    if (props.mode === 'raw') {
      await backend.setRawConfig(draft.value)
      loaded.value = draft.value
      void refreshSettings()
      if (!confirm('Saved. Reload the core now to apply? (Disconnect + Connect)')) {
        toast.value = 'Saved. Reload the core later to apply.'
        return
      }
      await backend.reloadCoreNow()
      await connection.refresh()
      toast.value = 'Core reloaded with the new config.'
      return
    }
    await backend.setCoreConfigSection(props.section, draft.value)
    loaded.value = draft.value
    void refreshSettings()
    if (!confirm('Saved. Reload the core now to apply? (Disconnect + Connect)')) {
      toast.value = 'Saved. Reload the core later to apply.'
      return
    }
    const id = connection.status.profileId || settings.settings.activeProfileId
    if (!id) {
      toast.value = 'Saved. No active profile to reload.'
      return
    }
    await connection.disconnect()
    await connection.connect(id)
    if (connection.error) toast.value = 'Reload failed: ' + connection.error
    else toast.value = 'Core reloaded with the new config.'
  } catch (e) {
    error.value = String(e)
  } finally {
    saving.value = false
  }
}

async function reset() {
  saving.value = true
  error.value = ''
  toast.value = ''
  try {
    if (props.mode === 'raw') {
      // Clearing the raw override re-renders the section-by-section
      // form: the backend returns the fresh default directly.
      const fresh = await backend.setRawConfig('')
      loaded.value = fresh
      draft.value = fresh
      void refreshSettings()
      toast.value = 'Reset to defaults.'
      return
    }
    await backend.resetCoreConfigSection(props.section)
    void refreshSettings()
    // Show the resulting state: the manager default again.
    await load()
    toast.value = 'Reset to defaults.'
  } catch (e) {
    error.value = String(e)
  } finally {
    saving.value = false
  }
}

onMounted(() => {
  load()
  connection.acquire()
})
onUnmounted(() => connection.release())
watch(
  [() => props.section, () => props.mode],
  () => {
    void load()
  },
)
// The rendered config is profile-specific (section defaults and the raw
// config both resolve the active OR running profile). When the target
// moves while this draft is open, saving would apply A's config to B, so
// the draft is frozen until the user reloads.
watch(targetID, () => {
  if (dirty.value) {
    staleTarget.value = true
    return
  }
  void load()
})
</script>
