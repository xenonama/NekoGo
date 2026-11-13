<!-- The app's own yes/no dialog (.nb-modal, the card the import and
     subscription dialogs use) for the destructive actions in Settings.
     Same ESC contract as the other modals: it means Cancel, so the
     waiting caller resumes without the action. -->
<template>
  <div v-if="box.request" class="nb-modal-overlay" @click.self="box.close(false)">
    <div class="nb-modal" role="alertdialog" aria-modal="true" :aria-label="box.request.title">
      <div class="nb-modal-head">
        <span class="nb-section-title">{{ box.request.title }}</span>
        <button class="nb-icon-btn" title="Close" @click="box.close(false)">
          <Icon name="x" :size="18" />
        </button>
      </div>
      <p class="nb-modal-hint">{{ box.request.body }}</p>
      <div class="nb-modal-foot">
        <button class="nb-act nb-act--ghost" @click="box.close(false)">
          <Icon name="x" :size="14" />Cancel
        </button>
        <button
          class="nb-act"
          :class="box.request.danger ? 'nb-act--danger' : 'nb-act--primary'"
          @click="box.close(true)"
        >
          <Icon :name="box.request.danger ? 'trash' : 'check'" :size="14" />{{ box.request.confirmLabel }}
        </button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, onUnmounted } from 'vue'
import { useConfirmStore } from '../stores/confirm'
import Icon from './Icon.vue'

const box = useConfirmStore()

function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape' && box.request) box.close(false)
}

onMounted(() => window.addEventListener('keydown', onKey))
onUnmounted(() => window.removeEventListener('keydown', onKey))
</script>
