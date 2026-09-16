import api from '@/composables/axios'
import type { PublicUser } from '@/api/friends'

interface ApiResponse<T> {
  code: number
  message: string
  data: T
}

export interface ChatMessage {
  id: number
  conversation_id: number
  seq: number
  sender: PublicUser | null
  client_message_id: string
  message_type: 'text' | 'system'
  content: string
  metadata: Record<string, unknown>
  created_at: string
}

export interface ConversationGroupSummary {
  id: number
  name: string
  avatar_url: string
  current_user_role: 'owner' | 'admin' | 'member'
  member_count: number
  version: number
}

interface ConversationBase {
  id: number
  can_send: boolean
  last_seq: number
  last_read_seq: number
  unread_count: number
  last_message: ChatMessage | null
  created_at: string
  updated_at: string
}

export interface DirectConversationSummary extends ConversationBase {
  type: 'direct'
  peer: PublicUser
  group: null
}

export interface GroupConversationSummary extends ConversationBase {
  type: 'group'
  peer: null
  group: ConversationGroupSummary
}

export type ConversationSummary =
  | DirectConversationSummary
  | GroupConversationSummary

export interface ConversationPage {
  items: ConversationSummary[]
  next_cursor?: string | null
}

export interface MessagePage {
  items: ChatMessage[]
  next_before_seq: number
  has_more: boolean
}

export interface ReadResult {
  conversation_id: number
  last_read_seq: number
  unread_count: number
}

export async function createDirectConversation(
  peerUserId: number
): Promise<ConversationSummary> {
  const response = await api.post<ApiResponse<ConversationSummary>>(
    '/api/v1/conversations/direct',
    { peer_user_id: peerUserId }
  )
  return response.data.data
}

export async function listConversations(
  cursor?: string,
  limit = 20
): Promise<ConversationPage> {
  const response = await api.get<ApiResponse<ConversationPage>>(
    '/api/v1/conversations',
    { params: { cursor: cursor || undefined, limit } }
  )
  return response.data.data
}

export async function listMessages(
  conversationId: number,
  beforeSeq?: number,
  limit = 50
): Promise<MessagePage> {
  const response = await api.get<ApiResponse<MessagePage>>(
    `/api/v1/conversations/${conversationId}/messages`,
    { params: { before_seq: beforeSeq || undefined, limit } }
  )
  return response.data.data
}

export async function markConversationRead(
  conversationId: number,
  lastReadSeq: number
): Promise<ReadResult> {
  const response = await api.post<ApiResponse<ReadResult>>(
    `/api/v1/conversations/${conversationId}/read`,
    { last_read_seq: lastReadSeq }
  )
  return response.data.data
}
