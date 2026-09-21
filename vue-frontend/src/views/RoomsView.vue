<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { ArrowLeft, Plus, Refresh, Right, Tickets } from '@element-plus/icons-vue'
import { listScripts, type ScriptListItem } from '@/api/scripts'
import type { RoomStatus } from '@/api/rooms'
import { useRoomsStore } from '@/stores/rooms'

const router = useRouter()
const roomsStore = useRoomsStore()

const scripts = ref<ScriptListItem[]>([])
const createVisible = ref(false)
const joinVisible = ref(false)
const createForm = reactive({ name: '', script_id: 0, max_players: 4 })
const roomCode = ref('')

const readyScripts = computed(() => scripts.value.filter(({ status }) => status === 'ready'))
const normalizedRoomCode = computed(() => roomCode.value.trim().toUpperCase())

const statusCopy: Record<RoomStatus, string> = {
  waiting: '等待中',
  playing: '已开局',
  paused: '已暂停',
  ended: '已结束'
}

async function refresh() {
  try {
    const [scriptPage] = await Promise.all([
      listScripts(1, 100),
      roomsStore.loadRooms()
    ])
    scripts.value = scriptPage.items ?? []
  } catch (error: any) {
    showError(error, roomsStore.errorMessage || '多人房间加载失败')
  }
}

function openCreate() {
  const first = readyScripts.value[0]
  createForm.name = ''
  createForm.script_id = first?.id ?? 0
  createForm.max_players = 4
  createVisible.value = true
}

async function submitCreate() {
  if (!createForm.name.trim() || !createForm.script_id) return
  try {
    const room = await roomsStore.create(createForm)
    createVisible.value = false
    ElMessage.success('多人房间已创建')
    await router.push(`/lobby/${room.id}`)
  } catch (error: any) {
    showError(error, '创建多人房间失败')
  }
}

async function submitJoin() {
  if (normalizedRoomCode.value.length !== 8) return
  try {
    const room = await roomsStore.join(normalizedRoomCode.value)
    joinVisible.value = false
    roomCode.value = ''
    ElMessage.success('已加入房间')
    await router.push(`/lobby/${room.id}`)
  } catch (error: any) {
    showError(error, '加入房间失败')
  }
}

function formatDate(value: string) {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '—' : new Intl.DateTimeFormat('zh-CN', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit'
  }).format(date)
}

function showError(error: any, fallback: string) {
  const code = error?.response?.data?.code
  const known: Record<number, string> = {
    1900: '房间信息不完整或房间码格式不正确',
    1901: '房间不存在或你已不在房间中',
    1902: '只能使用已解析完成的本人剧本创建房间',
    1903: '剧本预设角色数量不足以支持所选容量',
    1904: '房间人数已满',
    1905: '房间已经开局，不能再加入',
    1906: '你已被房主移出，不能重新加入该房间'
  }
  ElMessage.error(known[code] || error?.response?.data?.message || fallback)
}

onMounted(refresh)
</script>

<template>
  <main class="rooms-view">
    <header class="topbar">
      <div class="brand">
        <span class="brand-mark"><Tickets /></span>
        <div><strong>多人冒险</strong><small>创建房间，凭码集结队友</small></div>
      </div>
      <el-button :icon="ArrowLeft" @click="router.push('/dashboard')">返回剧本库</el-button>
    </header>

    <section class="hero-panel">
      <div>
        <p class="eyebrow">MULTIPLAYER LOBBIES</p>
        <h1>我的房间</h1>
        <p>选择已就绪剧本创建等待大厅，或使用队友分享的 8 位房间码加入。</p>
      </div>
      <div class="hero-actions">
        <el-button :icon="Refresh" :loading="roomsStore.loading" @click="refresh">刷新</el-button>
        <el-button data-testid="join-room-open" @click="joinVisible = true">凭码加入</el-button>
        <el-button type="primary" :icon="Plus" data-testid="create-room-open" @click="openCreate">创建房间</el-button>
      </div>
    </section>

    <el-alert v-if="roomsStore.errorMessage" type="error" :closable="false" :title="roomsStore.errorMessage" />

    <section v-loading="roomsStore.loading" class="room-grid">
      <button
        v-for="room in roomsStore.rooms"
        :key="room.id"
        type="button"
        class="room-card"
        data-testid="room-card"
        @click="router.push(`/lobby/${room.id}`)"
      >
        <span class="room-card-top">
          <el-tag :type="room.status === 'waiting' ? 'success' : 'info'" round>{{ statusCopy[room.status] }}</el-tag>
          <code>{{ room.room_code }}</code>
        </span>
        <strong>{{ room.name }}</strong>
        <small>容量 {{ room.max_players }} 人 · 版本 {{ room.version }}</small>
        <span class="room-card-bottom">{{ formatDate(room.created_at) }}<Right /></span>
      </button>
      <div v-if="!roomsStore.loading && !roomsStore.rooms.length" class="empty-state">
        <Tickets />
        <h2>还没有多人房间</h2>
        <p>创建一个新大厅，或输入房间码加入队友。</p>
      </div>
    </section>

    <el-dialog v-model="createVisible" title="创建多人房间" width="min(92vw, 520px)" :close-on-click-modal="!roomsStore.mutating">
      <el-form label-position="top">
        <el-form-item label="房间名称" required>
          <el-input v-model="createForm.name" maxlength="128" show-word-limit data-testid="create-room-name" />
        </el-form-item>
        <el-form-item label="剧本" required>
          <el-select v-model="createForm.script_id" style="width: 100%" placeholder="请选择已就绪剧本" data-testid="create-room-script">
            <el-option v-for="script in readyScripts" :key="script.id" :label="script.title" :value="script.id" />
          </el-select>
          <p v-if="!readyScripts.length" class="form-hint">暂无已解析完成的剧本，请先回到剧本库准备剧本。</p>
        </el-form-item>
        <el-form-item label="房间容量">
          <el-segmented v-model="createForm.max_players" :options="[4, 5, 6]" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="createVisible = false">取消</el-button>
        <el-button type="primary" :loading="roomsStore.mutating" :disabled="!createForm.name.trim() || !createForm.script_id" data-testid="create-room-submit" @click="submitCreate">创建大厅</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="joinVisible" title="凭房间码加入" width="min(92vw, 440px)" :close-on-click-modal="!roomsStore.mutating">
      <el-form label-position="top">
        <el-form-item label="8 位房间码">
          <el-input v-model="roomCode" maxlength="8" class="code-input" placeholder="例如 A7K9M2QX" data-testid="join-room-code" @input="roomCode = String(roomCode).toUpperCase()" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="joinVisible = false">取消</el-button>
        <el-button type="primary" :loading="roomsStore.mutating" :disabled="normalizedRoomCode.length !== 8" data-testid="join-room-submit" @click="submitJoin">加入大厅</el-button>
      </template>
    </el-dialog>
  </main>
</template>

<style scoped>
.rooms-view { min-height: 100vh; padding: 0 34px 44px; box-sizing: border-box; color: #e9e9f1; text-align: left; background: radial-gradient(circle at 10% 0, rgba(92,95,210,.2), transparent 32%), radial-gradient(circle at 90% 12%, rgba(233,69,96,.12), transparent 28%), #141424; }
.topbar { min-height: 74px; display: flex; align-items: center; justify-content: space-between; border-bottom: 1px solid #303047; }
.brand { display: flex; gap: 12px; align-items: center; }.brand-mark { width: 38px; height: 38px; display: grid; place-items: center; border-radius: 11px; color: #fff; background: #5c5fd2; }.brand strong, .brand small { display: block; }.brand small { margin-top: 3px; color: #858598; font-size: 12px; }
.hero-panel { display: flex; justify-content: space-between; gap: 24px; align-items: end; padding: 48px 0 30px; }.hero-panel h1 { margin: 8px 0; color: #fff; font-size: 40px; }.hero-panel p { color: #aaaabb; }.eyebrow { color: #8e91ff !important; font-size: 11px; letter-spacing: .18em; }.hero-actions { display: flex; gap: 10px; flex-wrap: wrap; justify-content: flex-end; }
.room-grid { min-height: 260px; display: grid; grid-template-columns: repeat(auto-fill, minmax(260px, 1fr)); gap: 18px; margin-top: 22px; }.room-card { min-height: 176px; padding: 20px; border: 1px solid #34344c; border-radius: 16px; color: inherit; text-align: left; background: rgba(31,31,50,.92); cursor: pointer; transition: transform .18s, border-color .18s; }.room-card:hover { transform: translateY(-2px); border-color: #6669df; }.room-card-top, .room-card-bottom { display: flex; align-items: center; justify-content: space-between; }.room-card code { color: #b9baff; background: #292943; }.room-card > strong { display: block; margin: 24px 0 7px; font-size: 19px; }.room-card > small { color: #8f8fa3; }.room-card-bottom { margin-top: 22px; color: #77778c; font-size: 12px; }.room-card-bottom svg { width: 17px; }
.empty-state { grid-column: 1 / -1; min-height: 250px; display: grid; place-content: center; justify-items: center; gap: 8px; border: 1px dashed #3b3b54; border-radius: 16px; color: #858598; }.empty-state > svg { width: 44px; }.empty-state h2 { margin: 4px 0; color: #e9e9f1; }.form-hint { margin-top: 7px; color: #d9a45f; font-size: 12px; }.code-input :deep(input) { text-transform: uppercase; letter-spacing: .2em; font-family: ui-monospace, Consolas, monospace; }
@media (max-width: 760px) { .rooms-view { padding: 0 16px 28px; }.topbar { gap: 12px; }.hero-panel { align-items: stretch; flex-direction: column; padding-top: 30px; }.hero-panel h1 { font-size: 32px; }.hero-actions { justify-content: flex-start; } }
</style>
