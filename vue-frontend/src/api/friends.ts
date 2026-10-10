import api from '@/composables/axios'

interface ApiResponse<T> {
  code: number
  message: string
  data: T
}

export type FriendshipStatus = 'pending' | 'accepted' | 'rejected'
export type FriendRequestDirection = 'incoming' | 'outgoing' | 'friend'
export type PresenceStatus = 'online' | 'offline' | 'unknown'

export interface PublicUser {
  id: number
  username: string
  nickname: string
  avatar_url: string
}

export interface FriendshipSummary {
  id: number
  status: FriendshipStatus
  direction?: 'incoming' | 'outgoing'
}

export interface UserSearchItem {
  user: PublicUser
  friendship: FriendshipSummary | null
}

export interface FriendRequestItem {
  id: number
  status: FriendshipStatus
  direction: FriendRequestDirection
  requested_by: number
  peer: PublicUser
  created_at: string
  updated_at: string
  responded_at?: string | null
}

export interface FriendItem {
  id: number
  peer: PublicUser
  presence: PresenceStatus
  updated_at: string
}

export interface CursorPage<T> {
  items: T[]
  next_cursor?: string | null
}

export async function searchUsers(keyword: string): Promise<UserSearchItem[]> {
  const response = await api.get<ApiResponse<{ items: UserSearchItem[] }>>(
    '/api/v1/users/search',
    { params: { keyword } }
  )
  return response.data.data.items ?? []
}

export async function sendFriendRequest(
  targetUserId: number
): Promise<FriendRequestItem> {
  const response = await api.post<ApiResponse<FriendRequestItem>>(
    '/api/v1/friend-requests',
    { target_user_id: targetUserId }
  )
  return response.data.data
}

export async function listFriendRequests(
  direction: 'incoming' | 'outgoing',
  cursor?: string,
  limit = 50
): Promise<CursorPage<FriendRequestItem>> {
  const response = await api.get<ApiResponse<CursorPage<FriendRequestItem>>>(
    '/api/v1/friend-requests',
    {
      params: {
        direction,
        status: 'pending',
        cursor: cursor || undefined,
        limit
      }
    }
  )
  return response.data.data
}

export async function acceptFriendRequest(
  requestId: number
): Promise<FriendRequestItem> {
  const response = await api.post<ApiResponse<FriendRequestItem>>(
    `/api/v1/friend-requests/${requestId}/accept`
  )
  return response.data.data
}

export async function rejectFriendRequest(
  requestId: number
): Promise<FriendRequestItem> {
  const response = await api.post<ApiResponse<FriendRequestItem>>(
    `/api/v1/friend-requests/${requestId}/reject`
  )
  return response.data.data
}

export async function listFriends(
  cursor?: string,
  limit = 100
): Promise<CursorPage<FriendItem>> {
  const response = await api.get<ApiResponse<CursorPage<FriendItem>>>(
    '/api/v1/friends',
    { params: { cursor: cursor || undefined, limit } }
  )
  return response.data.data
}

export async function deleteFriend(friendUserId: number): Promise<void> {
  await api.delete(`/api/v1/friends/${friendUserId}`)
}
