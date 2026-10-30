<!-- About tab: program identity page (hero, links, advanced, credits). -->
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

    <!-- Card 2 — Links -->
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

    <!-- Card 3 — Advanced (Phase A shell; real tools land in Phases B-F) -->
    <div v-if="settings.settings.advancedMode" class="about-card">
      <div class="about-title">Advanced</div>
      <button class="about-link" @click="openFolder">
        <Icon name="folder" :size="20" />
        <span class="about-link-meta">
          <span class="about-link-label">Open config folder</span>
          <span class="about-link-hint">backend.openConfigFolder()</span>
        </span>
        <Icon name="chevron" :size="18" class="about-link-chev" />
      </button>
      <button class="about-link" @click="openFolder">
        <Icon name="folder" :size="20" />
        <span class="about-link-meta">
          <span class="about-link-label">Open logs folder</span>
          <span class="about-link-hint">Logs live next to config; split later</span>
        </span>
        <Icon name="chevron" :size="18" class="about-link-chev" />
      </button>
      <button class="about-link" disabled title="Phase F">
        <Icon name="download" :size="20" />
        <span class="about-link-meta">
          <span class="about-link-label">Export all data</span>
          <span class="about-link-hint">Phase F</span>
        </span>
      </button>
      <button class="about-link" disabled title="Phase F">
        <Icon name="trash" :size="20" />
        <span class="about-link-meta">
          <span class="about-link-label">Reset to defaults</span>
          <span class="about-link-hint">Phase F</span>
        </span>
      </button>
    </div>

    <!-- Card 4 — Credits -->
    <div class="about-card">
      <div class="about-title">Credits</div>
      <ul class="about-credits">
        <li v-for="c in credits" :key="c.url">
          <button class="about-credit" @click="open(c.url)">{{ c.name }}</button>
        </li>
      </ul>
    </div>

    <!-- Card 5 — Footer -->
    <p class="about-footer">
      NekoGo is not affiliated with NekoBox, sing-box, or Xray.
      Use at your own risk.
    </p>
  </div>
</template>

<script setup lang="ts">
import Icon, { type IconName } from './Icon.vue'
import appIcon from '../assets/app.png'
import { backend } from '../api'
import { useSettingsStore } from '../stores/settings'

defineProps<{ version: string }>()
const settings = useSettingsStore()

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
