/**
 * CSRF double-submit token。
 *
 * 後端下發一個可被 JS 讀取的 creditflow_csrf cookie，
 * 我們把它的值放進 X-CSRF-Token header 一併送出。
 * 跨站頁面因同源政策讀不到 cookie，因此無法偽造相符的 header。
 */
const CSRF_COOKIE = 'creditflow_csrf'

function readCookie(name: string): string {
  if (!import.meta.client) return ''
  const match = document.cookie.match(new RegExp(`(?:^|; )${name}=([^;]*)`))
  return match ? decodeURIComponent(match[1]) : ''
}

export function useCsrf() {
  /** 取得 token；cookie 尚未存在時先向後端索取。 */
  async function ensureToken(): Promise<string> {
    const existing = readCookie(CSRF_COOKIE)
    if (existing) return existing
    try {
      const response = await $fetch<{ csrfToken: string }>('/api/v1/auth/csrf')
      // 後端已用 Set-Cookie 下發，這裡回傳的值供本次請求直接使用
      return response.csrfToken
    } catch {
      return ''
    }
  }

  /**
   * 發送會改變狀態的請求（POST/PATCH 等），自動附上 CSRF header。
   * GET 不需要 token，直接用 $fetch 即可。
   */
  async function mutate<T>(url: string, options: Record<string, unknown> = {}): Promise<T> {
    const token = await ensureToken()
    const headers = { ...(options.headers as Record<string, string> | undefined) }
    if (token) headers['X-CSRF-Token'] = token
    return await $fetch<T>(url, { ...options, headers })
  }

  return { ensureToken, mutate }
}
