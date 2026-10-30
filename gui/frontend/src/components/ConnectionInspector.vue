<!-- Phase H: Home connection inspector — the most recent connections
     (capped at 20), each row expanding to full metadata. Advanced only. -->
<template>
  <div class="nb-card nb-inspector">
    <div class="nb-chart-head">
      <span class="muted">Connections ({{ rows.length }})</span>
      <button class="nb-icon-btn" title="Refresh" :disabled="loading" @click="load">
        <Icon name="refresh" :size="15" />
      </button>
    </div>
    <p v-if="error" class="nb-json-error">{{ error }}</p>
    <p v-else-if="rows.length === 0 && !loading" class="muted">No connections — connect a profile first.</p>
    <div v-for="c in rows" :key="c.id" class="nb-insp-row">
      <button class="nb-insp-top" @click="toggle(c.id)">
        <span class="nb-px-name">{{ c.domain || c.dest }}</span>
        <span class="muted">{{ c.outbound }}</span>
        <span class="muted">↓{{ c.download }} ↑{{ c.upload }}</span>
      </button>
      <div v-if="open === c.id" class="nb-insp-meta selectable">
        <div>source: {{ c.id || '—' }}</div>
        <div>dest: {{ c.dest || '—' }}</div>
        <div>domain: {{ c.domain || '—' }}</div>
        <div>outbound: {{ c.outbound || '—' }}</div>
        <div>network: {{ c.network || '—' }}</div>
        <div>protocol: {{ c.protocol || '—' }}</div>
        <div>process: {{ c.process || '—' }}</div>
        <div>bytes in/out: {{ c.download }} / {{ c.upload }}</div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import Icon from './Icon.vue'
import { backend } from '../api'
import type { Connection } from '../types'

const rows = ref<Connection[]>([])
const loading = ref(false)
const error = ref('')
const open = ref('')
let timer: number | undefined

function toggle(id: string) {
  open.value = open.value === id ? '' : id
}

async function load() {
  if (loading.value) return
  loading.value = true
  error.value = ''
  try {
    const full = await backend.listConnectionsFull()
    rows.value = (full?.connections ?? []).slice(0, 20)
  } catch (e) {
    rows.value = []
    error.value = String(e)
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  void load()
  timer = window.setInterval(() => void load(), 5000)
})
onUnmounted(() => window.clearInterval(timer))
</script>
