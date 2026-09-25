/**
 * 需登入頁面的路由守衛。
 *
 * session 只有伺服器端能驗證，故在 client 端先確保已取得使用者資料
 * （首次進入時 plugin 尚未完成或直接以 URL 進入時需補取）。
 */
export default defineNuxtRouteMiddleware(async (to) => {
  const { user, ready, refresh } = useAuth()

  if (!ready.value) {
    await refresh()
  }

  if (!user.value) {
    // 記下原本要去的位置，登入後可導回
    return navigateTo({ path: '/login', query: { redirect: to.fullPath } })
  }
})
