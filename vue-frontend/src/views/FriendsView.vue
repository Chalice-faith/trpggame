<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  ArrowLeft,
  Check,
  Close,
  Delete,
  Plus,
  Refresh,
  Search
} from '@element-plus/icons-vue'
import type {
  FriendItem,
  FriendRequestItem,
  PresenceStatus,
  UserSearchItem
} from '@/api/friends'
import { useFriendsStore } from '@/stores/friends'
import { useIMStore } from '@/stores/im'

const router = useRouter()
const friendsStore = useFriendsStore()
const imStore = useIMStore()
const keyword = ref('')
const actingKey = ref('')

const hasSearch = computed(() => keyword.value.trim().length > 0)
const presenceCopy: Record<PresenceStatus, string> = {
  online: '在线',
  offline: '离线',
  unknown: '状态未知'
}

function userName(user: { nickname: string; username: string }) {
  return user.nickname.trim() || user.username
}

function avatarText(user: { nickname: string; username: string }) {
  return [...userName(user)][0]?.toUpperCase() || '?'
}

function searchAction(item: UserSearchItem) {
  if (!item.friendship) return 'add'
  if (item.friendship.status === 'accepted') return 'friend'
  if (
    item.friendship.status === 'pending' &&
    item.friendship.direction === 'incoming'
  ) {
    return 'incoming'
  }
  if (item.friendship.status === 'pending') return 'outgoing'
  return 'add'
}

async function runSearch() {
  if (!hasSearch.value) {
    friendsStore.searchResults = []
    return
  }
  try {
    await friendsStore.searchUsers(keyword.value)
  } catch (error: any) {
    ElMessage.error(error?.response?.data?.message || '搜索用户失败')
  }
}

async function sendRequest(item: UserSearchItem) {
  const key = `send-${item.user.id}`
  actingKey.value = key
  try {
    await friendsStore.sendRequest(item.user.id)
    ElMessage.success('好友申请已发送')
  } catch (error: any) {
    ElMessage.error(error?.response?.data?.message || '发送申请失败')
  } finally {
    actingKey.value = ''
  }
}

async function respond(request: FriendRequestItem, accept: boolean) {
  const key = `${accept ? 'accept' : 'reject'}-${request.id}`
  actingKey.value = key
  try {
    if (accept) await friendsStore.acceptRequest(request.id)
    else await friendsStore.rejectRequest(request.id)
    ElMessage.success(accept ? '已添加为好友' : '已拒绝好友申请')
  } catch (error: any) {
    ElMessage.error(error?.response?.data?.message || '处理申请失败')
  } finally {
    actingKey.value = ''
  }
}

async function removeFriend(item: FriendItem) {
  try {
    await ElMessageBox.confirm(
      `确定删除好友“${userName(item.peer)}”吗？`,
      '删除好友',
      { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消' }
    )
  } catch {
    return
  }

  const key = `delete-${item.peer.id}`
  actingKey.value = key
  try {
    await friendsStore.removeFriend(item.peer.id)
    ElMessage.success('好友已删除')
  } catch (error: any) {
    ElMessage.error(error?.response?.data?.message || '删除好友失败')
  } finally {
    actingKey.value = ''
  }
}

async function refresh() {
  try {
    await friendsStore.refreshAll()
  } catch {
    ElMessage.error(friendsStore.errorMessage)
  }
}

onMounted(refresh)
</script>

<template>
  <main class="friends-view">
    <header class="topbar">
      <div class="brand">
        <span class="brand-mark">D20</span>
        <div>
          <strong>好友与在线状态</strong>
          <small>寻找同伴，为多人冒险做准备</small>
        </div>
      </div>
      <div class="topbar-actions">
        <span
          class="connection-state"
          :class="{ connected: imStore.isConnected }"
        >
          {{ imStore.isConnected ? '实时连接正常' : '实时连接中断' }}
        </span>
        <el-button :icon="ArrowLeft" @click="router.push('/dashboard')">
          返回剧本库
        </el-button>
      </div>
    </header>

    <el-alert
      v-if="imStore.replacementNotice"
      class="replacement-alert"
      type="warning"
      show-icon
      :closable="false"
      :title="imStore.replacementNotice"
      description="为避免多个页面争用在线状态，本页面不会自动重连。重新登录后可恢复。"
    />

    <section class="search-panel" aria-labelledby="friend-search-title">
      <div>
        <p class="eyebrow">FIND PLAYERS</p>
        <h1 id="friend-search-title">寻找冒险同伴</h1>
        <p>可输入用户 ID、用户名或昵称，邮箱不会参与搜索。</p>
      </div>
      <div class="search-row">
        <el-input
          v-model="keyword"
          data-testid="friend-search-input"
          clearable
          placeholder="用户 ID / 用户名 / 昵称"
          :prefix-icon="Search"
          @keyup.enter="runSearch"
          @clear="friendsStore.searchResults = []"
        />
        <el-button
          type="primary"
          data-testid="friend-search-button"
          :loading="friendsStore.searching"
          :disabled="!hasSearch"
          @click="runSearch"
        >
          搜索
        </el-button>
      </div>

      <div v-if="friendsStore.searchResults.length" class="search-results">
        <article
          v-for="item in friendsStore.searchResults"
          :key="item.user.id"
          class="person-row"
          data-testid="search-result"
        >
          <el-avatar :size="42" :src="item.user.avatar_url">
            {{ avatarText(item.user) }}
          </el-avatar>
          <div class="person-copy">
            <strong>{{ userName(item.user) }}</strong>
            <span>@{{ item.user.username }} · ID {{ item.user.id }}</span>
          </div>
          <el-button
            v-if="searchAction(item) === 'add'"
            type="primary"
            plain
            :icon="Plus"
            data-testid="send-request-button"
            :loading="actingKey === `send-${item.user.id}`"
            @click="sendRequest(item)"
          >
            添加好友
          </el-button>
          <el-tag v-else-if="searchAction(item) === 'friend'" type="success">
            已是好友
          </el-tag>
          <el-tag v-else-if="searchAction(item) === 'incoming'" type="warning">
            待你处理
          </el-tag>
          <el-tag v-else type="info">申请已发送</el-tag>
        </article>
      </div>
    </section>

    <section class="content-grid">
      <article class="panel friends-panel">
        <div class="panel-heading">
          <div>
            <p class="eyebrow">FRIENDS</p>
            <h2>我的好友</h2>
          </div>
          <el-button
            text
            :icon="Refresh"
            :loading="friendsStore.loading"
            aria-label="刷新好友数据"
            @click="refresh"
          />
        </div>

        <el-alert
          v-if="friendsStore.errorMessage"
          :title="friendsStore.errorMessage"
          type="error"
          :closable="false"
          show-icon
        />

        <div
          v-if="!friendsStore.loading && !friendsStore.sortedFriends.length"
          class="empty-state"
        >
          还没有好友，从上方搜索一位同伴吧。
        </div>
        <article
          v-for="item in friendsStore.sortedFriends"
          :key="item.id"
          class="person-row friend-row"
          data-testid="friend-row"
        >
          <el-badge
            is-dot
            :class="`presence-${item.presence}`"
            class="presence-badge"
          >
            <el-avatar :size="46" :src="item.peer.avatar_url">
              {{ avatarText(item.peer) }}
            </el-avatar>
          </el-badge>
          <div class="person-copy">
            <strong>{{ userName(item.peer) }}</strong>
            <span>@{{ item.peer.username }} · {{ presenceCopy[item.presence] }}</span>
          </div>
          <el-button disabled>聊天（M2.2）</el-button>
          <el-button
            text
            type="danger"
            :icon="Delete"
            data-testid="delete-friend-button"
            :loading="actingKey === `delete-${item.peer.id}`"
            @click="removeFriend(item)"
          >
            删除
          </el-button>
        </article>
      </article>

      <div class="request-column">
        <article class="panel">
          <div class="panel-heading">
            <div>
              <p class="eyebrow">INCOMING</p>
              <h2>收到的申请</h2>
            </div>
            <el-tag round>{{ friendsStore.incomingRequests.length }}</el-tag>
          </div>
          <div v-if="!friendsStore.incomingRequests.length" class="empty-state compact">
            暂无待处理申请
          </div>
          <article
            v-for="request in friendsStore.incomingRequests"
            :key="request.id"
            class="person-row request-row"
            data-testid="incoming-request"
          >
            <el-avatar :size="38" :src="request.peer.avatar_url">
              {{ avatarText(request.peer) }}
            </el-avatar>
            <div class="person-copy">
              <strong>{{ userName(request.peer) }}</strong>
              <span>@{{ request.peer.username }}</span>
            </div>
            <el-button
              circle
              type="success"
              :icon="Check"
              data-testid="accept-request-button"
              :loading="actingKey === `accept-${request.id}`"
              aria-label="接受申请"
              @click="respond(request, true)"
            />
            <el-button
              circle
              :icon="Close"
              data-testid="reject-request-button"
              :loading="actingKey === `reject-${request.id}`"
              aria-label="拒绝申请"
              @click="respond(request, false)"
            />
          </article>
        </article>

        <article class="panel">
          <div class="panel-heading">
            <div>
              <p class="eyebrow">OUTGOING</p>
              <h2>发出的申请</h2>
            </div>
            <el-tag type="info" round>{{ friendsStore.outgoingRequests.length }}</el-tag>
          </div>
          <div v-if="!friendsStore.outgoingRequests.length" class="empty-state compact">
            暂无等待回应的申请
          </div>
          <article
            v-for="request in friendsStore.outgoingRequests"
            :key="request.id"
            class="person-row request-row"
          >
            <el-avatar :size="38" :src="request.peer.avatar_url">
              {{ avatarText(request.peer) }}
            </el-avatar>
            <div class="person-copy">
              <strong>{{ userName(request.peer) }}</strong>
              <span>@{{ request.peer.username }}</span>
            </div>
            <el-tag type="info">等待回应</el-tag>
          </article>
        </article>
      </div>
    </section>
  </main>
</template>

<style scoped>
.friends-view {
  min-height: 100vh;
  padding: 0 38px 56px;
  box-sizing: border-box;
  text-align: left;
  background:
    radial-gradient(circle at 12% 0%, rgba(63, 195, 172, 0.13), transparent 30%),
    radial-gradient(circle at 88% 8%, rgba(233, 69, 96, 0.13), transparent 28%),
    #141424;
}

.topbar,
.brand,
.topbar-actions,
.panel-heading,
.person-row,
.search-row {
  display: flex;
  align-items: center;
}

.topbar {
  min-height: 78px;
  justify-content: space-between;
  border-bottom: 1px solid rgba(255, 255, 255, 0.08);
}

.brand { gap: 12px; }
.brand strong,
.brand small { display: block; }
.brand strong { color: #f7f7fb; }
.brand small { margin-top: 3px; color: #85859d; }

.brand-mark {
  display: grid;
  width: 42px;
  height: 42px;
  place-items: center;
  border-radius: 12px;
  color: #fff;
  font-size: 12px;
  font-weight: 800;
  background: linear-gradient(135deg, #3fc3ac, #2a7e87);
}

.topbar-actions { gap: 16px; }
.connection-state { color: #9191a8; font-size: 13px; }
.connection-state::before {
  content: '';
  display: inline-block;
  width: 7px;
  height: 7px;
  margin-right: 7px;
  border-radius: 50%;
  background: #77778c;
}
.connection-state.connected { color: #61d6b7; }
.connection-state.connected::before { background: #3fc3ac; }

.replacement-alert { margin-top: 24px; }

.search-panel,
.panel {
  border: 1px solid rgba(255, 255, 255, 0.08);
  border-radius: 18px;
  background: rgba(27, 27, 47, 0.94);
  box-shadow: 0 18px 50px rgba(0, 0, 0, 0.18);
}

.search-panel {
  display: grid;
  grid-template-columns: minmax(260px, 0.8fr) minmax(340px, 1.2fr);
  gap: 34px;
  margin-top: 28px;
  padding: 30px;
}

.search-panel h1,
.panel h2 { margin: 3px 0 8px; color: #f8f8fb; }
.search-panel p { margin: 0; color: #9595aa; }
.eyebrow {
  margin: 0 !important;
  color: #3fc3ac !important;
  font-size: 11px;
  font-weight: 800;
  letter-spacing: 0.16em;
}
.search-row { gap: 10px; }
.search-results {
  grid-column: 1 / -1;
  border-top: 1px solid rgba(255, 255, 255, 0.07);
}

.content-grid {
  display: grid;
  grid-template-columns: minmax(0, 1.35fr) minmax(340px, 0.65fr);
  gap: 20px;
  margin-top: 20px;
}
.panel { padding: 24px; }
.panel-heading { justify-content: space-between; margin-bottom: 14px; }
.panel-heading h2 { margin-bottom: 0; font-size: 21px; }
.request-column { display: grid; gap: 20px; align-content: start; }

.person-row {
  gap: 12px;
  min-height: 62px;
  border-top: 1px solid rgba(255, 255, 255, 0.06);
}
.person-copy { min-width: 0; flex: 1; }
.person-copy strong,
.person-copy span { display: block; }
.person-copy strong {
  overflow: hidden;
  color: #f0f0f5;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.person-copy span { margin-top: 3px; color: #85859d; font-size: 13px; }
.request-row { min-height: 58px; }

.presence-badge :deep(.el-badge__content.is-dot) { top: 39px; right: 8px; }
.presence-online :deep(.el-badge__content) { background: #3fc3ac; }
.presence-offline :deep(.el-badge__content) { background: #69697d; }
.presence-unknown :deep(.el-badge__content) { background: #d19a4b; }

.empty-state {
  padding: 56px 16px;
  color: #77778e;
  text-align: center;
}
.empty-state.compact { padding: 25px 12px; }

@media (max-width: 900px) {
  .friends-view { padding: 0 18px 36px; }
  .search-panel,
  .content-grid { grid-template-columns: 1fr; }
  .search-panel { gap: 22px; }
  .topbar { align-items: flex-start; gap: 16px; padding: 16px 0; }
  .topbar-actions { align-items: flex-end; flex-direction: column; gap: 8px; }
}

@media (max-width: 580px) {
  .connection-state { display: none; }
  .friend-row { flex-wrap: wrap; padding: 10px 0; }
  .friend-row .person-copy { min-width: calc(100% - 64px); }
}
</style>
