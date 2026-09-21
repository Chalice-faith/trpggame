import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import {
  createRoom,
  getRoom,
  joinRoom,
  leaveRoom,
  listRooms,
  removeRoomMember,
  selectRoomCharacter,
  setRoomReady,
  startRoom,
  transferRoomOwner,
  type CreateRoomInput,
  type RoomSnapshot,
  type RoomSummary
} from '@/api/rooms'
import { useAuthStore } from '@/stores/auth'

export const useRoomsStore = defineStore('rooms', () => {
  const rooms = ref<RoomSummary[]>([])
  const currentRoom = ref<RoomSnapshot | null>(null)
  const loading = ref(false)
  const mutating = ref(false)
  const errorMessage = ref('')
  const revokedRoomId = ref<number | null>(null)

  const currentMember = computed(() => {
    const userId = useAuthStore().user?.id
    return currentRoom.value?.members.find(({ user }) => user.id === userId) ?? null
  })
  const isOwner = computed(() => currentRoom.value?.owner_id === useAuthStore().user?.id)
  const canStart = computed(() => {
    const room = currentRoom.value
    return !!room && room.status === 'waiting' && room.members.length >= 2 &&
      room.members.every((member) => !!member.character_id && member.is_ready)
  })

  async function loadRooms() {
    loading.value = true
    errorMessage.value = ''
    try {
      rooms.value = (await listRooms()).slice().sort((left, right) => right.id - left.id)
    } catch (error: any) {
      errorMessage.value = error?.response?.data?.message || '房间列表加载失败'
      throw error
    } finally {
      loading.value = false
    }
  }

  async function openRoom(roomId: number) {
    loading.value = true
    errorMessage.value = ''
    revokedRoomId.value = null
    try {
      const snapshot = await getRoom(roomId)
      applySnapshot(snapshot, true)
      return snapshot
    } catch (error: any) {
      if (error?.response?.data?.code === 1901) removeLocalRoom(roomId)
      errorMessage.value = error?.response?.data?.message || '大厅加载失败'
      throw error
    } finally {
      loading.value = false
    }
  }

  async function create(input: CreateRoomInput) {
    return mutate(async () => {
      const snapshot = await createRoom({
        name: input.name.trim(),
        script_id: input.script_id,
        max_players: input.max_players
      })
      applySnapshot(snapshot, true)
      return snapshot
    })
  }

  async function join(roomCode: string) {
    return mutate(async () => {
      const snapshot = await joinRoom(roomCode.trim().toUpperCase())
      applySnapshot(snapshot, true)
      return snapshot
    })
  }

  async function selectCharacter(characterId: number) {
    return mutateCurrent((room) => selectRoomCharacter(room.id, characterId, room.version))
  }

  async function setReady(ready: boolean) {
    return mutateCurrent((room) => setRoomReady(room.id, ready, room.version))
  }

  async function leave() {
    const roomId = currentRoom.value?.id
    const result = await mutateCurrent((room) => leaveRoom(room.id, room.version))
    if (roomId) removeLocalRoom(roomId)
    return result
  }

  async function removeMember(userId: number) {
    return mutateCurrent((room) => removeRoomMember(room.id, userId, room.version))
  }

  async function transferOwner(userId: number) {
    return mutateCurrent((room) => transferRoomOwner(room.id, userId, room.version))
  }

  async function start() {
    return mutateCurrent((room) => startRoom(room.id, room.version))
  }

  async function mutateCurrent(operation: (room: RoomSnapshot) => Promise<RoomSnapshot>) {
    const room = currentRoom.value
    if (!room) throw new Error('room not selected')
    return mutate(async () => {
      try {
        const snapshot = await operation(room)
        applySnapshot(snapshot, true)
        return snapshot
      } catch (error: any) {
        if (error?.response?.data?.code === 1908) await openRoom(room.id)
        throw error
      }
    })
  }

  async function mutate<T>(operation: () => Promise<T>) {
    if (mutating.value) throw new Error('room mutation in progress')
    mutating.value = true
    try {
      return await operation()
    } finally {
      mutating.value = false
    }
  }

  function applyRealtimeSnapshot(snapshot: RoomSnapshot) {
    const userId = useAuthStore().user?.id
    if (
      currentRoom.value?.id === snapshot?.id &&
      userId &&
      !snapshot.members?.some(({ user }) => user.id === userId)
    ) {
      handleAccessRevoked(snapshot.id)
      return
    }
    applySnapshot(snapshot, false)
  }

  function applySnapshot(snapshot: RoomSnapshot, authoritative: boolean) {
    if (!snapshot?.id || !snapshot.version) return
    const current = currentRoom.value
    if (current?.id === snapshot.id && snapshot.version < current.version) return
    if (current?.id === snapshot.id && !authoritative && snapshot.version === current.version) return
    if (current?.id === snapshot.id || authoritative) currentRoom.value = snapshot
    upsertSummary(snapshot)
  }

  function upsertSummary(room: RoomSummary) {
    const summary: RoomSummary = {
      id: room.id,
      name: room.name,
      script_id: room.script_id,
      owner_id: room.owner_id,
      status: room.status,
      max_players: room.max_players,
      room_code: room.room_code,
      version: room.version,
      created_at: room.created_at
    }
    const index = rooms.value.findIndex(({ id }) => id === summary.id)
    if (index >= 0) {
      if (summary.version >= rooms.value[index].version) rooms.value[index] = summary
    } else rooms.value.push(summary)
    rooms.value.sort((left, right) => right.id - left.id)
  }

  function handleAccessRevoked(roomId: number) {
    revokedRoomId.value = roomId
    removeLocalRoom(roomId)
  }

  function removeLocalRoom(roomId: number) {
    rooms.value = rooms.value.filter(({ id }) => id !== roomId)
    if (currentRoom.value?.id === roomId) currentRoom.value = null
  }

  function clearCurrentRoom() {
    currentRoom.value = null
    revokedRoomId.value = null
  }

  function clear() {
    rooms.value = []
    currentRoom.value = null
    loading.value = false
    mutating.value = false
    errorMessage.value = ''
    revokedRoomId.value = null
  }

  return {
    rooms,
    currentRoom,
    loading,
    mutating,
    errorMessage,
    revokedRoomId,
    currentMember,
    isOwner,
    canStart,
    loadRooms,
    openRoom,
    create,
    join,
    selectCharacter,
    setReady,
    leave,
    removeMember,
    transferOwner,
    start,
    applyRealtimeSnapshot,
    handleAccessRevoked,
    removeLocalRoom,
    clearCurrentRoom,
    clear
  }
})
