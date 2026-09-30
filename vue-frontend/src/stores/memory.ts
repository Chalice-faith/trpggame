import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { getGameMemoryStatus, type GameMemoryStatus } from '@/api/game'

export const useMemoryStore = defineStore('game-memory', () => {
  const status = ref<GameMemoryStatus | null>(null)
  const error = ref('')
  const roomId = ref<number | null>(null)
  let sequence = 0
  let inflight: Promise<void> | null = null

  const ready = computed(() => status.value?.status === 'disabled' || status.value?.status === 'ready')
  const expected = computed(() => status.value?.enabled && status.value.status === 'ready' && status.value.timeline_id && status.value.generation
    ? { timelineId: status.value.timeline_id, generation: status.value.generation }
    : undefined)
  const hint = computed(() => {
    if (error.value) return error.value
    switch (status.value?.status) {
      case 'pending': return '行动已提交，正在归档；请等待确认后继续。'
      case 'recovering': return '正在恢复游戏状态；请稍后刷新。'
      case 'blocked': return '游戏状态需要人工处理；请勿重复提交行动。'
      default: return ''
    }
  })

  async function refresh(targetRoomId: number) {
    if (roomId.value === targetRoomId && inflight) return inflight
    if (roomId.value !== targetRoomId) {
      roomId.value = targetRoomId
      status.value = null
    }
    const current = ++sequence
    const request = (async () => {
      try {
        const next = await getGameMemoryStatus(targetRoomId)
        if (current === sequence && roomId.value === targetRoomId) {
          status.value = next
          error.value = ''
        }
      } catch (cause: any) {
        if (current === sequence && roomId.value === targetRoomId) {
          error.value = cause?.response?.data?.message || '恢复状态暂时不可用，请刷新重试'
          status.value = null
        }
        throw cause
      }
    })()
    inflight = request
    try { await request } finally { if (inflight === request) inflight = null }
  }

  function reset() {
    sequence++
    inflight = null
    roomId.value = null
    status.value = null
    error.value = ''
  }

  return { status, error, roomId, ready, expected, hint, refresh, reset }
})
