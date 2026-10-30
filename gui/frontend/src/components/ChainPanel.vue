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
import { useSettingsStore } from '../stores/settings'
import { useConnectionStore } from '../stores/connection'

const settings = useSettingsStore()
const connection = useConnectionStore()
const s = computed(() => settings.settings)
const err = ref('')

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
