import assert from 'node:assert/strict'
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { DatabaseSync } from 'node:sqlite'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'
import { startFakeNode, until } from '../../runtime-sdk/testing/fake-node.js'
import { nativeErrorStep, streamedErrorInfo } from '../lib/errors.js'
import { stepsToItems, transcriptState } from '../lib/map.js'
import { createAgyRuntime } from '../lib/runtime.js'

const RAW = '11111111-2222-4333-8444-555555555555'
const SESSION = `agy-${RAW}`
const FAKE = fileURLToPath(new URL('./fake-acp-rewind.mjs', import.meta.url))
const AT = '2026-01-02T12:00:00Z'
const RESET = '2026-01-03T18:00:00Z'
const MESSAGE = 'Individual quota reached. Resets in 30h.'
const TRANSIENT_401 = 'UNAUTHENTICATED (code 401): Request is missing required authentication credential. Expected OAuth 2 access token, login cookie or other valid authentication credential. See https://developers.google.com/identity/sign-in/web/devconsole-project.'
const input = { step_index: 0, type: 'USER_INPUT', source: 'USER_EXPLICIT', status: 'DONE', created_at: AT, content: 'Check the result.' }
const error = {
  type: 'CORTEX_STEP_TYPE_ERROR_MESSAGE', metadata: { createdAt: AT },
  errorMessage: { error: {
    userErrorMessage: MESSAGE, errorCode: 429,
    details: JSON.stringify({ error: { details: [{ reason: 'QUOTA_EXHAUSTED', metadata: { quotaResetTimeStamp: RESET } }] } }),
  } },
}

test('native errors preserve structured subscription limits and their actual timestamp', () => {
  const step = nativeErrorStep(error, 1)
  const parsed = stepsToItems([input, step])
  assert.equal(parsed.lastText, '')
  assert.deepEqual(parsed.turnError, {
    turn_error: MESSAGE, turn_error_kind: 'usage_limit', turn_error_resets_at: '2026-01-03T18:00:00.000Z',
    turn_error_code: 'rate_limited', turn_error_after_work: false,
  })
  assert.equal(parsed.items.at(-1).role, 'assistant')
  assert.equal(parsed.items.at(-1).kind, 'text')
  assert.equal(parsed.items.at(-1).text, `API Error: ${MESSAGE}`)
  const existing = stepsToItems([{ ...step, content: 'API Error: 503 unavailable' }])
  assert.equal(existing.items[0].text, 'API Error: 503 unavailable')
  const work = { step_index: 1, type: 'PLANNER_RESPONSE', tool_calls: [{ name: 'run_command' }] }
  assert.equal(stepsToItems([input, work, { ...step, step_index: 2 }]).turnError.turn_error_after_work, true)
  assert.equal(stepsToItems([input, step, { ...input, step_index: 2 }]).turnError, undefined)
})

test('ordinary rate limits and malformed metadata keep their native error without inventing a reset', () => {
  for (const details of [undefined, 'invalid JSON', JSON.stringify({ error: { details: {} } }),
    JSON.stringify({ error: { details: [{ reason: 'QUOTA_EXHAUSTED', metadata: { quotaResetTimeStamp: 'invalid' } }] } })]) {
    const step = nativeErrorStep({ ...error, errorMessage: { error: { userErrorMessage: 'Too many requests', errorCode: 429, details } } }, 0)
    assert.deepEqual(step.turn_error, { turn_error: 'Too many requests', turn_error_code: 'rate_limited' })
  }
  assert.equal(nativeErrorStep({ type: 'CORTEX_STEP_TYPE_USER_INPUT' }, 0), null)
  assert.equal(nativeErrorStep({ type: 'CORTEX_STEP_TYPE_ERROR_MESSAGE' }, 0), null)
  assert.equal(nativeErrorStep({ ...error, status: 'CORTEX_STEP_STATUS_CANCELLED' }, 0), null)
})

// The shape Antigravity records when the model service refuses a picture it cannot decode (a truncated JPEG).
const INVALID_MEDIA_TEXT = 'INVALID_ARGUMENT (code 400): Request contains an invalid argument.'
const invalidMedia = {
  type: 'CORTEX_STEP_TYPE_ERROR_MESSAGE', status: 'CORTEX_STEP_STATUS_DONE', metadata: { createdAt: AT },
  errorMessage: { error: {
    userErrorMessage: 'Agent execution terminated due to error.', modelErrorMessage: INVALID_MEDIA_TEXT, shortError: INVALID_MEDIA_TEXT,
    errorCode: 400, userErrorNumber: 2011,
    details: JSON.stringify({ error: { code: 400, message: 'Request contains an invalid argument.', status: 'INVALID_ARGUMENT', details: [
      { '@type': 'type.googleapis.com/google.rpc.ErrorInfo', reason: 'INVALID_MEDIA', domain: 'cloudcode-pa.googleapis.com', metadata: { error_number: '2011' } },
    ] } }),
  } },
}

test('a request the service refuses as it is names its cause and is the user\'s to fix', () => {
  const step = nativeErrorStep(invalidMedia, 3)
  assert.deepEqual(step.turn_error, { turn_error: `${INVALID_MEDIA_TEXT} (INVALID_MEDIA)`, turn_error_kind: 'user' })
  assert.equal(stepsToItems([input, step]).items.at(-1).text, `API Error: ${INVALID_MEDIA_TEXT} (INVALID_MEDIA)`)
  const generic = (errorCode, shortError) => nativeErrorStep({ ...invalidMedia, errorMessage: { error: {
    userErrorMessage: 'Agent execution terminated due to error.', shortError, errorCode } } }, 0).turn_error
  // Only the wording changes for failures worth a Continue, or the transient 401 the runtime retries.
  assert.deepEqual(generic(503, 'UNAVAILABLE (code 503): overloaded'), { turn_error: 'UNAVAILABLE (code 503): overloaded' })
  assert.deepEqual(generic(401, TRANSIENT_401), { turn_error: TRANSIENT_401 })
  assert.deepEqual(generic(408, 'DEADLINE_EXCEEDED (code 408)'), { turn_error: 'DEADLINE_EXCEEDED (code 408)' })
  assert.deepEqual(generic(400, undefined), { turn_error: 'Agent execution terminated due to error.', turn_error_kind: 'user' })
  // A specific message of the user's own stays as it is.
  assert.deepEqual(nativeErrorStep({ ...invalidMedia, errorMessage: { error: { userErrorMessage: 'Model not available', errorCode: 404 } } }, 0)
    .turn_error, { turn_error: 'Model not available', turn_error_kind: 'user' })
})

const INTERRUPTED_NOTE = 'The stream was interrupted. Please continue the task you were working on.'

test('stopping a turn drops agy\'s stream-interrupted note in both export and native error paths', () => {
  for (const errorBody of [
    { userErrorMessage: INTERRUPTED_NOTE },
    { modelErrorMessage: INTERRUPTED_NOTE },
    { shortError: INTERRUPTED_NOTE },
    { userErrorMessage: 'Agent execution terminated due to error.', shortError: INTERRUPTED_NOTE },
    { userErrorMessage: 'The stream was interrupted.' },
  ]) {
    assert.equal(nativeErrorStep({
      type: 'CORTEX_STEP_TYPE_ERROR_MESSAGE',
      status: 'CORTEX_STEP_STATUS_DONE',
      metadata: { createdAt: AT },
      errorMessage: { error: errorBody },
    }, 1), null)
  }

  // Real upstream errors that mention stream interruption keep their error card and turn_error.
  const realUpstream = nativeErrorStep({
    type: 'CORTEX_STEP_TYPE_ERROR_MESSAGE',
    status: 'CORTEX_STEP_STATUS_DONE',
    metadata: { createdAt: AT },
    errorMessage: { error: { userErrorMessage: 'The stream was interrupted: 503 unavailable', errorCode: 503 } },
  }, 1)
  assert.deepEqual(realUpstream?.turn_error, { turn_error: 'The stream was interrupted: 503 unavailable' })

  const interrupted = { step_index: 2, type: 'ERROR_MESSAGE', source: 'SYSTEM', status: 'DONE', created_at: AT, error: INTERRUPTED_NOTE }
  const reply = { step_index: 1, type: 'PLANNER_RESPONSE', source: 'MODEL', status: 'DONE', created_at: AT, content: 'Partial reply.' }
  const stopped = stepsToItems([input, reply, interrupted])
  assert.equal(stopped.toSeq, 3)
  assert.equal(stopped.turnError, undefined)
  assert.equal(stopped.lastText, 'Partial reply.')
  assert.deepEqual(stopped.items.map((item) => [item.role, item.kind, item.text]), [
    ['user', 'text', input.content],
    ['assistant', 'text', 'Partial reply.'],
  ])
  assert.deepEqual(transcriptState([input, reply, interrupted]), { status: 'idle' })

  const blank = stepsToItems([input, { ...reply, content: '\n', thinking: 'Thinking' }, interrupted])
  assert.equal(blank.turnError, undefined)
  assert.deepEqual(blank.items.map((item) => [item.kind, item.text]), [
    ['text', input.content], ['thinking', 'Thinking'], ['notice', '已中断'],
  ])

  // An interrupted note still resets pending tool calls and does not trigger a stray wake notice.
  const afterWake = stepsToItems([
    input,
    reply,
    { step_index: 2, type: 'SYSTEM_MESSAGE', created_at: AT, content: 'background update' },
    { ...interrupted, step_index: 3 },
  ])
  assert.equal(afterWake.items.some((item) => item.wake), false)
  assert.equal(afterWake.turnError, undefined)

  const interruptedTool = stepsToItems([
    input,
    { step_index: 1, type: 'PLANNER_RESPONSE', created_at: AT, tool_calls: [{ name: 'run_command', args: {} }] },
    interrupted,
    { step_index: 3, type: 'USER_INPUT', source: 'USER_EXPLICIT', status: 'DONE', created_at: AT, content: 'Continue' },
    { step_index: 4, type: 'PLANNER_RESPONSE', created_at: AT, tool_calls: [{ name: 'view_file', args: {} }] },
    { step_index: 5, type: 'GENERIC', status: 'DONE', created_at: AT, content: 'file body' },
  ])
  const uses = interruptedTool.items.filter((item) => item.kind === 'tool_use')
  const result = interruptedTool.items.find((item) => item.kind === 'tool_result')
  assert.equal(result.tool_use_id, uses[1].tool_use_id)
  assert.equal(interruptedTool.items.some((item) => item.text?.startsWith('API Error:')), false)
  assert.equal(interruptedTool.turnError, undefined)

  // Real exported errors stay visible.
  const realExported = stepsToItems([
    input,
    { step_index: 1, type: 'ERROR_MESSAGE', source: 'SYSTEM', status: 'DONE', created_at: AT, error: 'The stream was interrupted: 503 unavailable' },
  ])
  assert.equal(realExported.turnError?.turn_error, 'The stream was interrupted: 503 unavailable')
  assert.equal(realExported.items.at(-1).text, 'API Error: The stream was interrupted: 503 unavailable')
})

test('runtime ignores an omitted native stream-interrupted note without emitting turn_error', async (t) => {
  const { row, items } = await fixture(t, {
    nativeError: {
      type: 'CORTEX_STEP_TYPE_ERROR_MESSAGE',
      status: 'CORTEX_STEP_STATUS_DONE',
      metadata: { createdAt: AT },
      errorMessage: { error: { userErrorMessage: INTERRUPTED_NOTE } },
    },
  })
  const page = await items()
  assert.equal(page.items.some((item) => item.text?.startsWith('API Error:')), false)
  assert.equal(row().turn_error, undefined)
})

async function fixture(t, options = {}) {
  const home = mkdtempSync(join(tmpdir(), 'agy-turn-error-'))
  const config = join(home, 'config')
  const logs = join(config, 'brain', RAW, '.system_generated/logs')
  mkdirSync(logs, { recursive: true })
  const transcript = join(logs, 'transcript_full.jsonl')
  const write = (steps) => writeFileSync(transcript, steps.map((step) => JSON.stringify(step)).join('\n') + '\n')
  write([input])
  writeFileSync(join(home, 'calls.jsonl'), '')
  const nativeFile = join(home, 'trajectory.json')
  const snapshot = {
    lazyService: true,
    holdErrors: options.holdErrors,
    status: 'CASCADE_RUN_STATUS_IDLE',
    trajectory: { cascadeId: RAW, steps: [
      { type: 'CORTEX_STEP_TYPE_USER_INPUT', metadata: { source: 'CORTEX_STEP_SOURCE_USER_EXPLICIT' }, userInput: { items: [{ text: input.content }] } },
      ...Array.from({ length: options.padding || 0 }, () => ({ type: 'CORTEX_STEP_TYPE_SYSTEM_MESSAGE' })),
      ...(options.rpcError ? [] : [options.nativeError || error]),
    ] },
    promptError: options.rpcError,
    promptPlan: options.promptPlan,
    stepsRequireLoad: options.stepsRequireLoad,
    stepsUnavailable: options.stepsUnavailable,
    stepsTimeout: options.stepsTimeout,
    stepsNotFoundUnlessLoaded: options.stepsNotFoundUnlessLoaded,
    stepsNotFoundAlways: options.stepsNotFoundAlways,
    resumeError: options.resumeError,
  }
  writeFileSync(nativeFile, JSON.stringify(snapshot))
  const db = new DatabaseSync(join(config, 'conversation_summaries.db'))
  db.exec('CREATE TABLE conversation_summaries (conversation_id TEXT PRIMARY KEY, status TEXT, not_fully_idle INTEGER, killed INTEGER, step_count INTEGER, title TEXT, workspace_uris TEXT, last_modified_time TEXT)')
  db.prepare('INSERT INTO conversation_summaries VALUES (?, ?, 0, 0, ?, ?, ?, ?)')
    .run(RAW, 'CASCADE_RUN_STATUS_IDLE', options.initialCount ?? snapshot.trajectory.steps.length, 'Error example', JSON.stringify([`file://${home}`]), new Date().toISOString())
  const state = (count) => db.prepare('UPDATE conversation_summaries SET step_count = ?, last_modified_time = ? WHERE conversation_id = ?').run(count, new Date().toISOString(), RAW)
  const binary = join(home, 'agy')
  writeFileSync(binary, `#!/bin/sh\nexec '${process.execPath}' '${FAKE}' '${home}' "$@"\n`)
  chmodSync(binary, 0o700)
  const node = await startFakeNode(t)
  const runtimeOptions = { home, agyDir: config, agyBin: binary, personadBin: binary, socketPath: node.path, log: () => {},
    transient401RetryDelayMs: options.retryDelay ?? 10, exportPollMs: 20, nativeErrorRetryMs: options.nativeErrorRetryMs }
  let runtime = createAgyRuntime(runtimeOptions)
  t.after(async () => { await runtime.stop(); db.close(); rmSync(home, { recursive: true, force: true }) })
  runtime.start()
  await node.ready
  const row = () => node.last('runtime.sessions')?.sessions.find((entry) => entry.session_id === SESSION)
  await until('session listed', row)
  const calls = () => readFileSync(join(home, 'calls.jsonl'), 'utf8').split('\n').filter(Boolean).map(JSON.parse)
  const items = () => node.peer.call('session.items', { session_id: SESSION })
  const refresh = async () => {
    const before = node.all('runtime.sessions').length
    await node.peer.call('session.rename', { session_id: SESSION, title: 'Refreshed' })
    await until('inventory refreshed', () => node.all('runtime.sessions').length > before)
  }
  const restart = async () => {
    await runtime.stop()
    const before = node.all('runtime.sessions').length
    runtime = createAgyRuntime(runtimeOptions)
    runtime.start()
    await until('restarted inventory', () => node.all('runtime.sessions').length > before)
  }
  return { node, row, calls, items, write, state, refresh, restart, transcript, nativeFile, idle: () => runtime.idle() }
}

for (const exported of [false, true]) {
  test(`retries one transient 401 ${exported ? 'recorded in the transcript' : 'returned by ACP'} without duplicating the user message`, async (t) => {
    const { node, row, calls, items, idle } = await fixture(t, { initialCount: 1, promptPlan: [
      { error: TRANSIENT_401, export: exported, undatedStep: exported }, { reply: 'Finished.', export: exported },
    ] })
    const send = (text, client_msg_id) => node.peer.call('session.send', { session_id: SESSION, text, client_msg_id })
    await send('Continue.', 'request-1')
    await until('retry succeeds', () => row()?.last_text === 'Finished.' && idle())
    assert.equal(calls().filter((call) => call.method === 'session/prompt').length, 2)
    assert.equal(row().turn_error, undefined)
    const page = await items()
    const users = page.items.filter((item) => item.role === 'user')
    assert.deepEqual(users.map((item) => item.text), [input.content, 'Continue.'])
    assert.equal(users.at(-1).client_msg_id, 'request-1')
    assert.equal(page.items.some((item) => item.text?.includes('UNAUTHENTICATED')), false)
    if (exported) {
      await send('Continue.', 'request-2')
      await until('next send settles', idle)
      assert.equal((await items()).items.filter((item) => item.text === 'Continue.').length, 2)
    }
  })
}

test('an empty Antigravity completion retries the same prompt once and displays one user message', async (t) => {
  const { node, calls, items, row, idle } = await fixture(t, { initialCount: 1, promptPlan: [
    { export: true, thinking: 'Thinking', reply: '\n' },
    { export: true, reply: 'Here is the answer.' },
  ] })
  await node.peer.call('session.send', { session_id: SESSION, text: 'Please answer.', client_msg_id: 'empty-retry' })
  await until('retried answer published', idle)
  const prompts = calls().filter((call) => call.method === 'session/prompt')
  assert.equal(prompts.length, 2)
  assert.deepEqual(prompts[1].params, prompts[0].params)
  const page = await items()
  const messages = page.items.filter((item) => item.kind === 'text')
  assert.deepEqual(messages.map((item) => item.text), [input.content, 'Please answer.', 'Here is the answer.'])
  assert.equal(messages[1].client_msg_id, 'empty-retry')
  assert.equal(row().turn_error, undefined)
})

test('a second empty completion publishes a retryable turn error', async (t) => {
  const { node, row, calls, items, idle } = await fixture(t, { initialCount: 1, promptPlan: [
    { export: true, thinking: 'Thinking', reply: '\n' },
    { export: true, thinking: 'Still thinking', reply: '  \t' },
    { export: true, reply: 'Next answer.' },
  ] })
  await node.peer.call('session.send', { session_id: SESSION, text: 'Please answer.' })
  await until('empty-response failure published', () => idle() && row()?.turn_error)
  assert.equal(calls().filter((call) => call.method === 'session/prompt').length, 2)
  assert.match(row().turn_error, /no response returned.*try again/i)
  assert.equal(row().turn_error_after_work, false)
  const page = (await items()).items
  assert.equal(page.filter((item) => item.text === 'Please answer.').length, 1)
  const notice = page.find((item) => item.kind === 'notice' && /no response returned.*try again/i.test(item.text))
  assert.ok(notice)

  await node.peer.call('session.send', { session_id: SESSION, text: 'Next request.' })
  await until('successor settled', () => idle() && row()?.last_text === 'Next answer.')
  assert.equal(row().turn_error, undefined)
  assert.ok((await items()).items.some((item) => item.id === notice.id && item.text === notice.text))
})

test('a blank cancelled turn is recorded before its queued successor and does not retry', async (t) => {
  const { node, row, calls, items, idle } = await fixture(t, { initialCount: 1, promptPlan: [
    { export: true, reply: '\n', thinking: 'Thinking', holdForCancel: true },
    { export: true, reply: 'New answer.' },
  ] })
  await node.peer.call('session.send', { session_id: SESSION, text: 'Interrupted request.' })
  await until('first prompt sent', () => calls().some((call) => call.method === 'session/prompt'))
  assert.equal((await node.peer.call('session.send', { session_id: SESSION, text: 'New request.' })).queued, true)
  await node.peer.call('session.queue.send_now', { session_id: SESSION })
  await until('successor settled', idle)
  const methods = calls().filter((call) => ['session/prompt', 'session/cancel'].includes(call.method))
  assert.deepEqual(methods.map((call) => call.method), ['session/prompt', 'session/cancel', 'session/prompt'])
  assert.equal(row().turn_error, undefined)
  const page = await items()
  const interrupted = page.items.findIndex((item) => item.kind === 'notice' && item.text === '已中断')
  assert.ok(interrupted > page.items.findIndex((item) => item.text === 'Interrupted request.'))
  assert.ok(interrupted < page.items.findIndex((item) => item.text === 'New request.'))
  assert.equal(page.items.filter((item) => item.kind !== 'turn_duration').at(-1).text, 'New answer.')
  assert.equal(page.items.at(-1).kind, 'turn_duration')
})

test('a blank ACP cancelled result never starts a retry', async (t) => {
  const { node, row, calls, items, idle } = await fixture(t, { initialCount: 1, promptPlan: [
    { export: true, thinking: 'Thinking', reply: '\n', stopReason: 'cancelled' },
  ] })
  await node.peer.call('session.send', { session_id: SESSION, text: 'Please answer.' })
  await until('cancelled turn settled', idle)
  assert.equal(calls().filter((call) => call.method === 'session/prompt').length, 1)
  assert.equal(row().turn_error, undefined)
  assert.equal((await items()).items.filter((item) => item.kind === 'notice' && item.text === '已中断').length, 1)
})

for (const [name, plan] of [
  ['an exported silent supervision finish', {
    reply: '\n', calls: [{ name: 'mcp_persona_supervision_finish', args: { visibility: 'silent' } }],
  }],
  ['an answer in the export without a live text update', { reply: 'Already answered.', exportOnly: true }],
]) {
  test(`does not retry ${name}`, async (t) => {
    const { node, row, calls, idle } = await fixture(t, { initialCount: 1, promptPlan: [{ ...plan, export: true }] })
    await node.peer.call('session.send', { session_id: SESSION, text: 'Please check.' })
    await until('turn settled', idle)
    assert.equal(calls().filter((call) => call.method === 'session/prompt').length, 1)
    assert.equal(row().turn_error, undefined)
  })
}

test('a plan-mode 401 retries despite a previous reply flushing and whole-second export timestamps', async (t) => {
  const prior = { prompt: 'Earlier request.', reply: 'Earlier reply.' }
  const { node, calls, items, row, idle } = await fixture(t, { initialCount: 1, promptPlan: [
    { reply: prior.reply },
    { error: TRANSIENT_401, export: true, planMode: true, wholeSeconds: true, flushReply: prior },
    { reply: 'Finished.', export: true, planMode: true, wholeSeconds: true },
  ] })
  await node.peer.call('session.send', { session_id: SESSION, text: prior.prompt })
  await until('previous turn settles without native export', () => row()?.last_text === prior.reply && idle())
  await node.peer.call('session.send', { session_id: SESSION, text: 'Continue.', client_msg_id: 'plan-retry' })
  await until('plan retry settles', () => row()?.last_text === 'Finished.' && idle())
  assert.equal(calls().filter((call) => call.method === 'session/prompt').length, 3)
  const page = await items()
  const visible = page.items.filter((item) => item.kind !== 'turn_duration')
  assert.deepEqual(visible.map((item) => item.text), [input.content, prior.prompt, prior.reply, '/plan Continue.', 'Finished.'])
  assert.equal(visible[3].client_msg_id, 'plan-retry')
  assert.equal(page.items.at(-1).kind, 'turn_duration')
})

for (const [reason, plan, attempts, retryDelay] of [
  ['revoked login', [{ error: `${TRANSIENT_401}; invalid_grant` }], 1],
  ['non-adjacent expired refresh token', [{ error: `${TRANSIENT_401}; refresh_token for this account has expired` }], 1],
  ['revoked account credential', [{ error: `${TRANSIENT_401}; revoked the account login credential` }], 1],
  ['explicit sign-in request', [{ error: `${TRANSIENT_401} Please sign in.` }], 1],
  ['assistant text', [{ error: TRANSIENT_401, reply: 'Partial answer' }], 1],
  ['tool work', [{ error: TRANSIENT_401, tool: true }], 1],
  ['tool work received during the delay', [{ error: TRANSIENT_401, lateTool: 30 }], 1, 250],
  ['a second transient failure', [{ error: TRANSIENT_401 }, { error: TRANSIENT_401 }], 2],
]) {
  test(`does not retry after ${reason}`, async (t) => {
    const { node, row, calls } = await fixture(t, { initialCount: 1, promptPlan: plan, retryDelay })
    await node.peer.call('session.send', { session_id: SESSION, text: 'Continue.' })
    await until('failure published', () => row()?.turn_error?.startsWith(TRANSIENT_401))
    assert.equal(calls().filter((call) => call.method === 'session/prompt').length, attempts)
  })
}

test('interrupt aborts the transient 401 delay and settles the turn', async (t) => {
  const { node, row, calls, idle } = await fixture(t, { initialCount: 1, retryDelay: 60_000,
    promptPlan: [{ error: TRANSIENT_401 }, { reply: 'Next.' }] })
  await node.peer.call('session.send', { session_id: SESSION, text: 'Continue.' })
  await until('first prompt sent', () => calls().some((call) => call.method === 'session/prompt'))
  assert.equal((await node.peer.call('session.interrupt', { session_id: SESSION })).settled, true)
  assert.equal(idle(), true)
  assert.equal(calls().filter((call) => call.method === 'session/prompt').length, 1)
  await node.peer.call('session.send', { session_id: SESSION, text: 'Try again.' })
  await until('next send completes', () => row()?.last_text === 'Next.')
})

for (const assistant of [false, true]) {
  test(`assistant sends use assistant MCP after ${assistant ? 'assistant' : 'ordinary'} modes`, async (t) => {
    const { node, calls } = await fixture(t, { initialCount: 1 })
    const modes = (flags = {}) => node.peer.call('session.modes', { session_id: SESSION, permission_mode: 'default', ...flags })
    const resumes = () => calls().filter((call) => call.method === 'session/resume').map((call) => call.params.mcpServers[0])
    const assistantMcp = (server) => server.env.some(({ name, value }) => name === 'PERSONA_ASSISTANT' && value === '1')
    await modes({ assistant })
    await modes()
    assert.deepEqual(resumes().map(assistantMcp), [assistant])
    await node.peer.call('session.send', { session_id: SESSION, text: 'Continue.', assistant: true })
    await modes()
    assert.deepEqual(resumes().map(assistantMcp), assistant ? [true] : [false, true])
    const loads = calls().filter((call) => call.method === 'session/resume')
    if (!assistant) {
      assert.notEqual(loads[0].worker, loads[1].worker)
      const closed = calls().find((call) => call.method === 'session/close')
      assert.equal(closed?.worker, loads[0].worker)
    }
    assert.match(resumes().at(-1).args[0], /mcp-proxy\.mjs$/)
  })
}

test('runtime recovers an omitted native error through a cached bounded tail without resuming the session', async (t) => {
  const { row, calls, items, refresh, restart, transcript } = await fixture(t)
  const original = readFileSync(transcript)
  const page = await items()
  assert.match(page.history, /:full:api-errors(?:$|:)/)
  assert.equal(page.to_seq, 2)
  assert.equal(page.items.at(-1).text, `API Error: ${MESSAGE}`)
  await until('quota fields published', () => row()?.turn_error_kind === 'usage_limit')
  assert.equal(row().turn_error_resets_at, '2026-01-03T18:00:00.000Z')
  await Promise.all([items(), items(), items()])
  await refresh()
  assert.equal(calls().filter((call) => call.method === 'GetCascadeTrajectorySteps').length, 1)
  assert.deepEqual(calls().find((call) => call.method === 'GetCascadeTrajectorySteps').params, { cascadeId: RAW, stepOffset: 1, disableRehydration: true })
  assert.equal(calls().filter((call) => call.method === 'session/list').length, 1)
  assert.equal(calls().some((call) => ['session/resume', 'session/prompt'].includes(call.method)), false)
  assert.deepEqual(readFileSync(transcript), original)
  await restart()
  assert.equal((await items()).items.at(-1).text, `API Error: ${MESSAGE}`)
  await until('quota recovered after restart', () => row()?.turn_error === MESSAGE)
})

test('an omitted invalid-media failure reaches the session row as the user\'s to fix, not a Continue', async (t) => {
  const { row, items, calls } = await fixture(t, { nativeError: invalidMedia })
  assert.equal((await items()).items.at(-1).text, `API Error: ${INVALID_MEDIA_TEXT} (INVALID_MEDIA)`)
  await until('invalid media published', () => row()?.turn_error_kind === 'user')
  assert.equal(row().turn_error, `${INVALID_MEDIA_TEXT} (INVALID_MEDIA)`)
  assert.equal(row().turn_error_code, undefined)
  assert.equal(calls().some((call) => ['session/resume', 'session/prompt'].includes(call.method)), false)
})

test('new user input and successful response clear the old native failure', async (t) => {
  const { row, items, write, state, refresh } = await fixture(t)
  await items()
  await until('initial error', () => row()?.turn_error === MESSAGE)
  write([input, { ...input, step_index: 2, created_at: '2026-01-02T12:01:00Z', content: 'Continue.' },
    { step_index: 3, type: 'PLANNER_RESPONSE', status: 'DONE', created_at: '2026-01-02T12:01:01Z', content: 'Finished.' }])
  state(4)
  await refresh()
  assert.equal(row().turn_error, undefined)
  assert.equal(row().last_text, 'Finished.')
  assert.equal((await items()).items.some((item) => item.kind === 'notice'), false)
})

test('native recovery reads only the final eight steps of a large export gap', async (t) => {
  const { calls, items } = await fixture(t, { padding: 20 })
  const page = await items()
  assert.equal(page.to_seq, 22)
  assert.equal(page.items.at(-1).text, `API Error: ${MESSAGE}`)
  assert.equal(calls().find((call) => call.method === 'GetCascadeTrajectorySteps').params.stepOffset, 14)
})

test('omitted errors of a conversation this process never loaded return at their own positions', async (t) => {
  const { row, calls, items, write, state, nativeFile } = await fixture(t, { stepsRequireLoad: true })
  const again = { ...input, step_index: 2, created_at: '2026-01-02T12:01:00Z', content: 'Try again.' }
  write([input, again])
  const snapshot = JSON.parse(readFileSync(nativeFile, 'utf8'))
  const [nativeInput] = snapshot.trajectory.steps
  snapshot.trajectory.steps = [nativeInput, error, nativeInput, error]
  writeFileSync(nativeFile, JSON.stringify(snapshot))
  state(4)
  const page = await items()
  assert.deepEqual(page.items.map((item) => [item.seq, item.text]),
    [[1, input.content], [2, `API Error: ${MESSAGE}`], [3, again.content], [4, `API Error: ${MESSAGE}`]])
  assert.match(page.history, /:native-errors:1$/)
  await until('last error published', () => row()?.turn_error === MESSAGE)
  const reads = calls().filter((call) => call.method === 'GetCascadeTrajectorySteps')
  assert.deepEqual(reads.map((call) => call.params.stepOffset), [1, 1])
  assert.equal(calls().filter((call) => call.method === 'session/resume').length, 1)
  await items()
  assert.equal(calls().filter((call) => call.method === 'GetCascadeTrajectorySteps').length, 2)
})

test('a failed native read shows a turn error and is retried at most twice', async (t) => {
  const { row, calls, items } = await fixture(t, { stepsUnavailable: true, nativeErrorRetryMs: 0 })
  const failure = 'Persona could not read this turn\'s result from Antigravity.'
  const page = await items()
  assert.deepEqual(page.items.map((item) => [item.seq, item.text]), [[1, input.content], [2, `API Error: ${failure}`]])
  await until('failure published', () => row()?.turn_error === failure)
  for (let i = 0; i < 4; i++) await items()
  assert.equal(calls().filter((call) => call.method === 'GetCascadeTrajectorySteps').length, 3)
})

for (const recovered of [true, false]) {
  test(`a native read that fails and then ${recovered ? 'finds the error replaces' : 'finds no error drops'} the failure under a new history`, async (t) => {
    const { row, items, nativeFile } = await fixture(t, { stepsUnavailable: true, nativeErrorRetryMs: 0,
      nativeError: recovered ? error : { type: 'CORTEX_STEP_TYPE_SYSTEM_MESSAGE' } })
    const failure = 'Persona could not read this turn\'s result from Antigravity.'
    const failed = await items()
    assert.equal(failed.items.at(-1).text, `API Error: ${failure}`)
    await until('failure published', () => row()?.turn_error === failure)
    const snapshot = JSON.parse(readFileSync(nativeFile, 'utf8'))
    delete snapshot.stepsUnavailable
    writeFileSync(nativeFile, JSON.stringify(snapshot))
    const page = await items()
    assert.notEqual(page.history, failed.history)
    assert.deepEqual(page.items.map((item) => [item.seq, item.text]),
      recovered ? [[1, input.content], [2, `API Error: ${MESSAGE}`]] : [[1, input.content]])
    await until('row updated', () => row()?.turn_error === (recovered ? MESSAGE : undefined))
  })
}

test('rewinding a failed turn removes its recovered error', async (t) => {
  const { node, row, items } = await fixture(t)
  await items()
  await until('initial error', () => row()?.turn_error === MESSAGE)
  await node.peer.call('session.rewind', { session_id: SESSION, prompt: input.content, occurrence: 1, prompts_after: 0, restore: 'conversation' })
  assert.deepEqual((await items()).items, [])
  await until('rewound inventory', () => row()?.turn_error === undefined)
})

test('a rejected ACP prompt publishes its failure through the normal session row', async (t) => {
  const { node, row, write, state, refresh } = await fixture(t, { rpcError: 'Upstream connection closed' })
  await node.peer.call('session.send', { session_id: SESSION, text: 'Continue.' })
  await until('RPC failure published', () => row()?.turn_error === 'Upstream connection closed')
  assert.notEqual(row().status, 'working')
  write([input, { ...input, step_index: 1, created_at: '2026-01-02T12:01:00Z', content: 'Another request.' }])
  state(2)
  await refresh()
  assert.equal(row().turn_error, undefined)
})

const STREAMED = 'FAILED_PRECONDITION (code 400): The request is not supported here.'

test('only agy\'s one-line streamed error shape reads as an error', () => {
  assert.deepEqual(streamedErrorInfo(`\n⚠ ${STREAMED}\n`), { turn_error: STREAMED, turn_error_kind: 'user' })
  assert.deepEqual(streamedErrorInfo('⚠ RESOURCE_EXHAUSTED (code 429): Slow down.'),
    { turn_error: 'RESOURCE_EXHAUSTED (code 429): Slow down.', turn_error_code: 'rate_limited' })
  for (const text of [`Done.\n⚠ ${STREAMED}`, `⚠ ${STREAMED}\nMore text.`, '⚠ Careful: this deletes files.', STREAMED, '⚠ failed (code 400): x'])
    assert.equal(streamedErrorInfo(text), null, text)
})

test('an API error agy streams as text is the turn error, settles idle and stays in history', async (t) => {
  // agy exports the input but not the error, and leaves its summary behind the export (#14628).
  const { node, row, items, idle } = await fixture(t, { initialCount: 1, promptPlan: [
    { export: true, streamOnly: true, reply: `\n⚠ ${STREAMED}\n` },
    { export: true, reply: 'Next answer.' },
  ] })
  await node.peer.call('session.send', { session_id: SESSION, text: 'Please answer.' })
  await until('streamed error published', () => idle() && row()?.turn_error)
  assert.equal(row().turn_error, STREAMED)
  assert.equal(row().turn_error_kind, 'user')
  assert.equal(row().status, 'idle')
  const page = (await items()).items
  assert.equal(page.some((item) => item.role === 'assistant' && item.text?.includes('⚠')), false)
  const notice = page.find((item) => item.kind === 'notice' && item.text === `API Error: ${STREAMED}`)
  assert.ok(notice)

  await node.peer.call('session.send', { session_id: SESSION, text: 'Next request.' })
  await until('successor settled', () => idle() && row()?.last_text === 'Next answer.')
  assert.equal(row().turn_error, undefined)
  assert.ok((await items()).items.some((item) => item.id === notice.id && item.text === notice.text))
})

test('a reply that only mentions an error after other words stays a reply', async (t) => {
  const reply = `The last run said: ⚠ ${STREAMED}`
  const { node, row, idle } = await fixture(t, { initialCount: 1, promptPlan: [{ export: true, reply }] })
  await node.peer.call('session.send', { session_id: SESSION, text: 'What happened?' })
  await until('reply published', () => idle() && row()?.last_text === reply)
  assert.equal(row().turn_error, undefined)
})

test('an attached picture that is cut short is not sent; the words and complete pictures are', async (t) => {
  const { node, calls } = await fixture(t)
  const seg = (marker, body) => Buffer.concat([Buffer.from([0xff, marker, 0, body.length + 2]), body])
  const jpeg = Buffer.concat([Buffer.from([0xff, 0xd8]), seg(0xdb, Buffer.alloc(65)), seg(0xc0, Buffer.alloc(9)),
    seg(0xda, Buffer.alloc(8)), Buffer.from([0x12, 0x34, 0xff, 0xd9])])
  const whole = jpeg.toString('base64')
  const cut = jpeg.subarray(0, jpeg.length - 2).toString('base64')
  await node.peer.call('session.send', { session_id: SESSION, text: 'Look at these.', images: [
    { media_type: 'image/jpeg', data: whole }, { media_type: 'image/jpeg', data: cut }] })
  await until('prompt sent', () => calls().some((call) => call.method === 'session/prompt'))
  const blocks = calls().find((call) => call.method === 'session/prompt').params.prompt
  assert.deepEqual(blocks.filter((b) => b.type === 'image').map((b) => b.data), [whole])
  assert.match(blocks.filter((b) => b.type === 'text').map((b) => b.text).join('\n'), /Look at these\./)
  assert.match(blocks.at(-1).text, /1 attached picture was incomplete or damaged and not sent/)
})

test('listing idle sessions does not start native error probes', async (t) => {
  const { calls, refresh } = await fixture(t)
  await refresh()
  assert.equal(calls().filter((call) => call.method === 'GetCascadeTrajectorySteps').length, 0)
})

test('a pending native error probe does not hold a completed prompt active', async (t) => {
  const { node, row, calls, items, state, refresh, nativeFile } = await fixture(t, { holdErrors: true, initialCount: 1 })
  const release = () => {
    const snapshot = JSON.parse(readFileSync(nativeFile, 'utf8'))
    snapshot.holdErrors = false
    writeFileSync(nativeFile, JSON.stringify(snapshot))
  }
  state(2)
  await node.peer.call('session.send', { session_id: SESSION, text: 'Continue.' })
  await until('completion probes the native tail', () => calls().some((call) => call.method === 'GetCascadeTrajectorySteps'))
  try {
    await refresh()
    assert.notEqual(row().status, 'working', 'native error reads must not hold a completed prompt active')
  } finally {
    release()
  }
  await items()
})

test('prepareTranscript refreshes the exported transcript after awaiting a native error probe', async (t) => {
  const { calls, items, write, state, nativeFile } = await fixture(t, { holdErrors: true })
  const release = () => {
    const snapshot = JSON.parse(readFileSync(nativeFile, 'utf8'))
    snapshot.holdErrors = false
    writeFileSync(nativeFile, JSON.stringify(snapshot))
  }
  const pending = items()
  await until('native error probe started', () => calls().some((call) => call.method === 'GetCascadeTrajectorySteps'))
  try {
    write([
      input,
      { step_index: 1, type: 'PLANNER_RESPONSE', status: 'DONE', created_at: '2026-01-02T12:00:05Z', content: 'Fresh reply during error probe' },
    ])
    state(2)
  } finally {
    release()
  }
  const page = await pending
  assert.ok(
    page.items.some((item) => item.text === 'Fresh reply during error probe'),
    'session.items must include steps written while awaiting errFlight',
  )
})

test('native read reloads via session/resume when GetCascadeTrajectorySteps returns 404 not_found for an unloaded session', async (t) => {
  const { row, calls, items } = await fixture(t, { stepsNotFoundUnlessLoaded: true, nativeErrorRetryMs: 0 })
  const page = await items()
  assert.deepEqual(page.items.map((item) => [item.seq, item.text]), [[1, input.content], [2, `API Error: ${MESSAGE}`]])
  await until('recovered native error published', () => row()?.turn_error === MESSAGE)
  assert.equal(calls().filter((call) => call.method === 'session/resume').length, 1)
  assert.equal(calls().filter((call) => call.method === 'GetCascadeTrajectorySteps').length, 2)
})

test('permanent trajectory not_found after reload stops retrying immediately and omits API Error prefix', async (t) => {
  const { row, calls, items } = await fixture(t, { stepsNotFoundAlways: true, nativeErrorRetryMs: 0 })
  const failure = 'Antigravity no longer has this turn\'s history. Start a new conversation if you cannot continue.'
  const page = await items()
  assert.deepEqual(page.items.map((item) => [item.seq, item.text]), [[1, input.content], [2, failure]])
  await until('permanent failure published', () => row()?.turn_error === failure)
  assert.equal(row().turn_error_kind, 'user')
  for (let i = 0; i < 4; i++) await items()
  assert.equal(calls().filter((call) => call.method === 'session/resume').length, 1)
  assert.equal(calls().filter((call) => call.method === 'GetCascadeTrajectorySteps').length, 2)
})

test('unreadable session on reload during native read stops retrying immediately and omits API Error prefix', async (t) => {
  const { row, calls, items } = await fixture(t, {
    stepsNotFoundUnlessLoaded: true,
    resumeError: `Failed to read session ${RAW}; retry, or load it with session/load`,
    nativeErrorRetryMs: 0,
  })
  const failure = 'Antigravity no longer has this turn\'s history. Start a new conversation if you cannot continue.'
  const page = await items()
  assert.deepEqual(page.items.map((item) => [item.seq, item.text]), [[1, input.content], [2, failure]])
  await until('permanent reload failure published', () => row()?.turn_error === failure)
  assert.equal(row().turn_error_kind, 'user')
  for (let i = 0; i < 4; i++) await items()
  assert.equal(calls().filter((call) => call.method === 'session/resume').length, 1)
  assert.equal(calls().filter((call) => call.method === 'GetCascadeTrajectorySteps').length, 1)
})

test('local native read timeout uses a distinct non-API timeout message and still retries up to twice', async (t) => {
  const { row, calls, items, nativeFile } = await fixture(t, { stepsTimeout: true, nativeErrorRetryMs: 0 })
  const timeoutText = 'Timed out reading this turn\'s result from Antigravity.'
  const page = await items()
  assert.deepEqual(page.items.map((item) => [item.seq, item.text]), [[1, input.content], [2, timeoutText]])
  await until('timeout notice published', () => row()?.turn_error === timeoutText)
  assert.equal(row().turn_error_kind, undefined)
  // Second attempt still times out; third attempt recovers once timeout clears.
  await items()
  assert.equal(calls().filter((call) => call.method === 'GetCascadeTrajectorySteps').length, 2)
  const snapshot = JSON.parse(readFileSync(nativeFile, 'utf8'))
  delete snapshot.stepsTimeout
  writeFileSync(nativeFile, JSON.stringify(snapshot))
  const recovered = await items()
  assert.deepEqual(recovered.items.map((item) => [item.seq, item.text]), [[1, input.content], [2, `API Error: ${MESSAGE}`]])
  await until('recovered after timeout', () => row()?.turn_error === MESSAGE)
})
