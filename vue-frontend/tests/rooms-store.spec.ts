// @vitest-environment jsdom

import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { RoomSnapshot } from '@/api/rooms'
import { useAuthStore } from '@/stores/auth'
import { useRoomsStore } from '@/stores/rooms'

const apiMocks = vi.hoisted(() => ({
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

vi.mock('@/api/rooms', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/api/rooms')>()),
  ...apiMocks
}))

function snapshot(overrides: Partial<RoomSnapshot> = {}): RoomSnapshot {
  return {
    id: 41,
    name: '迷雾庄园',
    script_id: 9,
    owner_id: 7,
    status: 'waiting',
    max_players: 4,
    room_code: 'A7K9M2QX',
    version: 3,
    created_at: '2026-09-21T03:00:00Z',
    members: [
      {
        user: { id: 7, username: 'keeper', nickname: '守秘人', avatar_url: '' },
        character_id: 101,
        is_ready: false,
        player_order: 0,
        joined_at: '2026-09-21T03:00:00Z'
      },
      {
        user: { id: 8, username: 'player', nickname: '调查员', avatar_url: '' },
        character_id: 102,
        is_ready: false,
        player_order: 1,
        joined_at: '2026-09-21T03:01:00Z'
      }
    ],
    characters: [
      { id: 101, name: '记者', description: '善于调查' },
      { id: 102, name: '医生', description: '擅长急救' },
      { id: 103, name: '教授', description: '博学多闻' },
      { id: 104, name: '侦探', description: '观察敏锐' }
    ],
    ...overrides
  }
}

describe('rooms store', () => {
  beforeEach(() => {
    localStorage.clear()
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
    apiMocks.listRooms.mockResolvedValue([snapshot()])
    apiMocks.getRoom.mockResolvedValue(snapshot())
  })

  it('uses the current version for mutations and applies the returned snapshot', async () => {
    const store = useRoomsStore()
    apiMocks.setRoomReady.mockResolvedValue(snapshot({
      version: 4,
      members: snapshot().members.map((member) => member.user.id === 7
        ? { ...member, is_ready: true }
        : member)
    }))

    await store.openRoom(41)
    await store.setReady(true)

    expect(apiMocks.setRoomReady).toHaveBeenCalledWith(41, true, 3)
    expect(store.currentRoom?.version).toBe(4)
    expect(store.currentMember?.is_ready).toBe(true)
  })

  it('ignores stale realtime snapshots and applies a newer version', async () => {
    const store = useRoomsStore()
    await store.openRoom(41)

    store.applyRealtimeSnapshot(snapshot({ name: '旧名称', version: 2 }))
    expect(store.currentRoom?.name).toBe('迷雾庄园')

    store.applyRealtimeSnapshot(snapshot({ name: '新名称', version: 4 }))
    expect(store.currentRoom).toMatchObject({ name: '新名称', version: 4 })
  })

  it('does not let a slower REST response overwrite a newer realtime version', async () => {
    const store = useRoomsStore()
    await store.openRoom(41)
    apiMocks.setRoomReady.mockImplementation(async () => {
      store.applyRealtimeSnapshot(snapshot({ name: '实时版本', version: 5 }))
      return snapshot({ name: '较慢响应', version: 4 })
    })

    await store.setReady(true)

    expect(store.currentRoom).toMatchObject({ name: '实时版本', version: 5 })
  })

  it('refreshes the authoritative snapshot after a version conflict', async () => {
    const store = useRoomsStore()
    await store.openRoom(41)
    apiMocks.selectRoomCharacter.mockRejectedValue({ response: { data: { code: 1908 } } })
    apiMocks.getRoom.mockResolvedValue(snapshot({ version: 4 }))

    await expect(store.selectCharacter(103)).rejects.toBeTruthy()

    expect(apiMocks.getRoom).toHaveBeenLastCalledWith(41)
    expect(store.currentRoom?.version).toBe(4)
  })

  it('removes local access when a committed realtime snapshot no longer includes the user', async () => {
    const store = useRoomsStore()
    await store.loadRooms()
    await store.openRoom(41)

    store.applyRealtimeSnapshot(snapshot({
      version: 4,
      members: snapshot().members.filter(({ user }) => user.id !== 7)
    }))

    expect(store.currentRoom).toBeNull()
    expect(store.rooms).toEqual([])
    expect(store.revokedRoomId).toBe(41)
  })

  it('does not revoke access from a replayed snapshot older than the joined room', async () => {
    const store = useRoomsStore()
    await store.loadRooms()
    await store.openRoom(41)

    store.applyRealtimeSnapshot(snapshot({
      version: 2,
      members: snapshot().members.filter(({ user }) => user.id !== 7)
    }))

    expect(store.currentRoom?.version).toBe(3)
    expect(store.currentMember?.user.id).toBe(7)
    expect(store.revokedRoomId).toBeNull()
    expect(store.rooms).toHaveLength(1)
  })

  it('reports start readiness only when at least two members selected roles and are ready', async () => {
    const store = useRoomsStore()
    apiMocks.getRoom.mockResolvedValue(snapshot({
      members: snapshot().members.map((member) => ({ ...member, is_ready: true }))
    }))

    await store.openRoom(41)

    expect(store.canStart).toBe(true)
  })
})
