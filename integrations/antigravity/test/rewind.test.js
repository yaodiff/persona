import assert from 'node:assert/strict'
import { appendFileSync, chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { get } from 'node:http'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { createAgyRuntime } from '../lib/runtime.js'

const RAW = '11111111-2222-4333-8444-555555555555'
const SESSION = `agy-${RAW}`
const FAKE = fileURLToPath(new URL('./fake-acp-rewind.mjs', import.meta.url))
const at = '2026-01-01T00:00:00Z'
const records = [
  { step_index: 0, type: 'USER_INPUT', content: 'Repeat' },
  { step_index: 1, type: 'PLANNER_RESPONSE', content: 'First answer' },
  { step_index: 2, type: 'USER_INPUT', content: 'Repeat' },
  { step_index: 3, type: 'PLANNER_RESPONSE', content: 'Second answer' },
].map((step) => ({ ...step, status: 'DONE', created_at: at }))
const nativeRecord = (step) => ({
  type: `CORTEX_STEP_TYPE_${step.type}`,
  metadata: { source: step.type === 'USER_INPUT' ? 'CORTEX_STEP_SOURCE_USER_EXPLICIT' : 'MODEL', createdAt: step.created_at },
  ...(step.type === 'USER_INPUT' ? { userInput: { items: [{ text: step.content }] } } : {}),
  ...(step.type === 'PLANNER_RESPONSE' ? { plannerResponse: { response: step.content } } : {}),
})

async function fixture(t, extra = {}) {
  const { runtimeOptions, ...snapshotExtra } = extra
  const home = mkdtempSync(join(tmpdir(), 'agy-rewind-'))
  const config = join(home, 'config')
  const logs = join(config, 'brain', RAW, '.system_generated/logs')
  mkdirSync(logs, { recursive: true })
  const transcript = join(logs, 'transcript_full.jsonl')
  writeFileSync(transcript, records.map((r) => JSON.stringify(r)).join('\n') + '\n')
  writeFileSync(join(home, 'calls.jsonl'), '')
  const nativeFile = join(home, 'trajectory.json')
  const snapshot = {
    status: 'CASCADE_RUN_STATUS_IDLE', trajectory: { cascadeId: RAW, steps: records.map(nativeRecord) }, ...snapshotExtra,
  }
  writeFileSync(nativeFile, JSON.stringify(snapshot))
  const db = new DatabaseSync(join(config, 'conversation_summaries.db'))
  db.exec('CREATE TABLE conversation_summaries (conversation_id TEXT PRIMARY KEY, status TEXT, not_fully_idle INTEGER, killed INTEGER, step_count INTEGER, title TEXT, workspace_uris TEXT, last_modified_time TEXT)')
  db.prepare('INSERT INTO conversation_summaries VALUES (?, ?, 0, 0, 4, ?, ?, ?)').run(RAW, 'CASCADE_RUN_STATUS_IDLE', 'Rewind example', JSON.stringify([`file://${home}`]), new Date().toISOString())
  const binary = join(home, 'agy')
  writeFileSync(binary, `#!/bin/sh\nexec '${process.execPath}' '${FAKE}' '${home}' "$@"\n`)
  chmodSync(binary, 0o700)
  const node = await startFakeNode(t)
  const options = { home, agyDir: config, agyBin: binary, socketPath: node.path, log: () => {}, ...runtimeOptions }
  let runtime = createAgyRuntime(options)
  t.after(async () => { await runtime.stop(); db.close(); rmSync(home, { recursive: true, force: true }) })
  runtime.start()
  await node.ready
  await until('rewind inventory', () => node.last('runtime.sessions')?.sessions.length)
  const calls = () => readFileSync(join(home, 'calls.jsonl'), 'utf8').split('\n').filter(Boolean).map(JSON.parse)
  const rewind = (extra = {}) => node.peer.call('session.rewind', { session_id: SESSION, prompt: 'Repeat', occurrence: 1, prompts_after: 0, restore: 'conversation', ...extra })
  const restart = async (update) => {
    await runtime.stop()
    await update?.()
    const before = node.all('runtime.sessions').length
    runtime = createAgyRuntime(options)
    runtime.start()
    await until('restarted inventory', () => node.all('runtime.sessions').length > before)
  }
  return { node, runtime, home, transcript, db, nativeFile, calls, rewind, restart }
}

test('ACP updates during rewind cannot start autonomous work or restore the old tail', async (t) => {
  const { node, home, rewind, runtime } = await fixture(t, { holdRevert: true })
  await node.peer.call('session.watch', { session_id: SESSION, since_seq: records.length })
  const pending = rewind()
  try {
    await until('rewind updates delivered', () => node.last('runtime.sessions')?.sessions[0]?.title === 'Rewind in progress')
    assert.notEqual(node.last('runtime.sessions').sessions[0].status, 'working')
    assert.ok(!node.all('session.delta').some((delta) => delta.text === 'Stale reply.'))
  } finally {
    writeFileSync(join(home, 'release-revert'), '')
    await pending
  }
  assert.equal(runtime.idle(), true)
  const final = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(texts(final), ['Repeat', 'First answer'])
})

test('the native fake answers port discovery and keeps serving the conversation', async (t) => {
  const { home, node, rewind } = await fixture(t)
  await rewind()
  await until('native service port', () => existsSync(join(home, 'service-port')))
  const port = Number(readFileSync(join(home, 'service-port'), 'utf8'))
  const probe = await new Promise((resolve) => {
    const req = get({ hostname: '127.0.0.1', port, path: '/', headers: { Connection: 'close', 'User-Agent': 'persona-ports' } }, (res) => {
      res.resume()
      res.on('end', () => resolve({ status: res.statusCode }))
    })
    req.on('error', (error) => resolve({ error }))
  })
  assert.equal(probe.error, undefined)
  assert.equal(probe.status, 404)
  const page = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(texts(page), ['Repeat', 'First answer'])
})

test('native rewind preserves the prefix, resets watched history and persists through replacement exports', async (t) => {
  const { node, transcript, rewind, calls, db } = await fixture(t)
  assert.equal(node.hello.caps.rewind, true)
  const initial = await node.peer.call('session.items', { session_id: SESSION })
  await node.peer.call('session.watch', { session_id: SESSION, since_seq: initial.to_seq })
  assert.equal((await rewind()).input_cleared, true)
  const native = calls().find((c) => c.method === 'RevertToCascadeStep')
  assert.deepEqual(native.params, { cascadeId: RAW, stepIndex: 1, conversationOnly: true, suppressErrorStep: true })
  const page = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(texts(page), ['Repeat', 'First answer'])
  assert.notEqual(page.history, initial.history)
  await until('history reset pushed', () => node.all('session.items').some((batch) => batch.history === page.history))
  const result = await rewind({ occurrence: 1, prompts_after: 0 })
  assert.equal(result.prompt, 'Repeat')
  const empty = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(empty.items, [])
  assert.equal(empty.to_seq, 0)
  assert.notEqual(empty.history, page.history)
  appendFileSync(transcript, [
    { step_index: 0, type: 'USER_INPUT', content: 'New branch', created_at: new Date(Date.now() + 10).toISOString(), status: 'DONE' },
    { step_index: 1, type: 'PLANNER_RESPONSE', content: 'New answer', created_at: new Date(Date.now() + 10).toISOString(), status: 'DONE' },
  ].map((s) => JSON.stringify(s)).join('\n') + '\n')
  db.exec('UPDATE conversation_summaries SET step_count = 2')
  const replacement = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(texts(replacement), ['New branch', 'New answer'])
  assert.notEqual(replacement.history, empty.history)
  assert.equal((await node.peer.call('session.items', { session_id: SESSION })).history, replacement.history)
})

test('rewound identical inputs and replacement replies cannot reuse earlier item identities', async (t) => {
  const { node, transcript, rewind, db } = await fixture(t)
  const initial = visible(await node.peer.call('session.items', { session_id: SESSION }))
  await rewind()
  // Native exports append the repeated input at its old index, potentially with the same timestamp.
  appendFileSync(transcript, [records[2], { ...records[3], content: 'Replacement answer' }]
    .map((step) => JSON.stringify(step)).join('\n') + '\n')
  db.exec('UPDATE conversation_summaries SET step_count = 4')
  const replacement = visible(await node.peer.call('session.items', { session_id: SESSION }))
  assert.deepEqual(replacement.slice(0, 2), initial.slice(0, 2), 'retained prefix stays stable')
  assert.equal(replacement[2].text, initial[2].text)
  assert.equal(replacement[2].seq, initial[2].seq)
  assert.notEqual(replacement[2].id, initial[2].id, 'a pending send must not inherit the old echo binding')
  assert.notEqual(replacement[3].id, initial[3].id, 'the old expanded reply must not match the new reply')
  const full = await node.peer.call('session.item', { session_id: SESSION, item_id: replacement[3].id })
  assert.equal(full.text, 'Replacement answer')
  await assert.rejects(node.peer.call('session.item', { session_id: SESSION, item_id: initial[3].id }), /not found/)

  appendFileSync(transcript, JSON.stringify({ ...records[3], content: 'Completed replacement answer' }) + '\n')
  const completed = visible(await node.peer.call('session.items', { session_id: SESSION }))
  assert.equal(completed[3].id, replacement[3].id, 'same-step completion keeps its identity')
  assert.equal(completed[3].text, 'Completed replacement answer')

  appendFileSync(transcript, [records[2], { ...records[3], content: 'Completed replacement answer' }]
    .map((step) => JSON.stringify(step)).join('\n') + '\n')
  const repeated = visible(await node.peer.call('session.items', { session_id: SESSION }))
  assert.notEqual(repeated[2].id, completed[2].id, 'another identical replacement is another send')
  assert.notEqual(repeated[3].id, completed[3].id)
  assert.deepEqual(repeated.slice(0, 2), initial.slice(0, 2))
})

test('complete export replacement also distinguishes new steps at reused indices', async (t) => {
  const { node, transcript, db } = await fixture(t)
  const initial = visible(await node.peer.call('session.items', { session_id: SESSION }))
  const next = records.map((step, index) => index < 2 ? step : { ...step, created_at: '2026-01-02T00:00:00Z' })
  writeFileSync(transcript, next.map((step) => JSON.stringify(step)).join('\n') + '\n')
  db.exec('UPDATE conversation_summaries SET step_count = 4')
  const replaced = visible(await node.peer.call('session.items', { session_id: SESSION }))
  assert.deepEqual(replaced.slice(0, 2), initial.slice(0, 2))
  assert.notEqual(replaced[2].id, initial[2].id)
  assert.notEqual(replaced[3].id, initial[3].id)
})

test('moved prompts and native busy state refuse before mutation', async (t) => {
  const { rewind, calls, nativeFile } = await fixture(t)
  await assert.rejects(rewind({ occurrence: 2, prompts_after: 0 }), (e) => e.code === 'rewind_mismatch')
  await assert.rejects(rewind({ restore: 'code_and_conversation' }), (e) => e.code === 'rewind_unsupported')
  const snapshot = JSON.parse(readFileSync(nativeFile, 'utf8'))
  writeFileSync(nativeFile, JSON.stringify({ ...snapshot, status: 'CASCADE_RUN_STATUS_RUNNING' }))
  await assert.rejects(rewind(), (e) => e.code === 'rewind_busy')
  assert.equal(calls().filter((c) => c.method === 'RevertToCascadeStep').length, 0)
})

test('a native failure preserves visible history and remains retryable', async (t) => {
  const { node, rewind, nativeFile } = await fixture(t, { fail: true })
  const initial = await node.peer.call('session.items', { session_id: SESSION })
  await assert.rejects(rewind(), /Native rewind refused/)
  const after = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(after, initial)
  const snapshot = JSON.parse(readFileSync(nativeFile, 'utf8'))
  writeFileSync(nativeFile, JSON.stringify({ ...snapshot, fail: false }))
  assert.equal((await rewind()).input_cleared, true)
})

test('plan-mode messages match their original native input', async (t) => {
  const { rewind, transcript } = await fixture(t)
  const plan = records.map((step) => step.step_index === 2 ? { ...step, content: '/plan Repeat' } : step)
  writeFileSync(transcript, plan.map((s) => JSON.stringify(s)).join('\n') + '\n')
  assert.equal((await rewind({ prompt: '/plan Repeat' })).input_cleared, true)
})


test('a companion restart verifies a native cut whose timestamp precedes the abandoned export', async (t) => {
  const { node, rewind, db, restart } = await fixture(t)
  await rewind()
  db.prepare('UPDATE conversation_summaries SET last_modified_time = ?').run('2025-01-01T00:00:00Z')
  await restart()
  const page = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(texts(page), ['Repeat', 'First answer'])
  assert.ok(page.history.endsWith(':cut:2'))
})

test('a companion restart preserves an empty native trajectory with no steps field', async (t) => {
  const { node, rewind, db, restart } = await fixture(t)
  await rewind({ occurrence: 2, prompts_after: 1 })
  db.prepare('UPDATE conversation_summaries SET last_modified_time = ?').run('2025-01-01T00:00:00Z')
  await restart()
  const page = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(page.items, [])
  assert.ok(page.history.endsWith(':cut:0'))
})

const later = '2026-01-01T00:01:00Z'
const visible = (page) => page.items.filter((item) => item.kind !== 'turn_duration')
const texts = (page) => visible(page).map((item) => item.text)

test('a sent turn appends to the history, its export changes it once and a rewind still replaces it (#12014)', async (t) => {
  const { node, transcript, db, nativeFile, rewind } = await fixture(t, { promptPlan: [{ reply: 'Third answer' }] })
  const idle = await node.peer.call('session.items', { session_id: SESSION })
  await node.peer.call('session.watch', { session_id: SESSION, since_seq: idle.to_seq })
  await node.peer.call('session.send', { session_id: SESSION, text: 'Third' })
  await until('the settled reply is pushed', () => node.last('session.items')?.items.some((item) => item.text === 'Third answer'))
  for (const batch of node.all('session.items')) {
    assert.equal(batch.history, idle.history, 'the send and its reply append')
    assert.ok(batch.from_seq >= idle.to_seq)
  }

  const turn = [{ step_index: 4, type: 'USER_INPUT', content: 'Third' }, { step_index: 5, type: 'PLANNER_RESPONSE', content: 'Third answer' }]
    .map((step) => ({ ...step, status: 'DONE', created_at: later }))
  appendFileSync(transcript, turn.map((step) => JSON.stringify(step)).join('\n') + '\n')
  const snapshot = JSON.parse(readFileSync(nativeFile, 'utf8'))
  snapshot.trajectory.steps.push(
    { type: 'CORTEX_STEP_TYPE_USER_INPUT', metadata: { source: 'CORTEX_STEP_SOURCE_USER_EXPLICIT' }, userInput: { items: [{ text: 'Third' }] } },
    { type: 'CORTEX_STEP_TYPE_PLANNER_RESPONSE', metadata: { source: 'MODEL' } })
  writeFileSync(nativeFile, JSON.stringify(snapshot))
  db.exec('UPDATE conversation_summaries SET step_count = 6')
  await until('the export replaces the projected turn', () => node.last('session.items')?.history !== idle.history)
  const exported = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(texts(exported), [...records.map((step) => step.content), 'Third', 'Third answer'])
  assert.equal(node.last('session.items').history, exported.history)
  assert.equal(node.last('session.items').from_seq, 0)

  await rewind({ prompt: 'Third' })
  const rewound = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(texts(rewound), records.map((step) => step.content))
  assert.notEqual(rewound.history, exported.history)
  await until('the rewind is pushed', () => node.last('session.items')?.history === rewound.history)
})

test('a restart changes the history only when projected steps were outstanding (#12014)', async (t) => {
  const { node, home, restart } = await fixture(t, { promptPlan: [{ reply: 'Third answer' }] })
  const items = () => node.peer.call('session.items', { session_id: SESSION })
  const idle = await items()
  await node.peer.call('session.send', { session_id: SESSION, text: 'Third' })
  await until('the reply waits for its export', async () => texts(await items()).includes('Third answer'))
  assert.equal((await items()).history, idle.history)
  const epoch = join(home, 'config/brain', RAW, '.persona-history-epoch')
  if (process.platform !== 'win32') assert.equal(statSync(epoch).mode & 0o777, 0o600)

  await restart()
  const restarted = await items()
  assert.notEqual(restarted.history, idle.history, 'clients drop the projected steps they hold')
  assert.deepEqual(texts(restarted), records.map((step) => step.content))
  await restart()
  assert.equal((await items()).history, restarted.history, 'nothing was outstanding')
})

test('a companion restart preserves a settled Persona-owned ACP turn while the native summary lags (#12003)', async (t) => {
  const { node, runtime, home, transcript, db, calls, restart } = await fixture(t, {
    promptPlan: [{ reply: 'Third answer', export: true }],
  })
  await node.peer.call('session.send', { session_id: SESSION, text: 'Third' })
  await until('Persona-owned ACP prompt settled', () => runtime.idle())
  const prompts = () => calls().filter((call) => call.method === 'session/prompt').length
  assert.equal(prompts(), 1)
  const exported = readFileSync(transcript, 'utf8').trim().split('\n').map(JSON.parse)
  assert.deepEqual(exported.slice(-2).map((step) => step.content), ['Third', 'Third answer'])
  const summary = db.prepare('SELECT status, step_count FROM conversation_summaries WHERE conversation_id = ?').get(RAW)
  assert.equal(summary.status, 'CASCADE_RUN_STATUS_IDLE')
  assert.ok(summary.step_count <= exported.at(-1).step_index, 'the native summary still lags the export')
  const markerPath = join(home, 'config/brain', RAW, '.persona-turn-completion')
  const marker = readFileSync(markerPath, 'utf8')
  assert.ok(!marker.includes('Third'), 'the marker stores hashes, not prompt or reply text')
  if (process.platform !== 'win32') assert.equal(statSync(markerPath).mode & 0o777, 0o600)

  await restart()
  const row = node.last('runtime.sessions').sessions.find((session) => session.session_id === SESSION)
  assert.equal(prompts(), 1, 'the restart does not send the settled prompt again')
  assert.equal(row.status, 'done')
})

test('a companion restart recovers a settled ACP reply exported while stopped (#12003)', async (t) => {
  const { node, runtime, transcript, calls, restart } = await fixture(t, {
    promptPlan: [{ reply: 'Third answer' }],
  })
  await node.peer.call('session.send', { session_id: SESSION, text: 'Third' })
  await until('Persona-owned ACP prompt settled', () => runtime.idle())
  await restart(() => {
    const turn = [
      { step_index: 4, type: 'USER_INPUT', content: 'Third' },
      { step_index: 5, type: 'PLANNER_RESPONSE', content: 'Third answer' },
    ].map((step) => ({ ...step, status: 'DONE', created_at: later }))
    appendFileSync(transcript, turn.map((step) => JSON.stringify(step)).join('\n') + '\n')
  })
  const row = node.last('runtime.sessions').sessions.find((session) => session.session_id === SESSION)
  assert.equal(row.status, 'done')
  assert.equal(calls().filter((call) => call.method === 'session/prompt').length, 1)
})

test('a companion restart leaves a lagging native summary unknown without settled ACP evidence (#12003)', async (t) => {
  const { node, transcript, calls, restart } = await fixture(t)
  await restart(() => {
    const turn = [
      { step_index: 4, type: 'USER_INPUT', content: 'Third' },
      { step_index: 5, type: 'PLANNER_RESPONSE', content: 'Third answer' },
    ].map((step) => ({ ...step, status: 'DONE', created_at: later }))
    appendFileSync(transcript, turn.map((step) => JSON.stringify(step)).join('\n') + '\n')
  })
  const row = node.last('runtime.sessions').sessions.find((session) => session.session_id === SESSION)
  assert.equal(row.status, 'unknown', 'trailing response text is not terminal evidence')
  assert.equal(calls().filter((call) => call.method === 'session/prompt').length, 0)
})

test('a settled ACP turn does not complete later native work after a restart (#12003)', async (t) => {
  const { node, runtime, transcript, calls, restart } = await fixture(t, {
    promptPlan: [{ reply: 'Third answer', export: true }],
  })
  await node.peer.call('session.send', { session_id: SESSION, text: 'Third' })
  await until('Persona-owned ACP prompt settled', () => runtime.idle())
  await restart(() => {
    appendFileSync(transcript, JSON.stringify({ step_index: 6, type: 'GENERIC', content: 'Later native activity', status: 'DONE', created_at: '2026-01-01T00:02:00Z' }) + '\n')
  })
  const row = node.last('runtime.sessions').sessions.find((session) => session.session_id === SESSION)
  assert.equal(row.status, 'unknown', 'the old ACP result is not evidence for later native work')
  assert.equal(calls().filter((call) => call.method === 'session/prompt').length, 1)
})

test('an empty export cannot crash settled ACP recovery (#12003)', async (t) => {
  const { node, runtime, transcript, db } = await fixture(t, { promptPlan: [{ reply: 'Third answer' }] })
  writeFileSync(transcript, '')
  db.exec('UPDATE conversation_summaries SET step_count = 0')
  await node.peer.call('session.send', { session_id: SESSION, text: 'Third' })
  await until('Persona-owned ACP prompt settled', () => runtime.idle())
  await until('empty-export inventory', () => node.last('runtime.sessions')?.sessions[0]?.status === 'unknown')
})

test('a companion restart uses the settled reply index after tool calls (#12003)', async (t) => {
  const { node, runtime, restart } = await fixture(t, {
    promptPlan: [{ reply: 'Third answer', toolStart: true, tool: true, export: true }],
  })
  await node.peer.call('session.send', { session_id: SESSION, text: 'Third' })
  await until('Persona-owned ACP prompt settled', () => runtime.idle())
  await restart()
  const row = node.last('runtime.sessions').sessions.find((session) => session.session_id === SESSION)
  assert.equal(row.status, 'done')
})

test('a native rewind clears settled-turn recovery evidence (#12003)', async (t) => {
  const { node, runtime, restart, rewind } = await fixture(t, {
    promptPlan: [{ reply: 'Third answer', export: true }],
  })
  await node.peer.call('session.send', { session_id: SESSION, text: 'Third' })
  await until('Persona-owned ACP prompt settled', () => runtime.idle())
  await rewind()
  await restart()
  const stale = node.last('runtime.sessions').sessions.find((session) => session.session_id === SESSION)
  if (stale.last_text === 'Third answer') assert.notEqual(stale.status, 'done', 'an abandoned reply cannot be restored as done')
  const page = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(texts(page), records.slice(0, 2).map((step) => step.content), 'native cut reconciliation removes the abandoned tail')
})

test('a failed validation after native rewind cannot restore abandoned evidence (#12003)', async (t) => {
  const { node, runtime, restart, rewind } = await fixture(t, {
    promptPlan: [{ reply: 'Third answer', export: true }], badAfterRevertLength: true,
  })
  await node.peer.call('session.send', { session_id: SESSION, text: 'Third' })
  await until('Persona-owned ACP prompt settled', () => runtime.idle())
  await assert.rejects(rewind(), /Reload its history/)
  await restart()
  const stale = node.last('runtime.sessions').sessions.find((session) => session.session_id === SESSION)
  if (stale.last_text === 'Third answer') assert.notEqual(stale.status, 'done', 'a mutated native rewind cannot revive its abandoned reply')
})

test('a companion restart recovers a truncated settled reply (#12003)', async (t) => {
  const reply = '## Report\n' + 'A detailed report row.\n'.repeat(200) + '\nEnd of report.'
  const { node, runtime, restart } = await fixture(t, {
    promptPlan: [{ reply, export: true, truncateReply: { head: '## Report\n', tail: '\nEnd of report.' } }],
  })
  await node.peer.call('session.send', { session_id: SESSION, text: 'Third' })
  await until('Persona-owned ACP prompt settled', () => runtime.idle())
  await restart()
  const row = node.last('runtime.sessions').sessions.find((session) => session.session_id === SESSION)
  assert.equal(row.status, 'done')
})

test('a failed follow-up cannot restore the previous settled reply (#12003)', async (t) => {
  const { node, runtime, restart } = await fixture(t, {
    promptPlan: [{ reply: 'Third answer', export: true }, { error: 'Synthetic failure' }],
  })
  await node.peer.call('session.send', { session_id: SESSION, text: 'Third' })
  await until('Persona-owned ACP prompt settled', () => runtime.idle())
  await node.peer.call('session.send', { session_id: SESSION, text: 'Fourth' })
  await until('the follow-up fails', () => runtime.idle())
  await restart()
  const row = node.last('runtime.sessions').sessions.find((session) => session.session_id === SESSION)
  assert.equal(row.status, 'unknown')
})

test('a restart with an unreadable history epoch does not reuse the history of projected steps (#12014)', {
  skip: process.platform === 'win32' || process.getuid?.() === 0,
}, async (t) => {
  const { node, home, restart } = await fixture(t, { promptPlan: [{ reply: 'Third answer' }] })
  const items = () => node.peer.call('session.items', { session_id: SESSION })
  const idle = await items()
  await node.peer.call('session.send', { session_id: SESSION, text: 'Third' })
  await until('the reply waits for its export', async () => texts(await items()).includes('Third answer'))
  const sent = await items()
  assert.equal(sent.history, idle.history)
  const epoch = join(home, 'config/brain', RAW, '.persona-history-epoch')
  chmodSync(epoch, 0o000)
  try {
    await restart()
    assert.notEqual((await items()).history, sent.history, 'clients drop the projected steps they hold')
  } finally {
    chmodSync(epoch, 0o600)
  }
})

test('a send changes the history at once when its brain directory is read-only (#12014)', {
  skip: process.platform === 'win32' || process.getuid?.() === 0,
}, async (t) => {
  const { node, home, restart } = await fixture(t, { promptPlan: [{ reply: 'Third answer' }] })
  const items = () => node.peer.call('session.items', { session_id: SESSION })
  const brain = join(home, 'config/brain', RAW)
  const idle = await items()
  chmodSync(brain, 0o500)
  try {
    await node.peer.call('session.send', { session_id: SESSION, text: 'Third' })
    await until('the reply waits for its export', async () => texts(await items()).includes('Third answer'))
    const sent = await items()
    assert.notEqual(sent.history, idle.history, 'no restart can report the history of these projected steps')
    assert.equal(existsSync(join(brain, '.persona-history-epoch')), false)
    await restart()
    assert.notEqual((await items()).history, sent.history)
  } finally {
    chmodSync(brain, 0o700)
  }
})

test('an inventory refresh revalidates an existing cut after native work advances', async (t) => {
  const { node, transcript, rewind, db, nativeFile, calls } = await fixture(t)
  const snapshot = JSON.parse(readFileSync(nativeFile, 'utf8'))
  const fullTrajectory = [...snapshot.trajectory.steps,
    nativeRecord({ ...records[2], step_index: 4, content: 'New question' }),
    nativeRecord({ ...records[3], step_index: 5, content: 'Recovered answer' })]
  await rewind()
  await until('rewound inventory', () => node.last('runtime.sessions')?.sessions[0]?.last_text === 'First answer')

  appendFileSync(transcript, [
    { ...records[2], step_index: 4, content: 'New question' },
    { ...records[3], step_index: 5, content: 'Recovered answer' },
  ].map((step) => JSON.stringify(step)).join('\n') + '\n')
  writeFileSync(nativeFile, JSON.stringify({ ...snapshot, trajectory: { ...snapshot.trajectory, steps: fullTrajectory } }))
  db.prepare('UPDATE conversation_summaries SET step_count = 6, last_modified_time = ?').run(new Date().toISOString())
  const checks = calls().filter((call) => call.method === 'GetCascadeTrajectory').length
  await node.peer.call('session.rename', { session_id: SESSION, title: 'Renamed' })
  await until('recovered last reply in the inventory', () => node.last('runtime.sessions')?.sessions[0]?.last_text === 'Recovered answer')
  assert.equal(calls().filter((call) => call.method === 'GetCascadeTrajectory').length, checks + 1)
  const page = await node.peer.call('session.items', { session_id: SESSION })
  assert.equal(texts(page).at(-1), 'Recovered answer')
  assert.equal(page.items.at(-1).kind, 'turn_duration')
})

test('a changed summary cannot expose the abandoned tail while the native trajectory is still rewound', async (t) => {
  const { node, transcript, rewind, db, nativeFile, calls } = await fixture(t)
  await rewind()
  await until('rewound inventory', () => node.last('runtime.sessions')?.sessions[0]?.last_text === 'First answer')
  appendFileSync(transcript, [
    { ...records[2], step_index: 4, content: 'Unexported question' },
    { ...records[3], step_index: 5, content: 'Abandoned answer' },
  ].map((step) => JSON.stringify(step)).join('\n') + '\n')
  db.prepare('UPDATE conversation_summaries SET step_count = 6, last_modified_time = ?').run(new Date().toISOString())
  const snapshot = JSON.parse(readFileSync(nativeFile, 'utf8'))
  assert.equal(snapshot.trajectory.steps.length, 2)
  const checks = calls().filter((call) => call.method === 'GetCascadeTrajectory').length
  const before = node.all('runtime.sessions').length
  await node.peer.call('session.rename', { session_id: SESSION, title: 'Renamed' })
  await until('inventory refreshed', () => node.all('runtime.sessions').length > before)
  const page = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(texts(page), ['Repeat', 'First answer'])
  assert.equal(calls().filter((call) => call.method === 'GetCascadeTrajectory').length, checks + 1)
  assert.equal(node.last('runtime.sessions').sessions[0].last_text, 'First answer')
})

test('native progress cannot clear a cut until the replacement export catches up', async (t) => {
  const { node, transcript, rewind, db, nativeFile } = await fixture(t)
  const snapshot = JSON.parse(readFileSync(nativeFile, 'utf8'))
  const replacement = [
    records[0], records[1],
    { ...records[2], content: 'Replacement question', created_at: '2026-01-02T00:00:00Z' },
    { ...records[3], content: 'Replacement answer', created_at: '2026-01-02T00:00:01Z' },
  ]
  await rewind()
  await until('rewound inventory', () => node.last('runtime.sessions')?.sessions[0]?.last_text === 'First answer')
  writeFileSync(nativeFile, JSON.stringify({ ...snapshot, trajectory: { ...snapshot.trajectory, steps: replacement.map(nativeRecord) } }))
  db.prepare('UPDATE conversation_summaries SET step_count = 4, last_modified_time = ?').run(new Date().toISOString())
  const before = node.all('runtime.sessions').length
  await node.peer.call('session.rename', { session_id: SESSION, title: 'Renamed' })
  await until('inventory refreshed', () => node.all('runtime.sessions').length > before)
  const lagging = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(texts(lagging), ['Repeat', 'First answer'])
  assert.ok(lagging.history.endsWith(':cut:2'))
  assert.equal(node.last('runtime.sessions').sessions[0].last_text, 'First answer')

  writeFileSync(transcript, replacement.map((step) => JSON.stringify(step)).join('\n') + '\n')
  await node.peer.call('session.rename', { session_id: SESSION, title: 'Renamed again' })
  await until('replacement export is visible', () => node.last('runtime.sessions')?.sessions[0]?.last_text === 'Replacement answer')
  const caughtUp = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(texts(caughtUp), replacement.map((step) => step.content))
})

// The export keeps the abandoned steps and numbers the next turn after its last line, as the fake's prompt export
// does, while the native trajectory continues right after the steps the rewind kept (#14297).
function proceed({ transcript, nativeFile, db }, question, answer) {
  const index = readFileSync(transcript, 'utf8').trim().split('\n').length
  const steps = [
    { ...records[2], step_index: index, content: question, created_at: later },
    { ...records[3], step_index: index + 1, content: answer, created_at: later },
  ]
  appendFileSync(transcript, steps.map((step) => JSON.stringify(step)).join('\n') + '\n')
  const snapshot = JSON.parse(readFileSync(nativeFile, 'utf8'))
  snapshot.trajectory.steps.push(...steps.map(nativeRecord))
  writeFileSync(nativeFile, JSON.stringify(snapshot))
  db.prepare('UPDATE conversation_summaries SET step_count = ?, last_modified_time = ?')
    .run(snapshot.trajectory.steps.length, new Date().toISOString())
}

test('a conversation that continues after a rewind shows its new steps behind the retained prefix (#14297)', async (t) => {
  const fx = await fixture(t)
  const { node, rewind, calls } = fx
  await rewind()
  const rewound = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(texts(rewound), ['Repeat', 'First answer'])
  assert.ok(rewound.history.endsWith(':cut:2'))
  await node.peer.call('session.watch', { session_id: SESSION, since_seq: rewound.to_seq })

  proceed(fx, 'New question', 'New answer')
  const continued = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(texts(continued), ['Repeat', 'First answer', 'New question', 'New answer'])
  assert.notEqual(continued.history, rewound.history, 'the page moves past the remembered cut')
  assert.ok(continued.to_seq > rewound.to_seq)
  await until('the continued page is pushed', () => node.all('session.items')
    .some((batch) => batch.history === continued.history && batch.items.some((item) => item.text === 'New answer')))

  proceed(fx, 'Later question', 'Later answer')
  const appended = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(texts(appended), [...texts(continued), 'Later question', 'Later answer'])
  assert.equal(appended.history, continued.history, 'later steps append')
  const checks = calls().filter((call) => call.method === 'GetCascadeTrajectory').length
  assert.deepEqual(texts(await node.peer.call('session.items', { session_id: SESSION })), texts(appended))
  assert.equal(calls().filter((call) => call.method === 'GetCascadeTrajectory').length, checks, 'a confirmed cut is not checked again')
})

test('a rewind after the conversation continued cuts at the visible step and the conversation continues again (#14297)', async (t) => {
  const fx = await fixture(t)
  const { node, rewind } = fx
  const page = () => node.peer.call('session.items', { session_id: SESSION })
  await rewind()
  proceed(fx, 'New question', 'New answer')
  proceed(fx, 'Later question', 'Later answer')
  const continued = await page()
  assert.deepEqual(texts(continued), ['Repeat', 'First answer', 'New question', 'New answer', 'Later question', 'Later answer'])

  await rewind({ prompt: 'Later question' })
  const again = await page()
  assert.deepEqual(texts(again), ['Repeat', 'First answer', 'New question', 'New answer'])
  assert.notEqual(again.history, continued.history)
  proceed(fx, 'Last question', 'Last answer')
  const resumed = await page()
  assert.deepEqual(texts(resumed), [...texts(again), 'Last question', 'Last answer'])

  await rewind({ prompt: 'New question', prompts_after: 1 })
  const first = await page()
  assert.deepEqual(texts(first), ['Repeat', 'First answer'])
  assert.notEqual(first.history, resumed.history)
})

test('a native rewind after the conversation continued hides the steps it dropped (#14297)', async (t) => {
  const fx = await fixture(t)
  const { node, rewind, nativeFile, db } = fx
  await rewind()
  proceed(fx, 'New question', 'New answer')
  proceed(fx, 'Later question', 'Later answer')
  const continued = await node.peer.call('session.items', { session_id: SESSION })
  assert.equal(texts(continued).length, 6)
  const snapshot = JSON.parse(readFileSync(nativeFile, 'utf8'))
  writeFileSync(nativeFile, JSON.stringify({ ...snapshot, trajectory: { ...snapshot.trajectory, steps: snapshot.trajectory.steps.slice(0, 4) } }))
  db.prepare('UPDATE conversation_summaries SET step_count = 4, last_modified_time = ?').run(new Date().toISOString())
  const rewound = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(texts(rewound), ['Repeat', 'First answer', 'New question', 'New answer'])
  assert.notEqual(rewound.history, continued.history)
})

test('a stale native check cannot erase a cut installed by a newer rewind', async (t) => {
  const { node, home, transcript, rewind, db, nativeFile } = await fixture(t)
  await rewind()
  await until('first rewound inventory', () => node.last('runtime.sessions')?.sessions[0]?.last_text === 'First answer')
  const watching = await node.peer.call('session.items', { session_id: SESSION })
  await node.peer.call('session.watch', { session_id: SESSION, since_seq: watching.to_seq })
  const abandoned = [
    { ...records[2], step_index: 4, content: 'Abandoned question' },
    { ...records[3], step_index: 5, content: 'Abandoned answer' },
  ]
  appendFileSync(transcript, abandoned.map((step) => JSON.stringify(step)).join('\n') + '\n')
  const snapshot = JSON.parse(readFileSync(nativeFile, 'utf8'))
  writeFileSync(nativeFile, JSON.stringify({ ...snapshot, holdTrajectoryOnce: true,
    trajectory: { ...snapshot.trajectory, steps: [...records.map(nativeRecord), ...abandoned.map(nativeRecord)] } }))
  db.prepare('UPDATE conversation_summaries SET step_count = 6, last_modified_time = ?').run(new Date().toISOString())
  await node.peer.call('session.rename', { session_id: SESSION, title: 'Start stale check' })
  await until('stale native check is held', () => existsSync(join(home, 'trajectory-held')))

  await rewind({ occurrence: 1, prompts_after: 1 })
  await until('second rewound inventory', () => node.last('runtime.sessions')?.sessions[0]?.last_text === 'First answer')
  writeFileSync(join(home, 'release-trajectory'), '')
  await until('stale native response is sent', () => existsSync(join(home, 'trajectory-response-sent')))
  await new Promise((resolve) => setTimeout(resolve, 100))
  const page = await node.peer.call('session.items', { session_id: SESSION })
  assert.equal(node.all('session.items').some((event) => event.items?.some((item) => item.text === 'Abandoned answer')), false)
  assert.equal(node.all('runtime.sessions').some((event) => event.sessions?.[0]?.last_text === 'Abandoned answer'), false)
  assert.deepEqual(texts(page), records.slice(0, 2).map((step) => step.content))
  assert.equal(node.last('runtime.sessions').sessions[0].last_text, 'First answer')
})

test('a stale check cannot publish while a newer rewind is installing its cut', async (t) => {
  const { node, home, transcript, rewind, db, nativeFile } = await fixture(t)
  await rewind()
  await until('first rewound inventory', () => node.last('runtime.sessions')?.sessions[0]?.last_text === 'First answer')
  const watching = await node.peer.call('session.items', { session_id: SESSION })
  await node.peer.call('session.watch', { session_id: SESSION, since_seq: watching.to_seq })
  const abandoned = [
    { ...records[2], step_index: 4, content: 'Abandoned question' },
    { ...records[3], step_index: 5, content: 'Abandoned answer' },
  ]
  appendFileSync(transcript, abandoned.map((step) => JSON.stringify(step)).join('\n') + '\n')
  const snapshot = JSON.parse(readFileSync(nativeFile, 'utf8'))
  writeFileSync(nativeFile, JSON.stringify({ ...snapshot, holdTrajectoryOnce: true,
    trajectory: { ...snapshot.trajectory, steps: [...records.map(nativeRecord), ...abandoned.map(nativeRecord)] } }))
  db.prepare('UPDATE conversation_summaries SET step_count = 6, last_modified_time = ?').run(new Date().toISOString())
  await node.peer.call('session.rename', { session_id: SESSION, title: 'Start stale check' })
  await until('stale native check is held', () => existsSync(join(home, 'trajectory-held')))

  const armed = JSON.parse(readFileSync(nativeFile, 'utf8'))
  writeFileSync(nativeFile, JSON.stringify({ ...armed, holdAfterRevertOnce: true }))
  const second = rewind({ occurrence: 1, prompts_after: 1 })
  await until('second rewind is installing its cut', () => existsSync(join(home, 'after-trajectory-held')))
  writeFileSync(join(home, 'release-trajectory'), '')
  await until('stale native response is sent', () => existsSync(join(home, 'trajectory-response-sent')))
  await new Promise((resolve) => setTimeout(resolve, 100))
  writeFileSync(join(home, 'release-after-trajectory'), '')
  await second
  const page = await node.peer.call('session.items', { session_id: SESSION })
  assert.equal(node.all('session.items').some((event) => event.items?.some((item) => item.text === 'Abandoned answer')), false)
  assert.equal(node.all('runtime.sessions').some((event) => event.sessions?.[0]?.last_text === 'Abandoned answer'), false)
  assert.deepEqual(texts(page), records.slice(0, 2).map((step) => step.content))
  assert.equal(node.last('runtime.sessions').sessions[0].last_text, 'First answer')
})

test('native rewind drops rewound-away token usage and model from runtime.sessions while disk still holds the old tail', async (t) => {
  const { node, transcript, rewind } = await fixture(t)
  const seeded = [
    records[0],
    {
      ...records[1],
      model: 'gemini-4-carbon-high',
      input_tokens: 10000,
      cache_read_tokens: 2000,
      output_tokens: 150,
    },
    records[2],
    {
      ...records[3],
      model: 'claude-opus-5-5-high',
      input_tokens: 5000,
      cache_read_tokens: 40000,
      cache_write_tokens: 500,
      output_tokens: 400,
      context_window: 1000000,
    },
  ]
  writeFileSync(transcript, seeded.map((step) => JSON.stringify(step)).join('\n') + '\n')
  const row = () => node.last('runtime.sessions')?.sessions.find((s) => s.session_id === SESSION)
  await node.peer.call('session.rename', { session_id: SESSION, title: 'Usage before rewind' })
  await until('initial token usage published', () => row()?.context_tokens === 45500 && row()?.output_tokens === 550)
  assert.equal(row().context_window, 1000000)
  assert.equal(row().model, 'claude-opus-5-5-high')

  // Partial rewind hides Turn 2 even though transcript_full.jsonl on disk still contains it.
  await rewind()
  await until('rewound session drops turn 2 usage', () => row()?.context_tokens === 12000 && row()?.output_tokens === 150)
  assert.equal(row().context_window, undefined)
  assert.equal(row().model, 'gemini-4-carbon-high')
  const page = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(page.items[1].usage, [{ model: 'gemini-4-carbon-high', in: 10000, out: 150, cache_read: 2000 }])
  assert.equal(visible(page).length, 2)
  assert.equal(page.items.at(-1).kind, 'turn_duration')

  // Full rewind clears all projected steps; session row omits usage and model rather than reading stale disk.
  await rewind({ occurrence: 1, prompts_after: 0 })
  await until('fully rewound session clears usage', () => row()?.context_tokens === undefined && row()?.output_tokens === undefined)
  assert.equal(row().context_window, undefined)
  assert.equal(row().model, undefined)
})

test('settled Persona ACP turn recovers from summary_behind after delayed shifted-index export and emits turn_duration (#15619)', async (t) => {
  const { node, runtime, transcript, db, nativeFile, restart } = await fixture(t, {
    promptPlan: [{ reply: 'Shifted final answer', toolStart: true, tool: true }],
  })
  const before = await node.peer.call('session.items', { session_id: SESSION })
  await node.peer.call('session.watch', { session_id: SESSION, since_seq: before.to_seq })
  await node.peer.call('session.send', { session_id: SESSION, text: 'Run check' })
  await until('Persona-owned ACP prompt settled', () => runtime.idle())

  // Simulate delayed native export whose real step_index shifts past the synthetic live index (internal steps + tools)
  // while conversation_summaries.db remains behind at step_count = 4 (native trajectory already holds all 10 steps).
  const flushed = [
    { step_index: 4, type: 'USER_INPUT', source: 'USER_EXPLICIT', status: 'DONE', created_at: later, content: 'Run check' },
    { step_index: 5, type: 'CHECKPOINT', status: 'DONE', created_at: later },
    { step_index: 6, type: 'PLANNER_RESPONSE', source: 'MODEL', status: 'DONE', created_at: later, content: 'Running tool', tool_calls: [{ name: 'run_command', args: { CommandLine: 'echo ok' } }] },
    { step_index: 7, type: 'GENERIC', source: 'MODEL', status: 'DONE', created_at: later, content: 'ok' },
    { step_index: 8, type: 'SYSTEM_MESSAGE', status: 'DONE', created_at: later, content: 'internal note' },
    { step_index: 9, type: 'PLANNER_RESPONSE', source: 'MODEL', status: 'DONE', created_at: later, content: 'Shifted final answer' },
  ]
  const snapshot = JSON.parse(readFileSync(nativeFile, 'utf8'))
  snapshot.trajectory.steps.push(...flushed.map(nativeRecord))
  writeFileSync(nativeFile, JSON.stringify(snapshot))
  appendFileSync(transcript, flushed.map((step) => JSON.stringify(step)).join('\n') + '\n')

  await until('session settles to done despite lagging summary DB', () => {
    const row = node.last('runtime.sessions')?.sessions.find((s) => s.session_id === SESSION)
    return row?.status === 'done' && row?.last_text === 'Shifted final answer'
  })
  const summary = db.prepare('SELECT status, step_count FROM conversation_summaries WHERE conversation_id = ?').get(RAW)
  assert.equal(summary.status, 'CASCADE_RUN_STATUS_IDLE')
  assert.equal(summary.step_count, 4)

  const page = await node.peer.call('session.items', { session_id: SESSION })
  const td = page.items.at(-1)
  assert.equal(td.kind, 'turn_duration')
  assert.equal(td.seq, 10)
  assert.equal(td.turn_duration.source, 'agy_turn_end')
  assert.equal(td.turn_duration.duration_ms, 0)
  assert.equal(td.turn_duration.user_item_id, page.items.find((it) => it.seq === 5 && it.role === 'user').id)
  await until('watched stream pushes turn_duration', () =>
    node.all('session.items').some((batch) => batch.items?.some((it) => it.id === td.id)),
  )

  // The in-memory match must have refreshed .persona-turn-completion with real (history, step_index=9) so restarts recover too.
  await restart()
  const afterRestart = node.last('runtime.sessions').sessions.find((s) => s.session_id === SESSION)
  assert.equal(afterRestart.status, 'done')
  const restartedPage = await node.peer.call('session.items', { session_id: SESSION })
  assert.equal(restartedPage.items.at(-1).id, td.id)
})

test('settled Persona ACP turn recovers when summary row is not yet present while external unverified turns stay unknown (#15619)', async (t) => {
  const { node, runtime, transcript, db } = await fixture(t, {
    promptPlan: [{ reply: 'Fresh summary answer', export: true }],
  })
  db.exec('DELETE FROM conversation_summaries')
  await node.peer.call('session.send', { session_id: SESSION, text: 'First without summary row' })
  await until('Persona-owned ACP prompt settled', () => runtime.idle())

  await until('session reports done even before summary row exists', () =>
    node.last('runtime.sessions')?.sessions.find((s) => s.session_id === SESSION)?.status === 'done',
  )
  const settledPage = await node.peer.call('session.items', { session_id: SESSION })
  assert.equal(settledPage.items.at(-1).kind, 'turn_duration')

  // Appending an external unverified turn without ACP settlement must drop back to unknown without trailing turn_duration.
  const external = [
    { step_index: 6, type: 'USER_INPUT', source: 'USER_EXPLICIT', status: 'DONE', created_at: '2026-01-01T00:02:00Z', content: 'External' },
    { step_index: 7, type: 'PLANNER_RESPONSE', source: 'MODEL', status: 'DONE', created_at: '2026-01-01T00:02:01Z', content: 'Unverified external reply' },
  ]
  appendFileSync(transcript, external.map((step) => JSON.stringify(step)).join('\n') + '\n')
  const before = node.all('runtime.sessions').length
  await node.peer.call('session.rename', { session_id: SESSION, title: 'Refresh' })
  await until('inventory refreshed', () => node.all('runtime.sessions').length > before)
  assert.equal(node.last('runtime.sessions').sessions.find((s) => s.session_id === SESSION).status, 'unknown')
  const externalPage = await node.peer.call('session.items', { session_id: SESSION })
  assert.notEqual(externalPage.items.at(-1).kind, 'turn_duration')
})

test('full rewind to step 0 reports idle instead of unknown no_relevant_step immediately and after restart', async (t) => {
  const { node, rewind, db, restart } = await fixture(t)
  const row = () => node.last('runtime.sessions')?.sessions.find((s) => s.session_id === SESSION)
  await rewind({ occurrence: 2, prompts_after: 1 })
  await until('rewound empty conversation settles to idle', () => row()?.status === 'idle')
  db.prepare('UPDATE conversation_summaries SET last_modified_time = ?').run('2025-01-01T00:00:00Z')
  await restart()
  // Trigger cut reconciliation via session.items so the empty native trajectory is verified after restart.
  const page = await node.peer.call('session.items', { session_id: SESSION })
  assert.deepEqual(page.items, [])
  await until('restarted empty rewound conversation reports idle', () => row()?.status === 'idle')
})

test('settled Persona ACP turn survives transient unspecified/empty summary_not_idle while staying fail-closed on active, unrecognized, and failed DB states', async (t) => {
  const { node, runtime, transcript, db, restart } = await fixture(t, {
    promptPlan: [{ reply: 'Settled across reconciliation', export: true }],
  })
  const row = () => node.last('runtime.sessions')?.sessions.find((s) => s.session_id === SESSION)
  const refresh = async (title) => {
    const before = node.all('runtime.sessions').length
    await node.peer.call('session.rename', { session_id: SESSION, title })
    await until('inventory refreshed', () => node.all('runtime.sessions').length > before)
    return row()
  }

  await node.peer.call('session.send', { session_id: SESSION, text: 'Run work' })
  await until('Persona-owned ACP prompt settled', () => runtime.idle())

  // Transient empty and UNSPECIFIED summary statuses keep settled done in-memory and across restart
  // when step_count does not exceed the exported steps (lagging or caught up at maxStep + 1 = 6).
  for (const transientStatus of ['', 'CASCADE_RUN_STATUS_UNSPECIFIED']) {
    for (const stepCount of [4, 6]) {
      db.prepare('UPDATE conversation_summaries SET status = ?, step_count = ? WHERE conversation_id = ?').run(transientStatus, stepCount, RAW)
      assert.equal((await refresh(`in-mem-${transientStatus || 'empty'}-${stepCount}`)).status, 'done')
      await restart()
      assert.equal(row().status, 'done', `restarted settled turn stays done under ${transientStatus || '<empty>'} (step_count=${stepCount})`)
    }
    // Unexported native steps (step_count > maxStep + 1) must stay fail-closed unknown even under UNSPECIFIED/empty.
    db.prepare('UPDATE conversation_summaries SET status = ?, step_count = 7 WHERE conversation_id = ?').run(transientStatus, RAW)
    assert.equal((await refresh(`ahead-${transientStatus || 'empty'}`)).status, 'unknown')
  }
  db.prepare('UPDATE conversation_summaries SET status = ?, step_count = 4 WHERE conversation_id = ?').run('CASCADE_RUN_STATUS_UNSPECIFIED', RAW)

  // Unrecognized future status must stay unknown (never guessed as done).
  db.prepare('UPDATE conversation_summaries SET status = ? WHERE conversation_id = ?').run('FUTURE_STATUS', RAW)
  assert.equal((await refresh('future-status')).status, 'unknown')

  // Active native statuses and not_fully_idle always win and report working.
  for (const active of [
    ['CASCADE_RUN_STATUS_RUNNING', 0],
    ['CASCADE_RUN_STATUS_CANCELING', 0],
    ['CASCADE_RUN_STATUS_BUSY', 0],
    ['CASCADE_RUN_STATUS_UNSPECIFIED', 1],
  ]) {
    db.prepare('UPDATE conversation_summaries SET status = ?, not_fully_idle = ? WHERE conversation_id = ?').run(active[0], active[1], RAW)
    assert.equal((await refresh(`active-${active[0]}-${active[1]}`)).status, 'working')
  }

  // Locked summary DB must report unknown (never masked by listedTranscriptCache).
  db.prepare('UPDATE conversation_summaries SET status = ?, not_fully_idle = 0 WHERE conversation_id = ?').run('CASCADE_RUN_STATUS_UNSPECIFIED', RAW)
  assert.equal((await refresh('cached-settled')).status, 'done')
  db.exec('BEGIN EXCLUSIVE')
  try {
    assert.equal((await refresh('locked-db')).status, 'unknown')
  } finally {
    db.exec('ROLLBACK')
  }
  assert.equal((await refresh('recovered-db')).status, 'done')

  // Later unverified external step invalidates settled recovery even under UNSPECIFIED.
  appendFileSync(transcript, JSON.stringify({ step_index: 6, type: 'USER_INPUT', source: 'USER_EXPLICIT', status: 'DONE', created_at: later, content: 'External follow-up' }) + '\n')
  assert.equal((await refresh('external-step')).status, 'unknown')
})

test('settled tool-only Persona ACP turn recovers as idle across summary_behind and unspecified status without emitting turn_duration', async (t) => {
  const { node, runtime, transcript, db, restart } = await fixture(t, {
    promptPlan: [
      { toolStart: true, tool: true, export: true, calls: [{ name: 'run_command', args: { CommandLine: 'echo ok' } }], toolOutput: 'ok' },
      { toolStart: true, tool: true, export: true, calls: [{ name: 'run_command', args: { CommandLine: 'echo two' } }], toolOutput: 'two' },
      { error: 'Follow-up failed' },
    ],
  })
  const row = () => node.last('runtime.sessions')?.sessions.find((s) => s.session_id === SESSION)
  const refresh = async (title) => {
    const before = node.all('runtime.sessions').length
    await node.peer.call('session.rename', { session_id: SESSION, title })
    await until('inventory refreshed', () => node.all('runtime.sessions').length > before)
    return row()
  }

  await node.peer.call('session.send', { session_id: SESSION, text: 'Run silent tool' })
  await until('tool-only turn settled', () => runtime.idle())
  // Summary DB still lags at step_count = 4 while exported transcript has steps 4 (USER_INPUT), 5 (PLANNER_RESPONSE tool_call), 6 (GENERIC).
  await until('tool-only turn reports idle under summary_behind', () => row()?.status === 'idle')
  const page = await node.peer.call('session.items', { session_id: SESSION })
  assert.equal(page.items.some((it) => it.kind === 'turn_duration' && it.seq >= 5), false, 'tool-only idle turn must not emit turn_duration')

  // Survives companion restart under summary_behind and under CASCADE_RUN_STATUS_UNSPECIFIED.
  await restart()
  assert.equal(row().status, 'idle', 'restarted tool-only turn stays idle under summary_behind')
  db.prepare('UPDATE conversation_summaries SET status = ? WHERE conversation_id = ?').run('CASCADE_RUN_STATUS_UNSPECIFIED', RAW)
  await restart()
  assert.equal(row().status, 'idle', 'restarted tool-only turn stays idle under UNSPECIFIED')

  // A later unverified running step invalidates the persisted tool-only marker.
  appendFileSync(transcript, JSON.stringify({ step_index: 7, type: 'GENERIC', status: 'RUNNING', created_at: later, content: 'Still running' }) + '\n')
  assert.equal((await refresh('later-running-tool')).status, 'unknown')

  // Even in-memory (without restart), a later external DONE GENERIC step cannot hijack or overwrite the settled tool-only turn.
  db.prepare('UPDATE conversation_summaries SET status = ? WHERE conversation_id = ?').run('CASCADE_RUN_STATUS_IDLE', RAW)
  await node.peer.call('session.send', { session_id: SESSION, text: 'Run second silent tool' })
  await until('second tool-only turn settled', () => runtime.idle())
  await until('second tool-only turn reports idle', () => row()?.status === 'idle')
  appendFileSync(transcript, JSON.stringify({ step_index: 11, type: 'GENERIC', status: 'DONE', created_at: later, content: 'External tool output' }) + '\n')
  assert.equal((await refresh('later-external-done-tool')).status, 'unknown')
  await restart()
  assert.equal(row().status, 'unknown', 'later external DONE GENERIC step did not overwrite persisted marker')

  // A failed follow-up clears the marker and leaves the session unknown.
  await node.peer.call('session.send', { session_id: SESSION, text: 'Will fail' })
  await until('failed follow-up settled', () => runtime.idle())
  await restart()
  assert.equal(row().status, 'unknown')
})

test('tool-only settled recovery refuses zero-result or failed tool turns and requires exact GENERIC count before binding', async (t) => {
  const { node, runtime, transcript, restart } = await fixture(t, {
    promptPlan: [
      { toolStartOnly: true, export: true, calls: [{ name: 'run_command', args: { CommandLine: 'echo incomplete' } }], toolOutput: 'incomplete' },
      { toolStart: true, toolFailed: true, export: true, calls: [{ name: 'run_command', args: { CommandLine: 'false' } }], toolOutput: 'boom' },
      { toolStart: true },
    ],
  })
  const row = () => node.last('runtime.sessions')?.sessions.find((s) => s.session_id === SESSION)
  const refresh = async (title) => {
    const before = node.all('runtime.sessions').length
    await node.peer.call('session.rename', { session_id: SESSION, title })
    await until('inventory refreshed', () => node.all('runtime.sessions').length > before)
    return row()
  }

  // Tool started without a completed tool_call_update (toolResults === 0 < liveToolsById.size === 1) must stay fail-closed.
  await node.peer.call('session.send', { session_id: SESSION, text: 'Incomplete tool' })
  await until('incomplete tool turn settled', () => runtime.idle())
  assert.equal((await refresh('incomplete-tool')).status, 'unknown')

  // Tool ending with status 'failed' (GENERIC status ERROR, 0 DONE results) must stay fail-closed.
  await node.peer.call('session.send', { session_id: SESSION, text: 'Failed tool' })
  await until('failed tool turn settled', () => runtime.idle())
  assert.equal((await refresh('failed-tool')).status, 'unknown')

  // Delayed export (persistedKey === '') for a 1-tool turn must reject 2 GENERIC steps (extra step from another turn).
  await node.peer.call('session.send', { session_id: SESSION, text: 'Delayed one-tool turn' })
  await until('delayed tool turn settled', () => runtime.idle())
  appendFileSync(transcript, [
    { step_index: 10, type: 'USER_INPUT', source: 'USER_EXPLICIT', status: 'DONE', created_at: later, content: 'Delayed one-tool turn' },
    { step_index: 11, type: 'GENERIC', status: 'DONE', created_at: later, content: 'First tool output' },
    { step_index: 12, type: 'GENERIC', status: 'DONE', created_at: later, content: 'Unexpected second tool output' },
  ].map((s) => JSON.stringify(s)).join('\n') + '\n')
  assert.equal((await refresh('extra-generic-before-bind')).status, 'unknown')
  await restart()
  assert.equal(row().status, 'unknown', 'extra GENERIC steps did not persist an idle marker')
})

test('autonomous wake clears pending unpersisted tool-only settled state so later GENERIC output stays unknown', async (t) => {
  const { node, runtime, home, transcript } = await fixture(t, {
    runtimeOptions: { exportPollMs: 40, autonomousIdleMs: 450, exportSettleTimeoutMs: 450 },
    promptPlan: [{ toolStart: true, wakeOnFile: 'trigger-wake' }],
  })
  const row = () => node.last('runtime.sessions')?.sessions.find((s) => s.session_id === SESSION)
  const refresh = async (title) => {
    const before = node.all('runtime.sessions').length
    await node.peer.call('session.rename', { session_id: SESSION, title })
    await until('inventory refreshed', () => node.all('runtime.sessions').length > before)
    return row()
  }

  // Tool-only turn finishes before export arrives, leaving settledTurns with persistedKey === ''.
  await node.peer.call('session.send', { session_id: SESSION, text: 'Tool before wake' })
  await until('tool turn settled', () => runtime.idle())
  // Trigger an autonomous wake; liveTurnForUpdate must clear settledTurns / TURN_COMPLETION / noCutChecks.
  writeFileSync(join(home, 'trigger-wake'), '')
  await until('autonomous wake working', () => !runtime.idle() && row()?.status === 'working')
  await until('autonomous wake retired on inactivity', () => runtime.idle() && row()?.status !== 'working')

  // Later export matching the old tool count cannot hijack the cleared settledTurns record.
  appendFileSync(transcript, [
    { step_index: 4, type: 'USER_INPUT', source: 'USER_EXPLICIT', status: 'DONE', created_at: later, content: 'Tool before wake' },
    { step_index: 5, type: 'GENERIC', status: 'DONE', created_at: later, content: 'Wake intermediate output' },
  ].map((s) => JSON.stringify(s)).join('\n') + '\n')
  assert.equal((await refresh('after-wake-export')).status, 'unknown')
})
