import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import {
  createGroup as createGroupApi,
  getGroup,
  inviteGroupMembers,
  listGroupMembers,
  listGroups,
  removeGroupMember,
  setGroupMemberRole,
  transferGroupOwner,
  updateGroup,
  type GroupMember,
  type GroupMemberChangedEventData,
  type GroupRole,
  type GroupSummary,
  type GroupUpdatedEventData
} from '@/api/groups'
import type { ConversationGroupSummary } from '@/api/chat'
import { useAuthStore } from '@/stores/auth'
import { useChatStore } from '@/stores/chat'

export const useGroupsStore = defineStore('groups', () => {
  const groups = ref<GroupSummary[]>([])
  const membersByGroup = ref<Record<number, GroupMember[]>>({})
  const selectedGroupId = ref<number | null>(null)
  const nextCursor = ref('')
  const loading = ref(false)
  const loadingMembers = ref(false)
  const mutating = ref(false)
  const errorMessage = ref('')

  const selectedGroup = computed(() =>
    groups.value.find(({ id }) => id === selectedGroupId.value) ?? null
  )
  const selectedMembers = computed(() =>
    selectedGroupId.value ? membersByGroup.value[selectedGroupId.value] ?? [] : []
  )

  async function loadGroups(reset = true) {
    loading.value = true
    errorMessage.value = ''
    try {
      const page = await listGroups(reset ? undefined : nextCursor.value)
      if (reset) groups.value = []
      for (const group of page.items ?? []) upsertGroup(group)
      nextCursor.value = page.next_cursor ?? ''
    } catch (error: any) {
      errorMessage.value = error?.response?.data?.message || '群组列表加载失败'
      throw error
    } finally {
      loading.value = false
    }
  }

  async function loadMoreGroups() {
    if (nextCursor.value) await loadGroups(false)
  }

  async function openGroup(groupId: number) {
    selectedGroupId.value = groupId
    const [group] = await Promise.all([getGroup(groupId), loadAllMembers(groupId)])
    if (selectedGroupId.value !== groupId) return
    upsertGroup(group)
  }

  async function loadAllMembers(groupId: number) {
    loadingMembers.value = true
    try {
      const members: GroupMember[] = []
      let cursor: string | undefined
      do {
        const page = await listGroupMembers(groupId, cursor)
        members.push(...(page.items ?? []))
        cursor = page.next_cursor || undefined
      } while (cursor)
      membersByGroup.value[groupId] = members
    } finally {
      loadingMembers.value = false
    }
  }

  async function create(name: string, avatarUrl = '') {
    return mutate(async () => {
      const group = await createGroupApi(name.trim(), avatarUrl.trim())
      upsertGroup(group)
      await openGroup(group.id)
      return group
    })
  }

  async function updateProfile(changes: { name?: string; avatar_url?: string }) {
    return mutateSelected((group) => updateGroup(group.id, group.version, changes), false)
  }

  async function invite(userIds: number[]) {
    return mutateSelected(
      (group) => inviteGroupMembers(group.id, userIds, group.version),
      true
    )
  }

  async function setRole(userId: number, role: Exclude<GroupRole, 'owner'>) {
    return mutateSelected(
      (group) => setGroupMemberRole(group.id, userId, role, group.version),
      true
    )
  }

  async function remove(userId: number) {
    const currentUserId = useAuthStore().user?.id
    const groupId = selectedGroupId.value
    const result = await mutateSelected(
      (group) => removeGroupMember(group.id, userId, group.version),
      userId !== currentUserId
    )
    if (userId === currentUserId && groupId) {
      removeLocalGroup(groupId)
    }
    return result
  }

  async function transfer(newOwnerUserId: number) {
    return mutateSelected(
      (group) => transferGroupOwner(group.id, newOwnerUserId, group.version),
      true
    )
  }

  async function mutateSelected(
    operation: (group: GroupSummary) => Promise<GroupSummary>,
    reloadMembers: boolean
  ) {
    const group = selectedGroup.value
    if (!group) throw new Error('group not selected')
    return mutate(async () => {
      try {
        const updated = await operation(group)
        upsertGroup(updated)
        if (reloadMembers && updated.current_user_role) await loadAllMembers(updated.id)
        return updated
      } catch (error: any) {
        if (error?.response?.data?.code === 1806) {
          await refreshGroup(group.id)
        }
        throw error
      }
    })
  }

  async function mutate<T>(operation: () => Promise<T>) {
    if (mutating.value) throw new Error('group mutation in progress')
    mutating.value = true
    try {
      return await operation()
    } finally {
      mutating.value = false
    }
  }

  async function refreshGroup(groupId: number) {
    try {
      const group = await getGroup(groupId)
      upsertGroup(group)
      if (selectedGroupId.value === groupId) await loadAllMembers(groupId)
    } catch (error: any) {
      if (error?.response?.data?.code === 1801) removeLocalGroup(groupId)
      else throw error
    }
  }

  function handleGroupUpdated(data: GroupUpdatedEventData) {
    const incoming = data?.group
    if (!incoming?.id) return
    const current = groups.value.find(({ id }) => id === incoming.id)
    if (current && incoming.version <= current.version) return
    if (!current) {
      void loadGroups(true).catch(() => undefined)
      return
    }
    Object.assign(current, incoming)
  }

  function handleMemberChanged(data: GroupMemberChangedEventData) {
    if (!data?.group_id || !data.version) return
    const currentUserId = useAuthStore().user?.id
    const removedCurrentUser =
      data.target_user_id === currentUserId &&
      (data.event === 'member_removed' || data.event === 'member_left')
    if (removedCurrentUser) {
      removeLocalGroup(data.group_id)
      return
    }
    const current = groups.value.find(({ id }) => id === data.group_id)
    if (current && data.version <= current.version) return
    void refreshGroup(data.group_id).catch(() => undefined)
  }

  function applyConversationGroup(group: ConversationGroupSummary) {
    const current = groups.value.find(({ id }) => id === group.id)
    if (!current || group.version <= current.version) return
    current.name = group.name
    current.avatar_url = group.avatar_url
    current.current_user_role = group.current_user_role
    current.member_count = group.member_count
    current.version = group.version
  }

  function upsertGroup(group: GroupSummary) {
    if (!group.current_user_role) {
      removeLocalGroup(group.id)
      return
    }
    const index = groups.value.findIndex(({ id }) => id === group.id)
    if (index >= 0) {
      if (group.version >= groups.value[index].version) groups.value[index] = group
    } else groups.value.push(group)
    groups.value.sort((left, right) => right.id - left.id)
  }

  function removeLocalGroup(groupId: number) {
    const group = groups.value.find(({ id }) => id === groupId)
    const chatStore = useChatStore()
    const conversationId = group?.conversation_id ?? chatStore.conversations.find(
      (conversation) => conversation.type === 'group' && conversation.group.id === groupId
    )?.id
    groups.value = groups.value.filter(({ id }) => id !== groupId)
    delete membersByGroup.value[groupId]
    if (selectedGroupId.value === groupId) selectedGroupId.value = null
    if (conversationId) chatStore.removeConversation(conversationId)
  }

  function clear() {
    groups.value = []
    membersByGroup.value = {}
    selectedGroupId.value = null
    nextCursor.value = ''
    loading.value = false
    loadingMembers.value = false
    mutating.value = false
    errorMessage.value = ''
  }

  return {
    groups,
    membersByGroup,
    selectedGroupId,
    nextCursor,
    loading,
    loadingMembers,
    mutating,
    errorMessage,
    selectedGroup,
    selectedMembers,
    loadGroups,
    loadMoreGroups,
    openGroup,
    create,
    updateProfile,
    invite,
    setRole,
    remove,
    transfer,
    refreshGroup,
    handleGroupUpdated,
    handleMemberChanged,
    applyConversationGroup,
    clear
  }
})
