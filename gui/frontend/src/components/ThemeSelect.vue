<!-- Custom theme dropdown. Uses the same store.setTheme() code path as the
     sidebar sun/moon toggle, so both stay in sync automatically. -->
<template>
  <div ref="root" class="nb-select">
    <button ref="toggleRef" class="nb-select-btn" :disabled="busy" @click="open = !open">
      <Icon :name="isDark ? 'moon' : 'sun'" :size="16" />
      <span>{{ isDark ? 'Dark' : 'Light' }}</span>
      <span class="nb-select-chev"><Icon name="chevron" :size="14" /></span>
    </button>
    <div v-if="open" class="nb-select-menu">
      <button
        v-for="opt in options"
        :key="opt.value"
        class="nb-select-opt"
        :class="{ active: theme === opt.value }"
        @click="choose(opt.value)"
      >
        <Icon :name="opt.icon" :size="16" />
        <span>{{ opt.label }}</span>
        <span class="nb-select-check"><Icon v-if="theme === opt.value" name="check" :size="14" /></span>
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import Icon, { type IconName } from './Icon.vue'
import { useSettingsStore } from '../stores/settings'

const settings = useSettingsStore()
const theme = computed(() => settings.settings.theme)
const isDark = computed(() => theme.value === 'dark')
const open = ref(false)
const busy = ref(false)
const root = ref<HTMLElement | null>(null)
// BUG-028: the toggle button ref — choosing an option (or ESC) unmounts
// the v-if menu while focus is inside it, so focus is returned here
// instead of dropping to <body>.
const toggleRef = ref<HTMLButtonElement | null>(null)

const options: { value: 'light' | 'dark'; label: string; icon: IconName }[] = [
  { value: 'light', label: 'Light', icon: 'sun' },
  { value: 'dark', label: 'Dark', icon: 'moon' },
]

async function choose(v: 'light' | 'dark') {
  open.value = false
  toggleRef.value?.focus()
  if (v === theme.value) return
  busy.value = true
  try {
    await settings.setTheme(v)
  } finally {
    busy.value = false
  }
}

function onDocClick(e: MouseEvent) {
  if (root.value && !root.value.contains(e.target as Node)) open.value = false
}
function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape' && open.value) {
    open.value = false
    toggleRef.value?.focus()
  }
}

onMounted(() => {
  document.addEventListener('click', onDocClick)
  document.addEventListener('keydown', onKey)
})
onBeforeUnmount(() => {
  document.removeEventListener('click', onDocClick)
  document.removeEventListener('keydown', onKey)
})
</script>
