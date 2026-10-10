import api from '@/composables/axios'
import type { PublicUser } from '@/api/friends'

interface ApiResponse<T> {
  code: number
  message: string
  data: T
}

export type RoomStatus = 'waiting' | 'playing' | 'paused' | 'ended'

export interface RoomSummary {
  id: number
  name: string
  script_id: number
  owner_id: number
  status: RoomStatus
  max_players: number
  room_code: string
  version: number
  created_at: string
}

export interface RoomMember {
  user: PublicUser
  character_id: number | null
  is_ready: boolean
  player_order: number
  joined_at: string
}

export interface RoomCharacter {
  id: number
  name: string
  description: string
}

export interface RoomSnapshot extends RoomSummary {
  members: RoomMember[]
  characters: RoomCharacter[]
}

export interface CreateRoomInput {
  name: string
  script_id: number
  max_players: number
}

export async function createRoom(input: CreateRoomInput): Promise<RoomSnapshot> {
  const response = await api.post<ApiResponse<RoomSnapshot>>('/api/v1/rooms', input)
  return response.data.data
}

export async function listRooms(): Promise<RoomSummary[]> {
  const response = await api.get<ApiResponse<RoomSummary[]>>('/api/v1/rooms')
  return response.data.data
}

export async function getRoom(roomId: number): Promise<RoomSnapshot> {
  const response = await api.get<ApiResponse<RoomSnapshot>>(`/api/v1/rooms/${roomId}`)
  return response.data.data
}

export async function joinRoom(roomCode: string): Promise<RoomSnapshot> {
  const response = await api.post<ApiResponse<RoomSnapshot>>('/api/v1/rooms/join', {
    room_code: roomCode
  })
  return response.data.data
}

export async function selectRoomCharacter(
  roomId: number,
  characterId: number,
  expectedVersion: number
): Promise<RoomSnapshot> {
  const response = await api.post<ApiResponse<RoomSnapshot>>(
    `/api/v1/rooms/${roomId}/character`,
    { character_id: characterId, expected_version: expectedVersion }
  )
  return response.data.data
}

export async function setRoomReady(
  roomId: number,
  ready: boolean,
  expectedVersion: number
): Promise<RoomSnapshot> {
  const response = await api.post<ApiResponse<RoomSnapshot>>(
    `/api/v1/rooms/${roomId}/ready`,
    { ready, expected_version: expectedVersion }
  )
  return response.data.data
}

export async function leaveRoom(
  roomId: number,
  expectedVersion: number
): Promise<RoomSnapshot> {
  const response = await api.post<ApiResponse<RoomSnapshot>>(
    `/api/v1/rooms/${roomId}/leave`,
    { expected_version: expectedVersion }
  )
  return response.data.data
}

export async function removeRoomMember(
  roomId: number,
  userId: number,
  expectedVersion: number
): Promise<RoomSnapshot> {
  const response = await api.post<ApiResponse<RoomSnapshot>>(
    `/api/v1/rooms/${roomId}/members/${userId}/remove`,
    { expected_version: expectedVersion }
  )
  return response.data.data
}

export async function transferRoomOwner(
  roomId: number,
  newOwnerUserId: number,
  expectedVersion: number
): Promise<RoomSnapshot> {
  const response = await api.post<ApiResponse<RoomSnapshot>>(
    `/api/v1/rooms/${roomId}/transfer`,
    { new_owner_user_id: newOwnerUserId, expected_version: expectedVersion }
  )
  return response.data.data
}

export async function startRoom(
  roomId: number,
  expectedVersion: number
): Promise<RoomSnapshot> {
  const response = await api.post<ApiResponse<RoomSnapshot>>(
    `/api/v1/rooms/${roomId}/start`,
    { expected_version: expectedVersion }
  )
  return response.data.data
}
