import { beforeEach, describe, expect, it } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useGameStore } from '@/stores/game'

describe('game store', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('normalizes selected character attributes for the status panel', () => {
    const store = useGameStore()
    store.setPlayerStatusFromAttributes({
      hp: 8,
      max_hp: 12,
      mp: 4,
      san: 55,
      items: [{ name: '手电筒', quantity: 1, description: '电量不足' }],
      buffs: [{ name: '警觉', duration: 3 }],
      location: '旧宅门厅'
    })

    expect(store.playerStatus).toMatchObject({
      hp: 8,
      maxHp: 12,
      mp: 4,
      maxMp: 4,
      san: 55,
      maxSan: 55,
      location: '旧宅门厅'
    })
    expect(store.playerStatus?.items[0]).toMatchObject({ name: '手电筒', quantity: 1 })
    expect(store.playerStatus?.buffs[0]).toMatchObject({ name: '警觉', duration: 3 })
  })

  it('applies authoritative state, item and buff changes', () => {
    const store = useGameStore()
    store.setPlayerStatusFromAttributes({ hp: 10, items: ['钥匙'], buffs: [{ name: '中毒', duration: 2 }] })

    store.applyStatusUpdate({
      player_id: 7,
      changes: {
        player_state_changes: { hp: '6', location: '书房' },
        items: [
          { name: '钥匙', quantity_delta: -1 },
          { name: '手稿', quantity_delta: 2, description: '褪色的文字' }
        ],
        buffs: [
          { name: '中毒', duration: 0 },
          { name: '警觉', duration: 4 }
        ]
      }
    })

    expect(store.playerStatus?.hp).toBe(6)
    expect(store.playerStatus?.location).toBe('书房')
    expect(store.playerStatus?.items).toEqual([
      { name: '手稿', quantity: 2, description: '褪色的文字' }
    ])
    expect(store.playerStatus?.buffs).toEqual([{ name: '警觉', duration: 4 }])
  })
})
