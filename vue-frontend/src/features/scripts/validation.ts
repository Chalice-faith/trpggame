export const MAX_SCRIPT_UPLOAD_BYTES = 50 * 1024 * 1024

export async function validateScriptPDF(file: File): Promise<string> {
  if (!file.name.toLowerCase().endsWith('.pdf')) {
    return '请选择 PDF 文件'
  }
  if (file.size <= 0) {
    return '文件内容不能为空'
  }
  if (file.size > MAX_SCRIPT_UPLOAD_BYTES) {
    return 'PDF 文件不能超过 50 MB'
  }

  const signature = new TextDecoder('ascii').decode(
    await file.slice(0, 5).arrayBuffer()
  )
  if (signature !== '%PDF-') {
    return '文件内容不是有效的 PDF'
  }
  return ''
}
