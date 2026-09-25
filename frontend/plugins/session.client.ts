/**
 * 應用啟動時取得登入狀態與 CSRF token。
 *
 * 只在 client 執行：session 與 CSRF 都存在 cookie，
 * SSR 階段取得的狀態無法安全地序列化給瀏覽器重用。
 */
export default defineNuxtPlugin(async () => {
  const { refresh } = useAuth()
  const { ensureToken } = useCsrf()

  // 並行取得，兩者互不相依
  await Promise.all([refresh(), ensureToken()])
})
