import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import { getMultiplayerGameState, type MultiplayerGameState } from '@/api/game'

type HistoryEntry = { role: 'user' | 'assistant' | 'system'; content: string }
type LiveStatus = MultiplayerGameState['status'] | 'ended'

export const useMultiplayerStore = defineStore('multiplayer', () => {
  const snapshot = ref<MultiplayerGameState | null>(null)
  const status = ref<LiveStatus | null>(null)
  const history = ref<HistoryEntry[]>([])
  const draft = ref('')
  const activeRequestId = ref<string | null>(null)
  const actionPending = ref(false)
  const lastDiceRoll = ref<Record<string, unknown> | null>(null)
  const lastSeq = ref(0)
  const loading = ref(false)
  const error = ref('')

  const currentTurn = computed(() => snapshot.value?.current_turn ?? 0)
  const currentActorId = computed(() => snapshot.value?.current_actor_id ?? null)
  const deadlineAt = computed(() => snapshot.value?.deadline_at ?? null)

  async function refresh(roomId: number) {
    loading.value = true
    try {
      applySnapshot(await getMultiplayerGameState(roomId))
      error.value = ''
    } catch (cause: any) {
      error.value = cause?.response?.data?.message || '多人游戏状态读取失败'
      throw cause
    } finally {
      loading.value = false
    }
  }

  function applySnapshot(next: MultiplayerGameState, seq = next.seq) {
    if (next.version !== 2 || !Number.isInteger(next.room_id) || !next.generation ||
      (snapshot.value && snapshot.value.room_id !== next.room_id) || seq < lastSeq.value) return
    const previous = snapshot.value
    snapshot.value = { ...next, seq }
    status.value = next.status
    lastSeq.value = seq
    history.value = next.recent_messages.map(({ role, content }) => ({ role, content }))
    if (!previous || previous.generation !== next.generation || previous.current_turn !== next.current_turn || next.status !== 'playing') {
      clearDraft()
    }
  }

  function markSubmitted(requestId: string) {
    activeRequestId.value = requestId
    actionPending.value = true
    draft.value = ''
  }

  function clearDraft() {
    draft.value = ''
    activeRequestId.value = null
    actionPending.value = false
  }

  function handleEvent(type: string, data: any, requestId = '', seq = 0) {
    const current = snapshot.value
    if (!current || (seq > 0 && seq <= lastSeq.value)) return
    if (seq > 0) lastSeq.value = seq

    if (type === 'game_ended') {
      current.status = 'ended'
      current.deadline_at = null
      status.value = 'ended'
      clearDraft()
      return
    }
    if (type === 'game_status_changed') {
      if (typeof data?.generation !== 'string') return
      current.generation = data.generation
      current.status = data.status
      current.current_turn = data.current_turn
      current.round_number = data.round_number
      current.current_actor_id = data.current_actor_id
      current.deadline_at = data.deadline_at ?? null
      status.value = data.status
      clearDraft()
      void refresh(current.room_id).catch(() => {})
      return
    }
    if (data?.generation !== current.generation) return

    switch (type) {
      case 'turn_start':
        current.current_turn = data.current_turn
        current.round_number = data.round_number
        current.current_actor_id = data.current_actor_id
        current.deadline_at = data.deadline_at
        clearDraft()
        break
      case 'action_started':
        if (data.current_turn !== current.current_turn) break
        current.deadline_at = null
        activeRequestId.value = requestId || activeRequestId.value
        actionPending.value = true
        draft.value = ''
        break
      case 'narrative_chunk':
        if (data.current_turn !== current.current_turn || !requestId || requestId !== activeRequestId.value) break
        draft.value += String(data.content ?? '')
        break
      case 'action_cancelled':
        if (!activeRequestId.value || requestId === activeRequestId.value) clearDraft()
        void refresh(current.room_id).catch(() => {})
        break
      case 'narrative_complete':
        if (typeof data.narrative === 'string' && data.narrative.trim() &&
          history.value.at(-1)?.content !== data.narrative) {
          history.value.push({ role: 'assistant', content: data.narrative })
        }
        clearDraft()
        void refresh(current.room_id).catch(() => {})
        break
      case 'turn_skip':
        clearDraft()
        void refresh(current.room_id).catch(() => {})
        break
      case 'dice_roll':
        lastDiceRoll.value = data.dice_roll ?? null
        break
      case 'status_update':
        void refresh(current.room_id).catch(() => {})
        break
    }
  }

  function reset() {
    snapshot.value = null
    status.value = null
    history.value = []
    lastDiceRoll.value = null
    lastSeq.value = 0
    loading.value = false
    error.value = ''
    clearDraft()
  }

  return {
    snapshot, status, history, draft, activeRequestId, actionPending, lastDiceRoll,
    lastSeq, loading, error, currentTurn, currentActorId, deadlineAt,
    refresh, applySnapshot, markSubmitted, clearDraft, handleEvent, reset
  }
})
