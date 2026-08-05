import api from '@/composables/axios'
import type { AxiosProgressEvent } from 'axios'

export type ScriptStatus = 'uploading' | 'parsing' | 'ready' | 'failed'

export interface ScriptListItem {
  id: number
  title: string
  description: string
  cover_url: string
  file_size: number
  status: ScriptStatus
  parse_error?: string
  chunk_count: number
  created_at: string
  updated_at: string
}

export interface ScriptListData {
  items: ScriptListItem[]
  total: number
  page: number
  page_size: number
}

export interface ScriptCharacter {
  id: number
  name: string
  description: string
  attributes: Record<string, unknown>
}

export interface ScriptDetail extends ScriptListItem {
  characters: ScriptCharacter[]
}

interface ApiResponse<T> {
  code: number
  message: string
  data: T
}

export interface UploadScriptInput {
  file: File
  title?: string
  description?: string
}

export interface UploadedScript {
  id: number
  title: string
  description: string
  file_size: number
  status: ScriptStatus
  created_at: string
}

export async function listScripts(
  page: number,
  pageSize: number
): Promise<ScriptListData> {
  const response = await api.get<ApiResponse<ScriptListData>>('/api/v1/scripts', {
    params: {
      page,
      page_size: pageSize
    }
  })
  return response.data.data
}

export async function uploadScript(
  input: UploadScriptInput,
  onProgress?: (percentage: number) => void
): Promise<UploadedScript> {
  const form = new FormData()
  form.append('file', input.file)
  if (input.title?.trim()) form.append('title', input.title.trim())
  if (input.description?.trim()) {
    form.append('description', input.description.trim())
  }

  const response = await api.post<ApiResponse<UploadedScript>>(
    '/api/v1/scripts/upload',
    form,
    {
      headers: { 'Content-Type': 'multipart/form-data' },
      timeout: 120_000,
      onUploadProgress(event: AxiosProgressEvent) {
        if (!onProgress || !event.total) return
        onProgress(Math.min(100, Math.round((event.loaded / event.total) * 100)))
      }
    }
  )
  return response.data.data
}

export async function getScript(scriptId: number): Promise<ScriptDetail> {
  const response = await api.get<ApiResponse<ScriptDetail>>(
    `/api/v1/scripts/${scriptId}`
  )
  return response.data.data
}

export async function retryScript(
  scriptId: number
): Promise<{ id: number; status: ScriptStatus }> {
  const response = await api.post<
    ApiResponse<{ id: number; status: ScriptStatus }>
  >(`/api/v1/scripts/${scriptId}/retry`)
  return response.data.data
}

export async function deleteScript(scriptId: number): Promise<void> {
  await api.delete(`/api/v1/scripts/${scriptId}`)
}
