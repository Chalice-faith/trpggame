import api from '@/composables/axios'

interface ApiResponse<T> {
  code: number
  message: string
  data: T
}

export type GameRoomStatus = 'waiting' | 'playing' | 'paused' | 'ended'

export interface GameSaveSummary {
  id: number
  save_name: string
  round_number: number
  is_auto: boolean
  created_at: string
}

export interface GameSavesResult {
  items: GameSaveSummary[]
  total: number
}

export interface GameStatusResult {
  room_id: number
  status: GameRoomStatus
}

export interface LoadGameResult extends GameStatusResult {
  save_id: number
  turn: number
}

export interface StartSoloGameResult {
  room_id: number
  game_status: string
  opening_narrative: string
}

export async function startSoloGame(
  scriptId: number,
  characterId: number
): Promise<StartSoloGameResult> {
  const response = await api.post<ApiResponse<StartSoloGameResult>>(
    '/api/v1/games/solo/start',
    { script_id: scriptId, character_id: characterId }
  )
  return response.data.data
}

export async function createGameSave(
  roomId: number,
  saveName: string
): Promise<{ save_id: number }> {
  const response = await api.post<ApiResponse<{ save_id: number }>>(
    `/api/v1/games/${roomId}/save`,
    { save_name: saveName.trim() }
  )
  return response.data.data
}

export async function listGameSaves(roomId: number): Promise<GameSavesResult> {
  const response = await api.get<ApiResponse<GameSavesResult>>(
    `/api/v1/games/${roomId}/saves`
  )
  return response.data.data
}

export async function loadGame(
  roomId: number,
  saveId: number
): Promise<LoadGameResult> {
  const response = await api.post<ApiResponse<LoadGameResult>>(
    `/api/v1/games/${roomId}/load`,
    { save_id: saveId }
  )
  return response.data.data
}

export async function pauseGame(roomId: number): Promise<GameStatusResult> {
  const response = await api.post<ApiResponse<GameStatusResult>>(
    `/api/v1/games/${roomId}/pause`
  )
  return response.data.data
}

export async function resumeGame(roomId: number): Promise<GameStatusResult> {
  const response = await api.post<ApiResponse<GameStatusResult>>(
    `/api/v1/games/${roomId}/resume`
  )
  return response.data.data
}

export async function endGame(roomId: number): Promise<GameStatusResult> {
  const response = await api.post<ApiResponse<GameStatusResult>>(
    `/api/v1/games/${roomId}/end`
  )
  return response.data.data
}
