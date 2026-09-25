/**
 * 帶認證的 API 讀取。
 *
 * SSR 階段的 $fetch 不會自動帶上瀏覽器的 cookie，
 * 因此需要認證的請求必須明確轉發傳入請求的 cookie header ——
 * 否則伺服器端渲染時一律視為未登入，畫面會閃一下空狀態或被守衛導走。
 *
 * 變更狀態的請求請改用 useCsrf().mutate()，它只在 client 執行，
 * 不需要這層處理。
 */
export function apiFetch<T>(url: string, options: Record<string, unknown> = {}): Promise<T> {
  const headers = {
    ...(import.meta.server ? useRequestHeaders(['cookie']) : {}),
    ...((options.headers as Record<string, string>) ?? {}),
  }
  return $fetch<T>(url, { ...options, headers })
}
