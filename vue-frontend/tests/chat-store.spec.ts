// @vitest-environment jsdom

import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { ChatMessage, ConversationSummary } from '@/api/chat'
import { useAuthStore } from '@/stores/auth'
import { useChatStore } from '@/stores/chat'
import { useIMStore } from '@/stores/im'

const apiMocks = vi.hoisted(() => ({
  createDirectConversation: vi.fn(),
  listConversations: vi.fn(),
  listMessages: vi.fn(),
  markConversationRead: vi.fn()
}))

vi.mock('@/api/chat', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/api/chat')>()),
  ...apiMocks
}))

const peer = {
  id: 8,
  username: 'player08',
  nickname: '调查员',
  avatar_url: ''
}

function conversation(overrides: Partial<ConversationSummary> = {}): ConversationSummary {
  return {
    id: 41,
    type: 'direct',
    peer,
    group: null,
    can_send: true,
    last_seq: 0,
    last_read_seq: 0,
    unread_count: 0,
    last_message: null,
    created_at: '2026-09-15T01:00:00Z',
    updated_at: '2026-09-15T01:00:00Z',
    ...overrides
  }
}

function message(seq: number, clientId = `00000000-0000-4000-8000-${String(seq).padStart(12, '0')}`): ChatMessage {
  return {
    id: seq,
    conversation_id: 41,
    seq,
    sender: peer,
    client_message_id: clientId,
    message_type: 'text',
    content: `message-${seq}`,
    metadata: {},
    created_at: `2026-09-15T01:00:${String(seq).padStart(2, '0')}Z`
  }
}

describe('chat store', () => {
  beforeEach(() => {
    localStorage.clear()
    setActivePinia(createPinia())
    const authStore = useAuthStore()
    authStore.user = {
      id: 7,
      username: 'player07',
      email: 'player07@example.test',
      nickname: '守秘人',
      avatar_url: ''
    }
    authStore.accessToken = 'access-token'
    apiMocks.listConversations.mockResolvedValue({ items: [], next_cursor: null })
    apiMocks.listMessages.mockResolvedValue({ items: [], next_before_seq: 0, has_more: false })
    apiMocks.markConversationRead.mockImplementation(
      (conversationId: number, lastReadSeq: number) =>
        Promise.resolve({
          conversation_id: conversationId,
          last_read_seq: lastReadSeq,
          unread_count: 0
        })
    )
  })

  it('replaces an optimistic message with the authoritative ack', () => {
    const chatStore = useChatStore()
    const imStore = useIMStore()
    chatStore.conversations = [conversation()]
    imStore.status = 'connected'
    const send = vi.spyOn(imStore, 'send').mockReturnValue(true)
    const clientId = '550e8400-e29b-41d4-a716-446655440000'
    vi.spyOn(globalThis.crypto, 'randomUUID').mockReturnValue(clientId)

    expect(chatStore.sendText(41, '  今晚开团吗？  ')).toBe(true)
    expect(send).toHaveBeenCalledWith(
      'chat_message',
      { conversation_id: 41, message_type: 'text', content: '今晚开团吗？' },
      clientId
    )
    expect(chatStore.messagesByConversation[41][0]).toMatchObject({
      client_message_id: clientId,
      delivery_status: 'pending'
    })

    const authoritative = {
      ...message(1, clientId),
      sender: {
        id: 7,
        username: 'player07',
        nickname: '守秘人',
        avatar_url: ''
      },
      content: '今晚开团吗？'
    }
    chatStore.handleAck(clientId, { message: authoritative, duplicate: false })

    expect(chatStore.messagesByConversation[41]).toHaveLength(1)
    expect(chatStore.messagesByConversation[41][0]).toMatchObject({
      id: 1,
      seq: 1,
      delivery_status: 'sent'
    })
    expect(chatStore.conversations[0].last_read_seq).toBe(1)
  })

  it('keeps a failed message and retries with the same client ID', () => {
    const chatStore = useChatStore()
    const imStore = useIMStore()
    chatStore.conversations = [conversation()]
    imStore.status = 'connected'
    const send = vi.spyOn(imStore, 'send').mockReturnValue(false)

    expect(chatStore.sendText(41, '重试我')).toBe(false)
    const failed = chatStore.messagesByConversation[41][0]
    expect(failed.delivery_status).toBe('failed')

    send.mockReturnValue(true)
    expect(chatStore.retryMessage(failed)).toBe(true)
    expect(failed.delivery_status).toBe('pending')
    expect(send.mock.calls[1][2]).toBe(failed.client_message_id)
  })

  it('deduplicates sequence data and requests a detected gap', () => {
    const chatStore = useChatStore()
    const imStore = useIMStore()
    chatStore.conversations = [conversation({ last_seq: 3 })]
    imStore.status = 'connected'
    const send = vi.spyOn(imStore, 'send').mockReturnValue(true)

    chatStore.handleIncoming({ message: message(3) })
    expect(send).toHaveBeenCalledWith(
      'im_sync',
      { conversation_id: 41, since_seq: 0, limit: 100 },
      expect.any(String)
    )

    chatStore.handleSyncBatch({
      conversation_id: 41,
      messages: [message(1), message(2), message(3)],
      next_seq: 3,
      has_more: false
    })
    expect(chatStore.messagesByConversation[41].map(({ seq }) => seq)).toEqual([1, 2, 3])
  })

  it('marks pending messages failed on disconnect and clears all user data on logout', () => {
    const chatStore = useChatStore()
    const imStore = useIMStore()
    chatStore.conversations = [conversation()]
    imStore.status = 'connected'
    vi.spyOn(imStore, 'send').mockReturnValue(true)
    chatStore.sendText(41, '未确认消息')

    chatStore.handleDisconnected()
    expect(chatStore.messagesByConversation[41][0].delivery_status).toBe('failed')

    chatStore.clear()
    expect(chatStore.conversations).toEqual([])
    expect(chatStore.messagesByConversation).toEqual({})
    expect(chatStore.selectedConversationId).toBeNull()
  })
})
