import type { Installment, Loan, PayResponse, Repayment, ScheduleResponse, SettleResponse } from '~/types/api'

/**
 * 單一合約的攤還表與繳款紀錄。
 *
 * 不使用 useState：還款頁一次只看一份合約，
 * 且切換合約時舊資料應立即失效而非殘留。
 */
export function useLoanSchedule() {
  const { mutate } = useCsrf()

  const loan = ref<Loan | null>(null)
  const schedule = ref<Installment[]>([])
  const repayments = ref<Repayment[]>([])
  const loading = ref(false)
  const busy = ref(false)

  /** 最早一期待繳或逾期的期數。 */
  const nextInstallment = computed(
    () => schedule.value.find((row) => row.status === 'due' || row.status === 'overdue') ?? null,
  )

  const totals = computed(() => ({
    principal: schedule.value.reduce((sum, row) => sum + row.principal, 0),
    interest: schedule.value.reduce((sum, row) => sum + row.interest, 0),
  }))

  /** 尚未繳納的期數，提前清償試算用。 */
  const unpaid = computed(() => schedule.value.filter((row) => row.status !== 'paid'))

  const payoffAmount = computed(() => unpaid.value.reduce((sum, row) => sum + row.principal, 0))
  const waivableInterest = computed(() => unpaid.value.reduce((sum, row) => sum + row.interest, 0))

  async function load(loanId: string) {
    if (!loanId) {
      loan.value = null
      schedule.value = []
      repayments.value = []
      return
    }
    loading.value = true
    try {
      const [scheduleResponse, repaymentRecords] = await Promise.all([
        apiFetch<ScheduleResponse>(`/api/v1/loans/${loanId}/schedule`),
        apiFetch<Repayment[]>(`/api/v1/loans/${loanId}/repayments`).catch(() => [] as Repayment[]),
      ])
      loan.value = scheduleResponse.loan
      schedule.value = scheduleResponse.schedule ?? []
      repayments.value = repaymentRecords
    } finally {
      loading.value = false
    }
  }

  /** 繳納指定期數。後端以冪等鍵防重複扣款，重送會回 409。 */
  async function payInstallment(loanId: string, installmentNo: number) {
    busy.value = true
    try {
      return await mutate<PayResponse>(
        `/api/v1/loans/${loanId}/installments/${installmentNo}/pay`,
        { method: 'POST' },
      )
    } finally {
      busy.value = false
    }
  }

  /** 提前清償：一次結清剩餘本金，未到期利息免除。 */
  async function settleLoan(loanId: string) {
    busy.value = true
    try {
      return await mutate<SettleResponse>(`/api/v1/loans/${loanId}/settle`, { method: 'POST' })
    } finally {
      busy.value = false
    }
  }

  return {
    loan,
    schedule,
    repayments,
    loading,
    busy,
    nextInstallment,
    totals,
    unpaid,
    payoffAmount,
    waivableInterest,
    load,
    payInstallment,
    settleLoan,
  }
}
