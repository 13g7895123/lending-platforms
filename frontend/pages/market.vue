<script setup lang="ts">
import { listingStatusMeta, type ListingItem } from '~/types/api'

useHead({ title: '投資市集 · CreditFlow' })

const { user, isInvestor } = useAuth()
const { filtered, loading, grade, term, minRate, loadListings, invest } = useMarket()
const { portfolio, loadPortfolio } = useInvestor()
const { openModal, closeModal } = useModal()
const { showToast } = useToast()

const bidAmount = ref(30000)

await useAsyncData('market-listings', async () => {
  await loadListings()
  if (isInvestor.value) await loadPortfolio()
  return true
})

/** 每萬元月收息，供出借人快速比較標的。 */
function monthlyInterestPerTenThousand(listing: ListingItem) {
  return pmt(10000, listing.rate, listing.termMonths) - 10000 / listing.termMonths
}

function statusMeta(status: string) {
  return listingStatusMeta[status] ?? { label: status, tone: 'info' as const }
}

/** 募資倒數的顯示文字與急迫程度。 */
function deadlineLabel(days: number) {
  if (days < 0) return { text: '已截止', tone: 'error' as const }
  if (days === 0) return { text: '今日截止', tone: 'error' as const }
  if (days <= 3) return { text: `剩 ${days} 天`, tone: 'warning' as const }
  return { text: `剩 ${days} 天`, tone: 'info' as const }
}

function openBidModal(listing: ListingItem) {
  if (!isInvestor.value) {
    showToast(
      user.value ? '只有出借人帳號可以投標' : '請先以出借人身分註冊或登入',
      'info',
    )
    return
  }

  // 預設投標金額不超過剩餘額度與可用餘額
  bidAmount.value = Math.max(
    1000,
    Math.min(30000, listing.remainingAmount, portfolio.value.balance),
  )

  openModal({
    kind: 'bid',
    title: `投標 · 標的 ${listing.id}`,
    description: `年化 ${listing.rate.toFixed(2)}% · 尚可投 ${formatNT(listing.remainingAmount)}`,
    rows: [
      { label: '可用餘額', value: formatNT(portfolio.value.balance), tone: 'brand-text' },
      { label: '借款期數', value: `${listing.termMonths} 期` },
      { label: '募集進度', value: `${listing.fundedPercent}%（${listing.investorCount} 人參與）` },
      { label: '募資期限', value: deadlineLabel(listing.daysRemaining).text },
      { label: '我已投入', value: formatNT(listing.myInvestedAmount) },
    ],
    input: {
      label: '投標金額（NT$）',
      placeholder: `1,000 ~ ${Math.min(listing.remainingAmount, portfolio.value.balance).toLocaleString('en-US')}`,
      required: true,
    },
    confirmLabel: '確認投標',
    onConfirm: async (value) => {
      const amount = Number(String(value).replace(/[^\d]/g, ''))
      if (!Number.isFinite(amount) || amount < 1000) {
        showToast('投標金額至少 1,000 元', 'error')
        return
      }
      try {
        const response = await invest(listing.id, amount)
        closeModal()
        if (response.fullyFunded && response.disbursedLoan) {
          showToast(
            `投標成功，標的已募滿並完成撥款（合約 ${response.disbursedLoan.id}）`,
          )
        } else {
          showToast(
            `投標成功，投入 ${formatNT(response.investment.amount)}，` +
              `募集進度 ${response.listing.fundedPercent}%`,
          )
        }
        await Promise.all([loadListings(), loadPortfolio()])
      } catch (error) {
        showToast(apiErrorMessage(error, '投標失敗'), 'error')
      }
    },
  })
}
</script>

<template>
  <section class="page active-page">
    <div class="container">
      <div class="page-intro">
        <div>
          <span class="section-kicker">INVESTMENT MARKET</span>
          <h1>投資市集</h1>
          <p>標的來自已通過授信的真實申請，募滿後平台自動撥款；期限內未募滿則全額退款</p>
        </div>
        <div v-if="isInvestor" class="repay-amount">
          <span>可用餘額</span>
          <strong>{{ formatNT(portfolio.balance) }}</strong>
          <NuxtLink class="btn small" to="/portfolio">我的投資</NuxtLink>
        </div>
        <NuxtLink v-else-if="!user" class="btn" to="/login?mode=register">
          註冊出借人帳號
        </NuxtLink>
      </div>

      <div class="filter-bar">
        <div class="field">
          <label for="market-grade">信用等級</label>
          <select id="market-grade" v-model="grade">
            <option>全部</option><option>A</option><option>B</option><option>C</option>
          </select>
        </div>
        <div class="field">
          <label for="market-term">期數</label>
          <select id="market-term" v-model="term">
            <option>全部</option><option>12 期以下</option><option>24~36 期</option><option>48 期以上</option>
          </select>
        </div>
        <div class="field">
          <label for="market-rate">最低年化</label>
          <select id="market-rate" v-model="minRate">
            <option>不限</option><option>4% 以上</option><option>6% 以上</option><option>8% 以上</option>
          </select>
        </div>
        <span class="filter-result">{{ filtered.length }} 筆募資中標的</span>
      </div>

      <div v-if="loading" class="empty-state">載入中…</div>

      <div v-else-if="filtered.length" class="listing-grid">
        <article v-for="listing in filtered" :key="listing.id" class="listing-card">
          <div class="listing-heading">
            <div>
              <strong>{{ listing.id }} · {{ listing.purpose }}</strong>
              <small>{{ listing.job || '—' }} · 年資 {{ listing.employmentYears || '—' }}</small>
            </div>
            <span class="grade" :class="listing.grade">{{ listing.grade }}</span>
          </div>

          <div class="listing-row">
            <span>年化報酬率</span>
            <strong class="green-text large">{{ listing.rate.toFixed(2) }}%</strong>
          </div>
          <div class="listing-row">
            <span>募資目標 / 期數</span>
            <strong>{{ formatNT(listing.targetAmount) }} / {{ listing.termMonths }} 期</strong>
          </div>
          <div class="listing-row">
            <span>每萬元月收息</span>
            <strong>{{ formatNT(monthlyInterestPerTenThousand(listing)) }}</strong>
          </div>

          <div class="funding">
            <div class="progress"><i :style="{ width: `${listing.fundedPercent}%` }" /></div>
            <div>
              <span>已募 {{ listing.fundedPercent }}%（{{ listing.investorCount }} 人）</span>
              <span>剩 {{ formatNT(listing.remainingAmount) }}</span>
            </div>
          </div>

          <div class="listing-row">
            <span>募資期限</span>
            <StatusTag
              :label="deadlineLabel(listing.daysRemaining).text"
              :tone="deadlineLabel(listing.daysRemaining).tone"
            />
          </div>

          <div v-if="listing.myInvestedAmount > 0" class="listing-row">
            <span>我已投入</span>
            <strong class="green-text">{{ formatNT(listing.myInvestedAmount) }}</strong>
          </div>

          <StatusTag
            v-if="listing.status !== 'funding'"
            :label="statusMeta(listing.status).label"
            :tone="statusMeta(listing.status).tone"
          />
          <StatusTag
            v-else-if="listing.daysRemaining < 0"
            label="募資已截止，將退還已投入資金"
            tone="error"
          />
          <button
            v-else
            class="btn primary small full"
            type="button"
            @click="openBidModal(listing)"
          >
            我要投標
          </button>
        </article>
      </div>

      <p v-else class="empty-state">
        目前沒有募資中的標的。標的來自風控核准的申請，核准後會自動上架。
      </p>
    </div>
  </section>
</template>
