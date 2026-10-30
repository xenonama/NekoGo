<!-- Single server card: flag, name+city (with a live "active" pill
     while its core is running), real latency badge, test, radio.
     The row is keyboard-focusable (Tab) and selects on Enter; the Test
     button stays independently focusable and stops propagation. -->
<template>
  <div
    class="nb-srv"
    :class="{ selected }"
    tabindex="0"
    role="button"
    :aria-label="'Select server ' + server.name"
    @click="$emit('select', server.id)"
    @keydown.enter="$emit('select', server.id)"
  >
    <span class="nb-flag">{{ flagEmoji(server.countryCode) }}</span>
    <span class="meta">
      <div class="name-row">
        <span class="name">{{ server.name }}</span>
        <span v-if="active" class="nb-active-pill" title="Connected">active</span>
      </div>
      <div class="city">{{ server.city }}</div>
    </span>
    <span class="nb-lat-slot">
      <span v-if="testing" class="nb-spin" title="Testing…" />
      <span v-else-if="failed && server.latencyMs >= 0" class="nb-lat lat-failed-num" :title="failTitle">● {{ server.latencyMs }} ms (Failed)</span>
      <span v-else-if="failed" class="nb-lat lat-bad nb-failed" title="Last test failed"><i class="nb-fail-dot" />Failed</span>
      <span v-else-if="server.latencyMs >= 0" class="nb-lat" :class="latClass" :title="title">
        ● {{ server.latencyMs }} ms
      </span>
      <span v-else class="nb-lat nb-untested" title="Never tested">Untested</span>
    </span>
    <button class="nb-icon-btn nb-srv-test" title="Test latency" :disabled="testing" @click.stop="$emit('test', server.id)">
      <Icon name="bolt" :size="14" />
    </button>
    <span class="nb-radio"><Icon v-if="selected" name="check" :size="12" /></span>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import Icon from './Icon.vue'
import { flagEmoji, isStale, latencyClass, type ServerRow } from '../data/servers'

const props = defineProps<{
  server: ServerRow
  selected: boolean
  active?: boolean
  testing: boolean
  failed: boolean
  failTitle?: string
}>()
defineEmits<{ (e: 'select', id: string): void; (e: 'test', id: string): void }>()

const latClass = computed(() =>
  isStale(props.server.testedAt) ? 'lat-stale' : latencyClass(props.server.latencyMs),
)
const title = computed(() =>
  isStale(props.server.testedAt) ? 'Measured over 1 hour ago' : 'Measured latency',
)
</script>
