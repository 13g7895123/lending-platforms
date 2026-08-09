<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'

type PageKey = 'home' | 'dashboard' | 'apply' | 'market' | 'repay' | 'admin'
type ToastKind = 'success' | 'error' | 'info'
type ModalKind = 'details' | 'payment' | 'prepay' | 'bid' | 'notice'

interface Loan {
  id: string
  product: string
  amount: number
  rate: number
  paidInstallments: number
  totalInstallments: number
  status: string
  statusTone: 'success' | 'warning' | 'info'
}

interface Listing {
  id: string
  purpose: string
  grade: 'A' | 'B' | 'C'
  rate: number
  amount: number
  term: number
  funded: number
  job: string
  years: string
  region: string
}

interface ModalRow {
  label: string
  value: string
  tone?: 'success' | 'warning' | 'error' | 'info' | 'brand-text'
}

interface ModalState {
  kind: ModalKind
  title: string
  description: string
  rows: ModalRow[]
  confirmLabel?: string
}

interface DashboardResponse {
  summary: {
    totalBorrowed: number
    monthlyPayment: number
    creditScore: number
    repaymentRate: number
  }
  loans: Loan[]
}

const navItems: { key: PageKey; label: string; icon: string }[] = [
  { key: 'home', label: '首頁', icon: '⌂' },
  { key: 'dashboard', label: '我的儀表板', icon: '▦' },
  { key: 'apply', label: '申請貸款', icon: '＋' },
  { key: 'market', label: '投資市集', icon: '◈' },
  { key: 'repay', label: '還款管理', icon: '↻' },
  { key: 'admin', label: '風控後台', icon: '⌁' },
]

const activePage = ref<PageKey>('home')
const menuOpen = ref(false)
const toast = ref<{ message: string; kind: ToastKind } | null>(null)
const modal = ref<ModalState | null>(null)
const apiStatus = ref<'checking' | 'online' | 'offline'>('checking')
const apiMessage = ref('連線檢查中')
let toastTimer: ReturnType<typeof setTimeout> | undefined

const calculator = reactive({ amount: 500000, term: 36, rate: 4.88 })
const rateOptions = [
  { label: 'A 級｜年利率 4.88%', value: 4.88 },
  { label: 'B 級｜年利率 6.80%', value: 6.8 },
  { label: 'C 級｜年利率 9.60%', value: 9.6 },
]

function pmt(principal: number, annualRate: number, months: number) {
  const monthlyRate = annualRate / 100 / 12
  if (!months) return 0
  return monthlyRate === 0
    ? principal / months
    : principal * monthlyRate / (1 - Math.pow(1 + monthlyRate, -months))
}

function formatNT(value: number) {
  return `NT$ ${Math.round(value || 0).toLocaleString('en-US')}`
}

const calculatorPayment = computed(() => pmt(calculator.amount, calculator.rate, calculator.term))
const calculatorTotal = computed(() => calculatorPayment.value * calculator.term)
const calculatorInterest = computed(() => calculatorTotal.value - calculator.amount)
const calculatorFee = computed(() => calculator.amount * 0.015)

const application = reactive({
  product: '個人信用貸款',
  amount: 500000,
  term: 36,
  purpose: '債務整合',
  name: '陳建宏',
  idNumber: 'A12****678',
  phone: '0912-345-678',
  email: 'chen@example.com',
  job: '上市櫃公司員工',
  years: '3~5 年',
  income: 1080000,
  expenses: 32000,
  housing: '自有有貸款',
  note: '',
})
const applicationStep = ref(1)
const applicationConsent = ref(true)
const applicationDocuments = ref(['身分證正反面.jpg', '近六個月薪轉存摺.pdf'])
const submittingApplication = ref(false)
const submittedApplicationId = ref('')

const applicationPayment = computed(() => pmt(application.amount, 4.88, application.term))
const applicationDbr = computed(() => {
  if (!application.income) return 0
  return applicationPayment.value * 12 / application.income * 100
})
const reviewRows = computed<ModalRow[]>(() => [
  { label: '貸款產品', value: application.product },
  { label: '申請金額', value: formatNT(application.amount) },
  { label: '期數', value: `${application.term} 期` },
  { label: '資金用途', value: application.purpose },
  { label: '預估月付金', value: formatNT(applicationPayment.value), tone: 'brand-text' },
  { label: '申請人', value: `${application.name} · ${application.idNumber}` },
  { label: '聯絡方式', value: `${application.phone} / ${application.email}` },
  { label: '職業／年資', value: `${application.job} · ${application.years}` },
  { label: '負債比試算', value: `${applicationDbr.value.toFixed(1)}% · ${applicationDbr.value < 22 ? '符合' : '需人工評估'}` },
  { label: '已上傳文件', value: `${applicationDocuments.value.length} 份` },
])

const fallbackLoans: Loan[] = [
  { id: 'LN-2025-08841', product: '個人信用貸款', amount: 500000, rate: 4.88, paidInstallments: 27, totalInstallments: 36, status: '正常繳款', statusTone: 'success' },
  { id: 'LN-2024-11207', product: '汽車貸款', amount: 680000, rate: 3.45, paidInstallments: 18, totalInstallments: 60, status: '正常繳款', statusTone: 'success' },
  { id: 'LN-2026-00412', product: '企業週轉金', amount: 1200000, rate: 5.2, paidInstallments: 0, totalInstallments: 0, status: '審核中', statusTone: 'warning' },
  { id: 'LN-2023-07733', product: '信用卡代償', amount: 250000, rate: 6.1, paidInstallments: 24, totalInstallments: 24, status: '已結清', statusTone: 'info' },
]
const dashboardLoans = ref<Loan[]>(fallbackLoans)
const dashboardSummary = reactive({
  totalBorrowed: 1430000,
  monthlyPayment: 14982,
  creditScore: 782,
  repaymentRate: 97.6,
})

const fallbackListings: Listing[] = [
  { id: 'A-2291', purpose: '債務整合', grade: 'A', rate: 5.8, amount: 500000, term: 36, funded: 78, job: '上市櫃員工', years: '5 年以上', region: '台北市' },
  { id: 'B-1187', purpose: '創業週轉', grade: 'B', rate: 7.2, amount: 1200000, term: 48, funded: 45, job: '自營商', years: '3~5 年', region: '台中市' },
  { id: 'A-2288', purpose: '裝潢修繕', grade: 'A', rate: 4.9, amount: 300000, term: 24, funded: 92, job: '軍公教', years: '5 年以上', region: '新北市' },
  { id: 'C-0442', purpose: '醫療支出', grade: 'C', rate: 9.6, amount: 180000, term: 18, funded: 31, job: '服務業', years: '1~3 年', region: '高雄市' },
  { id: 'B-1190', purpose: '教育進修', grade: 'B', rate: 6.5, amount: 400000, term: 36, funded: 66, job: '一般企業', years: '3~5 年', region: '桃園市' },
  { id: 'A-2295', purpose: '汽車購置', grade: 'A', rate: 5.2, amount: 680000, term: 60, funded: 12, job: '上市櫃員工', years: '5 年以上', region: '新竹市' },
  { id: 'B-1193', purpose: '信用卡代償', grade: 'B', rate: 7.8, amount: 250000, term: 24, funded: 88, job: '一般企業', years: '3~5 年', region: '台南市' },
  { id: 'C-0448', purpose: '營運週轉', grade: 'C', rate: 10.4, amount: 900000, term: 36, funded: 22, job: '自營商', years: '1~3 年', region: '彰化縣' },
]
const listings = ref<Listing[]>(fallbackListings)
const marketGrade = ref('全部')
const marketTerm = ref('全部')
const marketRate = ref('不限')
const filteredListings = computed(() => listings.value.filter((listing) => {
  if (marketGrade.value !== '全部' && listing.grade !== marketGrade.value) return false
  if (marketTerm.value === '12 期以下' && listing.term > 12) return false
  if (marketTerm.value === '24~36 期' && (listing.term < 24 || listing.term > 36)) return false
  if (marketTerm.value === '48 期以上' && listing.term < 48) return false
  if (marketRate.value === '4% 以上' && listing.rate < 4) return false
  if (marketRate.value === '6% 以上' && listing.rate < 6) return false
  if (marketRate.value === '8% 以上' && listing.rate < 8) return false
  return true
}))

const amortization = computed(() => {
  const principal = 500000
  const rate = 4.88
  const term = 36
  const monthly = pmt(principal, rate, term)
  let balance = principal
  return Array.from({ length: term }, (_, index) => {
    const installment = index + 1
    const interest = balance * rate / 100 / 12
    const principalPaid = monthly - interest
    balance = Math.max(0, balance - principalPaid)
    return {
      installment,
      date: `202${installment < 4 ? 5 : 6}/${String((installment + 9) % 12 + 1).padStart(2, '0')}/15`,
      monthly,
      principal: principalPaid,
      interest,
      balance,
      status: installment <= 9 ? '已繳' : installment === 10 ? '待繳' : '未到期',
      statusTone: installment <= 9 ? 'success' : installment === 10 ? 'warning' : 'info',
    }
  })
})

const adminTab = ref<'pending' | 'overdue' | 'portfolio'>('pending')
const pendingApplications = [
  { id: 'LN-2026-00519', applicant: '陳＊宏', product: '信貸', amount: 500000, score: 782, dbr: '17.4', flag: '—', recommendation: '建議核准', tone: 'success' as const, action: '核准' },
  { id: 'LN-2026-00518', applicant: '林＊芳', product: '企業週轉', amount: 1200000, score: 648, dbr: '21.8', flag: '近期查詢 5 次', recommendation: '需補件', tone: 'warning' as const, action: '要求補件' },
  { id: 'LN-2026-00517', applicant: '王＊翔', product: '信貸', amount: 300000, score: 735, dbr: '14.2', flag: '—', recommendation: '建議核准', tone: 'success' as const, action: '核准' },
  { id: 'LN-2026-00516', applicant: '張＊涵', product: '信用卡代償', amount: 450000, score: 512, dbr: '26.9', flag: 'DBR 超標', recommendation: '建議婉拒', tone: 'error' as const, action: '婉拒' },
]
const overdueLoans = [
  { id: 'LN-2024-09912', applicant: '黃＊真', overdue: 'M1 · 12 天', amount: 8420, balance: 214300, action: '簡訊+電話提醒', next: '08/06', tone: 'warning' as const },
  { id: 'LN-2023-04417', applicant: '鄭＊豪', overdue: 'M1 · 25 天', amount: 15600, balance: 388900, action: '協商還款計畫', next: '08/07', tone: 'warning' as const },
  { id: 'LN-2024-02205', applicant: '周＊庭', overdue: 'M2 · 48 天', amount: 29880, balance: 502100, action: '存證信函', next: '08/08', tone: 'error' as const },
  { id: 'LN-2022-08830', applicant: '謝＊安', overdue: 'M3+ · 96 天', amount: 61200, balance: 740500, action: '委外／法務', next: '08/12', tone: 'error' as const },
]

function navigate(page: PageKey) {
  activePage.value = page
  menuOpen.value = false
  if (import.meta.client) window.scrollTo({ top: 0, behavior: 'smooth' })
}

function showToast(message: string, kind: ToastKind = 'success') {
  toast.value = { message, kind }
  if (toastTimer) clearTimeout(toastTimer)
  toastTimer = setTimeout(() => { toast.value = null }, 3400)
}

function openModal(state: ModalState) {
  modal.value = state
}

function closeModal() {
  modal.value = null
}

function confirmModal() {
  if (!modal.value) return
  const kind = modal.value.kind
  closeModal()
  if (kind === 'details') navigate('repay')
  if (kind === 'bid') showToast('投標成功，資金已鎖定並等待標的完成募集')
  if (kind === 'payment') showToast('還款成功，剩餘本金 NT$ 368,982')
  if (kind === 'prepay') showToast('清償申請已送出，預計於 T+1 完成')
}

function addDocument() {
  const names = ['扣繳憑單_2025.pdf', '在職證明.jpg', '財力證明_定存單.pdf']
  const next = names.find((name) => !applicationDocuments.value.includes(name))
  if (!next) return showToast('展示資料中的文件已全部加入', 'info')
  applicationDocuments.value.push(next)
  showToast(`「${next}」上傳成功，OCR 驗證通過`)
}

function nextApplicationStep() {
  if (applicationStep.value < 4) applicationStep.value += 1
}

function previousApplicationStep() {
  if (applicationStep.value > 1) applicationStep.value -= 1
}

async function submitApplication() {
  if (!applicationConsent.value) {
    showToast('請先勾選同意服務條款與個資告知事項', 'error')
    return
  }
  submittingApplication.value = true
  try {
    const response = await $fetch<{ id: string }>('/api/v1/applications', {
      method: 'POST',
      body: {
        product: application.product,
        amount: application.amount,
        termMonths: application.term,
        purpose: application.purpose,
        applicantName: application.name,
        idNumber: application.idNumber,
        phone: application.phone,
        email: application.email,
        job: application.job,
        employmentYears: application.years,
        annualIncome: application.income,
        monthlyExpenses: application.expenses,
        housing: application.housing,
        note: application.note,
        documents: applicationDocuments.value,
      },
    })
    submittedApplicationId.value = response.id
    applicationStep.value = 5
    showToast('申請已送出，AI 初審完成')
  } catch {
    showToast('目前無法連線到申請服務，請稍後再試', 'error')
  } finally {
    submittingApplication.value = false
  }
}

function openLoanDetails(loan: Loan) {
  openModal({
    kind: 'details',
    title: `合約明細 ${loan.id}`,
    description: `${loan.product} · 本息平均攤還`,
    rows: [
      { label: '核貸金額', value: formatNT(loan.amount) },
      { label: '年利率 / APR', value: `${loan.rate.toFixed(2)}% / 5.42%` },
      { label: '期數', value: `${loan.totalInstallments || 36} 期（月繳）` },
      { label: '剩餘本金', value: formatNT(Math.max(0, loan.amount - loan.paidInstallments * 12000)) },
      { label: '繳款紀錄', value: loan.status, tone: loan.statusTone },
      { label: '資金來源', value: 'P2P 出借人 27 位' },
    ],
    confirmLabel: '前往還款管理',
  })
}

function openPaymentModal() {
  openModal({
    kind: 'payment',
    title: '確認還款',
    description: '將由約定帳戶（玉山銀行 ****3391）扣款',
    rows: [
      { label: '還款金額', value: 'NT$ 14,982', tone: 'brand-text' },
      { label: '合約', value: 'LN-2025-08841' },
      { label: '期數', value: '第 10 / 36 期' },
    ],
    confirmLabel: '確認扣款',
  })
}

function openPrepayModal() {
  openModal({
    kind: 'prepay',
    title: '提前清償試算',
    description: '合約 LN-2025-08841 · 一次結清剩餘本金',
    rows: [
      { label: '剩餘本金', value: 'NT$ 382,410' },
      { label: '結清日應付利息', value: 'NT$ 812' },
      { label: '提前清償違約金', value: 'NT$ 0（免收）', tone: 'success' },
      { label: '應付總額', value: 'NT$ 383,222', tone: 'brand-text' },
      { label: '可節省未到期利息', value: 'NT$ 21,470', tone: 'success' },
    ],
    confirmLabel: '申請清償',
  })
}

const selectedListing = ref<Listing | null>(null)
const bidAmount = ref(30000)
const bidYield = computed(() => selectedListing.value ? bidAmount.value * selectedListing.value.rate / 100 : 0)

function openBidModal(listing: Listing) {
  selectedListing.value = listing
  bidAmount.value = Math.min(30000, Math.round(listing.amount * (100 - listing.funded) / 100))
  openModal({
    kind: 'bid',
    title: `投標 · 標的 #${listing.id}`,
    description: `年化報酬 ${listing.rate.toFixed(1)}% · 尚可投 ${formatNT(listing.amount * (100 - listing.funded) / 100)}`,
    rows: [
      { label: '單筆上限', value: 'NT$ 100,000' },
      { label: '預估年收益', value: formatNT(bidYield.value), tone: 'brand-text' },
      { label: '撥款後首次收息', value: '次月 10 日' },
    ],
    confirmLabel: '確認投標',
  })
}

function filterMarket() {
  showToast(`篩選完成，符合 ${filteredListings.value.length} 筆標的`, 'info')
}

function adminAction(id: string, action: string) {
  showToast(`${id} 已標記為「${action}」，通知已發送給申請人`)
}

function openNotice(title: string, description: string) {
  openModal({ kind: 'notice', title, description, rows: [], confirmLabel: '知道了' })
}

watch(bidAmount, () => {
  if (modal.value?.kind === 'bid' && modal.value.rows[1]) {
    modal.value.rows[1].value = formatNT(bidYield.value)
  }
})

onMounted(async () => {
  try {
    await $fetch<{ status: string }>('/api/health')
    apiStatus.value = 'online'
    apiMessage.value = 'Go API · PostgreSQL 已連線'

    const dashboard = await $fetch<DashboardResponse>('/api/v1/dashboard')
    if (dashboard.loans?.length) dashboardLoans.value = dashboard.loans
    if (dashboard.summary) Object.assign(dashboardSummary, dashboard.summary)

    const remoteListings = await $fetch<Listing[]>('/api/v1/market/listings')
    if (remoteListings?.length) listings.value = remoteListings
  } catch {
    apiStatus.value = 'offline'
    apiMessage.value = '展示模式 · API 尚未啟動'
  }
})
</script>

<template>
  <div class="app-shell">
    <a class="skip-link" href="#main-content">跳到主要內容</a>
    <header class="site-header">
      <div class="container nav-bar">
        <button class="brand" type="button" aria-label="回到首頁" @click="navigate('home')">
          <span class="brand-mark">₵</span>
          <span>CreditFlow <small>信達金融</small></span>
        </button>

        <button class="menu-toggle" type="button" aria-label="開啟選單" :aria-expanded="menuOpen" @click="menuOpen = !menuOpen">☰</button>
        <nav class="main-nav" :class="{ open: menuOpen }" aria-label="主要導覽">
          <button
            v-for="item in navItems"
            :key="item.key"
            type="button"
            :class="{ active: activePage === item.key }"
            :aria-current="activePage === item.key ? 'page' : undefined"
            @click="navigate(item.key)"
          >
            <span class="nav-icon">{{ item.icon }}</span>{{ item.label }}
          </button>
        </nav>

        <div class="user-box">
          <div class="avatar">陳</div>
          <div><strong>陳建宏</strong><small>信用等級 A · VIP</small></div>
        </div>
      </div>
    </header>

    <main id="main-content" tabindex="-1">
      <section v-if="activePage === 'home'" class="page active-page">
        <div class="container hero-grid">
          <div class="hero-copy">
            <span class="eyebrow"><i class="pulse-dot" />智慧借貸，讓資金流動更有價值</span>
            <h1>把每一次資金需求，<em>變成前進的力量</em></h1>
            <p>CreditFlow 以透明定價、即時風控與多元資金來源，為借款人與出借人建立更值得信賴的金融連結。</p>
            <div class="hero-actions">
              <button class="btn primary" type="button" @click="navigate('apply')">立即試算並申請 <span>→</span></button>
              <button class="btn" type="button" @click="navigate('market')">我要出借投資</button>
            </div>
            <div class="trust-stats">
              <div><strong>98.6%</strong><small>案件即時回覆率</small></div>
              <div><strong>NT$ 86 億</strong><small>平台累計撮合金額</small></div>
              <div><strong>4.8 / 5</strong><small>用戶服務評分</small></div>
            </div>
          </div>

          <div class="calculator-card">
            <div class="card-heading"><div><span class="section-kicker">QUICK CALCULATOR</span><h2>貸款試算</h2></div><span class="heading-badge">即時估算</span></div>
            <div class="range-field">
              <div class="range-label"><label for="calc-amount">借款金額</label><strong>{{ formatNT(calculator.amount) }}</strong></div>
              <input id="calc-amount" v-model.number="calculator.amount" class="range-input" type="range" min="50000" max="3000000" step="10000">
              <div class="range-hints"><span>50,000</span><span>3,000,000</span></div>
            </div>
            <div class="range-field">
              <div class="range-label"><label for="calc-term">借款期數</label><strong>{{ calculator.term }} 期</strong></div>
              <input id="calc-term" v-model.number="calculator.term" class="range-input" type="range" min="6" max="84" step="6">
              <div class="range-hints"><span>6 期</span><span>84 期</span></div>
            </div>
            <div class="field compact-field"><label for="calc-grade">信用等級</label><select id="calc-grade" v-model.number="calculator.rate"><option v-for="option in rateOptions" :key="option.value" :value="option.value">{{ option.label }}</option></select></div>
            <div class="calculator-output">
              <div class="output-row highlight"><span>每月應繳</span><strong>{{ formatNT(calculatorPayment) }}</strong></div>
              <div class="output-row"><span>總利息支出</span><strong>{{ formatNT(calculatorInterest) }}</strong></div>
              <div class="output-row"><span>總還款金額</span><strong>{{ formatNT(calculatorTotal) }}</strong></div>
              <div class="output-row"><span>開辦費（1.5%，內含）</span><strong>{{ formatNT(calculatorFee) }}</strong></div>
            </div>
            <button class="btn primary full" type="button" @click="application.amount = calculator.amount; application.term = calculator.term; navigate('apply')">帶入資料去申請 <span>→</span></button>
          </div>
        </div>

        <div class="container feature-grid">
          <article class="feature-card"><span class="feature-icon cyan">✦</span><h3>透明定價</h3><p>利率、費用與還款明細一次掌握，沒有模糊不清的隱藏成本。</p></article>
          <article class="feature-card"><span class="feature-icon blue">⌁</span><h3>AI 智慧風控</h3><p>多維度資料分析，讓每個決策都有依據，也讓好信用更有價值。</p></article>
          <article class="feature-card"><span class="feature-icon green">↗</span><h3>靈活資金</h3><p>媒合多元出借資金，依需求找到合適的額度與期數。</p></article>
          <article class="feature-card"><span class="feature-icon purple">◌</span><h3>全程線上</h3><p>申請、審核、撥款與還款，使用一個平台就能輕鬆管理。</p></article>
        </div>
      </section>

      <section v-else-if="activePage === 'dashboard'" class="page active-page">
        <div class="container">
          <div class="page-intro"><div><span class="section-kicker">MY FINANCE</span><h1>我的儀表板</h1><p>掌握目前貸款、還款與信用狀態</p></div><div class="api-indicator" :class="apiStatus"><i />{{ apiMessage }}</div></div>
          <div class="kpi-grid">
            <article class="stat-card"><span>目前借款總額</span><strong>{{ formatNT(dashboardSummary.totalBorrowed) }}</strong><small class="positive">較上月 ↓ 8.4%</small></article>
            <article class="stat-card"><span>本月應繳</span><strong>{{ formatNT(dashboardSummary.monthlyPayment) }}</strong><small>下次扣款日 08/15</small></article>
            <article class="stat-card"><span>信用評分</span><strong>{{ dashboardSummary.creditScore }} <em>A</em></strong><div class="progress"><i style="width:78.2%" /></div><small>較上月 ↑ 12 分</small></article>
            <article class="stat-card"><span>正常還款率</span><strong>{{ dashboardSummary.repaymentRate.toFixed(1) }}%</strong><small class="positive">連續 9 期準時繳款</small></article>
          </div>
          <div class="content-grid wide-main">
            <article class="surface-card">
              <div class="card-title-row"><h2>我的貸款合約</h2><button class="btn small" type="button" @click="navigate('apply')">＋ 新增申請</button></div>
              <div class="table-scroll"><table><thead><tr><th>案件編號</th><th>產品</th><th>金額</th><th>利率</th><th>進度</th><th>狀態</th><th /></tr></thead><tbody><tr v-for="loan in dashboardLoans" :key="loan.id"><td class="mono">{{ loan.id }}</td><td>{{ loan.product }}</td><td class="mono">{{ formatNT(loan.amount) }}</td><td class="mono">{{ loan.rate.toFixed(2) }}%</td><td>{{ loan.totalInstallments ? `${loan.paidInstallments} / ${loan.totalInstallments}` : '審核中' }}</td><td><span class="tag" :class="loan.statusTone">{{ loan.status }}</span></td><td><button class="btn small" type="button" @click="openLoanDetails(loan)">明細</button></td></tr></tbody></table></div>
            </article>
            <aside class="surface-card score-card"><div class="card-title-row"><h2>信用健康度</h2><span class="tag success">良好</span></div><div class="score-ring"><div><strong>{{ dashboardSummary.creditScore }}</strong><span>/ 900</span></div></div><p>比 92% 的同齡用戶高</p><div class="metric-list"><div><span>繳款紀錄</span><strong>優良</strong></div><div><span>負債比</span><strong>17.4%</strong></div><div><span>近期查詢</span><strong>1 次</strong></div></div></aside>
          </div>
          <div class="content-grid two-columns"><article class="surface-card"><div class="card-title-row"><h2>近期交易紀錄</h2><button class="btn small" type="button" @click="showToast('交易紀錄已匯出 CSV', 'info')">匯出 CSV</button></div><div class="activity-list"><div><span class="activity-icon green">↓</span><div><strong>本期還款入帳</strong><small>LN-2025-08841 · 2026/07/15</small></div><b>− NT$ 14,982</b></div><div><span class="activity-icon blue">＋</span><div><strong>核貸款項撥款</strong><small>LN-2024-11207 · 2026/07/03</small></div><b>＋ NT$ 22,400</b></div><div><span class="activity-icon purple">◈</span><div><strong>信用評分更新</strong><small>系統自動更新 · 2026/07/01</small></div><b>782 分</b></div></div></article><article class="surface-card"><div class="card-title-row"><h2>待辦提醒</h2><span class="tag warning">2 項</span></div><div class="reminder-list"><div><div><strong>補件通知</strong><small>LN-2026-00412 需補上營業稅單</small></div><button class="btn small" type="button" @click="openNotice('文件補件', '請上傳最新一期營業稅單，完成後將進入人工覆核。')">處理</button></div><div><div><strong>3 檔新標的上架</strong><small>符合你的自動投標條件</small></div><button class="btn small" type="button" @click="navigate('market')">查看</button></div></div></article></div>
        </div>
      </section>

      <section v-else-if="activePage === 'apply'" class="page active-page">
        <div class="container narrow-page"><div class="page-intro"><div><span class="section-kicker">LOAN APPLICATION</span><h1>申請貸款</h1><p>約 3 分鐘完成資料填寫，AI 初審即時回覆</p></div><span class="secure-label">⌁ 資料全程加密</span></div>
          <div v-if="applicationStep < 5" class="stepper"><div v-for="step in 4" :key="step" :class="{ active: applicationStep === step, done: applicationStep > step }"><span>{{ applicationStep > step ? '✓' : step }}</span><small>{{ ['貸款需求', '個人資料', '上傳文件', '確認送出'][step - 1] }}</small></div></div>
          <article v-if="applicationStep === 1" class="surface-card form-card"><div class="form-heading"><h2>先告訴我們你的貸款需求</h2><p>依照你的條件提供初步試算結果</p></div><div class="form-grid"><div class="field"><label for="app-product">貸款產品</label><select id="app-product" v-model="application.product"><option>個人信用貸款</option><option>汽車貸款</option><option>企業週轉金</option><option>信用卡代償</option></select></div><div class="field"><label for="app-purpose">資金用途</label><select id="app-purpose" v-model="application.purpose"><option>債務整合</option><option>裝潢修繕</option><option>創業／營運週轉</option><option>教育進修</option><option>醫療支出</option><option>其他</option></select></div><div class="field"><label for="app-amount">申請金額（NT$）</label><input id="app-amount" v-model.number="application.amount" type="number" min="50000" max="3000000" step="10000"></div><div class="field"><label for="app-term">還款期數</label><select id="app-term" v-model.number="application.term"><option :value="12">12 期</option><option :value="24">24 期</option><option :value="36">36 期</option><option :value="48">48 期</option><option :value="60">60 期</option><option :value="84">84 期</option></select></div></div><div class="estimate-box"><div><span>預估月付金</span><strong>{{ formatNT(applicationPayment) }}</strong></div><div><span>預估總費用年百分率（APR）</span><b>5.42%</b></div><div><span>參考利率</span><b>4.88%</b></div></div><div class="form-actions end"><button class="btn primary" type="button" @click="nextApplicationStep">下一步 <span>→</span></button></div></article>
          <article v-else-if="applicationStep === 2" class="surface-card form-card"><div class="form-heading"><h2>建立你的申請人資料</h2><p>資料僅用於身分驗證與授信評估</p></div><div class="form-grid"><div class="field"><label for="app-name">姓名</label><input id="app-name" v-model="application.name"></div><div class="field"><label for="app-id">身分證字號</label><input id="app-id" v-model="application.idNumber"></div><div class="field"><label for="app-phone">行動電話</label><input id="app-phone" v-model="application.phone"></div><div class="field"><label for="app-email">電子信箱</label><input id="app-email" v-model="application.email" type="email"></div><div class="field"><label for="app-job">職業</label><select id="app-job" v-model="application.job"><option>軍公教</option><option>上市櫃公司員工</option><option>一般企業員工</option><option>自營商／SOHO</option><option>自由工作者</option></select></div><div class="field"><label for="app-years">年資</label><select id="app-years" v-model="application.years"><option>未滿 1 年</option><option>1~3 年</option><option>3~5 年</option><option>5 年以上</option></select></div><div class="field"><label for="app-income">年收入（NT$）</label><input id="app-income" v-model.number="application.income" type="number" step="10000"></div><div class="field"><label for="app-expenses">每月固定支出（NT$）</label><input id="app-expenses" v-model.number="application.expenses" type="number" step="1000"></div><div class="field full-field"><label for="app-housing">居住狀況</label><select id="app-housing" v-model="application.housing"><option>自有無貸款</option><option>自有有貸款</option><option>租屋</option><option>與父母同住</option></select></div></div><div class="form-actions"><button class="btn" type="button" @click="previousApplicationStep">← 上一步</button><button class="btn primary" type="button" @click="nextApplicationStep">下一步 <span>→</span></button></div></article>
          <article v-else-if="applicationStep === 3" class="surface-card form-card"><div class="form-heading"><h2>上傳必要文件</h2><p>支援 PDF、JPG、PNG，單檔上限 10 MB</p></div><button class="upload-box" type="button" @click="addDocument"><span>↑</span><strong>點擊或拖曳上傳</strong><small>身分證 · 薪轉存摺 · 扣繳憑單 · 財力證明</small></button><div class="document-list"><div v-for="document in applicationDocuments" :key="document"><span>▤</span>{{ document }}<span class="tag success">已驗證</span></div></div><div class="field note-field"><label for="app-note">備註（選填）</label><textarea id="app-note" v-model="application.note" rows="3" placeholder="有其他想讓審核人員了解的資訊嗎？" /></div><div class="form-actions"><button class="btn" type="button" @click="previousApplicationStep">← 上一步</button><button class="btn primary" type="button" @click="nextApplicationStep">下一步 <span>→</span></button></div></article>
          <article v-else-if="applicationStep === 4" class="surface-card form-card"><div class="form-heading"><h2>確認申請內容</h2><p>請確認資料無誤後送出申請</p></div><div class="review-grid"><div v-for="row in reviewRows" :key="row.label"><span>{{ row.label }}</span><strong :class="row.tone">{{ row.value }}</strong></div></div><label class="consent"><input v-model="applicationConsent" type="checkbox"><span>我已閱讀並同意服務條款、個人資料告知事項及聯徵查詢授權。</span></label><div class="form-actions"><button class="btn" type="button" @click="previousApplicationStep">← 上一步</button><button class="btn primary" type="button" :disabled="submittingApplication" @click="submitApplication">{{ submittingApplication ? '送出中…' : '✓ 確認送出申請' }}</button></div></article>
          <article v-else class="surface-card success-card"><span class="success-icon">✓</span><span class="section-kicker">APPLICATION RECEIVED</span><h2>申請已成功送出</h2><p>AI 初審已完成，專員將於一個工作日內與你聯繫。</p><div class="case-number">案件編號 <strong>{{ submittedApplicationId || 'LN-2026-00519' }}</strong></div><div class="success-actions"><button class="btn" type="button" @click="applicationStep = 1">再申請一筆</button><button class="btn primary" type="button" @click="navigate('dashboard')">前往儀表板追蹤</button></div></article>
        </div>
      </section>

      <section v-else-if="activePage === 'market'" class="page active-page"><div class="container"><div class="page-intro"><div><span class="section-kicker">INVESTMENT MARKET</span><h1>投資市集</h1><p>分散配置，讓閒置資金創造穩定收益</p></div><button class="btn" type="button" @click="showToast('自動投標已啟用：A/B 級、6% 以上、單筆上限 3 萬', 'info')">⚙ 自動投標設定</button></div><div class="filter-bar"><div class="field"><label for="market-grade">信用等級</label><select id="market-grade" v-model="marketGrade"><option>全部</option><option>A</option><option>B</option><option>C</option></select></div><div class="field"><label for="market-term">期數</label><select id="market-term" v-model="marketTerm"><option>全部</option><option>12 期以下</option><option>24~36 期</option><option>48 期以上</option></select></div><div class="field"><label for="market-rate">最低年化</label><select id="market-rate" v-model="marketRate"><option>不限</option><option>4% 以上</option><option>6% 以上</option><option>8% 以上</option></select></div><button class="btn primary" type="button" @click="filterMarket">套用篩選</button><span class="filter-result">{{ filteredListings.length }} 筆可投資標的</span></div><div class="listing-grid"><article v-for="listing in filteredListings" :key="listing.id" class="listing-card"><div class="listing-heading"><div><strong>#{{ listing.id }} · {{ listing.purpose }}</strong><small>{{ listing.job }} · 年資 {{ listing.years }} · {{ listing.region }}</small></div><span class="grade" :class="listing.grade">{{ listing.grade }}</span></div><div class="listing-row"><span>年化報酬率</span><strong class="green-text large">{{ listing.rate.toFixed(1) }}%</strong></div><div class="listing-row"><span>借款金額 / 期數</span><strong>{{ formatNT(listing.amount) }} / {{ listing.term }} 期</strong></div><div class="listing-row"><span>每萬元月收息</span><strong>{{ formatNT(pmt(10000, listing.rate, listing.term) - 10000 / listing.term) }}</strong></div><div class="funding"><div class="progress"><i :style="{ width: `${listing.funded}%` }" /></div><div><span>已募集 {{ listing.funded }}%</span><span>剩 {{ formatNT(listing.amount * (100 - listing.funded) / 100) }}</span></div></div><button class="btn primary small full" type="button" @click="openBidModal(listing)">我要投標</button></article></div></div></section>

      <section v-else-if="activePage === 'repay'" class="page active-page"><div class="container"><div class="page-intro"><div><span class="section-kicker">REPAYMENT CENTER</span><h1>還款管理</h1><p>清楚掌握每一期還款進度與資金安排</p></div><button class="btn" type="button" @click="openPrepayModal">試算提前清償</button></div><div class="repay-banner"><div><span>LN-2025-08841 · 個人信用貸款</span><strong>第 10 / 36 期</strong><small>下一期繳款日 2026/08/15</small></div><div class="repay-amount"><span>本期應繳</span><strong>{{ formatNT(14982) }}</strong><button class="btn primary" type="button" @click="openPaymentModal">立即還款</button></div></div><article class="surface-card"><div class="card-title-row"><div><h2>攤還明細</h2><p class="card-subtitle">本息平均攤還 · 年利率 4.88%</p></div><button class="btn small" type="button" @click="showToast('攤還表已下載 PDF', 'info')">下載 PDF</button></div><div class="table-scroll repayment-table"><table><thead><tr><th>期數</th><th>繳款日</th><th>應繳金額</th><th>本金</th><th>利息</th><th>剩餘本金</th><th>狀態</th></tr></thead><tbody><tr v-for="row in amortization" :key="row.installment"><td class="mono">{{ row.installment }}</td><td class="mono">{{ row.date }}</td><td class="mono">{{ formatNT(row.monthly) }}</td><td class="mono">{{ formatNT(row.principal) }}</td><td class="mono">{{ formatNT(row.interest) }}</td><td class="mono">{{ formatNT(row.balance) }}</td><td><span class="tag" :class="row.statusTone">{{ row.status }}</span></td></tr></tbody></table></div></article></div></section>

      <section v-else class="page active-page"><div class="container"><div class="page-intro"><div><span class="section-kicker">RISK CONTROL CONSOLE</span><h1>風控後台</h1><p>授信審核 · 逾期催收 · 資產品質監控（僅限風控人員）</p></div><span class="secure-label">⌁ 內部權限模式</span></div><div class="tabs"><button type="button" :class="{ active: adminTab === 'pending' }" @click="adminTab = 'pending'">待審件 (14)</button><button type="button" :class="{ active: adminTab === 'overdue' }" @click="adminTab = 'overdue'">逾期案件 (6)</button><button type="button" :class="{ active: adminTab === 'portfolio' }" @click="adminTab = 'portfolio'">資產品質</button></div><article v-if="adminTab === 'pending'" class="surface-card"><div class="table-scroll"><table><thead><tr><th>案件編號</th><th>申請人</th><th>產品</th><th>金額</th><th>AI 評分</th><th>DBR</th><th>風險標記</th><th>建議</th><th>操作</th></tr></thead><tbody><tr v-for="item in pendingApplications" :key="item.id"><td class="mono">{{ item.id }}</td><td>{{ item.applicant }}</td><td>{{ item.product }}</td><td class="mono">{{ formatNT(item.amount) }}</td><td :class="item.tone">{{ item.score }}</td><td class="mono">{{ item.dbr }}</td><td>{{ item.flag }}</td><td><span class="tag" :class="item.tone">{{ item.recommendation }}</span></td><td><button class="btn small" type="button" @click="adminAction(item.id, item.action)">{{ item.action }}</button></td></tr></tbody></table></div></article><article v-else-if="adminTab === 'overdue'" class="surface-card"><div class="table-scroll"><table><thead><tr><th>合約編號</th><th>借款人</th><th>逾期天數</th><th>逾期金額</th><th>剩餘本金</th><th>催收階段</th><th>下次聯繫</th><th>操作</th></tr></thead><tbody><tr v-for="item in overdueLoans" :key="item.id"><td class="mono">{{ item.id }}</td><td>{{ item.applicant }}</td><td><span class="tag" :class="item.tone">{{ item.overdue }}</span></td><td class="mono">{{ formatNT(item.amount) }}</td><td class="mono">{{ formatNT(item.balance) }}</td><td>{{ item.action }}</td><td class="mono">{{ item.next }}</td><td><button class="btn small" type="button" @click="showToast(`${item.id} 已排入今日處理清單`, 'info')">處理</button></td></tr></tbody></table></div></article><div v-else class="portfolio-grid"><article class="surface-card portfolio-card"><div class="card-title-row"><h2>放款資產分級</h2><strong>NT$ 84.6 億</strong></div><div class="risk-bar"><i style="width:64%;background:#34d399" /><i style="width:22%;background:#3b82f6" /><i style="width:9%;background:#fbbf24" /><i style="width:3.2%;background:#fb923c" /><i style="width:1.8%;background:#f87171" /></div><div class="legend"><span><i class="dot green-bg" />A 級 64.0%</span><span><i class="dot blue-bg" />B 級 22.0%</span><span><i class="dot yellow-bg" />C 級 9.0%</span><span><i class="dot red-bg" />逾期 1.8%</span></div><div class="metric-list"><div><span>逾期率（30D+）</span><strong class="green-text">1.82% · 達標</strong></div><div><span>呆帳率（M3+）</span><strong class="green-text">0.64% · 達標</strong></div><div><span>核准率</span><strong>42.6%</strong></div><div><span>催收回收率（M1）</span><strong class="green-text">88.4%</strong></div></div></article><article class="surface-card portfolio-card"><div class="card-title-row"><h2>AI 風控模型狀態</h2><span class="tag success">穩定</span></div><div class="metric-list"><div><span>模型版本</span><strong class="mono">v4.2.1</strong></div><div><span>特徵數量</span><strong>186 項</strong></div><div><span>AUC（驗證集）</span><strong class="green-text">0.871</strong></div><div><span>KS 值</span><strong class="green-text">0.542</strong></div><div><span>PSI 穩定度</span><strong class="green-text">0.043 · 穩定</strong></div><div><span>自動決策比例</span><strong>73.8%</strong></div></div><h3 class="subheading">今日告警</h3><div class="alert-item"><div><strong>同一 IP 多筆申請</strong><small>偵測到 4 件來自同網段</small></div><span class="tag error">高</span></div><div class="alert-item"><div><strong>薪轉證明疑似變造</strong><small>LN-2026-00516 OCR 異常</small></div><span class="tag warning">中</span></div></article></div></div></section>
    </main>

    <footer class="site-footer"><div class="container footer-inner"><span>© 2026 CreditFlow 信達金融科技股份有限公司 · 展示資料皆為模擬數據</span><span>客服 0800-000-168 · 服務時間 09:00–18:00</span></div></footer>

    <div v-if="modal" class="modal-backdrop" role="presentation" @click.self="closeModal"><div class="modal-card" role="dialog" aria-modal="true" :aria-label="modal.title"><button class="modal-close" type="button" aria-label="關閉視窗" @click="closeModal">×</button><span class="section-kicker">{{ modal.kind === 'bid' ? 'PLACE BID' : modal.kind === 'payment' ? 'PAYMENT' : 'DETAIL' }}</span><h2>{{ modal.title }}</h2><p>{{ modal.description }}</p><div v-if="modal.kind === 'bid'" class="field"><label for="bid-amount">投標金額（NT$）</label><input id="bid-amount" v-model.number="bidAmount" type="number" min="1000" step="1000"></div><div v-if="modal.rows.length" class="review-grid modal-rows"><div v-for="row in modal.rows" :key="row.label"><span>{{ row.label }}</span><strong :class="row.tone">{{ row.value }}</strong></div></div><div class="modal-actions"><button class="btn" type="button" @click="closeModal">取消</button><button class="btn primary" type="button" @click="confirmModal">{{ modal.confirmLabel || '知道了' }}</button></div></div></div>
    <Transition name="toast"><div v-if="toast" class="toast-message" :class="toast.kind" role="status" aria-live="polite"><span aria-hidden="true">{{ toast.kind === 'success' ? '✓' : toast.kind === 'error' ? '!' : 'i' }}</span>{{ toast.message }}</div></Transition>
  </div>
</template>
