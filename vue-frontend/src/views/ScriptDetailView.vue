<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  ArrowLeft,
  Calendar,
  Delete,
  Document,
  Files,
  Refresh
} from '@element-plus/icons-vue'
import {
  deleteScript,
  getScript,
  retryScript,
  type ScriptCharacter,
  type ScriptDetail,
  type ScriptStatus
} from '@/api/scripts'

const route = useRoute()
const router = useRouter()

const script = ref<ScriptDetail>()
const loading = ref(true)
const refreshing = ref(false)
const retrying = ref(false)
const deleting = ref(false)
const loadError = ref('')
let pollTimer: ReturnType<typeof setTimeout> | undefined

const scriptId = computed(() => Number(route.params.id))
const isProcessing = computed(
  () => script.value?.status === 'uploading' || script.value?.status === 'parsing'
)
const canDelete = computed(
  () => script.value?.status === 'ready' || script.value?.status === 'failed'
)
const canRetry = computed(() => script.value?.status === 'failed')

const statusPresentation: Record<
  ScriptStatus,
  { label: string; type: 'info' | 'warning' | 'success' | 'danger'; copy: string }
> = {
  uploading: {
    label: '上传中',
    type: 'info',
    copy: '文件正在写入对象存储，请稍候。'
  },
  parsing: {
    label: '解析中',
    type: 'warning',
    copy: '正在提取内容并构建向量索引，页面会自动刷新。'
  },
  ready: {
    label: '可使用',
    type: 'success',
    copy: '剧本解析完成，可以用于创建游戏。'
  },
  failed: {
    label: '解析失败',
    type: 'danger',
    copy: '剧本未能完成解析，请查看错误信息。'
  }
}

async function fetchDetail(options: { silent?: boolean } = {}) {
  if (!Number.isInteger(scriptId.value) || scriptId.value <= 0) {
    loadError.value = '无效的剧本编号'
    loading.value = false
    return
  }

  if (options.silent) {
    refreshing.value = true
  } else {
    loading.value = true
  }
  loadError.value = ''

  try {
    script.value = await getScript(scriptId.value)
  } catch (error: any) {
    if (error?.response?.status === 404) {
      loadError.value = '剧本不存在或已被删除'
    } else {
      loadError.value = error?.response?.data?.message || '剧本详情加载失败'
    }
    if (!options.silent) ElMessage.error(loadError.value)
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
  if (isProcessing.value) {
    pollTimer = setTimeout(() => fetchDetail({ silent: true }), 3000)
  }
}

async function handleDelete() {
  if (!script.value || !canDelete.value) return

  try {
    await ElMessageBox.confirm(
      `删除后将同时清理 PDF 文件和全部向量索引，此操作不可恢复。`,
      `确定删除“${script.value.title}”？`,
      {
        type: 'warning',
        confirmButtonText: '确认删除',
        cancelButtonText: '取消',
        confirmButtonClass: 'el-button--danger'
      }
    )
  } catch {
    return
  }

  deleting.value = true
  try {
    await deleteScript(script.value.id)
    ElMessage.success('剧本已删除')
    router.replace('/dashboard')
  } catch (error: any) {
    const message = error?.response?.data?.message || '剧本删除失败，请重试'
    ElMessage.error(message)
  } finally {
    deleting.value = false
  }
}

async function handleRetry() {
  if (!script.value || !canRetry.value || retrying.value) return

  retrying.value = true
  try {
    const result = await retryScript(script.value.id)
    script.value.status = result.status
    script.value.parse_error = undefined
    script.value.chunk_count = 0
    ElMessage.success('已重新提交解析任务')
    schedulePolling()
  } catch (error: any) {
    const message = error?.response?.data?.message || '重新解析失败，请稍后重试'
    ElMessage.error(message)
    await fetchDetail({ silent: true })
  } finally {
    retrying.value = false
  }
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
    dateStyle: 'medium',
    timeStyle: 'short'
  }).format(date)
}

function characterAttributes(character: ScriptCharacter): Array<[string, string]> {
  return Object.entries(character.attributes ?? {}).map(([key, value]) => [
    key,
    typeof value === 'object' ? JSON.stringify(value) : String(value)
  ])
}

onMounted(() => fetchDetail())
onBeforeUnmount(() => {
  if (pollTimer) clearTimeout(pollTimer)
})
</script>

<template>
  <main class="detail-view">
    <header class="detail-topbar">
      <el-button text :icon="ArrowLeft" @click="router.push('/dashboard')">
        返回剧本库
      </el-button>
      <el-button
        :icon="Refresh"
        :loading="refreshing"
        :disabled="loading"
        @click="fetchDetail({ silent: true })"
      >
        刷新
      </el-button>
    </header>

    <section v-if="loading" class="loading-panel">
      <el-skeleton :rows="8" animated />
    </section>

    <el-result
      v-else-if="loadError && !script"
      icon="error"
      :title="loadError"
      sub-title="请返回剧本库后重试"
    >
      <template #extra>
        <el-button type="primary" @click="router.push('/dashboard')">
          返回剧本库
        </el-button>
      </template>
    </el-result>

    <template v-else-if="script">
      <section class="detail-hero">
        <div class="book-cover">{{ script.title.slice(0, 1).toUpperCase() }}</div>
        <div class="hero-content">
          <div class="hero-status">
            <el-tag :type="statusPresentation[script.status].type" round>
              <span v-if="isProcessing" class="status-pulse" />
              {{ statusPresentation[script.status].label }}
            </el-tag>
            <span>{{ statusPresentation[script.status].copy }}</span>
          </div>
          <h1>{{ script.title }}</h1>
          <p>{{ script.description || '这个剧本暂时没有简介。' }}</p>
        </div>

        <div class="hero-actions">
          <el-button
            v-if="canRetry"
            type="primary"
            :icon="Refresh"
            :loading="retrying"
            :disabled="deleting"
            @click="handleRetry"
          >
            重新解析
          </el-button>
          <el-tooltip
            :disabled="canDelete"
            content="剧本处理完成后才可删除"
            placement="bottom"
          >
            <span>
              <el-button
                type="danger"
                plain
                :icon="Delete"
                :loading="deleting"
                :disabled="!canDelete || retrying"
                @click="handleDelete"
              >
                删除剧本
              </el-button>
            </span>
          </el-tooltip>
        </div>
      </section>

      <el-alert
        v-if="script.status === 'failed' && script.parse_error"
        class="parse-alert"
        title="解析错误"
        :description="script.parse_error"
        type="error"
        show-icon
        :closable="false"
      />

      <section class="stat-grid">
        <article>
          <el-icon><Document /></el-icon>
          <div><span>文件大小</span><strong>{{ formatFileSize(script.file_size) }}</strong></div>
        </article>
        <article>
          <el-icon><Files /></el-icon>
          <div><span>索引片段</span><strong>{{ script.chunk_count || 0 }}</strong></div>
        </article>
        <article>
          <el-icon><Calendar /></el-icon>
          <div><span>上传时间</span><strong>{{ formatDate(script.created_at) }}</strong></div>
        </article>
        <article>
          <el-icon><Refresh /></el-icon>
          <div><span>最近更新</span><strong>{{ formatDate(script.updated_at) }}</strong></div>
        </article>
      </section>

      <section class="characters-panel">
        <div class="section-heading">
          <div>
            <p class="eyebrow">PRESET CHARACTERS</p>
            <h2>预设角色</h2>
          </div>
          <span>{{ script.characters?.length || 0 }} 位</span>
        </div>

        <div v-if="script.characters?.length" class="character-grid">
          <article
            v-for="character in script.characters"
            :key="character.id"
            class="character-card"
          >
            <div class="character-avatar">{{ character.name.slice(0, 1) }}</div>
            <div>
              <h3>{{ character.name }}</h3>
              <p>{{ character.description || '暂无角色描述' }}</p>
              <dl v-if="characterAttributes(character).length">
                <template
                  v-for="[key, value] in characterAttributes(character)"
                  :key="key"
                >
                  <dt>{{ key }}</dt>
                  <dd>{{ value }}</dd>
                </template>
              </dl>
            </div>
          </article>
        </div>

        <el-empty
          v-else
          :image-size="84"
          description="当前剧本没有预设角色"
        />
      </section>
    </template>
  </main>
</template>

<style scoped>
.detail-view {
  width: 100%;
  min-height: 100vh;
  padding: 0 38px 64px;
  box-sizing: border-box;
  text-align: left;
  background:
    radial-gradient(circle at 15% 0%, rgba(233, 69, 96, 0.14), transparent 30%),
    linear-gradient(180deg, #141424, #1a1a2e);
}

.detail-topbar {
  min-height: 78px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  border-bottom: 1px solid rgba(255, 255, 255, 0.08);
}

.loading-panel {
  margin-top: 42px;
  padding: 32px;
  border-radius: 18px;
  background: rgba(255, 255, 255, 0.04);
}

.detail-hero {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) auto;
  gap: 24px;
  align-items: center;
  padding: 48px 4px 34px;
}

.book-cover {
  display: grid;
  width: 92px;
  height: 124px;
  place-items: center;
  border: 1px solid rgba(233, 69, 96, 0.28);
  border-radius: 12px;
  color: #f5c2cb;
  font-size: 32px;
  font-weight: 800;
  background: linear-gradient(145deg, rgba(233, 69, 96, 0.3), rgba(66, 38, 76, 0.5));
  box-shadow: 0 18px 44px rgba(0, 0, 0, 0.25);
}

.hero-status {
  display: flex;
  align-items: center;
  gap: 12px;
  color: #9999ad;
  font-size: 12px;
}

.hero-content h1 {
  margin: 10px 0 8px;
  color: #f7f7fb;
  font-size: clamp(30px, 4vw, 46px);
  font-weight: 700;
  letter-spacing: -0.035em;
}

.hero-content > p {
  max-width: 680px;
  color: #a5a5b5;
  font-size: 14px;
  line-height: 1.7;
}

.hero-actions {
  display: flex;
  gap: 10px;
  align-items: center;
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

.parse-alert {
  margin-bottom: 22px;
}

.stat-grid {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 12px;
  margin-bottom: 22px;
}

.stat-grid article {
  display: flex;
  align-items: center;
  gap: 14px;
  padding: 18px;
  border: 1px solid rgba(255, 255, 255, 0.08);
  border-radius: 14px;
  background: rgba(255, 255, 255, 0.035);
}

.stat-grid .el-icon {
  color: #e94560;
  font-size: 22px;
}

.stat-grid span,
.stat-grid strong {
  display: block;
}

.stat-grid span {
  color: #77778a;
  font-size: 11px;
}

.stat-grid strong {
  margin-top: 3px;
  color: #e7e7ee;
  font-size: 14px;
  font-weight: 600;
}

.characters-panel {
  overflow: hidden;
  border: 1px solid rgba(255, 255, 255, 0.09);
  border-radius: 18px;
  background: rgba(22, 22, 38, 0.86);
}

.section-heading {
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  padding: 24px;
  border-bottom: 1px solid rgba(255, 255, 255, 0.07);
}

.eyebrow {
  color: #e94560;
  font-size: 10px;
  font-weight: 800;
  letter-spacing: 0.15em;
}

.section-heading h2 {
  margin: 4px 0 0;
  color: #f7f7fb;
  font-size: 20px;
}

.section-heading > span {
  color: #77778a;
  font-size: 12px;
}

.character-grid {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 14px;
  padding: 20px;
}

.character-card {
  display: grid;
  grid-template-columns: auto 1fr;
  gap: 14px;
  padding: 18px;
  border: 1px solid rgba(255, 255, 255, 0.07);
  border-radius: 14px;
  background: rgba(255, 255, 255, 0.025);
}

.character-avatar {
  display: grid;
  width: 42px;
  height: 42px;
  place-items: center;
  border-radius: 12px;
  color: #f3b7c1;
  font-weight: 700;
  background: rgba(233, 69, 96, 0.15);
}

.character-card h3 {
  margin: 0 0 5px;
  color: #eeeef4;
  font-size: 15px;
}

.character-card p {
  color: #9292a4;
  font-size: 12px;
  line-height: 1.6;
}

.character-card dl {
  display: grid;
  grid-template-columns: max-content 1fr;
  gap: 5px 12px;
  margin: 12px 0 0;
  padding-top: 10px;
  border-top: 1px solid rgba(255, 255, 255, 0.06);
  font-size: 11px;
}

.character-card dt {
  color: #77778a;
}

.character-card dd {
  overflow-wrap: anywhere;
  margin: 0;
  color: #c7c7d1;
}

@keyframes pulse {
  50% {
    opacity: 0.25;
    transform: scale(0.8);
  }
}

@media (max-width: 820px) {
  .detail-view {
    padding: 0 16px 36px;
  }

  .detail-hero {
    grid-template-columns: auto 1fr;
  }

  .detail-hero > :last-child {
    grid-column: 1 / -1;
    justify-self: start;
  }

  .stat-grid {
    grid-template-columns: repeat(2, 1fr);
  }

  .character-grid {
    grid-template-columns: 1fr;
  }
}

@media (max-width: 520px) {
  .book-cover {
    width: 64px;
    height: 88px;
  }

  .hero-status {
    align-items: flex-start;
    flex-direction: column;
    gap: 6px;
  }

  .stat-grid {
    grid-template-columns: 1fr;
  }
}
</style>
