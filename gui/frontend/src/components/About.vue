<!-- About tab: program identity page (hero, version stats, links,
     advanced, credits). -->
<template>
  <div class="about">
    <!-- Card 1 — Hero / Identity -->
    <div class="about-hero">
      <img class="about-hero-icon" :src="appIcon" alt="NekoGo" />
      <div class="about-hero-text">
        <h2>NekoGo</h2>
        <p>A fork of NekoBox with a new GUI (Go + Wails3 + Vue 3).</p>
        <p class="about-version">Version {{ version }}</p>
      </div>
    </div>

    <!-- Card 2 — Version stats. The bottom-right app card shows the same
         app version; this adds the engines, all read from the backend so
         neither place can drift from the running build. sing-box and
         NekoBox are separate rows because they are separate binaries:
         the fork is stamped with the NekoGo build version, so only the
         official sing-box.exe reports a sing-box version. -->
    <div class="about-card">
      <div class="about-title">Version stats</div>
      <dl v-if="statRows.length" class="about-stats">
        <template v-for="row in statRows" :key="row.label">
          <dt>{{ row.label }}</dt>
          <dd :title="row.value">{{ row.value }}</dd>
        </template>
      </dl>
      <p v-else class="about-stats-wait">Reading versions…</p>
      <button class="nb-act nb-act--ghost" :disabled="!versions" @click="copyDiag">
        <Icon name="copy" :size="14" />{{ copyLabel }}
      </button>
    </div>

    <!-- Card 3 — Links -->
    <div class="about-card">
      <div class="about-title">Links</div>
      <button
        v-for="l in links"
        :key="l.url"
        class="about-link"
        @click="open(l.url)"
      >
        <Icon :name="l.icon" :size="20" />
        <span class="about-link-meta">
          <span class="about-link-label">{{ l.label }}</span>
          <span class="about-link-hint">{{ l.url }}</span>
        </span>
        <Icon name="chevron" :size="18" class="about-link-chev" />
      </button>
    </div>

    <!-- Card 4 — Advanced. The export/reset entries this used to carry
         were disabled "Phase F" placeholders; both shipped under
         Settings → Backup & reset, so pointing at them from here would
         only duplicate a live feature with a dead button. -->
    <div v-if="settings.settings.advancedMode" class="about-card">
      <div class="about-title">Advanced</div>
      <button class="about-link" @click="openFolder">
        <Icon name="folder" :size="20" />
        <span class="about-link-meta">
          <span class="about-link-label">Open config folder</span>
          <span class="about-link-hint">The folder holding config.json</span>
        </span>
        <Icon name="chevron" :size="18" class="about-link-chev" />
      </button>
    </div>

    <!-- Card 5 — Credits -->
    <div class="about-card">
      <div class="about-title">Credits</div>
      <ul class="about-credits">
        <li v-for="c in credits" :key="c.url">
          <button class="about-credit" @click="open(c.url)">{{ c.name }}</button>
        </li>
      </ul>
    </div>

    <!-- Card 6 — Footer -->
    <p class="about-footer">
      NekoGo is not affiliated with NekoBox, sing-box, or Xray.
      Use at your own risk.
    </p>
  </div>
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import Icon, { type IconName } from './Icon.vue'
import appIcon from '../assets/app.png'
import { backend } from '../api'
import { useSettingsStore } from '../stores/settings'
import { useConnectionStore } from '../stores/connection'
import { collectVersions, formatDiagText, type DiagVersions } from '../diag'
import { copyText } from '../clipboard'

const props = defineProps<{ version: string }>()
const settings = useSettingsStore()
const connection = useConnectionStore()

const versions = ref<DiagVersions | null>(null)
const copyState = ref<'' | 'ok' | 'fail'>('')
const copyLabel = computed(() =>
  copyState.value === 'ok' ? 'Copied' : copyState.value === 'fail' ? 'Copy failed' : 'Copy diagnostics',
)
let copyTimer = 0
onUnmounted(() => window.clearTimeout(copyTimer))

// A running core is the truth; idle, the configured engine is what the
// next Connect will use, which is what a bug report needs to say.
const coreText = computed(() =>
  connection.status.running
    ? connection.status.core
    : `${settings.settings.coreEngine || 'custom'} (idle)`,
)

const statRows = computed(() => {
  const v = versions.value
  if (!v) return []
  return [
    { label: 'NekoGo', value: v.app },
    { label: 'sing-box core', value: v.singBox },
    { label: 'NekoBox core', value: v.nekoBox },
    { label: 'Xray core', value: v.xray },
    { label: 'mihomo core', value: v.mihomo },
    { label: 'Engine', value: v.core },
    { label: 'OS', value: v.os },
  ]
})

async function loadVersions() {
  versions.value = await collectVersions({ version: props.version, coreText: coreText.value })
}

// About can mount before App.vue's version call resolves, and the engine
// changes on connect/disconnect — neither may leave a stale card behind.
watch([() => props.version, coreText], loadVersions, { immediate: true })

async function copyDiag() {
  if (!versions.value) return
  // WebView2 can deny the clipboard without a permission grant; saying
  // nothing would leave the user clicking a button that appears dead.
  copyState.value = (await copyText(formatDiagText(versions.value))) ? 'ok' : 'fail'
  window.clearTimeout(copyTimer)
  copyTimer = window.setTimeout(() => { copyState.value = '' }, 2000)
}

// TODO: make this list configurable (e.g. from settings or a JSON file)
// if the link set ever needs to change without a code edit.
const links: { label: string; url: string; icon: IconName }[] = [
  { label: 'GitHub repository', url: 'https://github.com/xenonama/NekoGo', icon: 'github' },
  { label: 'Report an issue', url: 'https://github.com/xenonama/NekoGo/issues', icon: 'bug' },
  { label: 'Releases', url: 'https://github.com/xenonama/NekoGo/releases', icon: 'tag' },
  { label: 'License (GPL-3.0)', url: 'https://www.gnu.org/licenses/gpl-3.0.html', icon: 'scale' },
]

const credits: { name: string; url: string }[] = [
  { name: 'patterniha', url: 'https://github.com/patterniha' },
  { name: 'CluvexStudio', url: 'https://github.com/CluvexStudio/Aether' },
  { name: 'sing-box', url: 'https://github.com/SagerNet/sing-box' },
  { name: 'Xray-core', url: 'https://github.com/XTLS/Xray-core' },
  { name: 'mihomo', url: 'https://github.com/MetaCubeX/mihomo' },
  { name: 'Wails', url: 'https://wails.io' },
  { name: 'Vue 3', url: 'https://vuejs.org' },
  { name: 'Apache Thrift', url: 'https://thrift.apache.org' },
  { name: 'gRPC', url: 'https://grpc.io' },
]

function open(url: string) {
  backend.openURL(url).catch((e) => console.error(e))
}

function openFolder() {
  backend.openConfigFolder().catch((e) => console.error(e))
}
</script>
