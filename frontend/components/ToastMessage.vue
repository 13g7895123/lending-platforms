<script setup lang="ts">
const { toast } = useToast()

const icon = computed(() => {
  if (!toast.value) return ''
  return toast.value.kind === 'success' ? '✓' : toast.value.kind === 'error' ? '!' : 'i'
})
</script>

<template>
  <Transition name="toast">
    <div v-if="toast" class="toast-message" :class="toast.kind" role="status" aria-live="polite">
      <span aria-hidden="true">{{ icon }}</span>{{ toast.message }}
    </div>
  </Transition>
</template>
