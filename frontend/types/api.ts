export type Role = 'borrower' | 'investor' | 'reviewer'
export type Tone = 'success' | 'warning' | 'info' | 'error'

export interface SessionUser {
  id: string
  email: string
  displayName: string
  role: Role
  creditScore: number
  /** 出借人的可用餘額；借款人一律為 0 */
  availableBalance: number
  /** 信箱是否已驗證。未驗證不阻擋登入，但不可送出貸款申請。 */
  emailVerified: boolean
}

export interface Loan {
  id: string
  product: string
  amount: number
  rate: number
  paidInstallments: number
  totalInstallments: number
  status: string
  statusTone: Tone
  monthlyPayment: number
}

export interface DashboardResponse {
  summary: {
    totalBorrowed: number
    monthlyPayment: number
    creditScore: number
    repaymentRate: number
  }
  loans: Loan[]
}

export interface Listing {
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

export interface ApplicationSummary {
  id: string
  userId: string
  applicantName: string
  product: string
  purpose: string
  amount: number
  termMonths: number
  /** 收入與支出只提供級距；完整金額需經風控的 reveal 稽核端點 */
  incomeRange: string
  expenseRange: string
  status: string
  createdAt: string
  /** PII 一律以遮罩形式回傳；完整值需經 reveal 稽核端點取得 */
  idNumberMasked: string
  phoneMasked: string
  /** 核准後的募資進度；尚未上架時 listingId 為空字串 */
  listingId: string
  fundedAmount: number
  fundedPercent: number
  estimatedPayment: number
  dbr: number
  creditScore: number
  grade: 'A' | 'B' | 'C'
  recommendation: string
  recommendTone: Tone
}

export interface Installment {
  installmentNo: number
  dueDate: string
  amountDue: number
  principal: number
  interest: number
  remainingBalance: number
  status: string
}

export interface ScheduleResponse {
  loan: Loan
  schedule: Installment[]
}

export type ReviewAction = 'approve' | 'reject' | 'request_more_info'

/** 申請狀態的中文標籤與配色，前後台共用。 */
export const applicationStatusMeta: Record<string, { label: string; tone: Tone }> = {
  pending: { label: '待審核', tone: 'warning' },
  reviewing: { label: '審核中', tone: 'warning' },
  approved: { label: '已核准', tone: 'success' },
  rejected: { label: '已婉拒', tone: 'error' },
  more_info_required: { label: '待補件', tone: 'warning' },
  // 核准後進入募資流程
  funding: { label: '募資中', tone: 'warning' },
  funded: { label: '已募滿', tone: 'info' },
  disbursed: { label: '已撥款', tone: 'success' },
}

export interface Repayment {
  id: number
  loanId: string
  installmentNo: number | null
  kind: 'installment' | 'prepayment'
  amount: number
  principal: number
  interest: number
  createdAt: string
}

export interface PayResponse {
  repayment: Repayment
  loan: Loan
  settled: boolean
}

export interface SettleResponse {
  repayment: Repayment
  loan: Loan
  settled: boolean
  waivedInterest: number
  closedCount: number
}

export interface OverdueItem {
  loanId: string
  installmentNo: number
  borrowerName: string
  product: string
  dueDate: string
  overdueDays: number
  amountDue: number
  remainingBalance: number
  stage: string
  stageTone: Tone
  action: string
}

/** 攤還期數狀態的中文標籤與配色。 */
export const installmentStatusMeta: Record<string, { label: string; tone: Tone }> = {
  scheduled: { label: '未到期', tone: 'info' },
  due: { label: '待繳', tone: 'warning' },
  paid: { label: '已繳', tone: 'success' },
  overdue: { label: '逾期', tone: 'error' },
}


// ---------------------------------------------------------------- P2P 撮合

export type ListingStatus = 'funding' | 'funded' | 'disbursed' | 'cancelled'

export interface ListingItem {
  id: string
  applicationId: string
  purpose: string
  grade: 'A' | 'B' | 'C'
  rate: number
  targetAmount: number
  fundedAmount: number
  termMonths: number
  job: string
  employmentYears: string
  region: string
  status: ListingStatus
  createdAt: string
  fundedAt: string | null
  fundingDeadline: string
  /** 距離募資截止的天數；負數表示已逾期（排程尚未掃到） */
  daysRemaining: number
  fundedPercent: number
  remainingAmount: number
  monthlyPayment: number
  investorCount: number
  /** 當前登入者已在此標的投入的金額；未登入為 0 */
  myInvestedAmount: number
}

export interface Investment {
  id: number
  listingId: string
  amount: number
  createdAt: string
  purpose: string
  grade: 'A' | 'B' | 'C'
  rate: number
  termMonths: number
  status: ListingStatus
  loanId: string | null
  estimatedReturn: number
  /** 實際已收回的金額，由借款人還款時按占比分潤累計 */
  principalReturned: number
  interestEarned: number
  outstandingPrincipal: number
}

export interface InvestResponse {
  investment: Investment
  listing: ListingItem
  balance: number
  fullyFunded: boolean
  disbursedLoan: Loan | null
}

export interface PortfolioResponse {
  balance: number
  totalInvested: number
  activeCount: number
  estimatedReturn: number
  weightedRate: number
  /** 實際已收回；與 estimatedReturn 的差距即為尚未實現的部分 */
  totalPrincipalReturned: number
  totalInterestEarned: number
  outstandingPrincipal: number
  investments: Investment[]
}

export interface DistributionRecord {
  id: number
  loanId: string
  listingId: string
  purpose: string
  principal: number
  interest: number
  total: number
  createdAt: string
}

/** 標的狀態的中文標籤與配色。 */
export const listingStatusMeta: Record<string, { label: string; tone: Tone }> = {
  funding: { label: '募資中', tone: 'warning' },
  funded: { label: '已募滿', tone: 'info' },
  disbursed: { label: '已撥款', tone: 'success' },
  cancelled: { label: '已取消', tone: 'error' },
}

/** 申請狀態補上募資階段。 */
export const fundingStatusMeta: Record<string, { label: string; tone: Tone }> = {
  funding: { label: '募資中', tone: 'warning' },
  funded: { label: '已募滿', tone: 'info' },
  disbursed: { label: '已撥款', tone: 'success' },
}

export interface DocumentRecord {
  id: string
  originalName: string
  contentType: string
  sizeBytes: number
  checksum: string
  createdAt: string
  /** OCR 擷取狀態；OCR 不判斷文件真偽，結果僅供風控參考 */
  ocrStatus: 'pending' | 'processing' | 'done' | 'failed' | 'skipped'
  ocrError?: string
  /** 各項目與申請人填寫資料的比對結果 */
  verifications?: Record<string, 'matched' | 'not_found' | 'unreadable'>
}

/** OCR 狀態的中文標籤與配色。 */
export const ocrStatusMeta: Record<string, { label: string; tone: Tone }> = {
  pending: { label: '待辨識', tone: 'info' },
  processing: { label: '辨識中', tone: 'warning' },
  done: { label: '已辨識', tone: 'success' },
  failed: { label: '辨識失敗', tone: 'error' },
  skipped: { label: '未辨識', tone: 'info' },
}

/** 比對結果的中文標籤與配色。 */
export const verificationMeta: Record<string, { label: string; tone: Tone }> = {
  matched: { label: '相符', tone: 'success' },
  not_found: { label: '未找到', tone: 'warning' },
  unreadable: { label: '無法辨識', tone: 'info' },
}

/** 比對項目的中文名稱。 */
export const verificationFieldLabels: Record<string, string> = {
  applicant_name: '姓名',
  id_number: '身分證',
}

/** 可接受的上傳型別；後端以 magic bytes 驗證，此處僅供 file input 提示。 */
export const acceptedDocumentTypes = '.pdf,.jpg,.jpeg,.png'
export const maxDocumentBytes = 10 * 1024 * 1024


/** 分頁後的清單回應。 */
export interface PageMeta {
  total: number
  limit: number
  offset: number
  hasMore: boolean
}

export interface Paginated<T> {
  items: T[]
  page: PageMeta
}
