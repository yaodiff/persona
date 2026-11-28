import { randomUUID } from 'node:crypto'
import { appendFileSync, existsSync, readFileSync, writeFileSync } from 'node:fs'
import { createServer } from 'node:http'
import { createInterface } from 'node:readline'

if (process.argv.includes('models')) { console.log('test-model\tTest Model'); process.exit(0) }

const directory = process.argv[2]
const log = process.argv[process.argv.indexOf('--log-file') + 1]
const calls = `${directory}/native-calls.jsonl`
appendFileSync(`${directory}/acp-starts.jsonl`, `${JSON.stringify({ pid: process.pid })}\n`)
const server = createServer(async (req, res) => {
  // Agent port discovery probes listeners with GET / (User-Agent persona-ports, docs/lessons/testing.md); it is not a
  // native call and must not kill the fake.
  if (req.method !== 'POST') {
    res.writeHead(404).end('{"message":"Unknown native method"}')
    return
  }
  const chunks = []
  for await (const chunk of req) chunks.push(chunk)
  const params = JSON.parse(Buffer.concat(chunks).toString())
  const method = req.url.split('/').at(-1)
  appendFileSync(calls, JSON.stringify({ method, params, worker: process.pid }) + '\n')
  if (method === 'RetrieveUserQuotaSummary') {
    res.end(JSON.stringify({ response: JSON.parse(readFileSync(`${directory}/quota.json`, 'utf8')) }))
    return
  }
  if (method === 'GetUserStatus') {
    const statusPath = `${directory}/user-status.json`
    if (existsSync(statusPath)) {
      const payload = JSON.parse(readFileSync(statusPath, 'utf8'))
      if (payload?.httpStatus) {
        res.writeHead(payload.httpStatus).end(JSON.stringify({ message: payload.message || 'status error' }))
        return
      }
      res.end(JSON.stringify(payload))
      return
    }
  }
  res.writeHead(404).end('{"message":"Unknown native method"}')
})
await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve))
writeFileSync(log, `Language server listening on random port at ${server.address().port} for HTTP\n`)
createInterface({ input: process.stdin }).on('line', (line) => {
  if (!line.trim()) return
  const req = JSON.parse(line)
  if (req.id === undefined) return
  if (req.method === 'session/new') {
    process.stdout.write(JSON.stringify({
      jsonrpc: '2.0', id: req.id,
      result: { sessionId: randomUUID(), models: { currentModelId: 'test-model', availableModels: [{ modelId: 'test-model', name: 'Test Model' }] } },
    }) + '\n')
    return
  }
  if (req.method === 'session/prompt') {
    process.stdout.write(JSON.stringify({
      jsonrpc: '2.0', method: 'session/update',
      params: { sessionId: req.params?.sessionId, update: { sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: 'ok' } } },
    }) + '\n')
    process.stdout.write(JSON.stringify({ jsonrpc: '2.0', id: req.id, result: { stopReason: 'end_turn' } }) + '\n')
    return
  }
  process.stdout.write(JSON.stringify({ jsonrpc: '2.0', id: req.id, result: {} }) + '\n')
})
process.stdin.on('end', () => server.close(() => process.exit(0)))
