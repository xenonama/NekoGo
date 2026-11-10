<!-- Live traffic: summary cards, per-second rate row, per-tag table. -->
<template>
  <div class="panel ts">
    <div v-if="stats.apiDown" class="nb-warn-banner">{{ upstream ? 'Traffic stats are unavailable on this core' : 'Core API unreachable — traffic data is stale' }}</div>
    <div class="ts-cards">
      <div class="ts-card">
        <div class="k">Core</div>
        <div class="v"><span class="ts-dot" :class="{ on: running }"></span>{{ coreLabel }}</div>
      </div>
      <div class="ts-card">
        <div class="k">Uplink</div>
        <div class="v up">{{ fmt(stats.uplink) }}</div>
      </div>
      <div class="ts-card">
        <div class="k">Downlink</div>
        <div class="v down">{{ fmt(stats.downlink) }}</div>
      </div>
    </div>
    <div class="ts-rate">
      <span :class="downDelta > 0 ? 'down' : 'idle'">↓ {{ downText }}</span>
      <span :class="upDelta > 0 ? 'up' : 'idle'">↑ {{ upText }}</span>
    </div>
    <template v-if="tags.length > 0">
      <h4>Per-tag breakdown</h4>
      <div class="ts-scroll">
        <table class="grid ts-tags">
          <thead><tr><th>Tag</th><th class="num">Up</th><th class="num">Down</th></tr></thead>
          <tbody>
            <tr v-for="tag in tags" :key="tag">
              <td>{{ tag }}</td>
              <td class="num">{{ fmt((stats.uplinkByTag || {})[tag] || 0) }}</td>
              <td class="num">{{ fmt((stats.downlinkByTag || {})[tag] || 0) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>
    <p v-else class="muted">No per-tag data available.</p>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useConnectionStore } from '../stores/connection'
import { formatRate } from '../data/servers'

const connection = useConnectionStore()
const stats = computed(() => connection.stats)
const running = computed(() => connection.status.running)
// Engine display names. The backend labels the two sing-box backends by
// family ("sing-box" custom, "upstream sing-box" official), which reads
// ambiguously next to the Settings → Connection choice, so the card
// spells out which binary is actually running. "_" while disconnected:
// the core name is reported even when idle, which reads as if it ran.
const coreLabel = computed(() => {
  if (!running.value || !stats.value.core) return '_'
  switch (stats.value.core) {
    case 'upstream sing-box':
      return 'sing-box'
    case 'sing-box':
      return 'NekoBox Core'
    case 'xray':
      return 'Xray'
    case 'spoof':
      return 'SNI Spoof'
    case 'mihomo':
      return 'Mihomo'
    default:
      return stats.value.core
  }
})
// The upstream CLI backend reports its own core name; only that backend
// has a stats feed that can be missing outright (Clash API failure).
const upstream = computed(() => stats.value.core === 'upstream sing-box')
const tags = computed(() => {
  const t = new Set<string>()
  Object.keys(stats.value.uplinkByTag || {}).forEach((k) => t.add(k))
  Object.keys(stats.value.downlinkByTag || {}).forEach((k) => t.add(k))
  return [...t]
})

// Per-second deltas derived from the cumulative backend counters — the same
// baseline logic as ConnectionCard.vue: -1 means "no baseline yet", and a
// zero delta (counters unchanged between two ticks) renders as "—".
const downDelta = ref(-1)
const upDelta = ref(-1)
let prevDown = -1
let prevUp = -1

function sampleRates() {
  const d = connection.stats.downlink ?? 0
  const u = connection.stats.uplink ?? 0
  if (running.value && prevDown >= 0) {
    downDelta.value = Math.max(0, d - prevDown)
    upDelta.value = Math.max(0, u - prevUp)
  } else {
    downDelta.value = -1
    upDelta.value = -1
  }
  prevDown = d
  prevUp = u
}

const downText = computed(() => (running.value && downDelta.value > 0 ? formatRate(downDelta.value) : '—'))
const upText = computed(() => (running.value && upDelta.value > 0 ? formatRate(upDelta.value) : '—'))

watch(
  running,
  (on) => {
    if (on) {
      prevDown = -1
      prevUp = -1
    } else {
      downDelta.value = -1
      upDelta.value = -1
    }
  },
  // The panel remounts on every return to the Console tab, so without
  // immediate a mount made mid-connection would never sample.
  { immediate: true },
)
// Sample on the connection store's own 1s tick (it replaces `stats`
// on every refresh) instead of running a second timer of our own.
watch(
  () => connection.stats,
  () => {
    if (running.value) sampleRates()
  },
)

// Keep traffic stats fresh while the panel is mounted.
onMounted(() => connection.acquire())
onUnmounted(() => connection.release())

function fmt(n: number) {
  if (!n) return '0 B'
  const u = ['B', 'KB', 'MB', 'GB']
  let i = 0
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++ }
  return `${n.toFixed(1)} ${u[i]}`
}
</script>
