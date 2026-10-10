// @vitest-environment jsdom

import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { getMultiplayerGameState, type MultiplayerGameState } from '@/api/game'
import { useMultiplayerStore } from '@/stores/multiplayer'

vi.mock('@/api/game', () => ({ getMultiplayerGameState: vi.fn() }))

function state(overrides: Partial<MultiplayerGameState> = {}): MultiplayerGameState {
  return {
    seq: 5, version: 2, room_id: 41, status: 'playing', generation: 'generation-a',
    current_turn: 0, round_number: 0, turn_order: [7, 8], current_actor_id: 7,
    deadline_at: '2026-09-23T10:01:00Z', summary_memory: 'A door opens.',
    players: [
      { user_id: 7, character_id: 101, player_state: { hp: '10' }, items: [], buffs: [] },
      { user_id: 8, character_id: 102, player_state: { hp: '8' }, items: [], buffs: [] }
    ],
    recent_messages: [{ role: 'assistant', content: 'A door opens.' }],
    ...overrides
  }
}

describe('multiplayer game store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('keeps chunks temporary and discards them after cancellation or generation change', () => {
    const store = useMultiplayerStore()
    store.applySnapshot(state())
    store.handleEvent('action_started', { generation: 'generation-a', current_turn: 0 }, 'request-1', 6)
    store.handleEvent('narrative_chunk', { generation: 'generation-a', current_turn: 0, content: 'Not committed' }, 'request-1', 7)
    expect(store.draft).toBe('Not committed')
    expect(store.history).toHaveLength(1)

    store.handleEvent('action_cancelled', { generation: 'generation-a', current_turn: 0 }, 'request-1', 8)
    expect(store.draft).toBe('')
    expect(store.history).toHaveLength(1)

    store.applySnapshot(state({ seq: 10, generation: 'generation-b', status: 'paused', deadline_at: null }))
    store.handleEvent('narrative_chunk', { generation: 'generation-a', current_turn: 0, content: 'Old timeline' }, 'request-1', 11)
    expect(store.draft).toBe('')
    expect(store.history).toHaveLength(1)
  })

  it('refreshes authoritative state after a committed action and rejects older snapshots', async () => {
    const store = useMultiplayerStore()
    store.applySnapshot(state())
    vi.mocked(getMultiplayerGameState).mockResolvedValue(state({
      seq: 9, current_turn: 1, current_actor_id: 8,
      recent_messages: [
        { role: 'assistant', content: 'A door opens.' },
        { role: 'user', content: 'Inspect the room' },
        { role: 'assistant', content: 'You see a key.' }
      ]
    }))
    store.handleEvent('action_started', { generation: 'generation-a', current_turn: 0 }, 'request-2', 6)
    store.handleEvent('narrative_chunk', { generation: 'generation-a', current_turn: 0, content: 'You see' }, 'request-2', 7)
    store.handleEvent('narrative_complete', { generation: 'generation-a', narrative: 'You see a key.', current_turn: 1 }, 'request-2', 8)
    await vi.waitFor(() => expect(store.currentTurn).toBe(1))
    expect(store.draft).toBe('')
    expect(store.history.at(-1)?.content).toBe('You see a key.')
    store.applySnapshot(state({ seq: 7 }))
    expect(store.currentTurn).toBe(1)
  })

  it('clears a pending action and deadline when the host pauses', () => {
    const store = useMultiplayerStore()
    store.applySnapshot(state())
    store.markSubmitted('request-3')
    vi.mocked(getMultiplayerGameState).mockResolvedValue(state({ seq: 7, status: 'paused', generation: 'generation-b', deadline_at: null }))
    store.handleEvent('game_status_changed', {
      generation: 'generation-b', status: 'paused', current_turn: 0,
      round_number: 0, current_actor_id: 7, deadline_at: null
    }, '', 6)
    expect(store.status).toBe('paused')
    expect(store.deadlineAt).toBeNull()
    expect(store.actionPending).toBe(false)
  })
})
