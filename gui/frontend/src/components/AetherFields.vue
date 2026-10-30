<template>
  <template v-if="model.aether">
    <label>Aether protocol</label>
    <PickSelect v-model="model.aether.protocol" icon="globe" title="Aether protocol" aria-label="Aether protocol" quiet :options="protocolOptions" />
    <label>Transport</label>
    <PickSelect v-model="model.aether.transport" icon="bolt" title="Transport" aria-label="Transport" quiet :options="transportOptions" />
    <label>Scan mode</label>
    <PickSelect v-model="model.aether.scanMode" icon="search" title="Scan mode" aria-label="Scan mode" quiet :options="scanModeOptions" />
    <label>Obfuscation</label>
    <PickSelect v-model="model.aether.obfuscation" icon="shield" title="Obfuscation" aria-label="Obfuscation" quiet :options="obfuscationOptions" />
    <label>IP version</label>
    <PickSelect v-model="model.aether.ipVersion" icon="layers" title="IP version" aria-label="IP version" quiet :options="ipVersionOptions" />
    <label>MASQUE SNI</label><input v-model="model.aether.masqueSni" placeholder="empty = core default" />
    <label>Fingerprint</label>
    <PickSelect v-model="model.aether.fingerprint" icon="tune" title="Fingerprint" aria-label="Fingerprint" quiet :options="fingerprintOptions" />
    <label>DNS resolvers</label><input v-model="model.aether.dns" placeholder="1.1.1.1,8.8.8.8" />
    <label>Exit country rule</label><input v-model="model.aether.exitLoc" placeholder="DE,!NL" />
    <template v-if="['gool', 'mim', 'wg-over-masque'].includes(model.aether.protocol || 'wg')">
      <label>Outer endpoint</label><input v-model="model.aether.wiwOuter" placeholder="host:port" />
      <label>Inner endpoint</label>
      <input
        v-model="model.aether.wiwInner"
        :placeholder="model.aether.protocol === 'wg-over-masque' ? 'blank = WARP-assigned WireGuard endpoint' : 'host:port'"
      />
    </template>
    <label>Fragment</label><input type="checkbox" v-model="model.aether.fragment" />
    <template v-if="model.aether.fragment">
      <label>Fragment size</label><input v-model="model.aether.fragmentSize" placeholder="10-20" />
      <label>Fragment delay (ms)</label><input v-model="model.aether.fragmentDelay" placeholder="0-100" />
    </template>
    <label>ECH</label><input type="checkbox" v-model="model.aether.ech" />
    <template v-if="model.aether.ech">
      <label>ECH DNS resolver</label><input v-model="model.aether.echDns" placeholder="empty = use DNS resolvers" />
      <label>ECH domain tree</label><input v-model="model.aether.echDomain" placeholder="example.com" />
    </template>
    <label>Psiphon</label>
    <PickSelect v-model="model.aether.psiphon" icon="cloud" title="Psiphon" aria-label="Psiphon" quiet :options="psiphonOptions" />
    <template v-if="model.aether.psiphon && model.aether.psiphon !== 'off'">
      <label>Psiphon mode</label>
      <PickSelect v-model="model.aether.psiphonMode" icon="globe" title="Psiphon mode" aria-label="Psiphon mode" quiet :options="psiphonModeOptions" />
      <template v-if="model.aether.psiphonMode === 'cdn'">
        <label>Psiphon CDN IPs</label><input v-model="model.aether.psiphonCdnIps" placeholder="comma-separated" />
        <label>Psiphon CDN SNI</label><input v-model="model.aether.psiphonCdnSni" />
        <label>Psiphon CDN set</label><input v-model="model.aether.psiphonCdnSets" placeholder="protocol set override" />
      </template>
      <label>Psiphon region</label><input v-model="model.aether.psiphonRegion" placeholder="US" />
      <label>Psiphon bundled list</label>
      <PickSelect v-model="psiphonBundledSel" icon="download" title="Psiphon bundled list" aria-label="Psiphon bundled list" quiet :options="psiphonBundledOptions" />
    </template>
    <label>Tor</label>
    <PickSelect v-model="model.aether.tor" icon="shield" title="Tor" aria-label="Tor" quiet :options="torOptions" />
    <template v-if="model.aether.tor && model.aether.tor !== 'off'">
      <label>Tor bridges</label>
      <PickSelect v-model="model.aether.torBridges" icon="list" title="Tor bridges" aria-label="Tor bridges" quiet :options="torBridgesOptions" />
      <template v-if="model.aether.torBridges === 'own'">
        <label>Bridge lines</label><textarea v-model="model.aether.torBridgeLines" rows="3" placeholder="one bridge per line" />
      </template>
      <label>Tor relays</label>
      <PickSelect v-model="model.aether.torRelays" icon="layers" title="Tor relays" aria-label="Tor relays" quiet :options="torRelaysOptions" />
    </template>
    <label>Aether command</label><input v-model="model.aether.command" placeholder="hand-written command line (optional)" />
  </template>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import PickSelect from './PickSelect.vue'
import type { Profile } from '../types'

const props = defineProps<{ model: Profile }>()

const protocolOptions = [
  { value: 'masque', label: 'masque' },
  { value: 'wg', label: 'wg (wireguard)' },
  { value: 'gool', label: 'gool' },
  { value: 'mim', label: 'mim' },
  { value: 'wg-over-masque', label: 'wg-over-masque' },
]
const transportOptions = [
  { value: 'h3', label: 'h3 (default)' },
  { value: 'h2', label: 'h2' },
]
const scanModeOptions = [
  { value: 'turbo', label: 'turbo' },
  { value: 'balanced', label: 'balanced (default)' },
  { value: 'thorough', label: 'thorough' },
  { value: 'verified', label: 'verified' },
  { value: 'ironclad', label: 'ironclad' },
]
const obfuscationOptions = ['auto', 'off', 'light', 'firewall', 'balanced', 'gfw', 'aggressive']
  .map((v) => ({ value: v, label: v }))
const ipVersionOptions = [
  { value: 'v4', label: 'v4' },
  { value: 'v6', label: 'v6' },
  { value: 'both', label: 'both' },
]
const fingerprintOptions = [
  { value: 'chrome', label: 'chrome (default)' },
  { value: 'firefox', label: 'firefox' },
  { value: 'semi-python', label: 'semi-python' },
  { value: 'go', label: 'go' },
]
const psiphonOptions = [
  { value: 'off', label: 'off (default)' },
  { value: 'chain', label: 'chain' },
  { value: 'reverse', label: 'reverse' },
  { value: 'only', label: 'only' },
]
const psiphonModeOptions = [
  { value: 'auto', label: 'auto (default)' },
  { value: 'cdn', label: 'cdn' },
  { value: 'direct', label: 'direct' },
]
const psiphonBundledOptions = [
  { value: 'builtin', label: 'bundled list (default)' },
  { value: 'fresh', label: 'fetch fresh' },
]
const torOptions = [
  { value: 'off', label: 'off (default)' },
  { value: 'chain', label: 'chain' },
  { value: 'reverse', label: 'reverse' },
  { value: 'only', label: 'only' },
]
const torBridgesOptions = [
  { value: 'auto', label: 'auto (default)' },
  { value: 'first', label: 'first' },
  { value: 'never', label: 'never' },
  { value: 'own', label: 'own' },
]
const torRelaysOptions = [
  { value: 'auto', label: 'auto (default)' },
  { value: 'only', label: 'only' },
  { value: 'off', label: 'off' },
]

// Psiphon server-list source: the tri-state *bool renders as a
// select (bundled by default, "fetch fresh" sets false explicitly).
const psiphonBundledSel = computed<string>({
  get: () => (props.model.aether?.psiphonBundledList === false ? 'fresh' : 'builtin'),
  set: (v) => {
    if (props.model.aether) props.model.aether.psiphonBundledList = v !== 'fresh'
  },
})
</script>
