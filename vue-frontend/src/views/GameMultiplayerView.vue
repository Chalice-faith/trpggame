<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  createGameSave, endGame, listGameSaves, loadGame, pauseGame, resumeGame,
  skipMultiplayerTurn, type GameSaveSummary
} from '@/api/game'
import { useAuthStore } from '@/stores/auth'
import { useMultiplayerStore } from '@/stores/multiplayer'
import { useRoomsStore } from '@/stores/rooms'
import { useWebSocketStore } from '@/stores/websocket'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const rooms = useRoomsStore()
const game = useMultiplayerStore()
const socket = useWebSocketStore()
const roomId = computed(() => Number(route.params.id))
const actionText = ref('')
const saveName = ref('')
const saves = ref<GameSaveSummary[]>([])
const savesVisible = ref(false)
const busy = ref('')
const now = ref(Date.now())
let clock: ReturnType<typeof setInterval> | undefined

const room = computed(() => rooms.currentRoom?.id === roomId.value ? rooms.currentRoom : null)
const isOwner = computed(() => room.value?.owner_id === auth.user?.id)
const isMyTurn = computed(() => game.currentActorId === auth.user?.id)
const canAct = computed(() => game.status === 'playing' && isMyTurn.value && !!game.deadlineAt &&
  socket.isConnected && !game.actionPending && !busy.value)
const remaining = computed(() => {
  if (!game.deadlineAt) return null
  return Math.max(0, Math.ceil((new Date(game.deadlineAt).getTime() - now.value) / 1000))
})
const countdown = computed(() => remaining.value === null ? '等待主持人' :
  `${String(Math.floor(remaining.value / 60)).padStart(2, '0')}:${String(remaining.value % 60).padStart(2, '0')}`)
const actors = computed(() => game.snapshot?.turn_order.map((id, index) => ({
  id, slot: index + 1,
  name: room.value?.members.find((member) => member.user.id === id)?.user.nickname ||
    room.value?.members.find((member) => member.user.id === id)?.user.username || `玩家 #${id}`,
  character: room.value?.characters.find((character) =>
    character.id === room.value?.members.find((member) => member.user.id === id)?.character_id)?.name || '角色'
})) ?? [])

function playerName(id: number) {
  return actors.value.find((actor) => actor.id === id)?.name || `玩家 #${id}`
}

function failure(error: any, fallback: string) {
  ElMessage.error(error?.response?.data?.message || fallback)
}

async function loadPage() {
  if (!Number.isSafeInteger(roomId.value) || roomId.value <= 0) {
    await router.replace('/rooms')
    return
  }
  game.reset()
  try {
    const roomSnapshot = await rooms.openRoom(roomId.value)
    if (roomSnapshot.status === 'waiting') {
      await router.replace(`/lobby/${roomId.value}`)
      return
    }
    await game.refresh(roomId.value)
    socket.setSequenceBaseline(roomId.value, game.snapshot?.seq ?? 0)
    socket.connect(roomId.value)
  } catch (error) {
    failure(error, '多人游戏加载失败')
  }
}

async function refreshPage() {
  try {
    const latestRoom = await rooms.openRoom(roomId.value)
    await game.refresh(latestRoom.id)
  } catch (error) { failure(error, '快照刷新失败') }
}

function submitAction() {
  const text = actionText.value.trim()
  if (!canAct.value || !text) return
  const requestId = crypto.randomUUID()
  socket.send('game_action', { request_id: requestId, expected_turn: game.currentTurn, action_text: text })
  game.markSubmitted(requestId)
  actionText.value = ''
}

async function skipTurn() {
  if (!canAct.value) return
  busy.value = 'skip'
  try {
    await skipMultiplayerTurn(roomId.value, game.currentTurn, crypto.randomUUID())
    await game.refresh(roomId.value)
  } catch (error) {
    failure(error, '跳过回合失败')
    await game.refresh(roomId.value).catch(() => {})
  } finally {
    busy.value = ''
  }
}

async function control(operation: 'pause' | 'resume' | 'save' | 'load' | 'end', saveId?: number) {
  if (!isOwner.value || busy.value) return
  if (operation === 'end' || operation === 'load') {
    try {
      await ElMessageBox.confirm(operation === 'end' ? '结束后不能继续当前游戏，确定结束吗？' :
        '读档会先暂停游戏并替换当前时间线，确定继续吗？', '请确认', { type: 'warning' })
    } catch { return }
  }
  busy.value = operation
  try {
    if (operation === 'pause') await pauseGame(roomId.value)
    if (operation === 'resume') await resumeGame(roomId.value)
    if (operation === 'save') {
      const name = saveName.value.trim()
      if (!name) { ElMessage.warning('请输入存档名称'); return }
      await createGameSave(roomId.value, name)
      saveName.value = ''
      await refreshSaves()
    }
    if (operation === 'load' && saveId) {
      await loadGame(roomId.value, saveId)
      savesVisible.value = false
    }
    if (operation === 'end') {
      await endGame(roomId.value)
      await rooms.openRoom(roomId.value)
      await game.refresh(roomId.value)
    } else await game.refresh(roomId.value)
    ElMessage.success({ pause: '已暂停', resume: '已继续', save: '已存档', load: '已读档，请手动继续', end: '游戏已结束' }[operation])
  } catch (error) {
    failure(error, '游戏操作失败')
    if (operation !== 'end') await game.refresh(roomId.value).catch(() => {})
  } finally { busy.value = '' }
}

async function refreshSaves() {
  try { saves.value = (await listGameSaves(roomId.value)).items ?? [] }
  catch (error) { failure(error, '存档列表加载失败') }
}

function openSaves() {
  savesVisible.value = true
  void refreshSaves()
}

watch(() => socket.lastError, (message) => { if (message) ElMessage.error(message) })
watch(roomId, () => { socket.disconnect(); void loadPage() })
onMounted(() => {
  clock = setInterval(() => { now.value = Date.now() }, 1000)
  void loadPage()
})
onBeforeUnmount(() => {
  if (clock) clearInterval(clock)
  socket.disconnect()
  game.reset()
})
</script>

<template>
  <main class="multiplayer-view">
    <header class="topbar">
      <div><button class="back" @click="router.push(`/lobby/${roomId}`)">← 返回大厅</button><h1>{{ room?.name || '多人冒险' }}</h1></div>
      <div class="top-actions"><el-tag :type="socket.isConnected ? 'success' : 'warning'">{{ socket.isConnected ? '实时连接' : '连接中断' }}</el-tag><el-button @click="refreshPage">刷新快照</el-button></div>
    </header>
    <el-alert v-if="game.error" type="error" :closable="false" :title="game.error" class="notice" />
    <el-alert v-if="game.status === 'paused'" type="warning" :closable="false" title="游戏已暂停，房主继续后会产生新的行动截止时间。" class="notice" />
    <el-alert v-if="game.status === 'ended' || room?.status === 'ended'" type="info" :closable="false" title="游戏已结束；运行态留存期间可查看最近记录，存档仍可查看。" class="notice" />
    <div v-if="game.snapshot" class="columns">
      <aside class="side">
        <section class="panel"><span class="eyebrow">TURN ORDER</span><h2>行动队列</h2>
          <p class="turn-meta">第 {{ game.snapshot.round_number + 1 }} 轮 · 第 {{ game.currentTurn + 1 }} 个行动槽</p>
          <ol class="roster"><li v-for="actor in actors" :key="actor.id" :class="{ active: game.status !== 'ended' && actor.id === game.currentActorId }"><span>{{ actor.slot }}</span><div><strong>{{ actor.name }}</strong><small>{{ actor.character }}</small></div><el-tag v-if="game.status !== 'ended' && actor.id === game.currentActorId" size="small">当前</el-tag></li></ol>
          <div class="timer"><span>{{ game.status === 'ended' ? '游戏状态' : '剩余时间' }}</span><strong>{{ game.status === 'ended' ? '已结束' : game.status === 'playing' ? countdown : '已暂停' }}</strong></div>
        </section>
        <section class="panel"><span class="eyebrow">PARTY STATUS</span><h2>队伍状态</h2>
          <article v-for="player in game.snapshot.players" :key="player.user_id" class="player-card"><strong>{{ playerName(player.user_id) }}</strong><small>角色 #{{ player.character_id }}</small><div class="attributes"><span v-for="(value, key) in player.player_state" :key="key">{{ key }} {{ value }}</span></div><small>道具：{{ player.items.map(item => `${item.name} ×${item.quantity}`).join('、') || '无' }}</small><small>效果：{{ player.buffs.map(buff => `${buff.name} (${buff.duration})`).join('、') || '无' }}</small></article>
        </section>
        <section class="panel"><span class="eyebrow">CONTROLS</span><h2>游戏控制</h2><div class="controls">
          <el-button @click="openSaves">查看存档</el-button>
          <template v-if="isOwner && game.status !== 'ended'"><el-button v-if="game.status === 'playing'" :loading="busy === 'pause'" :disabled="!!busy" @click="control('pause')">暂停</el-button><el-button v-else :loading="busy === 'resume'" :disabled="!!busy" @click="control('resume')">继续</el-button><el-input v-model="saveName" maxlength="256" placeholder="存档名称" /><el-button :loading="busy === 'save'" :disabled="!!busy || !saveName.trim()" @click="control('save')">手动存档</el-button><el-button type="danger" plain :loading="busy === 'end'" :disabled="!!busy" @click="control('end')">结束游戏</el-button></template>
        </div></section>
      </aside>
      <section class="story"><div class="story-heading"><span class="eyebrow">ADVENTURE LOG</span><h2>冒险记录</h2></div>
        <div class="messages" aria-live="polite"><p v-if="!game.history.length" class="empty">等待主持人开启故事…</p><article v-for="(entry, index) in game.history" :key="index" :class="['message', entry.role]"><small>{{ entry.role === 'assistant' ? 'GM' : entry.role === 'user' ? '队伍行动' : '系统' }}</small><p>{{ entry.content }}</p></article><article v-if="game.actionPending" class="message assistant draft"><small>GM 正在生成 · 尚未提交</small><p>{{ game.draft || '请稍候…' }}</p></article></div>
        <div v-if="game.lastDiceRoll" class="dice">最近检定：{{ game.lastDiceRoll.type }} · {{ game.lastDiceRoll.result }} / {{ game.lastDiceRoll.target }}</div>
        <form class="composer" @submit.prevent="submitAction"><p>{{ game.status === 'playing' ? isMyTurn ? '轮到你行动' : `等待 ${playerName(game.currentActorId || 0)} 行动` : '当前不能行动' }}</p><el-input v-model="actionText" type="textarea" :rows="3" maxlength="2000" show-word-limit :disabled="!canAct" placeholder="描述你的行动…" /><div class="composer-actions"><el-button :disabled="!canAct" :loading="busy === 'skip'" @click="skipTurn">跳过本回合</el-button><el-button type="primary" native-type="submit" :disabled="!canAct || !actionText.trim()">提交行动</el-button></div></form>
      </section>
    </div>
    <section v-else-if="room?.status === 'ended'" class="ended-panel"><h2>这场冒险已经结束</h2><p>运行态已关闭，房间成员仍可查看留存的存档。</p><el-button @click="openSaves">查看存档</el-button></section>
    <div v-else-if="game.loading" class="loading">正在读取多人运行态…</div>
    <el-drawer v-model="savesVisible" title="游戏存档" size="min(420px, 100vw)"><el-button @click="refreshSaves">刷新</el-button><el-empty v-if="!saves.length" description="暂无存档" /><article v-for="save in saves" :key="save.id" class="save-row"><div><strong>{{ save.save_name }}</strong><small>第 {{ save.round_number + (save.is_auto ? 0 : 1) }} 轮 · {{ save.is_auto ? '自动' : '手动' }}</small></div><el-button v-if="isOwner && game.status !== 'ended'" size="small" :disabled="!!busy" @click="control('load', save.id)">读档</el-button></article></el-drawer>
  </main>
</template>

<style scoped>
.multiplayer-view{min-height:100vh;padding:0 30px 32px;box-sizing:border-box;color:#eeeef5;background:#141424}.topbar{display:flex;justify-content:space-between;align-items:center;min-height:90px;border-bottom:1px solid #303047}.topbar h1{margin:5px 0 0;font-size:25px}.back{padding:0;border:0;background:none;color:#9292ab;cursor:pointer}.top-actions{display:flex;gap:10px;align-items:center}.notice{margin-top:16px}.columns{display:grid;grid-template-columns:330px minmax(0,1fr);gap:20px;margin-top:20px}.side{display:grid;align-content:start;gap:16px}.panel,.story,.ended-panel{border:1px solid #34344c;border-radius:16px;background:#1d1d30}.panel{padding:18px}.ended-panel{margin-top:20px;padding:28px}.ended-panel p{color:#9292a8}.panel h2,.story h2{margin:5px 0 15px;font-size:18px}.eyebrow{font-size:10px;letter-spacing:.16em;color:#989bff}.turn-meta,.empty{color:#9292a8;font-size:12px}.roster{list-style:none;margin:0;padding:0}.roster li{display:flex;align-items:center;gap:10px;padding:10px 8px;border-radius:9px}.roster li.active{background:#303057}.roster li>span{color:#aaaade}.roster li>div{flex:1}.roster strong,.roster small,.player-card strong,.player-card small{display:block}.roster small,.player-card small{color:#9292a8;font-size:11px}.timer{display:flex;justify-content:space-between;margin-top:12px;padding-top:14px;border-top:1px solid #39394f}.timer strong{font-variant-numeric:tabular-nums;color:#f3999f}.player-card{padding:11px 0;border-top:1px solid #34344c}.attributes{display:flex;flex-wrap:wrap;gap:6px;margin:8px 0}.attributes span{padding:3px 6px;border-radius:5px;background:#303047;font-size:11px}.controls{display:grid;grid-template-columns:1fr 1fr;gap:8px}.controls .el-button{margin:0}.controls .el-input{grid-column:1/-1}.story{display:flex;flex-direction:column;min-height:calc(100vh - 145px);overflow:hidden}.story-heading{padding:18px 20px;border-bottom:1px solid #34344c}.story-heading h2{margin-bottom:0}.messages{flex:1;min-height:260px;max-height:calc(100vh - 390px);overflow:auto;padding:20px}.message{max-width:780px;margin:0 0 15px;padding:12px 15px;border-radius:10px;background:#29293c;white-space:pre-wrap}.message.user{background:#2c3351}.message.draft{border:1px dashed #74749b;opacity:.8}.message small{color:#aaaad2}.message p{margin:7px 0 0;line-height:1.65}.dice{padding:8px 20px;color:#ffba9e}.composer{padding:18px 20px;border-top:1px solid #34344c}.composer p{margin:0 0 10px;color:#c4c4df}.composer-actions{display:flex;justify-content:flex-end;gap:8px;margin-top:10px}.save-row{display:flex;justify-content:space-between;align-items:center;padding:14px 0;border-bottom:1px solid #eee}.save-row small{display:block;color:#888}.loading{padding:60px;text-align:center;color:#aaa}@media(max-width:850px){.multiplayer-view{padding:0 14px 20px}.columns{grid-template-columns:1fr}.story{min-height:600px}.messages{max-height:500px}.topbar{gap:12px}}
</style>
