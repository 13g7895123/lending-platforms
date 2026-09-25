<script setup lang="ts">
import { listingStatusMeta } from '~/types/api'

definePageMeta({ middleware: 'investor' })
useHead({ title: '我的投資 · CreditFlow' })

const { portfolio, distributions, loading, disbursedCount, realisedRatio, loadPortfolio, topUp } = useInvestor()
const { openModal, closeModal } = useModal()
const { showToast } = useToast()
const { refresh } = useAuth()

await useAsyncData('portfolio', async () => {
  await loadPortfolio()
  return true
})

function statusMeta(status: string) {
  return listingStatusMeta[status] ?? { label: status, tone: 'info' as const }
}

function openTopUpModal() {
  openModal({
    kind: 'notice',
    title: '帳戶入金',
    description: '展示用功能：未接金流閘道，確認後直接增加可用餘額',
    rows: [{ label: '目前餘額', value: formatNT(portfolio.value.balance), tone: 'brand-text' }],
    input: { label: '入金金額（NT$）', placeholder: '例如 500000', required: true },
    confirmLabel: '確認入金',
    onConfirm: async (value) => {
      const amount = Number(String(value).replace(/[^\d]/g, ''))
      if (!Number.isFinite(amount) || amount <= 0) {
        showToast('請輸入有效金額', 'error')
        return
      }
      try {
        const balance = await topUp(amount)
        closeModal()
        showToast(`已入金 ${formatNT(amount)}，餘額 ${formatNT(balance)}`)
        // session 中的餘額也要更新，導覽列才會同步
        await refresh()
      } catch (error) {
        showToast(apiErrorMessage(error, '入金失敗'), 'error')
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
          <span class="section-kicker">MY PORTFOLIO</span>
          <h1>我的投資</h1>
          <p>掌握持倉分布、預估收益與可用資金</p>
        </div>
        <div class="page-actions">
          <NuxtLink class="btn" to="/market">前往市集</NuxtLink>
          <button class="btn primary" type="button" @click="openTopUpModal">入金</button>
        </div>
      </div>

      <div class="kpi-grid">
        <article class="stat-card">
          <span>可用餘額</span>
          <strong>{{ formatNT(portfolio.balance) }}</strong>
          <small>可立即用於投標</small>
        </article>
        <article class="stat-card">
          <span>累計投入</span>
          <strong>{{ formatNT(portfolio.totalInvested) }}</strong>
          <small>{{ portfolio.investments.length }} 筆投標</small>
        </article>
        <article class="stat-card">
          <span>已收利息</span>
          <strong class="green-text">{{ formatNT(portfolio.totalInterestEarned) }}</strong>
          <small>預估總收益 {{ formatNT(portfolio.estimatedReturn) }}（{{ realisedRatio.toFixed(1) }}%）</small>
        </article>
        <article class="stat-card">
          <span>待收本金</span>
          <strong>{{ formatNT(portfolio.outstandingPrincipal) }}</strong>
          <small>已收回 {{ formatNT(portfolio.totalPrincipalReturned) }}</small>
        </article>
      </div>

      <article class="surface-card">
        <div class="card-title-row">
          <div>
            <h2>持倉明細</h2>
            <p class="card-subtitle">已撥款的標的才開始產生利息</p>
          </div>
          <StatusTag
            v-if="portfolio.investments.length"
            :label="`${portfolio.investments.length} 筆`"
            tone="info"
          />
        </div>

        <div v-if="loading" class="empty-state">載入中…</div>

        <div v-else-if="portfolio.investments.length" class="table-scroll">
          <table>
            <thead>
              <tr>
                <th>投標時間</th><th>標的</th><th>用途</th><th>等級</th><th>年化</th>
                <th>投入金額</th><th>已收本金</th><th>待收本金</th><th>已收利息</th>
                <th>預估收益</th><th>狀態</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="item in portfolio.investments" :key="item.id">
                <td class="mono">{{ formatDate(item.createdAt) }}</td>
                <td class="mono">{{ item.listingId }}</td>
                <td>{{ item.purpose }}</td>
                <td><span class="grade" :class="item.grade">{{ item.grade }}</span></td>
                <td class="mono">{{ item.rate.toFixed(2) }}%</td>
                <td class="mono">{{ formatNT(item.amount) }}</td>
                <td class="mono">{{ formatNT(item.principalReturned) }}</td>
                <td class="mono">{{ formatNT(item.outstandingPrincipal) }}</td>
                <td class="mono green-text">{{ formatNT(item.interestEarned) }}</td>
                <td class="mono">{{ formatNT(item.estimatedReturn) }}</td>
                <td>
                  <StatusTag :label="statusMeta(item.status).label" :tone="statusMeta(item.status).tone" />
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <p v-else class="empty-state">
          尚未投資任何標的。
          <NuxtLink class="link-button" to="/market">前往投資市集</NuxtLink>
        </p>
      </article>

      <article class="surface-card">
        <div class="card-title-row">
          <div>
            <h2>收款明細</h2>
            <p class="card-subtitle">借款人還款時按投資占比分配</p>
          </div>
          <StatusTag v-if="distributions.length" :label="`${distributions.length} 筆`" tone="success" />
        </div>

        <div v-if="distributions.length" class="table-scroll">
          <table>
            <thead>
              <tr><th>收款時間</th><th>標的</th><th>用途</th><th>本金</th><th>利息</th><th>合計</th></tr>
            </thead>
            <tbody>
              <tr v-for="record in distributions" :key="record.id">
                <td class="mono">{{ formatDate(record.createdAt) }}</td>
                <td class="mono">{{ record.listingId }}</td>
                <td>{{ record.purpose }}</td>
                <td class="mono">{{ formatNT(record.principal) }}</td>
                <td class="mono green-text">{{ formatNT(record.interest) }}</td>
                <td class="mono">{{ formatNT(record.total) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <p v-else class="empty-state">
          尚無收款紀錄。借款人開始還款後，你的份額會自動入帳到可用餘額。
        </p>
      </article>

      <article class="surface-card">
        <div class="card-title-row"><h2>風險提醒</h2><StatusTag label="請詳閱" tone="warning" /></div>
        <div class="metric-list">
          <div><span>本金保障</span><strong>無</strong></div>
          <div><span>借款人違約風險</span><strong>由出借人承擔</strong></div>
          <div><span>提前贖回</span><strong>不支援</strong></div>
          <div><span>收益計算</span><strong>預估值；實收見「已收利息」</strong></div>
          <div><span>提前清償影響</span><strong>未到期利息免除，不予分配</strong></div>
        </div>
        <p class="card-note">
          預估收益假設借款人如期履約至最後一期。借款人若提前清償，未到期利息會被免除，
          實際收益將低於估算值。逾期與違約亦會影響實收金額。
          本平台尚未接金流閘道，入金與投標皆為展示用途。
        </p>
      </article>
    </div>
  </section>
</template>
