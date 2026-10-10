<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import {
  ArrowLeft,
  ChatDotRound,
  Refresh,
  UserFilled
} from '@element-plus/icons-vue'
import type { ConversationSummary } from '@/api/chat'
import { useAuthStore } from '@/stores/auth'
import { useChatStore, type ChatMessageView } from '@/stores/chat'
import { useIMStore } from '@/stores/im'

const route = useRoute()
const router = useRouter()
const authStore = useAuthStore()
const chatStore = useChatStore()
const imStore = useIMStore()
const draft = ref('')
const openingConversation = ref(false)

const canSend = computed(
  () => Boolean(chatStore.selectedConversation?.can_send && imStore.isConnected)
)
const composerPlaceholder = computed(() => {
  const conversation = chatStore.selectedConversation
  if (!conversation?.can_send) {
    return conversation?.type === 'group'
      ? '已不在群组中，无法继续发送'
      : '已不是好友，历史消息仅供查看'
  }
  if (!imStore.isConnected) return '实时连接已断开，重连后可继续发送'
  return '输入消息，Enter 发送，Shift + Enter 换行'
})

function routeConversationId() {
  const raw = Array.isArray(route.params.conversationId)
    ? route.params.conversationId[0]
    : route.params.conversationId
  const value = Number(raw)
  return Number.isInteger(value) && value > 0 ? value : null
}

async function openRouteConversation() {
  const conversationId = routeConversationId()
  if (!conversationId) {
    chatStore.selectedConversationId = null
    return
  }
  openingConversation.value = true
  try {
    while (
      !chatStore.conversations.some(({ id }) => id === conversationId) &&
      chatStore.nextCursor
    ) {
      await chatStore.loadMoreConversations()
    }
    if (!chatStore.conversations.some(({ id }) => id === conversationId)) {
      throw new Error('conversation not found')
    }
    await chatStore.openConversation(conversationId)
  } catch {
    ElMessage.error('会话加载失败或已不可用')
    await router.replace('/chat')
  } finally {
    openingConversation.value = false
  }
}

async function refreshConversations() {
  try {
    await chatStore.loadConversations(true)
    await openRouteConversation()
  } catch {
    ElMessage.error(chatStore.errorMessage || '会话列表加载失败')
  }
}

async function selectConversation(conversation: ConversationSummary) {
  if (conversation.id === routeConversationId()) {
    await chatStore.openConversation(conversation.id)
    return
  }
  await router.push(`/chat/${conversation.id}`)
}

function sendMessage() {
  const conversation = chatStore.selectedConversation
  if (!conversation || !draft.value.trim()) return
  if (!chatStore.sendText(conversation.id, draft.value)) {
    ElMessage.warning(composerPlaceholder.value)
    return
  }
  draft.value = ''
}

function handleComposerKeydown(event: KeyboardEvent) {
  if (event.key !== 'Enter' || event.shiftKey) return
  event.preventDefault()
  sendMessage()
}

function retryMessage(message: ChatMessageView) {
  if (!chatStore.retryMessage(message)) {
    ElMessage.warning('实时连接不可用，请稍后重试')
  }
}

function isMine(message: ChatMessageView) {
  return message.sender?.id === authStore.user?.id
}

function displayName(conversation: ConversationSummary) {
  if (conversation.type === 'group') return conversation.group.name
  return conversation.peer.nickname.trim() || conversation.peer.username
}

function avatarText(conversation: ConversationSummary) {
  return [...displayName(conversation)][0]?.toUpperCase() || '?'
}

function avatarUrl(conversation: ConversationSummary) {
  return conversation.type === 'group'
    ? conversation.group.avatar_url
    : conversation.peer.avatar_url
}

function conversationSubtitle(conversation: ConversationSummary) {
  return conversation.type === 'group'
    ? `${conversation.group.member_count} 人 · ${groupRoleCopy[conversation.group.current_user_role]}`
    : `@${conversation.peer.username}`
}

function systemMessageText(message: ChatMessageView) {
  const event = typeof message.metadata?.event === 'string' ? message.metadata.event : ''
  const target = Number(message.metadata?.target_user_id) || 0
  const actor = message.sender?.nickname.trim() || message.sender?.username || '成员'
  const copy: Record<string, string> = {
    group_created: `${actor} 创建了群组`,
    group_name_changed: `${actor} 修改了群名称`,
    group_avatar_changed: `${actor} 修改了群头像`,
    member_joined: `${actor} 邀请成员 #${target} 加入群组`,
    member_role_changed: `${actor} 调整了成员 #${target} 的角色`,
    member_removed: `${actor} 将成员 #${target} 移出群组`,
    member_left: `成员 #${target} 退出了群组`,
    owner_transferred: `${actor} 将群主转让给成员 #${target}`
  }
  return copy[event] || message.content
}

const groupRoleCopy = { owner: '群主', admin: '管理员', member: '成员' } as const

function formatTime(value: string) {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return ''
  const today = new Date()
  if (date.toDateString() === today.toDateString()) {
    return new Intl.DateTimeFormat('zh-CN', {
      hour: '2-digit',
      minute: '2-digit'
    }).format(date)
  }
  return new Intl.DateTimeFormat('zh-CN', {
    month: '2-digit',
    day: '2-digit'
  }).format(date)
}

onMounted(refreshConversations)
watch(() => route.params.conversationId, openRouteConversation)
</script>

<template>
  <main class="chat-view">
    <header class="topbar">
      <div class="brand">
        <span class="brand-mark"><ChatDotRound /></span>
        <div>
          <strong>冒险者消息</strong>
          <small>{{ chatStore.totalUnread ? `${chatStore.totalUnread} 条未读` : '所有消息已读' }}</small>
        </div>
      </div>
      <div class="topbar-actions">
        <span class="connection-state" :class="{ connected: imStore.isConnected }">
          {{ imStore.isConnected ? '实时连接正常' : '实时连接中断' }}
        </span>
        <el-button :icon="UserFilled" @click="router.push('/friends')">好友</el-button>
        <el-button :icon="UserFilled" @click="router.push('/groups')">群组</el-button>
        <el-button :icon="ArrowLeft" @click="router.push('/dashboard')">剧本库</el-button>
      </div>
    </header>

    <el-alert
      v-if="imStore.replacementNotice"
      class="connection-alert"
      type="warning"
      show-icon
      :closable="false"
      :title="imStore.replacementNotice"
    />

    <section class="chat-shell">
      <aside class="conversation-panel">
        <div class="conversation-heading">
          <div>
            <p class="eyebrow">CONVERSATIONS</p>
            <h1>会话</h1>
          </div>
          <el-button
            text
            :icon="Refresh"
            :loading="chatStore.loadingConversations"
            aria-label="刷新会话"
            @click="refreshConversations"
          />
        </div>

        <el-alert
          v-if="chatStore.errorMessage"
          type="error"
          :closable="false"
          :title="chatStore.errorMessage"
        />

        <div
          v-if="!chatStore.loadingConversations && !chatStore.conversations.length"
          class="empty-conversations"
        >
          <ChatDotRound />
          <strong>还没有会话</strong>
          <span>从好友页开始私聊，或创建一个冒险群组。</span>
          <el-button type="primary" plain @click="router.push('/groups')">创建群组</el-button>
        </div>

        <button
          v-for="conversation in chatStore.conversations"
          :key="conversation.id"
          type="button"
          class="conversation-row"
          :class="{ active: chatStore.selectedConversationId === conversation.id }"
          data-testid="conversation-row"
          @click="selectConversation(conversation)"
        >
          <el-avatar :size="46" :src="avatarUrl(conversation)">
            {{ avatarText(conversation) }}
          </el-avatar>
          <span class="conversation-copy">
            <span class="conversation-title">
              <strong>{{ displayName(conversation) }}</strong>
              <time v-if="conversation.last_message">
                {{ formatTime(conversation.last_message.created_at) }}
              </time>
            </span>
            <span class="conversation-preview">
              {{ conversation.last_message?.content || '开始新的对话' }}
            </span>
          </span>
          <el-badge
            v-if="conversation.unread_count"
            :value="conversation.unread_count > 99 ? '99+' : conversation.unread_count"
            class="unread-badge"
          />
        </button>

        <el-button
          v-if="chatStore.nextCursor"
          class="load-more-conversations"
          text
          :loading="chatStore.loadingConversations"
          @click="chatStore.loadMoreConversations"
        >
          加载更多会话
        </el-button>
      </aside>

      <section v-if="chatStore.selectedConversation" class="message-panel">
        <header class="message-heading">
          <el-avatar :size="40" :src="avatarUrl(chatStore.selectedConversation)">
            {{ avatarText(chatStore.selectedConversation) }}
          </el-avatar>
          <div>
            <strong>{{ displayName(chatStore.selectedConversation) }}</strong>
            <small>{{ conversationSubtitle(chatStore.selectedConversation) }}</small>
          </div>
          <el-button
            v-if="chatStore.selectedConversation.type === 'group'"
            text
            @click="router.push(`/groups/${chatStore.selectedConversation.group.id}`)"
          >
            群成员
          </el-button>
          <el-tag v-if="!chatStore.selectedConversation.can_send" type="warning">历史只读</el-tag>
        </header>

        <div class="message-scroll" data-testid="message-list">
          <div class="history-control">
            <el-button
              v-if="chatStore.currentHistory.hasMore"
              text
              :loading="chatStore.currentHistory.loading"
              data-testid="load-older-button"
              @click="chatStore.loadOlderMessages(chatStore.selectedConversation.id)"
            >
              加载更早消息
            </el-button>
            <span v-else-if="chatStore.currentHistory.loaded">已到达对话起点</span>
          </div>

          <div v-if="openingConversation" class="message-empty">正在加载会话……</div>
          <div
            v-else-if="!chatStore.currentMessages.length"
            class="message-empty"
          >
            在 {{ displayName(chatStore.selectedConversation) }} 发出第一条消息吧。
          </div>

          <article
            v-for="message in chatStore.currentMessages"
            :key="message.client_message_id || message.id"
            class="message-row"
            :class="{ mine: isMine(message), system: message.message_type === 'system' }"
            data-testid="chat-message"
          >
            <div v-if="message.message_type === 'system'" class="system-message">
              {{ systemMessageText(message) }}
            </div>
            <div v-else class="message-bubble">
              <small v-if="chatStore.selectedConversation.type === 'group' && !isMine(message)" class="sender-name">
                {{ message.sender?.nickname || message.sender?.username || '未知成员' }}
              </small>
              <p>{{ message.content }}</p>
              <footer>
                <time>{{ formatTime(message.created_at) }}</time>
                <span v-if="message.delivery_status === 'pending'">发送中</span>
                <span v-else-if="message.delivery_status === 'failed'" class="failed-copy">
                  {{ message.error_message || '发送失败' }}
                </span>
              </footer>
              <el-button
                v-if="message.delivery_status === 'failed'"
                class="retry-button"
                size="small"
                text
                data-testid="retry-message-button"
                @click="retryMessage(message)"
              >
                重试
              </el-button>
            </div>
          </article>
        </div>

        <footer class="composer">
          <el-alert
            v-if="!chatStore.selectedConversation.can_send"
            type="warning"
            :closable="false"
            :title="chatStore.selectedConversation.type === 'group' ? '你已不在群组中，不能继续发送。' : '好友关系已解除，历史消息仍可查看，但不能继续发送。'"
          />
          <div class="composer-row">
            <el-input
              v-model="draft"
              type="textarea"
              resize="none"
              :rows="2"
              maxlength="4000"
              show-word-limit
              data-testid="chat-composer"
              :disabled="!canSend"
              :placeholder="composerPlaceholder"
              @keydown="handleComposerKeydown"
            />
            <el-button
              type="primary"
              data-testid="send-message-button"
              :disabled="!canSend || !draft.trim()"
              @click="sendMessage"
            >
              发送
            </el-button>
          </div>
        </footer>
      </section>

      <section v-else class="message-panel no-selection">
        <ChatDotRound />
        <h2>选择一段对话</h2>
        <p>消息会以 MySQL 会话序号为准，重连后自动补齐缺口。</p>
      </section>
    </section>
  </main>
</template>

<style scoped>
.chat-view {
  min-height: 100vh;
  padding: 0 34px 36px;
  box-sizing: border-box;
  color: #e9e9f1;
  text-align: left;
  background:
    radial-gradient(circle at 8% 2%, rgba(63, 195, 172, 0.13), transparent 28%),
    radial-gradient(circle at 92% 10%, rgba(233, 69, 96, 0.12), transparent 25%),
    #141424;
}

.topbar,
.brand,
.topbar-actions,
.conversation-heading,
.conversation-title,
.message-heading,
.composer-row {
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
  background: linear-gradient(135deg, #3fc3ac, #2a7e87);
}
.brand-mark svg { width: 21px; }
.topbar-actions { gap: 10px; }
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
.connection-alert { margin-top: 18px; }

.chat-shell {
  display: grid;
  grid-template-columns: minmax(260px, 320px) minmax(0, 1fr);
  height: calc(100vh - 114px);
  min-height: 600px;
  margin-top: 18px;
  overflow: hidden;
  border: 1px solid rgba(255, 255, 255, 0.08);
  border-radius: 18px;
  background: rgba(25, 25, 44, 0.96);
  box-shadow: 0 24px 70px rgba(0, 0, 0, 0.24);
}

.conversation-panel {
  overflow-y: auto;
  border-right: 1px solid rgba(255, 255, 255, 0.08);
  background: rgba(17, 17, 32, 0.62);
}
.conversation-heading {
  min-height: 74px;
  justify-content: space-between;
  padding: 0 18px;
  border-bottom: 1px solid rgba(255, 255, 255, 0.07);
}
.eyebrow { margin: 0; color: #3fc3ac; font-size: 10px; font-weight: 800; letter-spacing: 0.14em; }
.conversation-heading h1 { margin: 2px 0 0; color: #f8f8fb; font-size: 22px; }
.conversation-row {
  position: relative;
  display: flex;
  width: 100%;
  min-height: 76px;
  align-items: center;
  gap: 11px;
  padding: 12px 15px;
  border: 0;
  border-bottom: 1px solid rgba(255, 255, 255, 0.055);
  color: inherit;
  text-align: left;
  cursor: pointer;
  background: transparent;
}
.conversation-row:hover { background: rgba(255, 255, 255, 0.035); }
.conversation-row.active {
  background: linear-gradient(90deg, rgba(63, 195, 172, 0.16), rgba(63, 195, 172, 0.04));
  box-shadow: inset 3px 0 #3fc3ac;
}
.conversation-copy { min-width: 0; flex: 1; }
.conversation-title { justify-content: space-between; gap: 8px; }
.conversation-title strong { overflow: hidden; color: #f1f1f6; text-overflow: ellipsis; white-space: nowrap; }
.conversation-title time { color: #727287; font-size: 11px; }
.conversation-preview {
  display: block;
  overflow: hidden;
  margin-top: 5px;
  color: #85859b;
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.unread-badge { margin-left: 2px; }
.load-more-conversations { width: 100%; margin: 8px 0; }
.empty-conversations {
  display: grid;
  min-height: 330px;
  padding: 20px;
  place-content: center;
  justify-items: center;
  gap: 9px;
  color: #77778c;
  text-align: center;
}
.empty-conversations svg { width: 34px; }
.empty-conversations strong { color: #bbbbca; }
.empty-conversations span { max-width: 220px; font-size: 13px; line-height: 1.5; }

.message-panel { display: flex; min-width: 0; flex-direction: column; }
.message-heading {
  min-height: 74px;
  gap: 11px;
  padding: 0 22px;
  border-bottom: 1px solid rgba(255, 255, 255, 0.07);
}
.message-heading div { min-width: 0; flex: 1; }
.message-heading strong,
.message-heading small { display: block; }
.message-heading strong { color: #f5f5f8; }
.message-heading small { margin-top: 2px; color: #77778c; font-size: 12px; }
.message-scroll {
  flex: 1;
  overflow-y: auto;
  padding: 12px clamp(18px, 4vw, 54px) 28px;
  background:
    linear-gradient(rgba(19, 19, 35, 0.67), rgba(19, 19, 35, 0.67)),
    radial-gradient(circle at 50% 0%, rgba(63, 195, 172, 0.07), transparent 38%);
}
.history-control { min-height: 38px; color: #67677c; font-size: 11px; text-align: center; }
.message-row { display: flex; margin: 12px 0; justify-content: flex-start; }
.message-row.mine { justify-content: flex-end; }
.message-row.system { justify-content: center; }
.system-message {
  max-width: 76%;
  padding: 6px 12px;
  border-radius: 999px;
  color: #85859b;
  font-size: 12px;
  text-align: center;
  background: rgba(255, 255, 255, 0.045);
}
.message-bubble {
  position: relative;
  max-width: min(72%, 680px);
  padding: 11px 14px 8px;
  border: 1px solid rgba(255, 255, 255, 0.065);
  border-radius: 5px 15px 15px 15px;
  background: #25253d;
}
.sender-name { display: block; margin-bottom: 4px; color: #63cbb7; font-size: 11px; }
.message-row.mine .message-bubble {
  border-color: rgba(63, 195, 172, 0.2);
  border-radius: 15px 5px 15px 15px;
  background: rgba(42, 126, 135, 0.34);
}
.message-bubble p { margin: 0; color: #ededf3; line-height: 1.6; white-space: pre-wrap; word-break: break-word; }
.message-bubble footer { display: flex; min-height: 17px; justify-content: flex-end; gap: 8px; margin-top: 5px; color: #88889d; font-size: 10px; }
.failed-copy { color: #f08d94; }
.retry-button { float: right; color: #ff9aa3; }
.message-empty { padding: 18vh 20px 0; color: #77778d; text-align: center; }

.composer {
  padding: 14px 18px 16px;
  border-top: 1px solid rgba(255, 255, 255, 0.08);
  background: rgba(21, 21, 38, 0.96);
}
.composer .el-alert { margin-bottom: 10px; }
.composer-row { align-items: stretch; gap: 10px; }
.composer-row .el-button { min-width: 76px; }
.no-selection { display: grid; place-content: center; justify-items: center; color: #6f6f84; text-align: center; }
.no-selection svg { width: 46px; }
.no-selection h2 { margin: 12px 0 4px; color: #b9b9c8; }
.no-selection p { margin: 0; font-size: 13px; }

@media (max-width: 820px) {
  .chat-view { padding: 0 14px 20px; }
  .topbar { align-items: flex-start; gap: 12px; padding: 14px 0; }
  .topbar-actions { align-items: flex-end; flex-wrap: wrap; justify-content: flex-end; }
  .connection-state { width: 100%; text-align: right; }
  .chat-shell { grid-template-columns: 112px minmax(0, 1fr); height: calc(100vh - 132px); margin-top: 10px; }
  .conversation-heading { padding: 0 10px; }
  .conversation-heading .eyebrow,
  .conversation-heading .el-button,
  .conversation-preview,
  .conversation-title time { display: none; }
  .conversation-heading h1 { font-size: 18px; }
  .conversation-row { min-height: 82px; flex-direction: column; gap: 5px; padding: 9px 6px; text-align: center; }
  .conversation-copy { width: 100%; }
  .conversation-title { justify-content: center; }
  .conversation-title strong { max-width: 96px; font-size: 12px; }
  .unread-badge { position: absolute; top: 8px; right: 10px; }
  .message-heading { padding: 0 14px; }
  .message-bubble { max-width: 86%; }
  .message-scroll { padding-right: 14px; padding-left: 14px; }
}

@media (max-width: 520px) {
  .brand small,
  .topbar-actions .connection-state,
  .topbar-actions .el-button:first-of-type { display: none; }
  .chat-shell { min-height: 520px; }
  .composer { padding: 10px; }
  .composer-row { flex-direction: column; }
  .composer-row .el-button { min-height: 38px; }
}
</style>
