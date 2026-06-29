import { defineStore } from 'pinia'
import { ref } from 'vue'

export const useWebSocketStore = defineStore('websocket', () => {
  // ---- state ----
  const socket = ref<WebSocket | null>(null)
  const isConnected = ref(false)
  const reconnectAttempts = ref(0)

  const maxReconnectAttempts = 5
  const reconnectDelay = 3000

  // ---- actions ----
  function connect(userId: number, roomId: number) {
    if (socket.value?.readyState === WebSocket.OPEN) return

    const wsUrl = `${import.meta.env.VITE_WS_URL}?user_id=${userId}&room_id=${roomId}`
    socket.value = new WebSocket(wsUrl)

    socket.value.onopen = () => {
      isConnected.value = true
      reconnectAttempts.value = 0
      console.log('[WS] Connected')
    }

    socket.value.onclose = () => {
      isConnected.value = false
      console.log('[WS] Disconnected')
      // 自动重连
      if (reconnectAttempts.value < maxReconnectAttempts) {
        reconnectAttempts.value++
        setTimeout(() => connect(userId, roomId), reconnectDelay)
      }
    }

    socket.value.onerror = (err) => {
      console.error('[WS] Error:', err)
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

  function handleMessage(_msg: any) {
    // TODO: Phase 1 M1.5 实现消息分发
    // 根据 msg.type 分发给对应处理逻辑
  }

  function send(type: string, data?: any) {
    if (socket.value?.readyState === WebSocket.OPEN) {
      socket.value.send(JSON.stringify({ type, data, timestamp: Date.now() }))
    }
  }

  function disconnect() {
    if (socket.value) {
      socket.value.close()
      socket.value = null
    }
    isConnected.value = false
  }

  return { socket, isConnected, reconnectAttempts, connect, send, disconnect }
})
