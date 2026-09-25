/** 出借人專屬頁面守衛。 */
export default defineNuxtRouteMiddleware(async (to) => {
  const { user, ready, refresh } = useAuth()

  if (!ready.value) {
    await refresh()
  }

  if (!user.value) {
    return navigateTo({ path: '/login', query: { redirect: to.fullPath } })
  }

  if (user.value.role !== 'investor') {
    // 借款人導回自己的儀表板，風控導回後台
    return navigateTo(user.value.role === 'reviewer' ? '/admin' : '/dashboard')
  }
})
