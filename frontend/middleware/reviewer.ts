/** 風控後台守衛：必須登入且角色為 reviewer。 */
export default defineNuxtRouteMiddleware(async (to) => {
  const { user, ready, refresh } = useAuth()

  if (!ready.value) {
    await refresh()
  }

  if (!user.value) {
    return navigateTo({ path: '/login', query: { redirect: to.fullPath } })
  }

  if (user.value.role !== 'reviewer') {
    // 導回儀表板而非顯示錯誤頁：borrower 沒有理由停留在這個 URL
    return navigateTo('/dashboard')
  }
})
