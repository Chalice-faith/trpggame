// @vitest-environment jsdom

import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { GroupSummary } from '@/api/groups'
import { useAuthStore } from '@/stores/auth'
import { useChatStore } from '@/stores/chat'
import { useGroupsStore } from '@/stores/groups'

const apiMocks = vi.hoisted(() => ({
  createGroup: vi.fn(),
  getGroup: vi.fn(),
  inviteGroupMembers: vi.fn(),
  listGroupMembers: vi.fn(),
  listGroups: vi.fn(),
  removeGroupMember: vi.fn(),
  setGroupMemberRole: vi.fn(),
  transferGroupOwner: vi.fn(),
  updateGroup: vi.fn()
}))

vi.mock('@/api/groups', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/api/groups')>()),
  ...apiMocks
}))

function group(overrides: Partial<GroupSummary> = {}): GroupSummary {
  return {
    id: 9,
    name: '周五夜调查局',
    avatar_url: '',
    owner_id: 7,
    conversation_id: 41,
    current_user_role: 'owner',
    member_count: 2,
    version: 3,
    created_at: '2026-09-15T01:00:00Z',
    updated_at: '2026-09-15T01:00:00Z',
    ...overrides
  }
}

describe('groups store', () => {
  beforeEach(() => {
    localStorage.clear()
    setActivePinia(createPinia())
    const authStore = useAuthStore()
    authStore.user = {
      id: 7,
      username: 'player07',
      email: 'player07@example.test',
      nickname: '守秘人',
      avatar_url: ''
    }
    authStore.accessToken = 'access-token'
    vi.clearAllMocks()
    apiMocks.listGroups.mockResolvedValue({ items: [group()], next_cursor: null })
    apiMocks.getGroup.mockResolvedValue(group())
    apiMocks.listGroupMembers.mockResolvedValue({
      items: [
        {
          id: 1,
          user: { id: 7, username: 'player07', nickname: '守秘人', avatar_url: '' },
          role: 'owner',
          joined_at: '2026-09-15T01:00:00Z'
        }
      ],
      next_cursor: null
    })
  })

  it('loads a group and refreshes members after a role mutation', async () => {
    const store = useGroupsStore()
    apiMocks.setGroupMemberRole.mockResolvedValue(group({ version: 4 }))

    await store.loadGroups()
    await store.openGroup(9)
    await store.setRole(8, 'admin')

    expect(apiMocks.setGroupMemberRole).toHaveBeenCalledWith(9, 8, 'admin', 3)
    expect(store.selectedGroup?.version).toBe(4)
    expect(apiMocks.listGroupMembers).toHaveBeenCalledTimes(2)
  })

  it('ignores stale realtime versions and applies a newer group update', () => {
    const store = useGroupsStore()
    store.groups = [group({ version: 5 })]

    store.handleGroupUpdated({
      group: { id: 9, name: '旧名称', avatar_url: '', owner_id: 7, member_count: 2, version: 4 }
    })
    expect(store.groups[0].name).toBe('周五夜调查局')

    store.handleGroupUpdated({
      group: { id: 9, name: '新名称', avatar_url: 'new.png', owner_id: 7, member_count: 3, version: 6 }
    })
    expect(store.groups[0]).toMatchObject({ name: '新名称', avatar_url: 'new.png', version: 6 })
  })

  it('removes the group and conversation immediately when the current user is removed', () => {
    const store = useGroupsStore()
    const chatStore = useChatStore()
    // 群页面从未打开时 Store 可能为空，但会话列表仍已由全局 IM 加载。
    store.groups = []
    store.selectedGroupId = 9
    chatStore.conversations = [
      {
        id: 41,
        type: 'group',
        peer: null,
        group: {
          id: 9,
          name: '周五夜调查局',
          avatar_url: '',
          current_user_role: 'owner',
          member_count: 2,
          version: 3
        },
        can_send: true,
        last_seq: 1,
        last_read_seq: 1,
        unread_count: 0,
        last_message: null,
        created_at: '2026-09-15T01:00:00Z',
        updated_at: '2026-09-15T01:00:00Z'
      }
    ]
    chatStore.selectedConversationId = 41

    store.handleMemberChanged({
      group_id: 9,
      event: 'member_removed',
      actor_user_id: 8,
      target_user_id: 7,
      version: 4
    })

    expect(store.groups).toEqual([])
    expect(store.selectedGroupId).toBeNull()
    expect(chatStore.conversations).toEqual([])
    expect(chatStore.selectedConversationId).toBeNull()
  })

  it('does not restore a departed group when the realtime leave event arrives before the API response', async () => {
    const store = useGroupsStore()
    store.groups = [group()]
    store.selectedGroupId = 9
    apiMocks.removeGroupMember.mockImplementation(async () => {
      store.handleMemberChanged({
        group_id: 9,
        event: 'member_left',
        actor_user_id: 7,
        target_user_id: 7,
        version: 4
      })
      return group({ current_user_role: null, member_count: 1, version: 4 })
    })

    await store.remove(7)

    expect(store.groups).toEqual([])
    expect(store.selectedGroupId).toBeNull()
  })

  it('never adds a group without current-user membership to the visible list', () => {
    const store = useGroupsStore()
    store.groups = [group()]
    // A leave response can arrive while the realtime event and route refresh are racing.
    apiMocks.listGroups.mockResolvedValue({
      items: [group({ current_user_role: null, member_count: 1, version: 4 })],
      next_cursor: null
    })
    return store.loadGroups().then(() => {
      expect(store.groups).toEqual([])
    })
  })

  it('refreshes authoritative data after a version conflict', async () => {
    const store = useGroupsStore()
    store.groups = [group()]
    store.selectedGroupId = 9
    apiMocks.updateGroup.mockRejectedValue({ response: { data: { code: 1806 } } })
    apiMocks.getGroup.mockResolvedValue(group({ name: '别人先改了', version: 4 }))

    await expect(store.updateProfile({ name: '我的修改' })).rejects.toBeTruthy()

    expect(apiMocks.getGroup).toHaveBeenCalledWith(9)
    expect(store.selectedGroup).toMatchObject({ name: '别人先改了', version: 4 })
  })
})
