import assert from 'node:assert/strict'
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { setTimeout as delay } from 'node:timers/promises'
import { fileURLToPath } from 'node:url'
import { DatabaseSync } from 'node:sqlite'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { MCP_SERVER_NAMES } from '../../runtime-sdk/lib/compat.js'
import { AgyAcpClient } from '../lib/acp.js'
import { toPersonaId } from '../lib/map.js'
import { createAgyRuntime } from '../lib/runtime.js'

const FAKE = fileURLToPath(new URL('./fake-acp-cancel.mjs', import.meta.url))
const SESSION = '0c7d2c4e-5b1a-4f3e-9a6d-2f8b1c3d4e5f' // the session the fake agent opens
const SLOW = 15_000 // spawning a node process can be slow on a busy machine

/** A temp dir with the fake's record file; afterwards every fake it started is killed and the dir removed. */
function scratch(t) {
  const dir = mkdtempSync(join(tmpdir(), 'agy-cancel-'))
  const record = join(dir, 'record.jsonl')
  writeFileSync(record, '')
  t.after(() => {
    for (const m of received(record)) {
      if (m.started) {
        try {
          process.kill(m.started, 'SIGKILL')
        } catch {}
      }
    }
    rmSync(dir, { recursive: true, force: true })
  })
  return { dir, record }
}

/** What the fake agent received, in order. */
function received(record) {
  return readFileSync(record, 'utf8').split('\n').filter(Boolean).map((line) => JSON.parse(line))
}

/** The answers to the fake's session/request_permission requests. */
function permissionAnswers(record) {
  return received(record).filter((m) => typeof m.id === 'string' && m.id.startsWith('perm-'))
}

const cancelled = { outcome: { outcome: 'cancelled' } }

test('cancel is an ACP notification: the running prompt ends with stopReason cancelled', async (t) => {
  const { record } = scratch(t)
  const acp = new AgyAcpClient({ bin: process.execPath, args: [FAKE, '--record', record, 'acp'] })
  t.after(() => acp.stop())
  await acp.ensureStarted()
  const prompt = acp.prompt(SESSION, [{ type: 'text', text: 'hold' }])
  await until('the prompt reaches the agent', () => received(record).some((m) => m.method === 'session/prompt'), SLOW)
  await acp.cancel(SESSION)
  assert.deepEqual(await prompt, { stopReason: 'cancelled' })
  const cancels = received(record).filter((m) => m.method === 'session/cancel')
  assert.equal(cancels.length, 1)
  assert.equal('id' in cancels[0], false, 'session/cancel carries no id')
  assert.deepEqual(cancels[0].params, { sessionId: SESSION })
})

test('cancel with no agy running starts none and resolves', async (t) => {
  const { record } = scratch(t)
  const acp = new AgyAcpClient({ bin: process.execPath, args: [FAKE, '--record', record, 'acp'] })
  t.after(() => acp.stop())
  await acp.cancel(SESSION)
  assert.equal(acp.proc, null)
  assert.deepEqual(received(record), [], 'no agy process was started')
})

test('prompt inactivity resets only on updates for the active session', async (t) => {
  const { record } = scratch(t)
  const acp = new AgyAcpClient({ bin: process.execPath, args: [FAKE, '--record', record, 'acp'], promptInactivityMs: 350 })
  t.after(() => acp.stop())
  assert.deepEqual(await acp.prompt(SESSION, [{ type: 'text', text: 'updates' }]), { stopReason: 'end_turn' })
  await assert.rejects(acp.prompt(SESSION, [{ type: 'text', text: 'unrelated-updates' }]), /prompt inactive for 350ms/)
  await until('watchdog cancels the session', () => received(record).some((m) => m.method === 'session/cancel' &&
    m.id === undefined && m.params.sessionId === SESSION), SLOW)
  assert.equal(acp.pending.size, 0)
})

test('prompt inactivity pauses for permission and resumes after the answer', async (t) => {
  const { record } = scratch(t)
  let answerPermission
  const acp = new AgyAcpClient({ bin: process.execPath, args: [FAKE, '--record', record, 'acp'], promptInactivityMs: 350,
    onPermissionRequest: () => new Promise((resolve) => { answerPermission = resolve }) })
  t.after(() => acp.stop())
  const prompt = acp.prompt(SESSION, [{ type: 'text', text: 'ask' }])
  const rejected = assert.rejects(prompt, /prompt inactive for 350ms/)
  await until('permission request received', () => answerPermission, SLOW)
  await delay(500)
  assert.equal(acp.pending.size, 1)
  assert.equal(received(record).some((m) => m.method === 'session/cancel'), false)
  answerPermission(cancelled)
  await rejected
  assert.equal(acp.pending.size, 0)
})

test('a disabled prompt watchdog leaves cancellation to the caller', async (t) => {
  const { record } = scratch(t)
  const acp = new AgyAcpClient({ bin: process.execPath, args: [FAKE, '--record', record, 'acp'], promptInactivityMs: 0 })
  t.after(() => acp.stop())
  const prompt = acp.prompt(SESSION, [{ type: 'text', text: 'hold' }])
  await until('prompt reached the agent', () => received(record).some((m) => m.method === 'session/prompt'), SLOW)
  assert.equal(acp.pending.size, 1)
  await acp.cancel(SESSION)
  assert.deepEqual(await prompt, { stopReason: 'cancelled' })
})

/** The agy runtime on a fake node, with an executable fake agy; nobody answers a card on the phone. */
async function agyRuntime(t, options = {}) {
  const { dir, record } = scratch(t)
  const agyBin = join(dir, 'agy')
  writeFileSync(agyBin, `#!/bin/sh\nexec '${process.execPath}' '${FAKE}' --record '${record}' "$@"\n`)
  chmodSync(agyBin, 0o755)
  // agy keeps each conversation in brain/<id>: the runtime lists the sessions it finds there.
  const agyDir = join(dir, 'config')
  mkdirSync(join(agyDir, 'brain', SESSION), { recursive: true })
  const n = await startFakeNode(t, options.node)
  n.resolve = () => new Promise(() => {})
  options.beforeStart?.(agyDir)
  const runtime = createAgyRuntime({ agyBin, agyDir, home: dir, socketPath: n.path, log: () => {}, ...options.runtime })
  t.after(() => runtime.stop())
  runtime.start()
  await n.ready
  const start = async (prompt, params) => {
    const { session } = await n.peer.call('session.start', { cwd: dir, prompt, ...params })
    assert.equal(session.session_id, toPersonaId(SESSION))
    return session.session_id
  }
  const status = (pid) => n.last('runtime.sessions')?.sessions.find((s) => s.session_id === pid)?.status
  // A rename pushes the inventory again: the status as the list reads it now.
  const refresh = async (pid) => {
    const before = n.all('runtime.sessions').length
    await n.peer.call('session.rename', { session_id: pid, title: 'Renamed' })
    await until('inventory refreshed', () => n.all('runtime.sessions').length > before, SLOW)
    return status(pid)
  }
  return { n, record, start, status, refresh, idle: () => runtime.idle() }
}

test('queue edits preserve photos and send-now cancels just the active prompt', async (t) => {
  const { n, record, start, idle } = await agyRuntime(t)
  assert.equal(n.hello.caps.queue_edit, true)
  assert.equal(n.hello.caps.queue_send_now, true)
  const session_id = await start('ask', { assistant: true })
  await until('permission card is waiting', () => n.asked.length > 0, SLOW)
  const row = () => n.last('runtime.sessions')?.sessions.find((session) => session.session_id === session_id)
  const send = (text, images) => n.peer.call('session.send', { session_id, text, images, assistant: true })
  const edit = (keep) => n.peer.call('session.queue.edit', { session_id, keep })
  const steer = () => n.peer.call('session.queue.send_now', { session_id })
  await assert.rejects(edit([]), { code: 'nothing_queued' })
  await assert.rejects(steer(), { code: 'nothing_queued' })
  const image = { mime: 'image/png', data: 'aW1hZ2U=' }
  for (const [text, images] of [['drop'], ['', [image]], ['reply']]) {
    assert.deepEqual(await send(text, images), { ok: true, queued: true })
  }
  await until('raw queue is visible', () => row()?.queued?.length === 3, SLOW)
  assert.deepEqual(row().queued, ['drop', '', 'reply'])
  for (const keep of [[3], [-1], [0, 0], [0.5]]) {
    await assert.rejects(edit(keep), { code: 'queue_changed' })
  }
  assert.deepEqual(await edit([2, 1]), { queue: ['drop', '', 'reply'], images: [null, [image], null] })
  await until('edited queue is visible', () => row()?.queued?.length === 2, SLOW)
  assert.deepEqual(row().queued, ['reply', ''])
  assert.deepEqual(await steer(), { sent: true })
  const prompts = () => received(record).filter((message) => message.method === 'session/prompt')
  await until('both follow-ups reach ACP', () => prompts().length === 3, SLOW)
  assert.deepEqual(prompts()[1].params.prompt, [{ type: 'text', text: 'reply' }])
  assert.deepEqual(prompts()[2].params.prompt, [{ type: 'image', mimeType: image.mime, data: image.data }])
  assert.equal(received(record).filter((message) => message.method === 'session/cancel').length, 1)
  assert.deepEqual(permissionAnswers(record)[0]?.result, cancelled)
  await until('popped queue is no longer visible', () => row() && !row().queued, SLOW)
  assert.equal(idle(), false)
  assert.equal((await n.peer.call('session.interrupt', { session_id })).settled, true)
})

for (const failPreparation of [false, true]) {
  test(`send-now skips a preparing turn and ${failPreparation ? 'reports a successor preparation error' : 'starts the queued prompt'}`, async (t) => {
    const { n, record, start } = await agyRuntime(t)
    const session_id = await start()
    const gate = Promise.withResolvers()
    const original = AgyAcpClient.prototype.ensureSessionLoaded
    let loads = 0
    t.mock.method(AgyAcpClient.prototype, 'ensureSessionLoaded', async function (...args) {
      loads += 1
      if (loads === 1) await gate.promise
      else if (failPreparation) throw new Error('Queued turn could not load its session')
      return original.apply(this, args)
    })
    const first = n.peer.call('session.send', { session_id, text: 'hold' })
    try {
      await until('first turn preparing', () => loads === 1, SLOW)
      assert.deepEqual(await n.peer.call('session.send', { session_id, text: 'reply' }), { ok: true, queued: true })
      assert.deepEqual(await n.peer.call('session.queue.send_now', { session_id }), { sent: true })
      await until('preparing turn cancelled', () => received(record).some((call) => call.method === 'session/cancel'), SLOW)
    } finally {
      gate.resolve()
      await first
    }

    const row = () => n.last('runtime.sessions')?.sessions.find((session) => session.session_id === session_id)
    if (failPreparation) {
      await until('successor preparation error published', () => row()?.turn_error === 'Queued turn could not load its session', SLOW)
    } else {
      await until('queued prompt completes', () => row()?.last_text === 'Finished.', SLOW)
    }
    assert.equal(loads, 2)
    const prompts = received(record).filter((call) => call.method === 'session/prompt')
    assert.deepEqual(prompts.map((call) => call.params.prompt), failPreparation ? [] : [[{ type: 'text', text: 'reply' }]])
  })
}

test('start with empty image entries opens an idle session without prompting', async (t) => {
  const { n, record, idle } = await agyRuntime(t)
  const { session } = await n.peer.call('session.start', { images: [{ mime: 'image/png' }] })
  assert.equal(session.status, 'idle')
  assert.equal(idle(), true)
  assert.equal(received(record).some((message) => message.method === 'session/prompt'), false)
})

test('a wedged prompt surfaces an error and the next send can recycle ACP', async (t) => {
  const { n, record, start, idle } = await agyRuntime(t, { runtime: { promptInactivityMs: 350 } })
  const pid = await start('ignore-cancel')
  const row = () => n.last('runtime.sessions')?.sessions.find((session) => session.session_id === pid)
  await until('timeout published', () => row()?.turn_error?.includes('prompt inactive'), SLOW)
  assert.notEqual(row().status, 'working')
  assert.equal(idle(), true)
  await n.peer.call('session.send', { session_id: pid, text: 'reply' })
  await until('next prompt completes', () => row()?.last_text === 'Finished.', SLOW)
  assert.equal(row().turn_error, undefined)
  assert.equal(received(record).filter((m) => m.started).length, 2)
})

/** agy's own conversation summary for SESSION in agyDir: what nativeWorking reads. */
function nativeSummary(agyDir, status, stepCount) {
  const db = new DatabaseSync(join(agyDir, 'conversation_summaries.db'))
  try {
    db.exec('CREATE TABLE IF NOT EXISTS conversation_summaries (conversation_id TEXT PRIMARY KEY, status TEXT, not_fully_idle INTEGER, killed INTEGER, step_count INTEGER, title TEXT, workspace_uris TEXT, last_modified_time TEXT)')
    db.prepare('INSERT OR REPLACE INTO conversation_summaries VALUES (?, ?, 0, 0, ?, ?, ?, ?)')
      .run(SESSION, status, stepCount, '', '[]', new Date().toISOString())
  } finally {
    db.close()
  }
}

// #11108: interrupt answers settled once the running turn has ended, so agent_interrupt need not poll for it.
test('interrupt answers settled once agy ends the cancelled turn', async (t) => {
  const { n, record, start, status } = await agyRuntime(t)
  const pid = await start('hold')
  await until('the prompt is running', () => received(record).some((m) => m.method === 'session/prompt'), SLOW)
  await until('the row is working', () => status(pid) === 'working', SLOW)
  const res = await n.peer.call('session.interrupt', { session_id: pid })
  assert.equal(res?.settled, true)
  assert.ok(received(record).some((m) => m.ended === SESSION), 'agy ended the prompt before settled was answered')
  await until('the row is no longer working', () => status(pid) !== 'working', SLOW)
})

test('interrupt does not answer settled when agy never ends the cancelled turn', async (t) => {
  const { n, record, start, status } = await agyRuntime(t, { runtime: { interruptSettleMs: 300 } })
  const pid = await start('ignore-cancel')
  await until('the prompt is running', () => received(record).some((m) => m.method === 'session/prompt'), SLOW)
  const res = await n.peer.call('session.interrupt', { session_id: pid })
  assert.notEqual(res?.settled, true)
  assert.ok(received(record).some((m) => m.method === 'session/cancel'), 'the cancel was sent')
  await until('the row is still working', () => status(pid) === 'working', SLOW)
})

// #11108: agy's summary can still say running right after a confirmed cancel; that alone is not work. A new native
// step is.
test('a summary still running after a confirmed cancel does not keep the row working until a new step', async (t) => {
  let agyDir
  const { n, record, start, status, refresh } = await agyRuntime(t, {
    beforeStart: (dir) => { agyDir = dir; nativeSummary(dir, 'CASCADE_RUN_STATUS_RUNNING', 1) },
  })
  const pid = await start('hold')
  await until('the prompt is running', () => received(record).some((m) => m.method === 'session/prompt'), SLOW)
  assert.equal((await n.peer.call('session.interrupt', { session_id: pid }))?.settled, true)
  await until('the row is no longer working', () => status(pid) !== undefined && status(pid) !== 'working', SLOW)
  assert.notEqual(await refresh(pid), 'working', 'the stale summary alone does not make it working again')
  nativeSummary(agyDir, 'CASCADE_RUN_STATUS_RUNNING', 2)
  assert.equal(await refresh(pid), 'working', 'a new native step is work again')
})

test('interrupt withdraws the permission card, answers agy cancelled and ends the turn', async (t) => {
  const { n, record, start, status } = await agyRuntime(t)
  const pid = await start('ask')
  await until('the card is on the phone', () => n.asked.length === 1, SLOW)
  assert.equal(n.asked[0].kind, 'permission')
  await n.peer.call('session.interrupt', { session_id: pid })
  await until('the card is withdrawn', () => n.last('interaction.cancel')?.id === n.asked[0].id, SLOW)
  await until('agy hears cancelled', () => permissionAnswers(record).length === 1, SLOW)
  assert.deepEqual(permissionAnswers(record)[0].result, cancelled)
  const cancels = received(record).filter((m) => m.method === 'session/cancel')
  assert.equal(cancels.length, 1)
  assert.equal('id' in cancels[0], false, 'session/cancel carries no id')
  await until('the turn ends', () => status(pid) === 'done', SLOW)
})

test('close cancels the active turn and withdraws its permission card even without native session/close', async (t) => {
  const { n, record, start, status } = await agyRuntime(t)
  const pid = await start('ask')
  await until('the card is on the phone', () => n.asked.length === 1, SLOW)
  await n.peer.call('session.close', { session_id: pid })
  await until('the card is withdrawn', () => n.last('interaction.cancel')?.id === n.asked[0].id, SLOW)
  await until('the native prompt ended', () => received(record).some((m) => m.ended === SESSION), SLOW)
  assert.deepEqual(permissionAnswers(record)[0].result, cancelled)
  await until('the session is no longer live', () => status(pid) === undefined, SLOW)
  await assert.rejects(n.peer.call('session.send', { session_id: pid, text: 'Continue' }), /session is closed/)
  const hellos = n.hellos
  const inventories = n.all('runtime.sessions').length
  n.peer.close()
  await until('the runtime reconnects', () => n.hellos > hellos, SLOW)
  await until('the closed inventory is republished', () => n.all('runtime.sessions').length > inventories && n.last('runtime.sessions')?.sessions.length === 0, SLOW)
})

test('close prevents queued prompts from starting after cancellation', async (t) => {
  const { n, record, start, status } = await agyRuntime(t)
  const pid = await start('hold')
  await until('the first prompt is running', () => received(record).some((m) => m.method === 'session/prompt'), SLOW)
  await n.peer.call('session.send', { session_id: pid, text: 'Queued' })
  await n.peer.call('session.close', { session_id: pid })
  await until('the native prompt ended', () => received(record).some((m) => m.ended === SESSION), SLOW)
  await until('the session is no longer live', () => status(pid) === undefined, SLOW)
  assert.equal(received(record).filter((m) => m.method === 'session/prompt').length, 1)
})

test('a permission request crossing close is cancelled without a new card', async (t) => {
  const { n, record, start, status } = await agyRuntime(t)
  const pid = await start('ask-on-cancel')
  await until('the prompt is running', () => received(record).some((m) => m.method === 'session/prompt'), SLOW)
  await n.peer.call('session.close', { session_id: pid })
  await until('the native prompt ended', () => received(record).some((m) => m.ended === SESSION), SLOW)
  await until('the session is no longer live', () => status(pid) === undefined, SLOW)
  assert.equal(n.asked.length, 0)
  assert.deepEqual(permissionAnswers(record)[0].result, cancelled)
})

test('a permission request that crosses the interrupt is answered cancelled without a card', async (t) => {
  const { n, record, start, status } = await agyRuntime(t)
  const pid = await start('ask-on-cancel')
  await until('the prompt reaches the agent', () => received(record).some((m) => m.method === 'session/prompt'), SLOW)
  await n.peer.call('session.interrupt', { session_id: pid })
  await until('agy hears cancelled', () => permissionAnswers(record).length === 1, SLOW)
  assert.deepEqual(permissionAnswers(record)[0].result, cancelled)
  await until('the turn ends', () => status(pid) === 'done', SLOW)
  assert.equal(n.asked.length, 0, 'no card went to the phone')
})

test('a card still out when the turn ends is withdrawn and agy hears cancelled', async (t) => {
  const { n, record, start, status } = await agyRuntime(t)
  const pid = await start('ask-then-end')
  await until('agy hears cancelled', () => permissionAnswers(record).length === 1, SLOW)
  assert.deepEqual(permissionAnswers(record)[0].result, cancelled)
  await until('the card is withdrawn', () => n.last('interaction.cancel'), SLOW)
  for (const ix of n.asked) assert.ok(n.all('interaction.cancel').some((c) => c.id === ix.id), 'every card that went out is taken off')
  await until('the turn ends', () => status(pid) === 'done', SLOW)
})

test('an answered card still selects the matching agy option', async (t) => {
  const { n, record, start, status } = await agyRuntime(t)
  n.resolve = () => ({ allow: true })
  const pid = await start('ask')
  await until('agy hears the answer', () => permissionAnswers(record).length === 1, SLOW)
  assert.deepEqual(permissionAnswers(record)[0].result, { outcome: { outcome: 'selected', optionId: 'allow' } })
  assert.equal(n.all('interaction.cancel').length, 0, 'an answered card is not withdrawn')
  await n.peer.call('session.interrupt', { session_id: pid })
  await until('the turn ends', () => status(pid) === 'done', SLOW)
})

test('interrupt cancels every queued follow-up without applying its options', async (t) => {
  const { n, record, start, status } = await agyRuntime(t)
  const pid = await start('hold')
  await until('first prompt running', () => received(record).some((m) => m.method === 'session/prompt'), SLOW)
  await n.peer.call('session.send', { session_id: pid, text: 'Queued one', model: 'queued-model' })
  await n.peer.call('session.send', { session_id: pid, text: 'Queued two' })
  assert.deepEqual(await n.peer.call('session.interrupt', { session_id: pid }), { ok: true, settled: true })
  await assert.rejects(n.peer.call('session.queue.send_now', { session_id: pid }), { code: 'nothing_queued' })
  await until('queue no longer working', () => status(pid) !== 'working', SLOW)
  assert.equal(received(record).filter((m) => m.method === 'session/prompt').length, 1)
  assert.equal(received(record).some((m) => m.params?.modelId === 'queued-model'), false)
})

test('permission cards use the runtime, session, kind and tool call key', async (t) => {
  const { n, start } = await agyRuntime(t)
  const pid = await start('ask')
  await until('permission card', () => n.asked.length > 0, SLOW)
  const { interactionId } = await import('../../runtime-sdk/index.js')
  assert.equal(n.asked[0].id, interactionId('agy', pid, 'permission', 'call-1'))
})

test('permission cards normalize canonical tool names before display titles', async (t) => {
  const cases = [
    ['_meta.toolName beats name and a raw-shell title',
      { _meta: { toolName: 'run_command' }, name: 'view_file', kind: 'read', title: 'printf hello', rawInput: { CommandLine: 'printf hello' } },
      'Bash', { command: 'printf hello', description: '' }, 'printf hello'],
    ['name beats a raw-shell title',
      { name: 'run_command', kind: 'execute', title: 'git status --short', rawInput: { command: 'git status --short' } },
      'Bash', { command: 'git status --short', description: '' }, 'git status --short'],
    ['name beats a File title for a write',
      { name: 'write_to_file', kind: 'edit', title: 'File /tmp/result.txt', rawInput: { TargetFile: '/tmp/result.txt', CodeContent: 'new\n' } },
      'Write', { file_path: '/tmp/result.txt', content: 'new\n' }, '/tmp/result.txt'],
    ['execute kind normalizes a raw-shell title',
      { kind: 'execute', title: 'npm test', rawInput: { command: 'npm test' } },
      'Bash', { command: 'npm test', description: '' }, 'npm test'],
    ['edit kind plus Content normalizes a File title to Write',
      { kind: 'edit', title: 'File /tmp/draft.txt', rawInput: { Path: '/tmp/draft.txt', Content: 'draft', Overwrite: true } },
      'Write', { file_path: '/tmp/draft.txt', content: 'draft' }, '/tmp/draft.txt'],
    ['read kind remains Read',
      { kind: 'read', title: 'File /tmp/input.txt', rawInput: { AbsolutePath: '/tmp/input.txt' } },
      'Read', { file_path: '/tmp/input.txt' }, '/tmp/input.txt'],
    ['command evidence cannot be downgraded by read presentation',
      { kind: 'read', title: 'File /tmp/result.txt', rawInput: { CommandLine: 'cat /tmp/result.txt' } },
      'Bash', { command: 'cat /tmp/result.txt', description: '' }, 'cat /tmp/result.txt'],
    ['write evidence cannot be downgraded by read presentation',
      { kind: 'read', title: 'File /tmp/draft.txt', rawInput: { TargetFile: '/tmp/draft.txt', Content: 'new' } },
      'Write', { file_path: '/tmp/draft.txt', content: 'new' }, '/tmp/draft.txt'],
    ['execute kind retains a command supplied only by the title',
      { kind: 'execute', title: 'git diff --check', rawInput: {} },
      'Bash', { command: 'git diff --check', description: '' }, 'git diff --check'],
    ['edit kind retains a path supplied only by a File title',
      { kind: 'edit', title: 'File /tmp/from-title.txt', rawInput: { Content: 'new' } },
      'Write', { file_path: '/tmp/from-title.txt', content: 'new' }, '/tmp/from-title.txt'],
    ['a distinct human title remains readable beside the command',
      { name: 'run_command', kind: 'execute', title: 'Run focused tests', rawInput: { command: 'npm test' } },
      'Bash', { command: 'npm test', description: 'Run focused tests' }, 'npm test'],
    ['Run-prefixed Persona MCP title normalizes to mcp__persona__<tool>',
      { kind: 'other', title: 'Run mcp_persona_19d68ad7_todo_decisions', rawInput: { todo_id: 'todo_1' } },
      'mcp__persona__todo_decisions', { todo_id: 'todo_1' }, 'todo_id: todo_1'],
    ['Run-prefixed proxied persona_call title unwraps target tool',
      { kind: 'other', title: `Run mcp_${MCP_SERVER_NAMES[1]}_19d68ad7_persona_call`, rawInput: { name: 'todo_decisions', arguments: { todo_id: 'todo_2' } } },
      'mcp__persona__todo_decisions', { todo_id: 'todo_2' }, 'todo_id: todo_2'],
  ]
  for (const [name, toolCall, toolName, toolInput, summary] of cases) {
    await t.test(name, async (t) => {
      const { n, start } = await agyRuntime(t)
      await start(`ask-shape ${JSON.stringify(toolCall)}`)
      await until('permission card', () => n.asked.length === 1, SLOW)
      assert.equal(n.asked[0].tool_name, toolName)
      assert.deepEqual(n.asked[0].tool_input, toolInput)
      assert.equal(n.asked[0].summary, summary)
    })
  }
})


test('cancelling before a prompt starts does not poison the next permission request', async (t) => {
  const { n, record, start } = await agyRuntime(t)
  const pid = await start()
  n.resolve = () => ({ allow: true })
  const [, interrupted] = await Promise.all([
    n.peer.call('session.send', { session_id: pid, text: 'hold' }),
    n.peer.call('session.interrupt', { session_id: pid }),
  ])
  assert.equal(interrupted.settled, true)
  assert.equal(received(record).some((call) => call.method === 'session/prompt'), false)
  await n.peer.call('session.send', { session_id: pid, text: 'ask' })
  await until('next permission answered', () => permissionAnswers(record).length > 0, SLOW)
  assert.equal(n.asked.length, 1)
  assert.deepEqual(permissionAnswers(record)[0].result, { outcome: { outcome: 'selected', optionId: 'allow' } })
  await n.peer.call('session.interrupt', { session_id: pid })
})

test('interrupt does not report settled while a later send is running', async (t) => {
  const { n, record, start, status } = await agyRuntime(t)
  const pid = await start('hold')
  await until('first prompt running', () => received(record).some((call) => call.method === 'session/prompt'), SLOW)
  let release, entered = false
  const gate = new Promise((resolve) => { release = resolve })
  const original = AgyAcpClient.prototype.cancel
  const mocked = t.mock.method(AgyAcpClient.prototype, 'cancel', async function (...args) {
    await original.apply(this, args)
    entered = true
    await gate
  })
  const interrupted = n.peer.call('session.interrupt', { session_id: pid })
  try {
    await until('old turn ended during cancel', () => entered && received(record).some((call) => call.ended === SESSION) && status(pid) !== 'working', SLOW)
    await n.peer.call('session.send', { session_id: pid, text: 'hold' })
    await until('later prompt running', () => received(record).filter((call) => call.method === 'session/prompt').length === 2, SLOW)
  } finally {
    release()
    mocked.mock.restore()
  }
  assert.notEqual((await interrupted).settled, true)
  await n.peer.call('session.interrupt', { session_id: pid })
})

test('an uncancelled successor clears cancellation left by a preparing turn', async (t) => {
  const { n, record, start } = await agyRuntime(t)
  const pid = await start()
  n.resolve = () => ({ allow: true })
  let release, entered = false, hold = true
  const gate = new Promise((resolve) => { release = resolve })
  const original = AgyAcpClient.prototype.ensureSessionLoaded
  const mocked = t.mock.method(AgyAcpClient.prototype, 'ensureSessionLoaded', async function (...args) {
    if (hold) {
      hold = false
      entered = true
      await gate
    }
    return original.apply(this, args)
  })
  const first = n.peer.call('session.send', { session_id: pid, text: 'hold' })
  let interrupted
  try {
    await until('first turn preparing', () => entered, SLOW)
    interrupted = n.peer.call('session.interrupt', { session_id: pid })
    await until('preparing turn cancelled', () => received(record).some((call) => call.method === 'session/cancel'), SLOW)
    assert.equal((await n.peer.call('session.send', { session_id: pid, text: 'ask' })).queued, true)
    assert.equal(received(record).some((call) => call.method === 'session/prompt'), false)
  } finally {
    release()
    mocked.mock.restore()
    await first
    await interrupted
  }
  await until('successor permission answered', () => permissionAnswers(record).length === 1, SLOW)
  assert.equal(received(record).filter((call) => call.method === 'session/prompt').length, 1)
  assert.equal(n.asked.length, 1)
  assert.deepEqual(permissionAnswers(record)[0].result, { outcome: { outcome: 'selected', optionId: 'allow' } })
  await n.peer.call('session.interrupt', { session_id: pid })
})

test('permission cards forward always_label, honor always responses, and never select reject options on allow or bypass', async (t) => {
  const fullOptions = [
    { optionId: 'once', name: 'Allow once', kind: 'allow_once' },
    { optionId: 'always', name: 'Always allow run_command', kind: 'allow_always' },
    { optionId: 'reject', name: 'Reject', kind: 'reject_once' },
  ]

  await t.test('allow_always exposes always_label and selects allow_always on { allow: true, always: true }', async (t) => {
    const { n, record, start } = await agyRuntime(t)
    n.resolve = () => ({ allow: true, always: true })
    const pid = await start(`ask-options ${JSON.stringify(fullOptions)}`)
    await until('permission answered', () => permissionAnswers(record).length === 1, SLOW)
    assert.equal(n.asked[0].always_label, 'Always allow run_command')
    assert.deepEqual(permissionAnswers(record)[0].result, { outcome: { outcome: 'selected', optionId: 'always' } })
    await n.peer.call('session.interrupt', { session_id: pid })
  })

  await t.test('allow_once is selected on { allow: true, always: false } when both options exist', async (t) => {
    const { n, record, start } = await agyRuntime(t)
    n.resolve = () => ({ allow: true, always: false })
    const pid = await start(`ask-options ${JSON.stringify(fullOptions)}`)
    await until('permission answered', () => permissionAnswers(record).length === 1, SLOW)
    assert.equal(n.asked[0].always_label, 'Always allow run_command')
    assert.deepEqual(permissionAnswers(record)[0].result, { outcome: { outcome: 'selected', optionId: 'once' } })
    await n.peer.call('session.interrupt', { session_id: pid })
  })

  await t.test('blank allow_always name falls back to Always allow', async (t) => {
    const { n, record, start } = await agyRuntime(t)
    n.resolve = () => ({ allow: true, always: true })
    const opts = [
      { optionId: 'once', name: 'Allow', kind: 'allow_once' },
      { optionId: 'always', name: '   ', kind: 'allow_always' },
    ]
    const pid = await start(`ask-options ${JSON.stringify(opts)}`)
    await until('permission answered', () => permissionAnswers(record).length === 1, SLOW)
    assert.equal(n.asked[0].always_label, 'Always allow')
    assert.deepEqual(permissionAnswers(record)[0].result, { outcome: { outcome: 'selected', optionId: 'always' } })
    await n.peer.call('session.interrupt', { session_id: pid })
  })

  await t.test('without allow_always, always_label is omitted and always falls back to allow_once', async (t) => {
    const { n, record, start } = await agyRuntime(t)
    n.resolve = () => ({ allow: true, always: true })
    const pid = await start('ask')
    await until('permission answered', () => permissionAnswers(record).length === 1, SLOW)
    assert.equal('always_label' in n.asked[0], false)
    assert.deepEqual(permissionAnswers(record)[0].result, { outcome: { outcome: 'selected', optionId: 'allow' } })
    await n.peer.call('session.interrupt', { session_id: pid })
  })

  await t.test('reject-only options cancel on allow instead of picking opts[0]', async (t) => {
    const { n, record, start } = await agyRuntime(t)
    n.resolve = () => ({ allow: true })
    const rejectOnly = [{ optionId: 'reject', name: 'Reject', kind: 'reject_once' }]
    const pid = await start(`ask-options ${JSON.stringify(rejectOnly)}`)
    await until('permission answered', () => permissionAnswers(record).length === 1, SLOW)
    assert.equal('always_label' in n.asked[0], false)
    assert.deepEqual(permissionAnswers(record)[0].result, cancelled)
    await n.peer.call('session.interrupt', { session_id: pid })
  })

  await t.test('bypassPermissions cancels reject-only options without prompting and still selects allow_always when offered', async (t) => {
    const { n, record, start } = await agyRuntime(t)
    const rejectOnly = [{ optionId: 'reject', name: 'Reject', kind: 'reject_once' }]
    const pid = await start(`ask-options ${JSON.stringify(rejectOnly)}`, { permission_mode: 'bypassPermissions' })
    await until('bypass reject-only answered', () => permissionAnswers(record).length === 1, SLOW)
    assert.equal(n.asked.length, 0)
    assert.deepEqual(permissionAnswers(record)[0].result, cancelled)
    await n.peer.call('session.interrupt', { session_id: pid })

    await n.peer.call('session.send', { session_id: pid, text: `ask-options ${JSON.stringify(fullOptions)}` })
    await until('bypass allow_always answered', () => permissionAnswers(record).length === 2, SLOW)
    assert.equal(n.asked.length, 0)
    assert.deepEqual(permissionAnswers(record)[1].result, { outcome: { outcome: 'selected', optionId: 'always' } })
    await n.peer.call('session.interrupt', { session_id: pid })
  })

  await t.test('dismissed or unanswered resolutions cancel instead of selecting reject_once or reject_always', async (t) => {
    const rejectAlwaysOptions = [
      { optionId: 'once', name: 'Allow', kind: 'allow_once' },
      { optionId: 'reject-forever', name: 'Always reject', kind: 'reject_always' },
    ]
    for (const answer of [{ dismiss: true }, undefined]) {
      const { n, record, start } = await agyRuntime(t)
      n.resolve = () => answer
      const pid = await start(`ask-options ${JSON.stringify(rejectAlwaysOptions)}`)
      await until('dismissed permission answered', () => permissionAnswers(record).length === 1, SLOW)
      assert.deepEqual(permissionAnswers(record)[0].result, cancelled)
      await n.peer.call('session.interrupt', { session_id: pid })
    }
  })

  await t.test('explicit deny selects reject_once even when always is set', async (t) => {
    const { n, record, start } = await agyRuntime(t)
    n.resolve = () => ({ allow: false, always: true })
    const pid = await start(`ask-options ${JSON.stringify(fullOptions)}`)
    await until('denied permission answered', () => permissionAnswers(record).length === 1, SLOW)
    assert.deepEqual(permissionAnswers(record)[0].result, { outcome: { outcome: 'selected', optionId: 'reject' } })
    await n.peer.call('session.interrupt', { session_id: pid })
  })

  await t.test('allow_always without allow_once does not escalate { allow: true, always: false }', async (t) => {
    const alwaysOnly = [
      { optionId: 'always', name: 'Always\n  allow\ttool', kind: 'allow_always' },
      { optionId: 'reject', name: 'Reject', kind: 'reject_once' },
    ]
    const { n, record, start } = await agyRuntime(t)
    n.resolve = () => ({ allow: true, always: false })
    const pid = await start(`ask-options ${JSON.stringify(alwaysOnly)}`)
    await until('single-use allow answered', () => permissionAnswers(record).length === 1, SLOW)
    assert.equal(n.asked[0].always_label, 'Always allow tool')
    assert.deepEqual(permissionAnswers(record)[0].result, cancelled)
    await n.peer.call('session.interrupt', { session_id: pid })

    n.resolve = () => ({ allow: true, always: true })
    await n.peer.call('session.send', { session_id: pid, text: `ask-options ${JSON.stringify(alwaysOnly)}` })
    await until('explicit always allow answered', () => permissionAnswers(record).length === 2, SLOW)
    assert.deepEqual(permissionAnswers(record)[1].result, { outcome: { outcome: 'selected', optionId: 'always' } })
    await n.peer.call('session.interrupt', { session_id: pid })
  })

  await t.test('Assistant sessions omit always_label and never pass allow_always to ACP', async (t) => {
    const { n, record, start } = await agyRuntime(t)
    assert.equal(n.hello.caps.keep_always, true)
    n.resolve = () => ({ allow: true, always: true })
    const pid = await start(`ask-options ${JSON.stringify(fullOptions)}`, { assistant: true })
    await until('assistant permission answered', () => permissionAnswers(record).length === 1, SLOW)
    assert.equal('always_label' in n.asked[0], false)
    assert.deepEqual(permissionAnswers(record)[0].result, { outcome: { outcome: 'selected', optionId: 'once' } })
    await n.peer.call('session.interrupt', { session_id: pid })

    // Remembered hub Always grant on a second matching call still selects allow_once, never allow_always.
    n.resolve = () => ({ allow: true, always: true, remembered: true })
    await n.peer.call('session.send', { session_id: pid, text: `ask-options ${JSON.stringify(fullOptions)}`, assistant: true })
    await until('remembered assistant permission answered', () => permissionAnswers(record).length === 2, SLOW)
    assert.equal('always_label' in n.asked[1], false)
    assert.deepEqual(permissionAnswers(record)[1].result, { outcome: { outcome: 'selected', optionId: 'once' } })
    await n.peer.call('session.interrupt', { session_id: pid })

    // Assistant request lacking allow_once (allow_always-only) cancels without raising a card or selecting allow_always.
    const alwaysOnly = [
      { optionId: 'always', name: 'Always allow', kind: 'allow_always' },
      { optionId: 'reject', name: 'Reject', kind: 'reject_once' },
    ]
    await n.peer.call('session.send', { session_id: pid, text: `ask-options ${JSON.stringify(alwaysOnly)}`, assistant: true })
    await until('assistant allow_always-only cancelled without asking hub', () => permissionAnswers(record).length === 3, SLOW)
    assert.equal(n.asked.length, 2)
    assert.deepEqual(permissionAnswers(record)[2].result, cancelled)
    await n.peer.call('session.interrupt', { session_id: pid })
  })

  await t.test('Assistant run_command in bypassPermissions routes through rt.ask when node has assistant_command_gate, forwards resolved Cwd, and never selects allow_always', async (t) => {
    const { n, record, start } = await agyRuntime(t, { node: { features: ['prefer_running', 'assistant_command_gate'] } })
    const olCmd = `ol POST /conversations/c1/messages '{"text":"hello"}'`
    const askSpec = {
      toolCall: { name: 'run_command', rawInput: { CommandLine: olCmd, Cwd: 'subdir' } },
      options: fullOptions,
    }
    n.resolve = () => ({ allow: true, always: true })
    const pid = await start(`ask-full ${JSON.stringify(askSpec)}`, { assistant: true, permission_mode: 'bypassPermissions' })
    await until('gated assistant bash allowed', () => permissionAnswers(record).length === 1, SLOW)
    assert.equal(n.asked.length, 1)
    assert.equal(n.asked[0].tool_name, 'Bash')
    assert.equal(n.asked[0].tool_input.command, olCmd)
    assert.equal(n.asked[0].cwd.endsWith('/subdir') || n.asked[0].cwd.endsWith('\\subdir'), true)
    assert.equal('always_label' in n.asked[0], false)
    assert.deepEqual(permissionAnswers(record)[0].result, { outcome: { outcome: 'selected', optionId: 'once' } })
    await n.peer.call('session.interrupt', { session_id: pid })

    // Explicit gate denial selects reject_once.
    n.resolve = () => ({ allow: false, message: 'denied by gate' })
    await n.peer.call('session.send', { session_id: pid, text: `ask-full ${JSON.stringify(askSpec)}`, assistant: true })
    await until('gated assistant bash denied', () => permissionAnswers(record).length === 2, SLOW)
    assert.equal(n.asked.length, 2)
    assert.deepEqual(permissionAnswers(record)[1].result, { outcome: { outcome: 'selected', optionId: 'reject' } })
    await n.peer.call('session.interrupt', { session_id: pid })

    // Non-Bash tool in bypassPermissions still bypasses locally and selects allow_once (never allow_always).
    const writeSpec = {
      toolCall: { name: 'write_file', rawInput: { Path: '/tmp/a.txt', Content: 'ok' } },
      options: fullOptions,
    }
    await n.peer.call('session.send', { session_id: pid, text: `ask-full ${JSON.stringify(writeSpec)}`, assistant: true })
    await until('assistant non-Bash bypassed with allow_once', () => permissionAnswers(record).length === 3, SLOW)
    assert.equal(n.asked.length, 2)
    assert.deepEqual(permissionAnswers(record)[2].result, { outcome: { outcome: 'selected', optionId: 'once' } })
    await n.peer.call('session.interrupt', { session_id: pid })

    // allow_always-only without allow_once cancels instead of leaking native always grant on Assistant bypass.
    const alwaysOnlyWrite = {
      toolCall: { name: 'write_file', rawInput: { Path: '/tmp/b.txt', Content: 'ok' } },
      options: [{ optionId: 'always', name: 'Always allow', kind: 'allow_always' }],
    }
    await n.peer.call('session.send', { session_id: pid, text: `ask-full ${JSON.stringify(alwaysOnlyWrite)}`, assistant: true })
    await until('assistant allow_always-only cancelled', () => permissionAnswers(record).length === 4, SLOW)
    assert.equal(n.asked.length, 2)
    assert.deepEqual(permissionAnswers(record)[3].result, cancelled)
    await n.peer.call('session.interrupt', { session_id: pid })
  })

  await t.test('Assistant run_command in bypassPermissions on an older node without assistant_command_gate bypasses locally with allow_once only', async (t) => {
    const { n, record, start } = await agyRuntime(t)
    const pid = await start(`ask-options ${JSON.stringify(fullOptions)}`, { assistant: true, permission_mode: 'bypassPermissions' })
    await until('legacy node assistant bypass answered', () => permissionAnswers(record).length === 1, SLOW)
    assert.equal(n.asked.length, 0)
    assert.deepEqual(permissionAnswers(record)[0].result, { outcome: { outcome: 'selected', optionId: 'once' } })
    await n.peer.call('session.interrupt', { session_id: pid })
  })

  await t.test('non-assistant run_command in bypassPermissions stays local even when node advertises assistant_command_gate', async (t) => {
    const { n, record, start } = await agyRuntime(t, { node: { features: ['prefer_running', 'assistant_command_gate'] } })
    const pid = await start(`ask-options ${JSON.stringify(fullOptions)}`, { permission_mode: 'bypassPermissions' })
    await until('non-assistant bypass answered', () => permissionAnswers(record).length === 1, SLOW)
    assert.equal(n.asked.length, 0)
    assert.deepEqual(permissionAnswers(record)[0].result, { outcome: { outcome: 'selected', optionId: 'always' } })
    await n.peer.call('session.interrupt', { session_id: pid })
  })

  await t.test('interrupting an Assistant turn while waiting on assistant_command_gate cancels cleanly', async (t) => {
    const { n, record, start } = await agyRuntime(t, { node: { features: ['prefer_running', 'assistant_command_gate'] } })
    const pid = await start(`ask-options ${JSON.stringify(fullOptions)}`, { assistant: true, permission_mode: 'bypassPermissions' })
    await until('gated assistant card pending', () => n.asked.length === 1, SLOW)
    await n.peer.call('session.interrupt', { session_id: pid })
    await until('gated assistant card cancelled on interrupt', () => permissionAnswers(record).length === 1, SLOW)
    assert.deepEqual(permissionAnswers(record)[0].result, cancelled)
  })

  await t.test('Assistant run_command over 24KB preserves the full unclipped command for node gate inspection', async (t) => {
    const { n, record, start } = await agyRuntime(t, { node: { features: ['prefer_running', 'assistant_command_gate'] } })
    const tail = `; lark-cli im +messages-send --chat-id oc_test --text "tail"`
    const longCmd = `echo ${'A'.repeat(26 * 1024)} ${tail}`
    const askSpec = {
      toolCall: { name: 'run_command', rawInput: { CommandLine: longCmd, Cwd: '', cwd: 'sub-2' } },
      options: fullOptions,
    }
    n.resolve = () => ({ allow: false, message: 'blocked' })
    const pid = await start(`ask-full ${JSON.stringify(askSpec)}`, { assistant: true, permission_mode: 'bypassPermissions' })
    await until('long assistant command denied by gate', () => permissionAnswers(record).length === 1, SLOW)
    assert.equal(n.asked.length, 1)
    assert.equal(n.asked[0].tool_input.command, longCmd)
    assert.equal(n.asked[0].tool_input.command.endsWith(tail), true)
    assert.equal(n.asked[0].cwd.endsWith('/sub-2') || n.asked[0].cwd.endsWith('\\sub-2'), true)
    assert.deepEqual(permissionAnswers(record)[0].result, { outcome: { outcome: 'selected', optionId: 'reject' } })
    await n.peer.call('session.interrupt', { session_id: pid })
  })

  await t.test('Assistant run_command in bypassPermissions does not fail open during a brief node disconnect', async (t) => {
    const { n, record, start } = await agyRuntime(t, {
      node: { features: ['prefer_running', 'assistant_command_gate'] },
      runtime: { retryMs: 400 },
    })
    const pid = await start('reply', { assistant: true, permission_mode: 'bypassPermissions' })
    await until('first reply settled', () => received(record).some((m) => m.method === 'session/prompt'), SLOW)
    const triggerFile = `${record}.go`
    const askSpec = {
      toolCall: { name: 'run_command', rawInput: { CommandLine: `ol POST /conversations/c1/messages '{"text":"reconnect"}'` } },
      options: fullOptions,
      waitFile: triggerFile,
    }
    let releaseHello
    n.holdHello = new Promise((resolve) => { releaseHello = resolve })
    n.resolve = async () => {
      await n.holdHello
      return { allow: false }
    }
    const prevPeer = n.peer
    await n.peer.call('session.send', { session_id: pid, text: `ask-full ${JSON.stringify(askSpec)}`, assistant: true })
    await until('second prompt reached fake ACP', () => received(record).filter((m) => m.method === 'session/prompt').length === 2, SLOW)
    // Disconnect the node and hold reconnect hello so rt.ready is guaranteed false when ACP asks permission.
    prevPeer.close(new Error('node restart'))
    await until('disconnected from fake node', () => !n.peers.has(prevPeer), SLOW)
    writeFileSync(triggerFile, 'go')
    await until('ACP emitted permission while node disconnected', () => received(record).some((m) => m.askedPermission), SLOW)
    await delay(100)
    assert.equal(permissionAnswers(record).length, 0, 'assistant run_command must not fail open while node is disconnected')
    releaseHello()
    await until('reconnected node received gated ask', () => n.asked.length >= 1, SLOW)
    await until('denied after reconnect', () => permissionAnswers(record).length === 1, SLOW)
    assert.deepEqual(permissionAnswers(record)[0].result, { outcome: { outcome: 'selected', optionId: 'reject' } })
    await n.peer.call('session.interrupt', { session_id: pid })
  })

  await t.test('non-assistant run_command does not attach per-call cwd onto permission cards', async (t) => {
    const { n, record, start } = await agyRuntime(t, { node: { features: ['prefer_running', 'assistant_command_gate'] } })
    n.resolve = () => ({ allow: true })
    const askSpec = {
      toolCall: { name: 'run_command', rawInput: { CommandLine: 'ls', Cwd: '/tmp' } },
      options: fullOptions,
    }
    const pid = await start(`ask-full ${JSON.stringify(askSpec)}`)
    await until('non-assistant bash answered', () => permissionAnswers(record).length === 1, SLOW)
    assert.equal(n.asked.length, 1)
    assert.equal('cwd' in n.asked[0], false)
    await n.peer.call('session.interrupt', { session_id: pid })
  })

  await t.test('detached pending permission cancels the turn cleanly without executing, retrying, or recording an error', async (t) => {
    const { n, record, start, status } = await agyRuntime(t, { node: { features: ['prefer_running', 'assistant_command_gate'] } })
    const askSpec = {
      toolCall: { name: 'run_command', rawInput: { CommandLine: `ol POST /conversations/c1/messages '{"text":"wait"}'` } },
      options: fullOptions,
    }
    n.resolve = (ix) => ({ dismiss: true, pending: true, approval_id: ix.id, message: 'Still waiting for owner.' })
    const pid = await start(`ask-full ${JSON.stringify(askSpec)}`, { assistant: true, permission_mode: 'bypassPermissions' })
    await until('pending permission answered cancelled', () => permissionAnswers(record).length === 1, SLOW)
    assert.deepEqual(permissionAnswers(record)[0].result, cancelled)
    await until('turn ended cleanly via session/cancel', () => received(record).some((m) => m.ended === SESSION), SLOW)
    await until('session no longer working', () => status(pid) !== 'working', SLOW)
    const row = n.last('runtime.sessions')?.sessions.find((s) => s.session_id === pid)
    assert.equal(row?.turn_error, undefined)
    assert.equal(received(record).filter((m) => m.method === 'session/prompt').length, 1)
    const items = await n.peer.call('session.items', { session_id: pid })
    assert.equal(items.items.some((it) => it.kind === 'notice' && it.text === '已中断'), false)
  })

  await t.test('detached pending permission lets a queued clarification question run immediately after cancel', async (t) => {
    const { n, record, start } = await agyRuntime(t, { node: { features: ['prefer_running', 'assistant_command_gate'] } })
    const askSpec = {
      toolCall: { name: 'run_command', rawInput: { CommandLine: `ol POST /conversations/c1/messages '{"text":"clarify"}'` } },
      options: fullOptions,
    }
    const pendingGate = Promise.withResolvers()
    n.resolve = () => pendingGate.promise
    const pid = await start(`ask-full ${JSON.stringify(askSpec)}`, { assistant: true, permission_mode: 'bypassPermissions' })
    await until('card waiting on hub', () => n.asked.length === 1, SLOW)
    // Owner asks a clarification question while the approval detaches as pending.
    const queued = await n.peer.call('session.send', { session_id: pid, text: 'reply', assistant: true })
    assert.deepEqual(queued, { ok: true, queued: true })
    pendingGate.resolve({ dismiss: true, pending: true, approval_id: n.asked[0].id, message: 'Owner asked a question.' })
    await until('pending card answered cancelled', () => permissionAnswers(record).length === 1, SLOW)
    assert.deepEqual(permissionAnswers(record)[0].result, cancelled)
    const prompts = () => received(record).filter((m) => m.method === 'session/prompt')
    await until('queued clarification question runs next', () => prompts().length === 2, SLOW)
    assert.deepEqual(prompts()[1].params.prompt, [{ type: 'text', text: 'reply' }])
    const row = () => n.last('runtime.sessions')?.sessions.find((s) => s.session_id === pid)
    await until('clarification answer completed', () => row()?.last_text === 'Finished.', SLOW)
    assert.equal(row()?.turn_error, undefined)
  })

  await t.test('explicit deny with owner note delivers refusal follow-up after turn ends while plain and gate_rule denies do not', async (t) => {
    const { n, record, start, status } = await agyRuntime(t, { node: { features: ['prefer_running', 'assistant_command_gate'] } })
    const askSpec = {
      toolCall: { name: 'run_command', rawInput: { CommandLine: `ol POST /conversations/c1/messages '{"text":"verbose"}'` } },
      options: fullOptions,
      endOnAnswer: true,
    }
    const prompts = () => received(record).filter((m) => m.method === 'session/prompt')

    // 1. Explicit deny WITH user-written note sends refusal note as follow-up turn.
    n.resolve = () => ({ allow: false, message: '  Keep it under one line  ' })
    const pid = await start(`ask-full ${JSON.stringify(askSpec)}`, { assistant: true, permission_mode: 'bypassPermissions' })
    await until('deny-with-note answered reject_once', () => permissionAnswers(record).length === 1, SLOW)
    assert.deepEqual(permissionAnswers(record)[0].result, { outcome: { outcome: 'selected', optionId: 'reject' } })
    await until('refusal note delivered as follow-up prompt', () => prompts().length === 2, SLOW)
    assert.deepEqual(prompts()[1].params.prompt, [{ type: 'text', text: '（没有执行）Keep it under one line' }])
    // The follow-up turn is held by fake-acp-cancel (default hold mode); verify it never polluted row.queued, then interrupt it.
    const row = () => n.last('runtime.sessions')?.sessions.find((s) => s.session_id === pid)
    assert.equal(row()?.queued, undefined)
    await n.peer.call('session.interrupt', { session_id: pid })
    await until('follow-up settled after interrupt', () => status(pid) !== 'working', SLOW)

    // 2. Plain deny WITHOUT note does not send an extra turn.
    n.resolve = () => ({ allow: false })
    await n.peer.call('session.send', { session_id: pid, text: `ask-full ${JSON.stringify(askSpec)}`, assistant: true })
    await until('plain deny answered', () => permissionAnswers(record).length === 2, SLOW)
    assert.deepEqual(permissionAnswers(record)[1].result, { outcome: { outcome: 'selected', optionId: 'reject' } })
    await until('plain deny turn ended', () => received(record).filter((m) => m.ended === SESSION).length === 3, SLOW)
    await until('session idle after plain deny', () => status(pid) !== 'working', SLOW)
    assert.equal(prompts().length, 3, 'plain deny without note must not start a follow-up turn')

    // 3. Gate-rule deny (reason: gate_rule) does not send an extra turn.
    n.resolve = () => ({ allow: false, message: '这个操作按配置不允许', reason: 'gate_rule' })
    await n.peer.call('session.send', { session_id: pid, text: `ask-full ${JSON.stringify(askSpec)}`, assistant: true })
    await until('rule deny answered', () => permissionAnswers(record).length === 3, SLOW)
    assert.deepEqual(permissionAnswers(record)[2].result, { outcome: { outcome: 'selected', optionId: 'reject' } })
    await until('rule deny turn ended', () => received(record).filter((m) => m.ended === SESSION).length === 4, SLOW)
    await until('session idle after rule deny', () => status(pid) !== 'working', SLOW)
    assert.equal(prompts().length, 4, 'gate_rule deny must not start a follow-up turn')

    // 4. Interrupting a turn that received a deny note drops the pending refusal follow-up.
    const holdAskSpec = { ...askSpec, endOnAnswer: false }
    n.resolve = () => ({ allow: false, message: 'Should be dropped on cancel' })
    await n.peer.call('session.send', { session_id: pid, text: `ask-full ${JSON.stringify(holdAskSpec)}`, assistant: true })
    await until('hold deny answered', () => permissionAnswers(record).length === 4, SLOW)
    await n.peer.call('session.interrupt', { session_id: pid })
    await until('cancelled turn settled', () => status(pid) !== 'working', SLOW)
    assert.equal(prompts().length, 5, 'interrupted turn must drop its queued refusal note')
  })

  await t.test('detached pending permission withdraws sibling asks cleanly without executing either', async (t) => {
    const { n, record, start, status } = await agyRuntime(t, { node: { features: ['prefer_running', 'assistant_command_gate'] } })
    const askSpec = {
      asks: [
        { toolCallId: 'call-1', toolCall: { name: 'run_command', rawInput: { CommandLine: `ol POST /conversations/c1/messages '{"text":"first"}'` } } },
        { toolCallId: 'call-2', toolCall: { name: 'run_command', rawInput: { CommandLine: `ol POST /conversations/c1/messages '{"text":"second"}'` } } },
      ],
      options: fullOptions,
    }
    const first = Promise.withResolvers()
    const second = Promise.withResolvers()
    let seen = 0
    n.resolve = () => (++seen === 1 ? first.promise : second.promise)
    const pid = await start(`ask-full ${JSON.stringify(askSpec)}`, { assistant: true, permission_mode: 'bypassPermissions' })
    await until('both sibling cards waiting on hub', () => n.asked.length === 2, SLOW)
    first.resolve({ dismiss: true, pending: true, approval_id: n.asked[0].id, message: 'Waiting on card 1.' })
    await until('both permission asks answered cancelled', () => permissionAnswers(record).length === 2, SLOW)
    assert.deepEqual(permissionAnswers(record).map((a) => a.result), [cancelled, cancelled])
    await until('sibling card withdrawn via interaction.cancel', () => n.all('interaction.cancel').some((ev) => ev.id === n.asked[1].id), SLOW)
    await until('turn ended cleanly', () => received(record).some((m) => m.ended === SESSION), SLOW)
    await until('session settled', () => status(pid) !== 'working', SLOW)
    assert.equal(n.last('runtime.sessions')?.sessions.find((s) => s.session_id === pid)?.turn_error, undefined)
  })

  await t.test('racing non-Bash ask in bypassPermissions is cancelled instead of auto-executing on pendingDetach', async (t) => {
    const { n, record, start, status } = await agyRuntime(t, { node: { features: ['prefer_running', 'assistant_command_gate'] } })
    const askSpec = {
      toolCall: { name: 'run_command', rawInput: { CommandLine: `ol POST /conversations/c1/messages '{"text":"wait"}'` } },
      options: fullOptions,
      askOnCancel: {
        toolCallId: 'call-race-write',
        toolCall: { name: 'write_file', rawInput: { Path: '/tmp/race.txt', Content: 'must not run' } },
        options: fullOptions,
      },
    }
    n.resolve = (ix) => ({ dismiss: true, pending: true, approval_id: ix.id, message: 'Still waiting.' })
    const pid = await start(`ask-full ${JSON.stringify(askSpec)}`, { assistant: true, permission_mode: 'bypassPermissions' })
    await until('both pending ask and racing write_file answered', () => permissionAnswers(record).length === 2, SLOW)
    assert.deepEqual(permissionAnswers(record).map((a) => a.result), [cancelled, cancelled])
    assert.equal(n.asked.length, 1, 'racing write_file must neither auto-execute nor raise a card')
    await until('turn ended cleanly', () => received(record).some((m) => m.ended === SESSION), SLOW)
    await until('session settled', () => status(pid) !== 'working', SLOW)
  })

  await t.test('earlier deny-with-note followed by detached pending drops stale refusal ahead of queued clarification', async (t) => {
    const { n, record, start } = await agyRuntime(t, { node: { features: ['prefer_running', 'assistant_command_gate'] } })
    const askSpec = {
      toolCall: { name: 'run_command', rawInput: { CommandLine: `ol POST /conversations/c1/messages '{"text":"first"}'` } },
      options: fullOptions,
      nextAsk: {
        toolCallId: 'call-2',
        toolCall: { name: 'run_command', rawInput: { CommandLine: `ol POST /conversations/c1/messages '{"text":"second"}'` } },
        options: fullOptions,
      },
    }
    const secondGate = Promise.withResolvers()
    let calls = 0
    n.resolve = () => {
      calls += 1
      if (calls === 1) return { allow: false, message: 'stale earlier note' }
      return secondGate.promise
    }
    const pid = await start(`ask-full ${JSON.stringify(askSpec)}`, { assistant: true, permission_mode: 'bypassPermissions' })
    await until('second card waiting on hub after first deny-with-note', () => n.asked.length === 2, SLOW)
    assert.deepEqual(permissionAnswers(record)[0].result, { outcome: { outcome: 'selected', optionId: 'reject' } })
    const queued = await n.peer.call('session.send', { session_id: pid, text: 'reply', assistant: true })
    assert.deepEqual(queued, { ok: true, queued: true })
    secondGate.resolve({ dismiss: true, pending: true, approval_id: n.asked[1].id, message: 'Clarifying card 2.' })
    await until('second card answered cancelled', () => permissionAnswers(record).length === 2, SLOW)
    assert.deepEqual(permissionAnswers(record)[1].result, cancelled)
    const prompts = () => received(record).filter((m) => m.method === 'session/prompt')
    await until('clarification prompt runs immediately next without stale refusal', () => prompts().length === 2, SLOW)
    assert.deepEqual(prompts()[1].params.prompt, [{ type: 'text', text: 'reply' }])
    const row = () => n.last('runtime.sessions')?.sessions.find((s) => s.session_id === pid)
    await until('clarification finished cleanly', () => row()?.last_text === 'Finished.', SLOW)
    assert.equal(prompts().length, 2, 'stale refusal note must never fire after pendingDetach')
  })
})

test('interrupt refuses with turn_over_background when only background work remains, and still cancels an active turn', async (t) => {
  let agyDir = ''
  const { n, record, start, refresh } = await agyRuntime(t, { beforeStart: (dir) => { agyDir = dir } })
  const pid = await start('reply')
  await n.peer.call('session.watch', { session_id: pid })
  await until('initial reply finished', () => n.last('runtime.sessions')?.sessions.find((s) => s.session_id === pid)?.last_text === 'Finished.', SLOW)

  const logsDir = join(agyDir, 'brain', SESSION, '.system_generated', 'logs')
  mkdirSync(logsDir, { recursive: true })
  const startedAt = new Date().toISOString()
  writeFileSync(join(logsDir, 'transcript.jsonl'), [
    JSON.stringify({ step_index: 0, type: 'USER_INPUT', created_at: startedAt, content: 'reply' }),
    JSON.stringify({ step_index: 1, type: 'PLANNER_RESPONSE', created_at: startedAt, tool_calls: [{ name: 'run_command', args: { CommandLine: '"sleep 60"' } }] }),
    JSON.stringify({ step_index: 2, type: 'GENERIC', status: 'RUNNING', created_at: startedAt,
      content: `Created At: ${startedAt}\nTool is running as a background task with task id: ${SESSION}/task-1\nTask Description: Watch build` }),
  ].join('\n') + '\n')
  await refresh(pid)

  await until('background task appears on runtime session row', () =>
    n.last('runtime.sessions')?.sessions.find((s) => s.session_id === pid)?.background?.length === 1, SLOW)

  await assert.rejects(
    n.peer.call('session.interrupt', { session_id: pid }),
    (err) => err?.code === 'turn_over_background' && err?.data?.delivered === 'none',
  )

  // Starting a real foreground turn again allows interrupt to cancel normally.
  await n.peer.call('session.send', { session_id: pid, text: 'hold' })
  await until('foreground prompt is active', () => received(record).filter((m) => m.method === 'session/prompt').length === 2, SLOW)
  const res = await n.peer.call('session.interrupt', { session_id: pid })
  assert.equal(res?.settled, true)

  // A native foreground turn (outside Persona's activeTurns/queuedTurns/autonomousTurns) with
  // background work still running is cancelled rather than refused as turn_over_background.
  nativeSummary(agyDir, 'CASCADE_RUN_STATUS_RUNNING', 2)
  assert.equal(await refresh(pid), 'working')
  await n.peer.call('session.interrupt', { session_id: pid })
  assert.ok(received(record).filter((m) => m.method === 'session/cancel').length >= 2)
})
