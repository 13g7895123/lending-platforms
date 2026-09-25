<script setup lang="ts">
const route = useRoute()
const { user, refresh } = useAuth()
const { showToast } = useToast()

useHead({ title: '驗證電子信箱 · CreditFlow' })

type State = 'verifying' | 'success' | 'failed' | 'missing'

const state = ref<State>('missing')
const message = ref('')
const resending = ref(false)

// token 來自信件連結；沒有 token 時顯示說明而非空白畫面
const token = computed(() => String(route.query.token ?? ''))

onMounted(async () => {
  if (!token.value) {
    state.value = 'missing'
    return
  }
  state.value = 'verifying'
  try {
    await $fetch('/api/v1/auth/verify-email', {
      method: 'POST',
      body: { token: token.value },
    })
    state.value = 'success'
    // 更新 session 中的驗證狀態，導覽列與申請頁才會同步
    await refresh()
  } catch (error) {
    state.value = 'failed'
    message.value = apiErrorMessage(error, '驗證失敗，請重新索取驗證信')
  }
})

async function resend() {
  resending.value = true
  try {
    const { mutate } = useCsrf()
    await mutate('/api/v1/auth/resend-verification', { method: 'POST' })
    showToast('驗證信已重新寄出，請查看信箱')
  } catch (error) {
    showToast(apiErrorMessage(error, '重寄失敗'), 'error')
  } finally {
    resending.value = false
  }
}
</script>

<template>
  <section class="page active-page">
    <div class="container narrow-page">
      <div class="page-intro">
        <div>
          <span class="section-kicker">EMAIL VERIFICATION</span>
          <h1>驗證電子信箱</h1>
        </div>
      </div>

      <article class="surface-card form-card">
        <div v-if="state === 'verifying'" class="empty-state">正在驗證…</div>

        <template v-else-if="state === 'success'">
          <span class="success-icon">✓</span>
          <h2>信箱驗證完成</h2>
          <p>你現在可以送出貸款申請了。</p>
          <div class="form-actions end">
            <NuxtLink class="btn primary" to="/apply">開始申請貸款</NuxtLink>
          </div>
        </template>

        <template v-else-if="state === 'failed'">
          <h2>驗證連結無法使用</h2>
          <p class="form-error" role="alert">{{ message }}</p>
          <p class="card-note">連結可能已過期（24 小時）或已被使用過。</p>
          <div class="form-actions end">
            <NuxtLink v-if="!user" class="btn" to="/login">前往登入</NuxtLink>
            <button v-else class="btn primary" type="button" :disabled="resending" @click="resend">
              {{ resending ? '寄送中…' : '重新寄送驗證信' }}
            </button>
          </div>
        </template>

        <template v-else>
          <h2>缺少驗證連結</h2>
          <p>請從我們寄給你的驗證信中點擊連結。</p>
          <div class="form-actions end">
            <button v-if="user" class="btn primary" type="button" :disabled="resending" @click="resend">
              {{ resending ? '寄送中…' : '寄送驗證信' }}
            </button>
            <NuxtLink v-else class="btn" to="/login">前往登入</NuxtLink>
          </div>
        </template>
      </article>
    </div>
  </section>
</template>
