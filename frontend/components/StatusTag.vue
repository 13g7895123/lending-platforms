<script setup lang="ts">
import type { Tone } from '~/types/api'

defineProps<{
  label: string
  tone?: Tone
}>()
</script>

<template>
  <span class="tag" :class="tone ?? 'info'">{{ label }}</span>
</template>
