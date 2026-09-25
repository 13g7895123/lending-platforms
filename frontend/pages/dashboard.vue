<script setup lang="ts">
import type { Loan } from '~/types/api'

definePageMeta({ middleware: 'auth' })
useHead({ title: '我的儀表板 · CreditFlow' })

const { summary, loans, applications, repayableLoans, loadDashboard, loadApplications } = useDashboard()
const { applicationLabel, applicationTone } = useStatus()
const { openModal, closeModal } = useModal()
const { showToast } = useToast()

const grade = computed(() => gradeForScore(summary.value.creditScore))

await useAsyncData('dashboard', async () => {
  await Promise.all([loadDashboard(), loadApplications()])
  return true
})

function openLoanDetails(loan: Loan) {
  openModal({
    kind: 'details',
    title: `合約明細 ${loan.id}`,
    description: `${loan.product} · 本息平均攤還`,
    rows: [
      { label: '核貸金額', value: formatNT(loan.amount) },
      { label: '年利率', value: `${loan.rate.toFixed(2)}%` },
      { label: '月付金', value: formatNT(loan.monthlyPayment), tone: 'brand-text' },
      { label: '期數', value: `${loan.totalInstallments} 期（月繳）` },
      { label: '已繳期數', value: `${loan.paidInstallments} 期` },
      { label: '繳款狀態', value: loan.status, tone: loan.statusTone },
    ],
    confirmLabel: '查看攤還明細',
    onConfirm: async () => {
      closeModal()
      await navigateTo(`/repay/${loan.id}`)
    },
  })
}
</script>

<template>
  <section class="page active-page">
    <div class="container">
      <div class="page-intro">
        <div>
          <span class="section-kicker">MY FINANCE</span>
          <h1>我的儀表板</h1>
          <p>掌握目前貸款、還款與信用狀態</p>
        </div>
      </div>

      <div class="kpi-grid">
        <article class="stat-card">
          <span>目前借款總額</span>
          <strong>{{ formatNT(summary.totalBorrowed) }}</strong>
          <small>未結清合約 {{ repayableLoans.length }} 筆</small>
        </article>
        <article class="stat-card">
          <span>每月應繳合計</span>
          <strong>{{ formatNT(summary.monthlyPayment) }}</strong>
          <small>依未結清合約加總</small>
        </article>
        <article class="stat-card">
          <span>信用評分</span>
          <strong>{{ summary.creditScore }} <em>{{ grade.grade }}</em></strong>
          <div class="progress"><i :style="{ width: `${(summary.creditScore / 900) * 100}%` }" /></div>
          <small>滿分 900</small>
        </article>
        <article class="stat-card">
          <span>正常還款率</span>
          <strong>{{ summary.repaymentRate.toFixed(1) }}%</strong>
          <small>依已到期期數計算</small>
        </article>
      </div>

      <div class="content-grid wide-main">
        <article class="surface-card">
          <div class="card-title-row">
            <h2>我的貸款合約</h2>
            <NuxtLink class="btn small" to="/apply">＋ 新增申請</NuxtLink>
          </div>

          <div v-if="loans.length" class="table-scroll">
            <table>
              <thead>
                <tr>
                  <th>合約編號</th><th>產品</th><th>金額</th><th>利率</th>
                  <th>月付金</th><th>進度</th><th>狀態</th><th />
                </tr>
              </thead>
              <tbody>
                <tr v-for="loan in loans" :key="loan.id">
                  <td class="mono">{{ loan.id }}</td>
                  <td>{{ loan.product }}</td>
                  <td class="mono">{{ formatNT(loan.amount) }}</td>
                  <td class="mono">{{ loan.rate.toFixed(2) }}%</td>
                  <td class="mono">{{ formatNT(loan.monthlyPayment) }}</td>
                  <td>{{ loan.paidInstallments }} / {{ loan.totalInstallments }}</td>
                  <td><StatusTag :label="loan.status" :tone="loan.statusTone" /></td>
                  <td><button class="btn small" type="button" @click="openLoanDetails(loan)">明細</button></td>
                </tr>
              </tbody>
            </table>
          </div>
          <p v-else class="empty-state">目前沒有貸款合約。申請核准後，合約會自動出現在這裡。</p>
        </article>

        <aside class="surface-card score-card">
          <div class="card-title-row">
            <h2>信用健康度</h2>
            <StatusTag
              :label="grade.grade === 'A' ? '良好' : grade.grade === 'B' ? '普通' : '需改善'"
              :tone="grade.grade === 'A' ? 'success' : grade.grade === 'B' ? 'warning' : 'error'"
            />
          </div>
          <div class="score-ring"><div><strong>{{ summary.creditScore }}</strong><span>/ 900</span></div></div>
          <div class="metric-list">
            <div><span>適用等級</span><strong>{{ grade.grade }} 級</strong></div>
            <div><span>參考年利率</span><strong>{{ grade.rate.toFixed(2) }}%</strong></div>
            <div><span>正常還款率</span><strong>{{ summary.repaymentRate.toFixed(1) }}%</strong></div>
          </div>
        </aside>
      </div>

      <article class="surface-card">
        <div class="card-title-row">
          <div>
            <h2>我的申請進度</h2>
            <p class="card-subtitle">核准後上架募資，募滿由平台自動撥款</p>
          </div>
          <StatusTag v-if="applications.length" :label="`${applications.length} 筆`" tone="info" />
        </div>

        <div v-if="applications.length" class="table-scroll">
          <table>
            <thead>
              <tr>
                <th>案件編號</th><th>產品</th><th>金額</th><th>期數</th>
                <th>預估月付金</th><th>DBR</th><th>募資進度</th><th>送出時間</th><th>狀態</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="item in applications" :key="item.id">
                <td class="mono">{{ item.id }}</td>
                <td>{{ item.product }}</td>
                <td class="mono">{{ formatNT(item.amount) }}</td>
                <td class="mono">{{ item.termMonths }}</td>
                <td class="mono">{{ formatNT(item.estimatedPayment) }}</td>
                <td class="mono">{{ item.dbr.toFixed(1) }}%</td>
                <td>
                  <template v-if="item.listingId">
                    <div class="progress compact"><i :style="{ width: `${item.fundedPercent}%` }" /></div>
                    <small class="mono">{{ item.fundedPercent }}% · {{ formatNT(item.fundedAmount) }}</small>
                  </template>
                  <span v-else>—</span>
                </td>
                <td class="mono">{{ formatDate(item.createdAt) }}</td>
                <td><StatusTag :label="applicationLabel(item.status)" :tone="applicationTone(item.status)" /></td>
              </tr>
            </tbody>
          </table>
        </div>
        <p v-else class="empty-state">
          尚未送出任何申請。
          <NuxtLink class="link-button" to="/apply">立即申請貸款</NuxtLink>
        </p>
      </article>
    </div>
  </section>
</template>
