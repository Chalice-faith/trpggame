<script setup lang="ts">
import { ref, watch } from 'vue'
import { listGameKeyEvents, type GameKeyEvent } from '@/api/game'

const props = defineProps<{ roomId: number; timelineId?: string }>()
const visible = ref(false)
const loading = ref(false)
const error = ref('')
const events = ref<GameKeyEvent[]>([])
const hasMore = ref(false)
const cursor = ref<{ position: number; index: number }>()
let requestVersion = 0

watch(() => [props.roomId, props.timelineId], () => {
  requestVersion++
  visible.value = false
  events.value = []
  hasMore.value = false
  cursor.value = undefined
  error.value = ''
  loading.value = false
})

async function load(reset = false) {
  if (!props.timelineId || loading.value) return
  if (reset) {
    events.value = []
    cursor.value = undefined
    hasMore.value = false
  }
  const version = ++requestVersion
  loading.value = true
  error.value = ''
  try {
    const page = await listGameKeyEvents(props.roomId, props.timelineId, cursor.value)
    if (version !== requestVersion || page.timeline_id !== props.timelineId) return
    events.value = [...events.value, ...page.items]
    hasMore.value = page.has_more
    cursor.value = page.has_more && page.next_position !== undefined
      ? { position: page.next_position, index: page.next_index ?? 0 } : undefined
  } catch (cause: any) {
    if (version !== requestVersion) return
    error.value = cause?.response?.data?.code === 1343
      ? '剧情分支已变化，请刷新游戏状态后重新打开。'
      : cause?.response?.data?.message || '关键事件暂时无法读取。'
  } finally {
    if (version === requestVersion) loading.value = false
  }
}

function open() {
  visible.value = true
  void load(true)
}
</script>

<template>
  <el-button :disabled="!timelineId" @click="open">关键事件</el-button>
  <el-drawer v-model="visible" title="关键事件" size="min(480px, 100vw)">
    <el-button :loading="loading" @click="load(true)">刷新</el-button>
    <el-alert v-if="error" :title="error" type="warning" :closable="false" class="event-feedback" />
    <el-empty v-if="!loading && !events.length && !error" description="当前剧情还没有关键事件" />
    <ol v-if="events.length" class="event-list">
      <li v-for="event in events" :key="event.id" class="event-card">
        <span class="event-position">记录 #{{ event.position }}</span>
        <h3>{{ event.name }}</h3>
        <p>{{ event.description }}</p>
        <details>
          <summary>查看来源行动与叙事</summary>
          <p v-if="event.source_action">玩家：{{ event.source_action }}</p>
          <p v-if="event.source_narrative">GM：{{ event.source_narrative }}</p>
          <small>提交 {{ event.source_commit_id }}</small>
        </details>
      </li>
    </ol>
    <el-button v-if="hasMore" :loading="loading" @click="load()">加载更多</el-button>
  </el-drawer>
</template>

<style scoped>
.event-feedback { margin: 12px 0; }
.event-list { display: grid; gap: 12px; padding: 0; list-style: none; }
.event-card { padding: 14px; border: 1px solid #dddded; border-radius: 10px; overflow-wrap: anywhere; }
.event-card h3 { margin: 6px 0; font-size: 15px; }
.event-card p { white-space: pre-wrap; line-height: 1.6; }
.event-position, .event-card small { color: #89899d; font-size: 11px; }
.event-card details { margin-top: 10px; }
.event-card summary { cursor: pointer; }
</style>
