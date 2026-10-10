// @vitest-environment jsdom

import { flushPromises, mount } from '@vue/test-utils'
import ElementPlus, { ElMessageBox } from 'element-plus'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import FriendsView from '@/views/FriendsView.vue'
import { useFriendsStore } from '@/stores/friends'

const apiMocks = vi.hoisted(() => ({
  searchUsers: vi.fn(),
  sendFriendRequest: vi.fn(),
  listFriendRequests: vi.fn(),
  acceptFriendRequest: vi.fn(),
  rejectFriendRequest: vi.fn(),
  listFriends: vi.fn(),
  deleteFriend: vi.fn()
}))
const chatApiMocks = vi.hoisted(() => ({
  createDirectConversation: vi.fn()
}))
const routerPush = vi.hoisted(() => vi.fn())

vi.mock('vue-router', () => ({
  useRouter: () => ({ push: routerPush })
}))

vi.mock('@/api/friends', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/api/friends')>()),
  ...apiMocks
}))

vi.mock('@/api/chat', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/api/chat')>()),
  ...chatApiMocks
}))

const peer = {
  id: 8,
  username: 'player08',
  nickname: '调查员',
  avatar_url: ''
}

function pendingRequest(id = 31) {
  return {
    id,
    status: 'pending' as const,
    direction: 'incoming' as const,
    requested_by: 8,
    peer,
    created_at: '2026-09-10T10:00:00Z',
    updated_at: '2026-09-10T10:00:00Z'
  }
}

function mountView() {
  return mount(FriendsView, {
    attachTo: document.body,
    global: { plugins: [ElementPlus] }
  })
}

describe('FriendsView', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    setActivePinia(createPinia())
    apiMocks.searchUsers.mockResolvedValue([
      { user: peer, friendship: null }
    ])
    apiMocks.sendFriendRequest.mockResolvedValue({
      ...pendingRequest(),
      direction: 'outgoing'
    })
    apiMocks.listFriendRequests.mockImplementation((direction: string) =>
      Promise.resolve({
        items: direction === 'incoming' ? [pendingRequest()] : [],
        next_cursor: null
      })
    )
    apiMocks.listFriends.mockResolvedValue({
      items: [
        {
          id: 41,
          peer,
          presence: 'offline',
          updated_at: '2026-09-10T10:00:00Z'
        }
      ],
      next_cursor: null
    })
    apiMocks.acceptFriendRequest.mockResolvedValue({
      ...pendingRequest(),
      status: 'accepted'
    })
    apiMocks.rejectFriendRequest.mockResolvedValue({
      ...pendingRequest(),
      status: 'rejected'
    })
    apiMocks.deleteFriend.mockResolvedValue(undefined)
    chatApiMocks.createDirectConversation.mockResolvedValue({
      id: 41,
      type: 'direct',
      peer,
      can_send: true,
      last_seq: 0,
      last_read_seq: 0,
      unread_count: 0,
      last_message: null,
      created_at: '2026-09-15T01:00:00Z',
      updated_at: '2026-09-15T01:00:00Z'
    })
  })

  it('searches users and sends a friend request', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper
      .get('[data-testid="friend-search-input"]')
      .setValue('调查员')
    await wrapper.get('[data-testid="friend-search-button"]').trigger('click')
    await flushPromises()

    expect(apiMocks.searchUsers).toHaveBeenCalledWith('调查员')
    expect(wrapper.text()).toContain('@player08')

    await wrapper.get('[data-testid="send-request-button"]').trigger('click')
    await flushPromises()
    expect(apiMocks.sendFriendRequest).toHaveBeenCalledWith(8)
    expect(wrapper.text()).toContain('申请已发送')
    wrapper.unmount()
  })

  it('accepts and rejects incoming requests', async () => {
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-testid="accept-request-button"]').trigger('click')
    await flushPromises()
    expect(apiMocks.acceptFriendRequest).toHaveBeenCalledWith(31)

    const store = useFriendsStore()
    store.incomingRequests = [pendingRequest(32)]
    await flushPromises()
    await wrapper.get('[data-testid="reject-request-button"]').trigger('click')
    await flushPromises()
    expect(apiMocks.rejectFriendRequest).toHaveBeenCalledWith(32)
    expect(store.incomingRequests).toHaveLength(0)
    wrapper.unmount()
  })

  it('deletes a friend and renders a realtime presence update', async () => {
    vi.spyOn(ElMessageBox, 'confirm').mockResolvedValue('confirm')
    const wrapper = mountView()
    await flushPromises()
    const store = useFriendsStore()

    store.applyPresence(8, 'online')
    await flushPromises()
    expect(wrapper.text()).toContain('在线')

    await wrapper.get('[data-testid="delete-friend-button"]').trigger('click')
    await flushPromises()
    expect(apiMocks.deleteFriend).toHaveBeenCalledWith(8)
    expect(wrapper.find('[data-testid="friend-row"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('creates or reuses a direct conversation from the friend row', async () => {
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-testid="chat-friend-button"]').trigger('click')
    await flushPromises()

    expect(chatApiMocks.createDirectConversation).toHaveBeenCalledWith(8)
    expect(routerPush).toHaveBeenCalledWith('/chat/41')
    wrapper.unmount()
  })
})
