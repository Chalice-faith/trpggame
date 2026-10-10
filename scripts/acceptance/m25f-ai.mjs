// Deterministic local AI fixture for M2.5-F browser acceptance only.
import { createServer } from 'node:http'

const port = Number(process.env.M25F_AI_PORT || 18000)

const server = createServer(async (request, response) => {
  if (request.method === 'GET' && request.url === '/health') {
    response.writeHead(200, { 'Content-Type': 'application/json' })
    response.end('{"status":"ok"}')
    return
  }
  if (request.method !== 'POST') {
    response.writeHead(404).end()
    return
  }
  let body = ''
  for await (const chunk of request) body += chunk
  let input
  try { input = JSON.parse(body) } catch { response.writeHead(400).end(); return }

  if (request.url === '/api/v1/ai/inference/start') {
    response.writeHead(200, { 'Content-Type': 'application/json' })
    response.end(JSON.stringify({ narrative: `验收剧本开场：${input.participants?.length || 1} 位玩家进入钟楼。` }))
    return
  }
  if (request.url === '/api/v1/ai/inference/action/stream') {
    const action = String(input.action || '')
    const narrative = `主持人确认 ${input.user_id} 的行动：${action}。钟楼的机关发出轻响。`
    response.writeHead(200, { 'Content-Type': 'application/x-ndjson; charset=utf-8' })
    response.write(JSON.stringify({ type: 'narrative_chunk', content: '钟楼的机关发出' }) + '\n')
    await new Promise((resolve) => setTimeout(resolve, action.includes('慢速') ? 20000 : 250))
    if (response.destroyed) return
    response.write(JSON.stringify({ type: 'narrative_chunk', content: '轻响。' }) + '\n')
    response.end(JSON.stringify({ type: 'complete', narrative }) + '\n')
    return
  }
  response.writeHead(404).end()
})

server.listen(port, '127.0.0.1', () => {
  process.stdout.write(`M2.5-F acceptance AI listening on 127.0.0.1:${port}\n`)
})
