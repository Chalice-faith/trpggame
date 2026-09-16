import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import {
  createDirectConversation,
  listConversations,
  listMessages,
  markConversationRead,
  type ChatMessage,
  type ConversationSummary
} from '@/api/chat'
import { useAuthStore } from '@/stores/auth'
import { useIMStore } from '@/stores/im'

export type DeliveryStatus = 'sent' | 'pending' | 'failed'

export interface ChatMessageView extends ChatMessage {
  delivery_status: DeliveryStatus
  error_message?: string
}

export interface ChatAckData {
  message: ChatMessage
  duplicate: boolean
}

export interface ChatMessageEventData {
  message: ChatMessage
}

export interface ConversationUpdatedData {
  conversation: ConversationSummary
}

export interface ImSyncBatchData {
  conversation_id: number
  messages: ChatMessage[]
  next_seq: number
  has_more: boolean
}

interface HistoryState {
  loaded: boolean
  loading: boolean
  hasMore: boolean
  nextBeforeSeq: number
}

const emptyHistory = (): HistoryState => ({
  loaded: false,
  loading: false,
  hasMore: false,
  nextBeforeSeq: 0
})

export const useChatStore = defineStore('chat', () => {
  const conversations = ref<ConversationSummary[]>([])
  const messagesByConversation = ref<Record<number, ChatMessageView[]>>({})
  const historyByConversation = ref<Record<number, HistoryState>>({})
  const selectedConversationId = ref<number | null>(null)
  const nextCursor = ref('')
  const loadingConversations = ref(false)
  const errorMessage = ref('')

  const syncingConversations = new Set<number>()
  const syncRequests = new Map<string, number>()
  let conversationLoad: Promise<void> | null = null
  let loadGeneration = 0

  const selectedConversation = computed(() =>
    conversations.value.find(({ id }) => id === selectedConversationId.value) ?? null
  )
  const currentMessages = computed(() =>
    selectedConversationId.value
      ? messagesByConversation.value[selectedConversationId.value] ?? []
      : []
  )
  const currentHistory = computed(() =>
    selectedConversationId.value
      ? ensureHistory(selectedConversationId.value)
      : emptyHistory()
  )
  const totalUnread = computed(() =>
    conversations.value.reduce((total, item) => total + item.unread_count, 0)
  )

  async function loadConversations(reset = true) {
    if (conversationLoad) {
      await conversationLoad
      return
    }
    const pending = performConversationLoad(reset, loadGeneration)
    conversationLoad = pending
    try {
      await pending
    } finally {
      if (conversationLoad === pending) conversationLoad = null
    }
  }

  async function performConversationLoad(reset: boolean, generation: number) {
    loadingConversations.value = true
    errorMessage.value = ''
    try {
      const page = await listConversations(reset ? undefined : nextCursor.value)
      if (generation !== loadGeneration) return
      if (reset) conversations.value = []
      for (const conversation of page.items ?? []) upsertConversation(conversation)
      nextCursor.value = page.next_cursor ?? ''
      if (useIMStore().isConnected) syncLoadedGaps()
    } catch (error: any) {
      if (generation !== loadGeneration) return
      errorMessage.value = error?.response?.data?.message || '会话列表加载失败'
      throw error
    } finally {
      if (generation === loadGeneration) loadingConversations.value = false
    }
  }

  async function loadMoreConversations() {
    if (!nextCursor.value) return
    await loadConversations(false)
  }

  async function createDirect(peerUserId: number) {
    const conversation = await createDirectConversation(peerUserId)
    upsertConversation(conversation)
    return conversation
  }

  async function openConversation(conversationId: number) {
    selectedConversationId.value = conversationId
    const history = ensureHistory(conversationId)
    if (!history.loaded) await loadLatestMessages(conversationId)
    const conversation = findConversation(conversationId)
    const maximum = maximumAuthoritativeSeq(conversationId)
    if (conversation && maximum < conversation.last_seq) requestSync(conversationId, maximum)
    await markReadThroughKnownMessages(conversationId)
  }

  async function loadLatestMessages(conversationId: number) {
    const history = ensureHistory(conversationId)
    if (history.loading) return
    history.loading = true
    try {
      const page = await listMessages(conversationId)
      mergeMessages(conversationId, page.items ?? [])
      history.loaded = true
      history.hasMore = page.has_more
      history.nextBeforeSeq = page.next_before_seq
    } finally {
      history.loading = false
    }
  }

  async function loadOlderMessages(conversationId: number) {
    const history = ensureHistory(conversationId)
    if (history.loading || !history.hasMore || !history.nextBeforeSeq) return
    history.loading = true
    try {
      const page = await listMessages(conversationId, history.nextBeforeSeq)
      mergeMessages(conversationId, page.items ?? [])
      history.hasMore = page.has_more
      history.nextBeforeSeq = page.next_before_seq
    } finally {
      history.loading = false
    }
  }

  function sendText(conversationId: number, rawContent: string, retryId?: string) {
    const conversation = findConversation(conversationId)
    const content = rawContent.trim()
    if (!conversation?.can_send || !content || [...content].length > 4000) return false

    const imStore = useIMStore()
    const clientMessageId = retryId ?? newRequestId()
    const messages = ensureMessages(conversationId)
    let pending = messages.find(({ client_message_id }) => client_message_id === clientMessageId)
    if (!pending) {
      const user = useAuthStore().user
      pending = {
        id: 0,
        conversation_id: conversationId,
        seq: 0,
        sender: user
          ? {
              id: user.id,
              username: user.username,
              nickname: user.nickname,
              avatar_url: user.avatar_url
            }
          : null,
        client_message_id: clientMessageId,
        message_type: 'text',
        content,
        metadata: {},
        created_at: new Date().toISOString(),
        delivery_status: 'pending'
      }
      messages.push(pending)
    } else {
      pending.delivery_status = 'pending'
      pending.error_message = undefined
    }

    const sent = imStore.send(
      'chat_message',
      { conversation_id: conversationId, message_type: 'text', content },
      clientMessageId
    )
    if (!sent) {
      pending.delivery_status = 'failed'
      pending.error_message = '实时连接不可用，请重连后重试'
    }
    return sent
  }

  function retryMessage(message: ChatMessageView) {
    if (message.delivery_status !== 'failed') return false
    return sendText(message.conversation_id, message.content, message.client_message_id)
  }

  function handleAck(requestId: string, data: ChatAckData) {
    if (!data?.message || !requestId) return
    mergeMessages(data.message.conversation_id, [data.message])
    applyMessageToConversation(data.message, true)
  }

  function handleIncoming(data: ChatMessageEventData) {
    const message = data?.message
    if (!message) return
    const previousMaximum = maximumAuthoritativeSeq(message.conversation_id)
    mergeMessages(message.conversation_id, [message])
    applyMessageToConversation(message, false)
    if (message.seq > previousMaximum + 1) requestSync(message.conversation_id, previousMaximum)
    if (selectedConversationId.value === message.conversation_id) {
      void markReadThroughKnownMessages(message.conversation_id)
    }
  }

  function handleConversationUpdated(data: ConversationUpdatedData) {
    if (!data?.conversation) return
    upsertConversation(data.conversation)
    if (selectedConversationId.value === data.conversation.id) {
      const maximum = maximumAuthoritativeSeq(data.conversation.id)
      if (maximum < data.conversation.last_seq) requestSync(data.conversation.id, maximum)
      else void markReadThroughKnownMessages(data.conversation.id)
    }
  }

  function handleSyncBatch(data: ImSyncBatchData) {
    const conversationId = data?.conversation_id
    if (!conversationId) return
    syncingConversations.delete(conversationId)
    for (const [requestId, target] of syncRequests) {
      if (target === conversationId) syncRequests.delete(requestId)
    }
    mergeMessages(conversationId, data.messages ?? [])
    for (const message of data.messages ?? []) applyMessageToConversation(message, false)
    if (data.has_more) requestSync(conversationId, data.next_seq)
    else if (selectedConversationId.value === conversationId) {
      void markReadThroughKnownMessages(conversationId)
    }
  }

  function handleError(requestId: string, message: string) {
    if (!requestId) return
    const syncConversationId = syncRequests.get(requestId)
    if (syncConversationId) {
      syncingConversations.delete(syncConversationId)
      syncRequests.delete(requestId)
    }
    for (const messages of Object.values(messagesByConversation.value)) {
      const pending = messages.find(
        (item) => item.client_message_id === requestId && item.delivery_status === 'pending'
      )
      if (pending) {
        pending.delivery_status = 'failed'
        pending.error_message = message || '发送失败，请重试'
        break
      }
    }
  }

  async function handleConnected() {
    try {
      await loadConversations(true)
      syncLoadedGaps()
    } catch {
      // 页面保留可重试入口；连接不因 REST 列表失败而断开。
    }
  }

  function handleDisconnected() {
    syncingConversations.clear()
    syncRequests.clear()
    for (const messages of Object.values(messagesByConversation.value)) {
      for (const message of messages) {
        if (message.delivery_status === 'pending') {
          message.delivery_status = 'failed'
          message.error_message = '连接已断开，请重试'
        }
      }
    }
  }

  async function markReadThroughKnownMessages(conversationId: number) {
    if (selectedConversationId.value !== conversationId) return
    const maximum = maximumAuthoritativeSeq(conversationId)
    const conversation = findConversation(conversationId)
    if (!conversation || maximum <= conversation.last_read_seq) return
    try {
      const result = await markConversationRead(conversationId, maximum)
      conversation.last_read_seq = result.last_read_seq
      conversation.unread_count = result.unread_count
    } catch {
      // 已读更新可在下次打开会话或收到事件时重试。
    }
  }

  function requestSync(conversationId: number, sinceSeq: number) {
    const imStore = useIMStore()
    if (!imStore.isConnected || syncingConversations.has(conversationId)) return false
    const requestId = newRequestId()
    const sent = imStore.send(
      'im_sync',
      { conversation_id: conversationId, since_seq: sinceSeq, limit: 100 },
      requestId
    )
    if (sent) {
      syncingConversations.add(conversationId)
      syncRequests.set(requestId, conversationId)
    }
    return sent
  }

  function syncLoadedGaps() {
    for (const conversation of conversations.value) {
      const maximum = maximumAuthoritativeSeq(conversation.id)
      if (maximum < conversation.last_seq) requestSync(conversation.id, maximum)
    }
  }

  function applyMessageToConversation(message: ChatMessage, sentByCurrentUser: boolean) {
    const conversation = findConversation(message.conversation_id)
    if (!conversation) return
    if (!conversation.last_message || message.seq >= conversation.last_message.seq) {
      conversation.last_message = message
    }
    conversation.last_seq = Math.max(conversation.last_seq, message.seq)
    if (sentByCurrentUser) conversation.last_read_seq = Math.max(conversation.last_read_seq, message.seq)
    conversation.unread_count = Math.max(conversation.last_seq - conversation.last_read_seq, 0)
    sortConversations()
  }

  function upsertConversation(conversation: ConversationSummary) {
    const index = conversations.value.findIndex(({ id }) => id === conversation.id)
    if (index >= 0) conversations.value[index] = conversation
    else conversations.value.push(conversation)
    sortConversations()
  }

  function sortConversations() {
    conversations.value.sort((left, right) => {
      const leftTime = Date.parse(left.last_message?.created_at ?? left.updated_at)
      const rightTime = Date.parse(right.last_message?.created_at ?? right.updated_at)
      return rightTime - leftTime || right.id - left.id
    })
  }

  function mergeMessages(conversationId: number, incoming: ChatMessage[]) {
    const messages = ensureMessages(conversationId)
    for (const item of incoming) {
      const view: ChatMessageView = { ...item, delivery_status: 'sent' }
      const index = messages.findIndex(
        (existing) =>
          (item.seq > 0 && existing.seq === item.seq) ||
          existing.client_message_id === item.client_message_id
      )
      if (index >= 0) messages[index] = view
      else messages.push(view)
    }
    messages.sort((left, right) => {
      if (left.seq && right.seq) return left.seq - right.seq
      if (left.seq) return -1
      if (right.seq) return 1
      return Date.parse(left.created_at) - Date.parse(right.created_at)
    })
  }

  function maximumAuthoritativeSeq(conversationId: number) {
    return ensureMessages(conversationId).reduce(
      (maximum, message) => Math.max(maximum, message.seq),
      0
    )
  }

  function ensureMessages(conversationId: number) {
    messagesByConversation.value[conversationId] ??= []
    return messagesByConversation.value[conversationId]
  }

  function ensureHistory(conversationId: number) {
    historyByConversation.value[conversationId] ??= emptyHistory()
    return historyByConversation.value[conversationId]
  }

  function findConversation(conversationId: number) {
    return conversations.value.find(({ id }) => id === conversationId)
  }

  function removeConversation(conversationId: number) {
    conversations.value = conversations.value.filter(({ id }) => id !== conversationId)
    delete messagesByConversation.value[conversationId]
    delete historyByConversation.value[conversationId]
    syncingConversations.delete(conversationId)
    for (const [requestId, target] of syncRequests) {
      if (target === conversationId) syncRequests.delete(requestId)
    }
    if (selectedConversationId.value === conversationId) selectedConversationId.value = null
  }

  function clear() {
    conversations.value = []
    messagesByConversation.value = {}
    historyByConversation.value = {}
    selectedConversationId.value = null
    nextCursor.value = ''
    loadingConversations.value = false
    errorMessage.value = ''
    syncingConversations.clear()
    syncRequests.clear()
    loadGeneration += 1
    conversationLoad = null
  }

  return {
    conversations,
    messagesByConversation,
    historyByConversation,
    selectedConversationId,
    nextCursor,
    loadingConversations,
    errorMessage,
    selectedConversation,
    currentMessages,
    currentHistory,
    totalUnread,
    loadConversations,
    loadMoreConversations,
    createDirect,
    openConversation,
    loadLatestMessages,
    loadOlderMessages,
    sendText,
    retryMessage,
    handleAck,
    handleIncoming,
    handleConversationUpdated,
    handleSyncBatch,
    handleError,
    handleConnected,
    handleDisconnected,
    requestSync,
    removeConversation,
    clear
  }
})

function newRequestId() {
  if (globalThis.crypto?.randomUUID) return globalThis.crypto.randomUUID()

  const bytes = new Uint8Array(16)
  if (globalThis.crypto?.getRandomValues) globalThis.crypto.getRandomValues(bytes)
  else {
    for (let index = 0; index < bytes.length; index += 1) {
      bytes[index] = Math.floor(Math.random() * 256)
    }
  }
  bytes[6] = (bytes[6] & 0x0f) | 0x40
  bytes[8] = (bytes[8] & 0x3f) | 0x80
  const hex = [...bytes].map((value) => value.toString(16).padStart(2, '0')).join('')
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
}
