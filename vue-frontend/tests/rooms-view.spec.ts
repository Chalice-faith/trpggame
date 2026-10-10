// @vitest-environment jsdom

import { flushPromises, mount } from '@vue/test-utils'
import ElementPlus from 'element-plus'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import RoomsView from '@/views/RoomsView.vue'
import { useAuthStore } from '@/stores/auth'

const routerMocks = vi.hoisted(() => ({ push: vi.fn() }))
const roomApi = vi.hoisted(() => ({
  createRoom: vi.fn(),
  getRoom: vi.fn(),
  joinRoom: vi.fn(),
  leaveRoom: vi.fn(),
  listRooms: vi.fn(),
  removeRoomMember: vi.fn(),
  selectRoomCharacter: vi.fn(),
  setRoomReady: vi.fn(),
  startRoom: vi.fn(),
  transferRoomOwner: vi.fn()
}))
const scriptApi = vi.hoisted(() => ({ listScripts: vi.fn() }))

vi.mock('vue-router', () => ({ useRouter: () => routerMocks }))
vi.mock('@/api/rooms', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/api/rooms')>()),
  ...roomApi
}))
vi.mock('@/api/scripts', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/api/scripts')>()),
  ...scriptApi
}))

const room = {
  id: 41,
  name: '迷雾庄园',
  script_id: 9,
  owner_id: 7,
  status: 'waiting' as const,
  max_players: 4,
  room_code: 'A7K9M2QX',
  version: 3,
  created_at: '2026-09-21T03:00:00Z',
  members: [],
  characters: []
}

describe('RoomsView', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    setActivePinia(createPinia())
    const authStore = useAuthStore()
    authStore.user = {
      id: 7,
      username: 'keeper',
      email: 'keeper@example.test',
      nickname: '守秘人',
      avatar_url: ''
    }
    authStore.accessToken = 'access-token'
    vi.clearAllMocks()
    roomApi.listRooms.mockResolvedValue([room])
    scriptApi.listScripts.mockResolvedValue({
      items: [{
        id: 9,
        title: '迷雾庄园',
        description: '',
        cover_url: '',
        file_size: 100,
        status: 'ready',
        chunk_count: 10,
        created_at: '2026-09-21T02:00:00Z',
        updated_at: '2026-09-21T02:00:00Z'
      }],
      total: 1,
      page: 1,
      page_size: 100
    })
  })

  it('lists active rooms and opens the selected lobby', async () => {
    const wrapper = mount(RoomsView, {
      attachTo: document.body,
      global: { plugins: [ElementPlus] }
    })
    await flushPromises()

    expect(wrapper.text()).toContain('迷雾庄园')
    await wrapper.get('[data-testid="room-card"]').trigger('click')
    expect(routerMocks.push).toHaveBeenCalledWith('/lobby/41')
    wrapper.unmount()
  })

  it('joins with a normalized room code and navigates to the lobby', async () => {
    roomApi.joinRoom.mockResolvedValue(room)
    const wrapper = mount(RoomsView, {
      attachTo: document.body,
      global: { plugins: [ElementPlus] }
    })
    await flushPromises()

    await wrapper.get('[data-testid="join-room-open"]').trigger('click')
    await flushPromises()
    const input = document.body.querySelector('[data-testid="join-room-code"]') as HTMLInputElement
    input.value = 'a7k9m2qx'
    input.dispatchEvent(new Event('input'))
    await flushPromises()
    const submit = document.body.querySelector('[data-testid="join-room-submit"]') as HTMLButtonElement
    submit.click()
    await flushPromises()

    expect(roomApi.joinRoom).toHaveBeenCalledWith('A7K9M2QX')
    expect(routerMocks.push).toHaveBeenCalledWith('/lobby/41')
    wrapper.unmount()
  })
})
