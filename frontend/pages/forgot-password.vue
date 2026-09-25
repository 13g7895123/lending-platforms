<script setup lang="ts">
useHead({ title: '忘記密碼 · CreditFlow' })

const email = ref('')
const busy = ref(false)
const sent = ref(false)

async function submit() {
  busy.value = true
  try {
    await $fetch('/api/v1/auth/forgot-password', {
      method: 'POST',
      body: { email: email.value },
    })
  } catch {
    // 不論結果都顯示相同訊息：回報「查無此帳號」會變成帳號列舉工具
  } finally {
    busy.value = false
    sent.value = true
  }
}
</script>

<template>
  <section class="page active-page">
    <div class="container narrow-page">
      <div class="page-intro">
        <div>
          <span class="section-kicker">PASSWORD RESET</span>
          <h1>忘記密碼</h1>
          <p>輸入註冊時使用的電子信箱，我們會寄出重設連結</p>
        </div>
      </div>

      <article class="surface-card form-card">
        <template v-if="sent">
          <span class="success-icon">✓</span>
          <h2>請查看你的信箱</h2>
          <p>若該信箱已註冊，重設連結將於數分鐘內寄達。連結有效 1 小時。</p>
          <div class="form-actions end">
            <NuxtLink class="btn" to="/login">回到登入</NuxtLink>
          </div>
        </template>

        <form v-else class="form-grid" @submit.prevent="submit">
          <div class="field full-field">
            <label for="forgot-email">電子信箱</label>
            <input id="forgot-email" v-model="email" type="email" autocomplete="email" required>
          </div>
          <div class="form-actions end full-field">
            <NuxtLink class="btn" to="/login">取消</NuxtLink>
            <button class="btn primary" type="submit" :disabled="busy">
              {{ busy ? '寄送中…' : '寄送重設連結' }}
            </button>
          </div>
        </form>
      </article>
    </div>
  </section>
</template>
