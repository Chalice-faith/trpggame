import { describe, expect, it } from 'vitest'

import {
  MAX_SCRIPT_UPLOAD_BYTES,
  validateScriptPDF
} from '@/features/scripts/validation'

function fileOf(
  name: string,
  content: BlobPart,
  options: FilePropertyBag = { type: 'application/pdf' }
) {
  return new File([content], name, options)
}

describe('validateScriptPDF', () => {
  it('accepts a PDF extension and signature', async () => {
    await expect(
      validateScriptPDF(fileOf('adventure.PDF', '%PDF-1.7\ncontent'))
    ).resolves.toBe('')
  })

  it('rejects a non-PDF extension', async () => {
    await expect(
      validateScriptPDF(fileOf('adventure.txt', '%PDF-1.7'))
    ).resolves.toBe('请选择 PDF 文件')
  })

  it('rejects an empty file', async () => {
    await expect(validateScriptPDF(fileOf('empty.pdf', ''))).resolves.toBe(
      '文件内容不能为空'
    )
  })

  it('rejects a file over 50 MiB before reading its signature', async () => {
    const oversized = fileOf('oversized.pdf', '%PDF-')
    Object.defineProperty(oversized, 'size', {
      value: MAX_SCRIPT_UPLOAD_BYTES + 1
    })

    await expect(validateScriptPDF(oversized)).resolves.toBe(
      'PDF 文件不能超过 50 MB'
    )
  })

  it('rejects a forged PDF extension', async () => {
    await expect(
      validateScriptPDF(fileOf('forged.pdf', 'plain text'))
    ).resolves.toBe('文件内容不是有效的 PDF')
  })
})
