import type { InvestResponse, ListingItem } from '~/types/api'

/**
 * 投資市集：募資中的標的與前端篩選。
 *
 * 標的來自真實的已核准申請（/v1/listings），而非靜態展示資料。
 */
export function useMarket() {
  const { mutate } = useCsrf()

  const listings = useState<ListingItem[]>('market-listings', () => [])
  const loading = useState('market-loading', () => false)
  const investing = useState('market-investing', () => false)

  const grade = ref('全部')
  const term = ref('全部')
  const minRate = ref('不限')

  const filtered = computed(() =>
    listings.value.filter((listing) => {
      if (grade.value !== '全部' && listing.grade !== grade.value) return false
      if (term.value === '12 期以下' && listing.termMonths > 12) return false
      if (term.value === '24~36 期' && (listing.termMonths < 24 || listing.termMonths > 36)) return false
      if (term.value === '48 期以上' && listing.termMonths < 48) return false
      if (minRate.value === '4% 以上' && listing.rate < 4) return false
      if (minRate.value === '6% 以上' && listing.rate < 6) return false
      if (minRate.value === '8% 以上' && listing.rate < 8) return false
      return true
    }),
  )

  async function loadListings(status = 'funding') {
    loading.value = true
    try {
      listings.value = await apiFetch<ListingItem[]>(`/api/v1/listings?status=${status}`)
    } catch {
      listings.value = []
    } finally {
      loading.value = false
    }
  }

  /**
   * 投標。requestId 讓後端以冪等鍵擋下重複送出；
   * 超過剩餘額度時後端只接受剩餘部分，不會超募。
   */
  async function invest(listingId: string, amount: number) {
    investing.value = true
    try {
      return await mutate<InvestResponse>(`/api/v1/listings/${listingId}/invest`, {
        method: 'POST',
        body: { amount, requestId: `${listingId}-${Date.now()}` },
      })
    } finally {
      investing.value = false
    }
  }

  return { listings, loading, investing, grade, term, minRate, filtered, loadListings, invest }
}
