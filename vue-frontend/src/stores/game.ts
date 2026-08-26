import { defineStore } from 'pinia'
import { ref } from 'vue'

export interface GameRoom {
  id: number
  script_id: number
  title: string
  status: string
  current_turn: number
  round_count: number
}

export interface PlayerStatus {
  hp: number
  maxHp: number
  mp: number
  maxMp: number
  san: number
  maxSan: number
  items: string[]
  buffs: Record<string, number>
}

export interface GameDiceRoll {
  type: string
  result: number
  target: number
  success: boolean
  critical_hit: boolean
  critical_miss: boolean
  description: string
  reason?: string
}

export const useGameStore = defineStore('game', () => {
  // ---- state ----
  const currentRoom = ref<GameRoom | null>(null)
  const playerStatus = ref<PlayerStatus | null>(null)
  const narrativeHistory = ref<Array<{ role: 'gm' | 'player'; content: string }>>([])
  const isStreaming = ref(false)
  const streamingNarrative = ref('')
  const lastDiceRoll = ref<GameDiceRoll | null>(null)
  const lastStatusUpdate = ref<Record<string, unknown> | null>(null)

  // ---- actions ----
  function setRoom(room: GameRoom) {
    currentRoom.value = room
  }

  function appendNarrative(role: 'gm' | 'player', content: string) {
    narrativeHistory.value.push({ role, content })
  }

  function beginNarrativeStream() {
    isStreaming.value = true
    streamingNarrative.value = ''
  }

  function appendNarrativeChunk(content: string) {
    if (!isStreaming.value) beginNarrativeStream()
    streamingNarrative.value += content
  }

  function completeNarrative(narrative: string, currentTurn?: number) {
    const content = narrative.trim() || streamingNarrative.value.trim()
    if (content) appendNarrative('gm', content)
    streamingNarrative.value = ''
    isStreaming.value = false
    if (currentRoom.value && currentTurn !== undefined) {
      currentRoom.value.current_turn = currentTurn
    }
  }

  function setDiceRoll(roll: GameDiceRoll) {
    lastDiceRoll.value = roll
  }

  function setStatusUpdate(update: Record<string, unknown>) {
    lastStatusUpdate.value = update
  }

  function updatePlayerStatus(changes: Partial<PlayerStatus>) {
    if (playerStatus.value) {
      Object.assign(playerStatus.value, changes)
    }
  }

  function reset() {
    currentRoom.value = null
    playerStatus.value = null
    narrativeHistory.value = []
    isStreaming.value = false
    streamingNarrative.value = ''
    lastDiceRoll.value = null
    lastStatusUpdate.value = null
  }

  return {
    currentRoom,
    playerStatus,
    narrativeHistory,
    isStreaming,
    streamingNarrative,
    lastDiceRoll,
    lastStatusUpdate,
    setRoom,
    appendNarrative,
    beginNarrativeStream,
    appendNarrativeChunk,
    completeNarrative,
    setDiceRoll,
    setStatusUpdate,
    updatePlayerStatus,
    reset
  }
})
