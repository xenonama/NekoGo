<!-- Rounded search input with icon + clear button. -->
<template>
  <div class="nb-search">
    <Icon name="search" :size="16" />
    <input :value="modelValue" :placeholder="placeholder ?? 'Search name or address'" @input="$emit('update:modelValue', ($event.target as HTMLInputElement).value)" />
    <button
      v-if="modelValue"
      class="nb-clear"
      title="Clear search"
      aria-label="Clear search"
      @click="$emit('update:modelValue', '')"
    >
      <Icon name="x" :size="14" />
    </button>
  </div>
</template>

<script setup lang="ts">
import Icon from './Icon.vue'

defineProps<{ modelValue: string; placeholder?: string }>()
defineEmits<{ (e: 'update:modelValue', v: string): void }>()
</script>
