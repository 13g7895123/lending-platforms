import type { SessionUser } from '~/types/api'

/**
 * 全站共用的登入狀態。
 *
 * session 存在 HttpOnly cookie 裡，前端讀不到也不該讀；
 * 這裡只快取 /v1/auth/me 回傳的使用者資料供 UI 判斷角色。
 */
export function useAuth() {
  const user = useState<SessionUser | null>('auth-user', () => null)
  const ready = useState<boolean>('auth-ready', () => false)

  const isAuthenticated = computed(() => user.value !== null)
  const isBorrower = computed(() => user.value?.role === 'borrower')
  const isInvestor = computed(() => user.value?.role === 'investor')
  const isReviewer = computed(() => user.value?.role === 'reviewer')

  /**
   * 取得目前登入者。
   * 以 apiFetch 呼叫，確保 SSR 階段也會帶上 cookie——
   * 否則路由守衛在伺服器端看不到 session，已登入者會被錯誤導向登入頁。
   */
  async function refresh() {
    try {
      user.value = await apiFetch<SessionUser>('/api/v1/auth/me')
    } catch {
      user.value = null
    } finally {
      ready.value = true
    }
  }

  async function login(email: string, password: string) {
    user.value = await $fetch<SessionUser>('/api/v1/auth/login', {
      method: 'POST',
      body: { email, password },
    })
    return user.value
  }

  /** 註冊。role 可選 borrower 或 investor；其他值後端一律降級為 borrower。 */
  async function register(
    email: string,
    password: string,
    displayName: string,
    role: 'borrower' | 'investor' = 'borrower',
  ) {
    user.value = await $fetch<SessionUser>('/api/v1/auth/register', {
      method: 'POST',
      body: { email, password, displayName, role },
    })
    return user.value
  }

  async function logout() {
    const { mutate } = useCsrf()
    try {
      // 登出會改變狀態，需帶 CSRF token（login/register 為豁免端點）
      await mutate('/api/v1/auth/logout', { method: 'POST' })
    } finally {
      user.value = null
    }
  }

  return {
    user, ready,
    isAuthenticated, isBorrower, isInvestor, isReviewer,
    refresh, login, register, logout,
  }
}

/** 從 $fetch 的錯誤中取出後端的 error 訊息，取不到時回傳通用文案。 */
export function apiErrorMessage(error: unknown, fallback: string) {
  const data = (error as { data?: { error?: string } } | undefined)?.data
  return data?.error || fallback
}
