// @vitest-environment jsdom

import { flushPromises, mount } from '@vue/test-utils'
import ElementPlus from 'element-plus'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import RoomLobbyView from '@/views/RoomLobbyView.vue'
import { useAuthStore } from '@/stores/auth'

const routeState = vi.hoisted(() => ({ params: { roomId: '41' } }))
const routerMocks = vi.hoisted(() => ({ push: vi.fn(), replace: vi.fn() }))
const socketMock = vi.hoisted(() => ({
  isConnected: true,
  lastError: '',
  connect: vi.fn(),
  disconnect: vi.fn()
}))
const roomApi = vi.hoisted(() => ({
  createRoom: vi.fn(),
  getRoom: vi.fn(),
  joinRoom: vi.fn(),
  leaveRoom: vi.fn(),
  listRooms: vi.fn(),
  removeRoomMember: vi.fn(),
  selectRoomCharacter: vi.fn(),
  setRoomReady: vi.fn(),
  startRoom: vi.fn(),
  transferRoomOwner: vi.fn()
}))

vi.mock('vue-router', () => ({
  useRoute: () => routeState,
  useRouter: () => routerMocks
}))
vi.mock('@/stores/websocket', () => ({ useWebSocketStore: () => socketMock }))
vi.mock('@/api/rooms', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/api/rooms')>()),
  ...roomApi
}))

function room(status: 'waiting' | 'playing' = 'waiting') {
  return {
    id: 41,
    name: '迷雾庄园',
    script_id: 9,
    owner_id: 7,
    status,
    max_players: 4,
    room_code: 'A7K9M2QX',
    version: status === 'waiting' ? 3 : 4,
    created_at: '2026-09-21T03:00:00Z',
    members: [
      {
        user: { id: 7, username: 'keeper', nickname: '守秘人', avatar_url: '' },
        character_id: 101,
        is_ready: true,
        player_order: 0,
        joined_at: '2026-09-21T03:00:00Z'
      },
      {
        user: { id: 8, username: 'player', nickname: '调查员', avatar_url: '' },
        character_id: 102,
        is_ready: true,
        player_order: 1,
        joined_at: '2026-09-21T03:01:00Z'
      }
    ],
    characters: [
      { id: 101, name: '记者', description: '善于调查' },
      { id: 102, name: '医生', description: '擅长急救' },
      { id: 103, name: '教授', description: '博学多闻' },
      { id: 104, name: '侦探', description: '观察敏锐' }
    ]
  }
}

describe('RoomLobbyView', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    setActivePinia(createPinia())
    const authStore = useAuthStore()
    authStore.user = {
      id: 7,
      username: 'keeper',
      email: 'keeper@example.test',
      nickname: '守秘人',
      avatar_url: ''
    }
    authStore.accessToken = 'access-token'
    vi.clearAllMocks()
    socketMock.isConnected = true
    socketMock.lastError = ''
    roomApi.getRoom.mockResolvedValue(room())
  })

  it('renders authoritative members, occupied roles and enabled owner start action', async () => {
    const wrapper = mount(RoomLobbyView, {
      attachTo: document.body,
      global: { plugins: [ElementPlus] }
    })
    await flushPromises()

    expect(socketMock.connect).toHaveBeenCalledWith(41)
    expect(wrapper.findAll('[data-testid="lobby-member-row"]')).toHaveLength(2)
    expect(wrapper.text()).toContain('记者')
    expect(wrapper.text()).toContain('医生')
    expect(wrapper.get('[data-testid="start-room-button"]').attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })

  it('keeps a started room on the M2.4 completion screen instead of exposing turn actions', async () => {
    roomApi.getRoom.mockResolvedValue(room('playing'))
    const wrapper = mount(RoomLobbyView, {
      attachTo: document.body,
      global: { plugins: [ElementPlus] }
    })
    await flushPromises()

    expect(wrapper.text()).toContain('多人回合将在 M2.5 开放')
    expect(wrapper.find('[data-testid="ready-button"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="start-room-button"]').exists()).toBe(false)
    wrapper.unmount()
  })
})
