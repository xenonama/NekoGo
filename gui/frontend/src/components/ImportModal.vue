<!-- Import-from-link modal: paste links, Parse previews, Import persists. -->
<template>
  <div v-if="show" class="nb-modal-overlay" @click.self="$emit('close')">
    <div class="nb-modal">
      <div class="nb-modal-head">
        <span class="nb-section-title">Import from Link</span>
        <button class="nb-icon-btn" title="Close" @click="$emit('close')">
          <Icon name="x" :size="18" />
        </button>
      </div>
      <p class="muted nb-modal-hint">
        One link per line. Supports vmess://, vless://, trojan://, ss://, socks:// and
        http(s):// subscription URLs.
      </p>
      <textarea
        ref="textRef"
        v-model="text"
        class="nb-modal-text"
        rows="6"
        placeholder="vless://…&#10;vmess://…&#10;https://example.com/sub.txt"
      />
      <div v-if="error" class="nb-conn-err">{{ error }}</div>
      <div v-if="preview" class="nb-modal-preview">
        <div class="nb-kv" v-for="p in preview.profiles" :key="p.id || p.name + p.address">
          <span class="badge">{{ p.protocol }}</span>
          <span class="nb-modal-name">{{ p.name }}</span>
          <span class="muted">{{ p.address }}:{{ p.port }}</span>
        </div>
        <div v-if="preview.profiles.length === 0" class="muted">No valid links found.</div>
        <div v-if="preview.invalid > 0" class="nb-conn-err">{{ preview.invalid }} invalid line(s) will be skipped.</div>
      </div>
      <div v-if="summary" class="nb-modal-summary">{{ summary }}</div>
      <div class="nb-modal-foot">
        <button class="nb-act" :disabled="busy || !text.trim()" @click="parse">
          <Icon name="list" :size="14" />Parse
        </button>
        <button class="nb-act nb-act--primary" :disabled="busy || !text.trim()" @click="doImport">
          <Icon name="plus" :size="14" />Import
        </button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { nextTick, onUnmounted, ref, watch } from 'vue'
import Icon from './Icon.vue'
import { backend } from '../api'
import type { LinkImportResult } from '../types'

const props = defineProps<{ show: boolean }>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'imported'): void }>()

const text = ref('')
const preview = ref<LinkImportResult | null>(null)
const busy = ref(false)
const error = ref('')
const summary = ref('')
const textRef = ref<HTMLTextAreaElement | null>(null)
// BUG-020: opening generation — closing mid-parse and reopening must
// not let the stale promise overwrite the fresh empty state.
let gen = 0

// BUG-019: ESC dismisses the modal (window listener is the single ESC
// path) with cleanup on unmount; the textarea autofocuses on open.
function onEsc(e: KeyboardEvent) {
  if (e.key === 'Escape' && props.show) emit('close')
}

watch(
  () => props.show,
  (v) => {
    if (v) {
      gen++
      text.value = ''
      preview.value = null
      busy.value = false
      error.value = ''
      summary.value = ''
      window.addEventListener('keydown', onEsc)
      void nextTick(() => textRef.value?.focus())
    } else {
      gen++
      window.removeEventListener('keydown', onEsc)
    }
  },
)

onUnmounted(() => window.removeEventListener('keydown', onEsc))

async function parse() {
  const g = gen
  busy.value = true
  error.value = ''
  summary.value = ''
  try {
    const r = await backend.previewLinks(text.value)
    if (g !== gen) return
    preview.value = r
  } catch (e) {
    if (g !== gen) return
    error.value = String(e)
  } finally {
    if (g === gen) busy.value = false
  }
}

async function doImport() {
  const g = gen
  busy.value = true
  error.value = ''
  try {
    const r = await backend.importLinks(text.value)
    if (g !== gen) return
    preview.value = r
    summary.value = `Imported ${r.profiles.length} profiles, skipped ${r.invalid} invalid links.`
    emit('imported')
  } catch (e) {
    if (g !== gen) return
    error.value = String(e)
  } finally {
    if (g === gen) busy.value = false
  }
}
</script>
