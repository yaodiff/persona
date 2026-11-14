import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { once } from 'node:events'
import { createInterface } from 'node:readline'
import { describe, it } from 'node:test'
import { fileURLToPath } from 'node:url'
import { mcpServers } from '../lib/compat.js'

const PROXY = fileURLToPath(new URL('../mcp-proxy.mjs', import.meta.url))
const FAKE = fileURLToPath(new URL('./fake-mcp-server.mjs', import.meta.url))

/** The proxy in front of the fake server, with a call(method, params) that resolves to the response. */
function proxied(t) {
  const proc = spawn(process.execPath, [PROXY, process.execPath, FAKE], { stdio: ['pipe', 'pipe', 'inherit'] })
  t.after(() => proc.kill())
  const waiting = new Map()
  createInterface({ input: proc.stdout }).on('line', (line) => {
    const msg = JSON.parse(line)
    waiting.get(msg.id)?.(msg)
  })
  let id = 0
  const call = (method, params) => new Promise((resolve) => {
    waiting.set(++id, resolve)
    proc.stdin.write(JSON.stringify({ jsonrpc: '2.0', id, method, params }) + '\n')
  })
  return { proc, call }
}

describe('MCP proxy (#7333)', { timeout: 15_000 }, () => {
  it('adds persona_call to the server\'s tools', async (t) => {
    const { call } = proxied(t)
    const res = await call('tools/list', {})
    assert.deepEqual(res.result.tools.map((x) => x.name), ['todo_create', 'agents_list', 'persona_call'])
  })

  it('sends persona_call on as a call of the named tool, and other calls unchanged', async (t) => {
    const { call } = proxied(t)
    await call('tools/list', {})
    const via = await call('tools/call', { name: 'persona_call', arguments: { name: 'todo_create', arguments: { title: 'x' } } })
    assert.deepEqual(JSON.parse(via.result.content[0].text), { name: 'todo_create', args: { title: 'x' } })
    const bare = await call('tools/call', { name: 'agents_list', arguments: {} })
    assert.deepEqual(JSON.parse(bare.result.content[0].text), { name: 'agents_list', args: {} })
    const noArgs = await call('tools/call', { name: 'persona_call', arguments: { name: 'agents_list' } })
    assert.deepEqual(JSON.parse(noArgs.result.content[0].text), { name: 'agents_list', args: {} })
  })

  it('lists the tools without a name and leaves an unknown one to the server', async (t) => {
    const { call } = proxied(t)
    await call('tools/list', {})
    const list = await call('tools/call', { name: 'persona_call', arguments: {} })
    assert.match(list.result.content[0].text, /todo_create, agents_list/)
    const unknown = await call('tools/call', { name: 'persona_call', arguments: { name: 'nope' } })
    assert.match(unknown.error.message, /unknown tool nope/)
  })

  it('ends with its server when agy closes stdin', async (t) => {
    const { proc } = proxied(t)
    proc.stdin.end()
    const [code] = await once(proc, 'exit')
    assert.equal(code, 0)
  })
})

describe('mcpServers', () => {
  it('passes one persona server; an assistant chat\'s through the proxy', () => {
    const agent = mcpServers('/bin/personad', 'agy-1', false)
    assert.deepEqual(agent.map((s) => [s.name, s.command, s.args]), [['persona', '/bin/personad', ['mcp']]])
    const chat = mcpServers('/bin/personad', 'agy-1', true)
    assert.deepEqual(chat.map((s) => [s.name, s.command, s.args]), [['persona', process.execPath, [PROXY, '/bin/personad', 'mcp']]])
    assert.ok(chat[0].env.some((e) => e.name === 'PERSONA_ASSISTANT' && e.value === '1'))
    assert.ok(chat[0].env.some((e) => e.name === 'PERSONA_SESSION_ID' && e.value === 'agy-1'))
  })
})
