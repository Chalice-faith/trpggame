<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import {
  ElMessage,
  type FormInstance,
  type FormRules,
  type UploadFile,
  type UploadFiles,
  type UploadInstance,
  type UploadRawFile
} from 'element-plus'
import {
  Plus,
  Refresh,
  SwitchButton,
  UploadFilled,
  View
} from '@element-plus/icons-vue'
import {
  listScripts,
  uploadScript,
  type ScriptListItem,
  type ScriptStatus
} from '@/api/scripts'
import { validateScriptPDF } from '@/features/scripts/validation'
import { useAuthStore } from '@/stores/auth'

const router = useRouter()
const authStore = useAuthStore()

const scripts = ref<ScriptListItem[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(10)
const loading = ref(false)
const refreshing = ref(false)
const loadError = ref('')
const uploadDialogVisible = ref(false)
const uploadFormRef = ref<FormInstance>()
const uploadRef = ref<UploadInstance>()
const selectedFile = ref<UploadRawFile>()
const uploading = ref(false)
const uploadProgress = ref(0)
const uploadForm = ref({
  title: '',
  description: ''
})
let pollTimer: ReturnType<typeof setTimeout> | undefined

const uploadRules: FormRules = {
  title: [
    { max: 200, message: '标题不能超过 200 个字符', trigger: 'blur' }
  ],
  description: [
    { max: 2000, message: '简介不能超过 2000 个字符', trigger: 'blur' }
  ]
}

const hasProcessingScripts = computed(() =>
  scripts.value.some(({ status }) => status === 'uploading' || status === 'parsing')
)

const statusPresentation: Record<
  ScriptStatus,
  { label: string; type: 'info' | 'warning' | 'success' | 'danger' }
> = {
  uploading: { label: '上传中', type: 'info' },
  parsing: { label: '解析中', type: 'warning' },
  ready: { label: '可使用', type: 'success' },
  failed: { label: '解析失败', type: 'danger' }
}

async function fetchScripts(options: { silent?: boolean } = {}) {
  if (options.silent) {
    refreshing.value = true
  } else {
    loading.value = true
  }
  loadError.value = ''

  try {
    const result = await listScripts(page.value, pageSize.value)
    scripts.value = result.items ?? []
    total.value = result.total

    if (scripts.value.length === 0 && page.value > 1 && result.total > 0) {
      page.value = Math.max(1, Math.ceil(result.total / pageSize.value))
      return
    }
  } catch (error: any) {
    loadError.value = error?.response?.data?.message || '剧本列表加载失败'
    if (!options.silent) {
      ElMessage.error(loadError.value)
    }
  } finally {
    loading.value = false
    refreshing.value = false
    schedulePolling()
  }
}

function schedulePolling() {
  if (pollTimer) {
    clearTimeout(pollTimer)
    pollTimer = undefined
  }
  if (hasProcessingScripts.value) {
    pollTimer = setTimeout(() => fetchScripts({ silent: true }), 3000)
  }
}

function handlePageChange(nextPage: number) {
  page.value = nextPage
}

function handlePageSizeChange(nextPageSize: number) {
  pageSize.value = nextPageSize
  page.value = 1
}

function handleLogout() {
  authStore.logout()
  router.push('/login')
}

function openScriptDetail(scriptId: number) {
  router.push(`/scripts/${scriptId}`)
}

function openUploadDialog() {
  resetUploadForm()
  uploadDialogVisible.value = true
}

async function handleFileChange(file: UploadFile, files: UploadFiles) {
  const rawFile = file.raw
  if (!rawFile) return

  const errorMessage = await validateScriptPDF(rawFile)
  if (errorMessage) {
    ElMessage.error(errorMessage)
    uploadRef.value?.clearFiles()
    selectedFile.value = undefined
    return
  }

  selectedFile.value = rawFile
  if (!uploadForm.value.title.trim()) {
    uploadForm.value.title = rawFile.name.replace(/\.pdf$/i, '').slice(0, 200)
  }

  if (files.length > 1) {
    uploadRef.value?.handleRemove(files[0])
  }
}

function handleFileRemove() {
  selectedFile.value = undefined
}

async function submitUpload() {
  if (!uploadFormRef.value) return
  const valid = await uploadFormRef.value.validate().catch(() => false)
  if (!valid) return
  if (!selectedFile.value) {
    ElMessage.warning('请先选择 PDF 文件')
    return
  }

  uploading.value = true
  uploadProgress.value = 0
  try {
    await uploadScript(
      {
        file: selectedFile.value,
        title: uploadForm.value.title,
        description: uploadForm.value.description
      },
      (percentage) => {
        uploadProgress.value = percentage
      }
    )
    uploadProgress.value = 100
    ElMessage.success('剧本已上传，正在后台解析')
    uploadDialogVisible.value = false
    page.value = 1
    await fetchScripts({ silent: true })
  } catch (error: any) {
    const message = error?.response?.data?.message || '剧本上传失败，请重试'
    ElMessage.error(message)
  } finally {
    uploading.value = false
  }
}

function resetUploadForm() {
  uploadRef.value?.clearFiles()
  uploadFormRef.value?.clearValidate()
  selectedFile.value = undefined
  uploadProgress.value = 0
  uploadForm.value = {
    title: '',
    description: ''
  }
}

function handleUploadDialogClose(done: () => void) {
  if (uploading.value) {
    ElMessage.warning('文件正在上传，请稍候')
    return
  }
  resetUploadForm()
  done()
}

function formatFileSize(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '—'
  const units = ['B', 'KB', 'MB', 'GB']
  const unitIndex = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1)
  const value = bytes / 1024 ** unitIndex
  return `${value.toFixed(unitIndex === 0 ? 0 : 1)} ${units[unitIndex]}`
}

function formatDate(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '—'
  return new Intl.DateTimeFormat('zh-CN', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit'
  }).format(date)
}

watch([page, pageSize], () => fetchScripts())

onMounted(() => fetchScripts())
onBeforeUnmount(() => {
  if (pollTimer) clearTimeout(pollTimer)
})
</script>

<template>
  <main class="dashboard-view">
    <header class="topbar">
      <div class="brand">
        <span class="brand-mark">D20</span>
        <div>
          <strong>TRPG Game</strong>
          <small>AI 剧本控制台</small>
        </div>
      </div>

      <div class="account">
        <span class="account-name">{{ authStore.user?.nickname || authStore.user?.username }}</span>
        <el-button text :icon="SwitchButton" @click="handleLogout">退出</el-button>
      </div>
    </header>

    <section class="hero-panel">
      <div>
        <p class="eyebrow">SCRIPT LIBRARY</p>
        <h1>我的剧本</h1>
        <p class="hero-copy">
          管理已上传的冒险模组，并查看 PDF 解析与向量索引进度。
        </p>
      </div>

      <div class="summary">
        <span>{{ total }}</span>
        <small>个剧本</small>
      </div>
    </section>

    <section class="library-panel" aria-labelledby="script-list-title">
      <div class="panel-heading">
        <div>
          <h2 id="script-list-title">剧本库</h2>
          <p v-if="hasProcessingScripts" class="polling-note">
            有剧本正在处理，将自动刷新状态
          </p>
          <p v-else class="panel-note">所有状态均已同步</p>
        </div>

        <div class="panel-actions">
          <el-button
            :icon="Refresh"
            :loading="refreshing"
            :disabled="loading"
            @click="fetchScripts({ silent: true })"
          >
            刷新
          </el-button>
          <el-button type="primary" :icon="Plus" @click="openUploadDialog">
            上传剧本
          </el-button>
        </div>
      </div>

      <el-alert
        v-if="loadError"
        class="load-alert"
        :title="loadError"
        type="error"
        show-icon
        :closable="false"
      />

      <el-table
        v-loading="loading"
        :data="scripts"
        class="script-table"
        empty-text="还没有剧本，下一步即可上传第一份 PDF"
      >
        <el-table-column label="剧本" min-width="250">
          <template #default="{ row }: { row: ScriptListItem }">
            <div class="script-title-cell">
              <div class="cover-placeholder">{{ row.title.slice(0, 1).toUpperCase() }}</div>
              <div class="script-copy">
                <strong>{{ row.title }}</strong>
                <span>{{ row.description || '暂无简介' }}</span>
              </div>
            </div>
          </template>
        </el-table-column>

        <el-table-column label="状态" width="130">
          <template #default="{ row }: { row: ScriptListItem }">
            <el-tooltip
              :disabled="row.status !== 'failed' || !row.parse_error"
              :content="row.parse_error"
              placement="top"
            >
              <el-tag
                :type="statusPresentation[row.status].type"
                effect="light"
                round
              >
                <span
                  v-if="row.status === 'uploading' || row.status === 'parsing'"
                  class="status-pulse"
                />
                {{ statusPresentation[row.status].label }}
              </el-tag>
            </el-tooltip>
            <p v-if="row.status === 'ready'" class="chunk-count">
              {{ row.chunk_count }} 个片段
            </p>
          </template>
        </el-table-column>

        <el-table-column label="文件大小" width="120">
          <template #default="{ row }: { row: ScriptListItem }">
            {{ formatFileSize(row.file_size) }}
          </template>
        </el-table-column>

        <el-table-column label="更新时间" width="150">
          <template #default="{ row }: { row: ScriptListItem }">
            {{ formatDate(row.updated_at) }}
          </template>
        </el-table-column>

        <el-table-column label="操作" width="100" fixed="right">
          <template #default="{ row }: { row: ScriptListItem }">
            <el-button
              text
              type="primary"
              :icon="View"
              @click="openScriptDetail(row.id)"
            >
              详情
            </el-button>
          </template>
        </el-table-column>
      </el-table>

      <div v-if="total > 0" class="pagination-row">
        <el-pagination
          v-model:current-page="page"
          v-model:page-size="pageSize"
          :total="total"
          :page-sizes="[10, 20, 50]"
          layout="total, sizes, prev, pager, next"
          background
          @current-change="handlePageChange"
          @size-change="handlePageSizeChange"
        />
      </div>
    </section>

    <el-dialog
      v-model="uploadDialogVisible"
      title="上传 PDF 剧本"
      width="min(560px, calc(100vw - 32px))"
      :close-on-click-modal="!uploading"
      :close-on-press-escape="!uploading"
      :before-close="handleUploadDialogClose"
      @closed="resetUploadForm"
    >
      <el-form
        ref="uploadFormRef"
        :model="uploadForm"
        :rules="uploadRules"
        label-position="top"
      >
        <el-form-item label="PDF 文件" required>
          <el-upload
            ref="uploadRef"
            class="pdf-uploader"
            drag
            action="#"
            accept=".pdf,application/pdf"
            :auto-upload="false"
            :limit="1"
            :disabled="uploading"
            :on-change="handleFileChange"
            :on-remove="handleFileRemove"
          >
            <el-icon class="upload-icon"><UploadFilled /></el-icon>
            <div class="upload-copy">
              将 PDF 拖到这里，或 <em>点击选择</em>
            </div>
            <template #tip>
              <div class="upload-tip">
                仅支持可提取文本的 PDF，文件大小不超过 50 MB
              </div>
            </template>
          </el-upload>
        </el-form-item>

        <el-form-item label="剧本标题" prop="title">
          <el-input
            v-model="uploadForm.title"
            maxlength="200"
            show-word-limit
            placeholder="默认使用 PDF 文件名"
            :disabled="uploading"
          />
        </el-form-item>

        <el-form-item label="简介（选填）" prop="description">
          <el-input
            v-model="uploadForm.description"
            type="textarea"
            :rows="3"
            maxlength="2000"
            show-word-limit
            resize="none"
            placeholder="简单介绍这次冒险的背景"
            :disabled="uploading"
          />
        </el-form-item>

        <div v-if="uploading" class="upload-progress">
          <div>
            <span>正在上传</span>
            <strong>{{ uploadProgress }}%</strong>
          </div>
          <el-progress
            :percentage="uploadProgress"
            :show-text="false"
            :stroke-width="8"
          />
        </div>
      </el-form>

      <template #footer>
        <el-button :disabled="uploading" @click="uploadDialogVisible = false">
          取消
        </el-button>
        <el-button
          type="primary"
          :loading="uploading"
          :disabled="!selectedFile"
          @click="submitUpload"
        >
          开始上传
        </el-button>
      </template>
    </el-dialog>
  </main>
</template>

<style scoped>
.dashboard-view {
  width: 100%;
  min-height: 100vh;
  padding: 0 38px 56px;
  box-sizing: border-box;
  text-align: left;
  background:
    radial-gradient(circle at 80% 0%, rgba(233, 69, 96, 0.16), transparent 28%),
    linear-gradient(180deg, #141424 0%, #1a1a2e 100%);
}

.topbar {
  min-height: 78px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  border-bottom: 1px solid rgba(255, 255, 255, 0.08);
}

.brand,
.account,
.script-title-cell {
  display: flex;
  align-items: center;
}

.brand {
  gap: 12px;
}

.brand-mark {
  display: grid;
  width: 42px;
  height: 42px;
  place-items: center;
  border-radius: 12px;
  color: #fff;
  font-size: 12px;
  font-weight: 800;
  letter-spacing: 0.06em;
  background: linear-gradient(135deg, #e94560, #9b3159);
  box-shadow: 0 8px 24px rgba(233, 69, 96, 0.28);
}

.brand strong,
.brand small {
  display: block;
}

.brand strong {
  color: #f7f7fb;
  font-size: 16px;
}

.brand small,
.account-name {
  color: #9999ad;
  font-size: 12px;
}

.account {
  gap: 12px;
}

.hero-panel {
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  padding: 54px 4px 34px;
}

.eyebrow {
  color: #e94560;
  font-size: 11px;
  font-weight: 800;
  letter-spacing: 0.18em;
}

.hero-panel h1 {
  margin: 8px 0 10px;
  color: #f7f7fb;
  font-size: clamp(34px, 5vw, 54px);
  font-weight: 700;
  letter-spacing: -0.04em;
}

.hero-copy,
.panel-note,
.polling-note {
  color: #9999ad;
  font-size: 14px;
}

.summary {
  min-width: 116px;
  padding: 16px 20px;
  border: 1px solid rgba(255, 255, 255, 0.08);
  border-radius: 16px;
  text-align: right;
  background: rgba(255, 255, 255, 0.04);
}

.summary span,
.summary small {
  display: block;
}

.summary span {
  color: #f7f7fb;
  font-size: 28px;
  font-weight: 700;
}

.summary small {
  color: #9999ad;
  font-size: 12px;
}

.library-panel {
  overflow: hidden;
  border: 1px solid rgba(255, 255, 255, 0.09);
  border-radius: 18px;
  background: rgba(22, 22, 38, 0.86);
  box-shadow: 0 24px 70px rgba(0, 0, 0, 0.24);
}

.panel-heading {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 22px 24px;
}

.panel-actions {
  display: flex;
  gap: 8px;
}

.panel-heading h2 {
  margin: 0 0 5px;
  color: #f7f7fb;
  font-size: 20px;
  font-weight: 650;
}

.polling-note {
  color: #e9a23b;
}

.load-alert {
  margin: 0 24px 18px;
  width: auto;
}

.script-table {
  --el-table-bg-color: transparent;
  --el-table-tr-bg-color: transparent;
  --el-table-header-bg-color: rgba(255, 255, 255, 0.03);
  --el-table-row-hover-bg-color: rgba(233, 69, 96, 0.06);
  --el-table-border-color: rgba(255, 255, 255, 0.07);
  --el-table-text-color: #c9c9d4;
  --el-table-header-text-color: #858598;
}

.script-title-cell {
  gap: 14px;
}

.cover-placeholder {
  display: grid;
  flex: 0 0 42px;
  height: 54px;
  place-items: center;
  border: 1px solid rgba(233, 69, 96, 0.25);
  border-radius: 8px;
  color: #f3b7c1;
  font-weight: 750;
  background: linear-gradient(145deg, rgba(233, 69, 96, 0.24), rgba(78, 49, 89, 0.28));
}

.script-copy {
  min-width: 0;
}

.script-copy strong,
.script-copy span {
  display: block;
}

.script-copy strong {
  overflow: hidden;
  color: #eeeef4;
  font-size: 14px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.script-copy span {
  overflow: hidden;
  max-width: 360px;
  margin-top: 4px;
  color: #858598;
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.status-pulse {
  display: inline-block;
  width: 6px;
  height: 6px;
  margin-right: 4px;
  border-radius: 50%;
  background: currentColor;
  animation: pulse 1.5s ease-in-out infinite;
}

.chunk-count {
  margin-top: 5px;
  color: #77778a;
  font-size: 11px;
}

.pagination-row {
  display: flex;
  justify-content: flex-end;
  padding: 20px 24px;
  border-top: 1px solid rgba(255, 255, 255, 0.07);
}

.pdf-uploader {
  width: 100%;
}

.pdf-uploader :deep(.el-upload),
.pdf-uploader :deep(.el-upload-dragger) {
  width: 100%;
}

.pdf-uploader :deep(.el-upload-dragger) {
  padding: 30px 20px;
  border-color: rgba(233, 69, 96, 0.35);
  background: rgba(233, 69, 96, 0.04);
}

.upload-icon {
  margin-bottom: 10px;
  color: #e94560;
  font-size: 38px;
}

.upload-copy {
  color: #b8b8c6;
}

.upload-copy em {
  color: #e94560;
  font-style: normal;
}

.upload-tip {
  color: #858598;
  font-size: 12px;
}

.upload-progress {
  padding: 14px 16px;
  border-radius: 10px;
  background: rgba(233, 69, 96, 0.06);
}

.upload-progress > div {
  display: flex;
  justify-content: space-between;
  margin-bottom: 8px;
  color: #9999ad;
  font-size: 12px;
}

.upload-progress strong {
  color: #e94560;
}

@keyframes pulse {
  50% {
    opacity: 0.25;
    transform: scale(0.8);
  }
}

@media (max-width: 720px) {
  .dashboard-view {
    padding: 0 16px 32px;
  }

  .account-name,
  .summary {
    display: none;
  }

  .hero-panel {
    padding-top: 38px;
  }

  .hero-copy {
    max-width: 320px;
  }

  .panel-heading,
  .pagination-row {
    padding-right: 16px;
    padding-left: 16px;
  }

  .panel-heading {
    align-items: flex-start;
    gap: 16px;
  }

  .panel-actions {
    flex-direction: column-reverse;
  }

  .pagination-row {
    overflow-x: auto;
    justify-content: flex-start;
  }
}
</style>
