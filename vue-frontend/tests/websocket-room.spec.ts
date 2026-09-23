// @vitest-environment jsdom

import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { RoomSnapshot } from '@/api/rooms'
import { useAuthStore } from '@/stores/auth'
import { useRoomsStore } from '@/stores/rooms'
import { useWebSocketStore } from '@/stores/websocket'
import { useMultiplayerStore } from '@/stores/multiplayer'
import { useGameStore } from '@/stores/game'

class MockWebSocket {
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSED = 3
  static instances: MockWebSocket[] = []

  readonly url: string
  readyState = MockWebSocket.CONNECTING
  onopen: (() => void) | null = null
  onclose: ((event: CloseEvent) => void) | null = null
  onerror: ((event: Event) => void) | null = null
  onmessage: ((event: MessageEvent) => void) | null = null
  sent: string[] = []

  constructor(url: string) {
    this.url = url
    MockWebSocket.instances.push(this)
  }

  open() {
    this.readyState = MockWebSocket.OPEN
    this.onopen?.()
  }

  send(payload: string) {
    this.sent.push(payload)
  }

  message(payload: unknown) {
    this.onmessage?.({ data: JSON.stringify(payload) } as MessageEvent)
  }

  close() {
    this.readyState = MockWebSocket.CLOSED
  }

  serverClose(code: number, reason: string) {
    this.readyState = MockWebSocket.CLOSED
    this.onclose?.({ code, reason } as CloseEvent)
  }
}

function room(version = 3): RoomSnapshot {
  return {
    id: 41,
    name: '迷雾庄园',
    script_id: 9,
    owner_id: 7,
    status: 'waiting',
    max_players: 4,
    room_code: 'A7K9M2QX',
    version,
    created_at: '2026-09-21T03:00:00Z',
    members: [{
      user: { id: 7, username: 'keeper', nickname: '守秘人', avatar_url: '' },
      character_id: 101,
      is_ready: false,
      player_order: 0,
      joined_at: '2026-09-21T03:00:00Z'
    }],
    characters: [{ id: 101, name: '记者', description: '善于调查' }]
  }
}

describe('game websocket room events', () => {
  beforeEach(() => {
    localStorage.clear()
    MockWebSocket.instances = []
    vi.stubGlobal('WebSocket', MockWebSocket)
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
  })

  it('applies a newer lobby snapshot even when its sequence is the current baseline', () => {
    const roomsStore = useRoomsStore()
    const socketStore = useWebSocketStore()
    roomsStore.currentRoom = room(3)

    socketStore.connect(41)
    const socket = MockWebSocket.instances[0]
    socket.open()
    socket.message({ type: 'subscribed', room_id: 41, seq: 8, data: { room_id: 41, seq: 8 } })
    socket.message({ type: 'room_snapshot', room_id: 41, seq: 8, data: { ...room(4), name: '实时大厅' } })

    expect(roomsStore.currentRoom).toMatchObject({ name: '实时大厅', version: 4 })
  })

  it('stops reconnecting and removes room access after close code 4003', () => {
    vi.useFakeTimers()
    const roomsStore = useRoomsStore()
    const socketStore = useWebSocketStore()
    roomsStore.currentRoom = room()
    roomsStore.rooms = [room()]

    socketStore.connect(41)
    const socket = MockWebSocket.instances[0]
    socket.open()
    socket.serverClose(4003, 'room_access_revoked')
    vi.runAllTimers()

    expect(socketStore.lastError).toBe('你已离开或被移出该房间')
    expect(roomsStore.currentRoom).toBeNull()
    expect(roomsStore.rooms).toEqual([])
    expect(MockWebSocket.instances).toHaveLength(1)
    vi.useRealTimers()
  })

  it('routes multiplayer chunks to a request-scoped draft without changing the solo timeline', () => {
    const socketStore = useWebSocketStore()
    const multiplayer = useMultiplayerStore()
    const solo = useGameStore()
    socketStore.connect(41)
    const socket = MockWebSocket.instances[0]
    socket.open()
    socket.message({ type: 'subscribed', room_id: 41, data: { seq: 0 } })
    socket.message({ type: 'game_runtime_snapshot', room_id: 41, seq: 1, data: {
      version: 2, room_id: 41, status: 'playing', generation: 'generation-a', current_turn: 0,
      round_number: 0, turn_order: [7, 8], current_actor_id: 7, deadline_at: '2099-01-01T00:00:00Z',
      players: [], summary_memory: '', recent_messages: [{ role: 'assistant', content: 'Opening' }]
    } })
    socket.message({ type: 'action_started', room_id: 41, seq: 2, request_id: 'request-1', data: {
      generation: 'generation-a', current_turn: 0, player_id: 7
    } })
    socket.message({ type: 'narrative_chunk', room_id: 41, seq: 3, request_id: 'request-1', data: {
      generation: 'generation-a', current_turn: 0, content: 'Draft', is_final: false
    } })
    expect(multiplayer.draft).toBe('Draft')
    expect(multiplayer.history).toHaveLength(1)
    expect(solo.narrativeHistory).toHaveLength(0)
  })

  it('replaces a socket when navigating to another room and ignores the old close', () => {
    const socketStore = useWebSocketStore()
    socketStore.connect(41)
    const first = MockWebSocket.instances[0]
    first.open()
    socketStore.connect(42)
    const second = MockWebSocket.instances[1]
    second.open()
    first.serverClose(1000, 'old room')
    expect(socketStore.roomId).toBe(42)
    expect(socketStore.isConnected).toBe(true)
    expect(MockWebSocket.instances).toHaveLength(2)
  })
})
