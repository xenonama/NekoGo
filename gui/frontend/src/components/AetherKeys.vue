<!-- WARP keys card, ported from PattN's AetherKeysWindow: the kinds
     --register takes on aether 2.3.0, the API address, fragment and ECH
     for the calls to the WARP API, the fingerprint, an optional carrier,
     and the command box. PattN's API server name / API host / exit-node
     rows belong to a newer fork and are left out: this core refuses them.
     The core's own output lands in the Aether log card below. -->
<template>
  <div class="nb-card" style="margin-top: 14px">
    <div class="toolbar" style="padding: 0">
      <strong>WARP keys</strong>
      <span v-if="running" class="muted">Getting new WARP keys…</span>
      <button class="nb-act nb-act--ghost" :disabled="running || !backendAvailable" @click="refresh">
        <Icon name="refresh" :size="14" />Refresh
      </button>
    </div>
    <p class="muted">
      Registers new WARP identities with the Aether core. The keys in use stay in place until every new
      one of the kind is ready, so a failed or cancelled run keeps them. New keys are used from the next
      connection.
    </p>

    <div class="keys-ident">
      <div v-for="k in aether.keysList" :key="k.file" class="keys-ident-row">
        <span class="mono">{{ keyLine(k) }}</span>
      </div>
      <div v-if="aether.keysList.length === 0" class="muted">Reading the WARP keys…</div>
    </div>

    <div class="form-grid">
      <label>Get new keys for</label>
      <PickSelect v-model="draft.kind" icon="layers" title="Get new keys for" aria-label="Get new keys for" quiet :options="kindOptions" />
      <label>API address</label>
      <input v-model="draft.enrollAddress" placeholder="blank = the address of the API server name" />
      <label>Fragment ClientHello</label><input type="checkbox" v-model="draft.fragment" />
      <p class="muted aether-span">
        Sends the ClientHello of the calls to the WARP API in pieces, for networks that filter the name
        of the API.
      </p>
      <template v-if="draft.fragment">
        <label>Fragment size</label><input v-model="draft.fragmentSize" placeholder="8-16" />
        <label>Fragment delay (ms)</label><input v-model="draft.fragmentDelay" placeholder="2-10" />
      </template>
      <label>ECH</label><input type="checkbox" v-model="draft.ech" />
      <p class="muted aether-span">
        Hides the server name of the calls to the WARP API; without a usable ECH key, no WARP key is
        requested.
      </p>
      <template v-if="draft.ech">
        <label>ECH DNS resolver</label>
        <input v-model="draft.echDns" list="aether-keys-ech-dns" placeholder="empty = use DNS resolvers" />
        <label>ECH domain tree</label>
        <input v-model="draft.echDomain" list="aether-keys-ech-domain" placeholder="cloudflare-ech.com" />
      </template>
      <label>Fingerprint</label>
      <PickSelect v-model="draft.fingerprint" icon="tune" title="Fingerprint" aria-label="Fingerprint" quiet :options="fingerprintOptions" />
      <label>Reach the API through</label>
      <PickSelect v-model="draft.carrier" icon="shield" title="Reach the API through" aria-label="Reach the API through" quiet :options="carrierOptions" />
      <p v-if="draft.carrier" class="muted aether-span">
        The carrier has to come up before the calls to the WARP API can ride it, so the run is given
        longer to finish.
      </p>

      <label>Aether command</label>
      <div class="aether-cmd">
        <textarea v-model="commandText" rows="3" spellcheck="false" />
        <template v-if="commandIsCustom">
          <p class="muted">Edited by hand: this line runs as written and the settings above are not applied.</p>
          <button class="nb-act nb-act--ghost" @click="useSettings">
            <Icon name="refresh" :size="14" />Use the settings
          </button>
        </template>
        <p v-else class="muted">The line these settings build. Edit it to run your own.</p>
      </div>
    </div>

    <div class="toolbar" style="padding: 12px 0 0">
      <button class="nb-act nb-act--primary" :disabled="getDisabled" @click="getKeys">
        <Icon name="download" :size="14" />{{ running ? 'Getting new WARP keys…' : 'Get new WARP keys' }}
      </button>
      <button v-if="running" class="nb-act nb-act--warn" @click="cancel">
        <Icon name="x" :size="14" />Cancel
      </button>
      <span v-if="errorText" class="latency-bad">{{ errorText }}</span>
    </div>
    <p v-if="running" class="muted">
      Registering new WARP keys; the keys in use stay in place until all the new ones are ready…
    </p>
    <p v-else-if="aether.keysStatus.lastOkAt" class="muted">
      The new WARP keys are in place and are used from the next connection ({{ fmtTime(aether.keysStatus.lastOkAt) }}).
    </p>
    <p v-if="tunnelRunning" class="muted">
      Disconnect the Aether profile before getting these keys; the running connection uses them.
    </p>
    <p v-if="scanRunning" class="muted">An Aether scan is running; wait for it to finish before getting new keys.</p>
    <p v-if="coreMissing" class="latency-bad">
      The Aether core is not installed; get aether-core under Settings → Updates.
    </p>

    <datalist id="aether-keys-ech-dns">
      <option v-for="v in echDnsPresets" :key="v" :value="v" />
    </datalist>
    <datalist id="aether-keys-ech-domain">
      <option v-for="v in echDomainPresets" :key="v" :value="v" />
    </datalist>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { backend, backendAvailable } from '../api'
import { useAetherStore } from '../stores/aether'
import { useSettingsStore } from '../stores/settings'
import Icon from './Icon.vue'
import PickSelect from './PickSelect.vue'
import type { AetherKeyInfo, AetherKeysSettings } from '../types'

const props = defineProps<{ tunnelRunning: boolean; scanRunning: boolean }>()

const aether = useAetherStore()
const settings = useSettingsStore()

const kindOptions = [
  { value: 'all', label: 'All (default)' },
  { value: 'wg', label: 'wg (wireguard)' },
  { value: 'masque', label: 'masque' },
  { value: 'gool', label: 'gool' },
  { value: 'mim', label: 'mim' },
]
const fingerprintOptions = [
  { value: 'chrome', label: 'chrome (default)' },
  { value: 'chrome-120', label: 'chrome 120 (no post-quantum share)' },
  { value: 'firefox', label: 'firefox' },
  { value: 'semi-python', label: 'semi-python' },
  { value: 'go', label: 'go' },
]
const carrierOptions = [
  { value: '', label: 'directly (default)' },
  { value: 'tor', label: 'Tor' },
  { value: 'psiphon', label: 'Psiphon' },
]
const echDnsPresets = [
  'udp://1.1.1.1',
  'udp://8.8.8.8',
  'https://1.1.1.1/dns-query@sni=www.microsoft.com',
  'https://doq.dns4all.eu/dns-query@address=194.0.5.3',
  'tcp://1.1.1.1',
  'tcp://8.8.8.8',
]
const echDomainPresets = [
  'cloudflare-ech.com',
  'crypto.cloudflare.com',
  'ip.gs',
  'api.cloudflareclient.com',
]

function blankKeys(): AetherKeysSettings {
  return {
    kind: 'all', enrollAddress: '', fragment: false, fragmentSize: '', fragmentDelay: '',
    ech: false, echDns: '', echDomain: '', fingerprint: '', carrier: '', command: '',
  }
}

const draft = reactive<AetherKeysSettings>(blankKeys())
const coreVersion = ref('')
const coreChecked = ref(false)
const saving = ref(false)

const running = computed(() => aether.keysStatus.running)
const coreMissing = computed(() => coreChecked.value && coreVersion.value === '')
const getDisabled = computed(() =>
  running.value || saving.value || !backendAvailable.value || coreMissing.value ||
  props.tunnelRunning || props.scanRunning,
)
const errorText = computed(() => {
  if (aether.keysError) return aether.keysError
  const s = aether.keysStatus
  // A failure of a run this session did not start (another tab, or before
  // a restart) is still the last thing that happened to the keys.
  if (s.lastError && (!s.lastOkAt || (s.lastErrorAt ?? '') > s.lastOkAt)) return s.lastError
  return ''
})

function keyLine(k: AetherKeyInfo): string {
  if (!k.present) return `${k.label} key: none yet; one is registered on the first scan or connection`
  const id = k.deviceId ?? ''
  const device = id.length > 8 ? `${id.slice(0, 8)}…` : id
  return `${k.label} key: device ${device}, IPv4 ${k.ipv4 || '—'}, IPv6 ${k.ipv6 || '—'}`
}

function fmtTime(t?: string): string {
  if (!t) return ''
  const d = new Date(t)
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleString(undefined, { hour12: false })
}

async function refresh() {
  await aether.refreshKeys()
  await checkCore()
}

async function checkCore() {
  if (!backendAvailable.value) {
    coreChecked.value = false
    return
  }
  try {
    coreVersion.value = await backend.aetherVersion()
  } catch {
    coreVersion.value = ''
  }
  coreChecked.value = true
}

async function getKeys() {
  if (getDisabled.value) return
  saving.value = true
  try {
    await persist()
  } catch (e) {
    aether.keysError = String(e)
    saving.value = false
    return
  }
  saving.value = false
  await aether.renewKeys({ ...draft })
}

async function cancel() {
  await aether.cancelKeys()
}

/** The card has no Save button of its own (PattN's window keeps its
 *  settings on close), so an edit is persisted once typing settles. The
 *  shared settings mirror is written too: a later save of the whole
 *  Settings must not put an older copy of these back. */
async function persist() {
  const copy = { ...draft }
  await backend.saveAetherKeysSettings(copy)
  settings.settings.aetherKeys = copy
}

const built = ref('')
const commandText = computed<string>({
  get: () => (draft.command?.trim() ? draft.command : built.value),
  set: (v) => {
    if (v === commandText.value) return
    if (!v.trim() && !draft.command?.trim()) return
    draft.command = v
  },
})
const commandIsCustom = computed(() => {
  const c = draft.command?.trim() ?? ''
  return c !== '' && c !== built.value.trim()
})

function useSettings() {
  draft.command = ''
}

let previewTimer: ReturnType<typeof setTimeout> | undefined
let saveTimer: ReturnType<typeof setTimeout> | undefined

async function refreshPreview() {
  if (!backendAvailable.value) {
    built.value = ''
    return
  }
  try {
    built.value = await backend.aetherKeysPreview({ ...draft })
  } catch {
    built.value = ''
  }
}

watch(
  draft,
  () => {
    if (previewTimer) clearTimeout(previewTimer)
    previewTimer = setTimeout(() => {
      previewTimer = undefined
      void refreshPreview()
    }, 250)
    if (saveTimer) clearTimeout(saveTimer)
    saveTimer = setTimeout(() => {
      saveTimer = undefined
      void persist().catch(() => {
        /* dev mode, or a transient IPC failure: the next edit retries */
      })
    }, 600)
  },
  { deep: true },
)

onMounted(() => {
  Object.assign(draft, blankKeys(), settings.settings.aetherKeys ?? {})
  void refreshPreview()
  void checkCore()
})

onUnmounted(() => {
  if (previewTimer) clearTimeout(previewTimer)
  // A tab switch destroys this card, so a pending edit is written now
  // rather than cancelled: there is no next edit to retry it.
  if (saveTimer) {
    clearTimeout(saveTimer)
    void persist().catch(() => {
      /* dev mode, or the window is already going down */
    })
  }
})
</script>

<style scoped>
.aether-span { grid-column: 1 / -1; margin: 0; }
.keys-ident { padding: 4px 0 10px; }
.keys-ident-row {
  padding: 4px 0; border-top: 1px solid var(--border);
  font-size: 12px; overflow-wrap: anywhere;
}
.keys-ident-row:first-child { border-top: 0; }
.aether-cmd { min-width: 0; }
.aether-cmd > p { margin: 6px 0; }
textarea {
  width: 100%; box-sizing: border-box; resize: vertical;
  font-family: Consolas, 'Cascadia Code', monospace; font-size: 12px;
  border-radius: 10px; padding: 8px 10px;
}
</style>