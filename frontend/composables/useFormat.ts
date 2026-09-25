/** 金額與百分比格式化，前後台共用同一組規則。 */
export function formatNT(value: number) {
  return `NT$ ${Math.round(value || 0).toLocaleString('en-US')}`
}

export function formatDate(value: string) {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '—'
  return `${date.getFullYear()}/${String(date.getMonth() + 1).padStart(2, '0')}/${String(date.getDate()).padStart(2, '0')}`
}

/**
 * 本息平均攤還月付金。
 * 與後端 backend/cmd/api/finance.go 的 monthlyPayment() 為同一公式，
 * 兩側結果必須一致。
 */
export function pmt(principal: number, annualRate: number, months: number) {
  if (!months) return 0
  const monthlyRate = annualRate / 100 / 12
  return monthlyRate === 0
    ? principal / months
    : (principal * monthlyRate) / (1 - Math.pow(1 + monthlyRate, -months))
}
