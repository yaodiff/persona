// A fake `agy acp` for supervision-policy.test.js.
//
// Flags:
//   --record <path>            append one JSON line per inbound request/notification
//   --no-additional-dirs       omit agentCapabilities.sessionCapabilities.additionalDirectories
//   --fail-resume              reject session/resume requests with an ACP error
//   --ignore-term              stay up after SIGTERM and stdin close
//
// Prompt behavior:
//   text containing 'hold'     keeps session/prompt open until session/cancel arrives
//   anything else              immediately emits a short agent message and ends with end_turn
import { appendFileSync } from 'node:fs'
import { randomUUID } from 'node:crypto'
import { createInterface } from 'node:readline'

if (process.argv.includes('models')) {
  console.log('test-model\tTest Model')
  process.exit(0)
}

const recordFile = process.argv[process.argv.indexOf('--record') + 1]
const noAdditionalDirs = process.argv.includes('--no-additional-dirs')
const failResume = process.argv.includes('--fail-resume')
const ignoreTerm = process.argv.includes('--ignore-term')
const flagValue = (name) => {
  const index = process.argv.indexOf(name)
  return index >= 0 ? process.argv[index + 1] : undefined
}
const resumeDefaultModel = flagValue('--resume-default-model')
const rejectModel = flagValue('--reject-model')
const record = (o) => appendFileSync(recordFile, `${JSON.stringify({ worker: process.pid, ...o })}\n`)
const send = (o) => process.stdout.write(`${JSON.stringify(o)}\n`)

const turns = new Map() // sessionId -> { id }
let nextPermission = 1

function askPermission(sessionId) {
  const id = `permission-${nextPermission++}`
  send({ jsonrpc: '2.0', id, method: 'session/request_permission', params: {
    sessionId, toolCall: { toolCallId: id, title: 'run_command', rawInput: { CommandLine: 'pwd' } },
    options: [{ kind: 'allow_once', optionId: 'yes' }, { kind: 'reject_once', optionId: 'no' }],
  } })
}

function onRequest(msg) {
  const { id, method, params } = msg
  const answer = (result) => send({ jsonrpc: '2.0', id, result })
  switch (method) {
    case 'initialize':
      return answer({
        protocolVersion: 1,
        agentInfo: { name: 'fake-agy', version: '1.5.12' },
        agentCapabilities: noAdditionalDirs
          ? { sessionCapabilities: {} }
          : { sessionCapabilities: { additionalDirectories: {} } },
      })
    case 'session/new': {
      const sessionId = randomUUID()
      return answer({
        sessionId,
        models: { currentModelId: 'test-model', availableModels: [{ modelId: 'test-model', name: 'Test Model' }] },
      })
    }
    case 'session/resume':
      if (failResume) {
        return send({ jsonrpc: '2.0', id, error: { code: -32000, message: 'session/resume refused additionalDirectories' } })
      }
      if (resumeDefaultModel) {
        return answer({
          models: {
            currentModelId: resumeDefaultModel,
            availableModels: [{ modelId: resumeDefaultModel, name: resumeDefaultModel }],
          },
        })
      }
      return answer({})
    case 'session/set_model':
      if (rejectModel && params?.modelId === rejectModel) {
        return send({ jsonrpc: '2.0', id, error: { code: -32602, message: `Unknown model ${params.modelId}` } })
      }
      return answer({})
    case 'session/set_mode':
    case 'session/close':
      return answer({})
    case 'session/set_config_option':
      answer({})
      if (params.value === 'wake-idle') {
        send({
          jsonrpc: '2.0',
          method: 'session/update',
          params: {
            sessionId: params.sessionId,
            update: { sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: 'autonomous step' } },
          },
        })
      }
      if (params.value === 'ask-idle') askPermission(params.sessionId)
      return
    case 'session/prompt': {
      const sessionId = params.sessionId
      const text = params.prompt?.find((b) => b.type === 'text')?.text || ''
      if (text.includes('hold') || text === 'ask') {
        turns.set(sessionId, { id })
        if (text === 'ask') askPermission(sessionId)
        return
      }
      send({
        jsonrpc: '2.0',
        method: 'session/update',
        params: {
          sessionId,
          update: { sessionUpdate: 'agent_message_chunk', content: { type: 'text', text: 'ok' } },
        },
      })
      record({ ended: sessionId })
      return answer({ stopReason: 'end_turn' })
    }
    default:
      return send({ jsonrpc: '2.0', id, error: { code: -32601, message: `Method not found: ${method}` } })
  }
}

function onNotification(msg) {
  if (msg.method !== 'session/cancel') return
  const sessionId = msg.params?.sessionId
  const turn = turns.get(sessionId)
  if (!turn) return
  turns.delete(sessionId)
  record({ ended: sessionId })
  send({ jsonrpc: '2.0', id: turn.id, result: { stopReason: 'cancelled' } })
}

if (ignoreTerm) {
  process.on('SIGTERM', () => record({ term: process.pid }))
  setInterval(() => {}, 60_000)
}
record({
  started: process.pid,
  fakeExtensionCredential: process.env.EXAMPLE_EXTENSION_CREDENTIAL ?? null,
  fakeExtensionAccount: process.env.EXAMPLE_EXTENSION_ACCOUNT ?? null,
  path: process.env.PATH ?? '',
})
const rl = createInterface({ input: process.stdin, crlfDelay: Infinity })
rl.on('line', (line) => {
  if (!line.trim()) return
  const msg = JSON.parse(line)
  record(msg)
  if (msg.method && msg.id !== undefined) onRequest(msg)
  else if (msg.method) onNotification(msg)
})
rl.on('close', () => { if (!ignoreTerm) process.exit(0) })
