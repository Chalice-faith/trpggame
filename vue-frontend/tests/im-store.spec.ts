// @vitest-environment jsdom

import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { nextTick } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import App from '@/App.vue'
import { useAuthStore, type User } from '@/stores/auth'
import { useFriendsStore } from '@/stores/friends'
import { IM_RECONNECT_DELAYS, useIMStore } from '@/stores/im'

class MockWebSocket {
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSING = 2
  static readonly CLOSED = 3
  static instances: MockWebSocket[] = []

  readonly url: string
  readyState = MockWebSocket.CONNECTING
  onopen: (() => void) | null = null
  onmessage: ((event: MessageEvent) => void) | null = null
  onerror: (() => void) | null = null
  onclose: ((event: CloseEvent) => void) | null = null

  constructor(url: string) {
    this.url = url
    MockWebSocket.instances.push(this)
  }

  open() {
    this.readyState = MockWebSocket.OPEN
    this.onopen?.()
  }

  serverClose(code = 1006, reason = '') {
    this.readyState = MockWebSocket.CLOSED
    this.onclose?.({ code, reason } as CloseEvent)
  }

  message(data: unknown) {
    this.onmessage?.({ data: JSON.stringify(data) } as MessageEvent)
  }

  close() {
    this.readyState = MockWebSocket.CLOSED
  }
}

const testUser: User = {
  id: 7,
  username: 'player07',
  email: 'player07@example.test',
  nickname: '守秘人',
  avatar_url: ''
}

function logIn() {
  const authStore = useAuthStore()
  authStore.accessToken = 'opaque-access-token'
  authStore.refreshToken = 'refresh-token'
  authStore.user = testUser
  return authStore
}

describe('global IM store', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    localStorage.clear()
    MockWebSocket.instances = []
    vi.stubGlobal('WebSocket', MockWebSocket)
    setActivePinia(createPinia())
  })

  afterEach(() => {
    vi.runOnlyPendingTimers()
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it('connects automatically after the authenticated profile is available', async () => {
    const authStore = useAuthStore()
    authStore.accessToken = 'opaque-access-token'
    authStore.refreshToken = 'refresh-token'
    const wrapper = mount(App, {
      global: { stubs: { RouterView: true } }
    })
    await flushPromises()

    expect(MockWebSocket.instances).toHaveLength(0)
    authStore.user = testUser
    await nextTick()
    expect(MockWebSocket.instances).toHaveLength(1)
    expect(MockWebSocket.instances[0].url).toContain('/ws/im?token=')
    wrapper.unmount()
  })

  it('uses 1, 2 and 4 second reconnect backoff after its one handshake refresh', async () => {
    const authStore = logIn()
    vi.spyOn(authStore, 'refreshAccessToken').mockResolvedValue(true)
    const imStore = useIMStore()
    await imStore.connect()

    MockWebSocket.instances[0].serverClose()
    await flushPromises()
    expect(authStore.refreshAccessToken).toHaveBeenCalledTimes(1)
    expect(MockWebSocket.instances).toHaveLength(2)

    MockWebSocket.instances[1].serverClose()
    await vi.advanceTimersByTimeAsync(IM_RECONNECT_DELAYS[0] - 1)
    expect(MockWebSocket.instances).toHaveLength(2)
    await vi.advanceTimersByTimeAsync(1)
    expect(MockWebSocket.instances).toHaveLength(3)

    MockWebSocket.instances[2].serverClose()
    await vi.advanceTimersByTimeAsync(IM_RECONNECT_DELAYS[1])
    expect(MockWebSocket.instances).toHaveLength(4)

    MockWebSocket.instances[3].serverClose()
    await vi.advanceTimersByTimeAsync(IM_RECONNECT_DELAYS[2])
    expect(MockWebSocket.instances).toHaveLength(5)
  })

  it('stops reconnecting and exposes a warning after close code 4001', async () => {
    logIn()
    const imStore = useIMStore()
    await imStore.connect()
    MockWebSocket.instances[0].open()
    MockWebSocket.instances[0].serverClose(4001, 'connection_replaced')

    await vi.advanceTimersByTimeAsync(120_000)
    expect(MockWebSocket.instances).toHaveLength(1)
    expect(imStore.replacementNotice).toContain('其他页面')
    expect(imStore.status).toBe('disconnected')
  })

  it('refreshes a nearly expired access token before reconnecting', async () => {
    const authStore = logIn()
    const imStore = useIMStore()
    await imStore.connect()
    MockWebSocket.instances[0].open()

    const payload = btoa(
      JSON.stringify({ exp: Math.floor(Date.now() / 1000) + 10 })
    )
      .replace(/=/g, '')
      .replace(/\+/g, '-')
      .replace(/\//g, '_')
    authStore.accessToken = `header.${payload}.signature`
    const refreshSpy = vi
      .spyOn(authStore, 'refreshAccessToken')
      .mockImplementation(async () => {
        authStore.accessToken = 'fresh-access-token'
        return true
      })

    MockWebSocket.instances[0].serverClose()
    await vi.advanceTimersByTimeAsync(IM_RECONNECT_DELAYS[0])

    expect(refreshSpy).toHaveBeenCalledTimes(1)
    expect(MockWebSocket.instances[1].url).toContain('fresh-access-token')
  })

  it('logout closes the socket and clears a pending reconnect timer', async () => {
    const authStore = logIn()
    const wrapper = mount(App, {
      global: { stubs: { RouterView: true } }
    })
    await flushPromises()
    const socket = MockWebSocket.instances[0]
    socket.open()
    socket.serverClose()
    await nextTick()

    authStore.logout()
    await nextTick()
    await vi.advanceTimersByTimeAsync(120_000)

    expect(MockWebSocket.instances).toHaveLength(1)
    expect(useIMStore().reconnectAttempts).toBe(0)
    wrapper.unmount()
  })

  it('merges presence locally and refreshes lists for friendship changes', async () => {
    logIn()
    const friendsStore = useFriendsStore()
    friendsStore.friends = [
      {
        id: 31,
        peer: { id: 8, username: 'player08', nickname: '调查员', avatar_url: '' },
        presence: 'offline',
        updated_at: '2026-09-10T10:00:00Z'
      }
    ]
    const refreshSpy = vi
      .spyOn(friendsStore, 'handleFriendshipUpdated')
      .mockResolvedValue()
    const imStore = useIMStore()

    imStore.handleMessage(
      JSON.stringify({
        type: 'presence',
        timestamp: Date.now(),
        data: { user_id: 8, status: 'online' }
      })
    )
    imStore.handleMessage(
      JSON.stringify({
        type: 'friendship_updated',
        timestamp: Date.now(),
        data: { friendship_id: 31, status: 'accepted' }
      })
    )
    await flushPromises()

    expect(friendsStore.friends[0].presence).toBe('online')
    expect(refreshSpy).toHaveBeenCalledTimes(1)
  })
})
