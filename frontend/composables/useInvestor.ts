import type { DistributionRecord, PortfolioResponse } from '~/types/api'

/** 出借人持倉、餘額與入金。 */
export function useInvestor() {
  const { mutate } = useCsrf()

  const emptyPortfolio = (): PortfolioResponse => ({
    balance: 0,
    totalInvested: 0,
    activeCount: 0,
    estimatedReturn: 0,
    weightedRate: 0,
    totalPrincipalReturned: 0,
    totalInterestEarned: 0,
    outstandingPrincipal: 0,
    investments: [],
  })

  const portfolio = useState<PortfolioResponse>('investor-portfolio', emptyPortfolio)
  const distributions = useState<DistributionRecord[]>('investor-distributions', () => [])
  const loading = useState('investor-loading', () => false)

  /** 已撥款的投資筆數：這些才開始真正產生利息。 */
  const disbursedCount = computed(
    () => portfolio.value.investments.filter((item) => item.status === 'disbursed').length,
  )

  /** 已實現收益占預估收益的比例，用於呈現「還在路上」的部分。 */
  const realisedRatio = computed(() => {
    if (portfolio.value.estimatedReturn <= 0) return 0
    return (portfolio.value.totalInterestEarned / portfolio.value.estimatedReturn) * 100
  })

  async function loadPortfolio() {
    loading.value = true
    try {
      const [summary, records] = await Promise.all([
        apiFetch<PortfolioResponse>('/api/v1/investments'),
        apiFetch<DistributionRecord[]>('/api/v1/investments/distributions').catch(
          () => [] as DistributionRecord[],
        ),
      ])
      portfolio.value = summary
      distributions.value = records
    } finally {
      loading.value = false
    }
  }

  /** 入金。展示用：真實平台需接金流閘道並核對入帳。 */
  async function topUp(amount: number) {
    const response = await mutate<{ balance: number }>('/api/v1/investments/top-up', {
      method: 'POST',
      body: { amount },
    })
    portfolio.value.balance = response.balance
    return response.balance
  }

  function clearPortfolio() {
    portfolio.value = emptyPortfolio()
    distributions.value = []
  }

  return {
    portfolio,
    distributions,
    loading,
    disbursedCount,
    realisedRatio,
    loadPortfolio,
    topUp,
    clearPortfolio,
  }
}
