<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  CollectionTag,
  Download,
  FolderOpened,
  Refresh,
  SwitchButton,
  Upload,
  VideoPause,
  VideoPlay
} from '@element-plus/icons-vue'
import {
  createGameSave,
  endGame,
  listGameSaves,
  loadGame,
  pauseGame,
  resumeGame,
  type GameSaveSummary
} from '@/api/game'
import { useGameStore } from '@/stores/game'
import { useWebSocketStore } from '@/stores/websocket'

const route = useRoute()
const router = useRouter()
const gameStore = useGameStore()
const websocketStore = useWebSocketStore()

const actionText = ref('')
const roomId = computed(() => Number(route.params.id))
const narrativeRef = ref<HTMLElement>()
const saveDialogVisible = ref(false)
const savesDrawerVisible = ref(false)
const saveName = ref('')
const saves = ref<GameSaveSummary[]>([])
const savesLoading = ref(false)
const saving = ref(false)
const busyAction = ref('')
const diceAnimationKey = ref(0)
const diceAnimating = ref(false)
let diceTimer: ReturnType<typeof setTimeout> | undefined

const roomStatus = computed(() => gameStore.currentRoom?.status || 'playing')
const isPaused = computed(() => roomStatus.value === 'paused')
const isEnded = computed(() => roomStatus.value === 'ended')
const canSend = computed(() =>
  roomStatus.value === 'playing' &&
  websocketStore.isConnected &&
  !gameStore.isStreaming &&
  actionText.value.trim().length > 0
)
const connectionText = computed(() => {
  if (websocketStore.isConnected) return '实时连接中'
  if (websocketStore.reconnectAttempts > 0) return `重连中（${websocketStore.reconnectAttempts}/5）`
  return '等待连接'
})
const playerStatus = computed(() => gameStore.playerStatus)

function apiError(error: any, fallback: string) {
  return error?.response?.data?.message || fallback
}

function submitAction() {
  if (roomStatus.value !== 'playing') {
    ElMessage.warning('当前房间未处于进行中状态')
    return
  }
  const requestId = websocketStore.sendGameAction(actionText.value)
  if (!requestId) {
    if (!websocketStore.isConnected) ElMessage.warning('游戏连接尚未建立')
    return
  }
  actionText.value = ''
}

function handleActionKeydown(event: KeyboardEvent) {
  if (event.key === 'Enter' && (event.ctrlKey || event.metaKey)) submitAction()
}

function statusValue(value: number | null | undefined) {
  return value === null || value === undefined || !Number.isFinite(value) ? '—' : value
}

function meterPercent(value: number | null | undefined, max: number | null | undefined) {
  if (value === null || value === undefined || max === null || max === undefined || max <= 0) return 0
  return Math.max(0, Math.min(100, (value / max) * 100))
}

function formatDate(value: string) {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '—'
  return new Intl.DateTimeFormat('zh-CN', {
    month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit'
  }).format(date)
}

function openSaveDialog() {
  if (isEnded.value) return
  saveName.value = `第 ${gameStore.currentRoom?.current_turn ?? 0} 回合存档`
  saveDialogVisible.value = true
}

async function handleSave() {
  const name = saveName.value.trim()
  if (!name) {
    ElMessage.warning('请输入存档名称')
    return
  }
  if ([...name].length > 256 || /[\u0000-\u001f\u007f]/.test(name)) {
    ElMessage.warning('存档名称长度或内容无效')
    return
  }

  saving.value = true
  try {
    await createGameSave(roomId.value, name)
    ElMessage.success('存档已保存')
    saveDialogVisible.value = false
    await refreshSaves()
  } catch (error) {
    ElMessage.error(apiError(error, '保存失败，请稍后重试'))
  } finally {
    saving.value = false
  }
}

async function refreshSaves() {
  if (!Number.isInteger(roomId.value) || roomId.value <= 0) return
  savesLoading.value = true
  try {
    const result = await listGameSaves(roomId.value)
    saves.value = result.items ?? []
  } catch (error) {
    ElMessage.error(apiError(error, '存档列表加载失败'))
  } finally {
    savesLoading.value = false
  }
}

async function openSaves() {
  savesDrawerVisible.value = true
  await refreshSaves()
}

async function handleLoad(save: GameSaveSummary) {
  try {
    await ElMessageBox.confirm(
      `将恢复到“${save.save_name}”（第 ${save.round_number} 回合），当前游戏会先暂停。`,
      '确认读档？',
      { type: 'warning', confirmButtonText: '确认读档', cancelButtonText: '取消' }
    )
  } catch {
    return
  }

  busyAction.value = 'load'
  try {
    const result = await loadGame(roomId.value, save.id)
    gameStore.setRoomStatus(result.status)
    gameStore.setCurrentTurn(result.turn)
    gameStore.failNarrative()
    savesDrawerVisible.value = false
    ElMessage.success(`已恢复到第 ${result.turn} 回合，请点击“继续游戏”`)
  } catch (error) {
    ElMessage.error(apiError(error, '读档失败，请稍后重试'))
  } finally {
    busyAction.value = ''
  }
}

async function handlePause() {
  if (busyAction.value || gameStore.isStreaming) return
  busyAction.value = 'pause'
  try {
    const result = await pauseGame(roomId.value)
    gameStore.setRoomStatus(result.status)
    gameStore.failNarrative()
    ElMessage.success('游戏已暂停')
  } catch (error) {
    ElMessage.error(apiError(error, '暂停失败，请稍后重试'))
  } finally {
    busyAction.value = ''
  }
}

async function handleResume() {
  if (busyAction.value || isEnded.value) return
  busyAction.value = 'resume'
  try {
    const result = await resumeGame(roomId.value)
    gameStore.setRoomStatus(result.status)
    ElMessage.success('游戏已恢复')
  } catch (error) {
    ElMessage.error(apiError(error, '恢复失败，请稍后重试'))
  } finally {
    busyAction.value = ''
  }
}

async function handleEnd() {
  try {
    await ElMessageBox.confirm(
      '结束后将停止当前房间的行动，已保存的存档仍可保留。',
      '确认结束游戏？',
      { type: 'warning', confirmButtonText: '结束游戏', cancelButtonText: '继续游戏', confirmButtonClass: 'el-button--danger' }
    )
  } catch {
    return
  }

  busyAction.value = 'end'
  try {
    const result = await endGame(roomId.value)
    gameStore.setRoomStatus(result.status)
    gameStore.failNarrative()
    websocketStore.disconnect()
    ElMessage.success('游戏已结束')
    await router.push('/dashboard')
  } catch (error) {
    ElMessage.error(apiError(error, '结束游戏失败，请稍后重试'))
  } finally {
    busyAction.value = ''
  }
}

function scrollNarrativeToEnd() {
  void nextTick(() => {
    if (narrativeRef.value) narrativeRef.value.scrollTop = narrativeRef.value.scrollHeight
  })
}

watch(() => [gameStore.narrativeHistory.length, gameStore.streamingNarrative], scrollNarrativeToEnd)
watch(() => gameStore.lastDiceRoll, (roll) => {
  if (!roll) return
  diceAnimationKey.value++
  diceAnimating.value = true
  if (diceTimer) clearTimeout(diceTimer)
  diceTimer = setTimeout(() => { diceAnimating.value = false }, 900)
})
watch(() => websocketStore.lastError, (message) => {
  if (message) ElMessage.error(message)
})

onMounted(() => {
  if (!Number.isInteger(roomId.value) || roomId.value <= 0) {
    ElMessage.error('无效的游戏房间')
    router.replace('/dashboard')
    return
  }
  if (!gameStore.currentRoom || gameStore.currentRoom.id !== roomId.value) {
    gameStore.setRoom({ id: roomId.value, script_id: 0, title: '单人冒险', status: 'playing', current_turn: 0, round_count: 0 })
  }
  websocketStore.connect(roomId.value)
  scrollNarrativeToEnd()
})

onBeforeUnmount(() => {
  if (diceTimer) clearTimeout(diceTimer)
  websocketStore.disconnect()
})
</script>

<template>
  <div class="game-play-view">
    <aside class="sidebar">
      <button class="back-button" type="button" @click="router.push('/dashboard')">← 返回剧本库</button>
      <div class="room-heading">
        <span class="eyebrow">ROOM {{ roomId }}</span>
        <h1>{{ gameStore.currentRoom?.title || '单人冒险' }}</h1>
        <div class="connection-row"><span :class="['connection-dot', { online: websocketStore.isConnected }]" /><span>{{ connectionText }}</span></div>
      </div>

      <section class="status-panel" aria-labelledby="status-title">
        <div class="section-label"><span id="status-title">角色状态</span><el-tag v-if="isPaused" type="warning" size="small">已暂停</el-tag><el-tag v-else-if="isEnded" type="info" size="small">已结束</el-tag></div>
        <template v-if="playerStatus">
          <div v-for="meter in [
            { key: 'hp', label: 'HP', value: playerStatus.hp, max: playerStatus.maxHp, tone: 'hp' },
            { key: 'mp', label: 'MP', value: playerStatus.mp, max: playerStatus.maxMp, tone: 'mp' },
            { key: 'san', label: 'SAN', value: playerStatus.san, max: playerStatus.maxSan, tone: 'san' }
          ]" :key="meter.key" class="meter-row">
            <div class="meter-label"><span>{{ meter.label }}</span><strong>{{ statusValue(meter.value) }}<small v-if="meter.max !== null"> / {{ statusValue(meter.max) }}</small></strong></div>
            <div class="meter-track"><span :class="['meter-fill', meter.tone]" :style="{ width: `${meterPercent(meter.value, meter.max)}%` }" /></div>
          </div>
          <div class="status-facts"><span>AC <strong>{{ statusValue(playerStatus.ac) }}</strong></span><span>等级 <strong>{{ statusValue(playerStatus.level) }}</strong></span></div>
          <div v-if="playerStatus.location" class="location-row">当前位置：{{ playerStatus.location }}</div>
          <div class="inventory-block"><span class="sub-label">道具</span><div v-if="playerStatus.items.length" class="chip-list"><el-tag v-for="item in playerStatus.items" :key="item.name" size="small" effect="plain">{{ item.name }} ×{{ item.quantity }}</el-tag></div><span v-else class="muted-copy">暂无道具</span></div>
          <div class="inventory-block"><span class="sub-label">Buff / Debuff</span><div v-if="playerStatus.buffs.length" class="chip-list"><el-tag v-for="buff in playerStatus.buffs" :key="buff.name" size="small" type="warning" effect="plain">{{ buff.name }}<template v-if="buff.duration"> · {{ buff.duration }} 回合</template></el-tag></div><span v-else class="muted-copy">暂无效果</span></div>
        </template>
        <p v-else class="muted-copy">角色状态将在进入游戏后同步。</p>
      </section>

      <section class="game-toolbar" aria-label="游戏控制">
        <div class="section-label"><span>游戏工具</span><el-icon><CollectionTag /></el-icon></div>
        <div class="toolbar-grid">
          <el-button :icon="Upload" :disabled="isEnded || !!busyAction || gameStore.isStreaming" @click="openSaveDialog">保存</el-button>
          <el-button :icon="FolderOpened" :loading="savesLoading" :disabled="!!busyAction" @click="openSaves">存档</el-button>
          <el-button v-if="!isPaused && !isEnded" :icon="VideoPause" :loading="busyAction === 'pause'" :disabled="!!busyAction || gameStore.isStreaming" @click="handlePause">暂停</el-button>
          <el-button v-else-if="isPaused" :icon="VideoPlay" :loading="busyAction === 'resume'" :disabled="!!busyAction" @click="handleResume">继续</el-button>
          <el-button type="danger" plain :icon="SwitchButton" :loading="busyAction === 'end'" :disabled="isEnded || !!busyAction" @click="handleEnd">结束</el-button>
        </div>
      </section>

      <section v-if="gameStore.lastDiceRoll" :key="diceAnimationKey" :class="['dice-card', { rolling: diceAnimating }]">
        <div class="section-label"><span>最近一次检定</span><span>{{ gameStore.lastDiceRoll.type }}</span></div>
        <div class="dice-result-row"><div class="dice-orb">{{ gameStore.lastDiceRoll.result }}</div><div><strong>{{ gameStore.lastDiceRoll.success ? '检定成功' : '检定失败' }}</strong><small>目标值 {{ gameStore.lastDiceRoll.target }}</small></div></div>
        <p>{{ gameStore.lastDiceRoll.description }}</p><small class="dice-reason">{{ gameStore.lastDiceRoll.reason }}</small>
      </section>
      <div class="turn-card"><span>当前回合</span><strong>{{ gameStore.currentRoom?.current_turn ?? 0 }}</strong></div>
    </aside>

    <main class="main-area">
      <section ref="narrativeRef" class="narrative-panel" aria-live="polite">
        <div v-if="gameStore.narrativeHistory.length === 0" class="empty-state"><span class="eyebrow">AI GAME MASTER</span><h2>故事从你的第一个行动开始</h2><p>描述你想做什么，主持人会根据剧本和角色状态推进冒险。</p></div>
        <article v-for="(message, index) in gameStore.narrativeHistory" :key="`${index}-${message.role}`" :class="['message', message.role]"><span class="message-role">{{ message.role === 'gm' ? 'GM' : '你' }}</span><p>{{ message.content }}</p></article>
        <article v-if="gameStore.isStreaming" class="message gm streaming"><span class="message-role">GM</span><p>{{ gameStore.streamingNarrative }}<span class="cursor" /></p></article>
      </section>
      <form class="action-bar" @submit.prevent="submitAction">
        <el-alert v-if="isPaused" title="游戏已暂停，请从左侧工具栏恢复后继续行动。" type="warning" :closable="false" class="paused-alert" />
        <el-alert v-if="isEnded" title="游戏已结束，当前房间不再接受新行动。" type="info" :closable="false" class="paused-alert" />
        <el-input v-model="actionText" type="textarea" :rows="3" maxlength="2000" show-word-limit resize="none" :disabled="gameStore.isStreaming || !websocketStore.isConnected || isPaused || isEnded" placeholder="例如：我检查书架后面的墙壁……" @keydown="handleActionKeydown" />
        <div class="action-footer"><span>Ctrl / ⌘ + Enter 提交行动</span><el-button type="primary" native-type="submit" :disabled="!canSend">{{ gameStore.isStreaming ? '主持人思考中…' : '提交行动' }}</el-button></div>
      </form>
    </main>

    <el-dialog v-model="saveDialogVisible" title="保存当前进度" width="min(440px, calc(100vw - 32px))" :close-on-click-modal="!saving" :close-on-press-escape="!saving">
      <el-input v-model="saveName" maxlength="256" show-word-limit :disabled="saving" placeholder="例如：进入书房前" @keyup.enter="handleSave" /><p class="dialog-hint">会保存当前回合、角色状态、道具、Buff 和最近对话。</p>
      <template #footer><el-button :disabled="saving" @click="saveDialogVisible = false">取消</el-button><el-button type="primary" :loading="saving" @click="handleSave">保存</el-button></template>
    </el-dialog>

    <el-drawer v-model="savesDrawerVisible" title="游戏存档" direction="rtl" size="min(420px, 100vw)">
      <div class="saves-heading"><span>共 {{ saves.length }} 个存档</span><el-button text :icon="Refresh" :loading="savesLoading" @click="refreshSaves">刷新</el-button></div>
      <el-skeleton v-if="savesLoading && saves.length === 0" :rows="5" animated /><el-empty v-else-if="saves.length === 0" description="还没有存档" />
      <div v-else class="save-list"><article v-for="save in saves" :key="save.id" class="save-item"><div><strong>{{ save.save_name }}</strong><span>第 {{ save.round_number }} 回合 · {{ formatDate(save.created_at) }}</span></div><el-tag v-if="save.is_auto" size="small" type="info">自动</el-tag><el-button type="primary" plain size="small" :loading="busyAction === 'load'" :disabled="!!busyAction || isEnded" :icon="Download" @click="handleLoad(save)">读档</el-button></article></div>
    </el-drawer>
  </div>
</template>

<style scoped>
.game-play-view { display: grid; grid-template-columns: 330px 1fr; height: 100vh; color: #e8e8f0; background: #141424; }
.sidebar { grid-row: 1 / 3; overflow-y: auto; padding: 24px; border-right: 1px solid #333; background: #18182a; }
.main-area { min-width: 0; display: flex; flex-direction: column; overflow: hidden; }
.back-button { padding: 0; border: 0; color: #9999ad; background: transparent; cursor: pointer; }
.room-heading { margin-top: 38px; }.eyebrow, .message-role, .connection-row, .turn-card > span, .action-footer > span, .sub-label, .muted-copy, .dialog-hint { color: #858598; font-size: 11px; }.eyebrow { color: #e94560; letter-spacing: .14em; }.room-heading h1 { margin: 8px 0; font-size: 24px; }.connection-row { display: flex; align-items: center; gap: 7px; }.connection-dot { width: 7px; height: 7px; border-radius: 50%; background: #77778a; }.connection-dot.online { background: #6fd29a; box-shadow: 0 0 0 4px rgba(111, 210, 154, .12); }
.status-panel, .game-toolbar, .dice-card, .turn-card { display: grid; gap: 13px; margin-top: 22px; padding: 16px; border: 1px solid rgba(255, 255, 255, .08); border-radius: 14px; background: rgba(255, 255, 255, .03); }.section-label, .meter-label, .status-facts, .saves-heading, .save-item { display: flex; align-items: center; justify-content: space-between; gap: 8px; }.section-label { color: #c9c9d4; font-size: 12px; font-weight: 650; }.meter-row { display: grid; gap: 6px; }.meter-label { color: #9999ad; font-size: 11px; }.meter-label strong { color: #eeeef4; font-size: 12px; }.meter-label small { color: #77778a; font-weight: 400; }.meter-track { height: 5px; overflow: hidden; border-radius: 5px; background: rgba(255, 255, 255, .08); }.meter-fill { display: block; height: 100%; border-radius: inherit; transition: width .35s ease; }.meter-fill.hp { background: #e96779; }.meter-fill.mp { background: #609ee8; }.meter-fill.san { background: #c38ae8; }.status-facts { padding-top: 2px; color: #858598; font-size: 11px; }.status-facts strong { margin-left: 4px; color: #e8e8f0; }.location-row { overflow: hidden; color: #b8b8c6; font-size: 11px; text-overflow: ellipsis; white-space: nowrap; }.inventory-block { display: grid; gap: 7px; padding-top: 3px; }.sub-label { color: #77778a; }.chip-list { display: flex; flex-wrap: wrap; gap: 5px; }
.toolbar-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 8px; }.toolbar-grid .el-button { width: 100%; margin: 0; }.dice-card { transition: border-color .3s ease, box-shadow .3s ease; }.dice-card.rolling { border-color: rgba(233, 69, 96, .7); box-shadow: 0 0 24px rgba(233, 69, 96, .18); }.dice-result-row { display: flex; align-items: center; gap: 12px; }.dice-orb { display: grid; width: 48px; height: 48px; place-items: center; border-radius: 14px; color: #fff; font-size: 22px; font-weight: 800; background: linear-gradient(145deg, #e94560, #9b3159); }.rolling .dice-orb { animation: dice-pop .9s ease; }.dice-result-row strong, .dice-result-row small { display: block; }.dice-result-row strong { color: #f2bdc7; font-size: 14px; }.dice-result-row small { margin-top: 3px; color: #858598; font-size: 11px; }.dice-card p { margin: 0; color: #b8b8c6; font-size: 12px; line-height: 1.55; }.dice-reason { color: #77778a; }.turn-card { margin-top: 22px; }.turn-card strong { color: #f7f7fb; font-size: 28px; }
.narrative-panel { flex: 1; overflow-y: auto; padding: 38px clamp(20px, 6vw, 84px); scroll-behavior: smooth; }.empty-state { max-width: 520px; margin: 18vh auto 0; text-align: center; }.empty-state h2 { margin: 12px 0; font-size: 26px; }.empty-state p { color: #9999ad; line-height: 1.7; }.message { max-width: 780px; margin: 0 auto 22px; padding: 16px 18px; border-radius: 14px; background: rgba(255, 255, 255, .035); }.message.player { margin-right: 0; background: rgba(233, 69, 96, .1); }.message-role { display: block; margin-bottom: 7px; }.message p { margin: 0; white-space: pre-wrap; line-height: 1.75; }.cursor { display: inline-block; width: 7px; height: 1em; margin-left: 4px; vertical-align: -2px; background: #e94560; animation: blink 1s steps(2, start) infinite; }.action-bar { padding: 18px clamp(20px, 6vw, 84px) 24px; border-top: 1px solid rgba(255, 255, 255, .08); background: #18182a; }.paused-alert { margin-bottom: 10px; }.action-footer { display: flex; align-items: center; justify-content: space-between; margin-top: 10px; }.dialog-hint { margin: 10px 0 0; line-height: 1.5; }.saves-heading { margin-bottom: 16px; color: #858598; font-size: 12px; }.save-list { display: grid; gap: 10px; }.save-item { padding: 13px; border: 1px solid rgba(255, 255, 255, .08); border-radius: 10px; background: rgba(255, 255, 255, .025); }.save-item > div { min-width: 0; }.save-item strong, .save-item span { display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }.save-item strong { color: #e8e8f0; font-size: 13px; }.save-item span { margin-top: 4px; color: #858598; font-size: 11px; }
@keyframes blink { 50% { opacity: 0; } } @keyframes dice-pop { 0% { transform: scale(.7) rotate(-12deg); } 55% { transform: scale(1.12) rotate(8deg); } 100% { transform: scale(1) rotate(0); } }
@media (max-width: 900px) { .game-play-view { grid-template-columns: 280px 1fr; }.sidebar { padding: 18px; } }
@media (max-width: 760px) { .game-play-view { display: flex; flex-direction: column; height: auto; min-height: 100vh; }.sidebar { max-height: none; padding: 16px; border-right: 0; border-bottom: 1px solid #333; }.room-heading { margin-top: 22px; }.status-panel { margin-top: 16px; }.game-toolbar { margin-top: 14px; }.dice-card, .turn-card { display: inline-grid; width: calc(50% - 7px); box-sizing: border-box; margin: 14px 8px 0 0; vertical-align: top; }.turn-card { margin-right: 0; }.narrative-panel { min-height: 52vh; padding: 28px 16px; }.action-bar { padding: 14px 16px 18px; } }
@media (max-width: 460px) { .dice-card, .turn-card { display: grid; width: 100%; margin-right: 0; }.action-footer { align-items: flex-end; gap: 10px; }.action-footer > span { max-width: 145px; line-height: 1.4; } }
</style>
