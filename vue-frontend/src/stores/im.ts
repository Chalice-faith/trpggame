import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import type { PresenceStatus, PublicUser } from '@/api/friends'
import { useAuthStore } from '@/stores/auth'
import { useFriendsStore } from '@/stores/friends'

export const IM_RECONNECT_DELAYS = [1000, 2000, 4000, 8000, 16000, 30000]
const TOKEN_REFRESH_WINDOW_MS = 30_000

type IMConnectionStatus = 'disconnected' | 'connecting' | 'connected'

interface IMMessage<T = unknown> {
  type: string
  timestamp: number
  data: T
}

interface PresenceEventData {
  user_id: number
  status: PresenceStatus
}

export interface FriendshipUpdatedData {
  friendship_id: number
  status: 'pending' | 'accepted' | 'rejected' | 'removed'
  requested_by: number
  peer: PublicUser
  updated_at: string
}

export const useIMStore = defineStore('im', () => {
  const status = ref<IMConnectionStatus>('disconnected')
  const reconnectAttempts = ref(0)
  const replacementNotice = ref('')
  const lastError = ref('')
  const isConnected = computed(() => status.value === 'connected')

  let socket: WebSocket | null = null
  let reconnectTimer: ReturnType<typeof setTimeout> | undefined
  let intentionalDisconnect = true
  let reconnectBlocked = false
  let handshakeRefreshUsed = false

  async function connect() {
    if (reconnectBlocked) return
    intentionalDisconnect = false
    await openSocket()
  }

  async function openSocket() {
    const authStore = useAuthStore()
    if (
      intentionalDisconnect ||
      reconnectBlocked ||
      !authStore.isLoggedIn ||
      socket?.readyState === WebSocket.CONNECTING ||
      socket?.readyState === WebSocket.OPEN
    ) {
      return
    }

    clearReconnectTimer()
    if (tokenExpiresSoon(authStore.accessToken)) {
      const refreshed = await authStore.refreshAccessToken()
      if (!refreshed) return
    }
    if (
      intentionalDisconnect ||
      reconnectBlocked ||
      !authStore.accessToken ||
      !authStore.isLoggedIn
    ) {
      return
    }

    const url = new URL(
      import.meta.env.VITE_IM_WS_URL || 'ws://localhost:8080/ws/im'
    )
    url.searchParams.set('token', authStore.accessToken)
    const candidate = new WebSocket(url.toString())
    socket = candidate
    status.value = 'connecting'
    lastError.value = ''
    let opened = false

    candidate.onopen = () => {
      if (socket !== candidate) return
      opened = true
      status.value = 'connected'
      reconnectAttempts.value = 0
      handshakeRefreshUsed = false
      replacementNotice.value = ''
    }

    candidate.onmessage = (event) => {
      if (socket !== candidate) return
      handleMessage(event.data)
    }

    candidate.onerror = () => {
      if (socket === candidate) lastError.value = 'IM 实时连接异常'
    }

    candidate.onclose = (event) => {
      if (socket !== candidate) return
      socket = null
      status.value = 'disconnected'

      if (intentionalDisconnect || !authStore.isLoggedIn) return
      if (event.code === 4001) {
        reconnectBlocked = true
        replacementNotice.value = '账号已在其他页面建立了新的实时连接'
        clearReconnectTimer()
        return
      }
      if (!opened && !handshakeRefreshUsed) {
        handshakeRefreshUsed = true
        void retryAfterHandshakeFailure()
        return
      }
      scheduleReconnect()
    }
  }

  async function retryAfterHandshakeFailure() {
    const authStore = useAuthStore()
    const refreshed = await authStore.refreshAccessToken()
    if (!refreshed || intentionalDisconnect || reconnectBlocked) return
    await openSocket()
  }

  function scheduleReconnect() {
    if (reconnectTimer || intentionalDisconnect || reconnectBlocked) return
    const delay = IM_RECONNECT_DELAYS[
      Math.min(reconnectAttempts.value, IM_RECONNECT_DELAYS.length - 1)
    ]
    reconnectAttempts.value += 1
    reconnectTimer = setTimeout(() => {
      reconnectTimer = undefined
      void openSocket()
    }, delay)
  }

  function handleMessage(raw: unknown) {
    if (typeof raw !== 'string') return
    let message: IMMessage
    try {
      message = JSON.parse(raw) as IMMessage
    } catch {
      return
    }
    const friendsStore = useFriendsStore()
    if (message.type === 'presence') {
      const data = message.data as PresenceEventData
      if (
        Number.isInteger(data?.user_id) &&
        (data.status === 'online' || data.status === 'offline')
      ) {
        friendsStore.applyPresence(data.user_id, data.status)
      }
    } else if (message.type === 'friendship_updated') {
      void friendsStore.handleFriendshipUpdated().catch(() => undefined)
    } else if (message.type === 'connection_replaced') {
      replacementNotice.value = '账号已在其他页面建立了新的实时连接'
    }
  }

  function disconnect() {
    intentionalDisconnect = true
    reconnectBlocked = false
    handshakeRefreshUsed = false
    clearReconnectTimer()
    const current = socket
    socket = null
    if (
      current &&
      (current.readyState === WebSocket.CONNECTING ||
        current.readyState === WebSocket.OPEN)
    ) {
      current.close(1000, 'logout')
    }
    status.value = 'disconnected'
    reconnectAttempts.value = 0
    replacementNotice.value = ''
    lastError.value = ''
  }

  function clearReconnectTimer() {
    if (!reconnectTimer) return
    clearTimeout(reconnectTimer)
    reconnectTimer = undefined
  }

  return {
    status,
    reconnectAttempts,
    replacementNotice,
    lastError,
    isConnected,
    connect,
    disconnect,
    handleMessage
  }
})

export function tokenExpiresSoon(token: string | null): boolean {
  if (!token) return true
  try {
    const payload = token.split('.')[1]
    if (!payload) return false
    const base64 = payload.replace(/-/g, '+').replace(/_/g, '/')
    const normalized = base64.padEnd(Math.ceil(base64.length / 4) * 4, '=')
    const claims = JSON.parse(atob(normalized)) as { exp?: number }
    if (!claims.exp) return false
    return claims.exp * 1000 - Date.now() <= TOKEN_REFRESH_WINDOW_MS
  } catch {
    return false
  }
}
