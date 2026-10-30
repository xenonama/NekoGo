<!-- Bottom app info card of the right panel. -->
<template>
  <button class="nb-appinfo" @click="$emit('open')">
    <img class="avatar-img" :src="appIcon" alt="NekoGo" />
    <span class="meta">
      <div class="t">NekoGo</div>
      <div class="v">v{{ version }}</div>
    </span>
    <span class="nb-chev"><Icon name="chevron" :size="18" /></span>
  </button>
</template>

<script setup lang="ts">
import Icon from './Icon.vue'
import appIcon from '../assets/app.png'

defineProps<{ version: string }>()
defineEmits<{ (e: 'open'): void }>()
</script>
