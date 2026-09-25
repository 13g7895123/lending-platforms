<script setup lang="ts">
const { user, isAuthenticated, isBorrower, isInvestor, isReviewer, logout } = useAuth()
const { showToast } = useToast()
const { clearDashboard } = useDashboard()
const { clearAdminData } = useAdmin()
const { clearPortfolio } = useInvestor()

const route = useRoute()
const menuOpen = ref(false)

interface NavItem {
  to: string
  label: string
  icon: string
  /** 限定角色；未指定表示公開或只需登入 */
  roles?: ('borrower' | 'investor' | 'reviewer')[]
  requiresAuth?: boolean
}

const navItems: NavItem[] = [
  { to: '/', label: '首頁', icon: '⌂' },
  { to: '/dashboard', label: '我的儀表板', icon: '▦', roles: ['borrower'] },
  { to: '/apply', label: '申請貸款', icon: '＋', roles: ['borrower'] },
  { to: '/repay', label: '還款管理', icon: '↻', roles: ['borrower'] },
  { to: '/market', label: '投資市集', icon: '◈' },
  { to: '/portfolio', label: '我的投資', icon: '◎', roles: ['investor'] },
  { to: '/admin', label: '風控後台', icon: '⌁', roles: ['reviewer'] },
]

// 每個角色只看到與自己相關的項目，避免點進去才被守衛導走
const visibleNavItems = computed(() =>
  navItems.filter((item) => {
    if (item.roles) {
      if (!user.value) return false
      return item.roles.includes(user.value.role)
    }
    if (item.requiresAuth) return isAuthenticated.value
    return true
  }),
)

/** 出借人在導覽列直接看到可用餘額。 */
const balanceLabel = computed(() =>
  isInvestor.value ? `餘額 ${formatNT(user.value?.availableBalance ?? 0)}` : '',
)

const roleLabel = computed(() => {
  if (isReviewer.value) return '風控人員'
  if (isInvestor.value) return balanceLabel.value
  if (isBorrower.value) return `信用評分 ${user.value?.creditScore ?? 0}`
  return ''
})

// 子路由（如 /repay/LN-123）也要讓父項目呈現選取狀態
function isActive(path: string) {
  if (path === '/') return route.path === '/'
  return route.path === path || route.path.startsWith(`${path}/`)
}

// 換頁後收起行動版選單
watch(() => route.fullPath, () => {
  menuOpen.value = false
})

async function signOut() {
  await logout()
  clearDashboard()
  clearAdminData()
  clearPortfolio()
  showToast('已登出', 'info')
  await navigateTo('/')
}
</script>

<template>
  <header class="site-header">
    <div class="container nav-bar">
      <NuxtLink class="brand" to="/" aria-label="回到首頁">
        <span class="brand-mark">₵</span>
        <span>CreditFlow <small>信達金融</small></span>
      </NuxtLink>

      <button
        class="menu-toggle"
        type="button"
        aria-label="開啟選單"
        :aria-expanded="menuOpen"
        @click="menuOpen = !menuOpen"
      >
        ☰
      </button>

      <nav class="main-nav" :class="{ open: menuOpen }" aria-label="主要導覽">
        <NuxtLink
          v-for="item in visibleNavItems"
          :key="item.to"
          :to="item.to"
          :class="{ active: isActive(item.to) }"
          :aria-current="isActive(item.to) ? 'page' : undefined"
        >
          <span class="nav-icon">{{ item.icon }}</span>{{ item.label }}
        </NuxtLink>
      </nav>

      <div v-if="user" class="user-box">
        <div class="avatar">{{ user.displayName.slice(0, 1) }}</div>
        <div>
          <strong>{{ user.displayName }}</strong>
          <small>{{ roleLabel }}</small>
        </div>
        <button class="btn small" type="button" @click="signOut">登出</button>
      </div>
      <div v-else class="user-box">
        <NuxtLink class="btn small" to="/login">登入</NuxtLink>
        <NuxtLink class="btn primary small" to="/login?mode=register">註冊</NuxtLink>
      </div>
    </div>
  </header>
</template>
