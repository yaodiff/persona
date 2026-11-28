import { appendFileSync, readFileSync } from 'node:fs'
import { createInterface } from 'node:readline'

if (process.argv.includes('models')) { console.log('test-model\tTest Model'); process.exit(0) }

const directory = process.argv[2]
const sessionId = '11111111-2222-4333-8444-555555555555'
const send = (value) => process.stdout.write(`${JSON.stringify({ jsonrpc: '2.0', ...value })}\n`)
let prompt
let rejected = false
let consumed = 0

const timer = setInterval(() => {
  const commands = readFileSync(`${directory}/updates.jsonl`, 'utf8').split('\n').filter(Boolean)
  for (const line of commands.slice(consumed)) {
    const { sessionId: target = sessionId, ...update } = JSON.parse(line)
    if (update.exit) process.exit(0)
    if (update.permission) {
      send({ id: update.permission, method: 'session/request_permission', params: {
        sessionId: target,
        toolCall: { toolCallId: 'read', title: 'read_file', rawInput: { Path: 'result.txt' } },
        options: [{ optionId: 'allow', kind: 'allow_once' }],
      } })
    } else if (update.end) {
      send({ id: prompt, result: { stopReason: 'end_turn' } })
      prompt = undefined
    } else {
      send({ method: 'session/update', params: { sessionId: target, update } })
    }
  }
  consumed = commands.length
}, 10)

const input = createInterface({ input: process.stdin })
input.on('line', (line) => {
  const request = JSON.parse(line)
  appendFileSync(`${directory}/calls.jsonl`, `${line}\n`)
  if (request.method === 'initialize') {
    send({ id: request.id, result: { agentCapabilities: { sessionCapabilities: { additionalDirectories: true } } } })
    return
  }
  if (request.method === 'session/set_model' && request.params.modelId === 'invalid-model') {
    send({ id: request.id, error: { code: -32000, message: 'Unknown model: invalid-model' } })
    return
  }
  if (request.method === 'session/prompt') {
    const text = request.params.prompt?.[0]?.text
    if (text === 'invalid-always' || (text === 'invalid-once' && !rejected)) {
      rejected = true
      send({ id: request.id, error: { code: -32000, message: 'Invalid session' } })
    } else prompt = request.id
    return
  }
  if (request.id !== undefined) send({ id: request.id, result: request.method === 'session/new' ? { sessionId } : {} })
})
input.on('close', () => { clearInterval(timer); process.exit(0) })
