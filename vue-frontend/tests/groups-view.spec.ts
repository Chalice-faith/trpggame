// @vitest-environment jsdom

import { flushPromises, mount } from '@vue/test-utils'
import ElementPlus from 'element-plus'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import GroupsView from '@/views/GroupsView.vue'
import { useAuthStore } from '@/stores/auth'

const routeState = vi.hoisted(() => ({ params: { groupId: '9' as string | undefined } }))
const routerMocks = vi.hoisted(() => ({ push: vi.fn(), replace: vi.fn() }))
const groupApi = vi.hoisted(() => ({
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
const friendApi = vi.hoisted(() => ({ listFriends: vi.fn() }))

vi.mock('vue-router', () => ({
  useRoute: () => routeState,
  useRouter: () => routerMocks
}))

vi.mock('@/api/groups', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/api/groups')>()),
  ...groupApi
}))

vi.mock('@/api/friends', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/api/friends')>()),
  ...friendApi
}))

const group = {
  id: 9,
  name: '周五夜调查局',
  avatar_url: '',
  owner_id: 7,
  conversation_id: 41,
  current_user_role: 'owner' as const,
  member_count: 2,
  version: 3,
  created_at: '2026-09-15T01:00:00Z',
  updated_at: '2026-09-15T01:00:00Z'
}

describe('GroupsView', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    routeState.params.groupId = '9'
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
    groupApi.listGroups.mockResolvedValue({ items: [group], next_cursor: null })
    groupApi.getGroup.mockResolvedValue(group)
    groupApi.listGroupMembers.mockResolvedValue({
      items: [
        {
          id: 2,
          user: { id: 8, username: 'player08', nickname: '调查员', avatar_url: '' },
          role: 'member',
          joined_at: '2026-09-15T01:01:00Z'
        },
        {
          id: 1,
          user: { id: 7, username: 'player07', nickname: '守秘人', avatar_url: '' },
          role: 'owner',
          joined_at: '2026-09-15T01:00:00Z'
        }
      ],
      next_cursor: null
    })
    friendApi.listFriends.mockResolvedValue({ items: [], next_cursor: null })
  })

  it('renders owner member-management actions and the group chat entry', async () => {
    const wrapper = mount(GroupsView, {
      attachTo: document.body,
      global: { plugins: [ElementPlus] }
    })
    await flushPromises()

    expect(wrapper.text()).toContain('周五夜调查局')
    expect(wrapper.findAll('[data-testid="group-member-row"]')).toHaveLength(2)
    expect(wrapper.find('[data-testid="toggle-role-button"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="remove-member-button"]').exists()).toBe(true)

    const chatButton = wrapper.findAll('button').find((button) => button.text().includes('进入群聊'))!
    await chatButton.trigger('click')
    expect(routerMocks.push).toHaveBeenCalledWith('/chat/41')
    wrapper.unmount()
  })

  it('creates a group once and navigates to it', async () => {
    routeState.params.groupId = undefined
    groupApi.createGroup.mockResolvedValue({ ...group, id: 10, conversation_id: 42, name: '新冒险队' })
    groupApi.getGroup.mockResolvedValue({ ...group, id: 10, conversation_id: 42, name: '新冒险队' })
    const wrapper = mount(GroupsView, {
      attachTo: document.body,
      global: { plugins: [ElementPlus] }
    })
    await flushPromises()

    await wrapper.get('[data-testid="create-group-open"]').trigger('click')
    await flushPromises()
    const input = document.body.querySelector('[data-testid="create-group-name"]') as HTMLInputElement
    input.value = '新冒险队'
    input.dispatchEvent(new Event('input'))
    await flushPromises()
    const submit = document.body.querySelector('[data-testid="create-group-submit"]') as HTMLButtonElement
    submit.click()
    await flushPromises()

    expect(groupApi.createGroup).toHaveBeenCalledTimes(1)
    expect(groupApi.createGroup).toHaveBeenCalledWith('新冒险队', '')
    expect(routerMocks.push).toHaveBeenCalledWith('/groups/10')
    wrapper.unmount()
  })
})
