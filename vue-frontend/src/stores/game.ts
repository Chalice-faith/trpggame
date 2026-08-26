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

export interface RuntimeItem {
  name: string
  quantity: number
  description: string
}

export interface RuntimeBuff {
  name: string
  duration: number
}

export interface PlayerStatus {
  hp: number | null
  maxHp: number | null
  mp: number | null
  maxMp: number | null
  san: number | null
  maxSan: number | null
  ac: number | null
  level: number | null
  location: string
  items: RuntimeItem[]
  buffs: RuntimeBuff[]
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

  function setRoomStatus(status: string) {
    if (currentRoom.value) currentRoom.value.status = status
  }

  function setCurrentTurn(turn: number) {
    if (currentRoom.value && Number.isInteger(turn) && turn >= 0) {
      currentRoom.value.current_turn = turn
    }
  }

  function setPlayerStatusFromAttributes(attributes: Record<string, unknown> = {}) {
    const readNumber = (...keys: string[]) => {
      for (const key of keys) {
        const value = Number(attributes[key])
        if (Number.isFinite(value)) return value
      }
      return null
    }
    const readItems = (value: unknown): RuntimeItem[] => {
      if (!Array.isArray(value)) return []
      return value.flatMap((item) => {
        if (typeof item === 'string') return [{ name: item, quantity: 1, description: '' }]
        if (!item || typeof item !== 'object') return []
        const record = item as Record<string, unknown>
        const name = String(record.name ?? record.item_name ?? '').trim()
        const quantity = Number(record.quantity ?? 1)
        if (!name || !Number.isFinite(quantity) || quantity <= 0) return []
        return [{ name, quantity: Math.floor(quantity), description: String(record.description ?? '') }]
      })
    }
    const readBuffs = (value: unknown): RuntimeBuff[] => {
      if (!Array.isArray(value)) return []
      return value.flatMap((item) => {
        if (typeof item === 'string') return [{ name: item, duration: 0 }]
        if (!item || typeof item !== 'object') return []
        const record = item as Record<string, unknown>
        const name = String(record.name ?? record.buff_name ?? '').trim()
        const duration = Number(record.duration ?? 0)
        return name && Number.isFinite(duration)
          ? [{ name, duration: Math.max(0, Math.floor(duration)) }]
          : []
      })
    }

    const hp = readNumber('hp')
    const mp = readNumber('mp')
    const san = readNumber('san')
    playerStatus.value = {
      hp,
      maxHp: readNumber('max_hp', 'hp_max') ?? hp,
      mp,
      maxMp: readNumber('max_mp', 'mp_max') ?? mp,
      san,
      maxSan: readNumber('max_san', 'san_max') ?? san,
      ac: readNumber('ac'),
      level: readNumber('level'),
      location: String(attributes.location ?? '').trim(),
      items: readItems(attributes.items ?? attributes.inventory),
      buffs: readBuffs(attributes.buffs ?? attributes.status_effects)
    }
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
    if (currentTurn !== undefined) setCurrentTurn(currentTurn)
  }

  function setDiceRoll(roll: GameDiceRoll) {
    lastDiceRoll.value = roll
  }

  function setStatusUpdate(update: Record<string, unknown>) {
    lastStatusUpdate.value = update
  }

  function applyStatusUpdate(update: Record<string, any>) {
    setStatusUpdate(update)
    const status = playerStatus.value
    if (!status) return
    const changes = update.changes && typeof update.changes === 'object'
      ? update.changes
      : update
    const playerChanges = changes.player_state_changes
    if (playerChanges && typeof playerChanges === 'object') {
      for (const [key, value] of Object.entries(playerChanges)) {
        const numeric = Number(value)
        if (key === 'hp' && Number.isFinite(numeric)) status.hp = numeric
        if (key === 'mp' && Number.isFinite(numeric)) status.mp = numeric
        if (key === 'san' && Number.isFinite(numeric)) status.san = numeric
        if (key === 'ac' && Number.isFinite(numeric)) status.ac = numeric
        if (key === 'level' && Number.isFinite(numeric)) status.level = numeric
        if (key === 'location') status.location = String(value)
      }
    }
    if (Array.isArray(changes.items)) {
      for (const item of changes.items) {
        const name = String(item?.name ?? '').trim()
        const delta = Number(item?.quantity_delta)
        if (!name || !Number.isFinite(delta) || delta === 0) continue
        const current = status.items.find((entry) => entry.name === name)
        if (current) {
          current.quantity += delta
          if (current.quantity <= 0) status.items = status.items.filter((entry) => entry.name !== name)
        } else if (delta > 0) {
          status.items.push({ name, quantity: delta, description: String(item?.description ?? '') })
        }
      }
    }
    if (Array.isArray(changes.buffs)) {
      for (const buff of changes.buffs) {
        const name = String(buff?.name ?? '').trim()
        const duration = Number(buff?.duration)
        if (!name || !Number.isFinite(duration)) continue
        const current = status.buffs.find((entry) => entry.name === name)
        if (duration <= 0) {
          status.buffs = status.buffs.filter((entry) => entry.name !== name)
        } else if (current) {
          current.duration = duration
        } else {
          status.buffs.push({ name, duration })
        }
      }
    }
  }

  function failNarrative() {
    streamingNarrative.value = ''
    isStreaming.value = false
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
    setRoomStatus,
    setCurrentTurn,
    setPlayerStatusFromAttributes,
    appendNarrative,
    beginNarrativeStream,
    appendNarrativeChunk,
    completeNarrative,
    setDiceRoll,
    setStatusUpdate,
    applyStatusUpdate,
    updatePlayerStatus,
    failNarrative,
    reset
  }
})
