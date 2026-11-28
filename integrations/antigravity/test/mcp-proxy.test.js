import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { once } from 'node:events'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { createInterface } from 'node:readline'
import { describe, it } from 'node:test'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { installCurrent, PACKAGES, packageHash, readShipped, storeVersion } from '../../runtime-sdk/lib/package.js'
import { mediaGuardPath, mcpServers } from '../lib/compat.js'

const PROXY = fileURLToPath(new URL('../mcp-proxy.mjs', import.meta.url))
const MEDIA_GUARD = fileURLToPath(new URL('../media-guard.mjs', import.meta.url))
const FAKE = fileURLToPath(new URL('./fake-mcp-server.mjs', import.meta.url))
const INTEGRATIONS = fileURLToPath(new URL('../../', import.meta.url))

function storedVersion(marker) {
  const files = readShipped(INTEGRATIONS, PACKAGES['persona-antigravity-runtime']).map((file) =>
    file.path === 'antigravity/companion.mjs' ? { ...file, data: Buffer.concat([file.data, Buffer.from(`\n// ${marker}\n`)]) } : file,
  )
  return { files, hash: packageHash(files) }
}

/** The proxy in front of the fake server, with a call(method, params) that resolves to the response. */
function proxied(t, tools, { proxy = PROXY, proxyArgs = [] } = {}) {
  const args = []
  if (tools) {
    const dir = mkdtempSync(join(tmpdir(), 'persona-mcp-proxy-'))
    t.after(() => rmSync(dir, { recursive: true, force: true }))
    args.push(join(dir, 'tools.json'))
    writeFileSync(args[0], JSON.stringify(tools))
  }
  const proc = spawn(process.execPath, [proxy, ...proxyArgs, process.execPath, FAKE, ...args], { stdio: ['pipe', 'pipe', 'inherit'] })
  t.after(() => proc.kill())
  const waiting = new Map()
  createInterface({ input: proc.stdout }).on('line', (line) => {
    const msg = JSON.parse(line)
    const resolve = waiting.get(msg.id)
    waiting.delete(msg.id)
    resolve?.(msg)
  })
  let id = 0
  const call = (method, params, requestID = ++id) => new Promise((resolve) => {
    waiting.set(requestID, resolve)
    proc.stdin.write(JSON.stringify({ jsonrpc: '2.0', id: requestID, method, params }) + '\n')
  })
  return { proc, call }
}

describe('MCP proxy (#7333)', { timeout: 15_000 }, () => {
  it('adds persona_call to the server\'s tools', async (t) => {
    const big = { name: 'kb_search', description: 'Search the knowledge base. '.repeat(20), inputSchema: { type: 'object', properties: {} } }
    const { call } = proxied(t, [big, { name: 'agents_list', description: 'List the Agents', inputSchema: { type: 'object', properties: {} } }])
    const res = await call('tools/list', {})
    assert.deepEqual(res.result.tools.map((x) => x.name), ['kb_search', 'persona_call'])
  })

  it('weighs a tool by what agy declares of it, not its output schema or annotations (#10756)', async (t) => {
    const padded = {
      name: 'mail_accounts', description: 'List mail accounts', inputSchema: { type: 'object', properties: { id: { type: 'string' } }, required: ['id'] },
      outputSchema: { type: 'object', description: 'x'.repeat(2000) }, annotations: { title: 'y'.repeat(500) },
    }
    const { call } = proxied(t, [padded])
    const res = await call('tools/list', {})
    assert.deepEqual(res.result.tools.map((x) => x.name), ['persona_call'])
    const listed = await call('tools/call', { name: 'persona_call', arguments: {} })
    assert.match(listed.result.content[0].text, /- mail_accounts: List mail accounts Arguments: \{"type":"object","properties":\{"id":\{"type":"string"\}\},"required":\["id"\]\}/)
  })

  it('lists a left-out tool with its whole input schema, references and alternatives included (#10756)', async (t) => {
    const schema = {
      type: 'object',
      $defs: { when: { type: 'string', enum: ['now', 'later'] } },
      properties: { at: { $ref: '#/$defs/when' }, target: { anyOf: [{ type: 'string' }, { type: 'integer' }] } },
      additionalProperties: false,
    }
    const { call } = proxied(t, [{ name: 'cancel_followup', description: 'Cancel', inputSchema: schema }])
    await call('tools/list', {})
    const listed = await call('tools/call', { name: 'persona_call', arguments: {} })
    const line = listed.result.content[0].text.split('\n').find((l) => l.startsWith('- cancel_followup:'))
    assert.deepEqual(JSON.parse(line.slice(line.indexOf('Arguments: ') + 'Arguments: '.length)), schema)
  })

  it('declares no tool smaller than persona_call, so agy never defers it (#10756)', async (t) => {
    const small = Array.from({ length: 40 }, (_, i) => ({ name: `connector_read_${i}`, description: 'Read one thing', inputSchema: { type: 'object', properties: {} } }))
    const big = { name: 'todo_create', description: 'Add a to-do. '.repeat(40), inputSchema: { type: 'object', properties: { title: { type: 'string' } } } }
    const { call } = proxied(t, [...small, big])
    const res = await call('tools/list', {})
    const size = (tool) => Buffer.byteLength(JSON.stringify(tool))
    const declared = res.result.tools
    const dispatch = declared.find((x) => x.name === 'persona_call')
    assert.deepEqual(declared.map((x) => x.name), ['todo_create', 'persona_call'])
    assert.ok(declared.every((x) => x === dispatch || size(x) > size(dispatch)), 'persona_call is the smallest declared tool')
    // The tools left out are still listed, with their arguments, and callable through it.
    const listed = await call('tools/call', { name: 'persona_call', arguments: {} })
    assert.match(listed.result.content[0].text, /connector_read_0, .*connector_read_39, todo_create/)
    assert.match(listed.result.content[0].text, /- connector_read_7: Read one thing Arguments: \{"type":"object","properties":\{\}\}/)
    assert.doesNotMatch(listed.result.content[0].text, /- todo_create:/)
    const via = await call('tools/call', { name: 'persona_call', arguments: { name: 'connector_read_7', arguments: {} } })
    assert.deepEqual(JSON.parse(via.result.content[0].text), { name: 'connector_read_7', args: {} })
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

  it('runs as a self-contained entry after its source generation loses every other file', async (t) => {
    const dir = mkdtempSync(join(tmpdir(), 'persona-mcp-proxy-standalone-'))
    t.after(() => rmSync(dir, { recursive: true, force: true }))
    const proxy = join(dir, 'mcp-proxy.mjs')
    writeFileSync(proxy, readFileSync(PROXY))
    const big = { name: 'kb_search', description: 'Search the knowledge base. '.repeat(20), inputSchema: { type: 'object', properties: {} } }
    const { call } = proxied(t, [big], { proxy })
    const res = await call('tools/list', {})
    assert.deepEqual(res.result.tools.map((x) => x.name), ['kb_search', 'persona_call'])
  })

  it('ends with its server when agy closes stdin', async (t) => {
    const { proc } = proxied(t)
    proc.stdin.end()
    const [code] = await once(proc, 'exit')
    assert.equal(code, 0)
  })
})

describe('command gate tools', { timeout: 15_000 }, () => {
  // What the node adds for a runtime's assistant chat (server/internal/node/assistant_runtime.go gateTools,
  // gateToolSchema): the assistant's instructions name it, so agy must see it declared, its schema as the node gives it.
  const gateSchema = (command) => ({
    type: 'object', required: ['args'],
    properties: { args: { type: 'array', items: { type: 'string' }, description: `The words after ${command}: METHOD PATH [JSON body] and its flags, one per item` } },
  })
  const chatAPI = {
    name: 'chat_api',
    description: 'Run the `ex` helper of the chat skill: `args` are the words after `ex`, e.g. ["GET", "/me"], ["GET", "/conversations?scope=group&limit=100"], ["POST", "/conversations/FEED_ID/messages", "{\\"text\\":\\"hi\\"}"]. Reads run at once; writes other people see are confirmed on the phone first, and a refusal comes back as the result. Returns what ex prints.',
    inputSchema: gateSchema('ex'),
  }

  it('declares it with its schema unchanged and passes its calls on by name', async (t) => {
    const { call } = proxied(t, [chatAPI, { name: 'agents_list', description: 'List the Agents', inputSchema: { type: 'object', properties: {} } }])
    const listed = await call('tools/list', {})
    assert.deepEqual(listed.result.tools.map((x) => x.name), ['chat_api', 'persona_call'])
    assert.deepEqual(listed.result.tools[0], chatAPI)
    const args = { args: ['GET', '/me'] }
    for (const params of [{ name: 'chat_api', arguments: args }, { name: 'persona_call', arguments: { name: 'chat_api', arguments: args } }]) {
      const res = await call('tools/call', params)
      assert.deepEqual(JSON.parse(res.result.content[0].text), { name: 'chat_api', args })
    }
  })

  it('keeps one with a short description reachable through persona_call, with its arguments', async (t) => {
    const short = { name: 'ex_api', description: 'Run ex.', inputSchema: gateSchema('ex') }
    const { call } = proxied(t, [short])
    const listed = await call('tools/list', {})
    assert.deepEqual(listed.result.tools.map((x) => x.name), ['persona_call'])
    const text = (await call('tools/call', { name: 'persona_call', arguments: {} })).result.content[0].text
    assert.ok(text.includes(`- ex_api: Run ex. Arguments: ${JSON.stringify(short.inputSchema)}`), text)
  })
})

describe('MCP declaration enums (#9944)', { timeout: 15_000 }, () => {
  // The template has language branches and is not JSON until the server renders it. Use its recorded MCP wire form.
  const rendered = JSON.parse(readFileSync(new URL('../../../server/internal/hub/testdata/prompts/mcp_tools/assistant/task_review.golden', import.meta.url), 'utf8'))
  const taskTool = { name: rendered.name, description: rendered.description, inputSchema: rendered.input_schema }
  const catalogTool = { name: 'tool_catalog', inputSchema: { type: 'object' } }

  it('projects the real task_review schema without its boolean enum and keeps boolean call arguments', async (t) => {
    assert.deepEqual(taskTool.inputSchema.properties.evidence.properties.ready.enum, [true])
    const { call } = proxied(t, [taskTool])
    const listed = await call('tools/list', {})
    const expected = structuredClone(taskTool)
    delete expected.inputSchema.properties.evidence.properties.ready.enum
    assert.deepEqual(listed.result.tools, [expected, listed.result.tools.at(-1)])
    assert.equal(listed.result.tools.at(-1).name, 'persona_call')
    for (const ready of [true, false]) {
      const args = { todo_id: 'todo_fixture', verdict: 'parked', evidence: { kind: 'merge_review', ready } }
      for (const params of [
        { name: 'task_review', arguments: args },
        { name: 'persona_call', arguments: { name: 'task_review', arguments: args } },
      ]) {
        const result = await call('tools/call', params)
        assert.deepEqual(JSON.parse(result.result.content[0].text), { name: 'task_review', args })
      }
    }
  })

  it('changes only enum keywords in schema positions, including properties named enum', async (t) => {
    const data = { enum: [true], properties: { enum: { enum: [1] } } }
    const schema = {
      type: 'object', required: ['enum'], default: data, examples: [data], const: data, 'x-metadata': data,
      properties: {
        enum: { type: 'boolean', enum: [true], default: true, examples: [true], const: true },
        numeric: { type: 'number', enum: [1, 2], minimum: 1 },
        mixed: { enum: ['one', true] }, empty: { enum: [] }, emptyString: { enum: ['valid', ''] },
        valid: { type: 'string', enum: ['accepted', 'false', ' '] },
        allowed: true, blocked: false, reference: { $ref: '#/$defs/enum' },
        tuple: { items: [{ enum: [0] }, { enum: ['ok'] }], additionalItems: { enum: [false] } },
        array: { items: { enum: [1] }, contains: { enum: [true] }, prefixItems: [{ enum: [false] }],
          unevaluatedItems: { enum: [0] } },
        union: { allOf: [{ enum: [true] }], anyOf: [{ enum: [1] }], oneOf: [{ enum: [false] }],
          not: { enum: [false] }, if: { enum: [true] }, then: { enum: [1] }, else: { enum: [0] } },
      },
      patternProperties: { '^enum$': { enum: [1] } }, additionalProperties: { enum: [true] },
      propertyNames: { enum: ['', 'name'] }, $defs: { enum: { enum: [1] } }, definitions: { enum: { enum: [true] } },
      dependencies: { enum: { enum: [true] }, requiredDependency: ['enum'] },
      dependentSchemas: { enum: { enum: [true] } }, unevaluatedProperties: { enum: [false] }, contentSchema: { enum: [1] },
    }
    const expected = structuredClone(schema)
    for (const name of ['enum', 'numeric', 'mixed', 'empty', 'emptyString']) delete expected.properties[name].enum
    delete expected.properties.tuple.items[0].enum
    delete expected.properties.tuple.additionalItems.enum
    for (const name of ['items', 'contains', 'unevaluatedItems']) delete expected.properties.array[name].enum
    delete expected.properties.array.prefixItems[0].enum
    for (const name of ['allOf', 'anyOf', 'oneOf']) delete expected.properties.union[name][0].enum
    for (const name of ['not', 'if', 'then', 'else']) delete expected.properties.union[name].enum
    delete expected.patternProperties['^enum$'].enum
    for (const name of ['additionalProperties', 'propertyNames', 'unevaluatedProperties', 'contentSchema']) delete expected[name].enum
    for (const name of ['$defs', 'definitions', 'dependencies', 'dependentSchemas']) delete expected[name].enum.enum
    const tool = { name: 'nested', inputSchema: schema, annotations: data, outputSchema: schema, _meta: data }
    const { call } = proxied(t, [tool])
    const result = await call('tools/list', {})
    assert.deepEqual(result.result.tools[0], { ...tool, inputSchema: expected })
  })

  it('correlates list responses by id and leaves tool results and ordinary messages unchanged', async (t) => {
    const tools = [taskTool, catalogTool]
    const { call } = proxied(t, tools)
    const pending = call('tools/list', { hold: true }, 'list')
    const catalog = await call('tools/call', { name: 'tool_catalog', arguments: {} }, 0)
    assert.deepEqual(catalog.result, { tools })
    const listed = await pending
    assert.equal(listed.result.tools[0].inputSchema.properties.evidence.properties.ready.enum, undefined)
    assert.deepEqual((await call('test/echo', { tools }, 'list')).result, { tools })
    assert.ok((await call('tools/list', { fail: true }, 0)).error)
    assert.deepEqual((await call('test/echo', { tools }, 0)).result, { tools })
  })
})

describe('mcpServers', () => {
  it('routes regular agent persona tools through the agent proxy while keeping persona_a2a separate', () => {
    const agent = mcpServers('/bin/personad', 'agy-1', false, 'test-ticket')
    const chat = mcpServers('/bin/personad', 'agy-2', true, 'test-ticket')
    assert.deepEqual(agent.map(({ name, command, args }) => [name, command, args]), [
      ['persona', process.execPath, [PROXY, '--agent', '/bin/personad', 'mcp']],
      ['persona_a2a', '/bin/personad', ['mcp']],
    ])
    assert.ok(agent[0].env.every(({ name }) => name !== 'PERSONA_A2A_TOKEN' && name !== 'PERSONA_ASSISTANT'))
    assert.ok(agent[0].env.some(({ name, value }) => name === 'PERSONA_MCP_EXCLUDE_A2A' && value === '1'))
    assert.ok(agent[1].env.some(({ name, value }) => name === 'PERSONA_A2A_TOKEN' && value === 'test-ticket'))
    assert.ok(chat[0].env.every(({ name }) => name !== 'PERSONA_A2A_TOKEN'))
  })
  it('passes agent chats through --agent proxy and assistant chats through default proxy', () => {
    const agent = mcpServers('/bin/personad', 'agy-1', false)
    assert.deepEqual(agent.map((s) => [s.name, s.command, s.args]), [['persona', process.execPath, [PROXY, '--agent', '/bin/personad', 'mcp']]])
    assert.ok(agent[0].env.every((e) => e.name !== 'PERSONA_ASSISTANT'))
    const chat = mcpServers('/bin/personad', 'agy-1', true)
    assert.deepEqual(chat.map((s) => [s.name, s.command, s.args]), [['persona', process.execPath, [PROXY, '/bin/personad', 'mcp']]])
    assert.ok(chat[0].env.some((e) => e.name === 'PERSONA_ASSISTANT' && e.value === '1'))
    assert.ok(chat[0].env.some((e) => e.name === 'PERSONA_SESSION_ID' && e.value === 'agy-1'))
  })

  it('keeps the media guard on its own path in a source checkout', () => {
    assert.equal(mediaGuardPath(), MEDIA_GUARD)
  })

  it('prefers the stable current proxy when loaded from an installed version', async (t) => {
    const store = mkdtempSync(join(tmpdir(), 'persona-mcp-current-'))
    t.after(() => rmSync(store, { recursive: true, force: true }))
    const v1 = storedVersion('prefer-current')
    installCurrent(store, v1.hash, v1.files)
    const compat = await import(pathToFileURL(join(store, v1.hash, 'antigravity', 'lib', 'compat.js')).href)
    const [server] = compat.mcpServers('/bin/personad', 'agy-current', true)
    assert.equal(server.args[0], join(realpathSync(store), 'current', 'antigravity', 'mcp-proxy.mjs'))
    assert.ok(existsSync(server.args[0]))
    assert.equal(compat.mediaGuardPath(), join(realpathSync(store), v1.hash, 'antigravity', 'media-guard.mjs'))
    assert.ok(existsSync(compat.mediaGuardPath()))
  })

  it('resolves again when a later reconnect first makes current available', async (t) => {
    const store = mkdtempSync(join(tmpdir(), 'persona-mcp-dynamic-'))
    t.after(() => rmSync(store, { recursive: true, force: true }))
    const [v1, v2] = ['A', 'B'].map((marker) => storedVersion(`dynamic-${marker}`))
    storeVersion(store, v1.hash, v1.files) // A directly installed version can be loaded before current exists.
    const compat = await import(pathToFileURL(join(store, v1.hash, 'antigravity', 'lib', 'compat.js')).href)
    installCurrent(store, v2.hash, v2.files)
    const [server] = compat.mcpServers('/bin/personad', 'agy-dynamic', true)
    assert.equal(server.args[0], join(realpathSync(store), 'current', 'antigravity', 'mcp-proxy.mjs'))
    assert.ok(existsSync(server.args[0]))
  })

  it('does not mistake a hash-named source checkout for a package store', async (t) => {
    const dir = mkdtempSync(join(tmpdir(), 'persona-mcp-source-name-'))
    t.after(() => rmSync(dir, { recursive: true, force: true }))
    const checkout = join(dir, 'a'.repeat(64))
    for (const file of readShipped(INTEGRATIONS, PACKAGES['persona-antigravity-runtime'])) {
      const path = join(checkout, file.path)
      mkdirSync(join(path, '..'), { recursive: true })
      writeFileSync(path, file.data)
    }
    const unrelated = join(dir, 'current', 'antigravity', 'mcp-proxy.mjs')
    mkdirSync(join(unrelated, '..'), { recursive: true })
    writeFileSync(unrelated, '// unrelated\n')
    const compat = await import(pathToFileURL(join(checkout, 'antigravity', 'lib', 'compat.js')).href)
    const [server] = compat.mcpServers('/bin/personad', 'agy-source-name', true)
    assert.equal(server.args[0], join(realpathSync(checkout), 'antigravity', 'mcp-proxy.mjs'))
  })

  it('re-resolves a deleted versioned proxy to current after consecutive upgrades', async (t) => {
    const store = mkdtempSync(join(tmpdir(), 'persona-mcp-upgrade-'))
    t.after(() => rmSync(store, { recursive: true, force: true }))
    const [v1, v2, v3] = ['A', 'B', 'C'].map((marker) => storedVersion(`upgrade-${marker}`))
    installCurrent(store, v1.hash, v1.files)
    const compat = await import(pathToFileURL(join(store, v1.hash, 'antigravity', 'lib', 'compat.js')).href)
    const original = join(store, v1.hash, 'antigravity', 'mcp-proxy.mjs')
    installCurrent(store, v2.hash, v2.files)
    installCurrent(store, v3.hash, v3.files)
    // Also cover deletion by an older installer that does not retain active generations.
    rmSync(join(store, v1.hash), { recursive: true, force: true })
    assert.equal(existsSync(original), false)
    const [server] = compat.mcpServers('/bin/personad', 'agy-reconnected', true)
    assert.equal(server.args[0], join(realpathSync(store), 'current', 'antigravity', 'mcp-proxy.mjs'))
    assert.ok(existsSync(server.args[0]))
  })
})

describe('MCP proxy assistant alwaysLoad filtering', { timeout: 15_000 }, () => {
  const toolBytes = (tool) => Buffer.byteLength(JSON.stringify({ name: tool.name, description: tool.description, inputSchema: tool.inputSchema }))
  const bigTool = (name, desc, extra = {}) => ({
    name,
    description: `${desc} `.repeat(45).trim(),
    inputSchema: { type: 'object', properties: { id: { type: 'string' } } },
    ...extra,
  })

  it('declares only alwaysLoad tools larger than persona_call and routes deferred tools through persona_call', async (t) => {
    const corePlan = bigTool('task_plan', 'Plan work for supervised tasks.', { _meta: { 'anthropic/alwaysLoad': true } })
    const coreKb = bigTool('kb_search', 'Search the knowledge base.', { _meta: { 'anthropic/alwaysLoad': true } })
    const deferredNoMeta = bigTool('phone_health_summary', 'Summarize daily phone health metrics.')
    const deferredFalse = bigTool('mail_search', 'Search mailbox messages.', { _meta: { 'anthropic/alwaysLoad': false } })
    const { call } = proxied(t, [corePlan, deferredNoMeta, coreKb, deferredFalse])

    const res = await call('tools/list', {})
    const declared = res.result.tools
    assert.deepEqual(declared.map((x) => x.name), ['task_plan', 'kb_search', 'persona_call'])
    const dispatch = declared.find((x) => x.name === 'persona_call')
    assert.ok(declared.every((x) => x === dispatch || toolBytes(x) > toolBytes(dispatch)), 'persona_call stays smallest declared tool')

    const summary = (await call('tools/call', { name: 'persona_call', arguments: {} })).result.content[0].text
    assert.match(summary, /task_plan, phone_health_summary, kb_search, mail_search/)
    assert.match(summary, /- phone_health_summary: .* Arguments: \{"type":"object","properties":\{"id":\{"type":"string"\}\}\}/)
    assert.match(summary, /- mail_search: .* Arguments: \{"type":"object","properties":\{"id":\{"type":"string"\}\}\}/)
    assert.doesNotMatch(summary, /- task_plan:/)
    assert.doesNotMatch(summary, /- kb_search:/)

    const forwarded = await call('tools/call', {
      name: 'persona_call',
      arguments: { name: 'phone_health_summary', arguments: { id: 'steps' } },
    })
    assert.deepEqual(JSON.parse(forwarded.result.content[0].text), { name: 'phone_health_summary', args: { id: 'steps' } })
    const forwardedFalse = await call('tools/call', {
      name: 'persona_call',
      arguments: { name: 'mail_search', arguments: { id: 'inbox' } },
    })
    assert.deepEqual(JSON.parse(forwardedFalse.result.content[0].text), { name: 'mail_search', args: { id: 'inbox' } })
  })

  it('falls back to declaring all tools larger than persona_call when all tools are alwaysLoad or none carry _meta', async (t) => {
    const allAlways = [
      bigTool('task_plan', 'Plan work.', { _meta: { 'anthropic/alwaysLoad': true } }),
      bigTool('phone_health_summary', 'Health summary.', { _meta: { 'anthropic/alwaysLoad': true } }),
    ]
    const resAll = await proxied(t, allAlways).call('tools/list', {})
    assert.deepEqual(resAll.result.tools.map((x) => x.name), ['task_plan', 'phone_health_summary', 'persona_call'])

    const noMeta = [
      bigTool('task_plan', 'Plan work.'),
      bigTool('phone_health_summary', 'Health summary.'),
    ]
    const resLegacy = await proxied(t, noMeta).call('tools/list', {})
    assert.deepEqual(resLegacy.result.tools.map((x) => x.name), ['task_plan', 'phone_health_summary', 'persona_call'])
  })

  it('keeps an alwaysLoad tool smaller than persona_call hidden to preserve the #10756 invariant', async (t) => {
    const tinyAlways = {
      name: 'kb_get',
      description: 'Read one note',
      inputSchema: { type: 'object', properties: {} },
      _meta: { 'anthropic/alwaysLoad': true },
    }
    const bigAlways = bigTool('task_plan', 'Plan work.', { _meta: { 'anthropic/alwaysLoad': true } })
    const deferred = bigTool('mail_search', 'Search mail.')
    const { call } = proxied(t, [tinyAlways, bigAlways, deferred])

    const res = await call('tools/list', {})
    const declared = res.result.tools
    assert.deepEqual(declared.map((x) => x.name), ['task_plan', 'persona_call'])
    const dispatch = declared.find((x) => x.name === 'persona_call')
    assert.ok(declared.every((x) => x === dispatch || toolBytes(x) > toolBytes(dispatch)), 'persona_call remains strictly smallest')

    const summary = (await call('tools/call', { name: 'persona_call', arguments: {} })).result.content[0].text
    assert.match(summary, /- kb_get: Read one note Arguments: \{"type":"object","properties":\{\}\}/)
    assert.match(summary, /- mail_search:/)
    assert.doesNotMatch(summary, /- task_plan:/)

    const via = await call('tools/call', { name: 'persona_call', arguments: { name: 'kb_get', arguments: {} } })
    assert.deepEqual(JSON.parse(via.result.content[0].text), { name: 'kb_get', args: {} })
  })
})

describe('MCP proxy --agent mode (#15822)', { timeout: 15_000 }, () => {
  const bulkSecondary = Array.from({ length: 34 }, (_, i) => ({
    name: i < 29 ? `phone_tool_${i}` : `mail_tool_${i - 29}`,
    description: `Secondary phone or mail tool ${i}. `.repeat(24),
    inputSchema: {
      type: 'object',
      properties: {
        id: { type: 'string', description: 'Identifier for this phone or mail item' },
        payload: { type: 'string', description: 'Payload data for this operation' },
      },
    },
  }))
  const reportTool = {
    name: 'report_to_supervisor',
    description: 'Report progress, questions, blockers, or done criteria to your supervisor. '.repeat(26),
    inputSchema: { type: 'object', required: ['kind', 'text'], properties: { kind: { type: 'string', enum: ['progress', 'question', 'blocked', 'done'] }, text: { type: 'string' } } },
  }
  const spawnTool = {
    name: 'task_spawn',
    description: 'Start a separate coding Agent on your current supervised task. '.repeat(10),
    inputSchema: { type: 'object', required: ['title', 'prompt'], properties: { title: { type: 'string' }, prompt: { type: 'string' } } },
  }
  const leaderPlanTool = {
    name: 'task_plan',
    description: 'Hand part of your own task to workers beneath the task you lead. '.repeat(36),
    inputSchema: { type: 'object', required: ['tasks'], properties: { tasks: { type: 'array', items: { type: 'object', properties: { role: { type: 'string', enum: ['worker'] } } } } } },
  }
  const leaderReviewTool = {
    name: 'task_review',
    description: 'Close, send back or hand up one of your direct worker tasks. '.repeat(26),
    inputSchema: { type: 'object', required: ['todo_id', 'verdict'], properties: { todo_id: { type: 'string' }, verdict: { type: 'string', enum: ['accepted', 'rework', 'needs_user', 'cancelled'] }, ready: { type: 'boolean', enum: [true] } } },
  }
  const smallCore = { name: 'todo_repositories_get', description: 'Read saved repositories.', inputSchema: { type: 'object', properties: { id: { type: 'string' } } } }
  const bigCore = {
    name: 'file_transfer',
    description: 'Copy files between enrolled computers through Persona. '.repeat(18),
    inputSchema: { type: 'object', required: ['direction'], properties: { direction: { type: 'string', enum: ['send', 'fetch', 'list'] } } },
  }
  const toolBytes = (tool) => Buffer.byteLength(JSON.stringify({ name: tool.name, description: tool.description, inputSchema: tool.inputSchema }))

  it('keeps leader task_plan, task_review, report_to_supervisor and task_spawn declared within budget while keeping secondary tools callable via persona_call', async (t) => {
    const catalog = [...bulkSecondary, reportTool, smallCore, bigCore, leaderPlanTool, leaderReviewTool, spawnTool]
    const { call } = proxied(t, catalog, { proxyArgs: ['--agent'] })
    const listed = await call('tools/list', {})
    const declared = listed.result.tools
    const names = declared.map((x) => x.name)
    for (const required of ['report_to_supervisor', 'file_transfer', 'task_plan', 'task_review', 'task_spawn', 'persona_call']) {
      assert.ok(names.includes(required), `missing required declared tool ${required}: ${names}`)
    }
    assert.ok(!names.includes('todo_repositories_get'), 'small core tool stays behind persona_call so persona_call stays smallest')
    assert.ok(!names.includes('machines_list'), 'assistant-only tool must never be invented')
    const dispatch = declared.find((x) => x.name === 'persona_call')
    assert.ok(declared.every((x) => x === dispatch || toolBytes(x) > toolBytes(dispatch)), 'persona_call remains the smallest declared tool')
    const reviewDeclared = declared.find((x) => x.name === 'task_review')
    assert.equal(reviewDeclared.inputSchema.properties.ready.enum, undefined, 'boolean enum is projected out for agy')
    const weightedBytes = declared.reduce((sum, x) => sum + toolBytes(x) + 256, 0)
    assert.ok(weightedBytes <= 26_000, `leader declarations stayed compact (${weightedBytes} B) instead of overflowing agy eager budget`)

    // Direct calls and persona_call work for declared leader tools and overflow secondary tools alike.
    const planCall = await call('tools/call', { name: 'task_plan', arguments: { tasks: [] } })
    assert.deepEqual(JSON.parse(planCall.result.content[0].text), { name: 'task_plan', args: { tasks: [] } })
    const summary = (await call('tools/call', { name: 'persona_call', arguments: {} })).result.content[0].text
    assert.match(summary, /- mail_tool_4:/)
    assert.match(summary, /- todo_repositories_get:/)
    assert.doesNotMatch(summary, /- task_plan:/)
    const overflowCall = await call('tools/call', { name: 'persona_call', arguments: { name: 'mail_tool_4', arguments: { id: 'p1' } } })
    assert.deepEqual(JSON.parse(overflowCall.result.content[0].text), { name: 'mail_tool_4', args: { id: 'p1' } })
  })

  it('never exposes task_plan, task_review or machines_list to a worker whose upstream catalog omits them', async (t) => {
    const workerCatalog = [...bulkSecondary, reportTool, smallCore, bigCore, spawnTool]
    const { call } = proxied(t, workerCatalog, { proxyArgs: ['--agent'] })
    const listed = await call('tools/list', {})
    const declared = listed.result.tools
    const names = declared.map((x) => x.name)
    assert.ok(names.includes('report_to_supervisor'))
    assert.ok(names.includes('task_spawn'))
    assert.ok(names.includes('persona_call'))
    for (const forbidden of ['task_plan', 'task_review', 'machines_list']) {
      assert.ok(!names.includes(forbidden), `worker declared forbidden tool ${forbidden}`)
    }
    const summary = (await call('tools/call', { name: 'persona_call', arguments: {} })).result.content[0].text
    for (const forbidden of ['task_plan', 'task_review', 'machines_list']) {
      assert.ok(!summary.includes(forbidden), `worker persona_call summary mentioned forbidden tool ${forbidden}`)
      const direct = await call('tools/call', { name: forbidden, arguments: {} })
      assert.match(direct.error.message, new RegExp(`unknown tool ${forbidden}`))
      const via = await call('tools/call', { name: 'persona_call', arguments: { name: forbidden, arguments: {} } })
      assert.match(via.error.message, new RegExp(`unknown tool ${forbidden}`))
    }
  })
})


describe('MCP proxy process cleanup', { timeout: 15_000 }, () => {
  async function fixture(t, mode = '') {
    const dir = mkdtempSync(join(tmpdir(), 'persona-mcp-lifecycle-'))
    const events = join(dir, 'events')
    const source = `
      const fs = require('node:fs');
      const [events, mode] = process.argv.slice(1);
      for (const signal of ['SIGTERM', 'SIGINT']) process.on(signal, () => {
        fs.appendFileSync(events, signal + '\\n');
        process.exit(0);
      });
      setInterval(() => {}, 1000);
      process.stdin.on('data', () => process.stdout.write(JSON.stringify({ result: 'response' }) + '\\n'));
      if (mode === 'close-input') {
        process.stdin.once('close', () => {
          fs.closeSync(0);
          process.stdout.write(JSON.stringify({ result: { pid: process.pid } }) + '\\n');
        });
        process.stdin.destroy();
      } else process.stdout.write(JSON.stringify({ result: { pid: process.pid } }) + '\\n');
    `
    const proc = spawn(process.execPath, [PROXY, process.execPath, '-e', source, events, mode], { stdio: ['pipe', 'pipe', 'pipe'] })
    let childPid
    let stderr = ''
    proc.stderr.on('data', (chunk) => { stderr += chunk })
    proc.stdin.on('error', () => {})
    t.after(() => {
      proc.kill('SIGKILL')
      if (childPid) { try { process.kill(childPid, 'SIGKILL') } catch {} }
      rmSync(dir, { recursive: true, force: true })
    })
    const [ready] = await once(proc.stdout, 'data')
    childPid = JSON.parse(String(ready)).result.pid
    return { proc, childPid, events, stderr: () => stderr }
  }

  function exited(proc) {
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error('proxy did not exit')), 2000)
      proc.once('exit', (code, signal) => { clearTimeout(timer); resolve({ code, signal }) })
    })
  }

  function assertGone(pid) {
    assert.throws(() => process.kill(pid, 0), { code: 'ESRCH' })
  }

  for (const signal of ['SIGTERM', 'SIGINT']) {
    it(`forwards ${signal} and waits for the child to exit`, async (t) => {
      const { proc, childPid, events } = await fixture(t)
      const done = exited(proc)
      proc.kill(signal)
      await done
      assert.equal(existsSync(events) && readFileSync(events, 'utf8'), signal + '\n')
      assertGone(childPid)
    })
  }

  it('ends a server that stays alive after stdin closes', async (t) => {
    const { proc, childPid } = await fixture(t)
    const done = exited(proc)
    proc.stdin.end()
    await done
    assertGone(childPid)
  })

  it('handles a broken child input without an uncaught EPIPE or orphan', async (t) => {
    const { proc, childPid, stderr } = await fixture(t, 'close-input')
    const done = exited(proc)
    proc.stdin.write('request\n')
    await done
    assert.doesNotMatch(stderr(), /Unhandled 'error' event|throw er;/)
    assertGone(childPid)
  })

  it('cleans up when its output consumer closes the pipe', async (t) => {
    const { proc, childPid, stderr } = await fixture(t)
    const done = exited(proc)
    proc.stdout.destroy()
    proc.stdin.write('request\n')
    await done
    assert.doesNotMatch(stderr(), /Unhandled 'error' event|throw er;/)
    assertGone(childPid)
  })
})
