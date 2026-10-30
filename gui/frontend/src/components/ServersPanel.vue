<!-- Right config rail: header, search + list + app info. -->
<template>
  <aside class="nb-servers">
    <div class="nb-servers-head">
      <span class="nb-servers-title"><Icon name="servers" :size="15" />Servers</span>
      <span class="nb-servers-count" :title="profiles.profiles.length + ' profiles loaded'">{{ profiles.profiles.length }}</span>
    </div>
    <div class="nb-search-row">
      <SearchBar v-model="search" />
      <button class="nb-srv-add" title="Import from link" aria-label="Import from link" @click="$emit('import')">
        <Icon name="plus" :size="16" />
      </button>
    </div>
    <ServerList :search="search" :selected-id="selectedId" @select="select" />
    <AppInfoCard :version="version" @open="$emit('openAbout')" />
  </aside>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import Icon from './Icon.vue'
import SearchBar from './SearchBar.vue'
import ServerList from './ServerList.vue'
import AppInfoCard from './AppInfoCard.vue'
import { useProfilesStore } from '../stores/profiles'
import { useSettingsStore } from '../stores/settings'

defineProps<{ selectedId: string; version: string }>()
const emit = defineEmits<{ (e: 'select', id: string): void; (e: 'openAbout'): void; (e: 'import'): void }>()

const settings = useSettingsStore()
const profiles = useProfilesStore()
const search = ref('')

async function select(id: string) {
  try {
    await settings.setActiveProfileId(id)
  } catch {
    // Persistence failed; the action already reverted the optimistic
    // set, so the old selection still shows. Nothing to navigate to.
    return
  }
  emit('select', id)
}
</script>
