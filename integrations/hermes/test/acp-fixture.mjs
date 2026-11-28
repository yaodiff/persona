#!/usr/bin/env node
import { randomUUID } from 'node:crypto'
import { closeSync, realpathSync } from 'node:fs'
import { access, readdir, readFile, writeFile } from 'node:fs/promises'
import { join } from 'node:path'
import { createInterface } from 'node:readline'
import { setTimeout as delay } from 'node:timers/promises'

const root = process.env.HERMES_TEST_STATE
const write = (message) => process.stdout.write(JSON.stringify({ jsonrpc: '2.0', ...message }) + '\n')
const reply = (id, result) => write({ id, result })
const update = (sessionId, update) => write({ method: 'session/update', params: { sessionId, update } })
const user = (text) => ({ sessionUpdate: 'user_message_chunk', content: { type: 'text', text } })
const agent = (text, messageId) => ({ sessionUpdate: 'agent_message_chunk', content: { type: 'text', text }, messageId })
const tool = { toolCallId: 'tool-1', title: 'Echo', kind: 'execute', rawInput: { command: 'echo sample' } }
let session
let active
let permissionId = 0

const lines = createInterface({ input: process.stdin, crlfDelay: Infinity })
function closeInput() {
  lines.close()
  closeSync(0)
  // Stay alive so the host observes a write failure before a process exit.
  setTimeout(() => process.exit(0), 8_000)
}

for await (const line of lines) {
  const { id, method, params, result } = JSON.parse(line)
  switch (method) {
    case 'initialize':
      reply(id, { protocolVersion: 1, agentInfo: { name: 'hermes-agent' },
        agentCapabilities: { loadSession: true, sessionCapabilities: { list: {} } } })
      break
    case 'session/list': {
      const files = (await readdir(root)).filter((file) => file.endsWith('.json')).sort()
      const next = params.cursor ? files.indexOf(params.cursor) + 1 : 0
      const selected = files[next]
      const sessions = selected ? [JSON.parse(await readFile(join(root, selected), 'utf-8')).info] : []
      if (process.env.HERMES_TEST_CLOSE_STDIN_AFTER_LIST) closeInput()
      reply(id, { sessions, ...(next + 1 < files.length ? { nextCursor: selected } : {}) })
      if (process.env.HERMES_TEST_EXIT_AFTER_LIST) process.stdout.write('', () => process.exit(0))
      break
    }
    case 'session/new':
      // process.cwd() is resolved; a temporary folder can be behind a link (macOS /var -> /private/var).
      if (process.cwd() !== realpathSync(params.cwd)) throw new Error('wrong worker directory')
      session = { info: { sessionId: randomUUID(), cwd: params.cwd }, updates: [] }
      reply(id, { sessionId: session.info.sessionId })
      break
    case 'session/load':
      session = JSON.parse(await readFile(join(root, params.sessionId + '.json'), 'utf-8'))
      if (process.cwd() !== realpathSync(session.info.cwd)) throw new Error('wrong restored worker directory')
      if (session.holdLoad) {
        await writeFile(join(root, 'load-started'), '')
        while (await access(join(root, 'load-hold')).then(() => true, () => false)) await delay(15)
      }
      if (session.failLoad) {
        delete session.failLoad
        await writeFile(join(root, params.sessionId + '.json'), JSON.stringify(session))
        write({ id, error: { code: -32000, message: 'Transient load failure' } })
        break
      }
      for (const event of session.updates) update(params.sessionId, event)
      reply(id, {})
      break
    case 'session/prompt': {
      if (active || params.sessionId !== session.info.sessionId) throw new Error('worker session overlap')
      const text = params.prompt[0].text
      active = { id, text }
      if (text === 'disconnect') process.exit(1)
      if (text === 'saved disconnect') {
        await save([agent('saved before exit')])
        process.exit(1)
      }
      if (text === 'close input') {
        await finish([agent('input closed')])
        closeInput()
      } else if (text === 'hold') {
        update(params.sessionId, agent('streaming'))
      } else if (text === 'segmented') {
        update(params.sessionId, agent('first ', 'one'))
        update(params.sessionId, agent('second', 'two'))
      } else if (text === 'permission') {
        active.permission = 'permission-' + ++permissionId
        write({ id: active.permission, method: 'session/request_permission', params: {
          sessionId: params.sessionId, toolCall: tool, options: [
            { kind: 'allow_always', optionId: 'always' }, { kind: 'allow_once', optionId: 'once' },
            { kind: 'reject_once', optionId: 'deny' },
          ],
        } })
      } else {
        await finish([agent('echo: '), agent(text)])
      }
      break
    }
    case 'session/cancel':
      if (params.sessionId !== session.info.sessionId || !active) throw new Error('unexpected cancellation')
      await finish([], 'cancelled')
      break
    default:
      if (active && id === active.permission) {
        const text = result?.outcome?.optionId ?? result?.outcome?.outcome
        await finish([
          { sessionUpdate: 'tool_call', ...tool },
          { sessionUpdate: 'tool_call_update', toolCallId: tool.toolCallId, status: 'completed', rawOutput: text },
          agent(text),
        ])
      }
  }
}

async function finish(events, reason = 'end_turn') {
  const { id } = active
  await save(events)
  active = undefined
  for (const event of events) update(session.info.sessionId, event)
  reply(id, { stopReason: reason })
}

async function save(events) {
  session.info.updatedAt = new Date().toISOString()
  session.updates.push(user(active.text), ...events)
  await writeFile(join(root, session.info.sessionId + '.json'), JSON.stringify(session))
}
