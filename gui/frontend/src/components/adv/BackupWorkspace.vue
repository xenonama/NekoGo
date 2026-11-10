<!-- Advanced Backup: BackupPanel's own four cards, flat. The per-card
     wrappers this replaced each repeated the card's own head ("Export"
     above "Export all data"), which cost four duplicate titles and
     ~500px of scroll; the "connect a profile" hint lives inside the
     raw-config card, so nothing here restates it. -->
<template>
  <BackupPanel />
</template>

<script setup lang="ts">
import BackupPanel from '../BackupPanel.vue'
</script>
