// A fake `agy` whose `acp` process keeps the account it started with, as agy acp does after the plan lapses (#10368).
// Usage: node fake-acp-stale.mjs <directory> models | acp [...]
//   <directory>/account.json  the CLI's account now: {models: [{modelId, name}], rejects?: [modelId]} or {error}
//   <directory>/hold          while it exists, session/prompt does not end
//   <directory>/calls.jsonl   every request an acp process receives, with its pid
//   <directory>/models-hold   while it exists, `agy models` does not answer; each run adds a line to discoveries
//   <directory>/native.json   the steps the native service returns; native-hold holds its answers, native.jsonl logs them
//   <directory>/overlaps      an acp process that starts while an earlier one is still running says so here
// With slowExit in the account it started with, an acp process takes 500 ms to exit once told to.
// With an id in the account it started with, an acp process replies "OK (<id>)"; with quota "exhausted" it answers every
// prompt with agy's streamed quota error line instead, as agy acp does for an account out of quota (#17860).
// `agy models` reads the account each time. An acp process reads it once when it starts, refuses models it did not
// have then (and the IDs in `rejects`) as unknown, and stays signed out from the first request that finds the CLI so.
import { appendFileSync, existsSync, readFileSync, writeFileSync } from 'node:fs'
import { randomUUID } from 'node:crypto'
import { createServer } from 'node:http'
import { join } from 'node:path'
import { createInterface } from 'node:readline'

const [directory, command] = process.argv.slice(2)
const account = () => JSON.parse(readFileSync(join(directory, 'account.json'), 'utf8'))
const released = (file) => new Promise((resolve) => {
  const wait = setInterval(() => {
    if (existsSync(join(directory, file))) return
    clearInterval(wait)
    resolve()
  }, 20)
})

if (command === 'models') {
  appendFileSync(join(directory, 'discoveries'), `${process.pid}\n`)
  await released('models-hold')
  const now = account()
  if (now.error) {
    console.error(now.error)
    process.exit(1)
  }
  console.error('Fetching available models...')
  for (const model of now.models) console.log(`${model.modelId}\t${model.name}`)
  process.exit(0)
}

// The native conversation service: its port goes to the private log, as agy writes it.
const service = createServer(async (req, res) => {
  // Agent port discovery probes listeners with GET / (User-Agent persona-ports, docs/lessons/testing.md); it is not a
  // native call and must not kill the fake.
  if (req.method !== 'POST') {
    res.writeHead(404).end('{"message":"Unknown native method"}')
    return
  }
  const chunks = []
  for await (const chunk of req) chunks.push(chunk)
  const params = JSON.parse(Buffer.concat(chunks).toString())
  appendFileSync(join(directory, 'native.jsonl'), `${JSON.stringify({ pid: process.pid, method: req.url.split('/').at(-1), params })}\n`)
  await released('native-hold')
  res.end(JSON.stringify({ steps: JSON.parse(readFileSync(join(directory, 'native.json'), 'utf8')).slice(params.stepOffset) }))
})
await new Promise((resolve) => service.listen(0, '127.0.0.1', resolve))
const logFile = process.argv[process.argv.indexOf('--log-file') + 1]
writeFileSync(logFile, `Language server listening on random port at ${service.address().port} for HTTP\n`)

const started = account()
const models = started.models || []
const pids = join(directory, 'pids')
const running = (existsSync(pids) ? readFileSync(pids, 'utf8').trim().split('\n').map(Number) : []).filter((pid) => {
  try {
    process.kill(pid, 0)
    return true
  } catch {
    return false
  }
})
if (running.length) appendFileSync(join(directory, 'overlaps'), `${process.pid} started while ${running.join(', ')} ran\n`)
appendFileSync(pids, `${process.pid}\n`)
const exit = () => setTimeout(() => process.exit(0), started.slowExit ? 500 : 0)
process.on('SIGTERM', exit)
let signedOut = Boolean(started.error)
const send = (value) => process.stdout.write(`${JSON.stringify(value)}\n`)
const answer = (id, result) => send({ jsonrpc: '2.0', id, result })
const fail = (id, message) => send({ jsonrpc: '2.0', id, error: { code: -32000, message } })

const input = createInterface({ input: process.stdin })
input.on('close', exit)
input.on('line', (line) => {
  const request = JSON.parse(line)
  const { id, method, params } = request
  if (!method) return
  appendFileSync(join(directory, 'calls.jsonl'), `${JSON.stringify({ pid: process.pid, ...request })}\n`)
  if (account().error) signedOut = true
  if (id === undefined) return
  if (method === 'initialize') return answer(id, { protocolVersion: 1, agentInfo: { version: 'test' } })
  if (method === 'session/close') return answer(id, {})
  if (signedOut) return fail(id, 'Please sign in')
  switch (method) {
    case 'session/new':
    case 'session/resume':
      return answer(id, { sessionId: params.sessionId || randomUUID(), models: { availableModels: models, currentModelId: models[0]?.modelId } })
    case 'session/set_model':
      if (!models.some((model) => model.modelId === params.modelId) || started.rejects?.includes(params.modelId)) {
        return send({ jsonrpc: '2.0', id, error: { code: -32602, message: `Unknown model ID: ${params.modelId}` } })
      }
      return answer(id, {})
    case 'session/set_mode':
    case 'session/set_config_option':
      return answer(id, {})
    case 'session/prompt': {
      const text = started.quota === 'exhausted'
        ? '⚠ RESOURCE_EXHAUSTED (code 429): Individual quota reached. Resets in 118h.'
        : started.id ? `OK (${started.id})` : 'OK'
      const end = () => {
        send({ jsonrpc: '2.0', method: 'session/update', params: { sessionId: params.sessionId, update: { sessionUpdate: 'agent_message_chunk', content: { type: 'text', text } } } })
        answer(id, { stopReason: 'end_turn' })
      }
      if (!existsSync(join(directory, 'hold'))) return end()
      const wait = setInterval(() => {
        if (existsSync(join(directory, 'hold'))) return
        clearInterval(wait)
        end()
      }, 20)
      return
    }
    default:
      return send({ jsonrpc: '2.0', id, error: { code: -32601, message: `Method not found: ${method}` } })
  }
})
