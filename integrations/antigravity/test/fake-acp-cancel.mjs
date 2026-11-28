// A fake `agy acp` for acp-cancel.test.js that cancels as the ACP spec says (protocol/prompt-turn, "Cancellation"):
// session/cancel is a notification, and a session/cancel request gets -32601 as an agent built on the ACP SDK answers
// it. The notification ends the held session/prompt with stopReason 'cancelled' once every permission request the
// turn made is answered. The prompt text picks what the turn does:
//   'ask'            request permission mid-prompt and wait for the answer
//   'ask-then-end'   request permission, then end the turn without waiting for the answer
//   'ask-on-cancel'  request permission when the cancel arrives (a request that crossed it on the wire)
//   'ignore-cancel'  hold the prompt and never end it, even when cancelled (an agent that does not confirm the cancel)
//   anything else    hold the prompt until it is cancelled
// Every message it receives goes to the --record file as one JSON line, after a first {"started": <pid>} line.
import { appendFileSync, existsSync } from 'node:fs'
import { createInterface } from 'node:readline'

if (process.argv.includes('models')) { console.log('test-model\tTest Model'); process.exit(0) }

const SESSION = '0c7d2c4e-5b1a-4f3e-9a6d-2f8b1c3d4e5f'
const recordFile = process.argv[process.argv.indexOf('--record') + 1]
const record = (o) => appendFileSync(recordFile, `${JSON.stringify(o)}\n`)
const send = (o) => process.stdout.write(`${JSON.stringify(o)}\n`)

let nextId = 1
const turns = new Map() // sessionId -> { id, mode, cancelled, open: Set of permission request ids }

function askPermission(sessionId, toolCallId, toolCall = { title: 'run_command', rawInput: { command: 'ls' } }, options) {
  const id = `perm-${nextId++}`
  record({ askedPermission: id, sessionId })
  send({
    jsonrpc: '2.0',
    id,
    method: 'session/request_permission',
    params: {
      sessionId,
      toolCall: { toolCallId, ...toolCall },
      options: options === undefined ? [
        { optionId: 'allow', name: 'Allow', kind: 'allow_once' },
        { optionId: 'reject', name: 'Reject', kind: 'reject_once' },
      ] : options,
    },
  })
  return id
}

function askElicitation(sessionId, toolCallId, params = {}) {
  const id = `elicit-${nextId++}`
  record({ askedElicitation: id, sessionId })
  send({
    jsonrpc: '2.0',
    id,
    method: 'elicitation/create',
    params: {
      sessionId,
      toolCallId,
      mode: 'form',
      message: 'The agent has a question.',
      requestedSchema: {
        type: 'object',
        properties: {
          q1: {
            type: 'string',
            title: 'Pick a fruit',
            oneOf: [
              { const: '1', title: '(Recommended) Apple' },
              { const: '2', title: 'Banana' },
            ],
          },
          q1_other: { type: 'string', title: 'Other', description: 'Optional free-form answer to: Pick a fruit' },
        },
      },
      ...params,
    },
  })
  return id
}

function endIfDone(sessionId) {
  const turn = turns.get(sessionId)
  if (!turn || !turn.cancelled || turn.open.size > 0) return
  turns.delete(sessionId)
  record({ ended: sessionId })
  send({ jsonrpc: '2.0', id: turn.id, result: { stopReason: 'cancelled' } })
}

function onRequest(msg) {
  const { id, method, params } = msg
  const answer = (result) => send({ jsonrpc: '2.0', id, result })
  switch (method) {
    case 'initialize':
      return answer({ protocolVersion: 1, agentInfo: { name: 'fake-agy', version: '0.0.0' }, agentCapabilities: {} })
    case 'session/new':
      return answer({ sessionId: SESSION })
    case 'session/resume':
    case 'session/set_model':
    case 'session/set_mode':
    case 'session/set_config_option':
      return answer({})
    case 'session/close':
      return send({ jsonrpc: '2.0', id, error: { code: -32601, message: 'Method not found: session/close' } })
    case 'session/prompt': {
      const sessionId = params.sessionId
      const mode = params.prompt?.find((b) => b.type === 'text')?.text?.trim() || ''
      if (mode === 'reply') {
        send({ jsonrpc: '2.0', method: 'session/update', params: { sessionId,
          update: { sessionUpdate: 'agent_message_chunk', content: { text: 'Finished.' } } } })
        return answer({ stopReason: 'end_turn' })
      }
      const turn = { id, mode, cancelled: false, open: new Set() }
      turns.set(sessionId, turn)
      if (mode === 'ask' || mode.startsWith('ask-shape ')) {
        const toolCall = mode === 'ask' ? undefined : JSON.parse(mode.slice('ask-shape '.length))
        turn.open.add(askPermission(sessionId, 'call-1', toolCall))
      }
      if (mode.startsWith('ask-options ')) {
        const options = JSON.parse(mode.slice('ask-options '.length))
        turn.open.add(askPermission(sessionId, 'call-1', undefined, options))
      }
      if (mode.startsWith('ask-full ')) {
        const { toolCall, options, waitFile, endOnAnswer, asks, askOnCancel, nextAsk } = JSON.parse(mode.slice('ask-full '.length))
        turn.endOnAnswer = !!endOnAnswer
        turn.askOnCancel = askOnCancel || null
        turn.nextAsk = nextAsk || null
        if (Array.isArray(asks) && asks.length) {
          for (const spec of asks) {
            turn.open.add(askPermission(sessionId, spec.toolCallId || `call-${nextId}`, spec.toolCall, spec.options ?? options))
          }
        } else if (waitFile) {
          const timer = setInterval(() => {
            if (!turns.has(sessionId) || turn.cancelled) {
              clearInterval(timer)
              return
            }
            if (existsSync(waitFile)) {
              clearInterval(timer)
              turn.open.add(askPermission(sessionId, 'call-1', toolCall, options))
            }
          }, 10)
        } else {
          turn.open.add(askPermission(sessionId, 'call-1', toolCall, options))
        }
      }
      if (mode.startsWith('elicit ')) {
        const { toolCallId = 'elicit-call-1', params: elicitParams, endOnAnswer = true } = JSON.parse(mode.slice('elicit '.length))
        turn.endOnAnswer = !!endOnAnswer
        turn.open.add(askElicitation(sessionId, toolCallId, elicitParams))
      }
      if (mode === 'updates' || mode === 'unrelated-updates') {
        let count = 0
        const timer = setInterval(() => {
          send({ jsonrpc: '2.0', method: 'session/update', params: {
            sessionId: mode === 'updates' ? sessionId : 'another-session', update: { sessionUpdate: 'heartbeat' },
          } })
          if (++count === 16 || !turns.has(sessionId)) {
            clearInterval(timer)
            if (turns.delete(sessionId)) answer({ stopReason: 'end_turn' })
          }
        }, 40)
      }
      if (mode === 'ask-then-end') {
        askPermission(sessionId, 'call-1')
        turns.delete(sessionId)
        answer({ stopReason: 'end_turn' })
      }
      return
    }
    default:
      // session/cancel included: it is a notification only.
      return send({ jsonrpc: '2.0', id, error: { code: -32601, message: `Method not found: ${method}` } })
  }
}

function onNotification(msg) {
  if (msg.method !== 'session/cancel') return
  const sessionId = msg.params?.sessionId
  const turn = turns.get(sessionId)
  if (!turn) return
  if (turn.mode === 'ignore-cancel') return
  turn.cancelled = true
  if (turn.mode === 'ask-on-cancel') turn.open.add(askPermission(sessionId, 'call-2'))
  if (turn.askOnCancel) {
    const spec = turn.askOnCancel
    turn.askOnCancel = null
    turn.open.add(askPermission(sessionId, spec.toolCallId || 'call-cancel', spec.toolCall, spec.options))
  }
  endIfDone(sessionId)
}

function onAnswer(msg) {
  for (const [sessionId, turn] of turns) {
    if (turn.open.delete(msg.id)) {
      if (turn.nextAsk && !turn.cancelled) {
        const spec = turn.nextAsk
        turn.nextAsk = null
        turn.open.add(askPermission(sessionId, spec.toolCallId || 'call-2', spec.toolCall, spec.options))
      } else if (turn.endOnAnswer && !turn.cancelled && turn.open.size === 0) {
        turns.delete(sessionId)
        record({ ended: sessionId, stopReason: 'end_turn' })
        send({ jsonrpc: '2.0', id: turn.id, result: { stopReason: 'end_turn' } })
      } else {
        endIfDone(sessionId)
      }
    }
  }
}

record({ started: process.pid })
const rl = createInterface({ input: process.stdin, crlfDelay: Infinity })
rl.on('line', (line) => {
  if (!line.trim()) return
  const msg = JSON.parse(line)
  record(msg)
  if (msg.method && msg.id !== undefined) onRequest(msg)
  else if (msg.method) onNotification(msg)
  else onAnswer(msg)
})
rl.on('close', () => process.exit(0))
