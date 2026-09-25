<script setup lang="ts">
import {
  acceptedDocumentTypes,
  ocrStatusMeta,
  verificationFieldLabels,
  verificationMeta,
} from '~/types/api'

const props = defineProps<{
  applicationId: string
  /** 唯讀模式：只列出與下載，不提供上傳與刪除 */
  readonly?: boolean
}>()

const { documents, loading, uploading, load, upload, remove, downloadUrl } = useDocuments()
const { showToast } = useToast()

const fileInput = ref<HTMLInputElement | null>(null)
const dragging = ref(false)

watch(() => props.applicationId, (id) => void load(id), { immediate: true })

async function handleFiles(files: FileList | null) {
  if (!files?.length) return

  // 逐一上傳：後端對每份申請有數量上限，逐一處理才能明確回報是哪一份失敗
  for (const file of Array.from(files)) {
    try {
      await upload(props.applicationId, file)
      showToast(`「${file.name}」上傳成功`)
    } catch (error) {
      const message =
        error instanceof Error && !('data' in error)
          ? error.message
          : apiErrorMessage(error, `「${file.name}」上傳失敗`)
      showToast(message, 'error')
    }
  }

  if (fileInput.value) fileInput.value.value = ''
}

function onDrop(event: DragEvent) {
  dragging.value = false
  void handleFiles(event.dataTransfer?.files ?? null)
}

async function confirmRemove(id: string, name: string) {
  try {
    await remove(id)
    showToast(`已移除「${name}」`, 'info')
  } catch (error) {
    showToast(apiErrorMessage(error, '移除失敗'), 'error')
  }
}

function typeLabel(contentType: string) {
  return { 'application/pdf': 'PDF', 'image/jpeg': 'JPEG', 'image/png': 'PNG' }[contentType]
    ?? contentType
}

function ocrMeta(status: string) {
  return ocrStatusMeta[status] ?? { label: status, tone: 'info' as const }
}

function verifyMeta(result: string) {
  return verificationMeta[result] ?? { label: result, tone: 'info' as const }
}

function fieldLabel(field: string) {
  return verificationFieldLabels[field] ?? field
}

// 仍在處理中的文件需要輪詢；全部處理完就停止
const hasPending = computed(() =>
  documents.value.some((item) => item.ocrStatus === 'pending' || item.ocrStatus === 'processing'),
)

let pollTimer: ReturnType<typeof setInterval> | undefined

onMounted(() => {
  pollTimer = setInterval(() => {
    if (hasPending.value) void load(props.applicationId)
  }, 5000)
})

onBeforeUnmount(() => {
  if (pollTimer) clearInterval(pollTimer)
})
</script>

<template>
  <div class="document-uploader">
    <template v-if="!readonly">
      <input
        ref="fileInput"
        class="visually-hidden"
        type="file"
        :accept="acceptedDocumentTypes"
        multiple
        @change="handleFiles(($event.target as HTMLInputElement).files)"
      >

      <button
        class="upload-box"
        :class="{ dragging }"
        type="button"
        :disabled="uploading"
        @click="fileInput?.click()"
        @dragover.prevent="dragging = true"
        @dragleave.prevent="dragging = false"
        @drop.prevent="onDrop"
      >
        <span>↑</span>
        <strong>{{ uploading ? '上傳中…' : '點擊或拖曳檔案至此' }}</strong>
        <small>接受 PDF、JPEG、PNG，單檔上限 10 MB</small>
      </button>
    </template>

    <p v-if="loading" class="empty-state">載入文件中…</p>

    <div v-else-if="documents.length" class="document-list">
      <div v-for="item in documents" :key="item.id" class="document-row">
        <div class="document-main">
          <span>▤</span>
          <a :href="downloadUrl(item.id)" download>{{ item.originalName }}</a>
          <small class="mono">{{ typeLabel(item.contentType) }} · {{ formatBytes(item.sizeBytes) }}</small>
          <StatusTag :label="ocrMeta(item.ocrStatus).label" :tone="ocrMeta(item.ocrStatus).tone" />
          <button
            v-if="!readonly"
            class="btn small"
            type="button"
            @click="confirmRemove(item.id, item.originalName)"
          >
            移除
          </button>
        </div>

        <div v-if="item.verifications && Object.keys(item.verifications).length" class="ocr-results">
          <span
            v-for="(result, field) in item.verifications"
            :key="field"
            class="ocr-result"
          >
            {{ fieldLabel(field) }}
            <StatusTag :label="verifyMeta(result).label" :tone="verifyMeta(result).tone" />
          </span>
        </div>

        <small v-if="item.ocrError" class="ocr-error">{{ item.ocrError }}</small>
      </div>
    </div>

    <p v-else class="empty-state">
      {{ readonly ? '此申請沒有附件。' : '尚未上傳任何文件。' }}
    </p>

    <p v-if="documents.length" class="card-note">
      文字辨識僅比對申請人填寫的姓名與身分證是否出現在文件中，
      <strong>不判斷文件真偽</strong>，結果僅供人工審核參考。
    </p>
  </div>
</template>
