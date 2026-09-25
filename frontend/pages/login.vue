<script setup lang="ts">
const route = useRoute()
const { user, isInvestor, isReviewer, login, register } = useAuth()
const { showToast } = useToast()
const { loadDashboard, loadApplications } = useDashboard()

useHead({ title: '登入 · CreditFlow' })

// ?mode=register 讓導覽列的「註冊」直接開在註冊表單
const mode = ref<'login' | 'register'>(route.query.mode === 'register' ? 'register' : 'login')
const form = reactive({
  email: '',
  password: '',
  displayName: '',
  role: 'borrower' as 'borrower' | 'investor',
})
const busy = ref(false)
const errorMessage = ref('')
// 帳號被鎖定時顯示的剩餘等待秒數
const retryAfter = ref(0)

let countdownTimer: ReturnType<typeof setInterval> | undefined

function startCountdown(seconds: number) {
  retryAfter.value = seconds
  if (countdownTimer) clearInterval(countdownTimer)
  countdownTimer = setInterval(() => {
    retryAfter.value -= 1
    if (retryAfter.value <= 0 && countdownTimer) {
      clearInterval(countdownTimer)
      countdownTimer = undefined
    }
  }, 1000)
}

onBeforeUnmount(() => {
  if (countdownTimer) clearInterval(countdownTimer)
})

/** 把秒數轉成易讀的等待時間。 */
const retryLabel = computed(() => {
  const seconds = retryAfter.value
  if (seconds <= 0) return ''
  if (seconds < 60) return `約 ${seconds} 秒`
  const minutes = Math.ceil(seconds / 60)
  if (minutes < 60) return `約 ${minutes} 分鐘`
  return `約 ${Math.ceil(minutes / 60)} 小時`
})

/** 依角色決定登入後的落地頁。 */
function landingPage() {
  if (isReviewer.value) return '/admin'
  if (isInvestor.value) return '/portfolio'
  return '/dashboard'
}

// 已登入者不需停留在此頁
onMounted(() => {
  if (user.value) {
    void navigateTo(landingPage(), { replace: true })
  }
})

function switchMode(next: 'login' | 'register') {
  mode.value = next
  errorMessage.value = ''
}

async function submit() {
  errorMessage.value = ''
  busy.value = true
  try {
    if (mode.value === 'login') {
      await login(form.email, form.password)
    } else {
      await register(form.email, form.password, form.displayName, form.role)
    }
    form.password = ''
    showToast(`歡迎回來，${user.value?.displayName ?? ''}`)

    // 載入登入後需要的資料，避免目標頁閃一下空狀態
    await Promise.all([loadDashboard(), loadApplications()])

    const redirect = typeof route.query.redirect === 'string' ? route.query.redirect : ''
    await navigateTo(redirect || landingPage(), { replace: true })
  } catch (error) {
    errorMessage.value = apiErrorMessage(
      error,
      mode.value === 'login' ? '登入失敗，請確認帳號密碼' : '註冊失敗，請稍後再試',
    )
    // 429 代表嘗試過於頻繁；Retry-After 告訴我們何時可再試
    const response = (error as { response?: { status?: number; headers?: Headers } }).response
    if (response?.status === 429) {
      const header = response.headers?.get('retry-after')
      startCountdown(Number(header) || 300)
    }
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
          <span class="section-kicker">{{ mode === 'login' ? 'SIGN IN' : 'CREATE ACCOUNT' }}</span>
          <h1>{{ mode === 'login' ? '登入 CreditFlow' : '建立你的帳號' }}</h1>
          <p>
            {{ mode === 'login'
              ? '登入後即可查看合約、申請貸款與追蹤還款'
              : '註冊後將以借款人身分開通服務' }}
          </p>
        </div>
        <span class="secure-label">⌁ 連線已加密</span>
      </div>

      <article class="surface-card form-card">
        <form class="form-grid" @submit.prevent="submit">
          <template v-if="mode === 'register'">
            <div class="field full-field">
              <label for="auth-name">姓名</label>
              <input id="auth-name" v-model="form.displayName" autocomplete="name" required>
            </div>

            <div class="field full-field">
              <label for="auth-role">我要</label>
              <select id="auth-role" v-model="form.role">
                <option value="borrower">借款 —— 申請貸款、管理還款</option>
                <option value="investor">出借 —— 投資標的、賺取利息</option>
              </select>
              <small class="field-hint">
                {{ form.role === 'investor'
                  ? '出借人可在投資市集投標已通過授信的標的'
                  : '借款人可申請貸款，核准後由出借人募資撥款' }}
              </small>
            </div>
          </template>

          <div class="field full-field">
            <label for="auth-email">電子信箱</label>
            <input id="auth-email" v-model="form.email" type="email" autocomplete="email" required>
          </div>

          <div class="field full-field">
            <label for="auth-password">密碼</label>
            <input
              id="auth-password"
              v-model="form.password"
              type="password"
              :autocomplete="mode === 'login' ? 'current-password' : 'new-password'"
              minlength="8"
              required
            >
            <small v-if="mode === 'register'" class="field-hint">至少 8 個字元</small>
          </div>

          <p v-if="errorMessage" class="form-error" role="alert">
            {{ errorMessage }}
            <template v-if="retryAfter > 0">
              <br>請於 {{ retryLabel }} 後再試，或
              <NuxtLink class="link-button" to="/forgot-password">重設密碼</NuxtLink>
              以立即解除。
            </template>
          </p>

          <div class="form-actions end full-field">
            <NuxtLink class="btn" to="/">取消</NuxtLink>
            <button class="btn primary" type="submit" :disabled="busy || retryAfter > 0">
              {{ busy ? '處理中…' : mode === 'login' ? '登入' : '註冊並登入' }}
            </button>
          </div>
        </form>

        <p v-if="mode === 'login'" class="auth-switch">
          <NuxtLink class="link-button" to="/forgot-password">忘記密碼？</NuxtLink>
        </p>

        <p class="auth-switch">
          <template v-if="mode === 'login'">
            還沒有帳號？<button type="button" class="link-button" @click="switchMode('register')">立即註冊</button>
          </template>
          <template v-else>
            已經有帳號了？<button type="button" class="link-button" @click="switchMode('login')">前往登入</button>
          </template>
        </p>

        <div class="demo-hint">
          <strong>展示帳號</strong>
          <span>借款人：demo@creditflow.test / demo1234</span>
          <span>出借人：investor@creditflow.test / invest1234</span>
          <span>風控員：reviewer@creditflow.test / review1234</span>
        </div>
      </article>
    </div>
  </section>
</template>
