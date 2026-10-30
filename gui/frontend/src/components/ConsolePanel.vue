<!-- Console panel: pill tabs switching between live traffic, logs,
     and the connections list. The former extra tool sections now
     live in the Advanced world (see the adv/ directory); the
     Connections list below is additionally re-hosted there through
     the additive only/startTab/hideTabs props. -->
<template>
  <div v-if="!hideTabs" class="console-tabs" role="tablist">
    <button
      v-if="showTab('traffic')"
      class="console-tab"
      :class="{ active: tab === 'traffic' }"
      role="tab"
      :aria-selected="tab === 'traffic'"
      @click="tab = 'traffic'"
    >
      <Icon name="bolt" :size="18" />
      <span>Traffic</span>
    </button>
    <button
      v-if="showTab('logs')"
      class="console-tab"
      :class="{ active: tab === 'logs' }"
      role="tab"
      :aria-selected="tab === 'logs'"
      @click="tab = 'logs'"
    >
      <Icon name="terminal" :size="18" />
      <span>Logs</span>
    </button>
    <button
      v-if="showTab('connection')"
      class="console-tab"
      :class="{ active: tab === 'connection' }"
      role="tab"
      :aria-selected="tab === 'connection'"
      @click="tab = 'connection'"
    >
      <Icon name="list" :size="18" />
      <span>Connection</span>
    </button>
  </div>
  <div class="console-fill">
    <TrafficStats v-if="tab === 'traffic' && showTab('traffic')" />
    <LogViewer v-else-if="tab === 'logs' && showTab('logs')" />
    <!-- Phase H: full connections list (same table style as RoutingRules,
         capped at 200 with the Truncated indicator). The backend exposes
         no per-connection killer, so Kill stays disabled with a tooltip. -->
    <div v-else-if="tab === 'connection' && showTab('connection')" class="net-stack">
      <div class="stg-card">
        <div class="stg-head">
          <span class="stg-head-chip"><Icon name="list" :size="22" /></span>
          <span class="stg-head-text">
            <span class="stg-head-title">Connections</span>
            <span class="stg-head-sub">Live snapshot from the running core</span>
          </span>
        </div>
        <div class="toolbar" style="padding: 0 0 8px">
          <button class="nb-act nb-act--primary" :disabled="connLoading" @click="loadConnections">
            <Icon name="refresh" :size="14" />{{ connLoading ? 'Refreshing…' : 'Refresh' }}
          </button>
          <span class="muted">{{ conns.length }} shown</span>
        </div>
        <p v-if="connError" class="nb-json-error">{{ connError }}</p>
        <p v-if="connTruncated" class="muted">showing first 200 (list truncated)</p>
        <p v-else-if="conns.length === 0 && !connLoading && !connError" class="muted">
          No connections — connect a profile first.
        </p>
        <div v-for="c in conns" :key="c.id" class="stg-row">
          <span class="stg-chip"><Icon name="globe" :size="18" /></span>
          <span class="stg-meta">
            <button class="nb-insp-top" @click="connOpen = connOpen === c.id ? '' : c.id">
              <span class="stg-title nb-mono">{{ c.domain || c.dest || c.id }}</span>
              <span class="stg-sub">{{ c.outbound }} · {{ c.network }} · ↓{{ c.download }} ↑{{ c.upload }}</span>
            </button>
            <span v-if="connOpen === c.id" class="stg-sub selectable nb-mono">
              id {{ c.id }} · dest {{ c.dest || '—' }} · domain {{ c.domain || '—' }} ·
              outbound {{ c.outbound || '—' }} · network {{ c.network || '—' }} ·
              protocol {{ c.protocol || '—' }} · process {{ c.process || '—' }} ·
              upload {{ c.upload }} · download {{ c.download }}
            </span>
          </span>
          <span class="stg-ctl">
            <button class="nb-act nb-act--sm nb-act--ghost" disabled title="Kill connection: not supported by backend">Kill</button>
          </span>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import Icon from './Icon.vue'
import TrafficStats from './TrafficStats.vue'
import LogViewer from './LogViewer.vue'
import { backend } from '../api'
import type { Connection } from '../types'

type ConsoleTab = 'traffic' | 'logs' | 'connection'

// The Advanced side re-hosts the connections list below
// (connections only, pill bar hidden). Console itself passes
// traffic/logs/connection.
const props = withDefaults(
  defineProps<{
    only?: ConsoleTab[]
    startTab?: ConsoleTab
    hideTabs?: boolean
  }>(),
  { only: undefined, startTab: undefined, hideTabs: false },
)

function showTab(t: ConsoleTab): boolean {
  return props.only === undefined || props.only.includes(t)
}

// Default tab: Traffic (or startTab when embedded in the hub).
const tab = ref<ConsoleTab>(
  props.startTab && showTab(props.startTab) ? props.startTab : 'traffic',
)

// Phase H: full connections list (display-capped at 200; the server
// itself caps at 500 with its own Truncated flag).
const conns = ref<Connection[]>([])
const connTruncated = ref(false)
const connLoading = ref(false)
const connError = ref('')
const connOpen = ref('')

async function loadConnections() {
  if (connLoading.value) return
  connLoading.value = true
  connError.value = ''
  try {
    const full = await backend.listConnectionsFull()
    conns.value = (full?.connections ?? []).slice(0, 200)
    connTruncated.value = full?.truncated === true || (full?.connections?.length ?? 0) > 200
  } catch (e) {
    conns.value = []
    connTruncated.value = false
    connError.value = String(e)
  } finally {
    connLoading.value = false
  }
}

watch(tab, (t) => {
  if (t === 'connection') {
    void loadConnections()
  }
})
</script>
