<script setup lang="ts">
import type { ApplicationSummary, ReviewAction } from '~/types/api'

definePageMeta({ middleware: 'reviewer' })
useHead({ title: '風控後台 · CreditFlow' })

const {
  loading, pendingQueue, reviewedQueue, overdueList, overdueSummary, portfolio,
  applications, applicationTotal, loadAdminData, reviewApplication,
} = useAdmin()
const { applicationLabel, applicationTone } = useStatus()
const { openModal, closeModal } = useModal()
const { showToast } = useToast()

type AdminTab = 'pending' | 'overdue' | 'reviewed' | 'portfolio'
const tab = ref<AdminTab>('pending')

// 展開中的案件：顯示其附件供審核參考
const expandedApplication = ref('')

function toggleDocuments(applicationId: string) {
  expandedApplication.value = expandedApplication.value === applicationId ? '' : applicationId
}

await useAsyncData('admin-data', async () => {
  await loadAdminData()
  return true
})

const actionLabels: Record<ReviewAction, string> = {
  approve: '核准',
  reject: '婉拒',
  request_more_info: '要求補件',
}

function openReviewModal(item: ApplicationSummary, action: ReviewAction) {
  const label = actionLabels[action]

  openModal({
    kind: 'review',
    title: `${label} · ${item.id}`,
    description:
      action === 'approve'
        ? `核准後將立即生成貸款合約與 ${item.termMonths} 期攤還表。`
        : '此決定將通知申請人，並記錄於審核軌跡。',
    rows: [
      { label: '申請人', value: item.applicantName },
      { label: '身分證', value: item.idNumberMasked || '—' },
      { label: '年收入級距', value: item.incomeRange },
      { label: '月支出級距', value: item.expenseRange },
      { label: '申請金額', value: formatNT(item.amount) },
      { label: '期數 / 等級', value: `${item.termMonths} 期 · ${item.grade} 級` },
      { label: '預估月付金', value: formatNT(item.estimatedPayment), tone: 'brand-text' },
      { label: 'DBR', value: `${item.dbr.toFixed(1)}%` },
      { label: 'AI 建議', value: item.recommendation, tone: item.recommendTone },
    ],
    confirmLabel: `確認${label}`,
    // 婉拒與補件必須說明原因，核准則可留白
    input: {
      label: '審核理由',
      placeholder: action === 'approve' ? '可補充核准附帶條件' : '請說明原因，將記錄於審核軌跡',
      required: action !== 'approve',
    },
    onConfirm: async (reason) => {
      try {
        const response = await reviewApplication(item.id, action, reason)
        closeModal()
        showToast(
          response.loan
            ? `${item.id} 已核准，合約 ${response.loan.id} 已生成`
            : `${item.id} 已更新為「${applicationLabel(response.status)}」`,
        )
        await loadAdminData()
      } catch (error) {
        showToast(apiErrorMessage(error, '審核失敗'), 'error')
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
          <span class="section-kicker">RISK CONTROL CONSOLE</span>
          <h1>風控後台</h1>
          <p>授信審核 · 逾期催收 · 資產品質監控（僅限風控人員）</p>
        </div>
        <button class="btn" type="button" :disabled="loading" @click="loadAdminData">
          {{ loading ? '載入中…' : '↻ 重新載入' }}
        </button>
      </div>

      <div class="tabs">
        <button type="button" :class="{ active: tab === 'pending' }" @click="tab = 'pending'">
          待審件 ({{ pendingQueue.length }})
        </button>
        <button type="button" :class="{ active: tab === 'overdue' }" @click="tab = 'overdue'">
          逾期案件 ({{ overdueSummary.count }})
        </button>
        <button type="button" :class="{ active: tab === 'reviewed' }" @click="tab = 'reviewed'">
          已處理 ({{ reviewedQueue.length }})
        </button>
        <button type="button" :class="{ active: tab === 'portfolio' }" @click="tab = 'portfolio'">
          資產品質
        </button>
      </div>

      <article v-if="tab === 'pending'" class="surface-card">
        <div v-if="pendingQueue.length" class="table-scroll">
          <table>
            <thead>
              <tr>
                <th>案件編號</th><th>申請人</th><th>身分證</th><th>年收入</th><th>產品</th>
                <th>金額</th><th>期數</th><th>評分</th><th>等級</th><th>DBR</th><th>建議</th><th>操作</th>
              </tr>
            </thead>
            <tbody>
              <template v-for="item in pendingQueue" :key="item.id">
              <tr>
                <td class="mono">{{ item.id }}</td>
                <td>{{ item.applicantName }}</td>
                <td class="mono">{{ item.idNumberMasked || '—' }}</td>
                <td class="mono">{{ item.incomeRange }}</td>
                <td>{{ item.product }}</td>
                <td class="mono">{{ formatNT(item.amount) }}</td>
                <td class="mono">{{ item.termMonths }}</td>
                <td class="mono" :class="item.recommendTone">{{ item.creditScore }}</td>
                <td><span class="grade" :class="item.grade">{{ item.grade }}</span></td>
                <td class="mono">{{ item.dbr.toFixed(1) }}%</td>
                <td><StatusTag :label="item.recommendation" :tone="item.recommendTone" /></td>
                <td class="action-cell">
                  <button class="btn small" type="button" @click="toggleDocuments(item.id)">
                    {{ expandedApplication === item.id ? '收合附件' : '附件' }}
                  </button>
                  <button class="btn small" type="button" @click="openReviewModal(item, 'approve')">核准</button>
                  <button class="btn small" type="button" @click="openReviewModal(item, 'request_more_info')">補件</button>
                  <button class="btn small" type="button" @click="openReviewModal(item, 'reject')">婉拒</button>
                </td>
              </tr>
              <tr v-if="expandedApplication === item.id">
                <td colspan="12">
                  <DocumentUploader :application-id="item.id" readonly />
                </td>
              </tr>
              </template>
            </tbody>
          </table>
        </div>
        <p v-else class="empty-state">目前沒有待審案件。</p>
        <p v-if="applicationTotal > applications.length" class="card-note">
          共 {{ applicationTotal }} 件，目前顯示最近 {{ applications.length }} 件。
        </p>
      </article>

      <article v-else-if="tab === 'overdue'" class="surface-card">
        <div class="card-title-row">
          <div>
            <h2>逾期催收清單</h2>
            <p class="card-subtitle">依逾期天數排序 · M1 未滿 30 天 / M2 30–59 天 / M3+ 60 天以上</p>
          </div>
          <div class="schedule-totals">
            <span>逾期金額 <strong class="mono">{{ formatNT(overdueSummary.amount) }}</strong></span>
            <span>涉及合約 <strong class="mono">{{ overdueSummary.loans }}</strong> 筆</span>
          </div>
        </div>

        <div v-if="overdueList.length" class="table-scroll">
          <table>
            <thead>
              <tr>
                <th>合約編號</th><th>借款人</th><th>產品</th><th>期數</th><th>繳款日</th>
                <th>逾期天數</th><th>逾期金額</th><th>剩餘本金</th><th>階段</th><th>建議處理</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="row in overdueList" :key="`${row.loanId}-${row.installmentNo}`">
                <td class="mono">{{ row.loanId }}</td>
                <td>{{ row.borrowerName }}</td>
                <td>{{ row.product }}</td>
                <td class="mono">{{ row.installmentNo }}</td>
                <td class="mono">{{ formatDate(row.dueDate) }}</td>
                <td class="mono">{{ row.overdueDays }} 天</td>
                <td class="mono">{{ formatNT(row.amountDue) }}</td>
                <td class="mono">{{ formatNT(row.remainingBalance) }}</td>
                <td><StatusTag :label="row.stage" :tone="row.stageTone" /></td>
                <td>{{ row.action }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <p v-else class="empty-state">目前沒有逾期案件。</p>
      </article>

      <article v-else-if="tab === 'reviewed'" class="surface-card">
        <div v-if="reviewedQueue.length" class="table-scroll">
          <table>
            <thead>
              <tr>
                <th>案件編號</th><th>申請人</th><th>產品</th><th>金額</th>
                <th>期數</th><th>等級</th><th>送出時間</th><th>結果</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="item in reviewedQueue" :key="item.id">
                <td class="mono">{{ item.id }}</td>
                <td>{{ item.applicantName }}</td>
                <td>{{ item.product }}</td>
                <td class="mono">{{ formatNT(item.amount) }}</td>
                <td class="mono">{{ item.termMonths }}</td>
                <td><span class="grade" :class="item.grade">{{ item.grade }}</span></td>
                <td class="mono">{{ formatDate(item.createdAt) }}</td>
                <td><StatusTag :label="applicationLabel(item.status)" :tone="applicationTone(item.status)" /></td>
              </tr>
            </tbody>
          </table>
        </div>
        <p v-else class="empty-state">尚未有已處理的案件。</p>
      </article>

      <div v-else class="portfolio-grid">
        <article class="surface-card portfolio-card">
          <div class="card-title-row">
            <h2>核准案件分級</h2>
            <strong>{{ formatNT(portfolio.approvedAmount) }}</strong>
          </div>

          <template v-if="portfolio.approved">
            <div class="risk-bar">
              <i :style="{ width: `${portfolio.gradeShare.A}%`, background: '#34d399' }" />
              <i :style="{ width: `${portfolio.gradeShare.B}%`, background: '#3b82f6' }" />
              <i :style="{ width: `${portfolio.gradeShare.C}%`, background: '#fbbf24' }" />
            </div>
            <div class="legend">
              <span><i class="dot green-bg" />A 級 {{ portfolio.gradeShare.A.toFixed(1) }}%</span>
              <span><i class="dot blue-bg" />B 級 {{ portfolio.gradeShare.B.toFixed(1) }}%</span>
              <span><i class="dot yellow-bg" />C 級 {{ portfolio.gradeShare.C.toFixed(1) }}%</span>
            </div>
          </template>

          <div class="metric-list">
            <div><span>申請總件數</span><strong>{{ portfolio.applications }} 件</strong></div>
            <div><span>已核准件數</span><strong>{{ portfolio.approved }} 件</strong></div>
            <div><span>核准率</span><strong>{{ portfolio.approvalRate.toFixed(1) }}%</strong></div>
            <div><span>待審件數</span><strong>{{ pendingQueue.length }} 件</strong></div>
            <div><span>逾期期數</span><strong>{{ overdueSummary.count }} 期</strong></div>
          </div>
          <p class="card-note">以上數字由申請與逾期資料即時聚合。</p>
        </article>

        <article class="surface-card portfolio-card">
          <div class="card-title-row"><h2>授信規則</h2><StatusTag label="規則版" tone="info" /></div>
          <div class="metric-list">
            <div><span>A 級門檻 / 利率</span><strong class="mono">評分 ≥ 750 · 4.88%</strong></div>
            <div><span>B 級門檻 / 利率</span><strong class="mono">評分 ≥ 650 · 6.80%</strong></div>
            <div><span>C 級門檻 / 利率</span><strong class="mono">評分 &lt; 650 · 9.60%</strong></div>
            <div><span>婉拒門檻</span><strong class="mono">DBR ≥ 25% 或評分 &lt; 600</strong></div>
            <div><span>補件門檻</span><strong class="mono">DBR ≥ 22% 或評分 &lt; 680</strong></div>
          </div>
          <p class="card-note">
            目前為規則式評分，尚未接入機器學習模型。建議欄位僅供輔助，最終決策仍由風控人員判斷。
          </p>
        </article>
      </div>
    </div>
  </section>
</template>
