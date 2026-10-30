<!-- Left sidebar: logo + theme toggle, nav menu, live connection
     status (dot + state, click toggles connect), bottom-left mode
     switch. Advanced adds the Chain hub to the nav; every other tab
     is shared by both modes. -->
<template>
  <aside class="nb-sidebar">
    <div class="nb-logo">
      <img class="nb-logo-img" :src="appIcon" alt="NekoGo" />
      <span class="nb-logo-text">NekoGo</span>
      <button
        class="nb-icon-btn nb-logo-toggle"
        :title="isDark ? 'Switch to light theme' : 'Switch to dark theme'"
        @click="toggleTheme"
      >
        <Icon :name="isDark ? 'sun' : 'moon'" :size="18" />
      </button>
    </div>
    <nav class="nb-nav">
      <button
        v-for="item in items"
        :key="item.key"
        class="nb-nav-item"
        :class="{ active: modelValue === item.key }"
        @click="$emit('update:modelValue', item.key)"
      >
        <Icon :name="item.icon" :size="20" />
        <span v-if="settings.settings.sidebarLabels !== false">{{ item.label }}</span>
      </button>
    </nav>
    <!-- Bottom-left master switch: the only Simple/Advanced toggle.
         It flips the same shell in place. -->
    <div class="nb-side-foot">
      <!-- Live state readout (parity with the Advanced StatusStrip):
           dot + word, click = connect/disconnect the active profile. -->
      <button
        class="nb-side-status"
        :class="stateClass"
        :disabled="connection.busy || connection.connectBusy"
        :title="statusTitle"
        @click="toggleConn"
      >
        <span class="nb-side-dot" />
        <span class="nb-side-state">{{ stateText }}</span>
      </button>
      <button
        class="nb-side-advrow"
        role="switch"
        :aria-checked="isAdvancedMode"
        :aria-label="modeLabel"
        :title="modeTitle"
        @click="toggleMode"
      >
        <span class="nb-side-advlabel">{{ modeLabel }}</span>
        <span class="adv-switch" :class="{ on: isAdvancedMode }" aria-hidden="true" />
      </button>
    </div>
  </aside>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted } from 'vue'
import Icon, { type IconName } from './Icon.vue'
import appIcon from '../assets/app.png'
import { useSettingsStore } from '../stores/settings'
import { useProfilesStore } from '../stores/profiles'
import { useConnectionStore } from '../stores/connection'

export type NavKey =
  | 'home'
  | 'proxies'
  | 'subscription'
  | 'settings'
  | 'console'
  | 'sni'
  | 'aether'
  | 'chain'
  | 'about'

const props = withDefaults(
  defineProps<{
    modelValue: NavKey
    /** Only the footer switch changes with the mode; nav is identical. */
    mode?: 'simple' | 'advanced'
  }>(),
  { mode: 'simple' },
)
const emit = defineEmits<{
  (e: 'update:modelValue', v: NavKey): void
  (e: 'exit'): void
}>()

const settings = useSettingsStore()
const profiles = useProfilesStore()
const connection = useConnectionStore()
const isDark = computed(() => settings.settings.theme === 'dark')

const stateText = computed(() =>
  connection.connectBusy ? 'Connecting…' : connection.status.running ? 'Connected' : 'Offline',
)
const stateClass = computed(() =>
  connection.connectBusy ? 'busy' : connection.status.running ? 'on' : 'off',
)
const statusTitle = computed(() => {
  if (connection.connectBusy) return 'Connect in flight…'
  return connection.status.running
    ? 'Connected — click to disconnect'
    : 'Click to connect the active profile'
})

/** The Chain hub is an expert surface: its page is the only place the
 *  legs are described together, so it is hidden in Simple mode. */
const isAdvancedMode = computed(() => props.mode === 'advanced')
const navItems: { key: NavKey; label: string; icon: IconName; advancedOnly?: boolean }[] = [
  { key: 'home', label: 'Home', icon: 'home' },
  { key: 'proxies', label: 'Proxies', icon: 'servers' },
  { key: 'subscription', label: 'Subscription', icon: 'cloud' },
  { key: 'sni', label: 'SNI Spoofing', icon: 'shield' },
  { key: 'aether', label: 'Aether', icon: 'cloud' },
  { key: 'chain', label: 'Chain', icon: 'layers', advancedOnly: true },
  { key: 'settings', label: 'Settings', icon: 'gear' },
  { key: 'console', label: 'Console', icon: 'terminal' },
  { key: 'about', label: 'About', icon: 'info' },
]

const items = computed(() =>
  navItems.filter((i) => !i.advancedOnly || isAdvancedMode.value),
)
const modeLabel = computed(() => (isAdvancedMode.value ? 'Simple Mode' : 'Advanced Mode'))
const modeTitle = computed(() =>
  isAdvancedMode.value ? 'Switch back to Simple mode' : 'Switch to the expert workspace',
)

// Keep the live status dot fresh while the sidebar is mounted.
onMounted(() => connection.acquire())
onUnmounted(() => connection.release())

function toggleTheme() {
  void settings.setTheme(isDark.value ? 'light' : 'dark')
}

/** Mode switch: on means Advanced. Entering is saved here; leaving is
 *  confirmed by the shell. */
function toggleMode() {
  if (isAdvancedMode.value) {
    emit('exit')
    return
  }
  void settings.setAdvancedMode(true)
}

/** Same toggle as the Home power button / Advanced status dot: never
 *  fires while a connect is in flight; persists selection first. */
async function toggleConn() {
  if (connection.busy || connection.connectBusy) return
  if (connection.status.running) {
    await connection.disconnect()
    return
  }
  const id = settings.settings.activeProfileId || profiles.profiles[0]?.id
  if (!id) return
  try {
    await settings.setActiveProfileId(id)
  } catch {
    return
  }
  await connection.connect(id)
}
</script>
