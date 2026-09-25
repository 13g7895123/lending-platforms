import { applicationStatusMeta, installmentStatusMeta, type Tone } from '~/types/api'

/** 申請與攤還期數的狀態標籤，多個頁面共用。 */
export function useStatus() {
  function applicationLabel(status: string) {
    return applicationStatusMeta[status]?.label ?? status
  }

  function applicationTone(status: string): Tone {
    return applicationStatusMeta[status]?.tone ?? 'info'
  }

  function installmentLabel(status: string) {
    return installmentStatusMeta[status]?.label ?? status
  }

  function installmentTone(status: string): Tone {
    return installmentStatusMeta[status]?.tone ?? 'info'
  }

  return { applicationLabel, applicationTone, installmentLabel, installmentTone }
}
