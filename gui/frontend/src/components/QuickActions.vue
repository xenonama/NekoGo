<!-- Quick Actions: 4 actions that all do something real. The old
     Rule/Global tiles only flipped local state with zero backend
     effect — replaced with Test All (real latency probes) and
     System Proxy (real settings toggle). -->
<template>
  <div>
    <div class="nb-section-head">
      <span class="nb-section-title">Quick Actions</span>
      <span v-if="testing" class="muted">{{ testedCount }} / {{ profiles.profiles.length }} tested</span>
    </div>
    <div class="nb-qa-grid">
      <button class="nb-qa" @click="autoSelect">
        <span class="ico ico-blue"><Icon name="bolt" :size="20" /></span>
        <span class="t">Auto Select</span>
        <span class="s">Fastest server</span>
      </button>
      <button class="nb-qa" :disabled="testing || profiles.profiles.length === 0" @click="testAll">
        <span class="ico ico-green"><Icon name="list" :size="20" /></span>
        <span class="t">{{ testing ? 'Testing…' : 'Test All' }}</span>
        <span class="s">{{ testing ? 'Probing latencies' : 'Check every server' }}</span>
      </button>
      <button class="nb-qa" :class="{ active: tunOn }" @click="toggleTun">
        <span class="ico ico-purple"><Icon name="shield" :size="20" /></span>
        <span class="t">TUN Mode</span>
        <span class="s">{{ tunOn ? 'Enabled' : 'Full tunnel' }}</span>
      </button>
      <button class="nb-qa" :class="{ active: coreRunning && sysProxyOn }" :disabled="sysProxyBusy || !coreRunning" @click="toggleSysProxy">
        <span class="ico ico-pink"><Icon name="globe" :size="20" /></span>
        <span class="t">System Proxy</span>
        <span class="s" :class="{ err: !!sysProxyMsg }">{{ sysProxyMsg || sysProxyLabel }}</span>
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import Icon from './Icon.vue'
import { backend } from '../api'
import { useProfilesStore } from '../stores/profiles'
import { useSettingsStore } from '../stores/settings'
import { useConnectionStore } from '../stores/connection'

const profiles = useProfilesStore()
const settings = useSettingsStore()
const connection = useConnectionStore()
const tunOn = computed(() => settings.settings.tunEnabled)
// Live OS proxy switch (Connect applies it in proxy mode and leaves it
// off in TUN mode; this button flips the real switch instead of a
// settings flag).
const sysProxyLive = ref<boolean | null>(null)
const sysProxyBusy = ref(false)
const sysProxyMsg = ref('')
const coreRunning = computed(() => connection.status.running)
const sysProxyOn = computed(() => sysProxyLive.value ?? settings.settings.systemProxy)
const sysProxyLabel = computed(() =>
  coreRunning.value ? (sysProxyOn.value ? 'On' : 'Off') : 'Connect first',
)
const testing = ref(false)
const testedCount = ref(0)

/** Pick the lowest-latency really measured server and mark it selected.
 *  Servers whose last test failed are skipped unless every measured
 *  server has an error (then latency alone decides). */
async function autoSelect() {
  if (profiles.profiles.length === 0) return
  const measured = profiles.profiles.filter((p) => (profiles.latencies[p.id] ?? -1) > 0)
  const clean = measured.filter((p) => !profiles.lastError[p.id])
  const pool = clean.length > 0 ? clean : measured.length > 0 ? measured : profiles.profiles
  const best = [...pool].sort(
    (a, b) => (profiles.latencies[a.id] ?? 9999) - (profiles.latencies[b.id] ?? 9999),
  )[0]
  try {
    await settings.setActiveProfileId(best.id)
  } catch {
    /* action already reverted the optimistic set; stay put */
  }
}

/** Probe every profile once (sequentially, failures recorded per
 *  row by the store — never aborts the batch). */
async function testAll() {
  if (testing.value || profiles.profiles.length === 0) return
  testing.value = true
  testedCount.value = 0
  try {
    for (const p of profiles.profiles) {
      try {
        await profiles.test(p.id)
      } catch {
        /* recorded on the row; keep going */
      }
      testedCount.value++
    }
  } finally {
    testing.value = false
  }
}

async function toggleTun() {
  const next = !settings.settings.tunEnabled
  settings.settings.tunEnabled = next
  try {
    await settings.save()
  } catch {
    // Disk did not keep the choice: roll back, or the tile keeps claiming a
    // tunnel mode the next connect will not actually use.
    settings.settings.tunEnabled = !next
  }
}

async function toggleSysProxy() {
  if (sysProxyBusy.value || !coreRunning.value) return
  sysProxyBusy.value = true
  sysProxyMsg.value = ''
  // Toggle FROM the displayed state (sysProxyOn = live ?? settings):
  // checking sysProxyLive alone could show "On" (settings fallback)
  // yet re-enable on click when the live probe never succeeded.
  const wantOn = !sysProxyOn.value
  try {
    if (wantOn) await backend.enableSystemProxyGUI()
    else await backend.disableSystemProxyGUI()
  } catch (e) {
    // This used to be a silent catch that deferred the message to the
    // System card — which is often not even mounted, so a refused call
    // rendered as a successful toggle.
    sysProxyMsg.value = String(e)
  } finally {
    sysProxyBusy.value = false
  }
  // Re-read the OS instead of assigning wantOn: this tile reports a
  // real switch, and one that refused to move must not render as moved.
  await refreshSysProxyLive()
}

async function refreshSysProxyLive() {
  try {
    sysProxyLive.value = await backend.getSystemProxyState()
  } catch {
    /* probe unavailable: the settings flag stays as fallback */
  }
}

// Connect applies the OS proxy in proxy mode and leaves it off in TUN
// mode (autoProxyOn owns the revert), so the real switch can flip while
// this card is mounted — re-read it on every core state change.
watch(() => connection.status.running, refreshSysProxyLive)

onMounted(() => {
  refreshSysProxyLive()
  connection.acquire()
})
onUnmounted(() => connection.release())
</script>
