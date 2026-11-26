<!-- Aether engine fields, in PattN's order and behind its gates
     (AddServerAetherViewModel.Refresh): a WARP-only row disappears when
     Psiphon or Tor runs alone, the MASQUE rows only where the protocol
     rides MASQUE, fragment only over HTTP/2, and obfuscation only where
     the core still shapes packets. -->
<template>
  <template v-if="model.aether">
    <template v-if="warpUsed">
      <label>Aether protocol</label>
      <PickSelect v-model="model.aether.protocol" icon="globe" title="Aether protocol" aria-label="Aether protocol" quiet :options="protocolOptions" />
    </template>
    <template v-if="showTransport">
      <label>Transport</label>
      <PickSelect v-model="model.aether.transport" icon="bolt" title="Transport" aria-label="Transport" quiet :options="transportOptions" />
      <label>MASQUE SNI</label><input v-model="model.aether.masqueSni" placeholder="www.cloudflare.com" />
      <p v-if="model.aether.masqueSni?.trim()" class="muted aether-span">
        Saved with the profile, but aether 2.3.0 has no MASQUE SNI flag, so this one is not sent.
      </p>
    </template>
    <template v-if="showFragment">
      <label>Fragment ClientHello</label><input type="checkbox" v-model="model.aether.fragment" />
      <template v-if="model.aether.fragment">
        <label>Fragment size</label><input v-model="model.aether.fragmentSize" placeholder="8-16" />
        <label>Fragment delay (ms)</label><input v-model="model.aether.fragmentDelay" placeholder="2-10" />
      </template>
    </template>
    <template v-if="showEch">
      <label>ECH</label><input type="checkbox" v-model="model.aether.ech" />
      <p class="muted aether-span">
        Hides the server name of the MASQUE handshake; without a usable key Aether does not connect.
      </p>
      <template v-if="model.aether.ech">
        <label>ECH DNS resolver</label>
        <input v-model="model.aether.echDns" list="aether-ech-dns" placeholder="empty = use DNS resolvers" />
        <label>ECH domain tree</label>
        <input v-model="model.aether.echDomain" list="aether-ech-domain" placeholder="cloudflare-ech.com" />
      </template>
    </template>
    <template v-if="warpUsed">
      <label>Scan mode</label>
      <PickSelect v-model="model.aether.scanMode" icon="search" title="Scan mode" aria-label="Scan mode" quiet :options="scanModeOptions" />
    </template>
    <template v-if="showObfuscation">
      <label>Obfuscation</label>
      <PickSelect v-model="model.aether.obfuscation" icon="shield" title="Obfuscation" aria-label="Obfuscation" quiet :options="obfuscationOptions" />
    </template>
    <template v-if="showTransport">
      <label>Fingerprint</label>
      <PickSelect v-model="model.aether.fingerprint" icon="tune" title="Fingerprint" aria-label="Fingerprint" quiet :options="fingerprintOptions" />
    </template>

    <label>Psiphon</label>
    <PickSelect v-model="model.aether.psiphon" icon="cloud" title="Psiphon" aria-label="Psiphon" quiet :options="psiphonOptions" />
    <template v-if="psiphonOn">
      <label>Psiphon connection</label>
      <PickSelect v-model="model.aether.psiphonMode" icon="globe" title="Psiphon connection" aria-label="Psiphon connection" quiet :options="psiphonModeOptions" />
      <template v-if="psiphonCdnShown">
        <label>CDN fronting IPs</label>
        <input v-model="model.aether.psiphonCdnIps" placeholder="comma-separated; blank = built-in list" />
        <template v-if="psiphonCdnSniShown">
          <label>CDN server names</label>
          <input v-model="model.aether.psiphonCdnSni" placeholder="comma-separated; blank = built-in list" />
        </template>
        <label>CDN edge lists</label>
        <details class="aether-fold" :open="cdnOpen" @toggle="cdnOpen = ($event.target as HTMLDetailsElement).open">
          <summary>{{ cdnSummary }}</summary>
          <p class="muted">
            Which of the edge lists built into Psiphon the fronting scan tries. None checked means all of
            them; beside IPs of your own they are tried after those.
          </p>
          <label v-for="s in cdnSetOptions" :key="s.value" class="aether-check">
            <input
              type="checkbox"
              :checked="cdnChosen.includes(s.value)"
              @change="toggleCdnSet(s.value, ($event.target as HTMLInputElement).checked)"
            />
            <span>{{ s.label }}</span>
          </label>
        </details>
      </template>
      <label>Exit country</label><input v-model="model.aether.psiphonRegion" placeholder="US" />
      <label>Bundled server list</label><input type="checkbox" v-model="psiphonBundled" />
      <p class="muted aether-span">
        On: NekoGo hands Psiphon the server list it ships, so the first connection needs no list download.
        Off: Psiphon fetches that list itself first, which a filtered network can block.
      </p>
    </template>

    <label>Tor</label>
    <PickSelect v-model="model.aether.tor" icon="shield" title="Tor" aria-label="Tor" quiet :options="torOptions" />
    <template v-if="torOn">
      <label>Bridges</label>
      <PickSelect v-model="model.aether.torBridges" icon="list" title="Bridges" aria-label="Bridges" quiet :options="torBridgesOptions" />
      <template v-if="torRelaysShown">
        <label>Bridge sources</label>
        <PickSelect v-model="model.aether.torRelays" icon="layers" title="Bridge sources" aria-label="Bridge sources" quiet :options="torRelaysOptions" />
      </template>
      <template v-if="torBridgeLinesShown">
        <label>Bridge lines</label>
        <textarea
          v-model="model.aether.torBridgeLines"
          rows="4"
          spellcheck="false"
          placeholder="one per line, as in torrc: obfs4 203.0.113.5:443 FINGERPRINT cert=… iat-mode=0"
        />
      </template>
    </template>

    <template v-if="showHops">
      <label>Outer endpoint</label><input v-model="model.aether.wiwOuter" placeholder="host:port (blank = scan)" />
      <label>Inner endpoint</label>
      <input
        v-model="model.aether.wiwInner"
        :placeholder="model.aether.protocol === 'wg-over-masque' ? 'blank = the endpoint WARP assigns' : 'host:port'"
      />
    </template>
    <template v-if="showEndpoint">
      <label>Address</label><input v-model="model.address" placeholder="host (blank = scan)" />
      <label>Port</label><input v-model.number="model.port" type="number" min="1" max="65535" />
    </template>
    <template v-if="warpUsed">
      <label>IP version</label>
      <PickSelect v-model="model.aether.ipVersion" icon="layers" title="IP version" aria-label="IP version" quiet :options="ipVersionOptions" />
    </template>

    <details
      v-if="warpUsed"
      class="aether-fold aether-span"
      :open="otherOpen"
      @toggle="otherOpen = ($event.target as HTMLDetailsElement).open"
    >
      <summary>Other settings{{ hasOtherSettings ? ' (set)' : '' }}</summary>
      <div class="form-grid aether-nested">
        <label>DNS resolvers</label><input v-model="model.aether.dns" placeholder="comma-separated; blank = 1.1.1.1,1.0.0.1" />
        <label>Exit country rule</label><input v-model="model.aether.exitLoc" placeholder="DE,SE allows only those; !IR,RU refuses those" />
      </div>
    </details>

    <label>Aether command</label>
    <div class="aether-cmd">
      <textarea v-model="commandText" rows="4" spellcheck="false" />
      <template v-if="commandIsCustom">
        <p class="muted">Edited by hand: this line runs as written and the settings above are not applied.</p>
        <button class="nb-act nb-act--ghost" @click="useSettings">
          <Icon name="refresh" :size="14" />Use the settings
        </button>
      </template>
      <p v-else class="muted">
        The line these settings build, on the placeholder port a command with no listener of its own gets.
        Edit it to run your own.
      </p>
    </div>

    <datalist id="aether-ech-dns">
      <option v-for="v in echDnsPresets" :key="v" :value="v" />
    </datalist>
    <datalist id="aether-ech-domain">
      <option v-for="v in echDomainPresets" :key="v" :value="v" />
    </datalist>
  </template>
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { backend, backendAvailable } from '../api'
import { aetherTwoHops, aetherWarpUsed } from '../aether'
import Icon from './Icon.vue'
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
  { value: 'chrome-120', label: 'chrome 120 (no post-quantum share)' },
  { value: 'firefox', label: 'firefox' },
  { value: 'semi-python', label: 'semi-python' },
  { value: 'go', label: 'go' },
]
const psiphonOptions = [
  { value: 'off', label: 'off (default)' },
  { value: 'chain', label: 'WARP → Psiphon' },
  { value: 'reverse', label: 'Psiphon → WARP' },
  { value: 'only', label: 'Psiphon only' },
]
const psiphonModeOptions = [
  { value: 'auto', label: 'automatic (default)' },
  { value: 'cdn', label: 'CDN fronting only' },
  { value: 'direct', label: 'direct only' },
]
const torOptions = [
  { value: 'off', label: 'off (default)' },
  { value: 'chain', label: 'WARP → Tor' },
  { value: 'reverse', label: 'Tor → WARP' },
  { value: 'only', label: 'Tor only' },
]
const torBridgesOptions = [
  { value: 'auto', label: 'when Tor is blocked (default)' },
  { value: 'first', label: 'from the start' },
  { value: 'never', label: 'never' },
  { value: 'own', label: 'my own bridge lines' },
]
const torRelaysOptions = [
  { value: 'auto', label: 'bridgedb and public relays (default)' },
  { value: 'only', label: 'public relays only' },
  { value: 'off', label: 'bridgedb only' },
]
// The edge lists aether carries, in the order its scan tries them
// (gui/aether.go aetherCdnSets).
const cdnSetOptions = [
  { value: 'cloudflare', label: 'Cloudflare' },
  { value: 'fastly', label: 'Fastly' },
  { value: 'cloudfront', label: 'CloudFront' },
  { value: 'psiphon-akamai', label: 'Akamai (Psiphon list)' },
  { value: 'psiphon-bunny', label: 'Bunny (Psiphon list)' },
  { value: 'vercel', label: 'Vercel' },
  { value: 'github', label: 'GitHub' },
  { value: 'curated-fronting', label: 'Curated fronting edges' },
  { value: 'legacy-android-overrides', label: 'Legacy Android overrides' },
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
  'consumer-masque.cloudflareclient.com',
  'consumer-masque-proxy.cloudflareclient.com',
]

const protocol = computed(() => props.model.aether?.protocol || 'wg')
const transport = computed(() => props.model.aether?.transport || 'h3')
const psiphon = computed(() => props.model.aether?.psiphon || 'off')
const tor = computed(() => props.model.aether?.tor || 'off')
const bridges = computed(() => props.model.aether?.torBridges || 'auto')

const warpUsed = computed(() => aetherWarpUsed(props.model.aether))
const overMasque = computed(() => ['masque', 'mim', 'wg-over-masque'].includes(protocol.value))
const twoHops = computed(() => aetherTwoHops(protocol.value))
// MASQUE over HTTP/2 takes no obfuscation at all: it shapes the UDP of
// WireGuard and HTTP/3 alone, and a reverse chain rides TCP too.
const masqueOverHttp2 = computed(() =>
  overMasque.value && (transport.value === 'h2' || tor.value === 'reverse' || psiphon.value === 'reverse'),
)
const showTransport = computed(() => warpUsed.value && overMasque.value)
const showFragment = computed(() => warpUsed.value && overMasque.value && transport.value === 'h2')
const showEch = computed(() => warpUsed.value && overMasque.value)
const showObfuscation = computed(() => warpUsed.value && !masqueOverHttp2.value)
const showHops = computed(() => warpUsed.value && twoHops.value)
const showEndpoint = computed(() => warpUsed.value && !twoHops.value)
const psiphonOn = computed(() => psiphon.value !== 'off')
const psiphonCdnShown = computed(() => psiphonOn.value && (props.model.aether?.psiphonMode || 'auto') !== 'direct')
const psiphonCdnSniShown = computed(() => psiphonCdnShown.value && !!props.model.aether?.psiphonCdnIps?.trim())
const torOn = computed(() => tor.value !== 'off')
const torRelaysShown = computed(() => torOn.value && (bridges.value === 'auto' || bridges.value === 'first'))
const torBridgeLinesShown = computed(() => torOn.value && bridges.value === 'own')

// Psiphon server-list source: the tri-state *bool renders as one
// checkbox, and "bundled" is what an unset value already means.
const psiphonBundled = computed<boolean>({
  get: () => props.model.aether?.psiphonBundledList !== false,
  set: (v) => {
    if (props.model.aether) props.model.aether.psiphonBundledList = v
  },
})

function parseCdnSets(text: string): string[] {
  const named = new Set(text.split(/[,\s]+/).filter(Boolean))
  return cdnSetOptions.map((s) => s.value).filter((v) => named.has(v))
}

const cdnChosen = computed(() => parseCdnSets(props.model.aether?.psiphonCdnSets ?? ''))
const cdnSummary = computed(() =>
  cdnChosen.value.length === 0
    ? 'All lists built into Psiphon'
    : cdnSetOptions.filter((s) => cdnChosen.value.includes(s.value)).map((s) => s.label).join(', '),
)

function toggleCdnSet(value: string, on: boolean) {
  const a = props.model.aether
  if (!a) return
  const chosen = new Set(cdnChosen.value)
  if (on) chosen.add(value)
  else chosen.delete(value)
  a.psiphonCdnSets = cdnSetOptions.map((s) => s.value).filter((v) => chosen.has(v)).join(',')
}

const hasOtherSettings = computed(() =>
  !!props.model.aether?.dns?.trim() || !!props.model.aether?.exitLoc?.trim(),
)
const otherOpen = ref(hasOtherSettings.value)
const cdnOpen = ref(cdnChosen.value.length > 0)
// Both folds open by themselves for a profile that already holds a value
// in them, so switching profiles never hides a setting that is set.
watch(() => props.model.id, () => {
  otherOpen.value = hasOtherSettings.value
  cdnOpen.value = cdnChosen.value.length > 0
})

const built = ref('')
const commandText = computed<string>({
  get: () => (props.model.aether?.command?.trim() ? props.model.aether.command : built.value),
  set: (v) => {
    const a = props.model.aether
    if (!a) return
    // The box hands back what it was given, and clearing it while it
    // shows the built line is no edit either: neither may turn the
    // rendered command into a hand-written one.
    if (v === commandText.value) return
    if (!v.trim() && !a.command?.trim()) return
    a.command = v
  },
})
const commandIsCustom = computed(() => {
  const c = props.model.aether?.command?.trim() ?? ''
  return c !== '' && c !== built.value.trim()
})

function useSettings() {
  if (props.model.aether) props.model.aether.command = ''
}

let previewTimer: ReturnType<typeof setTimeout> | undefined

async function refreshPreview() {
  if (!backendAvailable.value) {
    built.value = ''
    return
  }
  try {
    built.value = await backend.aetherCommandPreview({ ...props.model, aether: { ...props.model.aether } })
  } catch {
    built.value = ''
  }
}

watch(
  () => [props.model.aether, props.model.address, props.model.port, backendAvailable.value],
  () => {
    if (previewTimer) clearTimeout(previewTimer)
    previewTimer = setTimeout(() => {
      previewTimer = undefined
      void refreshPreview()
    }, 250)
  },
  { deep: true, immediate: true },
)

onUnmounted(() => {
  if (previewTimer) clearTimeout(previewTimer)
})
</script>

<style scoped>
/* Rows that are not one label + one control span the whole form grid. */
.aether-span { grid-column: 1 / -1; margin: 0; }
.aether-fold { min-width: 0; }
.aether-fold > summary {
  cursor: pointer; font-size: 13px; font-weight: 600; color: var(--accent);
  padding: 6px 0;
}
.aether-fold > p { margin: 4px 0 8px; }
.aether-nested { max-width: none; padding-bottom: 6px; }
.aether-check {
  display: flex; align-items: center; gap: 8px;
  font-size: 13px; font-weight: 400; padding: 3px 0; cursor: pointer;
}
.aether-cmd { min-width: 0; }
.aether-cmd > p { margin: 6px 0; }
textarea {
  width: 100%; box-sizing: border-box; resize: vertical;
  font-family: Consolas, 'Cascadia Code', monospace; font-size: 12px;
  border-radius: 10px; padding: 8px 10px;
}
</style>
