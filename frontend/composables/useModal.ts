import type { Tone } from '~/types/api'

export type ModalKind = 'details' | 'payment' | 'prepay' | 'bid' | 'notice' | 'review'

export interface ModalRow {
  label: string
  value: string
  tone?: Tone | 'brand-text'
}

export interface ModalState {
  kind: ModalKind
  title: string
  description: string
  rows: ModalRow[]
  confirmLabel?: string
  /**
   * 需要使用者輸入文字時的欄位設定（例如審核理由）。
   * 對話框在 layout 中渲染，頁面無法傳 slot，故以資料描述取代。
   */
  input?: {
    label: string
    placeholder?: string
    required?: boolean
  }
  /** 按下確認時執行；input 的內容由參數傳入。回傳 Promise 時顯示忙碌狀態。 */
  onConfirm?: (inputValue: string) => void | Promise<void>
}

/**
 * 跨頁共用的對話框。
 *
 * 確認行為由開啟方以 onConfirm 提供，因此 ModalDialog 元件不需要知道
 * 任何業務邏輯，也不需要一個集中的 switch 來分派各種 kind。
 */
export function useModal() {
  const modal = useState<ModalState | null>('modal', () => null)
  const busy = useState<boolean>('modal-busy', () => false)
  const inputValue = useState<string>('modal-input', () => '')
  const inputError = useState<string>('modal-input-error', () => '')

  function openModal(state: ModalState) {
    modal.value = state
    busy.value = false
    inputValue.value = ''
    inputError.value = ''
  }

  function closeModal() {
    modal.value = null
    busy.value = false
    inputValue.value = ''
    inputError.value = ''
  }

  async function confirmModal() {
    const current = modal.value
    if (!current) return

    if (current.input?.required && !inputValue.value.trim()) {
      inputError.value = `請填寫${current.input.label}`
      return
    }
    inputError.value = ''

    const handler = current.onConfirm
    if (!handler) {
      closeModal()
      return
    }
    busy.value = true
    try {
      await handler(inputValue.value)
    } finally {
      busy.value = false
    }
  }

  return { modal, busy, inputValue, inputError, openModal, closeModal, confirmModal }
}
