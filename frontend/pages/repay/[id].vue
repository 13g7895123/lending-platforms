<script setup lang="ts">
definePageMeta({ middleware: 'auth' })

const route = useRoute()
const loanId = computed(() => String(route.params.id))

const {
  loan, schedule, repayments, loading, busy,
  nextInstallment, totals, unpaid, payoffAmount, waivableInterest,
  load, payInstallment, settleLoan,
} = useLoanSchedule()

const { repayableLoans, loadDashboard } = useDashboard()
const { installmentLabel, installmentTone } = useStatus()
const { openModal, closeModal } = useModal()
const { showToast } = useToast()

useHead(() => ({ title: `${loanId.value} 還款明細 · CreditFlow` }))

const notFound = ref(false)

async function reload() {
  notFound.value = false
  try {
    await load(loanId.value)
  } catch (error) {
    // 後端對他人或不存在的合約一律回 404（不洩漏存在性）。
    // 前端也必須回報 404 狀態碼，否則監控與爬蟲會把錯誤頁當成正常頁面。
    notFound.value = true
    if (import.meta.server) {
      // useRequestEvent 只在 SSR 有值
      const event = useRequestEvent()
      if (event) setResponseStatus(event, 404)
    } else {
      showToast(apiErrorMessage(error, '找不到這份合約'), 'error')
    }
  }
}

await useAsyncData(`loan-${loanId.value}`, async () => {
  await reload()
  return true
})

// 直接以 URL 切換合約時也要重新載入
watch(loanId, reload)

function openPaymentModal() {
  const row = nextInstallment.value
  if (!row || !loan.value) return showToast('目前沒有待繳期數', 'info')

  openModal({
    kind: 'payment',
    title: '確認還款',
    description: '確認後將登記本期繳款紀錄並推進合約進度',
    rows: [
      { label: '還款金額', value: formatNT(row.amountDue), tone: 'brand-text' },
      { label: '合約', value: loan.value.id },
      { label: '期數', value: `第 ${row.installmentNo} / ${loan.value.totalInstallments} 期` },
      { label: '繳款日', value: formatDate(row.dueDate) },
    ],
    confirmLabel: '確認繳款',
    onConfirm: async () => {
      try {
        const response = await payInstallment(loanId.value, row.installmentNo)
        closeModal()
        showToast(
          response.settled
            ? `第 ${row.installmentNo} 期已繳納，合約已全數結清`
            : `第 ${row.installmentNo} 期已繳納 ${formatNT(response.repayment.amount)}`,
        )
        await Promise.all([reload(), loadDashboard()])
      } catch (error) {
        showToast(apiErrorMessage(error, '還款失敗'), 'error')
      }
    },
  })
}

function openPayoffModal() {
  if (!loan.value || !unpaid.value.length) return showToast('沒有可清償的期數', 'info')

  openModal({
    kind: 'prepay',
    title: '提前清償試算',
    description: `合約 ${loan.value.id} · 一次結清剩餘本金，未到期利息免除`,
    rows: [
      { label: '剩餘本金', value: formatNT(payoffAmount.value), tone: 'brand-text' },
      { label: '可節省未到期利息', value: formatNT(waivableInterest.value), tone: 'success' },
      { label: '剩餘期數', value: `${unpaid.value.length} 期` },
    ],
    confirmLabel: '確認清償',
    onConfirm: async () => {
      try {
        const response = await settleLoan(loanId.value)
        closeModal()
        showToast(
          `合約已結清，共結清 ${response.closedCount} 期，節省利息 ${formatNT(response.waivedInterest)}`,
        )
        await Promise.all([reload(), loadDashboard()])
        // 結清後導回列表，讓使用者看到還有哪些未結清合約
        const next = repayableLoans.value[0]
        await navigateTo(next ? `/repay/${next.id}` : '/repay')
      } catch (error) {
        showToast(apiErrorMessage(error, '清償失敗'), 'error')
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
          <span class="section-kicker">REPAYMENT CENTER</span>
          <h1>還款管理</h1>
          <p v-if="loan">{{ loan.id }} · {{ loan.product }} · 年利率 {{ loan.rate.toFixed(2) }}%</p>
        </div>
        <div class="page-actions">
          <NuxtLink class="btn" to="/repay">← 所有合約</NuxtLink>
          <button class="btn" type="button" :disabled="!unpaid.length || busy" @click="openPayoffModal">
            試算提前清償
          </button>
        </div>
      </div>

      <p v-if="notFound" class="empty-state">
        找不到這份合約，或它不屬於你的帳號。
        <NuxtLink class="link-button" to="/repay">回到合約列表</NuxtLink>
      </p>

      <template v-else-if="loan">
        <div v-if="nextInstallment" class="repay-banner">
          <div>
            <span>{{ loan.id }} · {{ loan.product }}</span>
            <strong>第 {{ nextInstallment.installmentNo }} / {{ loan.totalInstallments }} 期</strong>
            <small>下一期繳款日 {{ formatDate(nextInstallment.dueDate) }}</small>
          </div>
          <div class="repay-amount">
            <span>本期應繳</span>
            <strong>{{ formatNT(nextInstallment.amountDue) }}</strong>
            <button class="btn primary" type="button" :disabled="busy" @click="openPaymentModal">
              {{ busy ? '處理中…' : '立即還款' }}
            </button>
          </div>
        </div>

        <div v-else-if="schedule.length" class="repay-banner settled-banner">
          <div>
            <span>{{ loan.id }} · {{ loan.product }}</span>
            <strong>本合約已無待繳期數</strong>
            <small>共 {{ schedule.length }} 期已全數繳清</small>
          </div>
        </div>

        <article class="surface-card">
          <div class="card-title-row">
            <div>
              <h2>攤還明細</h2>
              <p class="card-subtitle">本息平均攤還 · 共 {{ schedule.length }} 期</p>
            </div>
            <div class="schedule-totals">
              <span>本金合計 <strong class="mono">{{ formatNT(totals.principal) }}</strong></span>
              <span>利息合計 <strong class="mono">{{ formatNT(totals.interest) }}</strong></span>
            </div>
          </div>

          <div v-if="loading" class="empty-state">載入中…</div>
          <div v-else class="table-scroll repayment-table">
            <table>
              <thead>
                <tr>
                  <th>期數</th><th>繳款日</th><th>應繳金額</th>
                  <th>本金</th><th>利息</th><th>剩餘本金</th><th>狀態</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="row in schedule" :key="row.installmentNo">
                  <td class="mono">{{ row.installmentNo }}</td>
                  <td class="mono">{{ formatDate(row.dueDate) }}</td>
                  <td class="mono">{{ formatNT(row.amountDue) }}</td>
                  <td class="mono">{{ formatNT(row.principal) }}</td>
                  <td class="mono">{{ formatNT(row.interest) }}</td>
                  <td class="mono">{{ formatNT(row.remainingBalance) }}</td>
                  <td><StatusTag :label="installmentLabel(row.status)" :tone="installmentTone(row.status)" /></td>
                </tr>
              </tbody>
            </table>
          </div>
        </article>

        <article class="surface-card">
          <div class="card-title-row">
            <div><h2>繳款紀錄</h2><p class="card-subtitle">實際入帳的還款交易</p></div>
            <StatusTag v-if="repayments.length" :label="`${repayments.length} 筆`" tone="success" />
          </div>

          <div v-if="repayments.length" class="table-scroll">
            <table>
              <thead>
                <tr><th>時間</th><th>類型</th><th>期數</th><th>金額</th><th>本金</th><th>利息</th></tr>
              </thead>
              <tbody>
                <tr v-for="row in repayments" :key="row.id">
                  <td class="mono">{{ formatDate(row.createdAt) }}</td>
                  <td>
                    <StatusTag
                      :label="row.kind === 'prepayment' ? '提前清償' : '單期繳款'"
                      :tone="row.kind === 'prepayment' ? 'info' : 'success'"
                    />
                  </td>
                  <td class="mono">{{ row.installmentNo ?? '—' }}</td>
                  <td class="mono">{{ formatNT(row.amount) }}</td>
                  <td class="mono">{{ formatNT(row.principal) }}</td>
                  <td class="mono">{{ formatNT(row.interest) }}</td>
                </tr>
              </tbody>
            </table>
          </div>
          <p v-else class="empty-state">尚無繳款紀錄。</p>
        </article>
      </template>
    </div>
  </section>
</template>
