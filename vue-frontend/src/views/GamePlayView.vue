<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { useGameStore } from '@/stores/game'
import { useWebSocketStore } from '@/stores/websocket'

const route = useRoute()
const router = useRouter()
const gameStore = useGameStore()
const websocketStore = useWebSocketStore()
const actionText = ref('')
const roomId = computed(() => Number(route.params.id))

const canSend = computed(() =>
  websocketStore.isConnected && !gameStore.isStreaming && actionText.value.trim().length > 0
)

function submitAction() {
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

onMounted(() => {
  if (!Number.isInteger(roomId.value) || roomId.value <= 0) {
    ElMessage.error('无效的游戏房间')
    router.replace('/dashboard')
    return
  }
  if (!gameStore.currentRoom || gameStore.currentRoom.id !== roomId.value) {
    gameStore.setRoom({
      id: roomId.value,
      script_id: 0,
      title: '单人冒险',
      status: 'playing',
      current_turn: 0,
      round_count: 0
    })
  }
  websocketStore.connect(roomId.value)
})

onBeforeUnmount(() => websocketStore.disconnect())
</script>

<template>
  <div class="game-play-view">
    <aside class="sidebar">
      <button class="back-button" type="button" @click="router.push('/dashboard')">
        ← 返回剧本库
      </button>
      <div class="room-heading">
        <span class="eyebrow">ROOM {{ roomId }}</span>
        <h1>{{ gameStore.currentRoom?.title || '单人冒险' }}</h1>
        <span :class="['connection', { online: websocketStore.isConnected }]">
          {{ websocketStore.isConnected ? '实时连接中' : '等待连接' }}
        </span>
      </div>
      <div v-if="gameStore.lastDiceRoll" class="dice-card">
        <span>最近一次检定</span>
        <strong>{{ gameStore.lastDiceRoll.type }} = {{ gameStore.lastDiceRoll.result }}</strong>
        <small>{{ gameStore.lastDiceRoll.success ? '成功' : '失败' }} · 目标 {{ gameStore.lastDiceRoll.target }}</small>
      </div>
      <div class="turn-card">
        <span>当前回合</span>
        <strong>{{ gameStore.currentRoom?.current_turn ?? 0 }}</strong>
      </div>
    </aside>
    <main class="main-area">
      <section class="narrative-panel" aria-live="polite">
        <div v-if="gameStore.narrativeHistory.length === 0" class="empty-state">
          <span class="eyebrow">AI GAME MASTER</span>
          <h2>故事从你的第一个行动开始</h2>
          <p>描述你想做什么，主持人会根据剧本和角色状态推进冒险。</p>
        </div>
        <article
          v-for="(message, index) in gameStore.narrativeHistory"
          :key="`${index}-${message.role}`"
          :class="['message', message.role]"
        >
          <span class="message-role">{{ message.role === 'gm' ? 'GM' : '你' }}</span>
          <p>{{ message.content }}</p>
        </article>
        <article v-if="gameStore.isStreaming" class="message gm streaming">
          <span class="message-role">GM</span>
          <p>{{ gameStore.streamingNarrative }}<span class="cursor" /></p>
        </article>
      </section>
      <form class="action-bar" @submit.prevent="submitAction">
        <el-input
          v-model="actionText"
          type="textarea"
          :rows="3"
          maxlength="2000"
          show-word-limit
          resize="none"
          :disabled="gameStore.isStreaming || !websocketStore.isConnected"
          placeholder="例如：我检查书架后面的墙壁……"
          @keydown="handleActionKeydown"
        />
        <div class="action-footer">
          <span>Ctrl / ⌘ + Enter 提交行动</span>
          <el-button type="primary" native-type="submit" :disabled="!canSend">
            {{ gameStore.isStreaming ? '主持人思考中…' : '提交行动' }}
          </el-button>
        </div>
      </form>
    </main>
  </div>
</template>

<style scoped>
.game-play-view {
  display: grid;
  grid-template-columns: 280px 1fr;
  grid-template-rows: 1fr auto;
  height: 100vh;
  color: #e8e8f0;
  background: #141424;
}
.sidebar {
  grid-row: 1 / 3;
  padding: 24px;
  border-right: 1px solid #333;
  background: #18182a;
}
.main-area {
  min-width: 0;
  display: flex;
  flex-direction: column;
  overflow: hidden;
}
.back-button {
  padding: 0;
  border: 0;
  color: #9999ad;
  background: transparent;
  cursor: pointer;
}
.room-heading {
  margin-top: 48px;
}
.eyebrow,
.message-role,
.connection,
.turn-card span,
.dice-card span,
.dice-card small,
.action-footer span {
  color: #858598;
  font-size: 11px;
}
.eyebrow {
  color: #e94560;
  letter-spacing: .14em;
}
.room-heading h1 {
  margin: 8px 0;
  font-size: 24px;
}
.connection.online { color: #6fd29a; }
.turn-card,
.dice-card {
  display: grid;
  gap: 6px;
  margin-top: 32px;
  padding: 16px;
  border: 1px solid rgba(255, 255, 255, .08);
  border-radius: 12px;
  background: rgba(255, 255, 255, .03);
}
.turn-card strong { font-size: 28px; }
.dice-card strong { color: #f2bdc7; font-size: 18px; }
.narrative-panel {
  flex: 1;
  overflow-y: auto;
  padding: 38px clamp(20px, 6vw, 84px);
}
.empty-state {
  max-width: 520px;
  margin: 18vh auto 0;
  text-align: center;
}
.empty-state h2 { margin: 12px 0; font-size: 26px; }
.empty-state p { color: #9999ad; line-height: 1.7; }
.message {
  max-width: 780px;
  margin: 0 auto 22px;
  padding: 16px 18px;
  border-radius: 14px;
  background: rgba(255, 255, 255, .035);
}
.message.player {
  margin-right: 0;
  background: rgba(233, 69, 96, .1);
}
.message-role { display: block; margin-bottom: 7px; }
.message p { margin: 0; white-space: pre-wrap; line-height: 1.75; }
.cursor {
  display: inline-block;
  width: 7px;
  height: 1em;
  margin-left: 4px;
  vertical-align: -2px;
  background: #e94560;
  animation: blink 1s steps(2, start) infinite;
}
.action-bar {
  padding: 18px clamp(20px, 6vw, 84px) 24px;
  border-top: 1px solid rgba(255, 255, 255, .08);
  background: #18182a;
}
.action-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-top: 10px;
}
@keyframes blink { 50% { opacity: 0; } }
@media (max-width: 760px) {
  .game-play-view { display: flex; flex-direction: column; height: auto; min-height: 100vh; }
  .sidebar { padding: 16px; border-right: 0; border-bottom: 1px solid #333; }
  .room-heading { margin-top: 22px; }
  .turn-card, .dice-card { display: inline-grid; margin: 14px 8px 0 0; }
  .narrative-panel { min-height: 56vh; }
}
</style>
