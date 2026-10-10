// @vitest-environment jsdom

import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { getGameMemoryStatus, type GameMemoryStatus } from '@/api/game'
import { useMemoryStore } from '@/stores/memory'

vi.mock('@/api/game', () => ({ getGameMemoryStatus: vi.fn() }))

const branch = (status: GameMemoryStatus['status'], roomId = 41): GameMemoryStatus => ({
  room_id: roomId, enabled: true, status,
  timeline_id: '550e8400-e29b-41d4-a716-446655440000',
  generation: '550e8400-e29b-41d4-a716-446655440001',
  head_position: 2, durable_position: status === 'ready' ? 2 : 1, revision: 2
})

describe('game memory status store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('holds actions during archive recovery and exposes expectations only when ready', async () => {
    const store = useMemoryStore()
    vi.mocked(getGameMemoryStatus).mockResolvedValueOnce(branch('pending')).mockResolvedValueOnce(branch('ready'))
    await store.refresh(41)
    expect(store.ready).toBe(false)
    expect(store.expected).toBeUndefined()
    expect(store.hint).toContain('归档')
    await store.refresh(41)
    expect(store.ready).toBe(true)
    expect(store.expected).toEqual({ timelineId: branch('ready').timeline_id, generation: branch('ready').generation })
  })

  it('does not accept a late response from a different room', async () => {
    const store = useMemoryStore()
    let completeOld!: (value: GameMemoryStatus) => void
    vi.mocked(getGameMemoryStatus)
      .mockImplementationOnce(() => new Promise(resolve => { completeOld = resolve }))
      .mockResolvedValueOnce({ room_id: 42, enabled: false, status: 'disabled' })
    const old = store.refresh(41)
    await store.refresh(42)
    completeOld(branch('ready'))
    await old
    expect(store.status?.room_id).toBe(42)
    expect(store.ready).toBe(true)
    expect(store.expected).toBeUndefined()
  })

  it('fails closed when the authority cannot be queried', async () => {
    const store = useMemoryStore()
    vi.mocked(getGameMemoryStatus).mockResolvedValueOnce(branch('ready')).mockRejectedValueOnce(new Error('offline'))
    await store.refresh(41)
    await expect(store.refresh(41)).rejects.toThrow('offline')
    expect(store.status).toBeNull()
    expect(store.ready).toBe(false)
  })
})
