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

export const useGameStore = defineStore('game', () => {
  // ---- state ----
  const currentRoom = ref<GameRoom | null>(null)
  const playerStatus = ref<PlayerStatus | null>(null)
  const narrativeHistory = ref<Array<{ role: 'gm' | 'player'; content: string }>>([])
  const isStreaming = ref(false)

  // ---- actions ----
  function setRoom(room: GameRoom) {
    currentRoom.value = room
  }

  function appendNarrative(role: 'gm' | 'player', content: string) {
    narrativeHistory.value.push({ role, content })
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
  }

  return { currentRoom, playerStatus, narrativeHistory, isStreaming, setRoom, appendNarrative, updatePlayerStatus, reset }
})
