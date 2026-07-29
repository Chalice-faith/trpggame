// @vitest-environment jsdom

import { flushPromises, mount } from '@vue/test-utils'
import ElementPlus from 'element-plus'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { ScriptDetail } from '@/api/scripts'
import ScriptDetailView from '@/views/ScriptDetailView.vue'

const { getScriptMock, retryScriptMock, deleteScriptMock } = vi.hoisted(() => ({
  getScriptMock: vi.fn(),
  retryScriptMock: vi.fn(),
  deleteScriptMock: vi.fn()
}))

vi.mock('vue-router', () => ({
  useRoute: () => ({ params: { id: '42' } }),
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() })
}))

vi.mock('@/api/scripts', async (importOriginal) => {
  const original = await importOriginal<typeof import('@/api/scripts')>()
  return {
    ...original,
    getScript: getScriptMock,
    retryScript: retryScriptMock,
    deleteScript: deleteScriptMock
  }
})

function scriptDetail(
  status: ScriptDetail['status'],
  parseError?: string
): ScriptDetail {
  return {
    id: 42,
    title: '雾都谜案',
    description: '测试剧本',
    cover_url: '',
    file_size: 1024,
    status,
    parse_error: parseError,
    chunk_count: status === 'ready' ? 8 : 0,
    created_at: '2026-07-29T10:00:00Z',
    updated_at: '2026-07-29T10:00:00Z',
    characters: []
  }
}

describe('ScriptDetailView', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.runOnlyPendingTimers()
    vi.useRealTimers()
  })

  it('polls while parsing and stops after reaching ready', async () => {
    getScriptMock
      .mockResolvedValueOnce(scriptDetail('parsing'))
      .mockResolvedValueOnce(scriptDetail('ready'))

    const wrapper = mount(ScriptDetailView, {
      global: { plugins: [ElementPlus] }
    })
    await flushPromises()
    expect(getScriptMock).toHaveBeenCalledTimes(1)

    await vi.advanceTimersByTimeAsync(3000)
    await flushPromises()
    expect(getScriptMock).toHaveBeenCalledTimes(2)
    expect(wrapper.text()).toContain('可使用')

    await vi.advanceTimersByTimeAsync(6000)
    await flushPromises()
    expect(getScriptMock).toHaveBeenCalledTimes(2)

    wrapper.unmount()
  })

  it('shows the parse error and retry action for a failed script', async () => {
    getScriptMock.mockResolvedValue(
      scriptDetail('failed', 'PDF 不含可提取文本')
    )

    const wrapper = mount(ScriptDetailView, {
      global: { plugins: [ElementPlus] }
    })
    await flushPromises()

    expect(wrapper.text()).toContain('解析错误')
    expect(wrapper.text()).toContain('PDF 不含可提取文本')
    expect(wrapper.text()).toContain('重新解析')

    wrapper.unmount()
  })
})
