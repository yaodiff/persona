import assert from 'node:assert/strict'
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { createAgyRuntime } from '../lib/runtime.js'
import { POLICY_DIR_NAME, readPolicy } from '../lib/policy.js'
import { SESSION_STATE } from '../lib/session-state.js'

const FAKE = fileURLToPath(new URL('./fake-acp-policy.mjs', import.meta.url))
const INTERNAL = '11111111-2222-4333-8444-555555555555'
const TRANSCRIPT = '22222222-3333-4444-8555-666666666666'

async function fixture(t, runtimeOptions = {}) {
  const home = mkdtempSync(join(tmpdir(), 'agy-prepared-'))
  const agyDir = join(home, 'config')
  const cwd = join(home, 'project')
  const record = join(home, 'requests.jsonl')
  const binary = join(home, 'agy')
  const personad = join(home, 'personad')
  mkdirSync(cwd)
  writeFileSync(record, '')
  writeFileSync(binary, `#!/bin/sh\nexec '${process.execPath}' '${FAKE}' --record '${record}' --resume-default-model resume-default "$@"\n`)
  writeFileSync(personad, '#!/bin/sh\nexit 0\n')
  chmodSync(binary, 0o700)
  chmodSync(personad, 0o700)
  const runtimes = []
  t.after(async () => {
    await Promise.all(runtimes.map((runtime) => runtime.stop()))
    rmSync(home, { recursive: true, force: true })
  })
  const calls = () => readFileSync(record, 'utf8').split('\n').filter(Boolean).map(JSON.parse)
  const launch = async (overrides = {}) => {
    const node = await startFakeNode(t)
    const runtime = createAgyRuntime({ home, agyDir, agyBin: binary, personadBin: personad,
      socketPath: node.path, log: () => {}, exportPollMs: 20, exportSettleTimeoutMs: 100,
      ...runtimeOptions, ...overrides })
    runtimes.push(runtime)
    runtime.start()
    await node.ready
    await until('the initial inventory', () => node.last('runtime.sessions'))
    return { node, runtime }
  }
  const statePath = (session) => join(agyDir, 'brain', session.replace(/^agy-/, ''), SESSION_STATE)
  const state = (session) => JSON.parse(readFileSync(statePath(session), 'utf8'))
  const prepare = (node, params = {}) => node.peer.call('session.start', {
    cwd, model: 'test-model', effort: 'high', permission_mode: 'plan',
    supervision_instructions: 'Preserve the supervision policy.', ...params,
  })
  const writeTranscript = (rawId, text = 'A real previous input.') => {
    const logs = join(agyDir, 'brain', rawId, '.system_generated', 'logs')
    mkdirSync(logs, { recursive: true })
    writeFileSync(join(logs, 'transcript.jsonl'), [
      { step_index: 0, source: 'USER_EXPLICIT', type: 'USER_INPUT', status: 'DONE', content: text },
      { step_index: 1, source: 'MODEL', type: 'PLANNER_RESPONSE', status: 'DONE', content: 'A real reply.' },
    ].map(JSON.stringify).join('\n') + '\n')
  }
  return { home, agyDir, cwd, calls, launch, state, statePath, prepare, writeTranscript }
}

test('prepared empty generation survives a new runtime and the first handoff is prompted exactly once', { timeout: 15_000 }, async (t) => {
  const f = await fixture(t)
  const first = await f.launch()
  const session = (await f.prepare(first.node)).session.session_id
  const rawId = session.slice(4)
  const empty = await first.node.peer.call('session.items', { session_id: session })
  assert.deepEqual(empty.items, [])
  assert.equal(empty.to_seq, 0)
  assert.ok(empty.history, 'an empty generation has a history identity')
  const saved = f.state(session)
  assert.equal(saved.runtime, 'agy')
  assert.equal(saved.launcher, 'agy')
  assert.equal(saved.cwd, f.cwd)
  assert.equal(saved.model, 'test-model')
  assert.equal(saved.mode_info.effort, 'high')
  assert.equal(saved.mode_info.requestedMode, 'plan')
  assert.match(saved.supervision_policy, /^[a-f0-9]{64}$/)
  assert.equal(saved.awaiting_first_message, true)
  assert.equal(statSync(f.statePath(session)).mode & 0o777, 0o600)
  await first.runtime.stop()

  const before = f.calls().length
  const second = await f.launch({ maxSessions: 1, listDays: 0 })
  const row = second.node.last('runtime.sessions').sessions.find((row) => row.session_id === session)
  assert.ok(row, 'prepared identities stay pinned even outside the native recent window')
  assert.equal(row.status, 'idle')
  assert.equal(row.model, 'test-model')
  assert.equal(row.effort, 'high')
  assert.equal(row.permission_mode, 'plan')
  const again = await second.node.peer.call('session.items', { session_id: session })
  assert.deepEqual(again, empty, 'reading and restart preserve the empty history generation')
  assert.equal(f.calls().slice(before).some((call) => ['session/new', 'session/resume', 'session/prompt'].includes(call.method)), false)

  const input = { session_id: session, client_msg_id: 'segment-once:first', text: 'Continue the task.', context: 'The previous segment handoff.' }
  await Promise.all([second.node.peer.call('session.send', input), second.node.peer.call('session.send', input)])
  await until('the first handoff ends', () => f.calls().some((call) => call.ended === rawId))
  assert.equal(f.calls().filter((call) => call.method === 'session/new').length, 1, 'resume keeps the native ID')
  const after = f.calls().slice(before)
  const prompts = after.filter((call) => call.method === 'session/prompt')
  assert.equal(prompts.length, 1)
  assert.equal(prompts[0].params.sessionId, rawId)
  assert.equal(prompts[0].params.prompt.filter((block) => block.type === 'resource').length, 1)
  assert.match(prompts[0].params.prompt.find((block) => block.type === 'resource').resource.text, /previous segment handoff/)
  const resume = after.find((call) => call.method === 'session/resume')
  assert.equal(resume.params.cwd, f.cwd)
  assert.deepEqual(resume.params.additionalDirectories, [join(f.agyDir, POLICY_DIR_NAME, rawId)])
  assert.ok(resume.params.mcpServers.some((server) => server.env.some((env) => env.name === 'PERSONA_SESSION_ID' && env.value === session)))
  assert.ok(after.some((call) => call.method === 'session/set_model' && call.params.modelId === 'test-model'))
  assert.ok(after.some((call) => call.method === 'session/set_mode' && call.params.modeId === 'plan'))
  assert.ok(after.some((call) => call.method === 'session/set_config_option' && call.params.configId === 'thought_level' && call.params.value === 'high'))
  assert.equal(readPolicy(join(f.agyDir, POLICY_DIR_NAME), rawId), 'Preserve the supervision policy.')
  assert.equal(f.state(session).awaiting_first_message, false)
})

test('native internal empty sessions are isolated while real transcripts remain readable', { timeout: 15_000 }, async (t) => {
  const f = await fixture(t)
  mkdirSync(join(f.agyDir, 'brain', INTERNAL), { recursive: true })
  mkdirSync(join(f.agyDir, 'cache'), { recursive: true })
  writeFileSync(join(f.agyDir, 'cache', 'conversation_metadata.json'), JSON.stringify({ conversations: { [INTERNAL]: { is_internal: true } } }))
  const db = new DatabaseSync(join(f.agyDir, 'conversation_summaries.db'))
  db.exec('CREATE TABLE conversation_summaries (conversation_id TEXT PRIMARY KEY, status TEXT, not_fully_idle INTEGER, killed INTEGER, step_count INTEGER, title TEXT, workspace_uris TEXT, last_modified_time TEXT)')
  db.prepare('INSERT INTO conversation_summaries VALUES (?, ?, 0, 0, 0, ?, ?, ?)')
    .run(INTERNAL, 'CASCADE_RUN_STATUS_IDLE', '', '[]', new Date().toISOString())
  db.close()
  f.writeTranscript(TRANSCRIPT)
  const first = await f.launch()
  const session = (await f.prepare(first.node)).session.session_id
  await first.runtime.stop()
  const second = await f.launch()
  const rows = second.node.last('runtime.sessions').sessions
  assert.ok(rows.some((row) => row.session_id === session))
  assert.equal(rows.some((row) => row.session_id === `agy-${INTERNAL}`), false)
  assert.ok(rows.some((row) => row.session_id === `agy-${TRANSCRIPT}`))
  const history = await second.node.peer.call('session.items', { session_id: `agy-${TRANSCRIPT}` })
  assert.equal(history.to_seq, 2)
  assert.deepEqual(history.items.map((item) => item.text), ['A real previous input.', 'A real reply.'])
  assert.equal(f.calls().filter((call) => call.method === 'session/new').length, 1)
})

test('an unfinished input without a transcript stays visible after restart and is never replayed or recreated', { timeout: 15_000 }, async (t) => {
  const f = await fixture(t)
  const first = await f.launch()
  const session = (await f.prepare(first.node)).session.session_id
  await first.node.peer.call('session.send', { session_id: session, client_msg_id: 'in-doubt-input', text: 'hold this input', context: 'Do not replay this handoff.' })
  await until('the pending prompt reaches ACP', () => f.calls().some((call) => call.method === 'session/prompt'))
  assert.equal(f.state(session).awaiting_first_message, false)
  await first.runtime.stop()
  const second = await f.launch()
  assert.ok(second.node.last('runtime.sessions').sessions.some((row) => row.session_id === session))
  const empty = await second.node.peer.call('session.items', { session_id: session })
  assert.deepEqual(empty.items, [])
  assert.equal(f.state(session).awaiting_first_message, false, 'empty history cannot erase dispatch evidence')
  assert.equal(f.calls().filter((call) => call.method === 'session/new').length, 1)
  assert.equal(f.calls().filter((call) => call.method === 'session/prompt').length, 1)
})

test('a prepared identity with a real transcript never resets that history or infers zero turns from its marker', { timeout: 15_000 }, async (t) => {
  const f = await fixture(t)
  const first = await f.launch()
  const session = (await f.prepare(first.node)).session.session_id
  await first.runtime.stop()
  f.writeTranscript(session.slice(4), 'Input received outside the companion.')
  const second = await f.launch()
  const page = await second.node.peer.call('session.items', { session_id: session })
  assert.equal(page.to_seq, 2)
  assert.equal(page.items[0].text, 'Input received outside the companion.')
  assert.equal(f.state(session).awaiting_first_message, false)
  assert.equal(f.calls().filter((call) => call.method === 'session/new').length, 1)
  assert.equal(f.calls().some((call) => call.method === 'session/prompt'), false)
})

test('deferred starts restore their identity and activation state without persisting or replaying the pending prompt', { timeout: 15_000 }, async (t) => {
  const f = await fixture(t)
  const first = await f.launch()
  const session = (await f.prepare(first.node, { defer_mcp: true, prompt: 'Private pending task.', client_msg_id: 'deferred-first' })).session.session_id
  assert.equal(f.state(session).deferred, true)
  assert.equal(readFileSync(f.statePath(session), 'utf8').includes('Private pending task.'), false)
  await first.runtime.stop()
  const second = await f.launch()
  assert.equal(second.node.last('runtime.sessions').sessions.find((row) => row.session_id === session).starting, true)
  assert.equal(f.calls().some((call) => call.method === 'session/prompt'), false)
  await second.node.peer.call('session.send', { session_id: session, text: 'Activate once.', client_msg_id: 'deferred-first' })
  await until('activation completes', () => f.calls().some((call) => call.ended === session.slice(4)))
  assert.equal(f.calls().filter((call) => call.method === 'session/prompt').length, 1)
  assert.equal(f.state(session).deferred, false)
})

test('assistant metadata and changed run options survive restart before the first input', { timeout: 15_000 }, async (t) => {
  const f = await fixture(t)
  const first = await f.launch()
  const session = (await f.prepare(first.node, { assistant: true })).session.session_id
  await first.node.peer.call('session.modes', { session_id: session, permission_mode: 'bypassPermissions', effort: 'low' })
  await first.runtime.stop()
  const before = f.calls().length
  const second = await f.launch()
  const row = second.node.last('runtime.sessions').sessions.find((row) => row.session_id === session)
  assert.deepEqual(row.capabilities, { rewind: false })
  assert.equal(row.permission_mode, 'bypassPermissions')
  assert.equal(row.effort, 'low')
  assert.equal(f.state(session).assistant, true)
  assert.equal(f.calls().slice(before).some((call) => call.method === 'session/resume'), false)
  await second.node.peer.call('session.send', { session_id: session, text: 'Resume the assistant.', client_msg_id: 'assistant-first' })
  await until('the assistant input completes', () => f.calls().some((call) => call.ended === session.slice(4)))
  assert.equal(f.calls().filter((call) => call.method === 'session/new').length, 1)
  assert.equal(f.calls().filter((call) => call.method === 'session/prompt').length, 1)
})

test('closed, malformed, and differently scoped markers do not resurrect empty sessions', { timeout: 15_000 }, async (t) => {
  const f = await fixture(t)
  const first = await f.launch()
  const closed = (await f.prepare(first.node)).session.session_id
  const malformed = (await f.prepare(first.node)).session.session_id
  const other = (await f.prepare(first.node)).session.session_id
  await first.node.peer.call('session.close', { session_id: closed })
  await first.runtime.stop()
  writeFileSync(f.statePath(malformed), '{incomplete')
  writeFileSync(f.statePath(other), JSON.stringify({ ...f.state(other), runtime: 'other-runtime' }))
  const second = await f.launch()
  assert.deepEqual(second.node.last('runtime.sessions').sessions, [])
  await assert.rejects(second.node.peer.call('session.send', { session_id: closed, text: 'Must stay closed.' }), /closed/)
  assert.equal(f.calls().some((call) => call.method === 'session/prompt'), false)
})

test('a saved channel routes through fresh node credentials instead of silently resuming on native OAuth', { timeout: 15_000 }, async (t) => {
  const f = await fixture(t)
  const first = await f.launch()
  const channel = { id: 'corp', auth_mode: 'oauth', rev: 'r1', env: { EXAMPLE_EXTENSION_CREDENTIAL: 'private-credential' } }
  const session = (await f.prepare(first.node, { channel, env: { EXTRA_TOKEN: 'private-environment' } })).session.session_id
  const encoded = readFileSync(f.statePath(session), 'utf8')
  assert.equal(encoded.includes('private-credential'), false)
  assert.equal(encoded.includes('private-environment'), false)
  await first.runtime.stop()
  const second = await f.launch()
  await assert.rejects(second.node.peer.call('session.send', { session_id: session, text: 'Need the configured channel.' }), (error) => error.code === 'channel_required')
  await second.node.peer.call('session.send', { session_id: session, text: 'Use the fresh channel.', channel })
  await until('the channel prompt completes', () => f.calls().some((call) => call.ended === session.slice(4)))
  assert.equal(f.calls().filter((call) => call.method === 'session/prompt').length, 1)
  assert.equal(f.calls().filter((call) => call.method === 'session/new').length, 1)
})

test('a missing durable supervision policy blocks resume until the node supplies it explicitly', { timeout: 15_000 }, async (t) => {
  const f = await fixture(t)
  const first = await f.launch()
  const session = (await f.prepare(first.node)).session.session_id
  await first.runtime.stop()
  rmSync(join(f.agyDir, POLICY_DIR_NAME, session.slice(4)), { recursive: true })
  const second = await f.launch()
  await assert.rejects(second.node.peer.call('session.send', { session_id: session, text: 'Need the saved policy.' }),
    (error) => error.code === 'assistant_supervision_runtime')
  assert.equal(f.state(session).awaiting_first_message, true)
  assert.equal(f.calls().some((call) => call.method === 'session/prompt'), false)
  await second.node.peer.call('session.send', { session_id: session, text: 'The node restores policy.',
    supervision_instructions: 'Preserve the supervision policy.' })
  await until('the policy-restored input completes', () => f.calls().some((call) => call.ended === session.slice(4)))
  assert.equal(f.calls().filter((call) => call.method === 'session/prompt').length, 1)
})

test('a prepared model that cannot be restored fails before input instead of running on the resume default', { timeout: 15_000 }, async (t) => {
  const f = await fixture(t)
  const first = await f.launch()
  const session = (await f.prepare(first.node)).session.session_id
  await first.runtime.stop()
  const second = await f.launch({ agyArgs: ['acp', '--reject-model', 'test-model'] })
  await assert.rejects(second.node.peer.call('session.send', { session_id: session, text: 'Keep the prepared model.' }), /Unknown model test-model/)
  assert.equal(f.state(session).awaiting_first_message, true)
  assert.equal(f.calls().some((call) => call.method === 'session/prompt'), false)
  assert.equal(f.calls().filter((call) => call.method === 'session/new').length, 1)
})

test('prepare cannot acknowledge an identity whose durable marker could not be saved', { timeout: 15_000 }, async (t) => {
  const f = await fixture(t)
  mkdirSync(f.agyDir, { recursive: true })
  writeFileSync(join(f.agyDir, 'brain'), 'blocked directory')
  const { node } = await f.launch()
  await assert.rejects(f.prepare(node), /Invalid Persona session directory/)
  assert.equal(f.calls().filter((call) => call.method === 'session/new').length, 1)
  assert.equal(f.calls().filter((call) => call.method === 'session/close').length, 1)
  assert.equal(f.calls().some((call) => call.method === 'session/prompt'), false)
})
