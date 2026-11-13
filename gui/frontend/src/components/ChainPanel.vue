<!-- Advanced-only Chain tab: one place for every layer that sits in
     front of the tunnel plus the transport that feeds it. Psiphon is
     the only exit chain today, on both the SNI-spoof and Aether legs;
     the switches here are the same persisted Settings flags the SNI
     Spoofing and Aether tabs edit, so the two stay in sync. -->
<template>
  <div class="nb-card nb-page">
    <h3>Chain</h3>
    <p class="muted">
      Layer an exit hop in front of NekoGo's tunnel. Psiphon is the only chain for now: it leaves
      through its own network while every connection it makes rides your tunnel. Changes apply on
      the next <strong>Connect</strong>.
    </p>
    <div v-if="connection.status.running" class="nb-warn-banner">
      Connected — chain and transport changes apply after you reconnect.
    </div>
    <div v-if="err" class="nb-json-error" style="padding: 4px 0 8px">{{ err }}</div>
  </div>

  <!-- Psiphon hub: one toggle per tunnel it can ride. -->
  <div class="nb-card nb-page">
    <h3>Psiphon</h3>
    <div class="stg-row">
      <span class="stg-chip"><Icon name="shield" :size="18" /></span>
      <span class="stg-meta">
        <span class="stg-title">SNI spoofing → Psiphon</span>
        <span class="stg-sub">
          <span class="mono">TUN → Psiphon → SNI-spoofed tunnel → your VPN server</span>.
          Same switch as the SNI Spoofing tab.
        </span>
      </span>
      <span class="stg-ctl">
        <button
          class="stg-toggle"
          :class="{ on: !!s.spoofPsiphon }"
          role="switch"
          :aria-checked="!!s.spoofPsiphon"
          aria-label="Run Psiphon after SNI spoofing"
          title="Run Psiphon after SNI spoofing"
          @click="flip('spoofPsiphon')"
        >
          <span class="knob" />
        </button>
      </span>
    </div>
    <div class="stg-row">
      <span class="stg-chip"><Icon name="cloud" :size="18" /></span>
      <span class="stg-meta">
        <span class="stg-title">Aether → Psiphon</span>
        <span class="stg-sub">
          <span class="mono">Aether tunnel → Psiphon → internet</span>. Uses the Aether core's own
          chain mode, so Psiphon's servers are dialled through the tunnel. Same switch as the
          Aether tab.
        </span>
      </span>
      <span class="stg-ctl">
        <button
          class="stg-toggle"
          :class="{ on: !!s.aetherPsiphon }"
          role="switch"
          :aria-checked="!!s.aetherPsiphon"
          aria-label="Run Psiphon after the Aether tunnel"
          title="Run Psiphon after the Aether tunnel"
          @click="flip('aetherPsiphon')"
        >
          <span class="knob" />
        </button>
      </span>
    </div>
    <p class="muted">
      The two legs are independent: the SNI one runs the spoof engine as its transport, the Aether
      one rides the WARP tunnel. A chain always runs in TUN mode, so NekoGo leaves the OS proxy off
      for those connects, and Connect waits until the chain carries real traffic before the
      transport starts. More exit layers (SSH, Tor, …) will land here as they are added.
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
            @change="setChainStr('chainPsiphonCdnIps', ($event.target as HTMLInputElement).value)"
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
            @change="setChainStr('chainPsiphonCdnSni', ($event.target as HTMLInputElement).value)"
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
            @change="setChainStr('chainPsiphonCdnSets', ($event.target as HTMLInputElement).value)"
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
      override replaces this rendering, and a Psiphon chain always forces TUN whatever this switch
      says. In TUN mode the traffic panel reads the tunnel adapter's own byte counters, so it tracks
      system traffic directly.
    </p>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
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

function setOtherRegion(text: string) {
  const cc = text.toUpperCase().replace(/[^A-Z]/g, '')
  otherRegion.value = cc
  void setChainStr('chainPsiphonRegion', cc)
}

type ChainStrKey =
  | 'chainPsiphonRegion' | 'chainPsiphonMode' | 'chainPsiphonCdnIps'
  | 'chainPsiphonCdnSni' | 'chainPsiphonCdnSets'

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

/** One shared persist path for every toggle: flip optimistically, save,
 *  and roll the UI back if disk did not keep the choice. */
async function flip(field: 'spoofPsiphon' | 'aetherPsiphon' | 'tunEnabled') {
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
