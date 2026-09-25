import type { DocumentRecord } from '~/types/api'
import { maxDocumentBytes } from '~/types/api'

/**
 * 申請文件的上傳、列出與刪除。
 *
 * 上傳走 multipart，不經過 useCsrf().mutate()（那個會把 body 當 JSON 處理），
 * 因此這裡自行附帶 CSRF token。
 */
export function useDocuments() {
  const { ensureToken } = useCsrf()

  const documents = ref<DocumentRecord[]>([])
  const uploading = ref(false)
  const loading = ref(false)

  async function load(applicationId: string) {
    if (!applicationId) {
      documents.value = []
      return
    }
    loading.value = true
    try {
      documents.value = await apiFetch<DocumentRecord[]>(
        `/api/v1/applications/${applicationId}/documents`,
      )
    } catch {
      documents.value = []
    } finally {
      loading.value = false
    }
  }

  /** 上傳單一檔案。回傳建立的紀錄，失敗時拋出錯誤。 */
  async function upload(applicationId: string, file: File) {
    if (file.size > maxDocumentBytes) {
      throw new Error(`檔案超過 ${maxDocumentBytes / 1024 / 1024} MB 上限`)
    }

    const token = await ensureToken()
    const body = new FormData()
    body.append('file', file)

    uploading.value = true
    try {
      const record = await $fetch<DocumentRecord>(
        `/api/v1/applications/${applicationId}/documents`,
        {
          method: 'POST',
          body,
          headers: token ? { 'X-CSRF-Token': token } : {},
        },
      )
      documents.value = [...documents.value, record]
      return record
    } finally {
      uploading.value = false
    }
  }

  async function remove(documentId: string) {
    const token = await ensureToken()
    await $fetch(`/api/v1/documents/${documentId}`, {
      method: 'DELETE',
      headers: token ? { 'X-CSRF-Token': token } : {},
    })
    documents.value = documents.value.filter((item) => item.id !== documentId)
  }

  function downloadUrl(documentId: string) {
    return `/api/v1/documents/${documentId}/download`
  }

  return { documents, loading, uploading, load, upload, remove, downloadUrl }
}

/** 把位元組數格式化為易讀的大小。 */
export function formatBytes(bytes: number) {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`
}
