// @vitest-environment jsdom

import { flushPromises, mount } from '@vue/test-utils'
import ElementPlus from 'element-plus'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import ChatView from '@/views/ChatView.vue'
import { useAuthStore } from '@/stores/auth'
import { useChatStore } from '@/stores/chat'
import { useIMStore } from '@/stores/im'

const routeState = vi.hoisted(() => ({ params: { conversationId: '41' as string | undefined } }))
const routerMocks = vi.hoisted(() => ({ push: vi.fn(), replace: vi.fn() }))
const apiMocks = vi.hoisted(() => ({
  createDirectConversation: vi.fn(),
  listConversations: vi.fn(),
  listMessages: vi.fn(),
  markConversationRead: vi.fn()
}))

vi.mock('vue-router', () => ({
  useRoute: () => routeState,
  useRouter: () => routerMocks
}))

vi.mock('@/api/chat', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/api/chat')>()),
  ...apiMocks
}))

const peer = { id: 8, username: 'player08', nickname: '调查员', avatar_url: '' }

function conversation(canSend = true) {
  return {
    id: 41,
    type: 'direct' as const,
    peer,
    group: null,
    can_send: canSend,
    last_seq: 1,
    last_read_seq: 0,
    unread_count: 1,
    last_message: {
      id: 1,
      conversation_id: 41,
      seq: 1,
      sender: peer,
      client_message_id: '00000000-0000-4000-8000-000000000001',
      message_type: 'text' as const,
      content: '准备好了吗？',
      metadata: {},
      created_at: '2026-09-15T01:00:01Z'
    },
    created_at: '2026-09-15T01:00:00Z',
    updated_at: '2026-09-15T01:00:01Z'
  }
}

function mountView() {
  return mount(ChatView, {
    attachTo: document.body,
    global: { plugins: [ElementPlus] }
  })
}

describe('ChatView', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    routeState.params.conversationId = '41'
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
    apiMocks.listConversations.mockResolvedValue({ items: [conversation()], next_cursor: null })
    apiMocks.listMessages.mockResolvedValue({
      items: [conversation().last_message],
      next_before_seq: 0,
      has_more: false
    })
    apiMocks.markConversationRead.mockResolvedValue({
      conversation_id: 41,
      last_read_seq: 1,
      unread_count: 0
    })
  })

  it('opens history, sends optimistically and applies an ack', async () => {
    const imStore = useIMStore()
    imStore.status = 'connected'
    const send = vi.spyOn(imStore, 'send').mockReturnValue(true)
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.text()).toContain('准备好了吗？')
    expect(apiMocks.markConversationRead).toHaveBeenCalledWith(41, 1)

    await wrapper.get('[data-testid="chat-composer"]').setValue('我准备好了')
    await wrapper.get('[data-testid="send-message-button"]').trigger('click')
    await flushPromises()

    expect(send).toHaveBeenCalledWith(
      'chat_message',
      { conversation_id: 41, message_type: 'text', content: '我准备好了' },
      expect.any(String)
    )
    expect(wrapper.text()).toContain('发送中')

    const chatStore = useChatStore()
    const pending = chatStore.currentMessages.find(({ delivery_status }) => delivery_status === 'pending')!
    chatStore.handleAck(pending.client_message_id, {
      duplicate: false,
      message: {
        ...pending,
        id: 2,
        seq: 2,
        delivery_status: undefined
      }
    })
    await flushPromises()
    expect(wrapper.text()).not.toContain('发送中')
    wrapper.unmount()
  })

  it('disables sending and shows the read-only boundary after friendship removal', async () => {
    apiMocks.listConversations.mockResolvedValue({
      items: [conversation(false)],
      next_cursor: null
    })
    const imStore = useIMStore()
    imStore.status = 'connected'
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.text()).toContain('历史只读')
    expect(wrapper.get('[data-testid="chat-composer"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-testid="send-message-button"]').attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })

  it('loads older history without duplicating the current window', async () => {
    apiMocks.listMessages
      .mockResolvedValueOnce({
        items: [{
          ...conversation().last_message,
          id: 2,
          seq: 2,
          client_message_id: '00000000-0000-4000-8000-000000000002',
          content: '较新的消息'
        }],
        next_before_seq: 2,
        has_more: true
      })
      .mockResolvedValueOnce({
        items: [{ ...conversation().last_message, content: '最早的消息' }],
        next_before_seq: 0,
        has_more: false
      })
    const imStore = useIMStore()
    imStore.status = 'connected'
    vi.spyOn(imStore, 'send').mockReturnValue(true)
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-testid="load-older-button"]').trigger('click')
    await flushPromises()

    expect(apiMocks.listMessages).toHaveBeenLastCalledWith(41, 2)
    expect(wrapper.text()).toContain('最早的消息')
    expect(wrapper.text()).toContain('较新的消息')
    wrapper.unmount()
  })

  it('disables the composer while disconnected and retries a failed message', async () => {
    const imStore = useIMStore()
    imStore.status = 'disconnected'
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.text()).toContain('实时连接中断')
    expect(wrapper.get('[data-testid="send-message-button"]').attributes('disabled')).toBeDefined()

    imStore.status = 'connected'
    const send = vi.spyOn(imStore, 'send').mockReturnValue(false)
    const chatStore = useChatStore()
    chatStore.sendText(41, '需要重试')
    await flushPromises()
    expect(wrapper.find('[data-testid="retry-message-button"]').exists()).toBe(true)

    send.mockReturnValue(true)
    await wrapper.get('[data-testid="retry-message-button"]').trigger('click')
    await flushPromises()
    expect(send).toHaveBeenLastCalledWith(
      'chat_message',
      { conversation_id: 41, message_type: 'text', content: '需要重试' },
      expect.any(String)
    )
    wrapper.unmount()
  })

  it('renders a group conversation and system messages without a fake peer', async () => {
    const groupConversation = {
      ...conversation(),
      type: 'group' as const,
      peer: null,
      group: {
        id: 9,
        name: '周五夜调查局',
        avatar_url: '',
        current_user_role: 'admin' as const,
        member_count: 3,
        version: 4
      },
      last_message: {
        ...conversation().last_message,
        sender: {
          id: 7,
          username: 'player07',
          nickname: '守秘人',
          avatar_url: ''
        },
        message_type: 'system' as const,
        content: 'member 8 joined the group',
        metadata: { event: 'member_joined', target_user_id: 8 }
      }
    }
    apiMocks.listConversations.mockResolvedValue({ items: [groupConversation], next_cursor: null })
    apiMocks.listMessages.mockResolvedValue({
      items: [groupConversation.last_message],
      next_before_seq: 0,
      has_more: false
    })
    const imStore = useIMStore()
    imStore.status = 'connected'
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.text()).toContain('周五夜调查局')
    expect(wrapper.text()).toContain('3 人 · 管理员')
    expect(wrapper.text()).toContain('守秘人 邀请成员 #8 加入群组')
    expect(wrapper.text()).toContain('群成员')
    wrapper.unmount()
  })
})
