<!-- Advanced Backup: simple card stack. -->
<template>
  <div class="nb-card nb-page">
    <h3>Backup &amp; reset</h3>
    <p class="muted">Export, import, inspect and reset all data.</p>
  </div>
  <div class="nb-card nb-page">
    <h3>Export</h3>
    <p class="muted">All settings, profiles, subscriptions and overrides in one file.</p>
    <BackupPanel :only="['export']" />
  </div>
  <div class="nb-card nb-page">
    <h3>Import</h3>
    <p class="muted">Restore from a backup file (replaces current data).</p>
    <BackupPanel :only="['import']" />
  </div>
  <div class="nb-card nb-page">
    <h3>View raw</h3>
    <p class="muted">Exact config the active core receives, overrides merged in.</p>
    <div v-if="!connection.status.running" class="nb-card nb-empty">
      <p class="muted">Connect a profile to see its live config.</p>
      <button class="nb-act nb-act--primary" @click="openProxies">
        <Icon name="servers" :size="14" />Open Proxies
      </button>
    </div>
    <BackupPanel v-else :only="['raw']" />
  </div>
  <div class="nb-card nb-page">
    <h3>Reset</h3>
    <p class="muted">Wipe everything back to defaults (cannot be undone).</p>
    <BackupPanel :only="['reset']" />
  </div>
</template>

<script setup lang="ts">
import Icon from '../Icon.vue'
import BackupPanel from '../BackupPanel.vue'
import { useConnectionStore } from '../../stores/connection'

const emit = defineEmits<{
  (e: 'goto', view: 'proxies'): void
}>()

const connection = useConnectionStore()

function openProxies() {
  emit('goto', 'proxies')
}
</script>
