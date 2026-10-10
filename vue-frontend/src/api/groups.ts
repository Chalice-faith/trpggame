import api from '@/composables/axios'
import type { PublicUser } from '@/api/friends'

interface ApiResponse<T> {
  code: number
  message: string
  data: T
}

export type GroupRole = 'owner' | 'admin' | 'member'

export interface GroupSummary {
  id: number
  name: string
  avatar_url: string
  owner_id: number
  conversation_id: number
  current_user_role: GroupRole | null
  member_count: number
  version: number
  created_at: string
  updated_at: string
}

export interface GroupMember {
  id: number
  user: PublicUser
  role: GroupRole
  joined_at: string
}

export interface GroupPage {
  items: GroupSummary[]
  next_cursor?: string | null
}

export interface GroupMemberPage {
  items: GroupMember[]
  next_cursor?: string | null
}

export interface GroupUpdatedEventData {
  group: Pick<
    GroupSummary,
    'id' | 'name' | 'avatar_url' | 'owner_id' | 'member_count' | 'version'
  >
}

export interface GroupMemberChangedEventData {
  group_id: number
  event: 'member_joined' | 'member_role_changed' | 'member_removed' | 'member_left' | 'owner_transferred'
  actor_user_id: number
  target_user_id: number
  version: number
}

export async function createGroup(name: string, avatarUrl = ''): Promise<GroupSummary> {
  const response = await api.post<ApiResponse<GroupSummary>>('/api/v1/groups', {
    name,
    avatar_url: avatarUrl
  })
  return response.data.data
}

export async function listGroups(cursor?: string, limit = 20): Promise<GroupPage> {
  const response = await api.get<ApiResponse<GroupPage>>('/api/v1/groups', {
    params: { cursor: cursor || undefined, limit }
  })
  return response.data.data
}

export async function getGroup(groupId: number): Promise<GroupSummary> {
  const response = await api.get<ApiResponse<GroupSummary>>(`/api/v1/groups/${groupId}`)
  return response.data.data
}

export async function listGroupMembers(
  groupId: number,
  cursor?: string,
  limit = 50
): Promise<GroupMemberPage> {
  const response = await api.get<ApiResponse<GroupMemberPage>>(
    `/api/v1/groups/${groupId}/members`,
    { params: { cursor: cursor || undefined, limit } }
  )
  return response.data.data
}

export async function updateGroup(
  groupId: number,
  expectedVersion: number,
  changes: { name?: string; avatar_url?: string }
): Promise<GroupSummary> {
  const response = await api.patch<ApiResponse<GroupSummary>>(
    `/api/v1/groups/${groupId}`,
    { ...changes, expected_version: expectedVersion }
  )
  return response.data.data
}

export async function inviteGroupMembers(
  groupId: number,
  userIds: number[],
  expectedVersion: number
): Promise<GroupSummary> {
  const response = await api.post<ApiResponse<GroupSummary>>(
    `/api/v1/groups/${groupId}/members`,
    { user_ids: userIds, expected_version: expectedVersion }
  )
  return response.data.data
}

export async function setGroupMemberRole(
  groupId: number,
  userId: number,
  role: Exclude<GroupRole, 'owner'>,
  expectedVersion: number
): Promise<GroupSummary> {
  const response = await api.patch<ApiResponse<GroupSummary>>(
    `/api/v1/groups/${groupId}/members/${userId}`,
    { role, expected_version: expectedVersion }
  )
  return response.data.data
}

export async function removeGroupMember(
  groupId: number,
  userId: number,
  expectedVersion: number
): Promise<GroupSummary> {
  const response = await api.delete<ApiResponse<GroupSummary>>(
    `/api/v1/groups/${groupId}/members/${userId}`,
    { params: { expected_version: expectedVersion } }
  )
  return response.data.data
}

export async function transferGroupOwner(
  groupId: number,
  newOwnerUserId: number,
  expectedVersion: number
): Promise<GroupSummary> {
  const response = await api.post<ApiResponse<GroupSummary>>(
    `/api/v1/groups/${groupId}/transfer`,
    { new_owner_user_id: newOwnerUserId, expected_version: expectedVersion }
  )
  return response.data.data
}
