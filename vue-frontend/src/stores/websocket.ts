import { defineStore } from 'pinia'
import { ref, shallowRef } from 'vue'
import { useAuthStore } from './auth'
import { useGameStore, type GameDiceRoll } from './game'
import { useRoomsStore } from './rooms'
import { useMultiplayerStore } from './multiplayer'
import type { RoomSnapshot } from '@/api/rooms'

interface ServerMessage {
  type: string
  room_id?: number
  seq?: number
  request_id?: string
  data?: any
}

export const useWebSocketStore = defineStore('websocket', () => {
  // ---- state ----
  const socket = shallowRef<WebSocket | null>(null)
  const isConnected = ref(false)
  const reconnectAttempts = ref(0)
  const lastSeq = ref(0)
  const roomId = ref<number | null>(null)
  const intentionalDisconnect = ref(false)
  const lastError = ref('')
  let reconnectTimer: ReturnType<typeof setTimeout> | undefined

  const maxReconnectAttempts = 5
  const reconnectDelay = 3000

  // ---- actions ----
  function connect(roomOrLegacyUserId: number, legacyRoomId?: number) {
    const targetRoomId = legacyRoomId ?? roomOrLegacyUserId
    if (socket.value && roomId.value === targetRoomId &&
      (socket.value.readyState === WebSocket.OPEN || socket.value.readyState === WebSocket.CONNECTING)) return
    if (socket.value && roomId.value !== targetRoomId) disconnect()
    const authStore = useAuthStore()
    if (!authStore.accessToken || !targetRoomId) return
    if (roomId.value !== null && roomId.value !== targetRoomId) lastSeq.value = 0
    roomId.value = targetRoomId
    intentionalDisconnect.value = false
    lastError.value = ''
    if (reconnectTimer) {
      clearTimeout(reconnectTimer)
      reconnectTimer = undefined
    }
    const wsUrl = `${import.meta.env.VITE_WS_URL}?token=${encodeURIComponent(authStore.accessToken)}&room_id=${targetRoomId}`
    const connection = new WebSocket(wsUrl)
    socket.value = connection

    connection.onopen = () => {
      if (socket.value !== connection) return
      isConnected.value = true
      reconnectAttempts.value = 0
      lastError.value = ''
      console.log('[WS] Connected')
    }

    connection.onclose = (event) => {
      if (socket.value !== connection) return
      isConnected.value = false
      console.log('[WS] Disconnected')
      if (event.code === 4003) {
        intentionalDisconnect.value = true
        lastError.value = '你已离开或被移出该房间'
        useRoomsStore().handleAccessRevoked(targetRoomId)
        return
      }
      if (event.code === 4001) {
        intentionalDisconnect.value = true
        lastError.value = '此连接已被同账号的新连接接管'
        return
      }
      // 自动重连
      if (!intentionalDisconnect.value && reconnectAttempts.value < maxReconnectAttempts) {
        reconnectAttempts.value++
        reconnectTimer = setTimeout(() => {
          reconnectTimer = undefined
          connect(targetRoomId)
        }, reconnectDelay)
      }
    }

    connection.onerror = (err) => {
      if (socket.value !== connection) return
      console.error('[WS] Error:', err)
      lastError.value = '实时连接发生错误'
    }

    connection.onmessage = (event) => {
      if (socket.value !== connection) return
      try {
        const msg = JSON.parse(event.data)
        handleMessage(msg)
      } catch {
        console.warn('[WS] Failed to parse message')
      }
    }
  }

  function handleMessage(msg: ServerMessage) {
    if (msg.type === 'subscribed') {
      const serverSeq = msg.data?.seq ?? 0
      if (serverSeq > lastSeq.value) {
        send('sync', { since_seq: lastSeq.value })
      } else {
        lastSeq.value = serverSeq
      }
      return
    }
    if (msg.type === 'sync_batch') {
      const batch = msg.data?.messages as ServerMessage[] | undefined
      for (const item of batch ?? []) dispatchSequenced(item)
      if (typeof msg.data?.next_seq === 'number') lastSeq.value = msg.data.next_seq
      return
    }
    if (msg.type === 'snapshot_required') {
      const targetRoomId = roomId.value
      if (targetRoomId && useMultiplayerStore().snapshot?.room_id === targetRoomId) {
        void useMultiplayerStore().refresh(targetRoomId).catch(() => {})
      } else if (targetRoomId && useRoomsStore().currentRoom?.id === targetRoomId) {
        void useRoomsStore().openRoom(targetRoomId).catch(() => {})
      }
      if (typeof msg.data?.next_seq === 'number') lastSeq.value = msg.data.next_seq
      return
    }
    dispatchSequenced(msg)
  }

  function dispatchSequenced(msg: ServerMessage) {
    const gameStore = useGameStore()
    const roomsStore = useRoomsStore()
    const multiplayerStore = useMultiplayerStore()
    if (typeof msg.seq === 'number') {
      if (msg.seq <= lastSeq.value) {
        if (msg.type === 'room_snapshot') {
          roomsStore.applyRealtimeSnapshot(msg.data as RoomSnapshot)
        }
        return
      }
      lastSeq.value = msg.seq
    }
    switch (msg.type) {
      case 'narrative_chunk':
        if (!msg.data?.generation) gameStore.appendNarrativeChunk(msg.data?.content ?? '')
        break
      case 'narrative_complete':
        if (!msg.data?.generation) gameStore.completeNarrative(msg.data?.narrative ?? '', msg.data?.current_turn)
        break
      case 'dice_roll':
        if (!msg.data?.generation) gameStore.setDiceRoll(msg.data as GameDiceRoll)
        break
      case 'status_update':
        if (!msg.data?.generation) gameStore.applyStatusUpdate(msg.data ?? {})
        break
      case 'room_snapshot':
      case 'room_member_joined':
      case 'room_member_left':
      case 'room_ready_changed':
      case 'room_character_selected':
      case 'game_started':
        roomsStore.applyRealtimeSnapshot(msg.data as RoomSnapshot)
        break
      case 'game_runtime_snapshot':
        multiplayerStore.applySnapshot({ ...msg.data, seq: msg.seq ?? multiplayerStore.lastSeq }, msg.seq ?? multiplayerStore.lastSeq)
        break
      case 'action_started':
      case 'action_cancelled':
      case 'turn_start':
      case 'turn_skip':
      case 'game_status_changed':
      case 'game_ended':
        multiplayerStore.handleEvent(msg.type, msg.data, msg.request_id, msg.seq)
        break
      case 'error':
        gameStore.failNarrative()
        if (msg.room_id && multiplayerStore.snapshot?.room_id === msg.room_id) {
          multiplayerStore.clearDraft()
          void multiplayerStore.refresh(msg.room_id).catch(() => {})
        }
        lastError.value = msg.data?.message ?? '行动处理失败'
        console.error('[WS] Server error:', lastError.value)
        break
      default:
        break
    }
    if (multiplayerStore.snapshot && ['narrative_chunk', 'narrative_complete', 'dice_roll', 'status_update'].includes(msg.type)) {
      multiplayerStore.handleEvent(msg.type, msg.data, msg.request_id, msg.seq)
    }
  }

  function setSequenceBaseline(targetRoomId: number, seq: number) {
    if (roomId.value !== null && roomId.value !== targetRoomId) return
    roomId.value = targetRoomId
    lastSeq.value = Math.max(lastSeq.value, seq)
  }

  function send(type: string, data?: any) {
    if (socket.value?.readyState === WebSocket.OPEN) {
      socket.value.send(JSON.stringify({ type, data }))
    }
  }

  function sendGameAction(actionText: string) {
    const text = actionText.trim()
    const gameStore = useGameStore()
    if (!text || !roomId.value || !isConnected.value || gameStore.isStreaming ||
      gameStore.currentRoom?.status !== 'playing') return null
    const requestId = crypto.randomUUID?.() || `${Date.now()}-${Math.random().toString(16).slice(2)}`
    gameStore.appendNarrative('player', text)
    gameStore.beginNarrativeStream()
    send('game_action', {
      request_id: requestId,
      expected_turn: gameStore.currentRoom?.current_turn ?? 0,
      action_text: text
    })
    return requestId
  }

  function disconnect() {
    intentionalDisconnect.value = true
    if (reconnectTimer) {
      clearTimeout(reconnectTimer)
      reconnectTimer = undefined
    }
    if (socket.value) {
      socket.value.close()
      socket.value = null
    }
    isConnected.value = false
    roomId.value = null
    lastSeq.value = 0
    reconnectAttempts.value = 0
  }

  return {
    socket,
    isConnected,
    reconnectAttempts,
    lastSeq,
    roomId,
    lastError,
    connect,
    send,
    setSequenceBaseline,
    sendGameAction,
    disconnect
  }
})
