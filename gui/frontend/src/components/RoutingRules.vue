<template>
  <div class="panel">
    <p class="muted">Routing rules are stored per profile via raw config passthrough:</p>
    <ul>
      <li><b>sing-box</b> profiles: put extra <code>route.rules</code> by embedding a full outbound in <i>Raw outbound JSON</i>, or extend <code>BuildSingBoxConfig</code> defaults.</li>
      <li><b>xray</b> profiles: extend the generated <code>routing.rules</code> in <code>BuildXrayConfig</code>, or supply a complete config through raw JSON.</li>
      <li><b>mihomo</b> profiles: the renderer sends every connection to the profile's own proxy (one <code>MATCH</code> rule into a single-member group). Per-profile rule lists are not editable.</li>
    </ul>
    <p class="muted">Active connections (the xray core exposes none):</p>
    <button class="nb-act" :disabled="loading" @click="load">
      <Icon name="refresh" :size="14" />{{ loading ? 'Refreshing…' : 'Refresh' }}
    </button>
    <p v-if="error" class="latency-bad">{{ error }}</p>
    <p v-if="truncated" class="muted">showing first 500 (server truncated the list)</p>
    <table class="grid" style="margin-top: 8px">
      <thead><tr><th>Dest</th><th>Outbound</th><th>Net</th><th>Up</th><th>Down</th></tr></thead>
      <tbody>
        <tr v-for="c in conns" :key="c.id">
          <td>{{ c.dest || c.domain }}</td><td>{{ c.outbound }}</td><td>{{ c.network }}</td>
          <td>{{ c.upload }}</td><td>{{ c.download }}</td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import Icon from './Icon.vue'
import { backend } from '../api'
import type { Connection } from '../types'

const conns = ref<Connection[]>([])
const truncated = ref(false)
// BUG-010: busy guard + surfaced errors. Without these, rapid clicks
// fire overlapping RPCs (last-to-resolve wins) and offline/xray-empty
// renders identically to zero connections with the reason discarded.
const loading = ref(false)
const error = ref('')
async function load() {
  if (loading.value) return
  loading.value = true
  error.value = ''
  try {
    const full = await backend.listConnectionsFull()
    if (full && !Array.isArray(full)) {
      conns.value = full.connections ?? []
      truncated.value = full.truncated === true
    } else {
      conns.value = (full ?? []) as unknown as Connection[]
      truncated.value = false
    }
  } catch (e) {
    conns.value = []
    truncated.value = false
    error.value = String(e)
  } finally {
    loading.value = false
  }
}
load()
</script>
