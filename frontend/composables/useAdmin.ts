import type { ApplicationSummary, Loan, OverdueItem, Paginated, ReviewAction } from '~/types/api'

/** 風控後台：待審清單、逾期催收、審核動作。 */
export function useAdmin() {
  const { mutate } = useCsrf()

  const applications = useState<ApplicationSummary[]>('admin-applications', () => [])
  // 後端回報的總筆數；大於已載入筆數時表示還有未顯示的案件
  const applicationTotal = useState('admin-application-total', () => 0)
  const overdueList = useState<OverdueItem[]>('admin-overdue', () => [])
  const loading = useState('admin-loading', () => false)

  const pendingQueue = computed(() =>
    applications.value.filter((item) => item.status === 'pending' || item.status === 'reviewing'),
  )

  const reviewedQueue = computed(() =>
    applications.value.filter(
      (item) =>
        item.status === 'approved' ||
        item.status === 'rejected' ||
        item.status === 'more_info_required',
    ),
  )

  const overdueSummary = computed(() => ({
    count: overdueList.value.length,
    amount: overdueList.value.reduce((sum, row) => sum + row.amountDue, 0),
    loans: new Set(overdueList.value.map((row) => row.loanId)).size,
  }))

  /** 資產品質由實際申請資料即時聚合，而非寫死的展示數字。 */
  const portfolio = computed(() => {
    const all = applications.value
    const approved = all.filter((item) => item.status === 'approved')
    const decided = all.filter((item) => item.status === 'approved' || item.status === 'rejected')

    const byGrade = { A: 0, B: 0, C: 0 }
    let approvedAmount = 0
    for (const item of approved) {
      byGrade[item.grade] += 1
      approvedAmount += item.amount
    }
    const denominator = approved.length || 1

    return {
      applications: all.length,
      approved: approved.length,
      approvedAmount,
      approvalRate: decided.length ? (approved.length / decided.length) * 100 : 0,
      gradeShare: {
        A: (byGrade.A / denominator) * 100,
        B: (byGrade.B / denominator) * 100,
        C: (byGrade.C / denominator) * 100,
      },
    }
  })

  async function loadAdminData() {
    loading.value = true
    try {
      const [queue, overdue] = await Promise.all([
        // limit 取上限：風控後台目前一次呈現所有待審件，
        // 案件量成長到需要翻頁時再加上分頁控制項
        apiFetch<Paginated<ApplicationSummary>>('/api/v1/admin/applications?limit=200'),
        apiFetch<OverdueItem[]>('/api/v1/admin/overdue'),
      ])
      applications.value = queue.items
      applicationTotal.value = queue.page.total
      overdueList.value = overdue
    } finally {
      loading.value = false
    }
  }

  /** 執行審核。核准時後端會在同一 transaction 生成合約與攤還表。 */
  async function reviewApplication(applicationID: string, action: ReviewAction, reason: string) {
    return await mutate<{ status: string; loan?: Loan }>(
      `/api/v1/admin/applications/${applicationID}`,
      { method: 'PATCH', body: { action, reason } },
    )
  }

  function clearAdminData() {
    applications.value = []
    overdueList.value = []
  }

  return {
    applications,
    applicationTotal,
    overdueList,
    loading,
    pendingQueue,
    reviewedQueue,
    overdueSummary,
    portfolio,
    loadAdminData,
    reviewApplication,
    clearAdminData,
  }
}
