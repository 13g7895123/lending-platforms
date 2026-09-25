import type { Tone } from '~/types/api'

export type ToastKind = 'success' | 'error' | 'info'

interface ToastState {
  message: string
  kind: ToastKind
}

/**
 * 跨頁共用的短暫通知。
 * 狀態放在 useState，因此換頁不會讓正在顯示的訊息消失。
 */
export function useToast() {
  const toast = useState<ToastState | null>('toast', () => null)
  // timer 存在模組層級而非 useState：它不需要跨 SSR 序列化
  const timer = useState<ReturnType<typeof setTimeout> | null>('toast-timer', () => null)

  function showToast(message: string, kind: ToastKind = 'success') {
    toast.value = { message, kind }
    if (timer.value) clearTimeout(timer.value)
    if (import.meta.client) {
      timer.value = setTimeout(() => {
        toast.value = null
      }, 3400)
    }
  }

  function dismissToast() {
    if (timer.value) clearTimeout(timer.value)
    toast.value = null
  }

  return { toast, showToast, dismissToast }
}

/** 把後端回傳的錯誤訊息轉成 toast 可顯示的字串。 */
export function toneForKind(kind: ToastKind): Tone {
  return kind === 'error' ? 'error' : kind === 'info' ? 'info' : 'success'
}
