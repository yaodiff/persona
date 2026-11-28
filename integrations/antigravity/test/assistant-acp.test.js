import assert from 'node:assert/strict'
import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { delimiter, join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'
import { test } from 'node:test'
import { setTimeout as delay } from 'node:timers/promises'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { toAgyId } from '../lib/map.js'
import { createAgyRuntime } from '../lib/runtime.js'
import '../lib/prompt-order.js'

const fake = fileURLToPath(new URL('./fake-acp-policy.mjs', import.meta.url))
const PNG = 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jB9sAAAAASUVORK5CYII='
const BAD_PNG = Buffer.from(PNG, 'base64').subarray(0, 8).toString('base64')

async function fixture(t, options = {}) {
  const home = mkdtempSync(join(tmpdir(), 'agy-isolation-'))
  const binary = join(home, 'agy')
  const record = join(home, 'requests.jsonl')
  const assistantDir = join(home, '.persona', 'assistant')
  mkdirSync(assistantDir, { recursive: true })
  writeFileSync(record, '')
  writeFileSync(binary, `#!/bin/sh\nexec '${process.execPath}' '${fake}' --record '${record}' "$@"\n`, { mode: 0o700 })
  const node = await startFakeNode(t, { features: ['assistant_pids'] })
  const runtime = createAgyRuntime({ home, agyBin: binary, personadBin: binary, socketPath: node.path, log: () => {}, ...options })
  t.after(async () => { await runtime.stop(); rmSync(home, { recursive: true, force: true }) })
  await runtime.start()
  await node.ready
  const calls = () => readFileSync(record, 'utf8').trim().split('\n').filter(Boolean).map(JSON.parse)
  const pids = () => node.last('runtime.assistant_pids')?.pids ?? {}
  const start = async (params) => (await node.peer.call('session.start', { cwd: home, ...params })).session.session_id
  const call = (method, session_id, params = {}) => node.peer.call(`session.${method}`, { session_id, ...params })
  const request = (method, session) => calls().findLast((entry) => entry.method === method && entry.params?.sessionId === toAgyId(session))
  return { node, home, runtime, assistantDir, calls, pids, start, call, request }
}

test('Assistant context is embedded data, never a slash command appended to the first or next request', async (t) => {
  const { calls, start, call, request } = await fixture(t)
  const session = await start({ assistant: true })
  // Task titles in a snapshot can mention commands for completely different conversations.
  const snapshot = '<persona-context>\n- Fix an update offer before /compact and preserve /model picks.\n</persona-context>'
  const messages = ['Check whether this provider supports a usage source.', 'Keep the existing provider configuration.']
  for (const [index, text] of messages.entries()) {
    await call('send', session, { assistant: true, text, context: snapshot })
    await until('prompt completed', () => calls().filter((entry) => entry.ended === toAgyId(session)).length === index + 1)
    const blocks = request('session/prompt', session).params.prompt
    assert.deepEqual(blocks, [
      { type: 'resource', resource: { uri: 'persona-context:///snapshot.txt', mimeType: 'text/plain', text: snapshot } },
      { type: 'text', text },
    ])
    assert.equal(blocks.filter((block) => block.type === 'text').some((block) => block.text.includes('/compact')), false)
  }
  // The owner's explicit native command still travels alone and can be interpreted by agy.
  await call('send', session, { assistant: true, text: '/compact' })
  await until('manual command completed', () => calls().filter((entry) => entry.ended === toAgyId(session)).length === 3)
  assert.deepEqual(request('session/prompt', session).params.prompt, [{ type: 'text', text: '/compact' }])
})

test('agy workflow commands keep their first text block at byte 0 and put context resources last', async (t) => {
  const { calls, start, call, request } = await fixture(t)
  const session = await start({ assistant: true })
  const snapshot = '<persona-context>\ncontext for the workflow\n</persona-context>'

  await call('send', session, {
    assistant: true,
    text: ' \t/boost fix the race\nkeep the body',
    context: snapshot,
    images: [
      { mime: 'image/png', data: PNG },
      { mime: 'image/png', data: BAD_PNG },
    ],
  })
  await until('workflow prompt completed', () => calls().some((entry) => entry.ended === toAgyId(session)))
  const blocks = request('session/prompt', session).params.prompt
  assert.deepEqual(blocks.map((block) => block.type), ['text', 'image', 'text', 'resource'])
  assert.equal(blocks[0].text, '/boost fix the race\nkeep the body')
  assert.match(blocks[2].text, /incomplete or damaged/)
  assert.deepEqual(blocks[3].resource.text, snapshot)

  await call('send', session, { assistant: true, text: '/tmp/report.md', context: snapshot })
  await until('path prompt completed', () => calls().filter((entry) => entry.ended === toAgyId(session)).length === 2)
  assert.deepEqual(request('session/prompt', session).params.prompt, [
    { type: 'resource', resource: { uri: 'persona-context:///snapshot.txt', mimeType: 'text/plain', text: snapshot } },
    { type: 'text', text: '/tmp/report.md' },
  ])
})

test('plain Assistant snapshots are wrapped as resource content without changing user whitespace', async (t) => {
  const { calls, start, call, request } = await fixture(t)
  const session = await start({ assistant: true })
  const text = '  First request.\nKeep this line.  '
  await call('send', session, { assistant: true, text, context: '  A note about /compact in another task.  ' })
  await until('prompt completed', () => calls().some((entry) => entry.ended === toAgyId(session)))
  assert.deepEqual(request('session/prompt', session).params.prompt, [
    { type: 'resource', resource: { uri: 'persona-context:///snapshot.txt', mimeType: 'text/plain', text: '<persona-context>\nA note about /compact in another task.\n</persona-context>' } },
    { type: 'text', text },
  ])
})

test('Agents share their own worker; a stall, recycle, or crash there does not interrupt an Assistant', async (t) => {
  const { node, runtime, assistantDir, calls, pids, start, call, request } = await fixture(t, { promptInactivityMs: 200 })
  const assistant = await start({ cwd: assistantDir })
  const otherAssistant = await start({ assistant: true })
  const agent = await start()
  const otherAgent = await start()
  await until('both assistant workers reported', () => pids()[assistant] && pids()[otherAssistant])
  const workers = { ...pids() }
  assert.notEqual(workers[assistant], workers[otherAssistant])
  const shared = request('session/resume', agent).worker
  assert.equal(request('session/resume', otherAgent).worker, shared)
  assert.ok(!Object.values(workers).includes(shared))
  assert.equal(calls().filter((entry) => entry.started).length, 3)

  const permission = Promise.withResolvers()
  node.resolve = () => permission.promise
  await call('send', assistant, { text: 'ask' })
  await until('Assistant permission', () => node.asked.some((card) => card.session_id === assistant))
  await call('send', agent, { text: 'hold' })
  await until('Agent inactivity reported', () => node.last('runtime.sessions')?.sessions.some((row) => row.session_id === agent && row.turn_error))
  await call('modes', otherAgent, { model: 'test-model' })
  const recycled = request('session/set_model', otherAgent).worker
  assert.notEqual(recycled, shared)
  assert.throws(() => process.kill(shared, 0), { code: 'ESRCH' })
  process.kill(recycled, 'SIGKILL')
  await until('crashed Agent worker replaced', async () => {
    try {
      await call('modes', agent, { model: 'test-model' })
      return request('session/set_model', agent).worker !== recycled
    } catch (error) {
      if (!/agy acp (exited|process|stdin)|write EPIPE/.test(error.message)) throw error
      return false
    }
  })

  assert.deepEqual(pids(), workers)
  await call('modes', otherAssistant, { model: 'test-model' })
  assert.equal(request('session/set_model', otherAssistant).worker, workers[otherAssistant])
  assert.equal(request('session/prompt', assistant).worker, workers[assistant])
  assert.equal((await call('interrupt', assistant)).settled, true)
  assert.equal(request('session/cancel', assistant).worker, workers[assistant])
  permission.resolve({ allow: true })
  await runtime.stop()
  await until('all stop-time PID reports cleared', () => Object.keys(pids()).length === 0)
  for (const entry of calls().filter((entry) => entry.started)) assert.throws(() => process.kill(entry.started, 0), { code: 'ESRCH' })
})

test('idle reaping spares active prompts and even permissions requested outside a turn', async (t) => {
  const { node, pids, start, call, request, assistantDir } = await fixture(t, { assistantAcpIdleMs: 350, promptInactivityMs: 0 })
  const pending = Promise.withResolvers()
  node.resolve = () => pending.promise
  const busy = await start({ cwd: assistantDir, prompt: 'hold' })
  const waiting = await start({ cwd: assistantDir })
  await call('modes', waiting, { effort: 'ask-idle' })
  await until('permission outside a prompt', () => node.asked.some((card) => card.session_id === waiting))
  assert.equal(request('session/prompt', waiting), undefined)
  const idle = await start({ cwd: assistantDir })
  const oldPID = await until('idle PID reported', () => pids()[idle])
  const busyPID = pids()[busy]
  const waitingPID = pids()[waiting]
  await until('only the idle worker reaped', () => !pids()[idle])
  await until('idle process gone', () => {
    try { process.kill(oldPID, 0); return false } catch (error) { assert.equal(error.code, 'ESRCH'); return true }
  })
  assert.deepEqual(pids(), { [busy]: busyPID, [waiting]: waitingPID })
  assert.equal((await call('interrupt', busy)).settled, true)
  pending.resolve({ allow: true })
  await call('modes', idle, { model: 'test-model' })
  const resumedPID = await until('reloaded worker reported', () => pids()[idle])
  assert.notEqual(resumedPID, oldPID)
  const resumed = request('session/resume', idle)
  assert.equal(resumed.worker, resumedPID)
  assert.ok(resumed.params.mcpServers[0].env.some(({ name, value }) => name === 'PERSONA_ASSISTANT' && value === '1'))
  await until('interrupted and approved workers also reaped', () => !pids()[busy] && !pids()[waiting])
})

test('an unexpected Assistant worker exit clears just its PID; the next send resumes on a fresh worker', async (t) => {
  const { pids, start, call, request } = await fixture(t)
  const crashed = await start({ assistant: true })
  const stable = await start({ assistant: true })
  const stablePID = await until('stable worker reported', () => pids()[stable])
  const crashedPID = pids()[crashed]
  process.kill(crashedPID, 'SIGKILL')
  await until('only crashed worker cleared', () => !pids()[crashed])
  assert.deepEqual(pids(), { [stable]: stablePID })
  await call('send', crashed, { text: 'continue' })
  const restarted = await until('replacement reported', () => pids()[crashed])
  assert.notEqual(restarted, crashedPID)
  assert.equal((await until('prompt reaches the replacement', () => request('session/prompt', crashed))).worker, restarted)
  await call('close', crashed)
  await until('closed worker removed', () => !pids()[crashed])
  assert.deepEqual(pids(), { [stable]: stablePID })
  assert.throws(() => process.kill(restarted, 0), { code: 'ESRCH' })
})

// Idle Assistant workers are reaped on their own idle timer even while a coding Agent runs on the shared worker,
// so unrelated coding work cannot pin every idle Assistant ACP process in memory indefinitely.
test('an Agent turn running in this companion does not pin idle Assistant workers', async (t) => {
  const { pids, start, call, assistantDir } = await fixture(t, { assistantAcpIdleMs: 350, promptInactivityMs: 0 })
  const agent = await start({ prompt: 'hold' })
  const assistant = await start({ cwd: assistantDir })
  await until('assistant worker reported', () => pids()[assistant])
  await until('idle Assistant worker retired while an Agent turn runs', () => !pids()[assistant], 5000)
  assert.equal((await call('interrupt', agent)).settled, true)
})

test('a send during idle retirement waits for the old worker to exit before starting a replacement', async (t) => {
  const { pids, start, call, calls, request } = await fixture(t, {
    agyArgs: ['--ignore-term'], assistantAcpIdleMs: 350,
  })
  const session = await start({ assistant: true })
  const oldPID = await until('original worker reported', () => pids()[session])
  await until('old worker is retiring', () => !pids()[session])
  process.kill(oldPID, 0)
  await call('send', session, { text: 'resume' })
  assert.throws(() => process.kill(oldPID, 0), { code: 'ESRCH' })
  const resumed = await until('replacement worker receives prompt', () => request('session/prompt', session))
  assert.notEqual(resumed.worker, oldPID)
  assert.deepEqual(calls().filter((entry) => entry.started).map((entry) => entry.started), [oldPID, resumed.worker])
  process.kill(resumed.worker, 'SIGKILL')
})

test('the generic Assistant environment reaches only its dedicated worker and each replacement', async (t) => {
  const { pids, start, call, calls, assistantDir } = await fixture(t, { assistantAcpIdleMs: 250, promptInactivityMs: 0 })
  const bin = join(assistantDir, 'fake-extension-bin')
  mkdirSync(bin, { recursive: true })
  writeFileSync(join(bin, 'fake-extension-cli'), '#!/bin/sh\nprintf fake\n', { mode: 0o700 })
  const environment = (handle) => ({
    PATH: `${bin}${delimiter}${process.env.PATH || ''}`,
    EXAMPLE_EXTENSION_CREDENTIAL: handle,
    EXAMPLE_EXTENSION_ACCOUNT: '2',
  })
  const latestWorker = () => calls().filter((entry) => entry.started).at(-1)
  const assertEnvironment = (handle) => {
    const started = latestWorker()
    assert.equal(started.fakeExtensionCredential, handle)
    assert.equal(started.fakeExtensionAccount, '2')
    assert.equal(started.path, environment(handle).PATH)
  }

  const assistant = await start({ assistant: true, env: environment('fake-credential-handle') })
  assertEnvironment('fake-credential-handle')
  await start({ env: environment('must-not-reach-an-agent') })
  assert.equal(latestWorker().fakeExtensionCredential, null)
  assert.notEqual(latestWorker().path, environment('must-not-reach-an-agent').PATH)

  await until('first Assistant worker retired', () => !pids()[assistant])
  await call('send', assistant, { text: 'resume', env: environment('fake-credential-handle-2') })
  assertEnvironment('fake-credential-handle-2')

  process.kill(pids()[assistant], 'SIGKILL')
  await until('crashed Assistant worker cleared', () => !pids()[assistant])
  await call('send', assistant, { text: 'restart', env: environment('fake-credential-handle-3') })
  assertEnvironment('fake-credential-handle-3')

  await until('second Assistant worker retired', () => !pids()[assistant])
  await call('modes', assistant, { model: 'test-model', env: environment('fake-credential-handle-4') })
  assertEnvironment('fake-credential-handle-4')
})

test('shared Agent worker exit withdraws pending Agent permission asks and retires autonomous Agent turns without disturbing Assistants', async (t) => {
  const { node, runtime, assistantDir, start, call, request } = await fixture(t, { promptInactivityMs: 0 })
  const agent = await start()
  const assistant = await start({ cwd: assistantDir })
  const sharedPid = request('session/resume', agent).worker
  const assistantAsk = Promise.withResolvers()
  const agentAsk = Promise.withResolvers()
  t.after(() => {
    assistantAsk.resolve({ dismiss: true })
    agentAsk.resolve({ dismiss: true })
  })
  node.resolve = (ix) => (ix.session_id === assistant ? assistantAsk.promise : agentAsk.promise)

  await call('modes', assistant, { effort: 'ask-idle' })
  const assistantCard = await until('Assistant permission card', () => node.asked.find((card) => card.session_id === assistant))

  const rowOf = (sid) => node.last('runtime.sessions')?.sessions.find((row) => row.session_id === sid)
  await call('modes', agent, { effort: 'wake-idle' })
  await until('Agent autonomous turn marked working', () => rowOf(agent)?.status === 'working')
  await call('modes', agent, { effort: 'ask-idle' })
  const agentCard = await until('Agent autonomous permission card', () => node.asked.find((card) => card.session_id === agent))
  assert.equal(runtime.idle(), false)

  process.kill(sharedPid, 'SIGKILL')

  await until('Agent permission card withdrawn on shared worker exit', () =>
    node.all('interaction.cancel').some((ev) => ev.id === agentCard.id))
  await until('Agent autonomous turn retired on shared worker exit', () => rowOf(agent)?.status !== 'working' && rowOf(agent)?.status !== 'blocked')
  assert.equal(node.all('interaction.cancel').some((ev) => ev.id === assistantCard.id), false,
    'Assistant permission card must not be withdrawn when shared Agent worker exits')

  assistantAsk.resolve({ allow: true })
  await until('runtime returns to idle after shared worker crash', () => runtime.idle())
})

test('shared Agent worker exit immediately retires an autonomous Agent turn without pending permissions', async (t) => {
  const { node, runtime, start, call, request } = await fixture(t, { promptInactivityMs: 0 })
  const agent = await start()
  const sharedPid = request('session/resume', agent).worker
  const rowOf = (sid) => node.last('runtime.sessions')?.sessions.find((row) => row.session_id === sid)

  await call('modes', agent, { effort: 'wake-idle' })
  await until('Agent autonomous turn marked working', () => rowOf(agent)?.status === 'working')
  assert.equal(runtime.idle(), false)

  process.kill(sharedPid, 'SIGKILL')

  await until('Agent autonomous turn retired on shared worker exit', () => rowOf(agent)?.status !== 'working')
  await until('runtime returns to idle after shared worker crash', () => runtime.idle())
})

test('shared Agent worker exit clears stale RUNNING native summary when retiring an autonomous turn', async (t) => {
  const agyDir = mkdtempSync(join(tmpdir(), 'agy-summary-'))
  t.after(() => rmSync(agyDir, { recursive: true, force: true }))
  const { node, runtime, start, call, request } = await fixture(t, { agyDir, promptInactivityMs: 0 })
  const agent = await start()
  const rawId = toAgyId(agent)
  mkdirSync(join(agyDir, 'brain', rawId), { recursive: true })
  const sharedPid = request('session/resume', agent).worker
  const rowOf = (sid) => node.last('runtime.sessions')?.sessions.find((row) => row.session_id === sid)

  await call('modes', agent, { effort: 'wake-idle' })
  await until('Agent autonomous turn marked working', () => !runtime.idle() && rowOf(agent)?.status === 'working')

  const db = new DatabaseSync(join(agyDir, 'conversation_summaries.db'))
  try {
    db.exec('CREATE TABLE IF NOT EXISTS conversation_summaries (conversation_id TEXT PRIMARY KEY, status TEXT, not_fully_idle INTEGER, killed INTEGER, step_count INTEGER, title TEXT, workspace_uris TEXT, last_modified_time TEXT)')
    db.prepare('INSERT OR REPLACE INTO conversation_summaries VALUES (?, ?, 0, 0, ?, ?, ?, ?)')
      .run(rawId, 'CASCADE_RUN_STATUS_RUNNING', 2, '', '[]', new Date().toISOString())
  } finally {
    db.close()
  }

  process.kill(sharedPid, 'SIGKILL')

  await until('Agent exits working despite stale RUNNING summary', () => rowOf(agent)?.status !== 'working')
  await until('runtime returns to idle after shared worker crash', () => runtime.idle())
})

test('shared Agent worker exit withdraws a pending ask even after the session is promoted to Assistant', async (t) => {
  const { node, runtime, start, call, request } = await fixture(t, { promptInactivityMs: 0 })
  const session = await start()
  const sharedPid = request('session/resume', session).worker
  const pendingAsk = Promise.withResolvers()
  t.after(() => pendingAsk.resolve({ dismiss: true }))
  node.resolve = () => pendingAsk.promise

  await call('modes', session, { effort: 'ask-idle' })
  const card = await until('shared worker permission card', () => node.asked.find((ix) => ix.session_id === session))
  assert.equal(runtime.idle(), false)

  // Promote the idle session to a dedicated Assistant worker while the old shared ask is still open.
  await call('modes', session, { assistant: true, model: 'test-model' })

  process.kill(sharedPid, 'SIGKILL')

  await until('shared worker permission card withdrawn after Assistant promotion', () =>
    node.all('interaction.cancel').some((ev) => ev.id === card.id))
  await until('runtime returns to idle after dead shared worker ask is withdrawn', () => runtime.idle())
})

test('assistant sessions advertise capabilities.rewind = false while ordinary sessions keep rewind enabled', async (t) => {
  const { node, home, assistantDir, call } = await fixture(t)
  const startFull = (params = {}) => node.peer.call('session.start', { cwd: home, ...params })
  const rowOf = (sid) => node.last('runtime.sessions')?.sessions.find((row) => row.session_id === sid)

  const assistantByDir = (await startFull({ cwd: assistantDir })).session
  const assistantByFlag = (await startFull({ assistant: true })).session
  const ordinary = (await startFull()).session

  assert.equal(assistantByDir.capabilities?.rewind, false, 'assistant dir start must advertise rewind = false')
  assert.equal(assistantByFlag.capabilities?.rewind, false, 'explicit assistant start must advertise rewind = false')
  assert.notEqual(ordinary.capabilities?.rewind, false, 'ordinary session start must not disable rewind')

  await until('all three sessions listed', () =>
    rowOf(assistantByDir.session_id) && rowOf(assistantByFlag.session_id) && rowOf(ordinary.session_id))
  assert.equal(rowOf(assistantByDir.session_id).capabilities?.rewind, false)
  assert.equal(rowOf(assistantByFlag.session_id).capabilities?.rewind, false)
  assert.notEqual(rowOf(ordinary.session_id).capabilities?.rewind, false)

  // Assistant rewind still fails closed with rewind_unsupported.
  await assert.rejects(
    call('rewind', assistantByDir.session_id, { prompt: 'hello', occurrence: 1, restore: 'conversation' }),
    (err) => err.code === 'rewind_unsupported',
  )

  // Promoting an ordinary session to Assistant updates its listed row to rewind = false.
  await call('modes', ordinary.session_id, { assistant: true, model: 'test-model' })
  await until('promoted assistant row advertises rewind = false', () => rowOf(ordinary.session_id)?.capabilities?.rewind === false)
})

test('assistant sessions discovered from disk advertise capabilities.rewind = false while ordinary sessions do not', async (t) => {
  const agyDir = mkdtempSync(join(tmpdir(), 'agy-disk-rewind-'))
  t.after(() => rmSync(agyDir, { recursive: true, force: true }))
  const home = mkdtempSync(join(tmpdir(), 'agy-disk-home-'))
  t.after(() => rmSync(home, { recursive: true, force: true }))
  const assistantDir = join(home, '.persona', 'assistant')
  const projectDir = join(home, 'workspace')
  mkdirSync(assistantDir, { recursive: true })
  mkdirSync(projectDir, { recursive: true })
  const asstId = '11111111-2222-4333-8444-555555555551'
  const agentId = '11111111-2222-4333-8444-555555555552'
  for (const rawId of [asstId, agentId]) {
    const logs = join(agyDir, 'brain', rawId, '.system_generated', 'logs')
    mkdirSync(logs, { recursive: true })
    writeFileSync(join(logs, 'transcript_full.jsonl'), JSON.stringify({
      step_index: 0, type: 'USER_INPUT', source: 'USER_EXPLICIT', status: 'DONE', created_at: '2026-01-01T00:00:00Z', content: 'hi',
    }) + '\n')
  }
  const db = new DatabaseSync(join(agyDir, 'conversation_summaries.db'))
  try {
    db.exec('CREATE TABLE conversation_summaries (conversation_id TEXT PRIMARY KEY, status TEXT, not_fully_idle INTEGER, killed INTEGER, step_count INTEGER, title TEXT, workspace_uris TEXT, last_modified_time TEXT)')
    const now = new Date().toISOString()
    db.prepare('INSERT INTO conversation_summaries VALUES (?, ?, 0, 0, 1, ?, ?, ?)').run(asstId, 'CASCADE_RUN_STATUS_IDLE', 'Assistant', JSON.stringify([pathToFileURL(assistantDir).href]), now)
    db.prepare('INSERT INTO conversation_summaries VALUES (?, ?, 0, 0, 1, ?, ?, ?)').run(agentId, 'CASCADE_RUN_STATUS_IDLE', 'Agent', JSON.stringify([pathToFileURL(projectDir).href]), now)
  } finally {
    db.close()
  }
  const node = await startFakeNode(t)
  const runtime = createAgyRuntime({ home, agyDir, agyBin: '/missing', personadBin: '/missing', socketPath: node.path, log: () => {} })
  t.after(() => runtime.stop())
  runtime.start()
  await node.ready
  const rowOf = (rawId) => node.last('runtime.sessions')?.sessions.find((row) => row.session_id === `agy-${rawId}`)
  await until('both disk sessions listed', () => rowOf(asstId) && rowOf(agentId))
  assert.equal(rowOf(asstId).capabilities?.rewind, false)
  assert.notEqual(rowOf(agentId).capabilities?.rewind, false)
})

test('idle reaping and companion restart restore the session model after session/resume resets it', async (t) => {
  const home = mkdtempSync(join(tmpdir(), 'agy-model-reload-'))
  const binary = join(home, 'agy')
  const record = join(home, 'requests.jsonl')
  const assistantDir = join(home, '.persona', 'assistant')
  mkdirSync(assistantDir, { recursive: true })
  writeFileSync(record, '')
  const writeFakeBin = (extraFlags) => writeFileSync(
    binary,
    `#!/bin/sh\nexec '${process.execPath}' '${fake}' --record '${record}' ${extraFlags} "$@"\n`,
    { mode: 0o700 },
  )
  writeFakeBin('--resume-default-model gemini-4-neon-high')
  const node = await startFakeNode(t, { features: ['assistant_pids'] })
  let runtime = createAgyRuntime({
    home,
    agyBin: binary,
    personadBin: binary,
    socketPath: node.path,
    log: () => {},
    assistantAcpIdleMs: 300,
    promptInactivityMs: 0,
  })
  t.after(async () => { await runtime.stop(); rmSync(home, { recursive: true, force: true }) })
  await runtime.start()
  await node.ready
  const calls = () => readFileSync(record, 'utf8').trim().split('\n').filter(Boolean).map(JSON.parse)
  const pids = () => node.last('runtime.assistant_pids')?.pids ?? {}
  const start = async (params) => (await node.peer.call('session.start', { cwd: assistantDir, ...params })).session.session_id
  const call = (method, session_id, params = {}) => node.peer.call(`session.${method}`, { session_id, ...params })

  const session = await start({ assistant: true, model: 'gemini-4-neon-xhigh' })
  const rawId = toAgyId(session)
  mkdirSync(join(home, '.gemini', 'antigravity-cli', 'brain', rawId), { recursive: true })
  const firstPID = await until('initial worker reported', () => pids()[session])
  await until('idle worker reaped', () => !pids()[session])

  // Send without an explicit model: session/resume resets native model to gemini-4-neon-high,
  // and the runtime must re-apply the persisted gemini-4-neon-xhigh before prompting.
  const beforeReload = calls().length
  await call('send', session, { assistant: true, text: 'after idle reap' })
  await until('reloaded prompt completed', () => calls().slice(beforeReload).some((entry) => entry.ended === rawId))
  const reloadedPID = await until('reloaded worker reported', () => pids()[session])
  assert.notEqual(reloadedPID, firstPID)
  const reloadCalls = calls().slice(beforeReload).filter((entry) => entry.params?.sessionId === rawId)
  const setModelIdx = reloadCalls.findIndex((entry) => entry.method === 'session/set_model' && entry.params?.modelId === 'gemini-4-neon-xhigh')
  const promptIdx = reloadCalls.findIndex((entry) => entry.method === 'session/prompt')
  assert.ok(setModelIdx >= 0 && promptIdx > setModelIdx, `expected model restore before prompt: ${JSON.stringify(reloadCalls)}`)

  // Simulate a full companion restart (fresh in-memory sessionModels) with a persisted .persona-model.
  await runtime.stop()
  const restartedNode = await startFakeNode(t, { features: ['assistant_pids'] })
  runtime = createAgyRuntime({
    home,
    agyBin: binary,
    personadBin: binary,
    socketPath: restartedNode.path,
    log: () => {},
    assistantAcpIdleMs: 300,
    promptInactivityMs: 0,
  })
  await runtime.start()
  await restartedNode.ready
  const restartedPids = () => restartedNode.last('runtime.assistant_pids')?.pids ?? {}
  const callRestarted = (method, session_id, params = {}) => restartedNode.peer.call(`session.${method}`, { session_id, ...params })

  const beforeRestart = calls().length
  await callRestarted('send', session, { assistant: true, text: 'after companion restart' })
  await until('post-restart prompt completed', () => calls().slice(beforeRestart).some((entry) => entry.ended === rawId))
  const restartCalls = calls().slice(beforeRestart).filter((entry) => entry.params?.sessionId === rawId)
  assert.ok(
    restartCalls.some((entry) => entry.method === 'session/set_model' && entry.params?.modelId === 'gemini-4-neon-xhigh'),
    `expected persisted model restore across companion restart: ${JSON.stringify(restartCalls)}`,
  )

  // An explicit caller model overrides the persisted model and becomes the new restored model.
  const beforeExplicit = calls().length
  await callRestarted('send', session, { assistant: true, text: 'switch model', model: 'gemini-4-neon-custom' })
  await until('explicit-model prompt completed', () => calls().slice(beforeExplicit).some((entry) => entry.ended === rawId))
  await until('worker reaped after explicit switch', () => !restartedPids()[session])

  const beforeCustomReload = calls().length
  await callRestarted('send', session, { assistant: true, text: 'after custom switch reap' })
  await until('custom-reload prompt completed', () => calls().slice(beforeCustomReload).some((entry) => entry.ended === rawId))
  const customReloadCalls = calls().slice(beforeCustomReload).filter((entry) => entry.params?.sessionId === rawId)
  assert.ok(
    customReloadCalls.some((entry) => entry.method === 'session/set_model' && entry.params?.modelId === 'gemini-4-neon-custom'),
    `expected updated persisted model restore: ${JSON.stringify(customReloadCalls)}`,
  )

  // If the persisted model is later rejected as retired, automatic restore degrades gracefully instead of failing the send.
  await until('worker reaped before retired-model check', () => !restartedPids()[session])
  writeFakeBin('--resume-default-model gemini-4-neon-high --reject-model gemini-4-neon-custom')
  const beforeRetired = calls().length
  await callRestarted('send', session, { assistant: true, text: 'retired model fallback' })
  await until('retired-model prompt still completes', () => calls().slice(beforeRetired).some((entry) => entry.ended === rawId))
  const retiredCalls = calls().slice(beforeRetired).filter((entry) => entry.params?.sessionId === rawId)
  assert.ok(retiredCalls.some((entry) => entry.method === 'session/prompt'))
})
