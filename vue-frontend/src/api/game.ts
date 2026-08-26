import api from '@/composables/axios'

interface ApiResponse<T> {
  code: number
  message: string
  data: T
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
