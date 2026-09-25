<script setup lang="ts">
const route = useRoute()
const { showToast } = useToast()

useHead({ title: '重設密碼 · CreditFlow' })

const token = computed(() => String(route.query.token ?? ''))
const password = ref('')
const confirmation = ref('')
const busy = ref(false)
const errorMessage = ref('')
const done = ref(false)

async function submit() {
  errorMessage.value = ''
  if (password.value !== confirmation.value) {
    errorMessage.value = '兩次輸入的密碼不一致'
    return
  }
  busy.value = true
  try {
    await $fetch('/api/v1/auth/reset-password', {
      method: 'POST',
      body: { token: token.value, password: password.value },
    })
    done.value = true
    showToast('密碼已更新，請以新密碼登入')
  } catch (error) {
    errorMessage.value = apiErrorMessage(error, '重設失敗，連結可能已失效')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <section class="page active-page">
    <div class="container narrow-page">
      <div class="page-intro">
        <div>
          <span class="section-kicker">PASSWORD RESET</span>
          <h1>設定新密碼</h1>
        </div>
      </div>

      <article class="surface-card form-card">
        <template v-if="done">
          <span class="success-icon">✓</span>
          <h2>密碼已更新</h2>
          <p>所有裝置上的登入狀態都已登出，請以新密碼重新登入。</p>
          <div class="form-actions end">
            <NuxtLink class="btn primary" to="/login">前往登入</NuxtLink>
          </div>
        </template>

        <template v-else-if="!token">
          <h2>缺少重設連結</h2>
          <p>請從我們寄給你的重設信中點擊連結。</p>
          <div class="form-actions end">
            <NuxtLink class="btn" to="/forgot-password">重新索取</NuxtLink>
          </div>
        </template>

        <form v-else class="form-grid" @submit.prevent="submit">
          <div class="field full-field">
            <label for="reset-password">新密碼</label>
            <input
              id="reset-password"
              v-model="password"
              type="password"
              autocomplete="new-password"
              minlength="8"
              required
            >
            <small class="field-hint">至少 8 個字元</small>
          </div>
          <div class="field full-field">
            <label for="reset-confirm">再次輸入新密碼</label>
            <input
              id="reset-confirm"
              v-model="confirmation"
              type="password"
              autocomplete="new-password"
              minlength="8"
              required
            >
          </div>

          <p v-if="errorMessage" class="form-error" role="alert">{{ errorMessage }}</p>

          <p class="card-note full-field">
            重設後所有裝置都會被登出。連結僅能使用一次。
          </p>

          <div class="form-actions end full-field">
            <NuxtLink class="btn" to="/login">取消</NuxtLink>
            <button class="btn primary" type="submit" :disabled="busy">
              {{ busy ? '更新中…' : '更新密碼' }}
            </button>
          </div>
        </form>
      </article>
    </div>
  </section>
</template>
