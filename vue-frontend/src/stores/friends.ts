import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import {
  acceptFriendRequest,
  deleteFriend as deleteFriendApi,
  listFriendRequests,
  listFriends,
  rejectFriendRequest,
  searchUsers as searchUsersApi,
  sendFriendRequest,
  type FriendItem,
  type FriendRequestItem,
  type PresenceStatus,
  type UserSearchItem
} from '@/api/friends'

const presenceRank: Record<PresenceStatus, number> = {
  online: 0,
  unknown: 1,
  offline: 2
}

function displayName(item: FriendItem): string {
  return item.peer.nickname.trim() || item.peer.username
}

export const useFriendsStore = defineStore('friends', () => {
  const searchResults = ref<UserSearchItem[]>([])
  const incomingRequests = ref<FriendRequestItem[]>([])
  const outgoingRequests = ref<FriendRequestItem[]>([])
  const friends = ref<FriendItem[]>([])
  const loading = ref(false)
  const searching = ref(false)
  const errorMessage = ref('')

  const sortedFriends = computed(() =>
    [...friends.value].sort((left, right) => {
      const statusDifference =
        presenceRank[left.presence] - presenceRank[right.presence]
      if (statusDifference !== 0) return statusDifference
      const nameDifference = displayName(left).localeCompare(
        displayName(right),
        'zh-CN'
      )
      return nameDifference || left.peer.id - right.peer.id
    })
  )

  async function searchUsers(keyword: string) {
    const normalized = keyword.trim()
    if (!normalized) {
      searchResults.value = []
      return
    }
    searching.value = true
    errorMessage.value = ''
    try {
      searchResults.value = await searchUsersApi(normalized)
    } finally {
      searching.value = false
    }
  }

  async function sendRequest(targetUserId: number) {
    const request = await sendFriendRequest(targetUserId)
    upsertRequest(outgoingRequests.value, request)
    const result = searchResults.value.find(
      ({ user }) => user.id === targetUserId
    )
    if (result) {
      result.friendship = {
        id: request.id,
        status: request.status,
        direction: 'outgoing'
      }
    }
  }

  async function fetchRequests(direction: 'incoming' | 'outgoing') {
    const target = direction === 'incoming' ? incomingRequests : outgoingRequests
    const result: FriendRequestItem[] = []
    let cursor: string | undefined
    do {
      const page = await listFriendRequests(direction, cursor)
      result.push(...(page.items ?? []))
      cursor = page.next_cursor || undefined
    } while (cursor)
    target.value = result
  }

  async function fetchFriends() {
    const result: FriendItem[] = []
    let cursor: string | undefined
    do {
      const page = await listFriends(cursor)
      result.push(...(page.items ?? []))
      cursor = page.next_cursor || undefined
    } while (cursor)
    friends.value = result
  }

  async function refreshAll() {
    loading.value = true
    errorMessage.value = ''
    try {
      await Promise.all([
        fetchFriends(),
        fetchRequests('incoming'),
        fetchRequests('outgoing')
      ])
    } catch (error: any) {
      errorMessage.value =
        error?.response?.data?.message || '好友数据加载失败，请稍后重试'
      throw error
    } finally {
      loading.value = false
    }
  }

  async function acceptRequest(requestId: number) {
    await acceptFriendRequest(requestId)
    await refreshAll()
  }

  async function rejectRequest(requestId: number) {
    await rejectFriendRequest(requestId)
    incomingRequests.value = incomingRequests.value.filter(
      ({ id }) => id !== requestId
    )
  }

  async function removeFriend(friendUserId: number) {
    await deleteFriendApi(friendUserId)
    friends.value = friends.value.filter(
      ({ peer }) => peer.id !== friendUserId
    )
  }

  function applyPresence(userId: number, status: PresenceStatus) {
    const friend = friends.value.find(({ peer }) => peer.id === userId)
    if (friend) friend.presence = status
  }

  async function handleFriendshipUpdated() {
    await refreshAll()
  }

  function clear() {
    searchResults.value = []
    incomingRequests.value = []
    outgoingRequests.value = []
    friends.value = []
    errorMessage.value = ''
    loading.value = false
    searching.value = false
  }

  return {
    searchResults,
    incomingRequests,
    outgoingRequests,
    friends,
    sortedFriends,
    loading,
    searching,
    errorMessage,
    searchUsers,
    sendRequest,
    fetchRequests,
    fetchFriends,
    refreshAll,
    acceptRequest,
    rejectRequest,
    removeFriend,
    applyPresence,
    handleFriendshipUpdated,
    clear
  }
})

function upsertRequest(
  requests: FriendRequestItem[],
  request: FriendRequestItem
) {
  const index = requests.findIndex(({ id }) => id === request.id)
  if (index >= 0) requests[index] = request
  else requests.unshift(request)
}
