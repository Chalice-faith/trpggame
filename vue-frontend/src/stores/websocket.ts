import { defineStore } from 'pinia'
import { ref } from 'vue'
import { useAuthStore } from './auth'
import { useGameStore, type GameDiceRoll } from './game'

interface ServerMessage {
  type: string
  room_id?: number
  seq?: number
  request_id?: string
  data?: any
}

export const useWebSocketStore = defineStore('websocket', () => {
  // ---- state ----
  const socket = ref<WebSocket | null>(null)
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
    if (socket.value?.readyState === WebSocket.OPEN) return

    const targetRoomId = legacyRoomId ?? roomOrLegacyUserId
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
    socket.value = new WebSocket(wsUrl)

    socket.value.onopen = () => {
      isConnected.value = true
      reconnectAttempts.value = 0
      lastError.value = ''
      console.log('[WS] Connected')
    }

    socket.value.onclose = () => {
      isConnected.value = false
      console.log('[WS] Disconnected')
      // 自动重连
      if (!intentionalDisconnect.value && reconnectAttempts.value < maxReconnectAttempts) {
        reconnectAttempts.value++
        reconnectTimer = setTimeout(() => {
          reconnectTimer = undefined
          connect(targetRoomId)
        }, reconnectDelay)
      }
    }

    socket.value.onerror = (err) => {
      console.error('[WS] Error:', err)
      lastError.value = '实时连接发生错误'
    }

    socket.value.onmessage = (event) => {
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
    dispatchSequenced(msg)
  }

  function dispatchSequenced(msg: ServerMessage) {
    const gameStore = useGameStore()
    if (typeof msg.seq === 'number') {
      if (msg.seq <= lastSeq.value) return
      lastSeq.value = msg.seq
    }
    switch (msg.type) {
      case 'narrative_chunk':
        gameStore.appendNarrativeChunk(msg.data?.content ?? '')
        break
      case 'narrative_complete':
        gameStore.completeNarrative(msg.data?.narrative ?? '', msg.data?.current_turn)
        break
      case 'dice_roll':
        gameStore.setDiceRoll(msg.data as GameDiceRoll)
        break
      case 'status_update':
        gameStore.applyStatusUpdate(msg.data ?? {})
        break
      case 'error':
        gameStore.failNarrative()
        lastError.value = msg.data?.message ?? '行动处理失败'
        console.error('[WS] Server error:', lastError.value)
        break
      default:
        break
    }
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
    sendGameAction,
    disconnect
  }
})
