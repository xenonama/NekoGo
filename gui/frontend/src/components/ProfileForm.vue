<template>
  <div class="panel">
    <h3>{{ model.id ? 'Edit profile' : 'Add profile' }}</h3>
    <div class="form-grid">
      <label>Name</label><input v-model="model.name" />
      <label>Core</label>
      <PickSelect
        :model-value="String(model.core)"
        icon="power"
        title="Core"
        aria-label="Core"
        quiet
        :options="coreOptions"
        @update:model-value="model.core = Number($event) as Profile['core']"
      />
      <p v-if="model.core === 2" class="muted">Routes trojan/vless through the built-in sni-spoof core. Requires admin + WinDivert (Windows only).</p>
      <label>Protocol</label>
      <PickSelect v-model="model.protocol" icon="globe" title="Protocol" aria-label="Protocol" quiet :options="protocolOptions" />
      <p v-if="model.protocol === 'aether'" class="muted">Runs the Aether engine next to sing-box (like PattNG): the profile dials its internal SOCKS listener.</p>
      <AetherFields v-if="model.protocol === 'aether'" :model="model" />
      <label>Address</label><input v-model="model.address" placeholder="example.com" />
      <label>Port</label><input v-model.number="model.port" type="number" />
      <label>ID / Password</label><input v-model="model.idOrPassword" placeholder="uuid or password" />
      <label>Method (ss)</label><input v-model="model.method" placeholder="aes-256-gcm" />
      <label>Network</label>
      <PickSelect v-model="model.network" icon="wifi" title="Network" aria-label="Network" quiet :options="networkOptions" />
      <label>TLS</label><input type="checkbox" v-model="model.tls" />
      <label>SNI</label><input v-model="model.serverName" />
      <label>Fingerprint</label><input v-model="model.fingerprint" placeholder="chrome" />
      <label>TLS fingerprint</label>
      <PickSelect v-model="model.tlsFingerprint" icon="tune" title="TLS fingerprint" aria-label="TLS fingerprint" quiet :options="tlsFingerprintOptions" />
      <label>Fragment mode</label>
      <PickSelect v-model="model.tlsFragment" icon="code" title="Fragment mode" aria-label="Fragment mode" quiet :options="tlsFragmentOptions" />
      <label>Fragment length</label><input v-model="model.tlsFragmentLen" placeholder="10-20" />
      <label>Fragment interval (ms)</label><input v-model.number="model.tlsFragmentInt" type="number" min="0" />
      <label>Mux</label><input type="checkbox" v-model="model.muxEnabled" />
      <label>Mux protocol</label>
      <PickSelect v-model="model.muxProtocol" icon="layers" title="Mux protocol" aria-label="Mux protocol" quiet :options="muxProtocolOptions" />
      <label>Mux concurrency</label><input v-model.number="model.muxConcurrency" type="number" min="0" max="1024" placeholder="0 = core default" />
      <label>REALITY</label><input type="checkbox" v-model="model.realityEnabled" />
      <label>REALITY pubkey</label><input v-model="model.realityPublicKey" />
      <label>REALITY shortId</label><input v-model="model.realityShortId" />
      <label>Flow</label><input v-model="model.flow" placeholder="xtls-rprx-vision" />
      <label>Path / Service</label><input v-model="model.path" />
      <label>Host</label><input v-model="model.host" />
      <label>Raw outbound JSON</label><textarea v-model="model.rawOutbound" rows="3" placeholder='{"type":"vless",...} or xray outbound' />
      <label>Test URL</label><input v-model="model.testUrl" />
    </div>
    <div class="toolbar" style="padding: 12px 0 0">
      <button class="nb-act nb-act--primary" :disabled="saving" @click="save">
        <Icon name="check" :size="14" />{{ saving ? 'Saving…' : 'Save' }}
      </button>
      <button class="nb-act nb-act--ghost" :disabled="saving" @click="$emit('cancel')">
        <Icon name="x" :size="14" />Cancel
      </button>
      <span v-if="err" class="latency-bad">{{ err }}</span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { reactive, ref, watch } from 'vue'
import { useProfilesStore } from '../stores/profiles'
import type { Profile } from '../types'
import AetherFields from './AetherFields.vue'
import Icon from './Icon.vue'
import PickSelect from './PickSelect.vue'

const coreOptions = [
  { value: '0', label: 'sing-box (nekobox_core)' },
  { value: '1', label: 'xray' },
  { value: '2', label: 'SNI Spoofing' },
]
const protocolOptions = ['vless', 'vmess', 'trojan', 'shadowsocks', 'socks', 'http', 'wireguard', 'hysteria2', 'tuic', 'aether']
  .map((v) => ({ value: v, label: v }))
// Networks both renderers can carry: kcp/h2 were dropped because sing-box
// rejects kcp and Xray removed the HTTP transport, and neither profile
// field set expresses their parameters (rawOutbound still can).
const networkOptions = ['tcp', 'ws', 'grpc', 'xhttp', 'httpupgrade', 'quic'].map((v) => ({ value: v, label: v }))
const tlsFingerprintOptions = [
  { value: '', label: 'none (use Fingerprint)' },
  ...['chrome', 'firefox', 'safari', 'ios', 'android', 'edge', '360', 'qq', 'random', 'randomized']
    .map((v) => ({ value: v, label: v })),
]
const tlsFragmentOptions = [
  { value: '', label: 'off' },
  ...['tlshello', '1-1', '1-2', '1-3', '1-5'].map((v) => ({ value: v, label: v })),
]
const muxProtocolOptions = [
  { value: '', label: 'h2mux (default)' },
  ...['h2mux', 'smux', 'yamux'].map((v) => ({ value: v, label: v })),
]

const props = defineProps<{ profile: Profile | null }>()
const emit = defineEmits(['saved', 'cancel'])
const profiles = useProfilesStore()
const err = ref('')

const blank: Profile = { id: '', name: '', core: 1, protocol: 'vless', address: '', port: 443, network: 'tcp' }
// fullBlank extends blank with every optional key the form can touch,
// set to its empty/zero value. The watcher overlays the incoming
// profile ON TOP of this (see applyProfile), so keys absent from the
// new profile are actively cleared instead of leaking from the
// previously edited profile (frontend BUG-002: edit-then-add kept
// e.g. method/flow/realityPublicKey on the wrong profile).
const fullBlank: Profile = {
  ...blank,
  idOrPassword: '',
  method: '',
  alterId: 0,
  security: '',
  tls: false,
  serverName: '',
  fingerprint: '',
  realityEnabled: false,
  realityPublicKey: '',
  realityShortId: '',
  realitySpiderX: '',
  flow: '',
  path: '',
  host: '',
  alpn: '',
  allowInsecure: false,
  tlsFingerprint: '',
  tlsFragment: '',
  tlsFragmentLen: '',
  tlsFragmentInt: 0,
  muxEnabled: false,
  muxProtocol: '',
  muxConcurrency: 0,
  rawOutbound: '',
  testUrl: '',
  aether: {
    protocol: '', transport: '', scanMode: '', obfuscation: '', ipVersion: '',
    masqueSni: '', fingerprint: '', dns: '', exitLoc: '', wiwOuter: '', wiwInner: '',
    fragment: false, fragmentSize: '', fragmentDelay: '', ech: false, echDns: '',
    echDomain: '', command: '', psiphon: '', psiphonMode: '', psiphonCdnIps: '',
    psiphonCdnSni: '', psiphonCdnSets: '', psiphonRegion: '', psiphonBundledList: undefined,
    tor: '', torBridges: '', torBridgeLines: '', torRelays: '',
  },
  group: '',
  lastConnectedAt: '',
  lastLatency: 0,
  lastTestedAt: '',
  countryCode: '',
  countryName: '',
  countryAt: '',
}
const model = reactive<Profile>({ ...blank })

// BUG-007: busy guard — without it a double-click fires two concurrent
// saves and a new profile gets two fresh ids (duplicates).
const saving = ref(false)

function applyProfile(p: Profile | null) {
  // Reset every field to the full defaults first, then overlay the
  // incoming profile. Keys absent from p stay at their empty value.
  // aether is a nested object, so it is spread too: sharing it made the
  // form alias fullBlank.aether (every new profile inherited the last
  // edits) or the live store row (Cancel kept the edits).
  Object.assign(model, { ...fullBlank, aether: { ...fullBlank.aether } })
  if (p) {
    Object.assign(model, p, { aether: { ...fullBlank.aether, ...(p.aether ?? {}) } })
  }
}

watch(() => props.profile, (p) => applyProfile(p), { immediate: true })

// Aether rides the sing-box core (the engine hosts its listener); keep
// the core selection consistent when a profile switches protocol in and
// out of aether, and seed a default aether settings object.
watch(() => model.protocol, (proto, old) => {
  if (!model.aether) model.aether = { ...fullBlank.aether }
  if (proto === 'aether' && (!model.aether.protocol || old === 'aether')) {
    if (!model.aether.protocol) model.aether.protocol = 'masque'
  }
  if (proto === 'aether' && old !== undefined && old !== 'aether' && model.core !== 0) {
    model.core = 0
  }
})

async function save() {
  if (saving.value) return
  // BUG-007: fail fast on the save path like Edit-as-JSON does, instead
  // of persisting port 0/99999 that only dies slowly at Connect. Raw
  // passthrough profiles bypass the check (backend-legal: port unused).
  // Aether profiles with a hand-written command that names their own
  // listener also bypass it (the engine port stands, profile port unused).
  if (!model.rawOutbound && !(model.protocol === 'aether' && model.aether?.command?.trim()
    && /--(?:bind|socks-listen|listen|listener)\b/.test(model.aether.command))) {
    const port = Number(model.port)
    if (!Number.isInteger(port) || port < 1 || port > 65535) {
      err.value = `Port must be 1..65535 (got ${model.port})`
      return
    }
  }
  saving.value = true
  err.value = ''
  try {
    await profiles.save({ ...model })
    emit('saved')
  } catch (e) {
    err.value = String(e)
  } finally {
    saving.value = false
  }
}
</script>
