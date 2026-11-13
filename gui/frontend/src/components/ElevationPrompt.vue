<!-- Relaunch-as-administrator dialog. An elevation refusal used to open
     window.confirm, which WebView2 renders as a browser box that matches
     nothing else in the app; this is the same .nb-modal the import and
     subscription dialogs use. -->
<template>
  <div v-if="elevation.prompt" class="nb-modal-overlay" @click.self="elevation.close(false)">
    <div class="nb-modal" role="alertdialog" aria-modal="true" aria-label="Relaunch as administrator">
      <div class="nb-modal-head">
        <span class="nb-section-title">Administrator rights needed</span>
        <button class="nb-icon-btn" title="Close" @click="elevation.close(false)">
          <Icon name="x" :size="18" />
        </button>
      </div>
      <p class="nb-modal-hint">{{ elevation.prompt }}</p>
      <p class="muted">
        NekoGo restarts elevated and, while a tunnel is running, the new instance reconnects to
        it. Windows asks first — choosing No leaves this instance running as it is.
      </p>
      <div class="nb-modal-foot">
        <button class="nb-act nb-act--ghost" @click="elevation.close(false)">
          <Icon name="x" :size="14" />Cancel
        </button>
        <button class="nb-act nb-act--primary" @click="elevation.close(true)">
          <Icon name="shield" :size="14" />Relaunch as administrator
        </button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, onUnmounted } from 'vue'
import { useElevationStore } from '../stores/elevation'
import Icon from './Icon.vue'

const elevation = useElevationStore()

// Same ESC contract as the other modals: it means the same as Cancel, so
// the waiting caller resumes without an elevation attempt.
function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape' && elevation.prompt) elevation.close(false)
}

onMounted(() => window.addEventListener('keydown', onKey))
onUnmounted(() => window.removeEventListener('keydown', onKey))
</script>
