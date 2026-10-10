<script setup lang="ts">
import { computed, onBeforeUnmount, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { ArrowLeft, Check, CopyDocument, Refresh, UserFilled } from '@element-plus/icons-vue'
import type { RoomMember } from '@/api/rooms'
import { useAuthStore } from '@/stores/auth'
import { useRoomsStore } from '@/stores/rooms'
import { useWebSocketStore } from '@/stores/websocket'

const route = useRoute()
const router = useRouter()
const authStore = useAuthStore()
const roomsStore = useRoomsStore()
const websocketStore = useWebSocketStore()

const roomId = computed(() => Number(Array.isArray(route.params.roomId) ? route.params.roomId[0] : route.params.roomId))
const room = computed(() => roomsStore.currentRoom)
const isWaiting = computed(() => room.value?.status === 'waiting')
const selectedCharacterId = computed(() => roomsStore.currentMember?.character_id ?? null)
const occupiedCharacterIds = computed(() => new Set(
  room.value?.members.map(({ character_id }) => character_id).filter((id): id is number => !!id) ?? []
))
const connectionCopy = computed(() => websocketStore.isConnected ? '实时连接正常' : '实时连接中断，操作后将从服务器刷新')

function characterName(characterId: number | null) {
  if (!characterId) return '尚未选择角色'
  return room.value?.characters.find(({ id }) => id === characterId)?.name ?? `角色 #${characterId}`
}

function userName(member: RoomMember) {
  return member.user.nickname.trim() || member.user.username
}

function avatarText(member: RoomMember) {
  return [...userName(member)][0]?.toUpperCase() || '?'
}

async function loadLobby() {
  if (!Number.isSafeInteger(roomId.value) || roomId.value <= 0) {
    await router.replace('/rooms')
    return
  }
  try {
    await roomsStore.openRoom(roomId.value)
    websocketStore.connect(roomId.value)
  } catch (error: any) {
    showError(error, '大厅加载失败')
    await router.replace('/rooms')
  }
}

async function refresh() {
  try {
    await roomsStore.openRoom(roomId.value)
  } catch (error: any) {
    showError(error, '大厅刷新失败')
  }
}

async function chooseCharacter(characterId: number) {
  if (!isWaiting.value || roomsStore.mutating) return
  try {
    await roomsStore.selectCharacter(characterId)
    ElMessage.success('角色选择已更新，准备状态已重置')
  } catch (error: any) {
    showError(error, '选择角色失败')
  }
}

async function toggleReady() {
  if (!roomsStore.currentMember?.character_id) {
    ElMessage.warning('请先选择角色')
    return
  }
  try {
    await roomsStore.setReady(!roomsStore.currentMember.is_ready)
  } catch (error: any) {
    showError(error, '更新准备状态失败')
  }
}

async function removeMember(member: RoomMember) {
  try {
    await ElMessageBox.confirm(`确定将“${userName(member)}”移出房间吗？`, '移出成员', {
      type: 'warning', confirmButtonText: '移出', cancelButtonText: '取消'
    })
    await roomsStore.removeMember(member.user.id)
    ElMessage.success('成员已移出房间')
  } catch (error: any) {
    if (error === 'cancel' || error === 'close') return
    showError(error, '移出成员失败')
  }
}

async function transferOwner(member: RoomMember) {
  try {
    await ElMessageBox.confirm(`确定将房主转让给“${userName(member)}”吗？`, '转让房主', {
      type: 'warning', confirmButtonText: '确认转让', cancelButtonText: '取消'
    })
    await roomsStore.transferOwner(member.user.id)
    ElMessage.success('房主已转让')
  } catch (error: any) {
    if (error === 'cancel' || error === 'close') return
    showError(error, '转让房主失败')
  }
}

async function leaveLobby() {
  const current = room.value
  if (!current || roomsStore.isOwner) return
  try {
    await ElMessageBox.confirm(`确定退出“${current.name}”吗？`, '退出房间', {
      type: 'warning', confirmButtonText: '退出', cancelButtonText: '取消'
    })
    await roomsStore.leave()
    websocketStore.disconnect()
    await router.replace('/rooms')
    ElMessage.success('已退出房间')
  } catch (error: any) {
    if (error === 'cancel' || error === 'close') return
    showError(error, '退出房间失败')
  }
}

async function startGame() {
  try {
    await ElMessageBox.confirm('开局后将冻结成员与角色，并进入多人行动界面。', '确认开局', {
      type: 'warning', confirmButtonText: '确认开局', cancelButtonText: '取消'
    })
    await roomsStore.start()
    ElMessage.success('房间已开局')
    await router.push(`/game/multiplayer/${roomId.value}`)
  } catch (error: any) {
    if (error === 'cancel' || error === 'close') return
    showError(error, '开局失败')
  }
}

async function copyCode() {
  if (!room.value?.room_code) return
  try {
    await navigator.clipboard.writeText(room.value.room_code)
    ElMessage.success('房间码已复制')
  } catch {
    ElMessage.warning(`房间码：${room.value.room_code}`)
  }
}

function showError(error: any, fallback: string) {
  const code = error?.response?.data?.code
  const known: Record<number, string> = {
    1901: '房间不存在或你已不在房间中',
    1905: '房间已不在等待状态',
    1908: '大厅状态已变化，页面已刷新，请重试',
    1909: '你没有执行此操作的权限',
    1910: '该角色不属于当前剧本',
    1911: '该角色已被其他成员选择',
    1912: '至少两人选角并全部准备后才能开局',
    1913: '房主退出前必须先转让房主身份'
  }
  ElMessage.error(known[code] || error?.response?.data?.message || fallback)
}

watch(() => roomsStore.revokedRoomId, async (revoked) => {
  if (revoked !== roomId.value) return
  websocketStore.disconnect()
  ElMessage.warning('你已离开或被移出该房间')
  await router.replace('/rooms')
})

watch(roomId, () => {
  websocketStore.disconnect()
  roomsStore.clearCurrentRoom()
  void loadLobby()
}, { immediate: true })

onBeforeUnmount(() => {
  websocketStore.disconnect()
  roomsStore.clearCurrentRoom()
})
</script>

<template>
  <main class="lobby-view">
    <header class="topbar">
      <el-button text :icon="ArrowLeft" @click="router.push('/rooms')">我的房间</el-button>
      <div class="connection" :class="{ online: websocketStore.isConnected }">
        <span />{{ connectionCopy }}
      </div>
      <el-button :icon="Refresh" :loading="roomsStore.loading" @click="refresh">刷新快照</el-button>
    </header>

    <section v-if="room" class="lobby-shell">
      <header class="lobby-heading">
        <div>
          <p class="eyebrow">{{ room.status === 'waiting' ? 'WAITING ROOM' : room.status === 'ended' ? 'ENDED ROOM' : 'GAME ROOM' }} · #{{ room.id }}</p>
          <h1>{{ room.name }}</h1>
          <p>剧本 #{{ room.script_id }} · {{ room.members.length }} / {{ room.max_players }} 人 · 版本 {{ room.version }}</p>
        </div>
        <div class="room-code">
          <small>房间码</small>
          <strong>{{ room.room_code }}</strong>
          <el-button text :icon="CopyDocument" aria-label="复制房间码" @click="copyCode" />
        </div>
      </header>

      <el-alert v-if="room.status === 'playing' || room.status === 'paused'" class="phase-alert" type="success" :closable="false" show-icon title="房间已开局，成员与角色已经冻结。" />
      <el-alert v-else-if="room.status === 'ended'" class="phase-alert" type="info" :closable="false" title="游戏已结束，可查看最近记录和存档。" />
      <el-alert v-if="websocketStore.lastError" class="phase-alert" type="warning" :closable="false" :title="websocketStore.lastError" />

      <div class="lobby-columns">
        <section class="panel member-panel">
          <div class="panel-heading"><div><p class="eyebrow">PARTY</p><h2>队伍成员</h2></div><el-tag round>{{ room.members.length }} / {{ room.max_players }}</el-tag></div>
          <article v-for="member in room.members" :key="member.user.id" class="member-row" data-testid="lobby-member-row">
            <el-avatar :size="44" :src="member.user.avatar_url">{{ avatarText(member) }}</el-avatar>
            <div class="member-copy">
              <strong>{{ userName(member) }} <el-tag v-if="member.user.id === room.owner_id" size="small" type="danger">房主</el-tag></strong>
              <small>{{ characterName(member.character_id) }}</small>
            </div>
            <el-tag :type="member.is_ready ? 'success' : 'info'">{{ member.is_ready ? '已准备' : '未准备' }}</el-tag>
            <template v-if="roomsStore.isOwner && member.user.id !== authStore.user?.id && isWaiting">
              <el-dropdown trigger="click">
                <el-button text>管理</el-button>
                <template #dropdown>
                  <el-dropdown-menu>
                    <el-dropdown-item @click="transferOwner(member)">转让房主</el-dropdown-item>
                    <el-dropdown-item divided @click="removeMember(member)">移出房间</el-dropdown-item>
                  </el-dropdown-menu>
                </template>
              </el-dropdown>
            </template>
          </article>
        </section>

        <section class="panel character-panel">
          <div class="panel-heading"><div><p class="eyebrow">CHARACTERS</p><h2>选择角色</h2></div><span class="selection">{{ characterName(selectedCharacterId) }}</span></div>
          <button
            v-for="character in room.characters"
            :key="character.id"
            type="button"
            class="character-card"
            :class="{ selected: selectedCharacterId === character.id, occupied: occupiedCharacterIds.has(character.id) && selectedCharacterId !== character.id }"
            :disabled="!isWaiting || roomsStore.mutating || (occupiedCharacterIds.has(character.id) && selectedCharacterId !== character.id)"
            data-testid="character-card"
            @click="chooseCharacter(character.id)"
          >
            <span><strong>{{ character.name }}</strong><small>{{ character.description || '暂无角色简介' }}</small></span>
            <Check v-if="selectedCharacterId === character.id" />
            <em v-else-if="occupiedCharacterIds.has(character.id)">已被选择</em>
          </button>
        </section>
      </div>

      <footer class="action-bar">
        <div>
          <strong>{{ room.status === 'ended' ? '这场冒险已结束' : roomsStore.currentMember?.is_ready ? '你已准备就绪' : selectedCharacterId ? '确认角色后即可准备' : '请先选择角色' }}</strong>
          <small>大厅状态以服务器版本为准，冲突时会自动刷新。</small>
        </div>
        <el-button v-if="!roomsStore.isOwner && isWaiting" type="danger" plain :disabled="roomsStore.mutating" @click="leaveLobby">退出房间</el-button>
        <el-button v-if="isWaiting" :type="roomsStore.currentMember?.is_ready ? 'warning' : 'success'" :loading="roomsStore.mutating" :disabled="!selectedCharacterId" data-testid="ready-button" @click="toggleReady">{{ roomsStore.currentMember?.is_ready ? '取消准备' : '准备就绪' }}</el-button>
        <el-button v-if="roomsStore.isOwner && isWaiting" type="primary" :loading="roomsStore.mutating" :disabled="!roomsStore.canStart" data-testid="start-room-button" @click="startGame">开始游戏</el-button>
        <el-button v-if="room.status === 'playing' || room.status === 'paused'" type="primary" data-testid="enter-game-button" @click="router.push(`/game/multiplayer/${roomId}`)">进入游戏</el-button>
        <el-button v-if="room.status === 'ended'" type="primary" data-testid="enter-game-button" @click="router.push(`/game/multiplayer/${roomId}`)">查看最近记录</el-button>
      </footer>
    </section>

    <section v-else v-loading="roomsStore.loading" class="loading-panel">
      <UserFilled /><p>正在读取权威大厅快照…</p>
    </section>
  </main>
</template>

<style scoped>
.lobby-view { min-height: 100vh; padding: 0 34px 42px; box-sizing: border-box; color: #e9e9f1; text-align: left; background: radial-gradient(circle at 8% 0, rgba(88,91,205,.19), transparent 30%), radial-gradient(circle at 92% 6%, rgba(63,195,172,.12), transparent 28%), #141424; }.topbar { min-height: 70px; display: flex; align-items: center; justify-content: space-between; border-bottom: 1px solid #303047; }.connection { display: flex; align-items: center; gap: 8px; color: #8b8b9d; font-size: 12px; }.connection > span { width: 8px; height: 8px; border-radius: 50%; background: #6f6f82; }.connection.online > span { background: #62d39b; box-shadow: 0 0 0 4px rgba(98,211,155,.12); }.connection.online { color: #a9dbc3; }
.lobby-shell { padding-top: 38px; }.lobby-heading { display: flex; justify-content: space-between; gap: 24px; align-items: center; }.lobby-heading h1 { margin: 7px 0; color: #fff; font-size: 36px; }.lobby-heading > div > p:last-child { color: #8e8ea2; }.eyebrow { color: #8e91ff; font-size: 11px; letter-spacing: .16em; }.room-code { display: grid; grid-template-columns: auto auto; align-items: center; column-gap: 6px; padding: 14px 16px; border: 1px solid #3b3b56; border-radius: 14px; background: #202036; }.room-code small { grid-column: 1 / -1; color: #838397; font-size: 10px; letter-spacing: .13em; }.room-code strong { font: 22px ui-monospace, Consolas, monospace; letter-spacing: .13em; color: #bcbdfd; }.phase-alert { margin-top: 20px; }
.lobby-columns { display: grid; grid-template-columns: minmax(320px, .86fr) minmax(360px, 1.14fr); gap: 20px; margin-top: 24px; }.panel { min-height: 420px; padding: 22px; border: 1px solid #33334a; border-radius: 16px; background: rgba(29,29,47,.94); }.panel-heading { display: flex; align-items: center; justify-content: space-between; margin-bottom: 18px; }.panel-heading h2 { margin: 4px 0 0; color: #f1f1f6; }.selection { max-width: 180px; overflow: hidden; color: #a8a9ee; font-size: 12px; text-overflow: ellipsis; white-space: nowrap; }.member-row { display: flex; align-items: center; gap: 12px; min-height: 62px; padding: 8px 0; border-bottom: 1px solid #303046; }.member-copy { min-width: 0; flex: 1; }.member-copy strong, .member-copy small { display: block; }.member-copy strong { color: #f0f0f5; }.member-copy small { margin-top: 5px; color: #858598; font-size: 12px; }.character-card { width: 100%; min-height: 72px; display: flex; justify-content: space-between; align-items: center; gap: 16px; padding: 13px 15px; margin-bottom: 10px; border: 1px solid #373750; border-radius: 12px; color: inherit; text-align: left; background: #25253b; cursor: pointer; }.character-card:hover:not(:disabled) { border-color: #777ae5; }.character-card.selected { border-color: #7174df; background: rgba(91,94,207,.2); }.character-card.occupied { opacity: .52; }.character-card span { min-width: 0; }.character-card strong, .character-card small { display: block; }.character-card small { margin-top: 5px; color: #8e8ea0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }.character-card svg { width: 21px; color: #8f92ff; }.character-card em { color: #848497; font-size: 11px; font-style: normal; white-space: nowrap; }
.action-bar { display: flex; align-items: center; justify-content: flex-end; gap: 10px; margin-top: 20px; padding: 18px 20px; border: 1px solid #34344b; border-radius: 15px; background: #1d1d30; }.action-bar > div { margin-right: auto; }.action-bar strong, .action-bar small { display: block; }.action-bar small { margin-top: 4px; color: #838397; font-size: 11px; }.loading-panel { min-height: 65vh; display: grid; place-content: center; justify-items: center; gap: 10px; color: #858598; }.loading-panel svg { width: 44px; }
@media (max-width: 820px) { .lobby-view { padding: 0 16px 26px; }.connection { display: none; }.lobby-heading { align-items: stretch; flex-direction: column; }.room-code { align-self: flex-start; }.lobby-columns { grid-template-columns: 1fr; }.panel { min-height: auto; }.action-bar { align-items: stretch; flex-direction: column; }.action-bar > div { margin: 0 0 8px; }.action-bar .el-button { margin-left: 0; } }
</style>
