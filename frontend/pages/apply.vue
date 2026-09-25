<script setup lang="ts">
import type { ModalRow } from '~/composables/useModal'

definePageMeta({ middleware: 'auth' })
useHead({ title: '申請貸款 · CreditFlow' })

const route = useRoute()
const { user } = useAuth()
const { mutate: sendMutation } = useCsrf()
const resending = ref(false)

// 未驗證信箱無法送出申請；在這裡提示而非讓使用者填完才被拒
const emailVerified = computed(() => user.value?.emailVerified !== false)

async function resendVerification() {
  resending.value = true
  try {
    await sendMutation('/api/v1/auth/resend-verification', { method: 'POST' })
    showToast('驗證信已重新寄出，請查看信箱')
  } catch (error) {
    showToast(apiErrorMessage(error, '重寄失敗'), 'error')
  } finally {
    resending.value = false
  }
}
const { mutate } = useCsrf()
const { showToast } = useToast()
const { loadDashboard, loadApplications } = useDashboard()

// 首頁試算機可透過 query 帶入金額與期數
const presetAmount = Number(route.query.amount)
const presetTerm = Number(route.query.term)
const allowedTerms = [12, 24, 36, 48, 60, 84]

const form = reactive({
  product: '個人信用貸款',
  amount: Number.isFinite(presetAmount) && presetAmount >= 50000 ? presetAmount : 500000,
  term: allowedTerms.includes(presetTerm) ? presetTerm : 36,
  purpose: '債務整合',
  name: user.value?.displayName ?? '',
  idNumber: '',
  phone: '',
  email: user.value?.email ?? '',
  job: '上市櫃公司員工',
  years: '3~5 年',
  income: 1080000,
  expenses: 32000,
  housing: '自有有貸款',
  note: '',
})

const step = ref(1)
const consent = ref(false)
const submitting = ref(false)
const creatingDraft = ref(false)

/**
 * 申請 id。文件必須附在既有申請上，因此進入上傳步驟前會先建立申請
 * （狀態 pending，風控看得到但尚未送出最終確認）。
 */
const applicationId = ref('')
const { documents } = useDocuments()

const payment = computed(() => pmt(form.amount, 4.88, form.term))
const dbr = computed(() => (form.income ? ((payment.value * 12) / form.income) * 100 : 0))

const reviewRows = computed<ModalRow[]>(() => [
  { label: '貸款產品', value: form.product },
  { label: '申請金額', value: formatNT(form.amount) },
  { label: '期數', value: `${form.term} 期` },
  { label: '資金用途', value: form.purpose },
  { label: '預估月付金', value: formatNT(payment.value), tone: 'brand-text' },
  { label: '申請人', value: `${form.name} · ${form.idNumber}` },
  { label: '聯絡方式', value: `${form.phone} / ${form.email}` },
  { label: '職業／年資', value: `${form.job} · ${form.years}` },
  { label: '負債比試算', value: `${dbr.value.toFixed(1)}% · ${dbr.value < 22 ? '符合' : '需人工評估'}` },
  { label: '已上傳文件', value: `${documents.value.length} 份` },
])

/**
 * 進入文件步驟前先建立申請，讓上傳有對象可附。
 * 已建立過就直接前進，避免重複送出。
 */
async function goToDocuments() {
  if (applicationId.value) {
    step.value = 3
    return
  }
  creatingDraft.value = true
  try {
    const response = await mutate<{ id: string }>('/api/v1/applications', {
      method: 'POST',
      body: applicationPayload(),
    })
    applicationId.value = response.id
    step.value = 3
  } catch (error) {
    showToast(apiErrorMessage(error, '無法建立申請，請確認資料是否完整'), 'error')
  } finally {
    creatingDraft.value = false
  }
}

/** 申請主體，建立與更新共用。 */
function applicationPayload() {
  return {
    product: form.product,
    amount: form.amount,
    termMonths: form.term,
    purpose: form.purpose,
    applicantName: form.name,
    idNumber: form.idNumber,
    phone: form.phone,
    email: form.email,
    job: form.job,
    employmentYears: form.years,
    annualIncome: form.income,
    monthlyExpenses: form.expenses,
    housing: form.housing,
    note: form.note,
    documents: [],
  }
}

async function submit() {
  if (!consent.value) {
    showToast('請先勾選同意服務條款與個資告知事項', 'error')
    return
  }
  // 申請在進入文件步驟時已建立；這裡只做最終確認
  if (!applicationId.value) {
    showToast('申請尚未建立，請回到上一步', 'error')
    return
  }
  submitting.value = true
  try {
    step.value = 5
    showToast('申請已送出，等待風控審核')
    await Promise.all([loadApplications(), loadDashboard()])
  } finally {
    submitting.value = false
  }
}

/** 重新開始一筆新申請。 */
function startOver() {
  applicationId.value = ''
  documents.value = []
  consent.value = false
  step.value = 1
}
</script>

<template>
  <section class="page active-page">
    <div class="container narrow-page">
      <div class="page-intro">
        <div>
          <span class="section-kicker">LOAN APPLICATION</span>
          <h1>申請貸款</h1>
          <p>約 3 分鐘完成資料填寫，送出後由風控人員審核</p>
        </div>
        <span class="secure-label">⌁ 身分證與電話加密儲存</span>
      </div>

      <article v-if="!emailVerified" class="surface-card">
        <div class="card-title-row">
          <h2>請先完成信箱驗證</h2>
          <StatusTag label="待驗證" tone="warning" />
        </div>
        <p class="card-note">
          送出貸款申請前需先驗證電子信箱（{{ user?.email }}）。
          請點擊我們寄出的驗證信中的連結；若未收到，可以重新寄送。
        </p>
        <div class="form-actions end">
          <button class="btn primary" type="button" :disabled="resending" @click="resendVerification">
            {{ resending ? '寄送中…' : '重新寄送驗證信' }}
          </button>
        </div>
      </article>

      <div v-if="emailVerified && step < 5" class="stepper">
        <div v-for="index in 4" :key="index" :class="{ active: step === index, done: step > index }">
          <span>{{ step > index ? '✓' : index }}</span>
          <small>{{ ['貸款需求', '個人資料', '上傳文件', '確認送出'][index - 1] }}</small>
        </div>
      </div>

      <article v-if="emailVerified && step === 1" class="surface-card form-card">
        <div class="form-heading"><h2>先告訴我們你的貸款需求</h2><p>依照你的條件提供初步試算結果</p></div>
        <div class="form-grid">
          <div class="field">
            <label for="app-product">貸款產品</label>
            <select id="app-product" v-model="form.product">
              <option>個人信用貸款</option><option>汽車貸款</option>
              <option>企業週轉金</option><option>信用卡代償</option>
            </select>
          </div>
          <div class="field">
            <label for="app-purpose">資金用途</label>
            <select id="app-purpose" v-model="form.purpose">
              <option>債務整合</option><option>裝潢修繕</option><option>創業／營運週轉</option>
              <option>教育進修</option><option>醫療支出</option><option>其他</option>
            </select>
          </div>
          <div class="field">
            <label for="app-amount">申請金額（NT$）</label>
            <input id="app-amount" v-model.number="form.amount" type="number" min="50000" max="3000000" step="10000">
          </div>
          <div class="field">
            <label for="app-term">還款期數</label>
            <select id="app-term" v-model.number="form.term">
              <option v-for="term in allowedTerms" :key="term" :value="term">{{ term }} 期</option>
            </select>
          </div>
        </div>
        <div class="estimate-box">
          <div><span>預估月付金</span><strong>{{ formatNT(payment) }}</strong></div>
          <div><span>參考利率（A 級）</span><b>4.88%</b></div>
          <div><span>負債比試算</span><b>{{ dbr.toFixed(1) }}%</b></div>
        </div>
        <div class="form-actions end">
          <button class="btn primary" type="button" @click="step = 2">下一步 <span>→</span></button>
        </div>
      </article>

      <article v-else-if="step === 2" class="surface-card form-card">
        <div class="form-heading"><h2>建立你的申請人資料</h2><p>身分證與電話會加密儲存，清單頁只顯示遮罩</p></div>
        <div class="form-grid">
          <div class="field"><label for="app-name">姓名</label><input id="app-name" v-model="form.name"></div>
          <div class="field"><label for="app-id">身分證字號</label><input id="app-id" v-model="form.idNumber"></div>
          <div class="field"><label for="app-phone">行動電話</label><input id="app-phone" v-model="form.phone"></div>
          <div class="field"><label for="app-email">電子信箱</label><input id="app-email" v-model="form.email" type="email"></div>
          <div class="field">
            <label for="app-job">職業</label>
            <select id="app-job" v-model="form.job">
              <option>軍公教</option><option>上市櫃公司員工</option><option>一般企業員工</option>
              <option>自營商／SOHO</option><option>自由工作者</option>
            </select>
          </div>
          <div class="field">
            <label for="app-years">年資</label>
            <select id="app-years" v-model="form.years">
              <option>未滿 1 年</option><option>1~3 年</option><option>3~5 年</option><option>5 年以上</option>
            </select>
          </div>
          <div class="field"><label for="app-income">年收入（NT$）</label><input id="app-income" v-model.number="form.income" type="number" step="10000"></div>
          <div class="field"><label for="app-expenses">每月固定支出（NT$）</label><input id="app-expenses" v-model.number="form.expenses" type="number" step="1000"></div>
          <div class="field full-field">
            <label for="app-housing">居住狀況</label>
            <select id="app-housing" v-model="form.housing">
              <option>自有無貸款</option><option>自有有貸款</option><option>租屋</option><option>與父母同住</option>
            </select>
          </div>
        </div>
        <div class="form-actions">
          <button class="btn" type="button" @click="step = 1">← 上一步</button>
          <button class="btn primary" type="button" :disabled="creatingDraft" @click="goToDocuments">
            {{ creatingDraft ? '建立申請中…' : '下一步' }} <span>→</span>
          </button>
        </div>
      </article>

      <article v-else-if="step === 3" class="surface-card form-card">
        <div class="form-heading">
          <h2>上傳必要文件</h2>
          <p>身分證 · 薪轉存摺 · 扣繳憑單 · 財力證明（案件編號 {{ applicationId }}）</p>
        </div>

        <DocumentUploader :application-id="applicationId" />

        <div class="field note-field">
          <label for="app-note">備註（選填）</label>
          <textarea id="app-note" v-model="form.note" rows="3" placeholder="有其他想讓審核人員了解的資訊嗎？" />
        </div>
        <div class="form-actions">
          <button class="btn" type="button" @click="step = 2">← 上一步</button>
          <button class="btn primary" type="button" @click="step = 4">下一步 <span>→</span></button>
        </div>
      </article>

      <article v-else-if="step === 4" class="surface-card form-card">
        <div class="form-heading"><h2>確認申請內容</h2><p>請確認資料無誤後送出申請</p></div>
        <div class="review-grid">
          <div v-for="row in reviewRows" :key="row.label">
            <span>{{ row.label }}</span><strong :class="row.tone">{{ row.value }}</strong>
          </div>
        </div>
        <label class="consent">
          <input v-model="consent" type="checkbox">
          <span>我已閱讀並同意服務條款、個人資料告知事項及聯徵查詢授權。</span>
        </label>
        <div class="form-actions">
          <button class="btn" type="button" @click="step = 3">← 上一步</button>
          <button class="btn primary" type="button" :disabled="submitting" @click="submit">
            {{ submitting ? '送出中…' : '✓ 確認送出申請' }}
          </button>
        </div>
      </article>

      <article v-else class="surface-card success-card">
        <span class="success-icon">✓</span>
        <span class="section-kicker">APPLICATION RECEIVED</span>
        <h2>申請已成功送出</h2>
        <p>風控人員將進行審核並查看你上傳的文件，核准後標的會上架募資。</p>
        <div class="case-number">案件編號 <strong>{{ applicationId }}</strong></div>
        <div class="success-actions">
          <button class="btn" type="button" @click="startOver">再申請一筆</button>
          <NuxtLink class="btn primary" to="/dashboard">前往儀表板追蹤</NuxtLink>
        </div>
      </article>
    </div>
  </section>
</template>
