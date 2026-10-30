<!-- Scrollable server list. Renders only real backend profiles;
     empty states replace the old mock catalog. Emits select/test. -->
<template>
  <div class="nb-list">
    <ServerCard
      v-for="s in servers"
      :key="s.id"
      :server="s"
      :selected="s.id === selectedId"
      :active="isActive(s.id)"
      :testing="testing.has(s.id)"
      :failed="!!profiles.lastError[s.id]"
      :fail-title="failTitle(s.id)"
      @select="$emit('select', $event)"
      @test="testOne"
    />
    <div v-if="servers.length === 0" class="nb-empty">
      <Icon name="cat" :size="48" class="nb-empty-cat" />
      <p><strong>{{ profiles.profiles.length === 0 ? 'No servers yet' : 'No servers found' }}</strong></p>
      <p class="muted">{{ profiles.profiles.length === 0 ? 'Add some in Proxies.' : 'Try a different search.' }}</p>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import Icon from './Icon.vue'
import ServerCard from './ServerCard.vue'
import { useProfilesStore } from '../stores/profiles'
import { useConnectionStore } from '../stores/connection'
import type { ServerRow } from '../data/servers'

const props = defineProps<{ search: string; selectedId: string }>()
defineEmits<{ (e: 'select', id: string): void }>()

const profiles = useProfilesStore()
const connection = useConnectionStore()
const testing = ref<Set<string>>(new Set())

/** The row carrying the live connection (parity with ProxiesTable's
 *  active pill: selection alone never says which core is running). */
function isActive(id: string): boolean {
  return connection.status.running && connection.status.profileId === id
}

const servers = computed<ServerRow[]>(() => {
  const q = props.search.trim().toLowerCase()
  return profiles.profiles
    .filter((p) => !q || p.name.toLowerCase().includes(q) || p.address.toLowerCase().includes(q))
    .map((p) => ({
      id: p.id,
      name: p.name,
      city: `${p.address}:${p.port}`,
      countryCode: p.countryCode ?? '',
      latencyMs: profiles.latencies[p.id] ?? -1,
      testedAt: profiles.testedAt[p.id] ?? p.lastTestedAt,
    }))
})

/** Tooltip for a failed row: error message + when it failed. */
function failTitle(id: string): string {
  const msg = profiles.lastError[id]
  if (!msg) return ''
  const at = profiles.lastErrorAt[id]
  let when = ''
  if (at) {
    const t = new Date(at).getTime()
    when = Number.isNaN(t) ? at : new Date(t).toLocaleString()
  }
  return when ? `${msg} (failed ${when})` : msg
}

// Keep the active-core pill fresh while the list is mounted.
onMounted(() => connection.acquire())
onUnmounted(() => connection.release())

async function testOne(id: string) {
  testing.value = new Set(testing.value).add(id)
  try {
    await profiles.test(id)
  } catch {
    /* failure is shown via the Failed badge */
  } finally {
    const s = new Set(testing.value)
    s.delete(id)
    testing.value = s
  }
}
</script>
