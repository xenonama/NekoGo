<!-- Phase H: real-time traffic chart (hand-rolled SVG, zero deps).
     Samples the 1s connection-store poll into a 60s up/down history. -->
<template>
  <div class="nb-card nb-traffic-chart">
    <div class="nb-chart-head">
      <span class="muted">Traffic — last 60s</span>
      <span class="nb-chart-legend">
        <span class="nb-dot down" />↓ {{ formatRate(lastDown) }}
        <span class="nb-dot up" />↑ {{ formatRate(lastUp) }}
      </span>
    </div>
    <svg viewBox="0 0 300 64" preserveAspectRatio="none" class="nb-chart-svg" aria-label="Traffic chart">
      <polyline :points="downPoints" class="nb-line down" />
      <polyline :points="upPoints" class="nb-line up" />
    </svg>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useConnectionStore } from '../stores/connection'
import { formatRate } from '../data/servers'

const connection = useConnectionStore()

const downs = ref<number[]>([])
const ups = ref<number[]>([])
let prevDown = -1
let prevUp = -1
function sample() {
  const d = connection.stats.downlink ?? 0
  const u = connection.stats.uplink ?? 0
  if (connection.status.running && prevDown >= 0) {
    downs.value = [...downs.value, Math.max(0, d - prevDown)].slice(-60)
    ups.value = [...ups.value, Math.max(0, u - prevUp)].slice(-60)
  } else if (!connection.status.running) {
    downs.value = []
    ups.value = []
  }
  prevDown = d
  prevUp = u
}

function toPoints(series: number[]): string {
  if (series.length === 0) return ''
  const max = Math.max(1, ...series)
  const n = series.length
  return series
    .map((v, i) => {
      // Right-aligned across a fixed 60-slot window.
      const x = 300 - (n - 1 - i) * (300 / 59)
      const y = 62 - (v / max) * 60
      return `${x.toFixed(1)},${y.toFixed(1)}`
    })
    .join(' ')
}

const downPoints = computed(() => toPoints(downs.value))
const upPoints = computed(() => toPoints(ups.value))
const lastDown = computed(() => (downs.value.length > 0 ? downs.value[downs.value.length - 1] : 0))
const lastUp = computed(() => (ups.value.length > 0 ? ups.value[ups.value.length - 1] : 0))

watch(
  () => connection.status.running,
  (on) => {
    if (on) {
      prevDown = -1
      prevUp = -1
    } else {
      downs.value = []
      ups.value = []
    }
  },
  { immediate: true },
)
// Sample on the connection store's own 1s tick (it replaces `stats`
// on every refresh) instead of running a second timer of our own.
watch(
  () => connection.stats,
  () => {
    if (connection.status.running) sample()
  },
)

// Keep the chart sampling fresh while mounted.
onMounted(() => connection.acquire())
onUnmounted(() => connection.release())
</script>
