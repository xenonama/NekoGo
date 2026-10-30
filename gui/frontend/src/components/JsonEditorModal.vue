<!-- Advanced Mode Phase B: editable raw-JSON profile editor.
     Plain <textarea> only (no editor deps). Validation is hand-rolled:
     JSON parse + required fields + immutable id + port range +
     numeric core enum (0 = sing-box, 1 = xray). Saves through the
     existing backend.saveProfile; backend errors are shown verbatim
     and the modal stays open with the draft preserved. -->
<template>
  <div
    v-if="modelValue && profile"
    class="nb-modal-overlay"
    @click.self="requestClose"
  >
    <div
      class="nb-modal nb-modal-wide"
      role="dialog"
      aria-label="Edit profile as JSON"
    >
      <div class="nb-modal-head">
        <h3 style="margin: 0">Edit profile as JSON</h3>
        <button
          class="nb-icon-btn"
          title="Close"
          aria-label="Close"
          @click="requestClose"
        >
          <Icon name="x" :size="16" />
        </button>
      </div>
      <p class="muted nb-modal-hint">
        Editing <strong>{{ profile.name }}</strong> — the
        <code>id</code> field cannot be changed.
      </p>
      <textarea
        ref="textRef"
        v-model="draft"
        class="nb-json-text selectable"
        rows="16"
        spellcheck="false"
        aria-label="Profile JSON"
      />
      <div class="nb-json-bar">
        <span v-if="validation.ok" class="nb-json-valid">Valid JSON</span>
        <span v-else class="nb-json-error">{{ validation.error }}</span>
        <span class="spacer" />
        <button class="nb-act nb-act--sm nb-act--ghost" :disabled="!canFormat" title="Re-format JSON" @click="format">
          <Icon name="code" :size="13" />Format
        </button>
      </div>
      <div v-if="backendError" class="nb-json-error nb-json-backend">
        {{ backendError }}
      </div>
      <div class="nb-modal-foot">
        <button class="nb-act nb-act--ghost" :disabled="saving" @click="reset">
          <Icon name="refresh" :size="14" />Reset
        </button>
        <button class="nb-act nb-act--ghost" :disabled="saving" @click="requestClose">
          <Icon name="x" :size="14" />Cancel
        </button>
        <button
          class="nb-act nb-act--primary"
          :disabled="!validation.ok || saving"
          @click="save"
        >
          <Icon name="check" :size="14" />{{ saving ? 'Saving…' : 'Save' }}
        </button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onUnmounted, ref, watch } from 'vue'
import Icon from './Icon.vue'
import { backend } from '../api'
import type { Profile } from '../types'

const props = defineProps<{
  modelValue: boolean
  profile: Profile | null
}>()

const emit = defineEmits<{
  (e: 'update:modelValue', v: boolean): void
  (e: 'saved', p: Profile): void
}>()

const draft = ref('')
const initial = ref('')
const backendError = ref('')
const saving = ref(false)
const textRef = ref<HTMLTextAreaElement | null>(null)

function snapshot(p: Profile): string {
  return JSON.stringify(p, null, 2)
}

function openInit() {
  if (props.profile) {
    initial.value = snapshot(props.profile)
    draft.value = initial.value
  } else {
    initial.value = ''
    draft.value = ''
  }
  backendError.value = ''
  saving.value = false
  void nextTick(() => textRef.value?.focus())
}

// BUG-006: observe the profile identity too, not just open state —
// opening Edit-as-JSON on B while open on A must re-init the draft,
// otherwise the header/validation target B while the draft stays on A
// and `id cannot be changed` locks Save forever.
watch(
  [() => props.modelValue, () => props.profile?.id],
  ([open, id], [wasOpen, wasId]) => {
    if (open && (!wasOpen || id !== wasId)) openInit()
  },
)

function onEsc(e: KeyboardEvent) {
  if (e.key === 'Escape' && props.modelValue) requestClose()
}

watch(
  () => props.modelValue,
  (open) => {
    if (open) window.addEventListener('keydown', onEsc)
    else window.removeEventListener('keydown', onEsc)
  },
)

// BUG-005: the window listener was added/removed only by the watcher,
// so unmounting with the modal open leaked it (later ESC calls
// requestClose on a dead component). The template @keydown.esc was a
// second ESC path showing confirm() twice — removed, window listener
// is the single path now.
onUnmounted(() => window.removeEventListener('keydown', onEsc))

interface DraftValidation {
  ok: boolean
  parsed?: Profile
  error?: string
}

/** Hand-rolled Profile shape check (no schema library per Phase B spec).
 *  NOTE: core is the numeric CoreType (0 = sing-box, 1 = xray,
 *  2 = sni-spoof), not the 'singbox'/'xray' strings — the Go backend
 *  unmarshals it into an int. Core 2 passes any protocol through:
 *  the spoof engine enforces trojan/vless at Start, not at save. */
function validateDraft(
  text: string,
  original: Profile,
): DraftValidation {
  let parsed: unknown
  try {
    parsed = JSON.parse(text)
  } catch (e) {
    const msg = e instanceof Error ? e.message : String(e)
    return { ok: false, error: 'Invalid JSON: ' + msg }
  }
  if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed))
    return { ok: false, error: 'Profile must be a JSON object' }
  const p = parsed as Record<string, unknown>
  // BUG-018: mirror the backend save rule (gui/app.go SaveProfile +
  // validateProfileForSave) — a rawOutbound-only profile is backend-legal
  // with empty address/protocol and an unused port, so the editor must
  // not demand them. Only id/name/core are unconditionally required.
  for (const k of ['id', 'name', 'core']) {
    if (p[k] === undefined || p[k] === null || p[k] === '')
      return { ok: false, error: `Missing required field: ${k}` }
  }
  if (p['id'] !== original.id)
    return { ok: false, error: 'id cannot be changed' }
  if (p['core'] !== 0 && p['core'] !== 1 && p['core'] !== 2)
    return { ok: false, error: 'core must be 0 (sing-box), 1 (xray) or 2 (sni-spoof)' }
  const hasRaw = typeof p['rawOutbound'] === 'string' && p['rawOutbound'] !== ''
  if (!hasRaw) {
    if (p['address'] === undefined || p['address'] === null || p['address'] === '')
      return { ok: false, error: 'Missing required field: address' }
    if (
      typeof p['port'] !== 'number' ||
      !Number.isInteger(p['port']) ||
      (p['port'] as number) < 1 ||
      (p['port'] as number) > 65535
    )
      return { ok: false, error: 'port must be 1..65535' }
  }
  // BUG-011: reject protocols the builders cannot route, so the failure
  // surfaces in the editor instead of at Connect. Raw passthrough
  // profiles bypass the check (any protocol is legal inside raw JSON).
  // Core 2 (sni-spoof) passes everything through: the engine enforces
  // trojan/vless at Start with a clear error, not at save.
  // Lists mirror gui/app.go validateProfileForSave — keep in sync.
  if (!hasRaw && p['core'] !== 2) {
    const routable =
      p['core'] === 1
        ? ['vless', 'vmess', 'trojan', 'shadowsocks']
        : ['shadowsocks', 'vmess', 'vless', 'trojan', 'socks', 'http', 'direct']
    if (!routable.includes(p['protocol'] as string))
      return {
        ok: false,
        error: `protocol "${String(p['protocol'])}" needs rawOutbound JSON for ${p['core'] === 1 ? 'xray' : 'sing-box'}`,
      }
  }
  // TLS fingerprint/fragment mirror gui/fingerprint.go + validateFragment:
  // optional, but when TLS renders they must name values both cores
  // accept (otherwise the core rejects the config at startup).
  const rangePair = (v: unknown): [number, number] | null => {
    if (typeof v !== 'string') return null
    const m = v.trim().match(/^(\d+)-(\d+)$/)
    if (!m) return null
    return [Number(m[1]), Number(m[2])]
  }
  if (p['tlsFragment'] !== undefined && p['tlsFragment'] !== null && p['tlsFragment'] !== '') {
    const mode = String(p['tlsFragment']).toLowerCase()
    if (mode !== 'off' && mode !== 'tlshello') {
      const r = rangePair(mode)
      if (!r || r[0] <= 0 || r[1] < r[0])
        return { ok: false, error: 'tlsFragment must be "off", "tlshello" or "N-M" (N >= 1)' }
    }
  }
  if (p['tlsFragmentLen'] !== undefined && p['tlsFragmentLen'] !== null && p['tlsFragmentLen'] !== '') {
    const r = rangePair(p['tlsFragmentLen'])
    if (!r || r[0] < 0 || r[1] < r[0] || r[1] === 0)
      return { ok: false, error: 'tlsFragmentLen must be "A-B" (e.g. "10-20")' }
  }
  if (p['tlsFragmentInt'] !== undefined && p['tlsFragmentInt'] !== null && p['tlsFragmentInt'] !== '') {
    if (typeof p['tlsFragmentInt'] !== 'number' || !Number.isInteger(p['tlsFragmentInt']) || (p['tlsFragmentInt'] as number) < 0)
      return { ok: false, error: 'tlsFragmentInt must be a non-negative integer (ms)' }
  }
  const rendersTLS =
    p['tls'] === true ||
    p['realityEnabled'] === true ||
    p['protocol'] === 'trojan' ||
    (typeof p['flow'] === 'string' && (p['flow'] as string).trim() !== '')
  if (!hasRaw && rendersTLS) {
    // Effective fingerprint like the backend: preset wins unless
    // empty/"none", else the legacy field (empty = chrome default).
    const preset = String(p['tlsFingerprint'] ?? '').trim().toLowerCase()
    const legacy = String(p['fingerprint'] ?? '').trim().toLowerCase()
    const eff = preset !== '' && preset !== 'none' ? preset : legacy
    const FPS = ['chrome', 'firefox', 'safari', 'ios', 'android', 'edge', '360', 'qq', 'random', 'randomized']
    if (eff !== '' && !FPS.includes(eff))
      return { ok: false, error: `fingerprint "${eff}" is not accepted by sing-box/xray (want one of ${FPS.join(', ')})` }
  }
  // Mux mirrors gui/mux_fragment.go validateMux — keep in sync.
  // Raw passthrough profiles skip these (fields are ignored there).
  if (!hasRaw && p['muxEnabled']) {
    const proto = String(p['muxProtocol'] ?? '').trim().toLowerCase()
    if (proto !== '' && !['h2mux', 'smux', 'yamux'].includes(proto))
      return { ok: false, error: 'muxProtocol must be one of h2mux, smux, yamux' }
    if (p['muxConcurrency'] !== undefined && p['muxConcurrency'] !== null && p['muxConcurrency'] !== '') {
      if (typeof p['muxConcurrency'] !== 'number' || !Number.isInteger(p['muxConcurrency']) || (p['muxConcurrency'] as number) < 0 || (p['muxConcurrency'] as number) > 1024)
        return { ok: false, error: 'muxConcurrency must be an integer 0..1024 (0 = core default)' }
    }
    if (typeof p['flow'] === 'string' && p['flow'].trim() !== '')
      return { ok: false, error: 'mux cannot be combined with an xtls flow (disable mux or clear the flow)' }
  }
  return { ok: true, parsed: p as unknown as Profile }
}

const validation = computed<DraftValidation>(() => {
  if (!props.profile) return { ok: false, error: 'No profile selected' }
  return validateDraft(draft.value, props.profile)
})

const isDirty = computed(() => draft.value !== initial.value)

const canFormat = computed(() => {
  try {
    JSON.parse(draft.value)
    return true
  } catch {
    return false
  }
})

function format() {
  try {
    draft.value = JSON.stringify(JSON.parse(draft.value), null, 2)
  } catch {
    /* unparseable: validation row already shows the error */
  }
}

/** Reset discards unsaved edits, restoring the open-time snapshot
 *  (BUG-018): restoring the live store value would leave
 *  draft !== initial after a background profile update, so isDirty
 *  stays true and Cancel still prompts right after a Reset. */
function reset() {
  draft.value = initial.value
  backendError.value = ''
}

function requestClose() {
  if (isDirty.value && !confirm('Discard changes?')) return
  emit('update:modelValue', false)
}

async function save() {
  const v = validation.value
  if (!v.ok || !v.parsed) return
  saving.value = true
  backendError.value = ''
  try {
    const saved = await backend.saveProfile(v.parsed)
    emit('saved', saved)
    emit('update:modelValue', false)
  } catch (e) {
    // Stay open with the draft preserved so the user can fix and retry.
    backendError.value = String(e)
  } finally {
    saving.value = false
  }
}
</script>
