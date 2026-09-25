import type { ApplicationSummary, DashboardResponse, Loan, Paginated } from '~/types/api'

/**
 * 儀表板資料：合約清單與摘要。
 *
 * 狀態放在 useState，讓還款頁與儀表板共用同一份合約清單，
 * 繳款後只需重載一次即可讓兩頁同步。
 */
export function useDashboard() {
  const loans = useState<Loan[]>('dashboard-loans', () => [])
  const summary = useState('dashboard-summary', () => ({
    totalBorrowed: 0,
    monthlyPayment: 0,
    creditScore: 0,
    repaymentRate: 0,
  }))
  const applications = useState<ApplicationSummary[]>('my-applications', () => [])
  const loading = useState('dashboard-loading', () => false)

  /** 未結清的合約，還款頁只處理這些。 */
  const repayableLoans = computed(() => loans.value.filter((loan) => loan.status !== '已結清'))

  async function loadDashboard() {
    loading.value = true
    try {
      const response = await apiFetch<DashboardResponse>('/api/v1/dashboard')
      loans.value = response.loans ?? []
      if (response.summary) Object.assign(summary.value, response.summary)
    } finally {
      loading.value = false
    }
  }

  async function loadApplications() {
    try {
      const response = await apiFetch<Paginated<ApplicationSummary>>('/api/v1/applications')
      applications.value = response.items
    } catch {
      applications.value = []
    }
  }

  function clearDashboard() {
    loans.value = []
    applications.value = []
    Object.assign(summary.value, {
      totalBorrowed: 0,
      monthlyPayment: 0,
      creditScore: 0,
      repaymentRate: 0,
    })
  }

  return {
    loans,
    summary,
    applications,
    loading,
    repayableLoans,
    loadDashboard,
    loadApplications,
    clearDashboard,
  }
}

/** 依信用評分決定等級與參考利率，與後端 gradeForScore 一致。 */
export function gradeForScore(score: number): { grade: 'A' | 'B' | 'C'; rate: number } {
  if (score >= 750) return { grade: 'A', rate: 4.88 }
  if (score >= 650) return { grade: 'B', rate: 6.8 }
  return { grade: 'C', rate: 9.6 }
}
