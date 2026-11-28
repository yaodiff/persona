import assert from 'node:assert/strict'
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'
import { test } from 'node:test'
import { setTimeout as delay } from 'node:timers/promises'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { createAgyRuntime } from '../lib/runtime.js'

const source = '11111111-2222-4333-8444-555555555555'
const branch = 'aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee'
const sessionId = `agy-${source}`
const branched = `agy-${branch}`
const fake = fileURLToPath(new URL('./fake-acp-rewind.mjs', import.meta.url))
const records = [
  { type: 'USER_INPUT', content: 'First question' },
  { type: 'PLANNER_RESPONSE', thinking: 'Reasoning', content: 'First draft', tool_calls: [{ name: 'view_file' }] },
  { type: 'GENERIC', content: 'Tool result' },
  { type: 'PLANNER_RESPONSE', content: 'First answer' },
  { type: 'USER_INPUT', content: 'Second question' },
  { type: 'PLANNER_RESPONSE', content: 'Second answer' },
].map((step, step_index) => ({ ...step, step_index, status: 'DONE', created_at: '2026-01-01T00:00:00Z' }))

async function fixture(t, { assistant = false, status = 'IDLE', pending = false, requireLoad = false, holdFork = false, preview, delayForkExport, models, resumeDefaults, askPermissionOnPrompt, sourceRecords, rejectModels, rejectConfigOptions, skipBrain = false } = {}) {
  const home = mkdtempSync(join(tmpdir(), 'agy-fork-'))
  const cwd = join(home, assistant ? '.persona/health' : 'Example workspace')
  const config = join(home, 'config')
  const logs = join(config, 'brain', source, '.system_generated/logs')
  const initialRecords = sourceRecords || records
  if (skipBrain) mkdirSync(config, { recursive: true })
  else mkdirSync(logs, { recursive: true })
  mkdirSync(cwd, { recursive: true })
  const jsonl = (steps) => steps.map((step) => JSON.stringify(step)).join('\n') + '\n'
  const transcript = jsonl(initialRecords)
  const transcriptPath = join(logs, 'transcript_full.jsonl')
  if (!skipBrain) writeFileSync(transcriptPath, transcript)
  writeFileSync(join(home, 'calls.jsonl'), '')
  let trajState = {
    forkId: branch, delayForkExport: delayForkExport ?? (assistant || pending), requireLoad, holdFork, status: `CASCADE_RUN_STATUS_${status}`,
    promptReply: pending ? 'Latest answer' : undefined,
    ...(models ? { models } : {}),
    ...(resumeDefaults ? { resumeDefaults } : {}),
    ...(askPermissionOnPrompt ? { askPermissionOnPrompt } : {}),
    ...(rejectModels ? { rejectModels } : {}),
    ...(rejectConfigOptions ? { rejectConfigOptions } : {}),
  }
  const snapshot = (steps, patch = {}) => {
    trajState = { ...trajState, ...patch }
    writeFileSync(join(home, 'trajectory.json'), JSON.stringify({
      ...trajState,
      trajectory: { cascadeId: source, steps: steps.map((step) => ({ type: `CORTEX_STEP_TYPE_${step.type}` })) },
    }))
  }
  snapshot(initialRecords)
  const db = new DatabaseSync(join(config, 'conversation_summaries.db'))
  db.exec('CREATE TABLE conversation_summaries (conversation_id TEXT, status TEXT, not_fully_idle INTEGER, killed INTEGER, step_count INTEGER, title TEXT, workspace_uris TEXT, last_modified_time TEXT)')
  if (!skipBrain) {
    db.prepare('INSERT INTO conversation_summaries VALUES (?, ?, 0, 0, ?, ?, ?, ?)').run(
      source, 'CASCADE_RUN_STATUS_IDLE', initialRecords.length, preview ? '' : 'Example chat', JSON.stringify([pathToFileURL(cwd).href]), new Date().toISOString())
  }
  if (preview) {
    mkdirSync(join(config, 'cache'))
    writeFileSync(join(config, 'cache', 'conversation_metadata.json'), JSON.stringify({ conversations: { [source]: { summary: { Title: '', Preview: preview } } } }))
  }
  const binary = join(home, 'agy')
  writeFileSync(binary, `#!/bin/sh\nexec '${process.execPath}' '${fake}' '${home}' "$@"\n`)
  chmodSync(binary, 0o700)
  const node = await startFakeNode(t, { features: ['assistant_pids'] })
  const runtimeOptions = { home, agyDir: config, agyBin: binary, personadBin: binary,
    socketPath: node.path, exportPollMs: 20, exportSettleTimeoutMs: 500, log: () => {} }
  let runtime = createAgyRuntime(runtimeOptions)
  t.after(async () => { await runtime.stop(); db.close(); rmSync(home, { recursive: true, force: true }) })
  runtime.start()
  await node.ready
  if (!skipBrain) {
    await until('source listed', () => node.last('runtime.sessions')?.sessions.some((row) => row.session_id === sessionId))
  }
  const fork = (at_item_id, session_id = sessionId, extra = {}) => node.peer.call('session.fork', { session_id, at_item_id, assistant, ...extra })
  const items = async (id = sessionId) => (await node.peer.call('session.items', { session_id: id })).items
  const calls = () => {
    const contents = readFileSync(join(home, 'calls.jsonl'), 'utf8')
    // A concurrent fake worker may still be appending the last JSONL record.
    // Completed malformed records must still fail; only an unfinished tail waits.
    return contents.slice(0, contents.lastIndexOf('\n') + 1).split('\n').filter(Boolean).map(JSON.parse)
  }
  const rowOf = (id = branched) => node.last('runtime.sessions')?.sessions.find((row) => row.session_id === id)
  const restart = async () => {
    await runtime.stop()
    const before = node.all('runtime.sessions').length
    runtime = createAgyRuntime(runtimeOptions)
    runtime.start()
    await until('restarted inventory', () => node.all('runtime.sessions').length > before)
  }
  const latest = [...initialRecords,
    { type: 'USER_INPUT', content: 'Latest question' }, { type: 'PLANNER_RESPONSE', thinking: 'Native reasoning' },
    { type: 'PLANNER_RESPONSE', content: 'Latest answer' },
  ].map((step, step_index) => ({ status: 'DONE', created_at: '2026-01-01T00:00:00Z', ...step, step_index }))
  return { node, home, cwd, config, runtime, fork, items, calls, rowOf, restart, snapshot: (patch) => snapshot(initialRecords, patch),
    checkSource: () => assert.equal(readFileSync(transcriptPath, 'utf8'), transcript),
    finishTurn: async () => {
      await node.peer.call('session.send', { session_id: sessionId, text: 'Latest question' })
      await until('prompt finished', () => runtime.idle())
      snapshot(latest)
      return (await items()).find((item) => item.text === 'Latest answer')
    },
    exportLatest: () => writeFileSync(transcriptPath, jsonl(latest)),
    releaseFork: () => writeFileSync(join(home, 'release-fork'), ''),
  }
}

test('native fork keeps the entire conversation in the same workspace', async (t) => {
  const { node, cwd, fork, items, calls, checkSource } = await fixture(t, { requireLoad: true })
  assert.equal(node.hello.caps.fork, true)
  assert.equal((await fork()).session_id, branched)
  assert.deepEqual(calls().find((call) => call.method === 'ForkConversation')?.params, {
    sourceCascadeId: source, forkAtStepIndex: -1,
    targetForkWorkspace: 'FORK_TARGET_CURRENT_WORKSPACE', sourceWorkspaceUris: [pathToFileURL(cwd).href],
  })
  const visible = (list) => list.filter((item) => item.kind !== 'turn_duration')
  const sourceItems = await items()
  assert.deepEqual(visible(await items(branched)), visible(sourceItems))
  assert.equal(sourceItems.at(-1).kind, 'turn_duration')
  checkSource()
})

test('a fork of an untitled conversation is titled from its prompt, marked as such', async (t) => {
  const { fork } = await fixture(t, { preview: 'First question' })
  const { session } = await fork()
  assert.equal(session.title, 'First question')
  assert.equal(session.title_source, 'prompt')
})

test('forking at a reply keeps its tool results and stops before the next user message', async (t) => {
  const { fork, items, calls, checkSource } = await fixture(t)
  const reply = (await items()).find((item) => item.text === 'First draft')
  assert.match(reply.id, /^2@[\da-f]+#1$/)
  assert.equal((await fork(reply.id)).session_id, branched)
  assert.equal(calls().find((call) => call.method === 'ForkConversation')?.params.forkAtStepIndex, 3)
  assert.deepEqual((await items(branched)).filter((item) => item.kind === 'text').map((item) => item.text),
    ['First question', 'First draft', 'First answer'])
  checkSource()
})

test('an assistant fork can be opened and continued before the native export arrives', async (t) => {
  const { node, cwd, fork, items, calls } = await fixture(t, { assistant: true, requireLoad: true })
  const result = await fork('4#0')
  assert.equal(result.session_id, branched)
  assert.equal(result.session.cwd, cwd)
  assert.equal(calls().find((call) => call.method === 'ForkConversation')?.params.forkAtStepIndex, 3)
  const listed = await until('assistant fork listed', () => node.last('runtime.sessions')?.sessions.find((row) => row.session_id === branched))
  assert.equal(listed.cwd, cwd)
  assert.equal(listed.status, 'idle')
  assert.equal((await items(branched)).at(-1).text, 'First answer')
  await node.peer.call('session.send', { session_id: branched, text: 'Continue the branch' })
  const sent = await until('branch continued', () => calls().find((call) => call.method === 'session/prompt'))
  assert.equal(sent.params.sessionId, branch)
  const forkWorker = calls().find((call) => call.method === 'ForkConversation').worker
  assert.notEqual(sent.worker, forkWorker)
  assert.deepEqual(node.last('runtime.assistant_pids').pids, { [sessionId]: forkWorker, [branched]: sent.worker })
  for (const id of [source, branch]) {
    const resume = calls().find((call) => call.method === 'session/resume' && call.params.sessionId === id)
    assert.equal(resume?.params.cwd, cwd)
    assert.equal(resume.worker, id === source ? forkWorker : sent.worker)
    assert.ok(resume.params.mcpServers[0].env.some(({ name, value }) => name === 'PERSONA_ASSISTANT' && value === '1'))
  }
  await node.peer.call('session.modes', { session_id: sessionId, permission_mode: 'default' })
  assert.equal(calls().filter((call) => call.method === 'session/resume' && call.params.sessionId === source).length, 1)
})

for (const whole of [false, true]) {
  test(`fork ${whole ? 'at the end' : 'at the latest reply'} waits for the completed turn export`, async (t) => {
    const { fork, items, calls, checkSource, finishTurn, exportLatest } = await fixture(t, { pending: true })
    const reply = await finishTurn()
    assert.equal(reply.seq, 8)
    checkSource()
    const result = fork(whole ? '' : reply.id)
    const exported = delay(80).then(() => exportLatest())
    const [forked] = await Promise.all([result, exported])
    assert.equal(forked.session_id, branched)
    assert.equal(calls().find((call) => call.method === 'ForkConversation')?.params.forkAtStepIndex, whole ? -1 : 8)
    assert.notEqual((await items()).at(-1).id, reply.id)
    assert.deepEqual((await items(branched)).slice(-3).map((item) => item.text),
      ['Latest question', 'Native reasoning', 'Latest answer'])
  })
}

test('fork refuses when the completed turn export never arrives', async (t) => {
  const { fork, calls, finishTurn } = await fixture(t, { pending: true })
  await finishTurn()
  await assert.rejects(fork(), (error) => error.code === 'fork_turn_running')
  assert.ok(!calls().some((call) => call.method === 'ForkConversation'))
})

test('a mode change cannot race a native fork', async (t) => {
  const { node, fork, calls, releaseFork } = await fixture(t, { holdFork: true })
  const result = fork()
  try {
    await until('native fork started', () => calls().some((call) => call.method === 'ForkConversation'))
    await assert.rejects(node.peer.call('session.modes', { session_id: sessionId, permission_mode: 'plan' }), /busy/)
  } finally {
    releaseFork()
    await result
  }
})

test('a native running turn refuses to fork', async (t) => {
  const { fork, calls } = await fixture(t, { status: 'RUNNING' })
  await assert.rejects(fork(), (error) => error.code === 'fork_turn_running')
  assert.ok(!calls().some((call) => call.method === 'ForkConversation'))
})

test('unknown, archived and stale targets do not fork', async (t) => {
  const { node, fork, calls } = await fixture(t)
  await assert.rejects(fork('', 'agy-11111111-eeee-4333-8444-555555555555'), (error) => error.code === 'not_found')
  await assert.rejects(fork('4@old#0'), (error) => error.code === 'fork_mismatch')
  await assert.rejects(fork('4#9'), (error) => error.code === 'fork_mismatch')
  await node.peer.call('session.close', { session_id: sessionId })
  await assert.rejects(fork(), (error) => error.code === 'not_found')
  assert.ok(!calls().some((call) => call.method === 'ForkConversation'))
})

test('fork preserves bypassPermissions, custom model, and effort, persists mode on disk, and applies them on the first turn', async (t) => {
  const catalogModels = [
    { modelId: 'gemini-future-medium', name: 'Gemini Future (medium)' },
    { modelId: 'gemini-future-xhigh', name: 'Gemini Future (xhigh)' },
    { modelId: 'gemini-future-low', name: 'Gemini Future (low)' },
  ]
  const { node, config, fork, calls, rowOf } = await fixture(t, {
    models: catalogModels,
    resumeDefaults: {
      model: 'gemini-future-medium',
      mode: 'default',
      configOptions: [
        { id: 'model', category: 'model', currentValue: 'gemini-future' },
        { id: 'thought_level', currentValue: 'medium' },
        { id: 'mode', currentValue: 'default' },
      ],
    },
    askPermissionOnPrompt: true,
  })
  await node.peer.call('launchers.models', {})
  writeFileSync(join(config, 'settings.json'), JSON.stringify({ model: 'gemini-future-medium', agentMode: 'default' }))
  await node.peer.call('session.modes', {
    session_id: sessionId,
    permission_mode: 'bypassPermissions',
    model: 'gemini-future-xhigh',
  })
  await until('source reflects bypass and custom model', () =>
    rowOf(sessionId)?.permission_mode === 'bypassPermissions' &&
    rowOf(sessionId)?.model === 'gemini-future-xhigh' &&
    rowOf(sessionId)?.effort === 'xhigh')

  const result = await fork()
  assert.equal(result.session_id, branched)
  assert.equal(result.session.permission_mode, 'bypassPermissions')
  assert.equal(result.session.model, 'gemini-future-xhigh')
  assert.equal(result.session.effort, 'xhigh')
  assert.equal(result.session.capabilities?.rewind, undefined)
  assert.equal(readFileSync(join(config, 'brain', branch, '.persona-permission-mode'), 'utf8').trim(), 'bypassPermissions')
  await until('forked row advertises preserved mode, model, and effort', () =>
    rowOf(branched)?.permission_mode === 'bypassPermissions' &&
    rowOf(branched)?.model === 'gemini-future-xhigh' &&
    rowOf(branched)?.effort === 'xhigh')
  assert.equal(rowOf(branched)?.capabilities?.rewind, undefined)

  // First turn on the fork: fake ACP resets resume to default/medium and asks for a permission.
  await node.peer.call('session.send', { session_id: branched, text: 'Continue in bypass' })
  await until('fork first turn permission auto-approved', () =>
    calls().some((call) => typeof call.id === 'string' && call.id.startsWith('fork-perm-') && call.result))
  const branchCalls = calls().filter((call) => call.params?.sessionId === branch)
  assert.equal(branchCalls.find((call) => call.method === 'session/set_model')?.params?.modelId, 'gemini-future-xhigh')
  assert.equal(branchCalls.find((call) => call.method === 'session/set_mode')?.params?.modeId, 'accept-edits')
  assert.equal(node.asked.length, 0, 'forked bypassPermissions session must not raise a phone card')
  await until('fork row retains preserved state after first turn', () =>
    rowOf(branched)?.status !== 'working' &&
    rowOf(branched)?.permission_mode === 'bypassPermissions' &&
    rowOf(branched)?.model === 'gemini-future-xhigh' &&
    rowOf(branched)?.effort === 'xhigh')
})

test('fork preserves non-variant model with explicit effort and applies thought_level on the first turn', async (t) => {
  const catalogModels = [
    { modelId: 'default-model', name: 'Default Model' },
    { modelId: 'claude-opus-5-5', name: 'Claude Opus' },
  ]
  const { node, config, fork, calls, rowOf } = await fixture(t, {
    models: catalogModels,
    resumeDefaults: {
      model: 'default-model',
      mode: 'default',
      configOptions: [
        { id: 'model', category: 'model', currentValue: 'default-model' },
        { id: 'thought_level', currentValue: 'medium' },
        { id: 'mode', currentValue: 'default' },
      ],
    },
  })
  await node.peer.call('launchers.models', {})
  writeFileSync(join(config, 'settings.json'), JSON.stringify({ model: 'default-model', agentMode: 'default' }))
  await node.peer.call('session.modes', {
    session_id: sessionId,
    permission_mode: 'plan',
    model: 'claude-opus-5-5',
    effort: 'high',
  })

  const result = await fork()
  assert.equal(result.session.permission_mode, 'plan')
  assert.equal(result.session.model, 'claude-opus-5-5')
  assert.equal(result.session.effort, 'high')
  assert.equal(readFileSync(join(config, 'brain', branch, '.persona-permission-mode'), 'utf8').trim(), 'plan')

  await node.peer.call('session.send', { session_id: branched, text: 'Continue with effort' })
  await until('first turn reaches prompt', () =>
    calls().some((call) => call.method === 'session/prompt' && call.params?.sessionId === branch))
  const branchCalls = calls().filter((call) => call.params?.sessionId === branch)
  assert.equal(branchCalls.find((call) => call.method === 'session/set_model')?.params?.modelId, 'claude-opus-5-5')
  assert.equal(branchCalls.find((call) => call.method === 'session/set_mode')?.params?.modeId, 'plan')
  assert.deepEqual(branchCalls.find((call) => call.method === 'session/set_config_option')?.params, {
    sessionId: branch,
    configId: 'thought_level',
    value: 'high',
  })
  await until('row keeps plan, model, and effort', () =>
    rowOf(branched)?.permission_mode === 'plan' &&
    rowOf(branched)?.model === 'claude-opus-5-5' &&
    rowOf(branched)?.effort === 'high')
})

test('fork persists .persona-permission-mode even when native export is delayed and survives companion restart', async (t) => {
  const { node, config, fork, calls, restart } = await fixture(t, {
    delayForkExport: true,
    resumeDefaults: { mode: 'default' },
    askPermissionOnPrompt: true,
  })
  await node.peer.call('session.modes', { session_id: sessionId, permission_mode: 'bypassPermissions' })
  await fork()
  assert.equal(readFileSync(join(config, 'brain', branch, '.persona-permission-mode'), 'utf8').trim(), 'bypassPermissions')

  await restart()
  await node.peer.call('session.send', { session_id: branched, text: 'Continue after restart' })
  await until('restarted fork auto-approves permission', () =>
    calls().some((call) => typeof call.id === 'string' && call.id.startsWith('fork-perm-') && call.result))
  assert.equal(node.asked.length, 0)
  assert.equal(calls().filter((call) => call.method === 'session/set_mode' && call.params?.sessionId === branch).at(-1)?.params?.modeId, 'accept-edits')
})

test('explicit mode/model overrides on fork or first send are not overwritten by source session state', async (t) => {
  const catalogModels = [
    { modelId: 'gemini-future-medium', name: 'Gemini Future (medium)' },
    { modelId: 'gemini-future-xhigh', name: 'Gemini Future (xhigh)' },
    { modelId: 'gemini-future-low', name: 'Gemini Future (low)' },
  ]
  await t.test('explicit fork params override source mode, model, and effort', async (t) => {
    const { node, config, fork, calls, rowOf } = await fixture(t, {
      models: catalogModels,
      resumeDefaults: { model: 'gemini-future-medium', mode: 'default' },
    })
    await node.peer.call('launchers.models', {})
    await node.peer.call('session.modes', {
      session_id: sessionId,
      permission_mode: 'bypassPermissions',
      model: 'gemini-future-xhigh',
    })
    const result = await fork('', sessionId, { permission_mode: 'plan', model: 'gemini-future-low' })
    assert.equal(result.session.permission_mode, 'plan')
    assert.equal(result.session.model, 'gemini-future-low')
    assert.equal(result.session.effort, 'low')
    assert.equal(readFileSync(join(config, 'brain', branch, '.persona-permission-mode'), 'utf8').trim(), 'plan')

    await node.peer.call('session.send', { session_id: branched, text: 'Run with override' })
    await until('prompt reached', () => calls().some((call) => call.method === 'session/prompt' && call.params?.sessionId === branch))
    const branchCalls = calls().filter((call) => call.params?.sessionId === branch)
    assert.equal(branchCalls.find((call) => call.method === 'session/set_model')?.params?.modelId, 'gemini-future-low')
    assert.equal(branchCalls.find((call) => call.method === 'session/set_mode')?.params?.modeId, 'plan')
    assert.equal(branchCalls.some((call) => call.method === 'session/set_config_option'), false)
    await until('row keeps override', () => rowOf(branched)?.permission_mode === 'plan' && rowOf(branched)?.model === 'gemini-future-low' && rowOf(branched)?.effort === 'low')
  })

  await t.test('explicit send params on first turn override inherited fork mode and model', async (t) => {
    const { node, fork, calls, rowOf } = await fixture(t, {
      models: catalogModels,
      resumeDefaults: { model: 'gemini-future-medium', mode: 'default' },
    })
    await node.peer.call('launchers.models', {})
    await node.peer.call('session.modes', {
      session_id: sessionId,
      permission_mode: 'bypassPermissions',
      model: 'gemini-future-xhigh',
    })
    await fork()

    await node.peer.call('session.send', {
      session_id: branched,
      text: 'Override on first send',
      mode: 'plan',
      model: 'gemini-future-low',
    })
    await until('prompt reached', () => calls().some((call) => call.method === 'session/prompt' && call.params?.sessionId === branch))
    const branchCalls = calls().filter((call) => call.params?.sessionId === branch)
    assert.equal(branchCalls.find((call) => call.method === 'session/set_model')?.params?.modelId, 'gemini-future-low')
    assert.equal(branchCalls.find((call) => call.method === 'session/set_mode')?.params?.modeId, 'plan')
    assert.equal(branchCalls.some((call) => call.method === 'session/set_config_option'), false)
    await until('row keeps send override', () => rowOf(branched)?.permission_mode === 'plan' && rowOf(branched)?.model === 'gemini-future-low' && rowOf(branched)?.effort === 'low')
  })

  await t.test('pre-turn permission-only session.modes preserves inherited fork model without re-setting on turn 1', async (t) => {
    const { node, fork, calls, rowOf } = await fixture(t, {
      models: catalogModels,
      resumeDefaults: { model: 'gemini-future-medium', mode: 'default' },
    })
    await node.peer.call('launchers.models', {})
    await node.peer.call('session.modes', {
      session_id: sessionId,
      permission_mode: 'bypassPermissions',
      model: 'gemini-future-xhigh',
    })
    await fork()

    await node.peer.call('session.modes', { session_id: branched, permission_mode: 'acceptEdits' })
    await until('row keeps inherited model with new mode', () =>
      rowOf(branched)?.permission_mode === 'acceptEdits' && rowOf(branched)?.model === 'gemini-future-xhigh' && rowOf(branched)?.effort === 'xhigh')

    await node.peer.call('session.send', { session_id: branched, text: 'Continue after modes' })
    await until('prompt reached', () => calls().some((call) => call.method === 'session/prompt' && call.params?.sessionId === branch))
    const branchCalls = calls().filter((call) => call.params?.sessionId === branch)
    assert.deepEqual(
      branchCalls.filter((call) => call.method === 'session/set_model').map((call) => call.params.modelId),
      ['gemini-future-xhigh'],
    )
    assert.equal(branchCalls.find((call) => call.method === 'session/set_mode')?.params?.modeId, 'accept-edits')
  })
})

test('assistant fork advertises capabilities.rewind = false on return and listed rows', async (t) => {
  const { fork, rowOf } = await fixture(t, { assistant: true, requireLoad: true })
  const result = await fork('4#0')
  assert.equal(result.session.capabilities?.rewind, false)
  await until('assistant fork listed with rewind false', () => rowOf(branched)?.capabilities?.rewind === false)
})

test('cold-session fork ignores loadSession resumeDefaults and preserves transcript model or falls back to configuredModel (#15537)', async (t) => {
  const catalogModels = [
    { modelId: 'gemini-future-medium', name: 'Gemini Future (medium)' },
    { modelId: 'gemini-future-xhigh', name: 'Gemini Future (xhigh)' },
    { modelId: 'gemini-future-low', name: 'Gemini Future (low)' },
  ]
  const resumeDefaults = {
    model: 'gemini-future-medium',
    mode: 'default',
    configOptions: [
      { id: 'model', category: 'model', currentValue: 'gemini-future' },
      { id: 'thought_level', currentValue: 'medium' },
      { id: 'mode', currentValue: 'default' },
    ],
  }

  await t.test('transcript model beats resumeDefaults without downgrading thought_level', async (t) => {
    const withModel = records.map((step) => step.type === 'PLANNER_RESPONSE' ? { ...step, model: 'Gemini Future (xhigh)' } : step)
    const { node, config, fork, calls, rowOf } = await fixture(t, {
      models: catalogModels,
      resumeDefaults,
      sourceRecords: withModel,
    })
    await node.peer.call('launchers.models', {})
    writeFileSync(join(config, 'settings.json'), JSON.stringify({ model: 'gemini-future-low', agentMode: 'default' }))

    // Cold session: never called session.modes/start on source in this process.
    const result = await fork()
    assert.equal(result.session.model, 'gemini-future-xhigh')
    assert.equal(result.session.effort, 'xhigh')

    await node.peer.call('session.send', { session_id: branched, text: 'Continue cold fork' })
    await until('cold fork prompt sent', () => calls().some((call) => call.method === 'session/prompt' && call.params?.sessionId === branch))
    const branchCalls = calls().filter((call) => call.params?.sessionId === branch)
    assert.deepEqual(
      branchCalls.filter((call) => call.method === 'session/set_model').map((call) => call.params.modelId),
      ['gemini-future-xhigh'],
    )
    assert.equal(branchCalls.some((call) => call.method === 'session/set_config_option'), false, 'must not downgrade thought_level from resumeDefaults')
    await until('row keeps transcript model and xhigh effort', () =>
      rowOf(branched)?.model === 'gemini-future-xhigh' && rowOf(branched)?.effort === 'xhigh')
  })

  await t.test('cold fork without transcript model lets turn 1 apply configuredModel() instead of locking onto resumeDefaults', async (t) => {
    const { node, config, fork, calls, rowOf } = await fixture(t, {
      models: catalogModels,
      resumeDefaults,
    })
    await node.peer.call('launchers.models', {})
    writeFileSync(join(config, 'settings.json'), JSON.stringify({ model: 'gemini-future-xhigh', agentMode: 'default' }))

    const result = await fork()
    assert.equal(result.session.model, undefined)
    assert.equal(result.session.effort, undefined)

    await node.peer.call('session.send', { session_id: branched, text: 'Apply configured default' })
    await until('prompt reached', () => calls().some((call) => call.method === 'session/prompt' && call.params?.sessionId === branch))
    const branchCalls = calls().filter((call) => call.params?.sessionId === branch)
    assert.deepEqual(
      branchCalls.filter((call) => call.method === 'session/set_model').map((call) => call.params.modelId),
      ['gemini-future-xhigh'],
    )
    assert.equal(branchCalls.some((call) => call.method === 'session/set_config_option'), false)
    await until('row shows configuredModel()', () =>
      rowOf(branched)?.model === 'gemini-future-xhigh' && rowOf(branched)?.effort === 'xhigh')
  })
})

test('retired transcript models and rejected implicit forkOptions clear cleanly without bricking the fork', async (t) => {
  const catalogModels = [
    { modelId: 'gemini-future-medium', name: 'Gemini Future (medium)' },
    { modelId: 'gemini-future-xhigh', name: 'Gemini Future (xhigh)' },
    { modelId: 'gemini-future-low', name: 'Gemini Future (low)' },
    { modelId: 'claude-opus-5-5', name: 'Claude Opus' },
  ]

  await t.test('cold fork ignores retired transcript model not in catalog and falls back to configuredModel', async (t) => {
    const withRetired = records.map((step) => step.type === 'PLANNER_RESPONSE' ? { ...step, model: 'retired-legacy-model' } : step)
    const { node, config, fork, calls } = await fixture(t, {
      models: catalogModels,
      resumeDefaults: { model: 'gemini-future-medium', mode: 'default' },
      sourceRecords: withRetired,
      rejectModels: ['retired-legacy-model'],
    })
    await node.peer.call('launchers.models', {})
    writeFileSync(join(config, 'settings.json'), JSON.stringify({ model: 'gemini-future-low', agentMode: 'default' }))

    const result = await fork()
    assert.equal(result.session.model, undefined)

    await node.peer.call('session.send', { session_id: branched, text: 'Continue after retired model' })
    await until('prompt sent cleanly', () => calls().some((call) => call.method === 'session/prompt' && call.params?.sessionId === branch))
    const setModels = calls().filter((call) => call.method === 'session/set_model' && call.params?.sessionId === branch).map((call) => call.params.modelId)
    assert.deepEqual(setModels, ['gemini-future-low'])
  })

  await t.test('rejected implicit inherited model on turn 1 clears forkOptions, preserves bypassPermissions, and falls back to configuredModel', async (t) => {
    const { node, config, fork, calls, rowOf, snapshot } = await fixture(t, {
      models: catalogModels,
      resumeDefaults: { model: 'gemini-future-medium', mode: 'default' },
      askPermissionOnPrompt: true,
    })
    await node.peer.call('launchers.models', {})
    writeFileSync(join(config, 'settings.json'), JSON.stringify({ model: 'gemini-future-low', agentMode: 'default' }))
    await node.peer.call('session.modes', {
      session_id: sessionId,
      permission_mode: 'bypassPermissions',
      model: 'gemini-future-xhigh',
    })
    await fork()

    // Retire gemini-future-xhigh on the backend before turn 1 of the fork.
    snapshot({
      models: catalogModels.filter((m) => m.modelId !== 'gemini-future-xhigh'),
      rejectModels: ['gemini-future-xhigh'],
    })

    await node.peer.call('session.send', { session_id: branched, text: 'Recover from rejected fork model' })
    await until('turn 1 permission auto-approved after fallback', () =>
      calls().some((call) => typeof call.id === 'string' && call.id.startsWith('fork-perm-') && call.result))
    assert.equal(node.asked.length, 0, 'bypassPermissions must still apply on fallback')
    const setModels = calls().filter((call) => call.method === 'session/set_model' && call.params?.sessionId === branch).map((call) => call.params.modelId)
    assert.equal(setModels.at(-1), 'gemini-future-low')
    await until('row settles with fallback model and bypassPermissions', () =>
      rowOf(branched)?.status !== 'working' &&
      rowOf(branched)?.model === 'gemini-future-low' &&
      rowOf(branched)?.permission_mode === 'bypassPermissions')

    // Subsequent send succeeds with no leftover rejected model lockup.
    await node.peer.call('session.send', { session_id: branched, text: 'Second turn works' })
    await until('second turn prompt reached', () =>
      calls().filter((call) => call.method === 'session/prompt' && call.params?.sessionId === branch).length === 2)
  })

  await t.test('rejected implicit inherited effort on turn 1 clears stale effort and keeps model and mode', async (t) => {
    const { node, fork, calls, rowOf, snapshot } = await fixture(t, {
      models: catalogModels,
      resumeDefaults: { model: 'gemini-future-medium', mode: 'default' },
    })
    await node.peer.call('launchers.models', {})
    await node.peer.call('session.modes', {
      session_id: sessionId,
      permission_mode: 'plan',
      model: 'claude-opus-5-5',
      effort: 'high',
    })
    await fork()

    snapshot({ rejectConfigOptions: ['thought_level'] })

    await node.peer.call('session.send', { session_id: branched, text: 'Recover from rejected effort' })
    await until('turn 1 prompt reached despite rejected thought_level', () =>
      calls().some((call) => call.method === 'session/prompt' && call.params?.sessionId === branch))
    await until('row keeps claude-opus-5-5 and plan with stale effort cleared', () =>
      rowOf(branched)?.status !== 'working' &&
      rowOf(branched)?.model === 'claude-opus-5-5' &&
      rowOf(branched)?.permission_mode === 'plan' &&
      rowOf(branched)?.effort === undefined)
  })

  await t.test('pre-turn permission-only session.modes recovers when inherited fork model is rejected', async (t) => {
    const { node, fork, rowOf, snapshot } = await fixture(t, {
      models: catalogModels,
      resumeDefaults: { model: 'gemini-future-medium', mode: 'default' },
    })
    await node.peer.call('launchers.models', {})
    await node.peer.call('session.modes', {
      session_id: sessionId,
      permission_mode: 'bypassPermissions',
      model: 'gemini-future-xhigh',
    })
    await fork()

    snapshot({
      models: catalogModels.filter((m) => m.modelId !== 'gemini-future-xhigh'),
      rejectModels: ['gemini-future-xhigh'],
    })

    await node.peer.call('session.modes', { session_id: branched, permission_mode: 'plan' })
    await until('permission_mode updated despite retired inherited fork model', () =>
      rowOf(branched)?.permission_mode === 'plan')
  })

  await t.test('invalidSession during set_model or thought_level is NOT swallowed by recoverForkOption and retries with fork options intact', async (t) => {
    const { node, fork, calls, rowOf, snapshot } = await fixture(t, {
      models: catalogModels,
      resumeDefaults: { model: 'gemini-future-medium', mode: 'default' },
    })
    await node.peer.call('launchers.models', {})
    await node.peer.call('session.modes', {
      session_id: sessionId,
      permission_mode: 'plan',
      model: 'claude-opus-5-5',
      effort: 'high',
    })
    await fork()

    // First attempt hits invalidSession on set_model; runTurn retries from attempt 0 -> 1 and preserves both model and effort.
    snapshot({ invalidSessionOnModelOnce: true })
    await node.peer.call('session.send', { session_id: branched, text: 'Retry invalidSession on set_model' })
    await until('prompt reached after invalidSession retry', () =>
      calls().some((call) => call.method === 'session/prompt' && call.params?.sessionId === branch))
    const branchCalls = calls().filter((call) => call.params?.sessionId === branch)
    assert.deepEqual(
      branchCalls.filter((call) => call.method === 'session/set_model').map((call) => call.params.modelId),
      ['claude-opus-5-5', 'claude-opus-5-5'],
    )
    assert.deepEqual(branchCalls.find((call) => call.method === 'session/set_config_option')?.params, {
      sessionId: branch,
      configId: 'thought_level',
      value: 'high',
    })
    await until('row keeps claude-opus-5-5 and high effort after invalidSession retry', () =>
      rowOf(branched)?.model === 'claude-opus-5-5' && rowOf(branched)?.effort === 'high')
  })

  await t.test('process crash during thought_level is NOT swallowed by recoverForkOption', async (t) => {
    const { node, fork, calls, snapshot } = await fixture(t, {
      models: catalogModels,
      resumeDefaults: { model: 'gemini-future-medium', mode: 'default' },
    })
    await node.peer.call('launchers.models', {})
    await node.peer.call('session.modes', {
      session_id: sessionId,
      permission_mode: 'plan',
      model: 'claude-opus-5-5',
      effort: 'high',
    })
    await fork()

    snapshot({ exitOnConfigOnce: true })
    await assert.rejects(
      node.peer.call('session.send', { session_id: branched, text: 'Crash during thought_level' }),
      /agy acp exited/,
    )
    assert.equal(calls().some((call) => call.method === 'session/prompt' && call.params?.sessionId === branch), false)

    // Because forkOptions was NOT cleared on crash, the next send still applies inherited model and effort.
    await node.peer.call('session.send', { session_id: branched, text: 'Retry after crash' })
    await until('prompt reached after crash recovery', () =>
      calls().some((call) => call.method === 'session/prompt' && call.params?.sessionId === branch))
    const branchCalls = calls().filter((call) => call.params?.sessionId === branch)
    assert.equal(branchCalls.filter((call) => call.method === 'session/set_model').at(-1)?.params?.modelId, 'claude-opus-5-5')
    assert.equal(branchCalls.filter((call) => call.method === 'session/set_config_option').at(-1)?.params?.value, 'high')
  })

  await t.test('explicit fork({ effort }) is preserved when implicit retired source model falls back to configuredModel', async (t) => {
    const { node, config, fork, calls, rowOf, snapshot } = await fixture(t, {
      models: catalogModels,
      resumeDefaults: { model: 'gemini-future-medium', mode: 'default' },
    })
    await node.peer.call('launchers.models', {})
    writeFileSync(join(config, 'settings.json'), JSON.stringify({ model: 'claude-opus-5-5', agentMode: 'default' }))
    await node.peer.call('session.modes', {
      session_id: sessionId,
      permission_mode: 'plan',
      model: 'gemini-future-xhigh',
    })
    await fork('', sessionId, { effort: 'high' })

    // Retire inherited source model before turn 1.
    snapshot({
      models: catalogModels.filter((m) => m.modelId !== 'gemini-future-xhigh'),
      rejectModels: ['gemini-future-xhigh'],
    })

    await node.peer.call('session.send', { session_id: branched, text: 'Fallback model but keep explicit fork effort' })
    await until('prompt reached after model fallback', () =>
      calls().some((call) => call.method === 'session/prompt' && call.params?.sessionId === branch))
    const branchCalls = calls().filter((call) => call.params?.sessionId === branch)
    assert.equal(branchCalls.filter((call) => call.method === 'session/set_model').at(-1)?.params?.modelId, 'claude-opus-5-5')
    assert.deepEqual(branchCalls.find((call) => call.method === 'session/set_config_option')?.params, {
      sessionId: branch,
      configId: 'thought_level',
      value: 'high',
    })
    await until('row keeps fallback model claude-opus-5-5 and explicit effort high', () =>
      rowOf(branched)?.model === 'claude-opus-5-5' && rowOf(branched)?.effort === 'high')
  })

  await t.test('implicit effort rejection on a variant model restores its catalog default_effort instead of clearing it', async (t) => {
    const { node, fork, calls, rowOf, snapshot } = await fixture(t, {
      models: catalogModels,
      resumeDefaults: { model: 'gemini-future-medium', mode: 'default' },
    })
    await node.peer.call('launchers.models', {})
    await node.peer.call('session.modes', {
      session_id: sessionId,
      model: 'gemini-future-xhigh',
    })
    // Simulate a source session whose stored effort differs from variant default_effort (e.g. from legacy ACP).
    await node.peer.call('session.modes', {
      session_id: sessionId,
      model: 'gemini-future-xhigh',
      effort: 'max',
    })
    await fork()

    snapshot({ rejectConfigOptions: ['thought_level'] })

    await node.peer.call('session.send', { session_id: branched, text: 'Reject implicit effort on variant' })
    await until('prompt reached despite rejected thought_level', () =>
      calls().some((call) => call.method === 'session/prompt' && call.params?.sessionId === branch))
    await until('row restores gemini-future-xhigh default_effort xhigh', () =>
      rowOf(branched)?.status !== 'working' &&
      rowOf(branched)?.model === 'gemini-future-xhigh' &&
      rowOf(branched)?.effort === 'xhigh')
  })
})

test('cold-session resumeModel (#15537) does not wipe user-set effort from pre-turn session.modes', async (t) => {
  const catalogModels = [
    { modelId: 'claude-opus-5-5', name: 'Claude Opus' },
    { modelId: 'gemini-future-medium', name: 'Gemini Future (medium)' },
  ]
  const { node, config, calls, rowOf } = await fixture(t, {
    models: catalogModels,
    resumeDefaults: { model: 'claude-opus-5-5', mode: 'default' },
  })
  await node.peer.call('launchers.models', {})
  writeFileSync(join(config, 'settings.json'), JSON.stringify({ model: 'claude-opus-5-5', agentMode: 'default' }))
  await node.peer.call('session.modes', { session_id: sessionId, effort: 'high' })
  await until('effort set before first turn', () => rowOf(sessionId)?.effort === 'high')

  await node.peer.call('session.send', { session_id: sessionId, text: 'First turn applies resumeModel' })
  await until('prompt sent', () => calls().some((call) => call.method === 'session/prompt' && call.params?.sessionId === source))
  await until('row preserves high effort after resumeModel', () =>
    rowOf(sessionId)?.status !== 'working' && rowOf(sessionId)?.effort === 'high')
})

test('switching explicitly from a variant fork model to a non-variant model clears stale effort', async (t) => {
  const catalogModels = [
    { modelId: 'gemini-future-xhigh', name: 'Gemini Future (xhigh)' },
    { modelId: 'claude-opus-5-5', name: 'Claude Opus' },
  ]
  const { node, fork, calls, rowOf } = await fixture(t, {
    models: catalogModels,
    resumeDefaults: { model: 'gemini-future-xhigh', mode: 'default' },
  })
  await node.peer.call('launchers.models', {})
  await node.peer.call('session.modes', {
    session_id: sessionId,
    permission_mode: 'bypassPermissions',
    model: 'gemini-future-xhigh',
  })
  const result = await fork()
  assert.equal(result.session.effort, 'xhigh')

  await node.peer.call('session.send', {
    session_id: branched,
    text: 'Switch to non-variant model',
    model: 'claude-opus-5-5',
  })
  await until('prompt reached', () => calls().some((call) => call.method === 'session/prompt' && call.params?.sessionId === branch))
  await until('row switches to claude-opus-5-5 and clears stale xhigh effort', () =>
    rowOf(branched)?.status !== 'working' &&
    rowOf(branched)?.model === 'claude-opus-5-5' &&
    rowOf(branched)?.effort === undefined &&
    rowOf(branched)?.permission_mode === 'bypassPermissions')
})

test('fork creates brain/ safely in a fresh agyDir to persist .persona-permission-mode and never follows a symlinked brain', async (t) => {
  await t.test('creates the branch directory after persisting a prepared source in a fresh agyDir', async (t) => {
    const { node, cwd, config, fork } = await fixture(t, {
      skipBrain: true,
      delayForkExport: true,
    })
    assert.equal(existsSync(join(config, 'brain')), false)
    const started = await node.peer.call('session.start', { cwd, permission_mode: 'bypassPermissions' })
    assert.equal(started.session.session_id, sessionId)
    assert.equal(existsSync(join(config, 'brain', source, '.persona-session.json')), true, 'prepare persists its identity before acknowledgement')
    assert.equal(existsSync(join(config, 'brain', branch)), false, 'the fork has not created its own directory yet')

    const result = await fork('', sessionId)
    assert.equal(result.session_id, branched)
    assert.equal(result.session.permission_mode, 'bypassPermissions')
    assert.equal(readFileSync(join(config, 'brain', branch, '.persona-permission-mode'), 'utf8').trim(), 'bypassPermissions')
  })

  await t.test('preparation refuses a symlinked brain directory before it can be used for a fork', async (t) => {
    const { node, home, cwd, config, fork } = await fixture(t, {
      skipBrain: true,
      delayForkExport: true,
    })
    const target = join(home, 'outside-target')
    mkdirSync(target, { recursive: true })
    symlinkSync(target, join(config, 'brain'))
    await assert.rejects(node.peer.call('session.start', { cwd, permission_mode: 'bypassPermissions' }), /Invalid Persona session directory/)
    assert.equal(existsSync(join(target, source)), false, 'prepare must not follow the symlink to persist an identity')
    assert.equal(existsSync(join(target, branch)), false, 'must never create directories or files through symlinked brain')
  })
})

test('fork and rewind enforce custom channel isolation before and after runtime restart', async (t) => {
  const { node, home, cwd, config, calls, restart } = await fixture(t, { delayForkExport: true })
  const customChannel = {
    id: 'gw-fork-iso',
    auth_mode: 'gemini-api-key',
    rev: 'r1',
    env: {
      GOOGLE_GEMINI_BASE_URL: 'https://gw.example.com',
      GEMINI_API_KEY: 'secret-fork-key',
      PERSONA_AGY_MODEL_PROVIDER: 'gemini',
    },
  }

  // Persist source session on the custom channel by starting it via session.start.
  await node.peer.call('session.start', { cwd, channel: customChannel })
  const startCall = calls().find((c) => c.method === 'session/new')
  assert.ok(startCall?.worker, 'custom channel ACP should spawn a dedicated worker')
  const customWorkerPid = startCall.worker

  // Forking with the custom channel routes ForkConversation to the custom channel worker and persists channel identity.
  const forked = await node.peer.call('session.fork', { session_id: sessionId, channel: customChannel })
  assert.equal(forked.session_id, branched)
  const forkCall = calls().find((c) => c.method === 'ForkConversation')
  assert.equal(forkCall?.worker, customWorkerPid, 'ForkConversation must run on the custom channel ACP')

  const savedBranch = JSON.parse(readFileSync(join(config, 'brain', branch, '.persona-session.json'), 'utf8'))
  assert.deepEqual(savedBranch.channel, { id: 'gw-fork-iso', auth_mode: 'gemini-api-key', rev: 'r1' })

  // Next send on the forked session (even without repeating channel) stays pinned to the custom channel worker.
  await node.peer.call('session.send', { session_id: branched, text: 'follow-up on fork' })
  await until('fork prompt sent', () => calls().some((c) => c.method === 'session/prompt' && c.params?.sessionId === branch))
  const promptCall = calls().find((c) => c.method === 'session/prompt' && c.params?.sessionId === branch)
  assert.equal(promptCall?.worker, customWorkerPid, 'subsequent turn on fork must stay on the custom channel worker')

  // Populate native userInput + metadata so rewind can match prompts in GetCascadeTrajectory.
  const trajPath = join(home, 'trajectory.json')
  const traj = JSON.parse(readFileSync(trajPath, 'utf8'))
  traj.trajectory.steps = records.map((step) => ({
    type: `CORTEX_STEP_TYPE_${step.type}`,
    metadata: { source: step.type === 'USER_INPUT' ? 'CORTEX_STEP_SOURCE_USER_EXPLICIT' : 'MODEL', createdAt: step.created_at },
    ...(step.type === 'USER_INPUT' ? { userInput: { items: [{ text: step.content }] } } : {}),
    ...(step.type === 'PLANNER_RESPONSE' ? { plannerResponse: { response: step.content } } : {}),
  }))
  writeFileSync(trajPath, JSON.stringify(traj))

  // After runtime restart, both fork and rewind must reject channel_required when channel is omitted
  // and route to the custom channel worker when channel is supplied.
  await restart()

  await assert.rejects(
    node.peer.call('session.send', { session_id: branched, text: 'missing channel' }),
    (err) => err?.code === 'channel_required',
    'restarted forked custom-channel session must require channel on send',
  )
  await assert.rejects(
    node.peer.call('session.fork', { session_id: sessionId }),
    (err) => err?.code === 'channel_required',
    'restarted custom-channel session must require channel on fork',
  )
  await assert.rejects(
    node.peer.call('session.rewind', { session_id: sessionId, restore: 'conversation', prompt: 'Second question', occurrence: 1, prompts_after: 0 }),
    (err) => err?.code === 'channel_required',
    'restarted custom-channel session must require channel on rewind',
  )

  await node.peer.call('session.rewind', {
    session_id: sessionId,
    restore: 'conversation',
    prompt: 'Second question',
    occurrence: 1,
    prompts_after: 0,
    channel: customChannel,
  })
  const rewindCall = calls().findLast((c) => c.method === 'RevertToCascadeStep')
  assert.ok(rewindCall, 'rewind with channel should invoke RevertToCascadeStep')
  assert.notEqual(rewindCall.worker, customWorkerPid, 'post-restart rewind should run on the fresh custom-channel worker')
  const postRestartWorkers = new Set(calls().filter((c) => c.worker !== customWorkerPid).map((c) => c.worker))
  assert.equal(postRestartWorkers.size, 1, 'restarted rewind must use only the custom-channel worker (never defaultAcp)')
})

test('concurrent cross-channel rewind on a running turn fails rewind_busy without hijacking routing, and fork rolls back on persistence failure', async (t) => {
  await t.test('concurrent cross-channel rewind rejects rewind_busy before mutating channel binding', async (t) => {
    const { node, home, cwd, calls, snapshot, runtime } = await fixture(t, { delayForkExport: true })
    const chA = {
      id: 'gw-chan-a',
      auth_mode: 'gemini-api-key',
      rev: 'r1',
      env: { GOOGLE_GEMINI_BASE_URL: 'https://a.example.com', GEMINI_API_KEY: 'secret-a', PERSONA_AGY_MODEL_PROVIDER: 'gemini' },
    }
    const chB = {
      id: 'gw-chan-b',
      auth_mode: 'gemini-api-key',
      rev: 'r1',
      env: { GOOGLE_GEMINI_BASE_URL: 'https://b.example.com', GEMINI_API_KEY: 'secret-b', PERSONA_AGY_MODEL_PROVIDER: 'gemini' },
    }
    await node.peer.call('session.start', { cwd, channel: chA })
    const workerA = calls().find((c) => c.method === 'session/new')?.worker
    assert.ok(workerA)

    // Hold the active turn so session.rewind collides with a live turn.
    snapshot({ promptPlan: [{ holdForCancel: true, reply: 'done' }] })
    await node.peer.call('session.send', { session_id: sessionId, text: 'in-flight on A', channel: chA })
    await until('prompt reached workerA', () => calls().some((c) => c.method === 'session/prompt' && c.worker === workerA))

    // Concurrent rewind with chB MUST reject rewind_busy BEFORE calling acpFor (no chB worker spawned, no rebind).
    await assert.rejects(
      node.peer.call('session.rewind', {
        session_id: sessionId,
        restore: 'conversation',
        prompt: 'Second question',
        occurrence: 1,
        prompts_after: 0,
        channel: chB,
      }),
      (err) => err?.code === 'rewind_busy',
    )

    // Interrupt must still cancel on workerA (not hijacked to chB) and no second worker should have been spawned.
    await node.peer.call('session.interrupt', { session_id: sessionId })
    await until('runtime idle after cancel', () => runtime.idle())
    const workers = new Set(calls().map((c) => c.worker))
    assert.deepEqual([...workers], [workerA], 'rejected cross-channel rewind must not spawn chB or rebind session')
    assert.ok(calls().some((c) => c.method === 'session/cancel' && c.worker === workerA))
  })

  await t.test('fork rolls back in-memory state, pool binding, and listed row when rememberPersonaSession fails', async (t) => {
    const { node, home, cwd, config, rowOf } = await fixture(t, { delayForkExport: true })
    const customChannel = {
      id: 'gw-rollback',
      auth_mode: 'gemini-api-key',
      rev: 'r1',
      env: { GOOGLE_GEMINI_BASE_URL: 'https://rb.example.com', GEMINI_API_KEY: 'secret-rb', PERSONA_AGY_MODEL_PROVIDER: 'gemini' },
    }
    await node.peer.call('session.start', { cwd, channel: customChannel })

    // Place a symlink at brain/<branch> so personaSessionStore.write fails closed.
    const outside = join(home, 'outside-branch-target')
    mkdirSync(outside, { recursive: true })
    symlinkSync(outside, join(config, 'brain', branch))

    await assert.rejects(
      node.peer.call('session.fork', { session_id: sessionId, channel: customChannel }),
      /Invalid Persona session directory/,
    )
    assert.equal(existsSync(join(outside, '.persona-session.json')), false, 'must never write through symlinked branch dir')
    await delay(50)
    assert.equal(rowOf(branched), undefined, 'rolled-back fork must not remain listed in runtime.sessions')
  })
})
