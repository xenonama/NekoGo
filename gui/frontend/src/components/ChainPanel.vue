<!-- Advanced-only Chain tab: one place for every layer that sits in
     front of the tunnel plus the transport that feeds it. Both exit
     chains (Psiphon and Tor) run on both the SNI-spoof and Aether legs;
     the Psiphon choice here is the same persisted Settings flag the SNI
     Spoofing and Aether tabs edit, so the two stay in sync. -->
<template>
  <div class="nb-card nb-page">
    <h3>Chain</h3>
    <p class="muted">
      Layer an exit hop in front of NekoGo's tunnel. Psiphon and Tor both leave through their own
      network while every connection they make rides your tunnel. Changes apply on the next
      <strong>Connect</strong>.
    </p>
    <div v-if="connection.status.running" class="nb-warn-banner">
      Connected — chain and transport changes apply after you reconnect.
    </div>
    <div v-if="err" class="nb-json-error" style="padding: 4px 0 8px">{{ err }}</div>
  </div>

  <!-- Exit-chain hub: one picker per tunnel. A leg carries at most one
       chain — it publishes one listener, the config routes into one
       outbound, and one child process is what readiness is probed on —
       so the choice is a single value, not two switches. -->
  <div class="nb-card nb-page">
    <h3>Exit chain</h3>
    <div class="stg-row">
      <span class="stg-chip"><Icon name="shield" :size="18" /></span>
      <span class="stg-meta">
        <span class="stg-title">After SNI spoofing</span>
        <span class="stg-sub">
          <span class="mono">TUN → chain → SNI-spoofed tunnel → your VPN server</span>. Psiphon
          here is the same switch the SNI Spoofing tab edits.
        </span>
      </span>
      <span class="stg-ctl">
        <PickSelect
          v-model="spoofLeg"
          icon="shield"
          title="SNI spoofing chain"
          aria-label="Exit chain after SNI spoofing"
          quiet
          :options="driverOptions"
        />
      </span>
    </div>
    <div class="stg-row">
      <span class="stg-chip"><Icon name="cloud" :size="18" /></span>
      <span class="stg-meta">
        <span class="stg-title">After the Aether tunnel</span>
        <span class="stg-sub">
          <span class="mono">Aether tunnel → chain → internet</span>. Uses the Aether core's own
          chain mode, so the hop's servers are dialled through the tunnel. Psiphon here is the same
          switch the Aether tab edits.
        </span>
      </span>
      <span class="stg-ctl">
        <PickSelect
          v-model="aetherLeg"
          icon="cloud"
          title="Aether chain"
          aria-label="Exit chain after the Aether tunnel"
          quiet
          :options="driverOptions"
        />
      </span>
    </div>
    <p class="muted">
      The two legs are independent: the SNI one runs the spoof engine as its transport, the Aether
      one rides the WARP tunnel. A chain always runs in TUN mode, so NekoGo leaves the OS proxy off
      for those connects, and Connect waits until the chain carries real traffic before the
      transport starts. Tor gets a longer wait than Psiphon: a cold start fetches a directory
      consensus before its first byte moves.
    </p>
  </div>

  <!-- Chain-level Psiphon options: what both legs above hand aether.exe.
       Same flags the Aether profile editor writes for one tunnel, here
       for the chain. -->
  <div class="nb-card nb-page">
    <h3>Psiphon options</h3>
    <p class="muted">
      Applies to both Psiphon legs above, on every connect. A profile that sets its own Psiphon
      options in the Aether editor keeps them — these are the chain's, not the tunnel's.
    </p>
    <div class="stg-row">
      <span class="stg-chip"><Icon name="globe" :size="18" /></span>
      <span class="stg-meta">
        <span class="stg-title">Egress country</span>
        <span class="stg-sub">
          Ask Psiphon to leave through this country. Psiphon only offers a handful at a time; if the
          one you pick is unavailable it falls back to its own choice.
        </span>
      </span>
      <span class="stg-ctl">
        <PickSelect
          v-model="regionChoice"
          icon="globe"
          title="Egress country"
          aria-label="Psiphon egress country"
          quiet
          :options="regionOptions"
        />
        <input
          v-if="regionChoice === 'custom'"
          class="stg-text"
          type="text"
          maxlength="2"
          aria-label="Custom egress country code"
          placeholder="DE"
          :value="regionText"
          @change="setOtherRegion(($event.target as HTMLInputElement).value)"
        />
      </span>
    </div>
    <div class="stg-row">
      <span class="stg-chip"><Icon name="cloud" :size="18" /></span>
      <span class="stg-meta">
        <span class="stg-title">Fronting mode</span>
        <span class="stg-sub">
          <span class="mono">auto</span> lets Psiphon choose, <span class="mono">cdn</span> fronts
          through CDN edges, <span class="mono">direct</span> dials servers without fronting (and
          ignores the three fields below).
        </span>
      </span>
      <span class="stg-ctl">
        <PickSelect
          v-model="modeChoice"
          icon="layers"
          title="Psiphon mode"
          aria-label="Psiphon fronting mode"
          quiet
          :options="modeOptions"
        />
      </span>
    </div>
    <template v-if="modeChoice === 'cdn'">
      <div class="stg-row">
        <span class="stg-chip"><Icon name="tune" :size="18" /></span>
        <span class="stg-meta">
          <span class="stg-title">Own CDN IPs</span>
          <span class="stg-sub">Comma- or space-separated edge addresses to front through</span>
        </span>
        <span class="stg-ctl">
          <input
            class="stg-text"
            type="text"
            aria-label="Psiphon CDN IPs"
            placeholder="1.1.1.1, 8.8.8.8"
            :value="s.chainPsiphonCdnIps ?? ''"
            @input="setChainLive('chainPsiphonCdnIps', ($event.target as HTMLInputElement).value)"
            @change="flushChainStr('chainPsiphonCdnIps', ($event.target as HTMLInputElement).value)"
          />
        </span>
      </div>
      <div class="stg-row">
        <span class="stg-chip"><Icon name="shield" :size="18" /></span>
        <span class="stg-meta">
          <span class="stg-title">CDN SNI names</span>
          <span class="stg-sub">Host names used beside your own CDN IPs (ignored without them)</span>
        </span>
        <span class="stg-ctl">
          <input
            class="stg-text"
            type="text"
            aria-label="Psiphon CDN SNI"
            placeholder="www.cloudflare.com"
            :value="s.chainPsiphonCdnSni ?? ''"
            @input="setChainLive('chainPsiphonCdnSni', ($event.target as HTMLInputElement).value)"
            @change="flushChainStr('chainPsiphonCdnSni', ($event.target as HTMLInputElement).value)"
          />
        </span>
      </div>
      <div class="stg-row">
        <span class="stg-chip"><Icon name="layers" :size="18" /></span>
        <span class="stg-meta">
          <span class="stg-title">CDN edge sets</span>
          <span class="stg-sub">
            Built-in lists to try: <span class="mono">cloudflare, fastly, cloudfront,
            psiphon-akamai, psiphon-bunny, vercel, github, curated-fronting</span>
          </span>
        </span>
        <span class="stg-ctl">
          <input
            class="stg-text"
            type="text"
            aria-label="Psiphon CDN edge sets"
            placeholder="cloudflare,fastly"
            :value="s.chainPsiphonCdnSets ?? ''"
            @input="setChainLive('chainPsiphonCdnSets', ($event.target as HTMLInputElement).value)"
            @change="flushChainStr('chainPsiphonCdnSets', ($event.target as HTMLInputElement).value)"
          />
        </span>
      </div>
    </template>
    <p class="muted">
      Psiphon fetches its own server list every connect, so there is no bundled-versus-fresh switch
      to offer: aether takes a path to a list file and NekoGo ships none. The aether log names the
      countries on offer (<span class="mono">AvailableEgressRegions</span>) when a choice comes back
      empty.
    </p>
  </div>

  <!-- Chain-level Tor options: the same three flags the Aether profile
       editor writes for one tunnel, here for both chain legs. No egress
       country: aether builds a circuit from whatever relays the directory
       offers, so the exit is not a picker's value like Psiphon's. -->
  <div class="nb-card nb-page">
    <h3>Tor options</h3>
    <p class="muted">
      Applies to both legs above when one runs Tor, on every connect. A profile that sets its own
      Tor options in the Aether editor keeps them — these are the chain's, not the tunnel's.
    </p>
    <div class="stg-row">
      <span class="stg-chip"><Icon name="list" :size="18" /></span>
      <span class="stg-meta">
        <span class="stg-title">Bridges</span>
        <span class="stg-sub">
          <span class="mono">auto</span> uses them only when the network looks like it blocks Tor,
          <span class="mono">first</span> always starts over one, <span class="mono">never</span>
          dials a plain relay, <span class="mono">own</span> takes the lines below.
        </span>
      </span>
      <span class="stg-ctl">
        <PickSelect
          v-model="bridgesChoice"
          icon="list"
          title="Tor bridges"
          aria-label="Tor bridge use"
          quiet
          :options="bridgesOptions"
        />
      </span>
    </div>
    <!-- Bridge lines do not fit the 140px control column, so they get
         their own full-width block under the picker. -->
    <div v-if="bridgesChoice === 'own'" class="stg-area-wrap">
      <label class="stg-title" for="chain-tor-bridge-lines">Bridge lines</label>
      <textarea
        id="chain-tor-bridge-lines"
        class="stg-area"
        rows="4"
        spellcheck="false"
        placeholder="obfs4 1.2.3.4:443 AAAAAAAAAAAAAAAAAAAA= cert=… iat-mode=0"
        :value="s.chainTorBridgeLines ?? ''"
        @input="setChainLive('chainTorBridgeLines', ($event.target as HTMLTextAreaElement).value)"
        @change="flushChainStr('chainTorBridgeLines', ($event.target as HTMLTextAreaElement).value)"
      />
      <p class="muted">
        One bridge per line, exactly as the Tor Project hands them out (<span class="mono">obfs4
        …</span>, <span class="mono">snowflake …</span>). Blank lines and <span class="mono">#</span>
        comments are ignored, and fetching stops: <span class="mono">own</span> means these lines.
      </p>
    </div>
    <!-- Any other shape keeps the lines and does not use them; the count
         is what tells a paste that it is waiting, not gone. -->
    <p v-if="bridgesChoice !== 'own' && savedBridges" class="muted">
      {{ savedBridges }} bridge {{ savedBridges === 1 ? 'line' : 'lines' }} saved, and
      <span class="mono">{{ bridgesChoice }}</span> does not hand {{ savedBridges === 1 ? 'it' : 'them' }}
      to the engine. <span class="mono">own</span> does.
    </p>
    <div class="stg-row">
      <span class="stg-chip"><Icon name="layers" :size="18" /></span>
      <span class="stg-meta">
        <span class="stg-title">Directory relays</span>
        <span class="stg-sub">
          <span class="mono">auto</span> mixes guard/middle/exit with fetched relays,
          <span class="mono">only</span> uses the fetched pool, <span class="mono">off</span> keeps
          them out. <span class="mono">only</span> is what a bridge makes necessary — a direct relay
          list is readable to the network you are hiding from.
        </span>
      </span>
      <span class="stg-ctl">
        <PickSelect
          v-model="relaysChoice"
          icon="layers"
          title="Tor relays"
          aria-label="Tor directory relays"
          quiet
          :options="relaysOptions"
        />
      </span>
    </div>
    <p class="muted">
      Tor chooses its own exit; there is no country to pick. When a connect comes back empty, the
      aether log says whether the directory fetch or the circuit build was the stage that stalled.
    </p>
  </div>

  <!-- Reconnect: what the button under Connect does about the exit chain,
       and whether it shows at all. The backend reads the mode when the
       button is pressed, so it applies from the next press. -->
  <div class="nb-card nb-page">
    <h3>Reconnect</h3>
    <div class="stg-row">
      <span class="stg-chip"><Icon name="refresh" :size="18" /></span>
      <span class="stg-meta">
        <span class="stg-title">Reconnect button</span>
        <span class="stg-sub">
          Whether the button is shown and what it does with the exit chain that was already up.
        </span>
      </span>
      <span class="stg-ctl">
        <PickSelect
          v-model="reconnectChoice"
          icon="refresh"
          title="Reconnect mode"
          aria-label="Reconnect mode"
          quiet
          :options="reconnectOptions"
        />
      </span>
    </div>
    <p class="muted">
      Reconnect always restarts the tunnel on the profile it was running.
      <span class="mono">restart</span> starts every leg again, the exit chain included — a cold
      Psiphon handshake or Tor circuit build, minutes on a bad network.
      <span class="mono">sticky</span> keeps the SNI-spoof chain's Psiphon or Tor child alive and
      hands it to the next connect on the same ports it already used, so only the spoof engine and
      its transport are remade; it also keeps the button below Connect whether or not a tunnel is
      up. The Aether tab's chain is never kept — it runs inside the aether engine, so stopping the
      engine stops it.
      <span class="mono">off</span> hides the button entirely.
    </p>
  </div>

  <!-- Transport: how system traffic reaches the tunnel outside a chain. -->
  <div class="nb-card nb-page">
    <h3>Transport mode</h3>
    <div class="stg-row">
      <span class="stg-chip"><Icon name="monitor" :size="18" /></span>
      <span class="stg-meta">
        <span class="stg-title">TUN mode</span>
        <span class="stg-sub">
          On: every app's traffic is captured by a virtual adapter (needs administrator rights).
          Off: only apps that honour the OS system proxy are proxied.
        </span>
      </span>
      <span class="stg-ctl">
        <button
          class="stg-toggle"
          :class="{ on: !!s.tunEnabled }"
          role="switch"
          :aria-checked="!!s.tunEnabled"
          aria-label="TUN mode"
          title="TUN mode"
          @click="flip('tunEnabled')"
        >
          <span class="knob" />
        </button>
      </span>
    </div>
    <p class="muted">
      Currently {{ s.tunEnabled ? 'TUN — all system traffic goes through the tunnel' : 'system proxy — mixed port ' + (s.mixedPort || 2080) }}
      on the next Connect. Xray profiles and “Xray for SNI spoofing” never use TUN, a full raw core
      override replaces this rendering, and an exit chain always forces TUN whatever this switch
      says. In TUN mode the traffic panel reads the tunnel adapter's own byte counters, so it tracks
      system traffic directly.
    </p>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from 'vue'
import Icon from './Icon.vue'
import PickSelect from './PickSelect.vue'
import { useSettingsStore } from '../stores/settings'
import { useConnectionStore } from '../stores/connection'

const settings = useSettingsStore()
const connection = useConnectionStore()
const s = computed(() => settings.settings)
const err = ref('')

/** Psiphon's usual egress regions. It is a convenience list, not a
 *  guarantee: Psiphon offers a varying subset and says so in the log,
 *  so "Other" takes any two-letter code the core can read. */
const psiphonRegions = [
  'AU', 'CA', 'CH', 'DE', 'DK', 'ES', 'FI', 'FR', 'GB', 'IE', 'IT',
  'JP', 'NL', 'NO', 'PL', 'PT', 'SE', 'SG', 'TR', 'US',
]
const regionOptions = [
  { value: 'auto', label: 'Any (Psiphon chooses)' },
  ...psiphonRegions.map((cc) => ({ value: cc, label: cc })),
  { value: 'custom', label: 'Other (2-letter code)…' },
]
const modeOptions = [
  { value: 'auto', label: 'auto (default)' },
  { value: 'cdn', label: 'cdn' },
  { value: 'direct', label: 'direct' },
]
/** The three states of one leg: no chain, or one of the two drivers. */
const driverOptions = [
  { value: 'none', label: 'None' },
  { value: 'psiphon', label: 'Psiphon' },
  { value: 'tor', label: 'Tor' },
]
const bridgesOptions = [
  { value: 'auto', label: 'auto (default)' },
  { value: 'first', label: 'first' },
  { value: 'never', label: 'never' },
  { value: 'own', label: 'own' },
]
const relaysOptions = [
  { value: 'auto', label: 'auto (default)' },
  { value: 'only', label: 'only' },
  { value: 'off', label: 'off' },
]
const reconnectOptions = [
  { value: 'restart', label: 'restart everything (default)' },
  { value: 'sticky', label: 'keep the last chain hop' },
  { value: 'off', label: 'no Reconnect button' },
]

/** A leg is stored as two booleans because the two drivers were not
 *  added at once, and the SNI/Aether tabs still switch the Psiphon one
 *  directly. Psiphon wins a file that somehow holds both: that is the
 *  rule the backend applies on save (resolveChainDrivers), so the panel
 *  reads what a connect will actually run. */
type Leg = 'spoof' | 'aether'
const legFields = {
  spoof: ['spoofPsiphon', 'spoofTor'],
  aether: ['aetherPsiphon', 'aetherTor'],
} as const

function legDriver(leg: Leg): string {
  const [psiphon, tor] = legFields[leg]
  if (settings.settings[psiphon]) return 'psiphon'
  if (settings.settings[tor]) return 'tor'
  return 'none'
}

/** Both flags move in one write and one save: a leg that ended up with
 *  two drivers armed would be resolved by the backend, and rolling the
 *  pair back together is what keeps the picker honest when disk refuses. */
async function setLeg(leg: Leg, driver: string) {
  const [psiphon, tor] = legFields[leg]
  const prev = { psiphon: settings.settings[psiphon], tor: settings.settings[tor] }
  settings.settings[psiphon] = driver === 'psiphon'
  settings.settings[tor] = driver === 'tor'
  err.value = ''
  try {
    await settings.save()
  } catch (e) {
    settings.settings[psiphon] = prev.psiphon
    settings.settings[tor] = prev.tor
    err.value = String(e)
  }
}

const spoofLeg = computed<string>({
  get: () => legDriver('spoof'),
  set: (v) => void setLeg('spoof', v),
})
const aetherLeg = computed<string>({
  get: () => legDriver('aether'),
  set: (v) => void setLeg('aether', v),
})

/** The country select and the free-text code are one value: whatever is
 *  stored decides both, so a code outside the list reopens in "Other". */
const regionCustom = ref(false)
const otherRegion = ref('')
const regionChoice = computed<string>({
  get: () => {
    const cc = (s.value.chainPsiphonRegion ?? '').toUpperCase()
    if (!cc) return regionCustom.value ? 'custom' : 'auto'
    return psiphonRegions.includes(cc) ? cc : 'custom'
  },
  set: (v) => {
    if (v === 'custom') {
      regionCustom.value = true
      void setChainStr('chainPsiphonRegion', otherRegion.value.toUpperCase())
      return
    }
    regionCustom.value = false
    otherRegion.value = ''
    void setChainStr('chainPsiphonRegion', v === 'auto' ? '' : v)
  },
})
/** The code shown in the Other box: what was typed this session, else
 *  what disk holds (a hand-edited config can name a country the list
 *  does not carry). */
const regionText = computed(
  () => otherRegion.value || (s.value.chainPsiphonRegion ?? '').toUpperCase(),
)
const modeChoice = computed<string>({
  get: () => s.value.chainPsiphonMode || 'auto',
  set: (v) => void setChainStr('chainPsiphonMode', v),
})
const bridgesChoice = computed<string>({
  get: () => s.value.chainTorBridges || 'auto',
  set: (v) => void setChainStr('chainTorBridges', v),
})
/** Bridge lines the tab holds whatever Bridges says, counted the way the
 *  backend counts what it will pass: blanks and # comments are not
 *  bridges. Only own renders the editor, so this is the one signal a
 *  saved paste is still there and simply not in use. */
const savedBridges = computed(() =>
  (s.value.chainTorBridgeLines ?? '')
    .split('\n')
    .map((line) => line.trim())
    .filter((line) => line !== '' && !line.startsWith('#')).length,
)
const relaysChoice = computed<string>({
  get: () => s.value.chainTorRelays || 'auto',
  set: (v) => void setChainStr('chainTorRelays', v),
})
/** A config that never carried the key means restart — the behaviour the
 *  button shipped with — and the backend canonicalises to the same word
 *  on save, so the panel never claims a mode disk does not hold. */
const reconnectChoice = computed<string>({
  get: () => s.value.reconnectMode || 'restart',
  set: (v) => void setChainStr('reconnectMode', v),
})

function setOtherRegion(text: string) {
  const cc = text.toUpperCase().replace(/[^A-Z]/g, '')
  otherRegion.value = cc
  void setChainStr('chainPsiphonRegion', cc)
}

type ChainStrKey =
  | 'chainPsiphonRegion' | 'chainPsiphonMode' | 'chainPsiphonCdnIps'
  | 'chainPsiphonCdnSni' | 'chainPsiphonCdnSets'
  | 'chainTorBridges' | 'chainTorRelays' | 'chainTorBridgeLines'
  | 'reconnectMode'

/** Same persist path as the toggles: write, save, roll back if the
 *  backend refused (a bad country code is rejected on save), so the
 *  panel never shows a value disk did not keep. */
async function setChainStr(field: ChainStrKey, value: string) {
  const prev = settings.settings[field]
  settings.settings[field] = value
  err.value = ''
  try {
    await settings.save()
  } catch (e) {
    settings.settings[field] = prev
    err.value = String(e)
  }
}

/** Free text in this tab arrives in bulk — a paste of bridge lines, a
 *  list of CDN IPs — and the panel re-renders on the connection poll, so
 *  a `:value` binding alone resets the control to the stored text under
 *  the cursor long before @change (blur) can fire: the paste vanished
 *  and never reached the core. Write the store as the text changes and
 *  let the save trail it, so one paste costs one write instead of one
 *  per key. A refused save is reported, not rolled back under the user. */
let chainTextTimer: number | undefined

function setChainLive(field: ChainStrKey, value: string) {
  settings.settings[field] = value
  err.value = ''
  if (chainTextTimer !== undefined) window.clearTimeout(chainTextTimer)
  chainTextTimer = window.setTimeout(() => void saveChainText(), 600)
}

function flushChainStr(field: ChainStrKey, value: string) {
  settings.settings[field] = value
  if (chainTextTimer !== undefined) {
    window.clearTimeout(chainTextTimer)
    chainTextTimer = undefined
  }
  void saveChainText()
}

async function saveChainText() {
  try {
    await settings.save()
  } catch (e) {
    err.value = String(e)
  }
}

// Leaving the tab with a save still pending must not drop the text.
onBeforeUnmount(() => {
  if (chainTextTimer !== undefined) void saveChainText()
})

/** Flip optimistically, save, and roll the UI back if disk did not keep
 *  the choice. */
async function flip(field: 'tunEnabled') {
  const next = !settings.settings[field]
  settings.settings[field] = next
  err.value = ''
  try {
    await settings.save()
  } catch (e) {
    settings.settings[field] = !next
    err.value = String(e)
  }
}
</script>
