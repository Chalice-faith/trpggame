// @vitest-environment jsdom

import { flushPromises, mount } from '@vue/test-utils'
import ElementPlus from 'element-plus'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import GameMultiplayerView from '@/views/GameMultiplayerView.vue'
import { useAuthStore } from '@/stores/auth'

const route = vi.hoisted(() => ({ params: { id: '41' } }))
const router = vi.hoisted(() => ({ push: vi.fn(), replace: vi.fn() }))
const socket = vi.hoisted(() => ({
  isConnected: true, lastError: '', connect: vi.fn(), disconnect: vi.fn(),
  setSequenceBaseline: vi.fn(), send: vi.fn()
}))
const roomApi = vi.hoisted(() => ({ getRoom: vi.fn() }))
const gameApi = vi.hoisted(() => ({
  getMultiplayerGameState: vi.fn(), listGameSaves: vi.fn(), skipMultiplayerTurn: vi.fn(),
  createGameSave: vi.fn(), loadGame: vi.fn(), pauseGame: vi.fn(), resumeGame: vi.fn(), endGame: vi.fn()
}))

vi.mock('vue-router', () => ({ useRoute: () => route, useRouter: () => router }))
vi.mock('@/stores/websocket', () => ({ useWebSocketStore: () => socket }))
vi.mock('@/api/rooms', async (original) => ({ ...(await original<typeof import('@/api/rooms')>()), ...roomApi }))
vi.mock('@/api/game', async (original) => ({ ...(await original<typeof import('@/api/game')>()), ...gameApi }))

function room(ownerId = 7) {
  return {
    id: 41, name: '迷雾庄园', script_id: 9, owner_id: ownerId, status: 'playing',
    max_players: 4, room_code: 'A7K9M2QX', version: 4, created_at: '2026-09-23T10:00:00Z',
    members: [7, 8].map((id, index) => ({
      user: { id, username: `user${id}`, nickname: `玩家${id}`, avatar_url: '' },
      character_id: 101 + index, is_ready: true, player_order: index, joined_at: '2026-09-23T10:00:00Z'
    })),
    characters: [{ id: 101, name: '记者', description: '' }, { id: 102, name: '医生', description: '' }]
  }
}

function state(actor = 7) {
  return {
    seq: 3, version: 2, room_id: 41, status: 'playing', generation: 'generation-a',
    current_turn: actor === 7 ? 0 : 1, round_number: 0, turn_order: [7, 8], current_actor_id: actor,
    deadline_at: '2099-09-23T10:01:00Z', summary_memory: '庄园',
    recent_messages: [{ role: 'assistant', content: '门打开了。' }],
    players: [7, 8].map((id) => ({ user_id: id, character_id: id + 94, player_state: { hp: '10' }, items: [], buffs: [] }))
  }
}

describe('GameMultiplayerView', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    setActivePinia(createPinia())
    const auth = useAuthStore()
    auth.user = { id: 7, username: 'user7', nickname: '玩家7', email: 'u7@example.test', avatar_url: '' }
    auth.accessToken = 'token'
    vi.clearAllMocks()
    roomApi.getRoom.mockResolvedValue(room())
    gameApi.getMultiplayerGameState.mockResolvedValue(state())
    gameApi.listGameSaves.mockResolvedValue({ items: [], total: 0 })
  })

  it('loads the authoritative state and shows the owner controls on their turn', async () => {
    const wrapper = mount(GameMultiplayerView, { attachTo: document.body, global: { plugins: [ElementPlus] } })
    await flushPromises()
    expect(socket.setSequenceBaseline).toHaveBeenCalledWith(41, 3)
    expect(socket.connect).toHaveBeenCalledWith(41)
    expect(wrapper.text()).toContain('行动队列')
    expect(wrapper.text()).toContain('玩家8')
    expect(wrapper.text()).toContain('轮到你行动')
    expect(wrapper.text()).toContain('手动存档')
    wrapper.unmount()
  })

  it('does not offer another player an action or host controls', async () => {
    roomApi.getRoom.mockResolvedValue(room(8))
    gameApi.getMultiplayerGameState.mockResolvedValue(state(8))
    const wrapper = mount(GameMultiplayerView, { attachTo: document.body, global: { plugins: [ElementPlus] } })
    await flushPromises()
    expect(wrapper.text()).toContain('等待 玩家8 行动')
    expect(wrapper.text()).not.toContain('手动存档')
    expect(wrapper.find('textarea').attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })

  it('shows zero-based saved round numbers as human-readable rounds', async () => {
    gameApi.listGameSaves.mockResolvedValue({
      items: [
        { id: 92, save_name: '自动存档-5轮', round_number: 5, is_auto: true, created_at: '2026-09-23T10:05:00Z' },
        { id: 91, save_name: '第二轮存档', round_number: 1, is_auto: false, created_at: '2026-09-23T10:00:00Z' }
      ],
      total: 2
    })
    const wrapper = mount(GameMultiplayerView, { attachTo: document.body, global: { plugins: [ElementPlus] } })
    await flushPromises()
    await wrapper.findAll('button').find((button) => button.text().includes('查看存档'))?.trigger('click')
    await flushPromises()
    expect(document.body.textContent).toContain('第 2 轮 · 手动')
    expect(document.body.textContent).toContain('第 5 轮 · 自动')
    wrapper.unmount()
  })

  it('reloads an ended game as read-only recent history', async () => {
    roomApi.getRoom.mockResolvedValue({ ...room(), status: 'ended' })
    gameApi.getMultiplayerGameState.mockResolvedValue({
      ...state(), status: 'ended', deadline_at: null
    })
    const wrapper = mount(GameMultiplayerView, { attachTo: document.body, global: { plugins: [ElementPlus] } })
    await flushPromises()
    expect(gameApi.getMultiplayerGameState).toHaveBeenCalledWith(41)
    expect(wrapper.text()).toContain('游戏已结束')
    expect(wrapper.text()).toContain('门打开了。')
    expect(wrapper.text()).not.toContain('手动存档')
    expect(wrapper.find('textarea').attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })
})
