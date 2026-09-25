<script setup lang="ts">
const { modal, busy, inputValue, inputError, closeModal, confirmModal } = useModal()

const kicker = computed(() => {
  switch (modal.value?.kind) {
    case 'bid': return 'PLACE BID'
    case 'payment': return 'PAYMENT'
    case 'prepay': return 'EARLY PAYOFF'
    case 'review': return 'CREDIT DECISION'
    default: return 'DETAIL'
  }
})

// Esc 關閉：對話框是覆蓋層，必須能以鍵盤離開
function onKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape' && modal.value && !busy.value) closeModal()
}

onMounted(() => window.addEventListener('keydown', onKeydown))
onBeforeUnmount(() => window.removeEventListener('keydown', onKeydown))
</script>

<template>
  <div v-if="modal" class="modal-backdrop" role="presentation" @click.self="closeModal">
    <div class="modal-card" role="dialog" aria-modal="true" :aria-label="modal.title">
      <button class="modal-close" type="button" aria-label="關閉視窗" @click="closeModal">×</button>
      <span class="section-kicker">{{ kicker }}</span>
      <h2>{{ modal.title }}</h2>
      <p>{{ modal.description }}</p>

      <div v-if="modal.rows.length" class="review-grid modal-rows">
        <div v-for="row in modal.rows" :key="row.label">
          <span>{{ row.label }}</span>
          <strong :class="row.tone">{{ row.value }}</strong>
        </div>
      </div>

      <div v-if="modal.input" class="field note-field">
        <label for="modal-input">
          {{ modal.input.label }}{{ modal.input.required ? '' : '（選填）' }}
        </label>
        <textarea
          id="modal-input"
          v-model="inputValue"
          rows="3"
          :placeholder="modal.input.placeholder"
        />
        <p v-if="inputError" class="form-error" role="alert">{{ inputError }}</p>
      </div>

      <div class="modal-actions">
        <button class="btn" type="button" :disabled="busy" @click="closeModal">取消</button>
        <button class="btn primary" type="button" :disabled="busy" @click="confirmModal">
          {{ busy ? '處理中…' : modal.confirmLabel || '知道了' }}
        </button>
      </div>
    </div>
  </div>
</template>
